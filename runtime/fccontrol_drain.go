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
	"net"
	"sync"

	"github.com/containerd/ttrpc"
)

// fcControlDrain keeps the shim alive until its controller has consumed the
// final RPC response and closed its connection. The shutdown context bounds
// clients that do not acknowledge the response by closing.
type fcControlDrain struct {
	net.Listener
	credentials ttrpc.Handshaker
	closeOnce   sync.Once
	closeErr    error

	mu       sync.Mutex
	active   int
	draining bool
	drained  chan struct{}
}

func newFCControlDrain(listener net.Listener) *fcControlDrain {
	return &fcControlDrain{
		Listener:    listener,
		credentials: ttrpc.UnixSocketRequireSameUser(),
		drained:     make(chan struct{}),
	}
}

func (d *fcControlDrain) Handshake(ctx context.Context, conn net.Conn) (net.Conn, interface{}, error) {
	approved, credentials, err := d.credentials.Handshake(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.draining {
		return nil, nil, net.ErrClosed
	}
	d.active++
	return &fcControlConn{Conn: approved, drain: d}, credentials, nil
}

func (d *fcControlDrain) Close() error {
	d.closeOnce.Do(func() { d.closeErr = d.Listener.Close() })
	return d.closeErr
}

func (d *fcControlDrain) Drain(ctx context.Context) error {
	d.mu.Lock()
	if !d.draining {
		d.draining = true
		if d.active == 0 {
			close(d.drained)
		}
	}
	d.mu.Unlock()
	// Stop accepting requests while existing controller connections finish.
	_ = d.Close()
	select {
	case <-d.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type fcControlConn struct {
	net.Conn
	drain *fcControlDrain
	once  sync.Once
}

func (c *fcControlConn) finished() {
	c.once.Do(func() {
		c.drain.mu.Lock()
		defer c.drain.mu.Unlock()
		c.drain.active--
		if c.drain.draining && c.drain.active == 0 {
			close(c.drain.drained)
		}
	})
}

func (c *fcControlConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil {
		c.finished()
	}
	return n, err
}

func (c *fcControlConn) Close() error {
	err := c.Conn.Close()
	c.finished()
	return err
}
