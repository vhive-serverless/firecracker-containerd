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
	"fmt"
	"time"

	"github.com/containerd/ttrpc"
	"github.com/firecracker-microvm/firecracker-containerd/eventbridge"
)

func (s *service) journalClient() eventbridge.JournalClient {
	return eventbridge.JournalClient{Client: func(ctx context.Context) (*ttrpc.Client, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.agentRPCMu.RLock()
		defer s.agentRPCMu.RUnlock()
		if s.agentRPC == nil {
			return nil, fmt.Errorf("agent session is not connected")
		}
		return s.agentRPC, nil
	}}
}

// resumeEventForwarding preserves the host publication cursor on source resume.
// A new clone initializes from the guest's captured acknowledged cursor.
func (s *service) resumeEventForwarding(ctx context.Context) error {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if err := s.shimCtx.Err(); err != nil {
		return err
	}
	if s.eventCancel != nil {
		if !s.eventStopping {
			return nil
		}
		select {
		case <-s.eventDone:
		case <-ctx.Done():
			return ctx.Err()
		}
		s.eventCancel, s.eventDone = nil, nil
		s.eventStopping = false
	}
	journal := s.journalClient()
	if s.eventForwarder == nil {
		cursor, err := journal.Cursor(ctx)
		if err != nil {
			return err
		}
		s.eventForwarder = eventbridge.NewJournalForwarder(cursor)
	}
	forwardCtx, cancel := context.WithCancel(s.shimCtx)
	done := make(chan struct{})
	s.eventCancel, s.eventDone = cancel, done
	go func() {
		defer close(done)
		for forwardCtx.Err() == nil {
			if err := s.eventForwarder.Step(forwardCtx, journal, s.remotePublisher); err != nil {
				if forwardCtx.Err() != nil {
					return
				}
				s.logger.WithError(err).Warn("agent event delivery pending; retaining journal cursor")
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-forwardCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}
	}()
	return nil
}

func (s *service) closeAgentOnShutdown() {
	<-s.shimCtx.Done()
	s.eventMu.Lock()
	done := s.eventDone
	if s.eventCancel != nil {
		s.eventCancel()
	}
	s.eventMu.Unlock()
	if done != nil {
		<-done
	}
	s.agentRPCMu.Lock()
	client := s.agentRPC
	s.agentRPC = nil
	s.agentRPCMu.Unlock()
	if client != nil {
		_ = client.Close()
	}
	if s.remotePublisher != nil {
		_ = s.remotePublisher.Close()
	}
}

// pauseEventForwarding cancels the long poll, waits for any publication to
// finish, then synchronizes the receipt before vCPU pause. A failure must abort
// snapshot preparation; otherwise a host-only publication cursor could be lost.
func (s *service) pauseEventForwarding(ctx context.Context) error {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if s.eventCancel != nil {
		s.eventStopping = true
		s.eventCancel()
		select {
		case <-s.eventDone:
		case <-ctx.Done():
			return ctx.Err()
		}
		s.eventCancel, s.eventDone = nil, nil
		s.eventStopping = false
	}
	if s.eventForwarder == nil {
		return nil
	}
	return s.journalClient().Ack(ctx, s.eventForwarder.Cursor())
}
