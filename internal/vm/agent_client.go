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
	"sync"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/ttrpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ReconnectingTaskClient keeps task monitors bound to a logical agent session.
// Mutations are never retried implicitly: an unknown outcome must be resolved
// by the checkpoint admission barrier. Only Wait, a read-only long poll, retries
// a closed transport after the coordinator installs a replacement session.
type ReconnectingTaskClient struct {
	mu      sync.RWMutex
	client  taskAPI.TTRPCTaskService
	changed chan struct{}
}

// NewReconnectingTaskClient creates a task client whose session can be replaced.
func NewReconnectingTaskClient(client taskAPI.TTRPCTaskService) *ReconnectingTaskClient {
	return &ReconnectingTaskClient{client: client, changed: make(chan struct{})}
}

// Replace installs the current session and wakes calls waiting to reconnect.
func (c *ReconnectingTaskClient) Replace(client taskAPI.TTRPCTaskService) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.client = client
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *ReconnectingTaskClient) current() (taskAPI.TTRPCTaskService, <-chan struct{}) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client, c.changed
}

var _ taskAPI.TTRPCTaskService = (*ReconnectingTaskClient)(nil)

// State reads task state from the current session.
func (c *ReconnectingTaskClient) State(ctx context.Context, req *taskAPI.StateRequest) (*taskAPI.StateResponse, error) {
	client, _ := c.current()
	return client.State(ctx, req)
}

// Create creates a task through the current session.
func (c *ReconnectingTaskClient) Create(ctx context.Context, req *taskAPI.CreateTaskRequest) (*taskAPI.CreateTaskResponse, error) {
	client, _ := c.current()
	return client.Create(ctx, req)
}

// Start starts a task through the current session.
func (c *ReconnectingTaskClient) Start(ctx context.Context, req *taskAPI.StartRequest) (*taskAPI.StartResponse, error) {
	client, _ := c.current()
	return client.Start(ctx, req)
}

// Delete deletes a task through the current session.
func (c *ReconnectingTaskClient) Delete(ctx context.Context, req *taskAPI.DeleteRequest) (*taskAPI.DeleteResponse, error) {
	client, _ := c.current()
	return client.Delete(ctx, req)
}

// Pids lists task processes through the current session.
func (c *ReconnectingTaskClient) Pids(ctx context.Context, req *taskAPI.PidsRequest) (*taskAPI.PidsResponse, error) {
	client, _ := c.current()
	return client.Pids(ctx, req)
}

// Pause pauses a task through the current session.
func (c *ReconnectingTaskClient) Pause(ctx context.Context, req *taskAPI.PauseRequest) (*emptypb.Empty, error) {
	client, _ := c.current()
	return client.Pause(ctx, req)
}

// Resume resumes a task through the current session.
func (c *ReconnectingTaskClient) Resume(ctx context.Context, req *taskAPI.ResumeRequest) (*emptypb.Empty, error) {
	client, _ := c.current()
	return client.Resume(ctx, req)
}

// Checkpoint checkpoints a task through the current session.
func (c *ReconnectingTaskClient) Checkpoint(ctx context.Context, req *taskAPI.CheckpointTaskRequest) (*emptypb.Empty, error) {
	client, _ := c.current()
	return client.Checkpoint(ctx, req)
}

// Kill signals a task through the current session.
func (c *ReconnectingTaskClient) Kill(ctx context.Context, req *taskAPI.KillRequest) (*emptypb.Empty, error) {
	client, _ := c.current()
	return client.Kill(ctx, req)
}

// Exec creates an exec process through the current session.
func (c *ReconnectingTaskClient) Exec(ctx context.Context, req *taskAPI.ExecProcessRequest) (*emptypb.Empty, error) {
	client, _ := c.current()
	return client.Exec(ctx, req)
}

// ResizePty resizes a task terminal through the current session.
func (c *ReconnectingTaskClient) ResizePty(ctx context.Context, req *taskAPI.ResizePtyRequest) (*emptypb.Empty, error) {
	client, _ := c.current()
	return client.ResizePty(ctx, req)
}

// CloseIO closes task IO through the current session.
func (c *ReconnectingTaskClient) CloseIO(ctx context.Context, req *taskAPI.CloseIORequest) (*emptypb.Empty, error) {
	client, _ := c.current()
	return client.CloseIO(ctx, req)
}

// Update updates a task through the current session.
func (c *ReconnectingTaskClient) Update(ctx context.Context, req *taskAPI.UpdateTaskRequest) (*emptypb.Empty, error) {
	client, _ := c.current()
	return client.Update(ctx, req)
}

// Wait waits for task exit, retrying closed transports after session replacement.
func (c *ReconnectingTaskClient) Wait(ctx context.Context, req *taskAPI.WaitRequest) (*taskAPI.WaitResponse, error) {
	for {
		client, changed := c.current()
		response, err := client.Wait(ctx, req)
		if !errors.Is(err, ttrpc.ErrClosed) {
			return response, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// Stats reads task statistics from the current session.
func (c *ReconnectingTaskClient) Stats(ctx context.Context, req *taskAPI.StatsRequest) (*taskAPI.StatsResponse, error) {
	client, _ := c.current()
	return client.Stats(ctx, req)
}

// Connect reads task connection details from the current session.
func (c *ReconnectingTaskClient) Connect(ctx context.Context, req *taskAPI.ConnectRequest) (*taskAPI.ConnectResponse, error) {
	client, _ := c.current()
	return client.Connect(ctx, req)
}

// Shutdown shuts down the agent through the current session.
func (c *ReconnectingTaskClient) Shutdown(ctx context.Context, req *taskAPI.ShutdownRequest) (*emptypb.Empty, error) {
	client, _ := c.current()
	return client.Shutdown(ctx, req)
}
