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
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type heldSnapshotStart struct {
	taskAPI.TTRPCTaskService
	entered, release chan struct{}
	calls            atomic.Int32
	completed        atomic.Bool
}

func (b *heldSnapshotStart) Start(ctx context.Context, _ *taskAPI.StartRequest) (*taskAPI.StartResponse, error) {
	b.calls.Add(1)
	close(b.entered)
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	b.completed.Store(true)
	return &taskAPI.StartResponse{Pid: 4242}, nil
}

// Exercise the production service's admission lock around an outstanding guest
// RPC. This uses only an SDK PID precondition; it never starts or signals a VMM.
func TestSnapshotAdmissionDrainsHeldMutation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	backend := &heldSnapshotStart{entered: make(chan struct{}), release: make(chan struct{})}
	machine, err := fc.NewMachine(ctx, fc.Config{}, fc.WithProcessRunner(&exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}))
	require.NoError(t, err)
	service := &service{machine: machine, agentClient: backend}
	type result struct {
		response *taskAPI.StartResponse
		err      error
	}
	completed := make(chan result, 1)
	go func() {
		response, err := service.Start(ctx, &taskAPI.StartRequest{ID: "held-start"})
		completed <- result{response, err}
	}()
	select {
	case <-backend.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	blocked, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	err = service.lockSnapshotTransition(blocked)
	stop()
	if err == nil {
		service.snapshotMu.Unlock()
		t.Fatal("snapshot transition passed an outstanding admitted mutation")
	}
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, backend.completed.Load())
	close(backend.release)
	require.NoError(t, service.lockSnapshotTransition(ctx))
	require.True(t, backend.completed.Load(), "the guest mutation result must complete before snapshot owns the cut")
	service.pendingPause = &pendingVMPause{}
	service.snapshotMu.Unlock()
	select {
	case result := <-completed:
		require.NoError(t, result.err)
		require.Equal(t, uint32(4242), result.response.Pid)
	case <-ctx.Done():
		t.Fatal("admitted mutation result was lost")
	}
	_, err = service.Start(ctx, &taskAPI.StartRequest{ID: "late-start"})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, int32(1), backend.calls.Load(), "mutation must not be retried or admitted across the prepared cut")
}
