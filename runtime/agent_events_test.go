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
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	apievents "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/ttrpc"
	"github.com/firecracker-microvm/firecracker-containerd/eventbridge"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type agentEventTestPublisher struct {
	mu        sync.Mutex
	published []string
	attempts  chan string
	publish   func(context.Context, string) error
}

func newAgentEventTestPublisher(publish func(context.Context, string) error) *agentEventTestPublisher {
	return &agentEventTestPublisher{attempts: make(chan string, 32), publish: publish}
}

func (p *agentEventTestPublisher) Publish(ctx context.Context, _ string, event events.Event) error {
	id := event.(*apievents.TaskExit).ID
	p.attempts <- id
	if p.publish != nil {
		if err := p.publish(ctx, id); err != nil {
			return err
		}
	}
	p.mu.Lock()
	p.published = append(p.published, id)
	p.mu.Unlock()
	return nil
}

func (*agentEventTestPublisher) Close() error { return nil }

func (p *agentEventTestPublisher) records() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.published...)
}

type agentEventTestFixture struct {
	ctx     context.Context
	service *service
	journal *eventbridge.Journal
	address string
}

func newAgentEventTestFixture(t *testing.T, publisher *agentEventTestPublisher) *agentEventTestFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), "event-coordinator-test"), 10*time.Second)
	journal := eventbridge.NewJournal(4096)
	server, err := ttrpc.NewServer()
	require.NoError(t, err)
	eventbridge.RegisterJournalService(server, journal)
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "e"))
	require.NoError(t, err)
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = server.Serve(ctx, listener)
	}()
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	fixture := &agentEventTestFixture{ctx: ctx, journal: journal, address: listener.Addr().String()}
	fixture.service = &service{shimCtx: ctx, shimCancel: cancel, remotePublisher: publisher, logger: logrus.NewEntry(logger)}
	fixture.reconnect(t)
	t.Cleanup(func() {
		cancel()
		fixture.service.eventMu.Lock()
		if fixture.service.eventCancel != nil {
			fixture.service.eventCancel()
		}
		done := fixture.service.eventDone
		fixture.service.eventMu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("event forwarder did not stop during fixture cleanup")
			}
		}
		fixture.service.agentRPCMu.Lock()
		client := fixture.service.agentRPC
		fixture.service.agentRPC = nil
		fixture.service.agentRPCMu.Unlock()
		if client != nil {
			_ = client.Close()
		}
		_ = journal.Close()
		_ = server.Close()
		_ = listener.Close()
		select {
		case <-served:
		case <-time.After(time.Second):
			t.Error("journal RPC server did not stop")
		}
	})
	return fixture
}

func (f *agentEventTestFixture) reconnect(t *testing.T) {
	t.Helper()
	connection, err := net.Dial("unix", f.address)
	require.NoError(t, err)
	f.service.agentRPCMu.Lock()
	previous := f.service.agentRPC
	f.service.agentRPC = ttrpc.NewClient(connection)
	f.service.agentRPCMu.Unlock()
	if previous != nil {
		require.NoError(t, previous.Close())
	}
}

func (f *agentEventTestFixture) publish(t *testing.T, id string) {
	t.Helper()
	require.NoError(t, f.journal.Publish(f.ctx, "/tasks/exit", &apievents.TaskExit{ContainerID: "task", ID: id, ExitStatus: 23}))
}

func (f *agentEventTestFixture) waitAck(t *testing.T, cursor uint64) {
	t.Helper()
	require.Eventually(t, func() bool { return f.journal.Cursor() == cursor }, 2*time.Second, time.Millisecond, "journal acknowledgement never reached %d", cursor)
}

func waitAgentEventAttempt(t *testing.T, publisher *agentEventTestPublisher, id string) {
	t.Helper()
	select {
	case actual := <-publisher.attempts:
		require.Equal(t, id, actual)
	case <-time.After(2 * time.Second):
		t.Fatal("publisher never received event ", id)
	}
}

func TestAgentEventForwardingReconnect(t *testing.T) {
	publisher := newAgentEventTestPublisher(nil)
	fixture := newAgentEventTestFixture(t, publisher)
	s := fixture.service
	fixture.publish(t, "first-session")
	require.NoError(t, s.resumeEventForwarding(fixture.ctx))
	fixture.waitAck(t, 1)
	fixture.reconnect(t)
	fixture.publish(t, "next-session")
	fixture.waitAck(t, 2)
	require.Equal(t, []string{"first-session", "next-session"}, publisher.records())
}

func TestAgentEventForwardingRetriesFailedPublication(t *testing.T) {
	allow := make(chan struct{})
	publisher := newAgentEventTestPublisher(func(_ context.Context, _ string) error {
		select {
		case <-allow:
			return nil
		default:
			return errors.New("publisher unavailable")
		}
	})
	fixture := newAgentEventTestFixture(t, publisher)
	fixture.publish(t, "retained")
	require.NoError(t, fixture.service.resumeEventForwarding(fixture.ctx))
	waitAgentEventAttempt(t, publisher, "retained")
	require.Zero(t, fixture.journal.Cursor())
	require.Empty(t, publisher.records())
	close(allow)
	fixture.waitAck(t, 1)
	require.Equal(t, []string{"retained"}, publisher.records())
}

func TestAgentEventForwardingReconnectRepairsLostAcknowledgement(t *testing.T) {
	var fixture *agentEventTestFixture
	var disconnect sync.Once
	publisher := newAgentEventTestPublisher(func(_ context.Context, _ string) error {
		disconnect.Do(func() {
			fixture.service.agentRPCMu.RLock()
			client := fixture.service.agentRPC
			fixture.service.agentRPCMu.RUnlock()
			_ = client.Close()
		})
		return nil
	})
	fixture = newAgentEventTestFixture(t, publisher)
	fixture.publish(t, "published-without-receipt")
	require.NoError(t, fixture.service.resumeEventForwarding(fixture.ctx))
	waitAgentEventAttempt(t, publisher, "published-without-receipt")
	require.Eventually(t, func() bool { return len(publisher.records()) == 1 }, time.Second, time.Millisecond)
	require.Zero(t, fixture.journal.Cursor())
	fixture.reconnect(t)
	fixture.waitAck(t, 1)
	fixture.publish(t, "after-reconnect")
	fixture.waitAck(t, 2)
	require.Equal(t, []string{"published-without-receipt", "after-reconnect"}, publisher.records())
}
