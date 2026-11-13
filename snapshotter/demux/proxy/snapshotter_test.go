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

package proxy

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/connectivity"
)

func TestCustomDialerBypassesDNSAndCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	dialed := make(chan string, 1)
	const endpoint = "/path/to/guest.vsock"
	remote, err := NewRemoteSnapshotter(ctx, endpoint, func(_ context.Context, address string) (net.Conn, error) {
		select {
		case dialed <- address:
		default:
		}
		return nil, errors.New("test dialer rejection")
	}, nil)
	require.NoError(t, err)
	defer remote.Close()
	result := make(chan error, 1)
	go func() { _, err := remote.Stat(ctx, "missing"); result <- err }()
	select {
	case address := <-dialed:
		require.Equal(t, endpoint, address)
	case <-ctx.Done():
		t.Fatal("gRPC attempted DNS instead of the custom vsock dialer")
	}
	cancel()
	require.Error(t, <-result)
	require.NoError(t, remote.Close())
	require.Equal(t, connectivity.Shutdown, remote.connection.GetState())
}
