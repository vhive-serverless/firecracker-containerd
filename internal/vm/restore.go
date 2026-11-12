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

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/containerd/api/types/task"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RestoredProcess identifies an existing process in captured guest memory.
// Task and exec identifiers remain guest identifiers; the restore coordinator
// separately owns any mapping to new containerd or Kubernetes identities.
type RestoredProcess struct {
	TaskID string
	ExecID string
	PID    uint32
}

// AdoptProcess takes ownership of proxy, including on failure. Initial task
// processes must be adopted before their execs. A new host manager gets fresh
// lifetime contexts; no Create, Exec, Start or Delete request reaches the guest.
func (m *taskManager) AdoptProcess(ctx context.Context, saved RestoredProcess, backend taskAPI.TTRPCTaskService, proxy IOProxy) (_ *taskAPI.StateResponse, err error) {
	defer func() {
		if err != nil {
			proxy.Close()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if saved.TaskID == "" || saved.PID == 0 {
		return nil, status.Error(codes.InvalidArgument, "restored task ID and PID are required")
	}
	state, err := backend.State(ctx, &taskAPI.StateRequest{ID: saved.TaskID, ExecID: saved.ExecID})
	if err != nil {
		return nil, err
	}
	stateID := saved.TaskID
	if saved.ExecID != "" {
		stateID = saved.ExecID
	}
	if state == nil || state.ID != stateID || state.Pid != saved.PID || state.Status == task.Status_UNKNOWN {
		return nil, status.Error(codes.FailedPrecondition, "restored guest process does not match captured task identity and PID")
	}
	proc, err := m.newProc(saved.TaskID, saved.ExecID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			proc.cancel()
			_, _ = m.deleteProc(saved.TaskID, saved.ExecID)
		}
	}()
	proc.proxy = proxy
	initDone, copyDone := proxy.start(proc)
	proc.ioCopyDone = copyDone
	select {
	case err = <-initDone:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	go m.monitorExit(proc, backend)
	go monitorIO(copyDone, proc, proxy)
	return state, nil
}
