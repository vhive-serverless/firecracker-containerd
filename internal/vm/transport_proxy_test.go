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
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type transportTestLink struct {
	left  chan net.Conn
	right chan net.Conn
}

func newTransportTestLink() *transportTestLink {
	return &transportTestLink{left: make(chan net.Conn, 1), right: make(chan net.Conn, 1)}
}

func (l *transportTestLink) connect(ch <-chan net.Conn) IOConnector {
	return func(ctx context.Context, _ *logrus.Entry) <-chan IOConnectorResult {
		result := make(chan IOConnectorResult, 1)
		go func() {
			defer close(result)
			select {
			case <-ctx.Done():
				result <- IOConnectorResult{Err: ctx.Err()}
			case conn := <-ch:
				result <- IOConnectorResult{ReadWriteCloser: conn}
			}
		}()
		return result
	}
}

func (l *transportTestLink) attach() { left, right := net.Pipe(); l.left <- left; l.right <- right }

func transportProxyTestProcess(ctx context.Context) *vmProc {
	return &vmProc{taskID: "task", execID: "exec", ctx: ctx, logger: logrus.NewEntry(logrus.New())}
}

func TestTransportProxyNamedFIFORebindRetainsPumps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sourcePath := filepath.Join(t.TempDir(), "guest-output")
	sinkPath := filepath.Join(t.TempDir(), "host-output")
	require.NoError(t, ensureTransportFIFO(sinkPath))
	externalReader, err := os.OpenFile(sinkPath, os.O_RDONLY|unix.O_NONBLOCK, 0)
	require.NoError(t, err)
	defer externalReader.Close()
	output, err := NewTransportFIFO(externalReader)
	require.NoError(t, err)
	link := newTransportTestLink()
	guest := newTransportProxy(nil, &IOConnectorPair{transport: &transportPair{path: sourcePath, sender: true, connect: link.connect(link.left)}}, nil)
	host := newTransportProxy(nil, &IOConnectorPair{transport: &transportPair{path: sinkPath, connect: link.connect(link.right)}}, nil)
	defer guest.Close()
	defer host.Close()
	guestInit, guestDone := guest.start(transportProxyTestProcess(ctx))
	hostInit, hostDone := host.start(transportProxyTestProcess(ctx))
	writer, err := openTransportFIFO(ctx, sourcePath, false)
	require.NoError(t, err)
	defer writer.Close()
	link.attach()
	require.NoError(t, <-guestInit)
	require.NoError(t, <-hostInit)
	guest.mu.Lock()
	retained := guest.streams["stdout"]
	guest.mu.Unlock()
	host.mu.Lock()
	destination := host.streams["stdout"]
	host.mu.Unlock()
	payload := bytes.Repeat([]byte("stream-prefix/\x00\xff"), 32<<10)
	written := make(chan error, 1)
	go func() { _, err := writer.Write(payload); _ = writer.Close(); written <- err }()
	transportEventually(t, func() bool {
		return destination.receiver.Cursor().Offset > 0 && retained.sender.Status().PendingBytes > 0
	})
	prepareCtx, stop := context.WithTimeout(ctx, 40*time.Millisecond)
	err = guest.PrepareIO(prepareCtx)
	stop()
	// If a frame boundary was captured before the sink filled, resume and wait
	// for the next already-read chunk to block. This is a valid successful cut.
	if err == nil {
		guest.ResumeIO()
		transportEventually(t, func() bool { return retained.sender.Status().PendingBytes > 0 })
		prepareCtx, stop = context.WithTimeout(ctx, 40*time.Millisecond)
		err = guest.PrepareIO(prepareCtx)
		stop()
	}
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = guest.DescribeIO()
	require.Error(t, err)
	next := newTransportTestLink()
	guestConfig := newTransportProxy(nil, &IOConnectorPair{transport: &transportPair{path: sourcePath, sender: true, connect: next.connect(next.left)}}, nil)
	hostConfig := newTransportProxy(nil, &IOConnectorPair{transport: &transportPair{path: sinkPath, connect: next.connect(next.right)}}, nil)
	manager := NewTaskManager(ctx, logrus.NewEntry(logrus.New())).(*taskManager)
	proc := transportProxyTestProcess(ctx)
	proc.proxy, proc.ioCopyDone = guest, guestDone
	manager.tasks[proc.taskID] = map[string]*vmProc{proc.execID: proc}
	require.NoError(t, manager.AttachIO(ctx, proc.taskID, proc.execID, guestConfig))
	require.NoError(t, host.rebind(ctx, hostConfig))
	guest.mu.Lock()
	require.Same(t, retained, guest.streams["stdout"])
	guest.mu.Unlock()
	host.mu.Lock()
	require.Same(t, destination, host.streams["stdout"])
	host.mu.Unlock()
	next.attach()
	got := make(chan []byte, 1)
	go func() {
		var result []byte
		buf := make([]byte, 257)
		for {
			n, err := output.Read(ctx, buf)
			result = append(result, buf[:n]...)
			if err != nil {
				got <- result
				return
			}
		}
	}()
	manager.ResumeIO()
	require.NoError(t, <-written)
	require.NoError(t, <-guestDone)
	require.NoError(t, <-hostDone)
	require.Equal(t, payload, <-got)
	// Natural completion must leave ACK replay service alive until deletion.
	require.True(t, guest.IsOpen())
	require.True(t, host.IsOpen())
	require.NoError(t, manager.PrepareIO(ctx))
	require.NoError(t, host.PrepareIO(ctx))
	registry, err := manager.DescribeIO()
	require.NoError(t, err)
	require.Len(t, registry, 1)
	require.Len(t, registry[0].Streams, 1)
	require.True(t, registry[0].Streams[0].Cursor.FIN)
	require.Equal(t, uint64(len(payload)), registry[0].Streams[0].Cursor.Offset)
	peer, err := host.DescribeIO()
	require.NoError(t, err)
	require.Equal(t, registry[0].Streams[0].Cursor, peer[0].Cursor)
}

func TestTransportProxyInitialFIFODoesNotInventEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdin")
	require.NoError(t, ensureTransportFIFO(path))
	file, err := openTransportFIFO(context.Background(), path, true)
	require.NoError(t, err)
	defer file.Close()
	fifo, err := NewTransportFIFO(file)
	require.NoError(t, err)
	read := transportInitialFIFORead(fifo)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, err = read(ctx, make([]byte, 32))
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded, "no writer has ever connected, so initialization cannot imply EOF")
	writer, err := openTransportFIFO(context.Background(), path, false)
	require.NoError(t, err)
	_, err = writer.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	buf := make([]byte, 32)
	n, err := read(ctx, buf)
	require.NoError(t, err)
	require.Equal(t, "hello", string(buf[:n]))
	_, err = read(ctx, buf)
	require.ErrorIs(t, err, io.EOF)
}

func TestTaskManagerRejectsRawCheckpointIO(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	manager := NewTaskManager(ctx, logrus.NewEntry(logrus.New())).(*taskManager)
	proc := transportProxyTestProcess(ctx)
	proc.proxy = NewIOConnectorProxy(nil, &IOConnectorPair{}, nil)
	manager.tasks[proc.taskID] = map[string]*vmProc{proc.execID: proc}
	require.ErrorContains(t, manager.PrepareIO(ctx), "unsupported raw")
	_, err := manager.DescribeIO()
	require.ErrorContains(t, err, "unsupported raw")
	proc.proxy = NewNullIOProxy()
	require.NoError(t, manager.PrepareIO(ctx))
	registry, err := manager.DescribeIO()
	require.NoError(t, err)
	require.Empty(t, registry[0].Streams)
}

func TestTransportHostFactoryValidatesGuestDirections(t *testing.T) {
	cursor := TransportCursor{StreamID: "task//stdout", SessionID: "clone", Offset: 123}
	saved := []TransportStreamIO{{Name: "stdout", Sender: true, Cursor: cursor}}
	proxy := NewTransportHostIOProxy(time.Second, "vsock", TransportPorts{Stdout: 1}, "", "host-output", "", saved).(*transportProxy)
	require.NoError(t, proxy.configErr)
	require.False(t, proxy.configs["stdout"].sender)
	require.Equal(t, cursor, *proxy.configs["stdout"].cursor)
	saved[0].Sender = false
	invalid := NewTransportHostIOProxy(time.Second, "vsock", TransportPorts{Stdout: 1}, "", "host-output", "", saved).(*transportProxy)
	require.Error(t, invalid.configErr)
}

// A receiver must remain available after a lost final ACK, even after its local
// FIN observer has closed the destination FIFO.
func TestTransportProxyLostFINAckCanReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "output")
	require.NoError(t, ensureTransportFIFO(path))
	reader, err := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
	require.NoError(t, err)
	defer reader.Close()
	link := newTransportTestLink()
	host := newTransportProxy(nil, &IOConnectorPair{transport: &transportPair{path: path, connect: link.connect(link.right)}}, nil)
	defer host.Close()
	initDone, copyDone := host.start(transportProxyTestProcess(ctx))
	input := newTransportTestInput()
	close(input.chunks)
	sender, err := NewTransportSender(ctx, TransportCursor{StreamID: "task/exec/stdout", SessionID: "initial"}, input.read, TransportOptions{})
	require.NoError(t, err)
	defer sender.Close()
	left, right := net.Pipe()
	link.right <- &transportFINAckFault{Conn: right}
	serveDone := make(chan error, 1)
	go func() { serveDone <- sender.Serve(ctx, left) }()
	require.NoError(t, <-initDone)
	require.NoError(t, <-copyDone)
	<-serveDone
	require.True(t, host.IsOpen())
	require.False(t, sender.Status().Cursor.FIN)
	left, right = net.Pipe()
	link.right <- right
	go func() { serveDone <- sender.Serve(ctx, left) }()
	select {
	case <-sender.Finished():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	sender.Close()
	<-serveDone
}

func TestTransportProxyHandshakeBeforeOutputReaderExists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "late-reader")
	link := newTransportTestLink()
	host := newTransportProxy(nil, &IOConnectorPair{transport: &transportPair{path: path, connect: link.connect(link.right)}}, nil)
	defer host.Close()
	initDone, copyDone := host.start(transportProxyTestProcess(ctx))
	input := newTransportTestInput()
	payload := []byte("buffered until a real native restore client opens its FIFO")
	input.chunks <- payload
	close(input.chunks)
	sender, err := NewTransportSender(ctx, TransportCursor{StreamID: "task/exec/stdout", SessionID: "initial"}, input.read, TransportOptions{})
	require.NoError(t, err)
	defer sender.Close()
	left, right := net.Pipe()
	link.right <- right
	done := make(chan error, 1)
	go func() { done <- sender.Serve(ctx, left) }()
	select {
	case err := <-initDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("handshake waited for nonexistent external FIFO reader")
	}
	prepareCtx, stop := context.WithTimeout(ctx, 40*time.Millisecond)
	_, err = sender.Prepare(prepareCtx)
	stop()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, len(payload), sender.Status().PendingBytes)
	require.Zero(t, host.streams["stdout"].receiver.Cursor().Offset, "no real FIFO reader means no accepted bytes or ACK")
	got := make(chan []byte, 1)
	go func() {
		file, err := os.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			got <- nil
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		got <- data
	}()
	sender.Resume()
	select {
	case <-sender.Finished():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.NoError(t, <-copyDone)
	require.Equal(t, payload, <-got)
	sender.Close()
	<-done
}

func TestTransportProxyCapturedFINOpensAndClosesForLateReader(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "already-finished")
	cursor := TransportCursor{StreamID: "task/exec/stdout", SessionID: "clone", Offset: 981, FIN: true}
	link := newTransportTestLink()
	host := newTransportProxy(nil, &IOConnectorPair{transport: &transportPair{path: path, connect: link.connect(link.right), cursor: &cursor}}, nil)
	defer host.Close()
	initDone, copyDone := host.start(transportProxyTestProcess(ctx))
	sender, err := NewTransportSender(ctx, cursor, func(context.Context, []byte) (int, error) {
		t.Error("captured FIN must not read historical input")
		return 0, io.EOF
	}, TransportOptions{StartPaused: true})
	require.NoError(t, err)
	defer sender.Close()
	left, right := net.Pipe()
	link.right <- right
	done := make(chan error, 1)
	go func() { done <- sender.Serve(ctx, left) }()
	select {
	case err := <-initDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("captured FIN initialization waited for reader")
	}
	require.NoError(t, <-copyDone)
	got := make(chan []byte, 1)
	go func() {
		file, err := os.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			got <- []byte("open error")
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		got <- data
	}()
	select {
	case data := <-got:
		require.Empty(t, data)
	case <-ctx.Done():
		t.Fatal("late FIFO reader did not receive actual EOF")
	}
	sender.Close()
	<-done
}

func TestTransportProxyCloseCancelsLazyFIFOOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "never-opened")
	cursor := TransportCursor{StreamID: "task/exec/stdout", SessionID: "clone", FIN: true}
	link := newTransportTestLink()
	host := newTransportProxy(nil, &IOConnectorPair{transport: &transportPair{path: path, connect: link.connect(link.right), cursor: &cursor}}, nil)
	initDone, _ := host.start(transportProxyTestProcess(ctx))
	require.NoError(t, host.waitReady(ctx))
	local := host.streams["stdout"].local
	host.Close()
	require.Error(t, <-initDone)
	transportEventually(t, func() bool { local.mu.Lock(); defer local.mu.Unlock(); return local.closed })
	local.mu.Lock()
	require.Nil(t, local.file, "cancelled open must not leak a FIFO descriptor")
	local.mu.Unlock()
}

// A snapshot can invalidate a connection without waking an idle sender, which
// does not read the socket until it has sent another frame. Simulate a lost
// barrier write while the old socket remains open, then replace only the network
// generations as source recovery does through AttachIO.
type transportDroppedWritesConn struct {
	net.Conn
	drop atomic.Bool
}

func (c *transportDroppedWritesConn) Write(p []byte) (int, error) {
	if c.drop.Load() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func TestTransportProxySourceReconnectAfterIdleReset(t *testing.T) {
	for _, session := range []string{"initial", "restored-source-session"} {
		for _, fin := range []bool{false, true} {
			name := session + "/open"
			if fin {
				name = session + "/finished"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				sourcePath := filepath.Join(t.TempDir(), "guest-output")
				sinkPath := filepath.Join(t.TempDir(), "host-output")
				require.NoError(t, ensureTransportFIFO(sinkPath))
				reader, err := os.OpenFile(sinkPath, os.O_RDONLY|unix.O_NONBLOCK, 0)
				require.NoError(t, err)
				defer reader.Close()
				cursor := TransportCursor{StreamID: "task/exec/stdout", SessionID: session, Offset: 17, FIN: fin}
				link := newTransportTestLink()
				guest := newTransportProxy(nil, &IOConnectorPair{transport: &transportPair{path: sourcePath, sender: true, cursor: &cursor, connect: link.connect(link.left)}}, nil)
				host := newTransportProxy(nil, &IOConnectorPair{transport: &transportPair{path: sinkPath, cursor: &cursor, connect: link.connect(link.right)}}, nil)
				defer guest.Close()
				defer host.Close()
				guestInit, _ := guest.start(transportProxyTestProcess(ctx))
				hostInit, _ := host.start(transportProxyTestProcess(ctx))
				left, right := net.Pipe()
				stale := &transportDroppedWritesConn{Conn: left}
				link.left <- stale
				link.right <- right
				require.NoError(t, <-guestInit)
				require.NoError(t, <-hostInit)
				require.NoError(t, guest.PrepareIO(ctx))
				retained := guest.streams["stdout"]
				destination := host.streams["stdout"]
				require.Equal(t, cursor, retained.sender.Status().Cursor)
				stale.drop.Store(true)
				prepareCtx, stop := context.WithTimeout(ctx, 30*time.Millisecond)
				err = guest.PrepareIO(prepareCtx)
				stop()
				require.ErrorIs(t, err, context.DeadlineExceeded, "a fresh barrier cannot cross the stale connection")
				require.False(t, retained.sender.Status().Prepared)
				next := newTransportTestLink()
				// Nil replacement cursors retain the source session, byte cut and FIN.
				guestConfig := newTransportProxy(nil, &IOConnectorPair{transport: &transportPair{path: sourcePath, sender: true, connect: next.connect(next.left)}}, nil)
				hostConfig := newTransportProxy(nil, &IOConnectorPair{transport: &transportPair{path: sinkPath, connect: next.connect(next.right)}}, nil)
				require.NoError(t, guest.rebind(ctx, guestConfig))
				require.NoError(t, host.rebind(ctx, hostConfig))
				next.attach()
				require.NoError(t, guest.PrepareIO(ctx))
				require.Same(t, retained, guest.streams["stdout"])
				require.Same(t, destination, host.streams["stdout"])
				require.Equal(t, cursor, retained.sender.Status().Cursor)
				require.Equal(t, cursor, destination.receiver.Cursor())
				require.True(t, retained.sender.Status().Paused)
				require.False(t, retained.sender.Status().Reading)
			})
		}
	}
}
