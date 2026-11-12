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
	"sync/atomic"
	"testing"
	"time"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/ttrpc"
)

type disconnectedAgent struct {
	taskAPI.TTRPCTaskService
	waiting chan struct{}
	starts  atomic.Int32
	exit    *taskAPI.WaitResponse
}

func (a *disconnectedAgent) Wait(context.Context, *taskAPI.WaitRequest) (*taskAPI.WaitResponse, error) {
	if a.waiting != nil {
		close(a.waiting)
	}
	if a.exit != nil {
		return a.exit, nil
	}
	return nil, ttrpc.ErrClosed
}

func (a *disconnectedAgent) Start(context.Context, *taskAPI.StartRequest) (*taskAPI.StartResponse, error) {
	a.starts.Add(1)
	return nil, ttrpc.ErrClosed
}

func TestAgentWaitSurvivesTransportReplacement(t *testing.T) {
	old := &disconnectedAgent{waiting: make(chan struct{})}
	client := NewReconnectingTaskClient(old)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan *taskAPI.WaitResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		response, err := client.Wait(ctx, &taskAPI.WaitRequest{ID: "still-running"})
		done <- response
		errCh <- err
	}()
	<-old.waiting
	select {
	case err := <-errCh:
		t.Fatalf("transport failure returned as task termination: %v", err)
	default:
	}
	client.Replace(&disconnectedAgent{exit: &taskAPI.WaitResponse{ExitStatus: 42}})
	select {
	case response := <-done:
		if response == nil || response.ExitStatus != 42 {
			t.Fatalf("wrong exit after reconnect: %v", response)
		}
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestAgentMutationIsNotReplayed(t *testing.T) {
	agent := &disconnectedAgent{}
	client := NewReconnectingTaskClient(agent)
	_, err := client.Start(context.Background(), &taskAPI.StartRequest{ID: "ambiguous-start"})
	if !errors.Is(err, ttrpc.ErrClosed) || agent.starts.Load() != 1 {
		t.Fatalf("mutation retry/outcome hidden: %v, %d", err, agent.starts.Load())
	}
}

func TestAgentWaitCancellationDoesNotWaitForReconnect(t *testing.T) {
	agent := &disconnectedAgent{}
	client := NewReconnectingTaskClient(agent)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Wait(ctx, &taskAPI.WaitRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
