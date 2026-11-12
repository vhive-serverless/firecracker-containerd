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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// PodForwardPort is separate from the task API and per-process IO ports.
const PodForwardPort uint32 = 10900

const portForwardHandshakeTimeout = 5 * time.Second

// OpenGuestTCP asks the guest to connect a vsock stream to its loopback port.
// The caller already selected the VM using the authorized Pod sandbox ID.
func OpenGuestTCP(conn net.Conn, port int32) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid guest TCP port %d", port)
	}
	if err := conn.SetDeadline(time.Now().Add(portForwardHandshakeTimeout)); err != nil {
		return err
	}
	defer conn.SetDeadline(time.Time{})
	header := []byte{'F', 'C', 'P', 'F', 1, 0, 0}
	binary.BigEndian.PutUint16(header[5:], uint16(port))
	if err := writeForwardBytes(conn, header); err != nil {
		return err
	}
	var length [2]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint16(length[:])
	if size > 1024 {
		return errors.New("invalid guest port-forward response")
	}
	if size != 0 {
		message := make([]byte, size)
		if _, err := io.ReadFull(conn, message); err != nil {
			return err
		}
		return fmt.Errorf("guest port-forward: %s", message)
	}
	return nil
}

// ServeGuestTCP handles one host connection. Only guest loopback is reachable;
// the protocol does not accept a caller-supplied address or vsock CID.
func ServeGuestTCP(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(portForwardHandshakeTimeout)); err != nil {
		return err
	}
	var header [7]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	if string(header[:4]) != "FCPF" || header[4] != 1 {
		return errors.New("unsupported guest port-forward protocol")
	}
	port := binary.BigEndian.Uint16(header[5:])
	if port == 0 {
		return errors.New("invalid guest port-forward port")
	}
	dialer := net.Dialer{Timeout: portForwardHandshakeTimeout}
	peer, err := dialer.DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	if err != nil {
		peer, err = dialer.DialContext(ctx, "tcp6", net.JoinHostPort("::1", strconv.Itoa(int(port))))
	}
	if err != nil {
		message := []byte(err.Error())
		if len(message) > 1024 {
			message = message[:1024]
		}
		var response [2]byte
		binary.BigEndian.PutUint16(response[:], uint16(len(message)))
		_ = writeForwardBytes(conn, append(response[:], message...))
		return err
	}
	defer peer.Close()
	if err := writeForwardBytes(conn, []byte{0, 0}); err != nil {
		return err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	return RelayStreams(ctx, conn, peer)
}

func writeForwardBytes(w io.Writer, data []byte) error {
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}

// RelayStreams copies both directions, preserves TCP/vsock half-closes, and
// closes both endpoints on cancellation or an IO failure.
func RelayStreams(ctx context.Context, a, b io.ReadWriteCloser) error {
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = a.Close(); _ = b.Close() }) }
	defer closeBoth()
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	results := make(chan error, 2)
	copyTo := func(dst io.ReadWriteCloser, src io.Reader) {
		_, err := io.Copy(dst, src)
		if err == nil {
			if half, ok := dst.(interface{ CloseWrite() error }); ok {
				err = half.CloseWrite()
			} else {
				err = dst.Close()
			}
		}
		if err != nil {
			closeBoth()
		}
		results <- err
	}
	go copyTo(a, b)
	go copyTo(b, a)
	first, second := <-results, <-results
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.Join(first, second)
}
