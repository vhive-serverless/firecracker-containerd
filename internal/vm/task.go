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
	"sync"
	"time"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/containerd/v2/pkg/protobuf/types"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// By default, once the task exits, wait defaultIOFlushTimeout for
	// the IO streams to close on their own before forcibly closing them.
	defaultIOFlushTimeout = 5 * time.Second
)

// TaskManager manages a set of tasks, ensuring that creation, deletion and
// io stream handling of the task happen in a synchronized manner. It's intended
// to contain logic common to both the host runtime shim and VM agent (which
// would otherwise be duped between the two).
type TaskManager interface {
	// CreateTask adds a task to be managed. It creates the task using the
	// provided request/TaskService and sets up IO streams using the provided
	// IOConnectorSet.
	// If the TaskManager was shutdown by a previous call to ShutdownIfEmpty, an
	// error will be returned.
	CreateTask(context.Context, *taskAPI.CreateTaskRequest, taskAPI.TTRPCTaskService, IOProxy) (*taskAPI.CreateTaskResponse, error)

	// DeleteProcess removes a task or exec being managed from the TaskManager.
	// It deletes the process from the provided TaskService using the provided
	// request. It also blocks until all IO for the task has been flushed or a
	// hardcoded timeout is hit.
	DeleteProcess(context.Context, *taskAPI.DeleteRequest, taskAPI.TTRPCTaskService) (*taskAPI.DeleteResponse, error)

	// ExecProcess adds an exec process to be managed. It must be an exec for a
	// Task already managed by the TaskManager.
	// If the TaskManager was shutdown by a previous call to ShutdownIfEmpty, an
	// error will be returned.
	ExecProcess(context.Context, *taskAPI.ExecProcessRequest, taskAPI.TTRPCTaskService, IOProxy) (*types.Empty, error)

	// AdoptProcess manages a process already present in a restored guest. It
	// verifies its captured PID and attaches I/O without creating or starting it.
	AdoptProcess(context.Context, RestoredProcess, taskAPI.TTRPCTaskService, IOProxy) (*taskAPI.StateResponse, error)

	// ShutdownIfEmpty will atomically check if there are no tasks left being managed and, if so,
	// prevent any new tasks from being added going forward (future attempts will return an error).
	// If any tasks are still in the TaskManager's map, no change will be made.
	// It returns a bool indicating whether TaskManager shut down as a result of the call.
	ShutdownIfEmpty() bool

	// AttachIO attaches the given IO proxy to a task or exec.
	AttachIO(context.Context, string, string, IOProxy) error

	// IsProxyOpen returns true if the given task or exec has an IO proxy
	// which hasn't been closed.
	IsProxyOpen(string, string) (bool, error)

	// PrepareIO drains locally owned senders; prepare the peer concurrently.
	PrepareIO(context.Context) error
	// DescribeIO returns clean cuts only after both peers completed PrepareIO.
	DescribeIO() ([]TransportProcessIO, error)
	// ResumeIO releases prepared reads or aborts a failed preparation.
	ResumeIO()
}

// NewTaskManager initializes a new TaskManager
func NewTaskManager(shimCtx context.Context, logger *logrus.Entry) TaskManager {
	return &taskManager{
		tasks:   make(map[string]map[string]*vmProc),
		shimCtx: shimCtx,
		logger:  logger,
	}
}

type taskManager struct {
	mu sync.Mutex
	// tasks is a map of taskID to a map of execID->vmProc for that task
	tasks      map[string]map[string]*vmProc
	isShutdown bool

	shimCtx context.Context
	logger  *logrus.Entry
}

type vmProc struct {
	taskID string
	execID string
	logger *logrus.Entry

	// ctx and cancel are tied to the exec.
	ctx    context.Context
	cancel context.CancelFunc

	// ioCopyDone guarantees that the exec's stdout and stderr are copied over,
	// before killing this vmProc.
	ioCopyDone <-chan error

	// proxy is used to copy the exec's stdio.
	proxy IOProxy
}

func (m *taskManager) newProc(taskID, execID string) (*vmProc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.isShutdown {
		return nil, fmt.Errorf("cannot create new exec %q in task %q after shutdown", execID, taskID)
	}

	_, taskExists := m.tasks[taskID]
	if !taskExists {
		if execID != "" {
			return nil, fmt.Errorf("cannot add exec %q to non-existent task %q", execID, taskID)
		}

		m.tasks[taskID] = make(map[string]*vmProc)
	}

	_, procExists := m.tasks[taskID][execID]
	if procExists {
		return nil, status.Errorf(codes.AlreadyExists, "exec %q already exists", execID)
	}

	proc := &vmProc{}
	proc.taskID = taskID
	proc.execID = execID
	proc.logger = m.logger.WithField("TaskID", taskID).WithField("ExecID", execID)
	proc.ctx, proc.cancel = context.WithCancel(m.shimCtx)

	m.tasks[taskID][execID] = proc
	return proc, nil
}

func (m *taskManager) deleteProc(taskID, execID string) (*vmProc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("cannot delete exec %q from non-existent task %q", execID, taskID)
	}

	proc, ok := m.tasks[taskID][execID]
	if !ok {
		return nil, fmt.Errorf("cannot delete non-existent exec %q from task %q", execID, taskID)
	}

	delete(m.tasks[taskID], execID)
	if len(m.tasks[taskID]) == 0 {
		delete(m.tasks, taskID)
	}

	return proc, nil
}

func (m *taskManager) ShutdownIfEmpty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.tasks) == 0 {
		m.isShutdown = true
		return true
	}

	return false
}

func (m *taskManager) CreateTask(
	reqCtx context.Context,
	req *taskAPI.CreateTaskRequest,
	taskService taskAPI.TTRPCTaskService,
	ioProxy IOProxy,
) (_ *taskAPI.CreateTaskResponse, err error) {
	taskID := req.ID
	execID := "" // ExecID of initial process in task is empty string by containerd convention

	proc, err := m.newProc(taskID, execID)
	if err != nil {
		return nil, err
	}
	proc.proxy = ioProxy

	defer func() {
		if err != nil {
			ioProxy.Close()
			proc.cancel()
			m.deleteProc(taskID, execID)
		}
	}()

	// Begin initializing stdio, but don't block on the initialization so we can send the Create
	// call (which will allow the stdio initialization to complete).
	initDone, copyDone := ioProxy.start(proc)
	proc.ioCopyDone = copyDone

	createResp, err := taskService.Create(reqCtx, req)
	if err != nil {
		return nil, err
	}

	// make sure stdio was initialized successfully
	err = <-initDone
	if err != nil {
		return nil, err
	}

	proc.logger.WithField("pid_in_vm", createResp.Pid).Info("successfully created task")

	go m.monitorExit(proc, taskService)
	go monitorIO(copyDone, proc, ioProxy)

	return createResp, nil
}

func (m *taskManager) ExecProcess(
	reqCtx context.Context,
	req *taskAPI.ExecProcessRequest,
	taskService taskAPI.TTRPCTaskService,
	ioProxy IOProxy,
) (_ *types.Empty, err error) {
	taskID := req.ID
	execID := req.ExecID

	proc, err := m.newProc(taskID, execID)
	if err != nil {
		return nil, err
	}
	proc.proxy = ioProxy

	defer func() {
		if err != nil {
			ioProxy.Close()
			proc.cancel()
			m.deleteProc(taskID, execID)
		}
	}()

	// Begin initializing stdio, but don't block on the initialization so we can send the Exec
	// call (which will allow the stdio initialization to complete).
	initDone, copyDone := ioProxy.start(proc)
	proc.ioCopyDone = copyDone

	execResp, err := taskService.Exec(reqCtx, req)
	if err != nil {
		return nil, err
	}

	// make sure stdio was initialized successfully
	err = <-initDone
	if err != nil {
		return nil, err
	}

	go m.monitorExit(proc, taskService)
	go monitorIO(copyDone, proc, ioProxy)

	return execResp, nil
}

func (m *taskManager) DeleteProcess(
	reqCtx context.Context,
	req *taskAPI.DeleteRequest,
	taskService taskAPI.TTRPCTaskService,
) (*taskAPI.DeleteResponse, error) {
	resp, err := taskService.Delete(reqCtx, req)
	if err != nil {
		return nil, err
	}

	proc, err := m.deleteProc(req.ID, req.ExecID)
	if err != nil {
		return nil, err
	}

	// Wait for the io streams to be flushed+closed before returning. Error is ignored
	// here as any io error is already logged in the IOProxy implementation directly.
	// Timeouts on waiting to close this channel after process exit are handled by the
	// IOProxy implementation being used for this proc. There's no extra timeout here
	// in order to keep things simpler.
	<-proc.ioCopyDone
	proc.proxy.Close()

	return resp, nil
}

func (m *taskManager) monitorExit(proc *vmProc, taskService taskAPI.TTRPCTaskService) {
	waitResp, waitErr := taskService.Wait(proc.ctx, &taskAPI.WaitRequest{
		ID:     proc.taskID,
		ExecID: proc.execID,
	})

	// Since the process is no longer running, cancel the context to
	// free the corresponding vmProc.
	proc.cancel()

	if waitErr == context.Canceled {
		return
	}

	if waitErr != nil {
		proc.logger.WithError(waitErr).Error("error waiting for exit")
	} else {
		proc.logger.
			WithField("exit_status", waitResp.ExitStatus).
			WithField("exited_at", waitResp.ExitedAt).
			Info("exited")
	}
}

func (m *taskManager) IsProxyOpen(taskID, execID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	proc, err := m.findProc(taskID, execID)
	if err != nil {
		return false, err
	}

	return proc.proxy.IsOpen(), nil
}

// findProc returns a vmProc if exists. The function itself doesn't lock the manager's mutex.
// The callers are responsible to lock/unlock since they may mutate the value.
func (m *taskManager) findProc(taskID, execID string) (*vmProc, error) {
	_, ok := m.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("cannot find exec %q from non-existent task %q", execID, taskID)
	}

	proc, ok := m.tasks[taskID][execID]
	if !ok {
		return nil, fmt.Errorf("cannot find non-existent exec %q from task %q", execID, taskID)
	}

	if proc.proxy == nil {
		return nil, fmt.Errorf("exec %q and task %q are present, but no proxy", taskID, execID)
	}
	return proc, nil
}

func (m *taskManager) AttachIO(ctx context.Context, taskID, execID string, proxy IOProxy) error {
	if err := ctx.Err(); err != nil {
		proxy.Close()
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	proc, err := m.findProc(taskID, execID)
	if err != nil {
		return err
	}
	if current, ok := proc.proxy.(*transportProxy); ok {
		replacement, framed := proxy.(*transportProxy)
		if !framed {
			proxy.Close()
			return errors.New("cannot replace framed streams with a raw IO proxy")
		}
		defer proxy.Close()
		return current.rebind(ctx, replacement)
	}

	// Stop the old FIFO readers before installing new ones. Otherwise two
	// generations can consume the same process output after reconnection.
	proc.proxy.Close()
	select {
	case <-proc.ioCopyDone:
	case <-ctx.Done():
		proxy.Close()
		return ctx.Err()
	}

	initDone, copyDone := proxy.start(proc)
	proc.proxy = proxy
	proc.ioCopyDone = copyDone

	// This must be in a goroutine. Otherwise, sending to initDone channel blocks forever.
	go func() {
		e := <-initDone
		if e != nil {
			proc.logger.Error("failed to initialize an io proxy")
			proxy.Close()
		}
	}()
	go monitorIO(copyDone, proc, proxy)

	return nil
}

func monitorIO(done <-chan error, proc *vmProc, proxy IOProxy) {
	<-done
	if _, framed := proxy.(CheckpointIOProxy); framed {
		// Retain network replay/FIN acknowledgements until final Delete/Close.
		return
	}
	proxy.Close()
	proc.logger.Debug("closed proxy")
}

type transportProcessProxy struct {
	taskID string
	execID string
	proxy  IOProxy
}

func (m *taskManager) ioProcesses() []transportProcessProxy {
	m.mu.Lock()
	defer m.mu.Unlock()
	processes := make([]transportProcessProxy, 0)
	for _, task := range m.tasks {
		for _, proc := range task {
			processes = append(processes, transportProcessProxy{taskID: proc.taskID, execID: proc.execID, proxy: proc.proxy})
		}
	}
	return processes
}

func (m *taskManager) PrepareIO(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	processes := m.ioProcesses()
	errs := make(chan error, len(processes))
	for _, proc := range processes {
		proxy := proc.proxy
		if _, null := proxy.(*nullIOProxy); null {
			errs <- nil
			continue
		}
		framed, ok := proxy.(CheckpointIOProxy)
		if !ok {
			errs <- fmt.Errorf("process %s/%s uses unsupported raw checkpoint IO", proc.taskID, proc.execID)
			continue
		}
		go func() { errs <- framed.PrepareIO(ctx) }()
	}
	var combined error
	for range processes {
		combined = errors.Join(combined, <-errs)
	}
	return combined
}

func (m *taskManager) DescribeIO() ([]TransportProcessIO, error) {
	processes := m.ioProcesses()
	result := make([]TransportProcessIO, 0, len(processes))
	for _, proc := range processes {
		entry := TransportProcessIO{TaskID: proc.taskID, ExecID: proc.execID}
		if _, null := proc.proxy.(*nullIOProxy); !null {
			framed, ok := proc.proxy.(CheckpointIOProxy)
			if !ok {
				return nil, fmt.Errorf("process %s/%s uses unsupported raw checkpoint IO", proc.taskID, proc.execID)
			}
			var err error
			entry.Streams, err = framed.DescribeIO()
			if err != nil {
				return nil, err
			}
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TaskID == result[j].TaskID {
			return result[i].ExecID < result[j].ExecID
		}
		return result[i].TaskID < result[j].TaskID
	})
	return result, nil
}

func (m *taskManager) ResumeIO() {
	for _, proc := range m.ioProcesses() {
		if framed, ok := proc.proxy.(CheckpointIOProxy); ok {
			framed.ResumeIO()
		}
	}
}
