// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

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

func TestAgentEventForwardingPauseResume(t *testing.T) {
	publisher := newAgentEventTestPublisher(nil)
	fixture := newAgentEventTestFixture(t, publisher)
	s := fixture.service
	fixture.publish(t, "before-pause")
	require.NoError(t, s.resumeEventForwarding(fixture.ctx))
	fixture.waitAck(t, 1)
	require.NoError(t, s.pauseEventForwarding(fixture.ctx))
	require.Equal(t, uint64(1), s.eventForwarder.Cursor())
	require.Equal(t, uint64(1), fixture.journal.Cursor(), "pause must seal the remotely published cut on the guest")

	fixture.publish(t, "while-paused")
	require.Equal(t, []string{"before-pause"}, publisher.records())
	fixture.reconnect(t)
	require.NoError(t, s.resumeEventForwarding(fixture.ctx))
	fixture.waitAck(t, 2)
	require.NoError(t, s.pauseEventForwarding(fixture.ctx))
	require.Equal(t, []string{"before-pause", "while-paused"}, publisher.records(), "source reconnect must preserve its publication cursor")
}

func TestAgentEventForwardingPublisherFailureRetainsCut(t *testing.T) {
	for _, mode := range []string{"blocked", "failed"} {
		t.Run(mode, func(t *testing.T) {
			allow := make(chan struct{})
			var release sync.Once
			publisher := newAgentEventTestPublisher(func(ctx context.Context, _ string) error {
				if mode == "failed" {
					select {
					case <-allow:
						return nil
					default:
						return errors.New("publisher unavailable")
					}
				}
				select {
				case <-allow:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			fixture := newAgentEventTestFixture(t, publisher)
			t.Cleanup(func() { release.Do(func() { close(allow) }) })
			s := fixture.service
			fixture.publish(t, "pending")
			require.NoError(t, s.resumeEventForwarding(fixture.ctx))
			waitAgentEventAttempt(t, publisher, "pending")
			require.NoError(t, s.pauseEventForwarding(fixture.ctx))
			require.Zero(t, fixture.journal.Cursor(), "failed or cancelled publication must not be acknowledged")
			require.Zero(t, s.eventForwarder.Cursor())
			require.Empty(t, publisher.records())
			_, err := s.journalClient().Next(fixture.ctx, 0)
			require.NoError(t, err, "the event must remain retained after cancellation")
			release.Do(func() { close(allow) })
			require.NoError(t, s.resumeEventForwarding(fixture.ctx))
			fixture.waitAck(t, 1)
			require.NoError(t, s.pauseEventForwarding(fixture.ctx))
			require.Equal(t, []string{"pending"}, publisher.records())
		})
	}
}

func TestAgentEventForwardingStopTimeoutBlocksResume(t *testing.T) {
	for _, recovery := range []string{"pause", "resume"} {
		t.Run(recovery, func(t *testing.T) {
			allow := make(chan struct{})
			var release sync.Once
			publisher := newAgentEventTestPublisher(func(_ context.Context, _ string) error {
				// Model an external publisher that completes successfully after its
				// caller cancels. A timeout must not start another forwarder over it.
				<-allow
				return nil
			})
			fixture := newAgentEventTestFixture(t, publisher)
			t.Cleanup(func() { release.Do(func() { close(allow) }) })
			s := fixture.service
			fixture.publish(t, "late-success")
			require.NoError(t, s.resumeEventForwarding(fixture.ctx))
			waitAgentEventAttempt(t, publisher, "late-success")
			s.eventMu.Lock()
			originalDone := s.eventDone
			s.eventMu.Unlock()
			stopCtx, stop := context.WithTimeout(fixture.ctx, 20*time.Millisecond)
			require.ErrorIs(t, s.pauseEventForwarding(stopCtx), context.DeadlineExceeded)
			stop()
			s.eventMu.Lock()
			stopping, done, hasCancel := s.eventStopping, s.eventDone, s.eventCancel != nil
			s.eventMu.Unlock()
			require.True(t, stopping)
			require.True(t, hasCancel)
			require.Equal(t, originalDone, done)
			require.Zero(t, fixture.journal.Cursor())

			resumeCtx, resume := context.WithTimeout(fixture.ctx, 20*time.Millisecond)
			require.ErrorIs(t, s.resumeEventForwarding(resumeCtx), context.DeadlineExceeded, "a timed-out stop must not be mistaken for an active forwarder")
			resume()
			s.eventMu.Lock()
			stillStopping, unchangedDone := s.eventStopping, s.eventDone
			s.eventMu.Unlock()
			require.True(t, stillStopping)
			require.Equal(t, originalDone, unchangedDone)

			release.Do(func() { close(allow) })
			select {
			case <-originalDone:
			case <-fixture.ctx.Done():
				t.Fatal("late publication did not complete: ", fixture.ctx.Err())
			}
			require.Equal(t, uint64(1), s.eventForwarder.Cursor(), "late successful publication must remain known on the host")
			require.Zero(t, fixture.journal.Cursor(), "the cancelled RPC cannot seal a guest receipt")
			if recovery == "pause" {
				require.NoError(t, s.pauseEventForwarding(fixture.ctx))
				require.Equal(t, uint64(1), fixture.journal.Cursor(), "retrying pause must synchronize the late publication")
			}
			fixture.publish(t, "after-recovery")
			require.NoError(t, s.resumeEventForwarding(fixture.ctx))
			fixture.waitAck(t, 2)
			require.NoError(t, s.pauseEventForwarding(fixture.ctx))
			require.Equal(t, []string{"late-success", "after-recovery"}, publisher.records())
		})
	}
}

func TestAgentEventForwardingCloneUsesGuestAck(t *testing.T) {
	publisher := newAgentEventTestPublisher(nil)
	fixture := newAgentEventTestFixture(t, publisher)
	fixture.publish(t, "already-published-before-capture")
	require.NoError(t, fixture.journal.Ack(1))
	fixture.publish(t, "captured-pending")
	s := fixture.service
	require.Nil(t, s.eventForwarder, "a clone has no retained source host cursor")
	require.NoError(t, s.resumeEventForwarding(fixture.ctx))
	fixture.waitAck(t, 2)
	require.NoError(t, s.pauseEventForwarding(fixture.ctx))
	require.Equal(t, uint64(2), s.eventForwarder.Cursor())
	require.Equal(t, []string{"captured-pending"}, publisher.records(), "a clone must start at the guest's captured receipt")
}

func TestAgentEventForwardingPauseRepairsLostAck(t *testing.T) {
	var fixture *agentEventTestFixture
	var disconnect sync.Once
	publisher := newAgentEventTestPublisher(func(_ context.Context, _ string) error {
		disconnect.Do(func() {
			// Publication succeeds externally, but the source connection is
			// gone before its guest acknowledgement can be sent.
			fixture.service.agentRPCMu.RLock()
			client := fixture.service.agentRPC
			fixture.service.agentRPCMu.RUnlock()
			_ = client.Close()
		})
		return nil
	})
	fixture = newAgentEventTestFixture(t, publisher)
	s := fixture.service
	fixture.publish(t, "published-without-receipt")
	require.NoError(t, s.resumeEventForwarding(fixture.ctx))
	waitAgentEventAttempt(t, publisher, "published-without-receipt")
	require.Error(t, s.pauseEventForwarding(fixture.ctx), "pause cannot seal a snapshot cut over a disconnected guest")
	require.Equal(t, uint64(1), s.eventForwarder.Cursor())
	require.Zero(t, fixture.journal.Cursor())
	require.Equal(t, []string{"published-without-receipt"}, publisher.records())

	fixture.reconnect(t)
	require.NoError(t, s.pauseEventForwarding(fixture.ctx))
	require.Equal(t, uint64(1), fixture.journal.Cursor(), "a pause retry must repair the missing receipt before capture")
	fixture.publish(t, "after-receipt-repair")
	require.NoError(t, s.resumeEventForwarding(fixture.ctx))
	fixture.waitAck(t, 2)
	require.NoError(t, s.pauseEventForwarding(fixture.ctx))
	require.Equal(t, []string{"published-without-receipt", "after-receipt-repair"}, publisher.records())
}
