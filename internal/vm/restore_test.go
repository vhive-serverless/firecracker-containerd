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
	"testing"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/containerd/api/types/task"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type restoredTaskBackend struct {
	mockTaskService
	stateIDOverride string
}

func (b *restoredTaskBackend) State(ctx context.Context, req *taskAPI.StateRequest) (*taskAPI.StateResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pid := uint32(41)
	id := req.ID
	if req.ExecID != "" {
		pid = 42
		id = req.ExecID
	}
	if b.stateIDOverride != "" {
		id = b.stateIDOverride
	}
	return &taskAPI.StateResponse{ID: id, Pid: pid, Status: task.Status_PAUSED}, nil
}

func TestAdoptProcessPreservesGuestTaskAndExec(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	backend := &restoredTaskBackend{}
	manager := NewTaskManager(ctx, logrus.NewEntry(logrus.New()))
	for _, saved := range []RestoredProcess{{TaskID: "captured", PID: 41}, {TaskID: "captured", ExecID: "exec", PID: 42}} {
		wait := make(chan struct{})
		backend.SetWaitCh(saved.TaskID, saved.ExecID, wait)
		t.Cleanup(func() { close(wait) })
		state, err := manager.AdoptProcess(ctx, saved, backend, NewNullIOProxy())
		require.NoError(t, err)
		require.Equal(t, task.Status_PAUSED, state.Status, "adoption must not resume the workload")
		require.Equal(t, saved.PID, state.Pid)
	}
	require.Empty(t, backend.PopCreateRequests("captured"))
	require.Empty(t, backend.PopExecRequests("captured"))
	require.False(t, manager.ShutdownIfEmpty())
	_, err := manager.DeleteProcess(ctx, &taskAPI.DeleteRequest{ID: "captured", ExecID: "exec"}, backend)
	require.NoError(t, err)
	_, err = manager.DeleteProcess(ctx, &taskAPI.DeleteRequest{ID: "captured"}, backend)
	require.NoError(t, err)
	require.True(t, manager.ShutdownIfEmpty(), "adopted processes must participate in normal lifecycle cleanup")
}

func TestAdoptProcessRejectsWrongIdentityAndMissingParent(t *testing.T) {
	ctx := context.Background()
	backend := &restoredTaskBackend{}
	manager := NewTaskManager(ctx, logrus.NewEntry(logrus.New()))
	_, err := manager.AdoptProcess(ctx, RestoredProcess{TaskID: "captured", PID: 99}, backend, NewNullIOProxy())
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	_, err = manager.AdoptProcess(ctx, RestoredProcess{TaskID: "captured", ExecID: "exec", PID: 42}, backend, NewNullIOProxy())
	require.Error(t, err)
	require.True(t, manager.ShutdownIfEmpty(), "failed adoption must leave no host task ownership")
	require.Empty(t, backend.PopCreateRequests("captured"))
	require.Empty(t, backend.PopExecRequests("captured"))
	require.Empty(t, backend.PopDeleteRequests("captured"), "failed adoption must not delete a guest process")
}

func TestAdoptProcessRejectsSubstitutedProcessIdentity(t *testing.T) {
	for _, saved := range []RestoredProcess{{TaskID: "captured", PID: 41}, {TaskID: "captured", ExecID: "exec", PID: 42}} {
		t.Run("exec="+saved.ExecID, func(t *testing.T) {
			backend := &restoredTaskBackend{stateIDOverride: "exec"}
			if saved.ExecID != "" {
				backend.stateIDOverride = saved.TaskID
			}
			manager := NewTaskManager(context.Background(), logrus.NewEntry(logrus.New()))
			_, err := manager.AdoptProcess(context.Background(), saved, backend, NewNullIOProxy())
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.True(t, manager.ShutdownIfEmpty())
		})
	}
}
