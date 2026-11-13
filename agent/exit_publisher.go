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

	"github.com/containerd/containerd/api/events"
	coreevents "github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/pkg/shim"
)

// exitPublisher closes a per-task barrier only after its init exit has reached
// the event exchange. runc's Wait can complete before its OOM watcher teardown
// and exit publication; Delete must not overtake that publication.
type exitPublisher struct {
	shim.Publisher
	mu    sync.Mutex
	exits map[string]chan struct{}
}

func (p *exitPublisher) begin(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exits[id] = make(chan struct{})
}

func (p *exitPublisher) forget(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.exits, id)
}

func (p *exitPublisher) wait(ctx context.Context, id string) error {
	p.mu.Lock()
	done := p.exits[id]
	p.mu.Unlock()
	if done == nil {
		return nil
	} // task was never started
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *exitPublisher) Publish(ctx context.Context, topic string, event coreevents.Event) error {
	if err := p.Publisher.Publish(ctx, topic, event); err != nil {
		return err
	}
	if exit, ok := event.(*events.TaskExit); ok && exit.ID == exit.ContainerID {
		p.mu.Lock()
		if done := p.exits[exit.ContainerID]; done != nil {
			select {
			case <-done:
			default:
				close(done)
			}
		}
		p.mu.Unlock()
	}
	return nil
}
