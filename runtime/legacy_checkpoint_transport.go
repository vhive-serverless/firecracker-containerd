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
	"fmt"
	"sort"
	"time"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/containerd/api/types/task"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// pendingVMPause survives cancellation between the three legacy API calls.
// All access is serialized by snapshotMu; successful recovery clears it last.
type pendingVMPause struct {
	Cut        *checkpointTransportCut
	Tasks      []string
	IOPrepared bool
	VMPaused   bool
	MayReset   bool
}

func (s *service) lockSnapshotTransition(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !s.snapshotMu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	if err := ctx.Err(); err != nil {
		s.snapshotMu.Unlock()
		return err
	}
	return nil
}

func (s *service) prepareLegacyPause(ctx context.Context) (retErr error) {
	if s.pendingPause != nil {
		paused, err := s.isPaused(ctx)
		if err != nil {
			return err
		}
		if paused && s.pendingPause.Cut != nil {
			return nil
		}
		return status.Error(codes.FailedPrecondition, "previous pause recovery is pending; resume the VM first")
	}
	paused, err := s.isPaused(ctx)
	if err != nil {
		return err
	}
	if paused {
		return status.Error(codes.FailedPrecondition, "VM was paused without a transport barrier")
	}
	pending := &pendingVMPause{}
	s.pendingPause = pending
	defer func() {
		if retErr != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			retErr = errors.Join(retErr, s.resumePreparedVM(cleanup))
		}
	}()
	s.fifosMu.Lock()
	ids := make([]string, 0, len(s.fifos))
	for id := range s.fifos {
		ids = append(ids, id)
	}
	s.fifosMu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		state, err := s.agentClient.State(ctx, &taskAPI.StateRequest{ID: id})
		if err != nil {
			return err
		}
		if state.Status == task.Status_RUNNING {
			pending.Tasks = append(pending.Tasks, id)
			if _, err := s.agentClient.Pause(ctx, &taskAPI.PauseRequest{ID: id}); err != nil {
				return err
			}
		}
	}
	pending.IOPrepared = true
	pending.Cut, err = s.sealIOTransport(ctx)
	if err != nil {
		return err
	}
	pending.Cut.ResumeTasks = append([]string(nil), pending.Tasks...)
	pending.VMPaused = true // A failed response can still have paused the VMM.
	return s.machine.PauseVM(ctx)
}

func (s *service) resumePreparedVM(ctx context.Context) error {
	pending := s.pendingPause
	if pending == nil {
		return s.machine.ResumeVM(ctx)
	}
	if pending.VMPaused {
		if err := s.machine.ResumeVM(ctx); err != nil {
			return err
		}
		pending.VMPaused = false
	}
	if pending.MayReset {
		if err := s.connectAgent(ctx); err != nil {
			return err
		}
		// Certify a fresh round trip on every retained IO stream before
		// reporting source recovery; gates remain closed during this check.
		if pending.IOPrepared {
			if err := s.reconnectSourceIO(ctx, pending.Cut); err != nil {
				return err
			}
			if _, err := s.sealIOTransport(ctx); err != nil {
				return err
			}
		}
		pending.MayReset = false
	}
	if pending.IOPrepared {
		if err := s.resumeSnapshotTransport(ctx); err != nil {
			return err
		}
		pending.IOPrepared = false
	}
	for len(pending.Tasks) > 0 {
		id := pending.Tasks[0]
		state, err := s.agentClient.State(ctx, &taskAPI.StateRequest{ID: id})
		if err != nil {
			return err
		}
		if state.Status == task.Status_PAUSED {
			if _, err := s.agentClient.Resume(ctx, &taskAPI.ResumeRequest{ID: id}); err != nil {
				return err
			}
		}
		pending.Tasks = pending.Tasks[1:]
	}
	s.pendingPause = nil
	return nil
}

// reconnectSourceIO replaces network generations after a snapshot invalidates
// vsock connections. An idle sender cannot observe the old connection's reset
// until its next frame, so do not wait for implicit reconnection to recertify a
// prepared cut. Attach reuses both FIFO pumps and their current cursors; omitting
// replacement cursors also retains a restored source's noninitial session.
func (s *service) reconnectSourceIO(ctx context.Context, cut *checkpointTransportCut) error {
	if cut == nil {
		return fmt.Errorf("source IO reconnect requires a prepared transport cut")
	}
	for _, process := range cut.Processes {
		if len(process.Streams) == 0 {
			continue
		}
		s.fifosMu.Lock()
		host, ok := s.fifos[process.TaskID][process.ExecID]
		s.fifosMu.Unlock()
		if !ok {
			return fmt.Errorf("source IO reconnect missing FIFOs for %s/%s", process.TaskID, process.ExecID)
		}
		// Fresh ports give the guest new listener lifetimes as well as new sockets.
		if err := s.attachNewProxy(ctx, s.logger, process.TaskID, process.ExecID, host); err != nil {
			return fmt.Errorf("reconnect source IO %s/%s: %w", process.TaskID, process.ExecID, err)
		}
	}
	return nil
}
