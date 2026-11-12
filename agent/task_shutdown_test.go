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
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/api/events"
	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	tasktypes "github.com/containerd/containerd/api/types/task"
	coreevents "github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/pkg/protobuf/types"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type shutdownTaskBackend struct {
	taskAPI.TTRPCTaskService
	exit   chan struct{}
	once   sync.Once
	killed chan *taskAPI.KillRequest
}

func (b *shutdownTaskBackend) Create(context.Context, *taskAPI.CreateTaskRequest) (*taskAPI.CreateTaskResponse, error) {
	return &taskAPI.CreateTaskResponse{Pid: 99}, nil
}
func (b *shutdownTaskBackend) State(context.Context, *taskAPI.StateRequest) (*taskAPI.StateResponse, error) {
	return &taskAPI.StateResponse{ID: "captured", Pid: 99, Status: tasktypes.Status_RUNNING}, nil
}
func (b *shutdownTaskBackend) Kill(_ context.Context, req *taskAPI.KillRequest) (*types.Empty, error) {
	b.killed <- req
	b.once.Do(func() { close(b.exit) })
	return &types.Empty{}, nil
}
func (b *shutdownTaskBackend) Wait(ctx context.Context, _ *taskAPI.WaitRequest) (*taskAPI.WaitResponse, error) {
	select {
	case <-b.exit:
		return &taskAPI.WaitResponse{ExitStatus: 137}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type shutdownExitSink struct{ entered, release chan struct{} }

func (s *shutdownExitSink) Publish(ctx context.Context, _ string, _ coreevents.Event) error {
	close(s.entered)
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*shutdownExitSink) Close() error { return nil }

func TestAgentShutdownWaitsForTaskExitPublicationBeforeCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	shimCtx, cancelShim := context.WithCancel(context.Background())
	defer cancelShim()
	backend := &shutdownTaskBackend{exit: make(chan struct{}), killed: make(chan *taskAPI.KillRequest, 1)}
	manager := vm.NewTaskManager(shimCtx, logrus.NewEntry(logrus.New()))
	_, err := manager.CreateTask(ctx, &taskAPI.CreateTaskRequest{ID: "captured"}, backend, vm.NewNullIOProxy())
	require.NoError(t, err)
	sink := &shutdownExitSink{entered: make(chan struct{}), release: make(chan struct{})}
	publisher := &exitPublisher{Publisher: sink, exits: make(map[string]chan struct{})}
	publisher.begin("captured")
	service := &TaskService{taskManager: manager, runcService: backend, exitPublisher: publisher, shimCancel: cancelShim}
	result := make(chan error, 1)
	go func() {
		_, err := service.Shutdown(ctx, &taskAPI.ShutdownRequest{ID: "clone", Now: true})
		result <- err
	}()
	select {
	case request := <-backend.killed:
		require.True(t, request.All)
		require.Equal(t, "captured", request.ID)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// An outstanding task wait can observe real exit while agent transport is alive.
	waited, err := backend.Wait(ctx, &taskAPI.WaitRequest{ID: "captured"})
	require.NoError(t, err)
	require.Equal(t, uint32(137), waited.ExitStatus)
	published := make(chan error, 1)
	go func() {
		published <- publisher.Publish(ctx, "/tasks/exit", &events.TaskExit{ID: "captured", ContainerID: "captured", ExitStatus: 137})
	}()
	<-sink.entered
	select {
	case <-shimCtx.Done():
		t.Fatal("agent canceled before its exit publication completed")
	default:
	}
	select {
	case err := <-result:
		t.Fatalf("shutdown passed an unpublished exit: %v", err)
	case <-time.After(15 * time.Millisecond):
	}
	close(sink.release)
	require.NoError(t, <-published)
	require.NoError(t, <-result)
	select {
	case <-shimCtx.Done():
	case <-ctx.Done():
		t.Fatal("successful shutdown did not cancel agent")
	}
}

func TestAgentShutdownEmptyManagerDoesNotNeedEventConsumer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	shimCtx, cancelShim := context.WithCancel(context.Background())
	defer cancelShim()
	service := &TaskService{taskManager: vm.NewTaskManager(shimCtx, logrus.NewEntry(logrus.New())), exitPublisher: &exitPublisher{exits: make(map[string]chan struct{})}, shimCancel: cancelShim}
	_, err := service.Shutdown(ctx, &taskAPI.ShutdownRequest{Now: true})
	require.NoError(t, err)
	require.ErrorIs(t, shimCtx.Err(), context.Canceled)
	_, err = service.Start(ctx, &taskAPI.StartRequest{ID: "late"})
	require.Error(t, err)
	require.Empty(t, service.exitPublisher.exits, "rejected Start must not add a publication barrier")
}

func TestAgentShutdownPublicationDeadlineDoesNotCancelEarly(t *testing.T) {
	shimCtx, cancelShim := context.WithCancel(context.Background())
	defer cancelShim()
	manager := vm.NewTaskManager(shimCtx, logrus.NewEntry(logrus.New()))
	// A pending real exit publication can outlive an empty manager after a
	// concurrent Delete. It must obey the request deadline without early cancel.
	publisher := &exitPublisher{exits: make(map[string]chan struct{})}
	publisher.begin("pending-exit")
	service := &TaskService{taskManager: manager, exitPublisher: publisher, shimCancel: cancelShim}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	_, err := service.Shutdown(ctx, &taskAPI.ShutdownRequest{Now: true})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, shimCtx.Err())
	publisher.forget("pending-exit")
	_, err = service.Shutdown(context.Background(), &taskAPI.ShutdownRequest{Now: true})
	require.NoError(t, err)
}
