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

	task "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/containerd/v2/pkg/protobuf/types"
	"github.com/containerd/log"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	ioproxy "github.com/firecracker-microvm/firecracker-containerd/proto/service/ioproxy/ttrpc"
)

// ioProxyHandler implements IOProxyService that exposes the state of
// IOProxy instances.
type ioProxyHandler struct {
	runcService task.TTRPCTaskService
	taskManager vm.TaskManager
}

var _ ioproxy.IOProxyService = &ioProxyHandler{}

// State returns whether the given exec's IOProxy is still open or not.
func (ps *ioProxyHandler) State(_ context.Context, req *ioproxy.StateRequest) (*ioproxy.StateResponse, error) {
	open, err := ps.taskManager.IsProxyOpen(req.ID, req.ExecID)
	if err != nil {
		return nil, err
	}
	return &ioproxy.StateResponse{IsOpen: open}, nil
}

// Prepare gates and drains guest senders. The host prepares its own senders
// while receiver loops remain active; Describe follows both successful drains.
func (ps *ioProxyHandler) Prepare(ctx context.Context, _ *types.Empty) (*types.Empty, error) {
	if err := ps.taskManager.PrepareIO(ctx); err != nil {
		return nil, err
	}
	return &types.Empty{}, nil
}

// Describe returns clean process cursors after both sides completed Prepare.
func (ps *ioProxyHandler) Describe(_ context.Context, _ *types.Empty) (*ioproxy.DescribeResponse, error) {
	processes, err := ps.taskManager.DescribeIO()
	if err != nil {
		return nil, err
	}
	response := &ioproxy.DescribeResponse{}
	for _, process := range processes {
		entry := &ioproxy.TransportProcess{TaskID: process.TaskID, ExecID: process.ExecID}
		for _, stream := range process.Streams {
			entry.Streams = append(entry.Streams, &ioproxy.TransportStream{Name: stream.Name, Sender: stream.Sender,
				Cursor: &ioproxy.TransportCursor{StreamID: stream.Cursor.StreamID, SessionID: stream.Cursor.SessionID, Offset: stream.Cursor.Offset, FIN: stream.Cursor.FIN}})
		}
		response.Processes = append(response.Processes, entry)
	}
	return response, nil
}

// Resume releases guest read gates after successful recovery or preparation abort.
func (ps *ioProxyHandler) Resume(_ context.Context, _ *types.Empty) (*types.Empty, error) {
	ps.taskManager.ResumeIO()
	return &types.Empty{}, nil
}

// Attach a new IOProxy to the given exec.
func (ps *ioProxyHandler) Attach(ctx context.Context, req *ioproxy.AttachRequest) (*types.Empty, error) {
	state, err := ps.runcService.State(ctx, &task.StateRequest{ID: req.ID, ExecID: req.ExecID})
	if err != nil {
		return nil, err
	}

	logger := log.G(ctx).WithField("TaskID", req.ID).WithField("ExecID", req.ExecID)

	var proxy vm.IOProxy
	if vm.IsAgentOnlyIO(state.Stdout, logger) {
		proxy = vm.NewNullIOProxy()
	} else {
		proxy = vm.NewIOConnectorProxy(
			vm.InputPair(req.StdinPort, state.Stdin),
			vm.OutputPair(state.Stdout, req.StdoutPort),
			vm.OutputPair(state.Stderr, req.StderrPort),
		)
	}

	streams := make([]vm.TransportStreamIO, 0, len(req.Streams))
	for _, stream := range req.Streams {
		if stream == nil || stream.Cursor == nil {
			proxy.Close()
			return nil, errors.New("missing captured IO stream cursor")
		}
		streams = append(streams, vm.TransportStreamIO{Name: stream.Name, Sender: stream.Sender,
			Cursor: vm.TransportCursor{StreamID: stream.Cursor.StreamID, SessionID: stream.Cursor.SessionID, Offset: stream.Cursor.Offset, FIN: stream.Cursor.FIN}})
	}
	if err := vm.SetTransportCursors(proxy, streams); err != nil {
		proxy.Close()
		return nil, err
	}

	err = ps.taskManager.AttachIO(ctx, req.ID, req.ExecID, proxy)
	if err != nil {
		return nil, err
	}

	return &types.Empty{}, nil
}
