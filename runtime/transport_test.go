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
	"testing"
	"time"

	"github.com/containerd/containerd/v2/pkg/protobuf/types"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	ioproxy "github.com/firecracker-microvm/firecracker-containerd/proto/service/ioproxy/ttrpc"
	"github.com/stretchr/testify/require"
)

type transportTestTasks struct {
	vm.TaskManager
	prepare   func(context.Context) error
	processes []vm.TransportProcessIO
	resumed   bool
}

func (m *transportTestTasks) PrepareIO(ctx context.Context) error          { return m.prepare(ctx) }
func (m *transportTestTasks) DescribeIO() ([]vm.TransportProcessIO, error) { return m.processes, nil }
func (m *transportTestTasks) ResumeIO()                                    { m.resumed = true }

type transportTestPeer struct {
	ioproxy.IOProxyService
	prepare   func(context.Context) error
	processes []*ioproxy.TransportProcess
	resumeErr error
	resumed   bool
}

func (p *transportTestPeer) Prepare(ctx context.Context, _ *types.Empty) (*types.Empty, error) {
	return &types.Empty{}, p.prepare(ctx)
}
func (p *transportTestPeer) Describe(context.Context, *types.Empty) (*ioproxy.DescribeResponse, error) {
	return &ioproxy.DescribeResponse{Processes: p.processes}, nil
}
func (p *transportTestPeer) Resume(context.Context, *types.Empty) (*types.Empty, error) {
	p.resumed = true
	return &types.Empty{}, p.resumeErr
}

func TestPrepareTransportCoordinatesPeersAndMatchesCursors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	hostStarted, guestStarted := make(chan struct{}), make(chan struct{})
	cursor := vm.TransportCursor{StreamID: "task//stdout", SessionID: "session", Offset: 42}
	host := &transportTestTasks{processes: []vm.TransportProcessIO{{TaskID: "task", Streams: []vm.TransportStreamIO{{Name: transportStdout, Sender: false, Cursor: cursor}}}}}
	host.prepare = func(ctx context.Context) error {
		close(hostStarted)
		select {
		case <-guestStarted:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	guestStreams := []vm.TransportStreamIO{{Name: transportStdout, Sender: true, Cursor: cursor}}
	guest := &transportTestPeer{processes: []*ioproxy.TransportProcess{{TaskID: "task", Streams: transportStreamsToWire(guestStreams)}}}
	guest.prepare = func(ctx context.Context) error {
		close(guestStarted)
		select {
		case <-hostStarted:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s := &service{taskManager: host, ioProxyClient: guest}
	processes, err := s.prepareIOTransport(ctx)
	require.NoError(t, err)
	require.Equal(t, []vm.TransportProcessIO{{TaskID: "task", Streams: guestStreams}}, processes)
	// Cursor disagreement cannot be presented as a clean preparation.
	guest.processes[0].Streams[0].Cursor.Offset++
	host.prepare = func(context.Context) error { return nil }
	guest.prepare = func(context.Context) error { return nil }
	_, err = s.prepareIOTransport(ctx)
	require.ErrorContains(t, err, "mismatched stream cut")
}

func TestTransportPreparationFailureCancelsPeerAndResumeReleasesHost(t *testing.T) {
	failure := errors.New("guest unavailable")
	host := &transportTestTasks{prepare: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}
	guest := &transportTestPeer{prepare: func(context.Context) error { return failure }, resumeErr: failure}
	s := &service{taskManager: host, ioProxyClient: guest}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := s.prepareIOTransport(ctx)
	require.ErrorIs(t, err, failure)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, s.resumeIOTransport(ctx), failure)
	require.True(t, host.resumed, "failed guest RPC must not leave host reads gated")
	require.True(t, guest.resumed)
}

func TestTransportRejectsInvalidWireRegistries(t *testing.T) {
	stream := func(name, id string, sender bool) *ioproxy.TransportStream {
		return &ioproxy.TransportStream{Name: name, Sender: sender, Cursor: &ioproxy.TransportCursor{StreamID: id, SessionID: "session"}}
	}
	for _, processes := range [][]*ioproxy.TransportProcess{
		{nil},
		{{TaskID: "../escape"}},
		{{TaskID: "task"}, {TaskID: "task"}},
		{{TaskID: "task", Streams: []*ioproxy.TransportStream{nil}}},
		{{TaskID: "task", Streams: []*ioproxy.TransportStream{stream(transportStdout, "another//stdout", true)}}},
		{{TaskID: "task", Streams: []*ioproxy.TransportStream{stream(transportStdout, "task//stdout", false)}}},
		{{TaskID: "task", Streams: []*ioproxy.TransportStream{stream(transportStdout, "task//stdout", true), stream(transportStdout, "task//stdout", true)}}},
	} {
		_, err := transportProcessesFromWire(processes)
		require.Error(t, err)
	}
}
