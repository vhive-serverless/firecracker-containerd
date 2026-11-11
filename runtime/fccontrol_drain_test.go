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
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/pkg/protobuf/types"
	"github.com/containerd/ttrpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFCControlDrainPreservesStopReply(t *testing.T) {
	for i := range 32 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			path := filepath.Join(t.TempDir(), "control.sock")
			listener, err := net.Listen("unix", path)
			require.NoError(t, err)
			drain := newFCControlDrain(listener)
			server, err := ttrpc.NewServer(ttrpc.WithServerHandshaker(drain))
			require.NoError(t, err)
			defer server.Close()

			stopped := make(chan error, 1)
			server.Register("control", map[string]ttrpc.Method{
				"Stop": func(context.Context, func(interface{}) error) (interface{}, error) {
					go func() {
						err := drain.Drain(ctx)
						stopped <- errors.Join(err, server.Close())
					}()
					return nil, status.Error(codes.Internal, "forcefully terminated VM")
				},
			})
			served := make(chan error, 1)
			go func() { served <- server.Serve(ctx, drain) }()

			conn, err := net.Dial("unix", path)
			require.NoError(t, err)
			client := ttrpc.NewClient(conn)
			defer client.Close()
			err = client.Call(ctx, "control", "Stop", &types.Empty{}, &types.Empty{})
			require.Equal(t, codes.Internal, status.Code(err), "stop reply lost: %v", err)
			require.ErrorContains(t, err, "forcefully terminated VM")
			select {
			case err := <-stopped:
				t.Fatalf("shim exited before controller acknowledged the reply: %v", err)
			default:
			}
			require.NoError(t, client.Close())
			select {
			case err := <-stopped:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case err := <-served:
				require.True(t, errors.Is(err, net.ErrClosed) || errors.Is(err, ttrpc.ErrServerClosed), "unexpected Serve error: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}
