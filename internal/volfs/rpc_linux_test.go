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

package volfs

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type announcedListener struct {
	net.Listener
	ready chan struct{}
	once  sync.Once
}

func (l *announcedListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.ready) })
	return l.Listener.Accept()
}

func rpcServer(t *testing.T) (*Server, Descriptor, DialFunc, string) {
	t.Helper()
	return rpcSourceServer(t, t.TempDir(), false)
}

func rpcSourceServer(t *testing.T, root string, readOnly bool) (*Server, Descriptor, DialFunc, string) {
	t.Helper()
	export, err := OpenExport(root, readOnly, Limits{})
	require.NoError(t, err)
	server := NewServer()
	descriptor, err := server.Register(export)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	announced := &announcedListener{Listener: listener, ready: make(chan struct{})}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(context.Background(), announced) }()
	<-announced.ready
	t.Cleanup(func() {
		require.NoError(t, server.Close())
		select {
		case err := <-finished:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Error("volume RPC server did not stop")
		}
	})
	dial := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	}
	return server, descriptor, dial, root
}

func rpcClient(t *testing.T, descriptor Descriptor, dial DialFunc) *Client {
	t.Helper()
	client, err := NewClient(context.Background(), descriptor, dial)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

type lostReplyConn struct {
	net.Conn
	drop      *atomic.Bool
	afterRead func()
}

func (c *lostReplyConn) Read(data []byte) (int, error) {
	n, err := c.Conn.Read(data)
	if n > 0 && c.drop.CompareAndSwap(true, false) {
		// Read an actual response before breaking the stream: the host already
		// performed the operation, but the client has no complete reply.
		if c.afterRead != nil {
			c.afterRead()
		}
		_ = c.Close()
		return 0, io.ErrUnexpectedEOF
	}
	return n, err
}

func TestRPCReconnectDoesNotRepeatMutations(t *testing.T) {
	_, descriptor, dial, root := rpcServer(t)
	var drop atomic.Bool
	var connections atomic.Int32
	var cancelOnDrop context.CancelFunc
	client := rpcClient(t, descriptor, func(ctx context.Context) (net.Conn, error) {
		conn, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		connections.Add(1)
		return &lostReplyConn{Conn: conn, drop: &drop, afterRead: func() {
			if cancelOnDrop != nil {
				cancelOnDrop()
			}
		}}, nil
	})
	drop.Store(true)
	created, err := client.Call(context.Background(), Request{Operation: "create", Parent: Root,
		Name: []byte("file"), Flags: unix.O_RDWR | unix.O_APPEND | unix.O_EXCL, Mode: 0600, Cred: caller()})
	require.NoError(t, err)
	require.NotNil(t, created.Entry)
	require.NotZero(t, created.Handle)
	require.GreaterOrEqual(t, connections.Load(), int32(2))
	// A canceled caller cannot abandon an already issued mutation. Its result
	// is resolved with the mount lifetime and the next request stays ordered.
	writeCtx, cancelWrite := context.WithCancel(context.Background())
	defer cancelWrite()
	cancelOnDrop = cancelWrite
	drop.Store(true)
	written, err := client.Call(writeCtx, Request{Operation: "write", Handle: created.Handle, Data: []byte("once")})
	require.NoError(t, err)
	require.Equal(t, 4, written.Written)
	require.ErrorIs(t, writeCtx.Err(), context.Canceled)
	data, err := os.ReadFile(filepath.Join(root, "file"))
	require.NoError(t, err)
	require.Equal(t, "once", string(data))
	drop.Store(true)
	_, err = client.Call(context.Background(), Request{Operation: "rename", Parent: Root, Name: []byte("file"), NewParent: Root, NewName: []byte("renamed")})
	require.NoError(t, err)
	_, err = client.Call(context.Background(), Request{Operation: "release", Handle: created.Handle})
	require.NoError(t, err)
	_, err = client.Call(context.Background(), Request{Operation: "forget", Node: created.Entry.Node, Count: 1})
	require.NoError(t, err)
	data, err = os.ReadFile(filepath.Join(root, "renamed"))
	require.NoError(t, err)
	require.Equal(t, "once", string(data))
}

func TestRPCBinaryNamesLargeIOAndConcurrentCalls(t *testing.T) {
	_, descriptor, dial, root := rpcServer(t)
	client := rpcClient(t, descriptor, dial)
	name := []byte{'v', 0xff}
	created, err := client.Call(context.Background(), Request{Operation: "create", Parent: Root,
		Name: name, Flags: unix.O_RDWR | unix.O_EXCL, Mode: 0600, Cred: caller()})
	require.NoError(t, err)
	data := bytes.Repeat([]byte{0, 0xff, 'a', 'b'}, MaxIO/4)
	_, err = client.Call(context.Background(), Request{Operation: "write", Handle: created.Handle, Data: data})
	require.NoError(t, err)
	reply, err := client.Call(context.Background(), Request{Operation: "read", Handle: created.Handle, Size: len(data)})
	require.NoError(t, err)
	require.Equal(t, data, reply.Data)
	listing, err := client.Call(context.Background(), Request{Operation: "readdir", Node: Root})
	require.NoError(t, err)
	require.Equal(t, name, listing.Entries[0].Name)
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			_, err := client.Call(context.Background(), Request{Operation: "write", Handle: created.Handle,
				Offset: int64(index * 1024), Data: bytes.Repeat([]byte{byte(index)}, 1024)})
			require.NoError(t, err)
		}(i)
	}
	workers.Wait()
	actual, err := os.ReadFile(filepath.Join(root, string(name)))
	require.NoError(t, err)
	for i := 0; i < 32; i++ {
		require.Equal(t, bytes.Repeat([]byte{byte(i)}, 1024), actual[i*1024:(i+1)*1024])
	}
}

func TestRPCReplayRejectsChangedContentAndWrongSequence(t *testing.T) {
	_, descriptor, dial, root := rpcServer(t)
	conn, err := dial(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	request := Request{Version: ProtocolVersion, Token: descriptor.Token, Session: strings.Repeat("a", 32), ID: 1,
		Operation: "create", Parent: Root, Name: []byte("first"), Flags: unix.O_RDWR | unix.O_EXCL, Mode: 0600, Cred: caller()}
	call := func(request Request) Reply {
		data, err := json.Marshal(request)
		require.NoError(t, err)
		require.NoError(t, writeFrame(conn, data))
		data, err = readFrame(conn)
		require.NoError(t, err)
		var reply Reply
		require.NoError(t, json.Unmarshal(data, &reply))
		return reply
	}
	first := call(request)
	require.Zero(t, first.Status)
	replayed := call(request)
	require.Equal(t, first, replayed)
	request.Name = []byte("second")
	changed := call(request)
	require.True(t, changed.Fatal)
	require.EqualValues(t, unix.EPROTO, changed.Status)
	_, err = os.Stat(filepath.Join(root, "second"))
	require.ErrorIs(t, err, os.ErrNotExist)
	request.ID = 3
	require.EqualValues(t, unix.ESTALE, call(request).Status)
}

func TestRPCCapabilityRevocationAndSessionOwnership(t *testing.T) {
	server, descriptor, dial, _ := rpcServer(t)
	client := rpcClient(t, descriptor, dial)
	_, err := client.Call(context.Background(), Request{Operation: "getattr", Node: Root})
	require.NoError(t, err)
	other := rpcClient(t, descriptor, dial)
	_, err = other.Call(context.Background(), Request{Operation: "getattr", Node: Root})
	require.ErrorIs(t, err, unix.EBUSY)
	invalid := descriptor
	invalid.Token = strings.Repeat("0", 32)
	unauthorized := rpcClient(t, invalid, dial)
	_, err = unauthorized.Call(context.Background(), Request{Operation: "getattr", Node: Root})
	require.ErrorIs(t, err, unix.EACCES)
	require.NoError(t, server.Unregister(descriptor.Token))
	require.NoError(t, server.Unregister(descriptor.Token))
	_, err = client.Call(context.Background(), Request{Operation: "getattr", Node: Root})
	require.ErrorIs(t, err, unix.EACCES)
}

func TestRPCRejectsOversizedFrameBeforeReadingBody(t *testing.T) {
	_, _, dial, _ := rpcServer(t)
	conn, err := dial(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], MaxFrame+1)
	_, err = conn.Write(prefix[:])
	require.NoError(t, err)
	var data [1]byte
	_, err = conn.Read(data[:])
	require.Error(t, err)
	var timeout net.Error
	if errors.As(err, &timeout) {
		require.False(t, timeout.Timeout())
	}
}

func TestRPCClientCancellationAndClose(t *testing.T) {
	_, descriptor, dial, _ := rpcServer(t)
	client := rpcClient(t, descriptor, dial)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Call(canceled, Request{Operation: "getattr", Node: Root})
	require.ErrorIs(t, err, context.Canceled)
	_, err = client.Call(context.Background(), Request{Operation: "getattr", Node: Root})
	require.NoError(t, err) // pre-issue cancellation did not consume request ID 1
	entered := make(chan struct{})
	blocked := rpcClient(t, descriptor, func(ctx context.Context) (net.Conn, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	result := make(chan error, 1)
	go func() {
		_, err := blocked.Call(context.Background(), Request{Operation: "getattr", Node: Root})
		result <- err
	}()
	<-entered
	require.NoError(t, blocked.Close())
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("client shutdown did not interrupt dial")
	}
}
