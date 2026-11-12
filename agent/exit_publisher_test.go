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
	"time"

	"github.com/containerd/containerd/api/events"
	coreevents "github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/core/runtime"
	"github.com/stretchr/testify/require"
)

type blockedExitSink struct {
	published chan struct{}
	release   chan struct{}
}

func (p *blockedExitSink) Publish(context.Context, string, coreevents.Event) error {
	close(p.published)
	<-p.release
	return nil
}
func (*blockedExitSink) Close() error { return nil }

func TestDeleteWaitsForExitPublication(t *testing.T) {
	sink := &blockedExitSink{published: make(chan struct{}), release: make(chan struct{})}
	publisher := &exitPublisher{Publisher: sink, exits: make(map[string]chan struct{})}
	const id = "container"
	publisher.begin(id)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	published := make(chan error, 1)
	go func() {
		published <- publisher.Publish(ctx, runtime.TaskExitEventTopic, &events.TaskExit{ID: id, ContainerID: id})
	}()
	<-sink.published
	// runc's process is already marked exited, while publication is delayed.
	deadline, stop := context.WithCancel(context.Background())
	stop()
	require.ErrorIs(t, publisher.wait(deadline, id), context.Canceled)
	close(sink.release)
	require.NoError(t, <-published)
	require.NoError(t, publisher.wait(ctx, id))
	publisher.forget(id)
	require.Empty(t, publisher.exits)
	require.NoError(t, publisher.wait(ctx, "never-started"))
}
