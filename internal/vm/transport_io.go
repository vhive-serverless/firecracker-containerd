// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//	http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package vm

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"runtime"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	transportDefaultFrame = 32 << 10
	transportMaxFrame     = 1 << 20
	transportMaxIdentity  = 256
	transportHeaderSize   = 32
	transportMagic        = 0x46435431 // FCT1, version 1.
	transportHello        = 1
	transportHelloAck     = 2
	transportData         = 3
	transportAck          = 4
	transportFIN          = 5
	transportBarrier      = 6
	transportBarrierAck   = 7
)

var (
	// ErrTransportProtocol indicates incompatible identities, offsets or frames.
	ErrTransportProtocol = errors.New("invalid checkpoint IO transport frame")
	// ErrTransportBusy prevents overlapping connection generations on one pump.
	ErrTransportBusy = errors.New("checkpoint IO transport already has a connection")
	// ErrTransportResumed means Resume interrupted a Prepare operation.
	ErrTransportResumed = errors.New("checkpoint IO transport resumed during preparation")
)

// TransportCursor is a clean stream cut returned by Prepare. Offset counts bytes
// accepted by the destination FIFO, not bytes consumed by its application. FIN
// represents source EOF; connection loss never changes FIN. StreamID identifies
// a task/exec/stdio direction; SessionID identifies an execution branch and must
// match at both ends. A clone may replace SessionID on BOTH copies of a clean
// cut, then construct fresh endpoints with StartPaused. Never reset Offset or
// replay already acknowledged input when constructing a clone.
type TransportCursor struct {
	StreamID  string
	SessionID string
	Offset    uint64
	FIN       bool
}

// TransportOptions bounds memory per endpoint. FrameSize defaults to 32 KiB and
// may not exceed 1 MiB. The receiver must permit the sender's FrameSize.
type TransportOptions struct {
	FrameSize   int
	StartPaused bool
}

// TransportReadFunc must return promptly when ctx is cancelled without closing
// the source. A successful partial read is retained even if cancellation races
// it. Exactly one sender owns this reader for its lifetime, across connections.
type TransportReadFunc func(ctx context.Context, p []byte) (int, error)

// TransportWriteFunc reports the exact number of bytes accepted, including on
// error, and obeys ctx without closing the destination. Returning (n, err) with
// n > 0 commits those n bytes; a replacement connection must not rewrite them.
type TransportWriteFunc func(ctx context.Context, p []byte) (int, error)

// TransportStatus is diagnostic state. Only a successful Prepare returns a
// checkpointable sender cut; sampling Status is not a checkpoint barrier.
type TransportStatus struct {
	Cursor       TransportCursor
	PendingBytes int
	Reading      bool
	Paused       bool
	Prepared     bool
}

// TransportSender retains one pending frame and the source reader independently
// of network lifetime. It never closes the source. Call Close only at final
// process/stream teardown, not when Serve returns a network error.
type TransportSender struct {
	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
	read         TransportReadFunc
	frameSize    int
	cursor       TransportCursor
	pending      []byte
	eof          bool
	readErr      error
	reading      bool
	readCancel   context.CancelFunc
	paused       bool
	serial       uint64
	wantBarrier  uint64
	ackedBarrier uint64
	changed      chan struct{}
	serveGate    chan struct{}
	prepareGate  chan struct{}
	done         chan struct{}
	finished     chan struct{}
}

// NewTransportSender starts a single FIFO pump. The caller retains ownership of
// the local endpoint and must keep it open until Close and Done have completed.
func NewTransportSender(ctx context.Context, cursor TransportCursor, read TransportReadFunc, opts TransportOptions) (*TransportSender, error) {
	size, err := transportConfig(cursor, opts)
	if err != nil {
		return nil, err
	}
	if read == nil {
		return nil, errors.New("checkpoint IO reader is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &TransportSender{ctx: ctx, cancel: cancel, read: read, frameSize: size,
		cursor: cursor, eof: cursor.FIN, paused: opts.StartPaused, changed: make(chan struct{}),
		serveGate: make(chan struct{}, 1), prepareGate: make(chan struct{}, 1), done: make(chan struct{}), finished: make(chan struct{})}
	if cursor.FIN {
		close(s.finished)
	}
	go s.pump()
	return s, nil
}

func transportConfig(cursor TransportCursor, opts TransportOptions) (int, error) {
	if len(cursor.StreamID) == 0 || len(cursor.StreamID) > transportMaxIdentity || len(cursor.SessionID) == 0 || len(cursor.SessionID) > transportMaxIdentity {
		return 0, fmt.Errorf("stream and session identities must contain 1..%d bytes", transportMaxIdentity)
	}
	size := opts.FrameSize
	if size == 0 {
		size = transportDefaultFrame
	}
	if size < 1 || size > transportMaxFrame {
		return 0, fmt.Errorf("checkpoint IO frame size must be 1..%d", transportMaxFrame)
	}
	return size, nil
}

func (s *TransportSender) signal() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *TransportSender) pump() {
	defer close(s.done)
	for {
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			return
		}
		if s.paused || s.pending != nil || s.eof || s.readErr != nil {
			changed := s.changed
			s.mu.Unlock()
			select {
			case <-s.ctx.Done():
				return
			case <-changed:
				continue
			}
		}
		size := s.frameSize
		if remaining := math.MaxUint64 - s.cursor.Offset; remaining < uint64(size) {
			size = int(remaining)
		}
		if size == 0 {
			s.readErr = errors.New("checkpoint IO byte offset overflow")
			s.signal()
			s.mu.Unlock()
			continue
		}
		readCtx, cancel := context.WithCancel(s.ctx)
		s.reading, s.readCancel = true, cancel
		s.signal()
		s.mu.Unlock()

		buf := make([]byte, size)
		n, err := s.read(readCtx, buf)
		cancelled := readCtx.Err() != nil
		cancel()

		s.mu.Lock()
		s.reading, s.readCancel = false, nil
		if n < 0 || n > len(buf) {
			s.readErr = errors.New("invalid checkpoint IO read count")
		} else {
			if n > 0 {
				s.pending = buf[:n]
			}
			switch {
			case errors.Is(err, io.EOF):
				s.eof = true
			case err != nil && !cancelled:
				s.readErr = err
			case n == 0 && err == nil:
				s.readErr = io.ErrNoProgress
			}
		}
		s.signal()
		s.mu.Unlock()
	}
}

// Prepare gates new source reads, interrupts an idle read without closing its
// FD, drains already-read bytes/FIN, then round-trips an ordered barrier. On
// Each successful call requires a fresh barrier round trip, including after a
// previously prepared connection was reset. A failed call retries its outstanding
// barrier without changing the byte cut. On timeout the gate STAYS CLOSED and
// pending bytes remain owned by this pump;
// the coordinator must explicitly Resume to abort or retry Prepare. This does
// not freeze workloads or prepare other streams/control/events on its own.
func (s *TransportSender) Prepare(ctx context.Context) (TransportCursor, error) {
	select {
	case s.prepareGate <- struct{}{}:
		defer func() { <-s.prepareGate }()
	case <-ctx.Done():
		return TransportCursor{}, ctx.Err()
	case <-s.ctx.Done():
		return TransportCursor{}, s.ctx.Err()
	}
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return TransportCursor{}, err
	}
	s.paused = true
	if s.readCancel != nil {
		s.readCancel()
	}
	if s.wantBarrier == 0 || s.wantBarrier == s.ackedBarrier {
		if s.serial == math.MaxUint64 {
			s.mu.Unlock()
			return TransportCursor{}, errors.New("checkpoint IO barrier serial overflow")
		}
		s.serial++
		s.wantBarrier = s.serial
	}
	target := s.wantBarrier
	s.signal()
	for {
		if err := s.ctx.Err(); err != nil {
			s.mu.Unlock()
			return TransportCursor{}, err
		}
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return TransportCursor{}, err
		}
		if s.wantBarrier != target {
			s.mu.Unlock()
			return TransportCursor{}, ErrTransportResumed
		}
		if s.readErr != nil {
			err := s.readErr
			s.mu.Unlock()
			return TransportCursor{}, err
		}
		if s.ackedBarrier == target && !s.reading && s.pending == nil && (!s.eof || s.cursor.FIN) {
			cursor := s.cursor
			s.mu.Unlock()
			return cursor, nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return TransportCursor{}, ctx.Err()
		case <-s.ctx.Done():
			return TransportCursor{}, s.ctx.Err()
		case <-changed:
		}
		s.mu.Lock()
	}
}

// Resume releases a successful cut or aborts preparation. It never resets byte
// offsets, drops pending bytes or reopens a source after FIN. It is idempotent.
func (s *TransportSender) Resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused, s.wantBarrier = false, 0
	s.signal()
}

func (s *TransportSender) Status() TransportStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return TransportStatus{Cursor: s.cursor, PendingBytes: len(s.pending), Reading: s.reading,
		Paused: s.paused, Prepared: s.wantBarrier != 0 && s.ackedBarrier == s.wantBarrier}
}

// Close ends the pump and active Serve without closing the local FIFO.
func (s *TransportSender) Close() { s.cancel() }

// Done closes when the single local source reader has stopped.
func (s *TransportSender) Done() <-chan struct{} { return s.done }

// Finished closes when the peer has acknowledged a genuine source FIN.
func (s *TransportSender) Finished() <-chan struct{} { return s.finished }

// Serve owns conn, including on error. Only one Serve may run at a time; cancel
// and wait for the old call before attaching a replacement. Network errors leave
// the pump, cursor, pending frame, source FD and FIN state intact.
func (s *TransportSender) Serve(ctx context.Context, conn net.Conn) error {
	return s.serve(ctx, conn, nil)
}

func (s *TransportSender) serve(ctx context.Context, conn net.Conn, ready func()) error {
	ctx, cleanup, err := transportConnection(ctx, s.ctx, conn, s.serveGate)
	if err != nil {
		return err
	}
	defer cleanup()
	s.mu.Lock()
	cursor := s.cursor
	s.mu.Unlock()
	hello := transportFrame{kind: transportHello, offset: cursor.Offset, fin: cursor.FIN,
		serial: uint64(s.frameSize), payload: transportIdentity(cursor)}
	if err := writeTransportFrame(conn, hello); err != nil {
		return err
	}
	response, err := readTransportFrame(conn, s.frameSize)
	if err != nil {
		return err
	}
	if response.kind != transportHelloAck || response.serial < uint64(s.frameSize) {
		return ErrTransportProtocol
	}
	s.mu.Lock()
	end := s.cursor.Offset + uint64(len(s.pending))
	valid := response.offset >= s.cursor.Offset && response.offset <= end && (!s.cursor.FIN || response.fin) && (!response.fin || s.eof)
	s.mu.Unlock()
	if !valid {
		return ErrTransportProtocol
	}
	if ready != nil {
		ready()
	}
	for {
		frame, err := s.nextFrame(ctx)
		if err != nil {
			return err
		}
		if err := writeTransportFrame(conn, frame); err != nil {
			return err
		}
		ack, err := readTransportFrame(conn, s.frameSize)
		if err != nil {
			return err
		}
		kind := byte(transportAck)
		if frame.kind == transportBarrier {
			kind = transportBarrierAck
		}
		if ack.kind != kind || len(ack.payload) != 0 || ack.offset != frame.offset+uint64(len(frame.payload)) || ack.serial != frame.serial || ack.fin != (frame.fin || frame.kind == transportFIN) {
			return ErrTransportProtocol
		}
		s.mu.Lock()
		switch frame.kind {
		case transportData:
			s.cursor.Offset, s.pending = ack.offset, nil
		case transportFIN:
			if !s.cursor.FIN {
				s.cursor.FIN = true
				close(s.finished)
			}
		case transportBarrier:
			if s.wantBarrier == frame.serial {
				s.ackedBarrier = frame.serial
			}
		}
		s.signal()
		s.mu.Unlock()
	}
}

func (s *TransportSender) nextFrame(ctx context.Context) (transportFrame, error) {
	for {
		s.mu.Lock()
		frame := transportFrame{offset: s.cursor.Offset, fin: s.cursor.FIN}
		switch {
		case s.pending != nil:
			frame.kind, frame.payload = transportData, s.pending
		case s.eof && !s.cursor.FIN:
			frame.kind = transportFIN
		case s.readErr != nil:
			err := s.readErr
			s.mu.Unlock()
			return transportFrame{}, err
		case s.paused && !s.reading && s.wantBarrier != 0 && s.ackedBarrier != s.wantBarrier:
			frame.kind, frame.serial = transportBarrier, s.wantBarrier
		}
		changed := s.changed
		s.mu.Unlock()
		if frame.kind != 0 {
			return frame, nil
		}
		select {
		case <-ctx.Done():
			return transportFrame{}, ctx.Err()
		case <-changed:
		}
	}
}

// TransportReceiver retains accepted-byte offsets and the last frame's digest
// across connection loss, including partial destination writes. It never closes
// the destination. At most one bounded wire frame is resident during Serve.
type TransportReceiver struct {
	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	write     TransportWriteFunc
	frameSize int
	cursor    TransportCursor
	last      *transportReplay
	serveGate chan struct{}
	finished  chan struct{}
}

type transportReplay struct {
	offset uint64
	length uint64
	hash   [sha256.Size]byte
}

// NewTransportReceiver creates a destination ledger without starting network IO.
func NewTransportReceiver(ctx context.Context, cursor TransportCursor, write TransportWriteFunc, opts TransportOptions) (*TransportReceiver, error) {
	size, err := transportConfig(cursor, opts)
	if err != nil {
		return nil, err
	}
	if write == nil {
		return nil, errors.New("checkpoint IO writer is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &TransportReceiver{ctx: ctx, cancel: cancel, write: write, frameSize: size,
		cursor: cursor, serveGate: make(chan struct{}, 1), finished: make(chan struct{})}
	if cursor.FIN {
		close(r.finished)
	}
	return r, nil
}

// Cursor is diagnostic accepted state, not proof that the sender drained. Use
// the cursor returned by sender.Prepare for capturing/forking a clean cut.
func (r *TransportReceiver) Cursor() TransportCursor {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cursor
}

// Finished closes only after a genuine FIN (or a restored FIN cursor). The owner
// may use it to close its local stdin write endpoint. A reset does not close it.
func (r *TransportReceiver) Finished() <-chan struct{} { return r.finished }

// Close ends active Serve without closing the local FIFO.
func (r *TransportReceiver) Close() { r.cancel() }

// Serve owns conn. The caller must cancel and join the old generation before
// replacement; a blocked context-aware local write is cancelled without closing
// its FD. Replaying the same pending frame skips the accepted prefix.
func (r *TransportReceiver) Serve(ctx context.Context, conn net.Conn) error {
	return r.serve(ctx, conn, nil)
}

func (r *TransportReceiver) serve(ctx context.Context, conn net.Conn, ready func()) error {
	ctx, cleanup, err := transportConnection(ctx, r.ctx, conn, r.serveGate)
	if err != nil {
		return err
	}
	defer cleanup()
	hello, err := readTransportFrame(conn, r.frameSize)
	if err != nil {
		return err
	}
	r.mu.Lock()
	cursor := r.cursor
	valid := hello.kind == transportHello && string(hello.payload) == string(transportIdentity(cursor)) && hello.serial > 0 && hello.serial <= uint64(r.frameSize) && hello.offset <= cursor.Offset && (!hello.fin || cursor.FIN)
	if hello.offset != cursor.Offset {
		valid = valid && r.last != nil && hello.offset == r.last.offset
	}
	r.mu.Unlock()
	if !valid {
		return ErrTransportProtocol
	}
	if err := writeTransportFrame(conn, transportFrame{kind: transportHelloAck, offset: cursor.Offset, fin: cursor.FIN, serial: uint64(r.frameSize)}); err != nil {
		return err
	}
	if ready != nil {
		ready()
	}
	for {
		frame, err := readTransportFrame(conn, r.frameSize)
		if err != nil {
			return err
		}
		switch frame.kind {
		case transportData:
			if err := r.accept(ctx, frame); err != nil {
				return err
			}
		case transportFIN:
			r.mu.Lock()
			valid := frame.offset == r.cursor.Offset && (r.last == nil || r.cursor.Offset == r.last.offset+r.last.length)
			if valid && !r.cursor.FIN {
				r.cursor.FIN = true
				close(r.finished)
			}
			r.mu.Unlock()
			if !valid {
				return ErrTransportProtocol
			}
		case transportBarrier:
			r.mu.Lock()
			valid := frame.offset == r.cursor.Offset && frame.fin == r.cursor.FIN && frame.serial != 0 && (r.last == nil || r.cursor.Offset == r.last.offset+r.last.length)
			r.mu.Unlock()
			if !valid {
				return ErrTransportProtocol
			}
		default:
			return ErrTransportProtocol
		}
		cursor = r.Cursor()
		kind := byte(transportAck)
		if frame.kind == transportBarrier {
			kind = transportBarrierAck
		}
		if err := writeTransportFrame(conn, transportFrame{kind: kind, offset: cursor.Offset, fin: cursor.FIN, serial: frame.serial}); err != nil {
			return err
		}
	}
}

// RebindCursor changes only the branch identity at a clean, detached cut. Live
// reconnects do not call it. Both endpoints of a clone must receive the same new
// session identity before attaching their replacement connections.
func (s *TransportSender) RebindCursor(cursor TransportCursor) error {
	if _, err := transportConfig(cursor, TransportOptions{}); err != nil {
		return err
	}
	select {
	case s.serveGate <- struct{}{}:
		defer func() { <-s.serveGate }()
	default:
		return ErrTransportBusy
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.paused || s.reading || s.pending != nil || s.wantBarrier == 0 || s.wantBarrier != s.ackedBarrier || cursor.StreamID != s.cursor.StreamID || cursor.Offset != s.cursor.Offset || cursor.FIN != s.cursor.FIN {
		return ErrTransportProtocol
	}
	s.cursor.SessionID = cursor.SessionID
	return nil
}

// RebindCursor verifies the accepted cut before changing a detached receiver's
// session. The coordinator must have prepared the peer sender first.
func (r *TransportReceiver) RebindCursor(cursor TransportCursor) error {
	if _, err := transportConfig(cursor, TransportOptions{}); err != nil {
		return err
	}
	select {
	case r.serveGate <- struct{}{}:
		defer func() { <-r.serveGate }()
	default:
		return ErrTransportBusy
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cursor.StreamID != r.cursor.StreamID || cursor.Offset != r.cursor.Offset || cursor.FIN != r.cursor.FIN || (r.last != nil && r.cursor.Offset != r.last.offset+r.last.length) {
		return ErrTransportProtocol
	}
	r.cursor.SessionID, r.last = cursor.SessionID, nil
	return nil
}

func (r *TransportReceiver) accept(ctx context.Context, frame transportFrame) error {
	hash := sha256.Sum256(frame.payload)
	r.mu.Lock()
	end := frame.offset + uint64(len(frame.payload))
	if len(frame.payload) == 0 || end < frame.offset || frame.fin || r.cursor.FIN || frame.offset > r.cursor.Offset {
		r.mu.Unlock()
		return ErrTransportProtocol
	}
	if r.last != nil && (frame.offset < r.cursor.Offset || r.cursor.Offset < r.last.offset+r.last.length) {
		if frame.offset != r.last.offset || uint64(len(frame.payload)) != r.last.length || hash != r.last.hash || r.cursor.Offset > end {
			r.mu.Unlock()
			return ErrTransportProtocol
		}
	} else if frame.offset != r.cursor.Offset {
		r.mu.Unlock()
		return ErrTransportProtocol
	} else {
		r.last = &transportReplay{offset: frame.offset, length: uint64(len(frame.payload)), hash: hash}
	}
	accepted := r.cursor.Offset - frame.offset
	r.mu.Unlock()
	for accepted < uint64(len(frame.payload)) {
		n, err := r.write(ctx, frame.payload[accepted:])
		if n < 0 || n > len(frame.payload)-int(accepted) {
			return errors.New("invalid checkpoint IO write count")
		}
		r.mu.Lock()
		r.cursor.Offset += uint64(n)
		r.mu.Unlock()
		accepted += uint64(n)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

type transportFrame struct {
	kind    byte
	fin     bool
	offset  uint64
	serial  uint64
	payload []byte
}

func transportIdentity(cursor TransportCursor) []byte {
	p := make([]byte, 4+len(cursor.StreamID)+len(cursor.SessionID))
	binary.BigEndian.PutUint16(p, uint16(len(cursor.StreamID)))
	binary.BigEndian.PutUint16(p[2:], uint16(len(cursor.SessionID)))
	copy(p[4:], cursor.StreamID)
	copy(p[4+len(cursor.StreamID):], cursor.SessionID)
	return p
}

func writeTransportFrame(w io.Writer, frame transportFrame) error {
	var header [transportHeaderSize]byte
	binary.BigEndian.PutUint32(header[:], transportMagic)
	header[4] = frame.kind
	if frame.fin {
		header[5] = 1
	}
	binary.BigEndian.PutUint64(header[8:], frame.offset)
	binary.BigEndian.PutUint64(header[16:], frame.serial)
	binary.BigEndian.PutUint32(header[24:], uint32(len(frame.payload)))
	if err := transportWriteAll(w, header[:]); err != nil {
		return err
	}
	return transportWriteAll(w, frame.payload)
}

func transportWriteAll(w io.Writer, p []byte) error {
	for len(p) != 0 {
		n, err := w.Write(p)
		if n < 0 || n > len(p) {
			return errors.New("invalid checkpoint IO network write count")
		}
		p = p[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

func readTransportFrame(r io.Reader, maxFrame int) (transportFrame, error) {
	var header [transportHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return transportFrame{}, err
	}
	if binary.BigEndian.Uint32(header[:]) != transportMagic || header[5] > 1 || binary.BigEndian.Uint16(header[6:]) != 0 || binary.BigEndian.Uint32(header[28:]) != 0 {
		return transportFrame{}, ErrTransportProtocol
	}
	frame := transportFrame{kind: header[4], fin: header[5] == 1, offset: binary.BigEndian.Uint64(header[8:]), serial: binary.BigEndian.Uint64(header[16:])}
	size := binary.BigEndian.Uint32(header[24:])
	limit := maxFrame
	if frame.kind == transportHello {
		limit = 4 + 2*transportMaxIdentity
	}
	if size > uint32(limit) || (frame.kind != transportData && frame.kind != transportHello && size != 0) {
		return transportFrame{}, ErrTransportProtocol
	}
	frame.payload = make([]byte, int(size))
	_, err := io.ReadFull(r, frame.payload)
	return frame, err
}

func transportConnection(ctx, lifetime context.Context, conn net.Conn, gate chan struct{}) (context.Context, func(), error) {
	if conn == nil {
		return nil, nil, errors.New("checkpoint IO connection is required")
	}
	select {
	case gate <- struct{}{}:
	default:
		_ = conn.Close()
		return nil, nil, ErrTransportBusy
	}
	merged, cancel := context.WithCancel(lifetime)
	stop := context.AfterFunc(ctx, cancel)
	if ctx.Err() != nil {
		cancel()
	}
	stopClose := context.AfterFunc(merged, func() { _ = conn.Close() })
	return merged, func() {
		stop()
		cancel()
		stopClose()
		_ = conn.Close()
		<-gate
	}, nil
}

// TransportFIFO adapts an already-open, exclusively owned FIFO/pipe descriptor
// to context-aware pump operations. Construction enables O_NONBLOCK; callers
// must not share this open file description with blocking-IO users. No method
// closes the file or treats temporary backpressure as EOF. FIFO startup/opening
// policy belongs to the caller: read with no writers is a genuine FIFO EOF.
type TransportFIFO struct {
	file *os.File // Retain the file so its finalizer cannot close fd.
	fd   int
}

// NewTransportFIFO enables cancellable nonblocking IO on a retained pipe/FIFO.
func NewTransportFIFO(file *os.File) (*TransportFIFO, error) {
	if file == nil {
		return nil, errors.New("checkpoint IO FIFO is required")
	}
	fd := int(file.Fd())
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFIFO {
		return nil, errors.New("checkpoint IO endpoint must be a pipe or FIFO")
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, err
	}
	return &TransportFIFO{file: file, fd: fd}, nil
}

// Read waits for bytes or real EOF without closing the descriptor on cancellation.
func (f *TransportFIFO) Read(ctx context.Context, p []byte) (int, error) {
	return f.io(ctx, p, false)
}

// Write waits for FIFO capacity without closing the descriptor on cancellation.
func (f *TransportFIFO) Write(ctx context.Context, p []byte) (int, error) {
	return f.io(ctx, p, true)
}

func (f *TransportFIFO) io(ctx context.Context, p []byte, write bool) (int, error) {
	defer runtime.KeepAlive(f.file)
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		var n int
		var err error
		if write {
			n, err = unix.Write(f.fd, p)
		} else {
			n, err = unix.Read(f.fd, p)
		}
		if err == unix.EINTR {
			continue
		}
		if err != unix.EAGAIN && err != unix.EWOULDBLOCK {
			if n < 0 {
				n = 0
			}
			if !write && n == 0 && err == nil {
				err = io.EOF
			}
			return n, err
		}
		events := int16(unix.POLLIN)
		if write {
			events = unix.POLLOUT
		}
		fds := []unix.PollFd{{Fd: int32(f.fd), Events: events}}
		// A short finite poll bounds cancellation without closing a retained FD.
		if _, err := unix.Poll(fds, 20); err != nil && err != unix.EINTR {
			return 0, err
		}
		if fds[0].Revents&unix.POLLNVAL != 0 {
			return 0, unix.EBADF
		}
	}
}
