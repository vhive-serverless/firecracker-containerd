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

package main

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/containerd/ttrpc"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/types/known/emptypb"
)

// A restored agent can still have a connection to its source host. Once its
// canceled Wait handler responds, that captured transport may never finish a
// write. Keep the receive path usable so this models a stalled response rather
// than an already-closed socket.
type stalledAgentConn struct {
	net.Conn
	armed   chan struct{}
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *stalledAgentConn) Write(data []byte) (int, error) {
	select {
	case <-c.armed:
		c.once.Do(func() { close(c.started) })
		<-c.release
	default:
	}
	return c.Conn.Write(data)
}

type agentTestListener struct {
	net.Listener
	stale    *stalledAgentConn
	accepted bool
}

func (l *agentTestListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil && !l.accepted {
		l.accepted = true
		if l.stale != nil {
			l.stale.Conn = conn
			return l.stale, nil
		}
	}
	return conn, err
}

func TestAgentShutdownFlushesResponseAndBoundsCapturedConnection(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		name := "live connections"
		if stalled {
			name = "captured connection cannot write"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			agentCtx, cancelAgent := context.WithCancel(context.Background())
			defer cancelAgent()
			group, agentCtx := errgroup.WithContext(agentCtx)
			server, err := ttrpc.NewServer()
			require.NoError(t, err)
			defer server.Close()
			waiting := make(chan struct{})
			liveWaiting := make(chan struct{})
			server.Register("shutdown-test", map[string]ttrpc.Method{
				"Wait": func(ctx context.Context, _ func(any) error) (any, error) {
					close(waiting)
					<-ctx.Done()
					return nil, ctx.Err()
				},
				"Hold": func(ctx context.Context, _ func(any) error) (any, error) {
					close(liveWaiting)
					<-ctx.Done()
					return nil, ctx.Err()
				},
				"Ready": func(context.Context, func(any) error) (any, error) {
					return &emptypb.Empty{}, nil
				},
				"Shutdown": func(context.Context, func(any) error) (any, error) {
					cancelAgent()
					return &emptypb.Empty{}, nil
				},
			})
			rawListener, err := net.Listen("unix", filepath.Join(t.TempDir(), "agent.sock"))
			require.NoError(t, err)
			defer rawListener.Close()
			listener := &agentTestListener{Listener: rawListener}
			if stalled {
				listener.stale = &stalledAgentConn{armed: make(chan struct{}), started: make(chan struct{}), release: make(chan struct{})}
				defer close(listener.stale.release)
			}
			group.Go(func() error {
				err := server.Serve(agentCtx, listener)
				if errors.Is(err, ttrpc.ErrServerClosed) {
					return nil
				}
				return err
			})
			drainResult := make(chan error, 1)
			group.Go(func() error {
				<-agentCtx.Done()
				drainResult <- shutdownAgentServer(server, 500*time.Millisecond)
				return agentCtx.Err()
			})
			dial := func() *ttrpc.Client {
				connection, err := net.Dial("unix", listener.Addr().String())
				require.NoError(t, err)
				client := ttrpc.NewClient(connection)
				t.Cleanup(func() { _ = client.Close() })
				return client
			}
			captured := dial()
			waitResult := make(chan error, 1)
			go func() {
				waitResult <- captured.Call(ctx, "shutdown-test", "Wait", &emptypb.Empty{}, &emptypb.Empty{})
			}()
			select {
			case <-waiting:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// Two completed replies ensure the connection loop has observed
			// the retained Wait as active before its response write is stalled.
			for range 2 {
				require.NoError(t, captured.Call(ctx, "shutdown-test", "Ready", &emptypb.Empty{}, &emptypb.Empty{}))
			}
			if stalled {
				close(listener.stale.armed)
			}
			live := dial()
			liveWaitResult := make(chan error, 1)
			go func() {
				liveWaitResult <- live.Call(ctx, "shutdown-test", "Hold", &emptypb.Empty{}, &emptypb.Empty{})
			}()
			select {
			case <-liveWaiting:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// A completed response while Hold remains pending makes ttrpc's
			// connection state active before shutdown starts its idle sweep.
			for range 2 {
				require.NoError(t, live.Call(ctx, "shutdown-test", "Ready", &emptypb.Empty{}, &emptypb.Empty{}))
			}
			require.NoError(t, live.Call(ctx, "shutdown-test", "Shutdown", &emptypb.Empty{}, &emptypb.Empty{}), "live shutdown response must flush before final agent exit")
			if stalled {
				select {
				case <-listener.stale.started:
				case <-ctx.Done():
					t.Fatal("captured response never entered the blocked write")
				}
			}
			select {
			case err := <-drainResult:
				if stalled {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				} else {
					require.NoError(t, err)
				}
			case <-ctx.Done():
				t.Fatal("agent drain prevented process exit")
			}
			require.ErrorIs(t, group.Wait(), context.Canceled, "bounded drain must leave the main group on its successful cancellation path")
			require.Error(t, <-liveWaitResult, "live pending Hold must be canceled")
			if !stalled {
				require.Error(t, <-waitResult, "ordinary pending Wait must be canceled")
			}
		})
	}
}
