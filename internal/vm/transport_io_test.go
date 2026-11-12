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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type transportTestBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *transportTestBuffer) write(_ context.Context, p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *transportTestBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.b.Bytes()...)
}

type transportTestInput struct {
	chunks chan []byte
	active atomic.Int32
	max    atomic.Int32
	calls  atomic.Int32
}

func newTransportTestInput() *transportTestInput {
	return &transportTestInput{chunks: make(chan []byte, 16)}
}

func (in *transportTestInput) read(ctx context.Context, p []byte) (int, error) {
	active := in.active.Add(1)
	defer in.active.Add(-1)
	in.calls.Add(1)
	for old := in.max.Load(); active > old; old = in.max.Load() {
		if in.max.CompareAndSwap(old, active) {
			break
		}
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case data, ok := <-in.chunks:
		if !ok {
			return 0, io.EOF
		}
		if len(data) > len(p) {
			return 0, errors.New("test chunk too large")
		}
		return copy(p, data), nil
	}
}

func transportTestCursor() TransportCursor {
	return TransportCursor{StreamID: "task/exec/stdout", SessionID: "source-session"}
}

func transportTestPair(t *testing.T, cursor TransportCursor, read TransportReadFunc, write TransportWriteFunc, opts TransportOptions) (*TransportSender, *TransportReceiver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	s, err := NewTransportSender(ctx, cursor, read, opts)
	require.NoError(t, err)
	r, err := NewTransportReceiver(ctx, cursor, write, opts)
	require.NoError(t, err)
	t.Cleanup(func() {
		s.Close()
		r.Close()
		select {
		case <-s.Done():
		case <-time.After(time.Second):
			t.Error("source pump did not stop")
		}
	})
	return s, r
}

type transportTestGeneration struct {
	cancel context.CancelFunc
	sender chan error
	recv   chan error
}

func startTransportGeneration(t *testing.T, s *TransportSender, r *TransportReceiver, wrapSender, wrapReceiver func(net.Conn) net.Conn) *transportTestGeneration {
	t.Helper()
	left, right := net.Pipe()
	if wrapSender != nil {
		left = wrapSender(left)
	}
	if wrapReceiver != nil {
		right = wrapReceiver(right)
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &transportTestGeneration{cancel: cancel, sender: make(chan error, 1), recv: make(chan error, 1)}
	go func() { g.sender <- s.Serve(ctx, left) }()
	go func() { g.recv <- r.Serve(ctx, right) }()
	t.Cleanup(cancel)
	return g
}

func (g *transportTestGeneration) stop(t *testing.T) {
	t.Helper()
	g.cancel()
	for _, ch := range []chan error{g.sender, g.recv} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("connection generation did not stop")
		}
	}
}

func transportEventually(t *testing.T, predicate func() bool) {
	t.Helper()
	require.Eventually(t, predicate, 2*time.Second, time.Millisecond)
}

// A frame header is emitted in one Write by writeTransportFrame. Fail after a
// chosen prefix of that header or its payload; this exercises actual wire IO.
type transportFaultConn struct {
	net.Conn
	kind      byte
	header    int
	payload   int
	inPayload bool
	fired     atomic.Bool
}

func (c *transportFaultConn) Write(p []byte) (int, error) {
	if !c.fired.Load() && len(p) == transportHeaderSize && binary.BigEndian.Uint32(p) == transportMagic && p[4] == c.kind {
		if c.header >= 0 {
			c.fired.Store(true)
			n, err := c.Conn.Write(p[:c.header])
			_ = c.Close()
			if err != nil {
				return n, err
			}
			return n, io.ErrClosedPipe
		}
		c.inPayload = true
		return c.Conn.Write(p)
	}
	if c.inPayload && !c.fired.Load() {
		c.fired.Store(true)
		n, err := c.Conn.Write(p[:c.payload])
		_ = c.Close()
		if err != nil {
			return n, err
		}
		return n, io.ErrClosedPipe
	}
	return c.Conn.Write(p)
}

func TestTransportReconnectRetainsFramesAndFIN(t *testing.T) {
	for _, fault := range []struct {
		name    string
		sender  bool
		kind    byte
		header  int
		payload int
	}{
		{name: "data-before-header", sender: true, kind: transportData},
		{name: "data-partial-header", sender: true, kind: transportData, header: 17},
		{name: "data-partial-payload", sender: true, kind: transportData, header: -1, payload: 3},
		{name: "data-lost-ack", kind: transportAck},
		{name: "data-partial-ack", kind: transportAck, header: 19},
		{name: "fin-lost-ack", kind: transportAck},
	} {
		t.Run(fault.name, func(t *testing.T) {
			input := newTransportTestInput()
			payload := []byte("bytes that must survive a broken connection exactly once")
			input.chunks <- payload
			close(input.chunks)
			var output transportTestBuffer
			s, r := transportTestPair(t, transportTestCursor(), input.read, output.write, TransportOptions{})
			var injected *transportFaultConn
			wrap := func(conn net.Conn) net.Conn {
				injected = &transportFaultConn{Conn: conn, kind: fault.kind, header: fault.header, payload: fault.payload}
				if fault.name == "fin-lost-ack" {
					return &transportFINAckFault{Conn: conn}
				}
				return injected
			}
			var g *transportTestGeneration
			if fault.sender {
				g = startTransportGeneration(t, s, r, wrap, nil)
			} else {
				g = startTransportGeneration(t, s, r, nil, wrap)
			}
			if fault.name == "fin-lost-ack" {
				transportEventually(t, func() bool { return r.Cursor().FIN })
			} else {
				transportEventually(t, injected.fired.Load)
			}
			g.stop(t)
			if fault.name != "fin-lost-ack" {
				require.False(t, s.Status().Cursor.FIN, "reset must not manufacture EOF")
			}
			g = startTransportGeneration(t, s, r, nil, nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			cut, err := s.Prepare(ctx)
			require.NoError(t, err)
			// Prepare cancels an idle read; if EOF had not yet been observed,
			// allow the real EOF read and prepare again.
			if !cut.FIN {
				s.Resume()
				transportEventually(t, func() bool { return s.Status().Cursor.FIN })
				cut, err = s.Prepare(ctx)
				require.NoError(t, err)
			}
			require.True(t, cut.FIN)
			require.Equal(t, uint64(len(payload)), cut.Offset)
			require.Equal(t, cut, r.Cursor())
			require.Equal(t, payload, output.bytes())
			require.Equal(t, int32(1), input.max.Load(), "replacement must retain exactly one source reader")
			g.stop(t)
		})
	}
}

type transportFINAckFault struct{ net.Conn }

func (c *transportFINAckFault) Write(p []byte) (int, error) {
	if len(p) == transportHeaderSize && p[4] == transportAck && p[5] == 1 {
		_ = c.Close()
		return 0, io.ErrClosedPipe
	}
	return c.Conn.Write(p)
}

func TestTransportPartialDestinationWriteAndChangedReplay(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "identical-replay", true: "changed-replay-rejected"}[changed], func(t *testing.T) {
			var output transportTestBuffer
			var partial atomic.Bool
			partial.Store(true)
			r, err := NewTransportReceiver(context.Background(), transportTestCursor(), func(ctx context.Context, p []byte) (int, error) {
				if partial.Swap(false) {
					n, _ := output.write(ctx, p[:4])
					return n, io.ErrUnexpectedEOF
				}
				return output.write(ctx, p)
			}, TransportOptions{})
			require.NoError(t, err)
			t.Cleanup(r.Close)
			payload := []byte("0123456789")
			for attempt := 0; attempt < 2; attempt++ {
				client, server := net.Pipe()
				_ = client.SetDeadline(time.Now().Add(time.Second))
				done := make(chan error, 1)
				go func() { done <- r.Serve(context.Background(), server) }()
				require.NoError(t, writeTransportFrame(client, transportFrame{kind: transportHello, serial: transportDefaultFrame, payload: transportIdentity(transportTestCursor())}))
				_, err := readTransportFrame(client, transportDefaultFrame)
				require.NoError(t, err)
				if changed && attempt == 1 {
					payload[0] = 'x'
				}
				require.NoError(t, writeTransportFrame(client, transportFrame{kind: transportData, payload: payload}))
				if attempt == 0 || changed {
					_, err := readTransportFrame(client, transportDefaultFrame)
					require.Error(t, err)
					serveErr := <-done
					if attempt == 0 {
						require.ErrorIs(t, serveErr, io.ErrUnexpectedEOF)
					} else {
						require.ErrorIs(t, serveErr, ErrTransportProtocol)
					}
					require.Equal(t, uint64(4), r.Cursor().Offset)
				} else {
					ack, err := readTransportFrame(client, transportDefaultFrame)
					require.NoError(t, err)
					require.Equal(t, uint64(len(payload)), ack.offset)
					_ = client.Close()
					<-done
					require.Equal(t, payload, output.bytes())
				}
				_ = client.Close()
			}
			if changed {
				require.Equal(t, []byte("0123"), output.bytes())
			}
		})
	}
}

func TestTransportPrepareBackpressureAbortAndReadBoundary(t *testing.T) {
	input := newTransportTestInput()
	first := bytes.Repeat([]byte("a"), 128)
	second := bytes.Repeat([]byte("b"), 128)
	input.chunks <- first
	input.chunks <- second
	close(input.chunks)
	blocked := make(chan struct{})
	entered := make(chan struct{})
	var enterOnce sync.Once
	var output transportTestBuffer
	s, r := transportTestPair(t, transportTestCursor(), input.read, func(ctx context.Context, p []byte) (int, error) {
		enterOnce.Do(func() { close(entered) })
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-blocked:
			return output.write(ctx, p)
		}
	}, TransportOptions{FrameSize: 128})
	g := startTransportGeneration(t, s, r, nil, nil)
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, err := s.Prepare(ctx)
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, s.Status().Paused)
	require.False(t, s.Status().Prepared)
	require.Equal(t, len(first), s.Status().PendingBytes)
	require.Equal(t, int32(1), input.calls.Load(), "pending frame bounds source consumption")
	require.Empty(t, output.bytes())
	select {
	case <-r.Finished():
		t.Fatal("blocked/reset connection manufactured FIN")
	default:
	}
	s.Resume() // Explicit preparation abort; pending frame remains intact.
	close(blocked)
	transportEventually(t, func() bool { return s.Status().Cursor.FIN })
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cut, err := s.Prepare(ctx)
	require.NoError(t, err)
	require.Equal(t, append(first, second...), output.bytes())
	require.Equal(t, uint64(256), cut.Offset)
	require.Equal(t, int32(1), input.max.Load())
	g.stop(t)
}

func TestTransportPreparedCutForksWithoutReplayingSourceInput(t *testing.T) {
	input := newTransportTestInput()
	input.chunks <- []byte("accepted-before-cut")
	var sourceOutput transportTestBuffer
	s, r := transportTestPair(t, transportTestCursor(), input.read, sourceOutput.write, TransportOptions{})
	g := startTransportGeneration(t, s, r, nil, nil)
	transportEventually(t, func() bool { return s.Status().Cursor.Offset == uint64(len("accepted-before-cut")) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cut, err := s.Prepare(ctx)
	require.NoError(t, err)
	require.False(t, cut.FIN)
	require.False(t, s.Status().Reading, "prepare must interrupt idle source read")
	require.True(t, s.Status().Prepared)
	input.chunks <- []byte("source-only-unread-input")
	before := input.calls.Load()
	// Repeated Prepare is idempotent and leaves the external source input alone.
	repeated, err := s.Prepare(ctx)
	require.NoError(t, err)
	require.Equal(t, cut, repeated)
	require.Equal(t, before, input.calls.Load())
	g.stop(t)

	cloneInput := newTransportTestInput()
	cloneInput.chunks <- []byte("clone-new-input")
	close(cloneInput.chunks)
	var cloneOutput transportTestBuffer
	cloneCursor := cut
	cloneCursor.SessionID = "independent-clone-session"
	clone, cloneReceiver := transportTestPair(t, cloneCursor, cloneInput.read, cloneOutput.write, TransportOptions{StartPaused: true})
	cloneGeneration := startTransportGeneration(t, clone, cloneReceiver, nil, nil)
	cloneCut, err := clone.Prepare(ctx)
	require.NoError(t, err)
	require.Equal(t, cloneCursor, cloneCut)
	require.Empty(t, cloneOutput.bytes())
	require.Zero(t, cloneInput.calls.Load())
	clone.Resume()
	transportEventually(t, func() bool { return clone.Status().Cursor.FIN })
	require.Equal(t, []byte("clone-new-input"), cloneOutput.bytes())
	require.Equal(t, cut.Offset+uint64(len("clone-new-input")), cloneReceiver.Cursor().Offset)

	g = startTransportGeneration(t, s, r, nil, nil)
	s.Resume()
	close(input.chunks)
	transportEventually(t, func() bool { return s.Status().Cursor.FIN })
	require.Equal(t, []byte("accepted-before-cutsource-only-unread-input"), sourceOutput.bytes())
	require.Equal(t, int32(1), input.max.Load())
	g.stop(t)
	cloneGeneration.stop(t)
}

func TestTransportBarrierAckLossAndExplicitResume(t *testing.T) {
	input := newTransportTestInput() // Idle reader must remain open across reset.
	var output transportTestBuffer
	s, r := transportTestPair(t, transportTestCursor(), input.read, output.write, TransportOptions{})
	var injected *transportFaultConn
	g := startTransportGeneration(t, s, r, nil, func(conn net.Conn) net.Conn {
		injected = &transportFaultConn{Conn: conn, kind: transportBarrierAck}
		return injected
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, err := s.Prepare(ctx)
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, injected.fired.Load())
	require.False(t, s.Status().Prepared, "lost barrier ACK cannot certify a cut")
	g.stop(t)
	g = startTransportGeneration(t, s, r, nil, nil)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cut, err := s.Prepare(ctx)
	require.NoError(t, err)
	require.Equal(t, transportTestCursor(), cut)
	result := make(chan error, 1)
	s.Resume()
	g.stop(t)
	go func() { _, err := s.Prepare(ctx); result <- err }()
	transportEventually(t, func() bool { return s.Status().Paused })
	s.Resume()
	require.ErrorIs(t, <-result, ErrTransportResumed)
}

func TestTransportFIFODescriptorsSurviveNetworkAndIdlePrepare(t *testing.T) {
	inputReader, inputWriter, err := os.Pipe()
	require.NoError(t, err)
	outputReader, outputWriter, err := os.Pipe()
	require.NoError(t, err)
	for _, f := range []*os.File{inputReader, inputWriter, outputReader, outputWriter} {
		t.Cleanup(func() { _ = f.Close() })
	}
	input, err := NewTransportFIFO(inputReader)
	require.NoError(t, err)
	output, err := NewTransportFIFO(outputWriter)
	require.NoError(t, err)
	s, r := transportTestPair(t, transportTestCursor(), input.Read, output.Write, TransportOptions{FrameSize: 1024})
	g := startTransportGeneration(t, s, r, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cut, err := s.Prepare(ctx)
	require.NoError(t, err)
	require.False(t, cut.FIN, "an idle open FIFO is not EOF")
	g.stop(t)
	for _, f := range []*os.File{inputReader, outputWriter} {
		_, err := unix.FcntlInt(f.Fd(), unix.F_GETFD, 0)
		require.NoError(t, err, "network reset must retain local FIFO descriptors")
	}
	// File.Fd may change Go-managed descriptors to blocking mode. Reassert the
	// adapter contract after inspecting descriptors through os.File.Fd.
	require.NoError(t, unix.SetNonblock(input.fd, true))
	require.NoError(t, unix.SetNonblock(output.fd, true))
	got := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(outputReader); got <- data }()
	payload := bytes.Repeat([]byte{0, 1, 2, 3, 0xfe, 0xff}, 32<<10)
	written := make(chan error, 1)
	go func() {
		_, err := inputWriter.Write(payload)
		_ = inputWriter.Close()
		written <- err
	}()
	g = startTransportGeneration(t, s, r, nil, nil)
	s.Resume()
	transportEventually(t, func() bool { return s.Status().Cursor.FIN })
	require.NoError(t, <-written)
	cut, err = s.Prepare(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(len(payload)), cut.Offset)
	require.True(t, cut.FIN)
	require.NoError(t, outputWriter.Close()) // Owner applies genuine FIN.
	require.Equal(t, payload, <-got)
	g.stop(t)
}

func TestTransportRejectsIncompatibleConnectionsAndOversizedFrames(t *testing.T) {
	input := newTransportTestInput()
	var output transportTestBuffer
	s, r := transportTestPair(t, transportTestCursor(), input.read, output.write, TransportOptions{})
	g := startTransportGeneration(t, s, r, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := s.Prepare(ctx)
	require.NoError(t, err)
	left, right := net.Pipe()
	t.Cleanup(func() { _ = right.Close() })
	require.ErrorIs(t, s.Serve(ctx, left), ErrTransportBusy)
	g.stop(t)

	left, right = net.Pipe()
	_ = left.SetDeadline(time.Now().Add(time.Second))
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx, right) }()
	wrong := transportTestCursor()
	wrong.SessionID = "other-branch"
	require.NoError(t, writeTransportFrame(left, transportFrame{kind: transportHello, serial: transportDefaultFrame, payload: transportIdentity(wrong)}))
	_, err = readTransportFrame(left, transportDefaultFrame)
	require.Error(t, err)
	require.ErrorIs(t, <-done, ErrTransportProtocol)
	_ = left.Close()

	var header [transportHeaderSize]byte
	binary.BigEndian.PutUint32(header[:], transportMagic)
	header[4] = transportData
	binary.BigEndian.PutUint32(header[24:], transportMaxFrame+1)
	_, err = readTransportFrame(bytes.NewReader(header[:]), transportMaxFrame)
	require.ErrorIs(t, err, ErrTransportProtocol, "oversized allocation must be rejected before payload read")
}

func TestTransportNamedFIFOBackpressureAndPartialReplay(t *testing.T) {
	openFIFO := func(name string) (*os.File, *os.File) {
		path := filepath.Join(t.TempDir(), name)
		require.NoError(t, unix.Mkfifo(path, 0600))
		reader, err := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
		require.NoError(t, err)
		writer, err := os.OpenFile(path, os.O_WRONLY|unix.O_NONBLOCK, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
		return reader, writer
	}
	inputReader, inputWriter := openFIFO("stdin")
	outputReader, outputWriter := openFIFO("stdout")
	input, err := NewTransportFIFO(inputReader)
	require.NoError(t, err)
	output, err := NewTransportFIFO(outputWriter)
	require.NoError(t, err)
	capacity, err := unix.FcntlInt(uintptr(output.fd), unix.F_GETPIPE_SZ, 0)
	require.NoError(t, err)
	frameSize := capacity * 2
	payload := bytes.Repeat([]byte("partial FIFO bytes\x00\xff"), frameSize/10+1)
	var acceptedBytes atomic.Uint64
	blockedWrite := make(chan struct{})
	var blockedOnce sync.Once
	s, r := transportTestPair(t, transportTestCursor(), input.Read, func(ctx context.Context, p []byte) (int, error) {
		if acceptedBytes.Load() >= uint64(capacity) {
			blockedOnce.Do(func() { close(blockedWrite) })
		}
		n, err := output.Write(ctx, p)
		acceptedBytes.Add(uint64(n))
		return n, err
	}, TransportOptions{FrameSize: frameSize})
	written := make(chan error, 1)
	go func() {
		_, err := inputWriter.Write(payload)
		_ = inputWriter.Close()
		written <- err
	}()
	g := startTransportGeneration(t, s, r, nil, nil)
	select {
	case <-blockedWrite:
	case <-time.After(time.Second):
		t.Fatal("test did not reach a write blocked by a full FIFO")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	_, err = s.Prepare(ctx)
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	accepted := r.Cursor().Offset
	require.Less(t, accepted, uint64(len(payload)))
	require.LessOrEqual(t, s.Status().PendingBytes, frameSize)
	g.stop(t) // Cancels a blocked FIFO write, retaining its exact accepted prefix.
	require.Equal(t, accepted, r.Cursor().Offset)
	for _, fd := range []int{input.fd, output.fd} {
		_, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		require.NoError(t, err)
	}
	readOutput, err := NewTransportFIFO(outputReader)
	require.NoError(t, err)
	got := make(chan []byte, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var result []byte
		buf := make([]byte, 257)
		for {
			n, err := readOutput.Read(ctx, buf)
			result = append(result, buf[:n]...)
			if err != nil {
				got <- result
				return
			}
		}
	}()
	g = startTransportGeneration(t, s, r, nil, nil)
	s.Resume()
	transportEventually(t, func() bool { return s.Status().Cursor.FIN })
	require.NoError(t, <-written)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cut, err := s.Prepare(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(len(payload)), cut.Offset)
	require.True(t, cut.FIN)
	require.NoError(t, outputWriter.Close())
	require.Equal(t, payload, <-got, "partial accepted bytes must not be replayed into the FIFO twice")
	g.stop(t)
}

func TestTransportPreparePreservesReadBytesRacingCancellation(t *testing.T) {
	entered := make(chan struct{})
	payload := []byte("read completed while prepare cancelled its context")
	var output transportTestBuffer
	s, r := transportTestPair(t, transportTestCursor(), func(ctx context.Context, p []byte) (int, error) {
		close(entered)
		<-ctx.Done()
		return copy(p, payload), ctx.Err()
	}, output.write, TransportOptions{})
	<-entered
	g := startTransportGeneration(t, s, r, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cut, err := s.Prepare(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(len(payload)), cut.Offset)
	require.Equal(t, payload, output.bytes())
	require.False(t, cut.FIN)
	require.False(t, s.Status().Reading)
	g.stop(t)
}

func TestTransportPrepareRecertifiesPreviouslyPreparedConnection(t *testing.T) {
	for _, finished := range []bool{false, true} {
		name := "open-stream"
		if finished {
			name = "finished-stream"
		}
		t.Run(name, func(t *testing.T) {
			input := newTransportTestInput()
			input.chunks <- []byte("accepted before the previous barrier")
			if finished {
				close(input.chunks)
			}
			var output transportTestBuffer
			sender, receiver := transportTestPair(t, transportTestCursor(), input.read, output.write, TransportOptions{})
			generation := startTransportGeneration(t, sender, receiver, nil, nil)
			transportEventually(t, func() bool { return sender.Status().Cursor.Offset > 0 && (!finished || sender.Status().Cursor.FIN) })
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			first, err := sender.Prepare(ctx)
			cancel()
			require.NoError(t, err)
			require.Equal(t, finished, first.FIN)
			generation.stop(t)
			ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
			_, err = sender.Prepare(ctx)
			cancel()
			require.ErrorIs(t, err, context.DeadlineExceeded, "a cached ACK must not certify a reset connection")
			require.True(t, sender.Status().Paused)
			require.False(t, sender.Status().Prepared)
			require.False(t, sender.Status().Reading, "recertification must not reopen the source FIFO gate")
			generation = startTransportGeneration(t, sender, receiver, nil, nil)
			ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			second, err := sender.Prepare(ctx)
			cancel()
			require.NoError(t, err)
			require.Equal(t, first, second)
			require.Equal(t, []byte("accepted before the previous barrier"), output.bytes())
			generation.stop(t)
		})
	}
}
