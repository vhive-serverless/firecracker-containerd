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
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

// TransportPorts identifies the guest's per-process stdio listeners.
type TransportPorts struct{ Stdin, Stdout, Stderr uint32 }

// TransportStreamIO describes one retained process pump. Sender is relative to
// the side returning the registry; a host created from a guest registry reverses
// that role. Only describe after both sides' PrepareIO calls have completed.
type TransportStreamIO struct {
	Name   string
	Sender bool
	Cursor TransportCursor
}

// TransportProcessIO is the transport registry for an existing guest process.
type TransportProcessIO struct {
	TaskID  string
	ExecID  string
	Streams []TransportStreamIO
}

// CheckpointIOProxy preserves local FIFO ownership across connection generations.
// Both sides must prepare their senders before DescribeIO defines a shared cut.
// A failed PrepareIO retains pending bytes and read gates until ResumeIO.
type CheckpointIOProxy interface {
	IOProxy
	PrepareIO(context.Context) error
	DescribeIO() ([]TransportStreamIO, error)
	ResumeIO()
}

type transportPair struct {
	path    string
	sender  bool
	connect IOConnector
	cursor  *TransportCursor
}

type transportProxy struct {
	mu          sync.Mutex
	configs     map[string]*transportPair
	streams     map[string]*transportProxyStream
	configErr   error
	ctx         context.Context
	cancel      context.CancelFunc
	closed      bool
	started     bool
	ready       chan struct{}
	initErr     error
	initTimeout time.Duration
}

type transportProxyStream struct {
	errMu       sync.Mutex
	terminalErr error
	name        string
	config      *transportPair
	local       *transportLocalFIFO
	sender      *TransportSender
	receiver    *TransportReceiver
	ctx         context.Context
	cancel      context.CancelFunc
	netCancel   context.CancelFunc
	netDone     chan struct{}
	initialized chan error
	initOnce    sync.Once
	complete    chan struct{}
	logger      *logrus.Entry
}

// A receiver may establish its network handshake before a real FIFO reader
// exists. DATA is acknowledged only after get/write succeeds. Opening is
// cancellable per network generation; an already-open descriptor is retained.
type transportLocalFIFO struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	fifo   *TransportFIFO
	closed bool
}

func (f *transportLocalFIFO) get(ctx context.Context) (*TransportFIFO, error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil, io.ErrClosedPipe
	}
	if f.fifo != nil {
		fifo := f.fifo
		f.mu.Unlock()
		return fifo, nil
	}
	f.mu.Unlock()
	file, err := openTransportFIFO(ctx, f.path, false)
	if err != nil {
		return nil, err
	}
	fifo, err := NewTransportFIFO(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		_ = file.Close()
		return nil, io.ErrClosedPipe
	}
	if f.fifo != nil {
		_ = file.Close()
		return f.fifo, nil
	}
	f.file, f.fifo = file, fifo
	return fifo, nil
}

func (f *transportLocalFIFO) write(ctx context.Context, p []byte) (int, error) {
	fifo, err := f.get(ctx)
	if err != nil {
		return 0, err
	}
	return fifo.Write(ctx, p)
}

func (f *transportLocalFIFO) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	if f.file != nil {
		_ = f.file.Close()
	}
}

func (f *transportLocalFIFO) finish(ctx context.Context) {
	// A real open-then-close releases a later blocking reader and yields EOF.
	// No dummy read end or bytes are introduced to force initialization.
	defer f.close()
	_, _ = f.get(ctx)
}

func newTransportProxy(stdin, stdout, stderr *IOConnectorPair) *transportProxy {
	p := &transportProxy{configs: make(map[string]*transportPair), streams: make(map[string]*transportProxyStream), ready: make(chan struct{})}
	for name, pair := range map[string]*IOConnectorPair{"stdin": stdin, "stdout": stdout, "stderr": stderr} {
		if pair == nil {
			continue
		}
		if pair.transport == nil {
			p.configErr = errors.New("cannot mix raw and checkpoint-framed IO connectors")
			continue
		}
		config := *pair.transport
		p.configs[name] = &config
	}
	return p
}

// NewTransportHostIOProxy constructs the host half of framed stdio. Saved
// descriptors come from the GUEST registry. A clone must pass the captured byte
// offsets/FIN with the new session also installed on its guest using AttachIO.
// Source reconnection instead rebinds the existing proxy without replacing it.
func NewTransportHostIOProxy(timeout time.Duration, udsPath string, ports TransportPorts, stdin, stdout, stderr string, saved []TransportStreamIO) IOProxy {
	pair := func(path string, port uint32, sender bool) *IOConnectorPair {
		if path == "" {
			return nil
		}
		return &IOConnectorPair{transport: &transportPair{path: path, sender: sender, connect: VSockDialConnector(timeout, udsPath, port)}}
	}
	p := newTransportProxy(pair(stdin, ports.Stdin, true), pair(stdout, ports.Stdout, false), pair(stderr, ports.Stderr, false))
	p.initTimeout = timeout
	if err := p.setCursors(saved, true); err != nil {
		p.configErr = err
	}
	return p
}

// SetTransportCursors supplies GUEST-orientation cursors to an unstarted guest
// proxy used as AttachIO's rebind configuration. It never consumes FIFO bytes.
func SetTransportCursors(proxy IOProxy, saved []TransportStreamIO) error {
	p, ok := proxy.(*transportProxy)
	if !ok {
		if len(saved) == 0 {
			return nil
		}
		return errors.New("cursor registry requires framed IO proxy")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return errors.New("cannot replace cursors on a started IO proxy")
	}
	return p.setCursors(saved, false)
}

func (p *transportProxy) setCursors(saved []TransportStreamIO, reverse bool) error {
	if len(saved) == 0 {
		return nil
	}
	if len(saved) != len(p.configs) {
		return errors.New("captured stream set differs from IO endpoints")
	}
	seen := make(map[string]bool)
	for _, stream := range saved {
		cfg := p.configs[stream.Name]
		sender := stream.Sender
		if reverse {
			sender = !sender
		}
		if cfg == nil || cfg.sender != sender || seen[stream.Name] {
			return errors.New("captured stream identity or direction differs from IO endpoints")
		}
		cursor := stream.Cursor
		if _, err := transportConfig(cursor, TransportOptions{}); err != nil {
			return err
		}
		cfg.cursor = &cursor
		seen[stream.Name] = true
	}
	return nil
}

func ensureTransportFIFO(path string) error {
	if err := unix.Mkfifo(path, 0600); err != nil && err != unix.EEXIST {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return fmt.Errorf("IO endpoint %q is not a FIFO", path)
	}
	return nil
}

func openTransportFIFO(ctx context.Context, path string, sender bool) (*os.File, error) {
	flag := os.O_WRONLY | unix.O_NONBLOCK
	if sender {
		flag = os.O_RDONLY | unix.O_NONBLOCK
	}
	for {
		file, err := os.OpenFile(path, flag, 0)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, unix.ENXIO) {
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// An initially unconnected named FIFO reads as zero bytes. Wait for actual data
// or HUP before the first syscall so initialization cannot manufacture FIN before
// runc/the external writer has opened the peer. After first data, ordinary FIFO
// EOF rules apply. HUP appears once a writer has connected and closed.
func transportInitialFIFORead(f *TransportFIFO) TransportReadFunc {
	first := true
	return func(ctx context.Context, p []byte) (int, error) {
		for first {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			fds := []unix.PollFd{{Fd: int32(f.fd), Events: unix.POLLIN}}
			if _, err := unix.Poll(fds, 20); err != nil && err != unix.EINTR {
				return 0, err
			}
			if fds[0].Revents&unix.POLLNVAL != 0 {
				return 0, unix.EBADF
			}
			if fds[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
				first = false
			}
		}
		return f.Read(ctx, p)
	}
}

func (p *transportProxy) start(proc *vmProc) (<-chan error, <-chan error) {
	initDone := make(chan error, 1)
	copyDone := make(chan error, 1)
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		initDone <- errors.New("framed IO proxy already started")
		close(initDone)
		close(copyDone)
		return initDone, copyDone
	}
	p.started = true
	p.ctx, p.cancel = context.WithCancel(context.Background())
	if p.closed {
		p.cancel()
	}
	// Paths must exist synchronously before the runc Create/Exec call.
	for _, cfg := range p.configs {
		if err := ensureTransportFIFO(cfg.path); err != nil {
			p.configErr = err
			break
		}
	}
	p.mu.Unlock()
	go func() {
		defer close(initDone)
		timeout := p.initTimeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		initCtx, cancelInit := context.WithTimeout(p.ctx, timeout)
		defer cancelInit()
		p.mu.Lock()
		err := p.configErr
		p.mu.Unlock()
		if err == nil {
			err = p.initialize(initCtx, proc)
		}
		p.mu.Lock()
		p.initErr = err
		close(p.ready)
		p.mu.Unlock()
		if err != nil {
			initDone <- err
			p.Close()
			copyDone <- err
			close(copyDone)
			return
		}
		p.mu.Lock()
		streams := make([]*transportProxyStream, 0, len(p.streams))
		for _, stream := range p.streams {
			streams = append(streams, stream)
		}
		p.mu.Unlock()
		for _, stream := range streams {
			select {
			case err = <-stream.initialized:
				if err != nil {
					initDone <- err
					p.Close()
					copyDone <- err
					close(copyDone)
					return
				}
			case <-initCtx.Done():
				initDone <- initCtx.Err()
				copyDone <- initCtx.Err()
				p.Close()
				close(copyDone)
				return
			}
		}
		initDone <- nil
		go func() {
			defer close(copyDone)
			for _, stream := range streams {
				if stream.name == "stdin" {
					select {
					case <-stream.complete:
					case <-proc.ctx.Done(): // A dead process cannot consume more input.
					case <-p.ctx.Done():
						copyDone <- p.ctx.Err()
						return
					}
				} else {
					select {
					case <-stream.complete:
					case <-p.ctx.Done():
						copyDone <- p.ctx.Err()
						return
					}
				}
			}
			copyDone <- nil
		}()
	}()
	return initDone, copyDone
}

func (p *transportProxy) initialize(initCtx context.Context, proc *vmProc) error {
	for name, cfg := range p.configs {
		local := &transportLocalFIFO{path: cfg.path}
		var fifo *TransportFIFO
		var err error
		if cfg.sender {
			file, openErr := openTransportFIFO(initCtx, cfg.path, true)
			if openErr != nil {
				return openErr
			}
			fifo, err = NewTransportFIFO(file)
			if err != nil {
				_ = file.Close()
				return err
			}
			local.file, local.fifo = file, fifo
		}
		ctx, cancel := context.WithCancel(p.ctx)
		stream := &transportProxyStream{name: name, config: cfg, local: local, ctx: ctx, cancel: cancel, initialized: make(chan error, 1), complete: make(chan struct{}), logger: proc.logger}
		cursor := TransportCursor{StreamID: fmt.Sprintf("%s/%s/%s", proc.taskID, proc.execID, name), SessionID: "initial"}
		opts := TransportOptions{}
		if cfg.cursor != nil {
			if cfg.cursor.StreamID != cursor.StreamID {
				cancel()
				local.close()
				return errors.New("captured stream ID differs from process identity")
			}
			cursor = *cfg.cursor
			opts.StartPaused = true
		}
		var finished <-chan struct{}
		if cfg.sender {
			stream.sender, err = NewTransportSender(ctx, cursor, transportInitialFIFORead(fifo), opts)
			if err == nil {
				finished = stream.sender.Finished()
			}
		} else {
			stream.receiver, err = NewTransportReceiver(ctx, cursor, local.write, opts)
			if err == nil {
				finished = stream.receiver.Finished()
			}
		}
		if err != nil {
			cancel()
			local.close()
			return err
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			cancel()
			local.close()
			return context.Canceled
		}
		p.streams[name] = stream
		p.mu.Unlock()
		stream.startNetwork()
		go func() {
			select {
			case <-finished:
				// FIN closes the real FIFO; network replay service remains live
				// until final proxy Close, including for a lost FIN ACK.
				close(stream.complete)
				if stream.receiver != nil {
					local.finish(ctx)
				} else {
					local.close()
				}
			case <-ctx.Done():
				local.close()
			}
		}()
	}
	return nil
}

func (s *transportProxyStream) startNetwork() {
	s.errMu.Lock()
	s.terminalErr = nil
	s.errMu.Unlock()
	ctx, cancel := context.WithCancel(s.ctx)
	s.netCancel = cancel
	s.netDone = make(chan struct{})
	go func() {
		defer close(s.netDone)
		for ctx.Err() == nil {
			result := <-s.config.connect(ctx, s.logger)
			err := result.Err
			if err == nil {
				conn, ok := result.ReadWriteCloser.(net.Conn)
				if !ok {
					if result.ReadWriteCloser != nil {
						_ = result.Close()
					}
					err = errors.New("framed IO connector must return net.Conn")
				} else {
					ready := func() { s.initOnce.Do(func() { s.initialized <- nil }) }
					if s.sender != nil {
						err = s.sender.serve(ctx, conn, ready)
					} else {
						err = s.receiver.serve(ctx, conn, ready)
					}
				}
			}
			if ctx.Err() != nil {
				break
			}
			if errors.Is(err, ErrTransportProtocol) {
				s.errMu.Lock()
				s.terminalErr = err
				s.errMu.Unlock()
				s.logger.WithError(err).Error("framed IO protocol failed; retaining FIFO state")
				s.initOnce.Do(func() { s.initialized <- err })
				return
			}
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
		s.initOnce.Do(func() { s.initialized <- ctx.Err() })
	}()
}

func (p *transportProxy) waitReady(ctx context.Context) error {
	p.mu.Lock()
	started := p.started
	p.mu.Unlock()
	if !started {
		return errors.New("framed IO proxy has not started")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ready:
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.initErr
}

func (p *transportProxy) PrepareIO(ctx context.Context) error {
	if err := p.waitReady(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	streams := make([]*transportProxyStream, 0, len(p.streams))
	for _, stream := range p.streams {
		stream.errMu.Lock()
		err := stream.terminalErr
		stream.errMu.Unlock()
		if err != nil {
			p.mu.Unlock()
			return err
		}
		if stream.sender != nil {
			streams = append(streams, stream)
		}
	}
	p.mu.Unlock()
	errs := make(chan error, len(streams))
	for _, stream := range streams {
		go func() {
			_, err := stream.sender.Prepare(ctx)
			if err != nil {
				err = fmt.Errorf("prepare stream %s: %w", stream.sender.Status().Cursor.StreamID, err)
			}
			errs <- err
		}()
	}
	var combined error
	for range streams {
		combined = errors.Join(combined, <-errs)
	}
	return combined
}

func (p *transportProxy) DescribeIO() ([]TransportStreamIO, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return nil, errors.New("framed IO proxy has not started")
	}
	select {
	case <-p.ready:
	default:
		return nil, errors.New("framed IO proxy is not initialized")
	}
	if p.initErr != nil {
		return nil, p.initErr
	}
	streams := make([]TransportStreamIO, 0, len(p.streams))
	for name, stream := range p.streams {
		stream.errMu.Lock()
		err := stream.terminalErr
		stream.errMu.Unlock()
		if err != nil {
			return nil, err
		}
		var cursor TransportCursor
		if stream.sender != nil {
			state := stream.sender.Status()
			if !state.Prepared {
				return nil, fmt.Errorf("stream %s has no prepared byte cut", name)
			}
			cursor = state.Cursor
		} else {
			cursor = stream.receiver.Cursor()
		}
		streams = append(streams, TransportStreamIO{Name: name, Sender: stream.sender != nil, Cursor: cursor})
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].Name < streams[j].Name })
	return streams, nil
}

func (p *transportProxy) ResumeIO() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, stream := range p.streams {
		if stream.sender != nil {
			stream.sender.Resume()
		}
	}
}

func (p *transportProxy) rebind(ctx context.Context, replacement *transportProxy) error {
	if err := p.waitReady(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("framed IO proxy is closed")
	}
	if replacement.configErr != nil {
		return replacement.configErr
	}
	if len(replacement.configs) != len(p.streams) {
		return errors.New("cannot change live stream set")
	}
	for name, stream := range p.streams {
		cfg := replacement.configs[name]
		if cfg == nil || cfg.path != stream.config.path || cfg.sender != stream.config.sender {
			return errors.New("cannot replace live FIFO or stream direction")
		}
	}
	// Stop all old network users before rebinding any cursor. FIFO pumps live.
	for _, stream := range p.streams {
		stream.netCancel()
	}
	for _, stream := range p.streams {
		select {
		case <-stream.netDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for name, stream := range p.streams {
		cfg := replacement.configs[name]
		if cfg.cursor != nil {
			var err error
			if stream.sender != nil {
				err = stream.sender.RebindCursor(*cfg.cursor)
			} else {
				err = stream.receiver.RebindCursor(*cfg.cursor)
			}
			if err != nil {
				return err
			}
		}
		stream.config = cfg
	}
	for _, stream := range p.streams {
		stream.startNetwork()
	}
	return nil
}

func (p *transportProxy) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.cancel != nil {
		p.cancel()
	}
}

func (p *transportProxy) IsOpen() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closed
}
