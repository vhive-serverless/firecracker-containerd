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
	"crypto/sha256"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func forwardTCPPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer listener.Close()
	client, err := net.DialTCP("tcp4", nil, listener.Addr().(*net.TCPAddr))
	require.NoError(t, err)
	server, err := listener.AcceptTCP()
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	deadline := time.Now().Add(15 * time.Second)
	require.NoError(t, client.SetDeadline(deadline))
	require.NoError(t, server.SetDeadline(deadline))
	return client, server
}

func TestPortForwardPreservesHalfCloseAndBinaryResponse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	workload, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer workload.Close()
	request := bytes.Repeat([]byte{0, 1, 255, 128}, 1<<16)
	response := bytes.Repeat([]byte{255, 0, 17, 129}, 1<<20)
	workloadDone := make(chan error, 1)
	go func() {
		conn, err := workload.Accept()
		if err != nil {
			workloadDone <- err
			return
		}
		defer conn.Close()
		data, err := io.ReadAll(conn)
		if err == nil && !bytes.Equal(data, request) {
			err = io.ErrUnexpectedEOF
		}
		if err == nil {
			_, err = io.Copy(conn, bytes.NewReader(response))
		}
		workloadDone <- err
	}()
	_, portText, err := net.SplitHostPort(workload.Addr().String())
	require.NoError(t, err)
	port, err := strconv.ParseInt(portText, 10, 32)
	require.NoError(t, err)
	host, guest := forwardTCPPair(t)
	guestDone := make(chan error, 1)
	go func() { guestDone <- ServeGuestTCP(ctx, guest) }()
	require.NoError(t, OpenGuestTCP(host, int32(port)))
	client, frontend := forwardTCPPair(t)
	frontendDone := make(chan error, 1)
	go func() { frontendDone <- RelayStreams(ctx, frontend, host) }()
	_, err = io.Copy(client, bytes.NewReader(request))
	require.NoError(t, err)
	require.NoError(t, client.CloseWrite())
	actual, err := io.ReadAll(client)
	require.NoError(t, err)
	require.Len(t, actual, len(response))
	require.Equal(t, sha256.Sum256(response), sha256.Sum256(actual))
	require.NoError(t, <-workloadDone)
	require.NoError(t, <-guestDone)
	require.NoError(t, <-frontendDone)
}

func TestPortForwardCancellationClosesBothDirections(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	leftClient, left := forwardTCPPair(t)
	rightClient, right := forwardTCPPair(t)
	done := make(chan error, 1)
	go func() { done <- RelayStreams(ctx, left, right) }()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("port-forward did not stop on cancellation")
	}
	data := make([]byte, 1)
	_, err := leftClient.Read(data)
	require.Error(t, err)
	_, err = rightClient.Read(data)
	require.Error(t, err)
}

func TestPortForwardRejectsInvalidPortAndProtocol(t *testing.T) {
	client, server := forwardTCPPair(t)
	require.Error(t, OpenGuestTCP(client, 0))
	require.Error(t, OpenGuestTCP(client, 65536))
	done := make(chan error, 1)
	go func() { done <- ServeGuestTCP(context.Background(), server) }()
	_, err := client.Write([]byte("invalid"))
	require.NoError(t, err)
	require.ErrorContains(t, <-done, "unsupported guest port-forward protocol")
}
