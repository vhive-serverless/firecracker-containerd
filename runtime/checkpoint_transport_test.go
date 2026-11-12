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
	"testing"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCheckpointTransportRejectsInvalidVersionAndThawSet(t *testing.T) {
	valid := func() *checkpointTransportCut {
		return &checkpointTransportCut{Version: 1, Processes: []vm.TransportProcessIO{{TaskID: "task"}, {TaskID: "task", ExecID: "exec"}}, ResumeTasks: []string{"task"}}
	}
	require.NoError(t, validateCheckpointTransport(valid()))
	require.Error(t, validateCheckpointTransport(nil))
	for _, mutate := range []func(*checkpointTransportCut){
		func(c *checkpointTransportCut) { c.Version = 2 },
		func(c *checkpointTransportCut) { c.Processes = append(c.Processes, c.Processes[0]) },
		func(c *checkpointTransportCut) { c.ResumeTasks = []string{"missing"} },
		func(c *checkpointTransportCut) { c.ResumeTasks = []string{"task", "task"} },
		func(c *checkpointTransportCut) { c.Processes = c.Processes[1:] },
	} {
		cut := valid()
		mutate(cut)
		require.Error(t, validateCheckpointTransport(cut))
	}
}

func TestSealTransportPersistsAcknowledgedEventCursor(t *testing.T) {
	fixture := newAgentEventTestFixture(t, newAgentEventTestPublisher(nil))
	s := fixture.service
	noError := func(context.Context) error { return nil }
	s.taskManager = &transportTestTasks{prepare: noError}
	s.ioProxyClient = &transportTestPeer{prepare: noError}
	fixture.publish(t, "before-capture")
	require.NoError(t, s.resumeEventForwarding(fixture.ctx))
	fixture.waitAck(t, 1)
	cut, err := s.sealIOTransport(fixture.ctx)
	require.NoError(t, err)
	require.NoError(t, validateCheckpointTransport(cut))
	require.Equal(t, uint64(1), cut.EventCursor)
	require.Equal(t, cut.EventCursor, fixture.journal.Cursor())
	s.eventMu.Lock()
	require.Nil(t, s.eventCancel, "capture must stop event publication before vCPU pause")
	s.eventMu.Unlock()
}

func TestLegacyPauseBlocksTaskAdmission(t *testing.T) {
	s := &service{pendingPause: &pendingVMPause{}}
	ctx := context.Background()
	_, err := s.Create(ctx, &taskAPI.CreateTaskRequest{})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	_, err = s.Exec(ctx, &taskAPI.ExecProcessRequest{})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	_, err = s.Start(ctx, &taskAPI.StartRequest{})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	_, err = s.Delete(ctx, &taskAPI.DeleteRequest{})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestSnapshotTransitionRespectsCancellation(t *testing.T) {
	s := &service{}
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, s.lockSnapshotTransition(ctx), context.Canceled)
}
