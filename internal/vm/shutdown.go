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
	"errors"
	"fmt"
	"sort"
	"syscall"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/errdefs"
	"github.com/containerd/errdefs/pkg/errgrpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (m *taskManager) Shutdown(ctx context.Context, backend taskAPI.TTRPCTaskService) error {
	m.mu.Lock()
	m.isShutdown = true
	// Keep the admitted entries even if a failed Create removes itself while
	// we wait: the backend may have created a process before IO setup failed.
	if m.shutdownProcesses == nil {
		m.shutdownProcesses = make([]*vmProc, 0)
		for _, task := range m.tasks {
			for _, proc := range task {
				m.shutdownProcesses = append(m.shutdownProcesses, proc)
			}
		}
	}
	// Retain the first cut across deadline/retry, including a failed admitted
	// Create that may have created its backend before IO initialization failed.
	processes := append([]*vmProc(nil), m.shutdownProcesses...)
	starts := make([]<-chan struct{}, 0, len(m.starts))
	for done := range m.starts {
		starts = append(starts, done)
	}
	for done := range m.deletes {
		starts = append(starts, done)
	}
	m.mu.Unlock()
	for _, done := range starts {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for _, proc := range processes {
		select {
		case <-proc.admissionDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	sort.Slice(processes, func(i, j int) bool {
		if processes[i].taskID != processes[j].taskID {
			return processes[i].taskID < processes[j].taskID
		}
		return processes[i].execID < processes[j].execID
	})
	killed := make(map[string]bool)
	var result error
	for _, proc := range processes {
		if killed[proc.taskID] {
			continue
		}
		killed[proc.taskID] = true
		_, err := backend.Kill(ctx, &taskAPI.KillRequest{ID: proc.taskID, Signal: uint32(syscall.SIGKILL), All: true})
		native := errgrpc.ToNative(err)
		// A raced exit or an already deleted task is safe; Wait below still
		// confirms every surviving registered process, including execs.
		if native != nil && !errdefs.IsNotFound(native) && !errdefs.IsFailedPrecondition(native) {
			result = errors.Join(result, fmt.Errorf("kill task %s during shutdown: %w", proc.taskID, err))
		}
	}
	if result != nil {
		return result
	}
	for _, proc := range processes {
		state, err := backend.State(ctx, &taskAPI.StateRequest{ID: proc.taskID, ExecID: proc.execID})
		gone := errdefs.IsNotFound(errgrpc.ToNative(err))
		if err != nil && !gone {
			return fmt.Errorf("inspect task %s/%s during shutdown: %w", proc.taskID, proc.execID, err)
		}
		// An exec created but never started has no OS process to wait for.
		if !gone {
			if state.Pid == 0 {
				// A never-started exec has no output producer. Release its
				// unused proxy rather than waiting for an EOF that cannot occur.
				proc.cancel()
				proc.proxy.Close()
				select {
				case <-proc.ioCopyDone:
				case <-ctx.Done():
					return ctx.Err()
				}
				continue
			}
			_, err = backend.Wait(ctx, &taskAPI.WaitRequest{ID: proc.taskID, ExecID: proc.execID})
			if err != nil && !errdefs.IsNotFound(errgrpc.ToNative(err)) {
				return fmt.Errorf("wait task %s/%s during shutdown: %w", proc.taskID, proc.execID, err)
			}
		}
		// monitorExit normally cancels this lifetime. Do so here as well after
		// confirmed exit so input stops while outstanding output copies finish.
		proc.cancel()
		select {
		case <-proc.ioCopyDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func (m *taskManager) StartProcess(ctx context.Context, req *taskAPI.StartRequest, backend taskAPI.TTRPCTaskService) (*taskAPI.StartResponse, error) {
	m.mu.Lock()
	if m.isShutdown {
		m.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "cannot start a process after task manager shutdown")
	}
	proc, err := m.findProc(req.ID, req.ExecID)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	done := make(chan struct{})
	m.starts[done] = struct{}{}
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.starts, done); close(done); m.mu.Unlock() }()
	select {
	case <-proc.admissionDone:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return backend.Start(ctx, req)
}
