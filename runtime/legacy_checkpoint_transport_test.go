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

	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/protobuf/types"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	ioproxy "github.com/firecracker-microvm/firecracker-containerd/proto/service/ioproxy/ttrpc"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type sourceReconnectGuest struct {
	ioproxy.IOProxyService
	attach func(*ioproxy.AttachRequest) error
}

func (g sourceReconnectGuest) Attach(_ context.Context, request *ioproxy.AttachRequest) (*types.Empty, error) {
	return &types.Empty{}, g.attach(request)
}

type sourceReconnectTasks struct {
	vm.TaskManager
	attach func(string, string, vm.IOProxy) error
}

func (m sourceReconnectTasks) AttachIO(_ context.Context, taskID, execID string, proxy vm.IOProxy) error {
	defer proxy.Close()
	return m.attach(taskID, execID, proxy)
}

func TestReconnectSourceIOPreservesProcessRegistry(t *testing.T) {
	logger := logrus.NewEntry(logrus.New())
	cut := &checkpointTransportCut{Version: 1, Processes: []vm.TransportProcessIO{
		{TaskID: "task", Streams: []vm.TransportStreamIO{{Name: "stdout", Sender: true, Cursor: vm.TransportCursor{StreamID: "task//stdout", SessionID: "restored-source", Offset: 17}}}},
		{TaskID: "task", ExecID: "exec", Streams: []vm.TransportStreamIO{{Name: "stdout", Sender: true, Cursor: vm.TransportCursor{StreamID: "task/exec/stdout", SessionID: "restored-source", Offset: 23, FIN: true}}}},
		{TaskID: "agent-only"},
	}}
	var order []string
	ports := make(map[uint32]bool)
	guest := sourceReconnectGuest{attach: func(request *ioproxy.AttachRequest) error {
		order = append(order, "guest/"+request.ID+"/"+request.ExecID)
		require.Empty(t, request.Streams, "source reconnect must retain current session/cursors rather than installing a clone identity")
		for _, port := range []uint32{request.StdinPort, request.StdoutPort, request.StderrPort} {
			require.NotZero(t, port)
			require.False(t, ports[port], "each process must receive fresh listener ports")
			ports[port] = true
		}
		return nil
	}}
	manager := sourceReconnectTasks{attach: func(taskID, execID string, proxy vm.IOProxy) error {
		order = append(order, "host/"+taskID+"/"+execID)
		_, ok := proxy.(vm.CheckpointIOProxy)
		require.True(t, ok)
		return nil
	}}
	s := &service{logger: logger, jailer: newNoopJailer(context.Background(), logger, vm.Dir(t.TempDir())), ioProxyClient: guest, taskManager: manager,
		fifos: map[string]map[string]cio.Config{"task": {
			"":     {Stdout: "host-init-output"},
			"exec": {Stdout: "host-exec-output"},
		}},
	}
	require.NoError(t, s.reconnectSourceIO(context.Background(), cut))
	require.Equal(t, []string{"guest/task/", "host/task/", "guest/task/exec", "host/task/exec"}, order)
	require.Equal(t, "restored-source", cut.Processes[0].Streams[0].Cursor.SessionID)
	require.True(t, cut.Processes[1].Streams[0].Cursor.FIN)
}

func TestReconnectSourceIORejectsMissingCutOrFIFO(t *testing.T) {
	s := &service{}
	require.ErrorContains(t, s.reconnectSourceIO(context.Background(), nil), "prepared transport cut")
	cut := &checkpointTransportCut{Processes: []vm.TransportProcessIO{{TaskID: "task", Streams: []vm.TransportStreamIO{{Name: "stdout"}}}}}
	require.ErrorContains(t, s.reconnectSourceIO(context.Background(), cut), "missing FIFOs for task/")
}

func TestReconnectSourceIOPropagatesAttachFailure(t *testing.T) {
	for _, side := range []string{"guest", "host"} {
		t.Run(side, func(t *testing.T) {
			failure := errors.New("connection replacement failed")
			logger := logrus.NewEntry(logrus.New())
			guest := sourceReconnectGuest{attach: func(*ioproxy.AttachRequest) error {
				if side == "guest" {
					return failure
				}
				return nil
			}}
			manager := sourceReconnectTasks{attach: func(string, string, vm.IOProxy) error {
				require.Equal(t, "host", side, "host attach must not run after guest failure")
				return failure
			}}
			s := &service{logger: logger, jailer: newNoopJailer(context.Background(), logger, vm.Dir(t.TempDir())), ioProxyClient: guest, taskManager: manager,
				fifos: map[string]map[string]cio.Config{"task": {"": {Stdout: "host-output"}}},
			}
			cut := &checkpointTransportCut{Processes: []vm.TransportProcessIO{{TaskID: "task", Streams: []vm.TransportStreamIO{{Name: "stdout"}}}}}
			err := s.reconnectSourceIO(context.Background(), cut)
			require.ErrorIs(t, err, failure)
			require.ErrorContains(t, err, "reconnect source IO task/")
		})
	}
}
