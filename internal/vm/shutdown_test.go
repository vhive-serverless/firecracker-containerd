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
	"sync"
	"syscall"
	"testing"
	"time"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	tasktypes "github.com/containerd/containerd/api/types/task"
	"github.com/containerd/containerd/v2/pkg/protobuf/types"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type shutdownBackend struct {
	taskAPI.TTRPCTaskService
	mu                           sync.Mutex
	processes                    map[string]chan struct{}
	kills                        []*taskAPI.KillRequest
	waitStarted                  chan struct{}
	createEntered, createRelease chan struct{}
	execEntered, execRelease     chan struct{}
	startEntered, startRelease   chan struct{}
	pidZero                      map[string]bool
	killErr                      error
	waitRelease                  <-chan struct{}
}

func newShutdownBackend() *shutdownBackend {
	return &shutdownBackend{processes: make(map[string]chan struct{}), waitStarted: make(chan struct{}, 128)}
}
func (b *shutdownBackend) add(id, exec string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.processes[id+"/"+exec] = make(chan struct{})
}
func (b *shutdownBackend) Create(ctx context.Context, req *taskAPI.CreateTaskRequest) (*taskAPI.CreateTaskResponse, error) {
	if b.createEntered != nil {
		close(b.createEntered)
		select {
		case <-b.createRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	b.add(req.ID, "")
	return &taskAPI.CreateTaskResponse{Pid: 123}, nil
}
func (b *shutdownBackend) Exec(ctx context.Context, req *taskAPI.ExecProcessRequest) (*types.Empty, error) {
	if b.execEntered != nil {
		close(b.execEntered)
		select {
		case <-b.execRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	b.add(req.ID, req.ExecID)
	return &types.Empty{}, nil
}
func (b *shutdownBackend) State(_ context.Context, req *taskAPI.StateRequest) (*taskAPI.StateResponse, error) {
	b.mu.Lock()
	_, ok := b.processes[req.ID+"/"+req.ExecID]
	pidZero := b.pidZero[req.ID+"/"+req.ExecID]
	b.mu.Unlock()
	if !ok {
		return nil, status.Error(codes.NotFound, "gone")
	}
	id := req.ID
	if req.ExecID != "" {
		id = req.ExecID
	}
	pid := uint32(123)
	if pidZero {
		pid = 0
	}
	return &taskAPI.StateResponse{ID: id, Pid: pid, Status: tasktypes.Status_RUNNING}, nil
}
func (b *shutdownBackend) Wait(ctx context.Context, req *taskAPI.WaitRequest) (*taskAPI.WaitResponse, error) {
	b.mu.Lock()
	done, ok := b.processes[req.ID+"/"+req.ExecID]
	b.mu.Unlock()
	if !ok {
		return nil, status.Error(codes.NotFound, "gone")
	}
	b.waitStarted <- struct{}{}
	select {
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if b.waitRelease != nil {
		select {
		case <-b.waitRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &taskAPI.WaitResponse{ExitStatus: 137}, nil
}
func (b *shutdownBackend) Kill(_ context.Context, req *taskAPI.KillRequest) (*types.Empty, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.kills = append(b.kills, req)
	if b.killErr != nil {
		return nil, b.killErr
	}
	for key, done := range b.processes {
		if len(key) > len(req.ID) && key[:len(req.ID)+1] == req.ID+"/" {
			select {
			case <-done:
			default:
				close(done)
			}
		}
	}
	return &types.Empty{}, nil
}
func (b *shutdownBackend) killCount() int { b.mu.Lock(); defer b.mu.Unlock(); return len(b.kills) }

func TestTaskShutdownKillsGroupsAndWaitsForExecs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	manager := NewTaskManager(ctx, logrus.NewEntry(logrus.New()))
	backend := newShutdownBackend()
	_, err := manager.CreateTask(ctx, &taskAPI.CreateTaskRequest{ID: "captured"}, backend, NewNullIOProxy())
	require.NoError(t, err)
	_, err = manager.ExecProcess(ctx, &taskAPI.ExecProcessRequest{ID: "captured", ExecID: "worker"}, backend, NewNullIOProxy())
	require.NoError(t, err)
	release := make(chan struct{})
	backend.waitRelease = release
	done := make(chan error, 1)
	go func() { done <- manager.Shutdown(ctx, backend) }()
	require.Eventually(t, func() bool { return backend.killCount() == 1 }, time.Second, time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("shutdown returned before confirmed exits: %v", err)
	case <-time.After(15 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-done)
	backend.mu.Lock()
	require.Len(t, backend.kills, 1)
	require.True(t, backend.kills[0].All)
	require.Empty(t, backend.kills[0].ExecID)
	require.Equal(t, uint32(syscall.SIGKILL), backend.kills[0].Signal)
	backend.mu.Unlock()
	_, err = manager.CreateTask(ctx, &taskAPI.CreateTaskRequest{ID: "late"}, backend, NewNullIOProxy())
	require.Error(t, err)
	_, err = manager.ExecProcess(ctx, &taskAPI.ExecProcessRequest{ID: "captured", ExecID: "late"}, backend, NewNullIOProxy())
	require.Error(t, err)
	require.Error(t, manager.AttachIO(ctx, "captured", "", NewNullIOProxy()))
}

func TestTaskShutdownWaitsForAdmittedCreateAndExec(t *testing.T) {
	for _, exec := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "exec"}[exec], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			manager := NewTaskManager(ctx, logrus.NewEntry(logrus.New())).(*taskManager)
			backend := newShutdownBackend()
			entered, release := make(chan struct{}), make(chan struct{})
			if exec {
				_, err := manager.CreateTask(ctx, &taskAPI.CreateTaskRequest{ID: "pending"}, backend, NewNullIOProxy())
				require.NoError(t, err)
				backend.execEntered = entered
				backend.execRelease = release
			} else {
				backend.createEntered = entered
				backend.createRelease = release
			}
			admitted := make(chan error, 1)
			go func() {
				var err error
				if exec {
					_, err = manager.ExecProcess(ctx, &taskAPI.ExecProcessRequest{ID: "pending", ExecID: "worker"}, backend, NewNullIOProxy())
				} else {
					_, err = manager.CreateTask(ctx, &taskAPI.CreateTaskRequest{ID: "pending"}, backend, NewNullIOProxy())
				}
				admitted <- err
			}()
			<-entered
			stopped := make(chan error, 1)
			go func() { stopped <- manager.Shutdown(ctx, backend) }()
			require.Eventually(t, func() bool { manager.mu.Lock(); defer manager.mu.Unlock(); return manager.isShutdown }, time.Second, time.Millisecond)
			require.Equal(t, 0, backend.killCount(), "backend creation must finish before kill enumeration")
			close(release)
			require.NoError(t, <-admitted)
			require.NoError(t, <-stopped)
			require.Equal(t, 1, backend.killCount())
		})
	}
}

func TestTaskShutdownHonorsDeadlineAndRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	manager := NewTaskManager(ctx, logrus.NewEntry(logrus.New()))
	backend := newShutdownBackend()
	_, err := manager.CreateTask(ctx, &taskAPI.CreateTaskRequest{ID: "running"}, backend, NewNullIOProxy())
	require.NoError(t, err)
	backend.mu.Lock()
	backend.killErr = status.Error(codes.PermissionDenied, "cannot signal")
	backend.mu.Unlock()
	require.ErrorContains(t, manager.Shutdown(ctx, backend), "cannot signal")
	backend.mu.Lock()
	backend.killErr = nil
	backend.mu.Unlock()
	release := make(chan struct{})
	backend.waitRelease = release
	deadline, stop := context.WithTimeout(ctx, 15*time.Millisecond)
	defer stop()
	require.ErrorIs(t, manager.Shutdown(deadline, backend), context.DeadlineExceeded)
	close(release)
	require.NoError(t, manager.Shutdown(ctx, backend))
	require.Error(t, manager.Shutdown(contextWithCanceled(), backend))
}
func contextWithCanceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func (b *shutdownBackend) Start(ctx context.Context, _ *taskAPI.StartRequest) (*taskAPI.StartResponse, error) {
	if b.startEntered != nil {
		close(b.startEntered)
		select {
		case <-b.startRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &taskAPI.StartResponse{Pid: 123}, nil
}
func TestTaskShutdownWaitsForAdmittedStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	manager := NewTaskManager(ctx, logrus.NewEntry(logrus.New())).(*taskManager)
	backend := newShutdownBackend()
	_, err := manager.CreateTask(ctx, &taskAPI.CreateTaskRequest{ID: "pending"}, backend, NewNullIOProxy())
	require.NoError(t, err)
	backend.startEntered = make(chan struct{})
	backend.startRelease = make(chan struct{})
	started := make(chan error, 1)
	go func() {
		_, err := manager.StartProcess(ctx, &taskAPI.StartRequest{ID: "pending"}, backend)
		started <- err
	}()
	<-backend.startEntered
	stopped := make(chan error, 1)
	go func() { stopped <- manager.Shutdown(ctx, backend) }()
	require.Eventually(t, func() bool { manager.mu.Lock(); defer manager.mu.Unlock(); return manager.isShutdown }, time.Second, time.Millisecond)
	require.Equal(t, 0, backend.killCount())
	_, err = manager.StartProcess(ctx, &taskAPI.StartRequest{ID: "pending"}, backend)
	require.Error(t, err)
	close(backend.startRelease)
	require.NoError(t, <-started)
	require.NoError(t, <-stopped)
}

type shutdownOutputProxy struct {
	done                  chan error
	closed                chan struct{}
	finishOnce, closeOnce sync.Once
}

func newShutdownOutputProxy() *shutdownOutputProxy {
	return &shutdownOutputProxy{done: make(chan error), closed: make(chan struct{})}
}
func (p *shutdownOutputProxy) start(*vmProc) (<-chan error, <-chan error) {
	ready := make(chan error)
	close(ready)
	return ready, p.done
}
func (p *shutdownOutputProxy) finish() { p.finishOnce.Do(func() { close(p.done) }) }
func (p *shutdownOutputProxy) Close()  { p.closeOnce.Do(func() { close(p.closed) }); p.finish() }
func (p *shutdownOutputProxy) IsOpen() bool {
	select {
	case <-p.closed:
		return false
	default:
		return true
	}
}

func TestTaskShutdownDrainsOutputAfterBackendDeleted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	manager := NewTaskManager(ctx, logrus.NewEntry(logrus.New()))
	backend := newShutdownBackend()
	proxy := newShutdownOutputProxy()
	_, err := manager.CreateTask(ctx, &taskAPI.CreateTaskRequest{ID: "deleted"}, backend, proxy)
	require.NoError(t, err)
	backend.mu.Lock()
	close(backend.processes["deleted/"])
	delete(backend.processes, "deleted/")
	backend.killErr = status.Error(codes.NotFound, "gone")
	backend.mu.Unlock()
	deadline, stop := context.WithTimeout(ctx, 15*time.Millisecond)
	defer stop()
	require.ErrorIs(t, manager.Shutdown(deadline, backend), context.DeadlineExceeded, "missing backend must not bypass pending output")
	proxy.finish()
	require.NoError(t, manager.Shutdown(ctx, backend))
}
func TestTaskShutdownClosesNeverStartedExecProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	manager := NewTaskManager(ctx, logrus.NewEntry(logrus.New()))
	backend := newShutdownBackend()
	proxy := newShutdownOutputProxy()
	_, err := manager.CreateTask(ctx, &taskAPI.CreateTaskRequest{ID: "captured"}, backend, NewNullIOProxy())
	require.NoError(t, err)
	_, err = manager.ExecProcess(ctx, &taskAPI.ExecProcessRequest{ID: "captured", ExecID: "never-started"}, backend, proxy)
	require.NoError(t, err)
	backend.pidZero = map[string]bool{"captured/never-started": true}
	require.NoError(t, manager.Shutdown(ctx, backend))
	require.False(t, proxy.IsOpen())
	select {
	case <-proxy.done:
	default:
		t.Fatal("unused proxy did not finish")
	}
}

type failedAdmissionProxy struct{ *shutdownOutputProxy }

func (p *failedAdmissionProxy) start(*vmProc) (<-chan error, <-chan error) {
	ready := make(chan error, 1)
	ready <- status.Error(codes.Internal, "IO setup failed after Create")
	close(ready)
	return ready, p.done
}

func TestTaskShutdownRetainsFailedAdmissionAcrossDeadlineRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	manager := NewTaskManager(ctx, logrus.NewEntry(logrus.New()))
	backend := newShutdownBackend()
	backend.createEntered = make(chan struct{})
	backend.createRelease = make(chan struct{})
	created := make(chan error, 1)
	go func() {
		_, err := manager.CreateTask(ctx, &taskAPI.CreateTaskRequest{ID: "partial"}, backend, &failedAdmissionProxy{newShutdownOutputProxy()})
		created <- err
	}()
	<-backend.createEntered
	deadline, stop := context.WithTimeout(ctx, 15*time.Millisecond)
	defer stop()
	require.ErrorIs(t, manager.Shutdown(deadline, backend), context.DeadlineExceeded)
	close(backend.createRelease)
	require.ErrorContains(t, <-created, "IO setup failed")
	require.NoError(t, manager.Shutdown(ctx, backend), "retry must retain backend identity even though failed Create removed its map entry")
	require.Equal(t, 1, backend.killCount())
}

func (b *shutdownBackend) Delete(_ context.Context, req *taskAPI.DeleteRequest) (*taskAPI.DeleteResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := req.ID + "/" + req.ExecID
	if done, ok := b.processes[key]; ok {
		select {
		case <-done:
		default:
			close(done)
		}
		delete(b.processes, key)
	}
	return &taskAPI.DeleteResponse{}, nil
}
func TestTaskShutdownWaitsForConcurrentDeleteOutputDrain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	manager := NewTaskManager(ctx, logrus.NewEntry(logrus.New())).(*taskManager)
	backend := newShutdownBackend()
	proxy := newShutdownOutputProxy()
	_, err := manager.CreateTask(ctx, &taskAPI.CreateTaskRequest{ID: "deleting"}, backend, proxy)
	require.NoError(t, err)
	deleted := make(chan error, 1)
	go func() {
		_, err := manager.DeleteProcess(ctx, &taskAPI.DeleteRequest{ID: "deleting"}, backend)
		deleted <- err
	}()
	require.Eventually(t, func() bool { manager.mu.Lock(); defer manager.mu.Unlock(); return len(manager.tasks) == 0 }, time.Second, time.Millisecond)
	deadline, stop := context.WithTimeout(ctx, 15*time.Millisecond)
	defer stop()
	require.ErrorIs(t, manager.Shutdown(deadline, backend), context.DeadlineExceeded, "removing registry entry must not bypass in-flight Delete's output drain")
	proxy.finish()
	require.NoError(t, <-deleted)
	require.NoError(t, manager.Shutdown(ctx, backend))
}
