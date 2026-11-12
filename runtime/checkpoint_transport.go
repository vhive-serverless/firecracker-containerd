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
	"time"

	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	ioproxy "github.com/firecracker-microvm/firecracker-containerd/proto/service/ioproxy/ttrpc"
)

// checkpointTransportCut is written only after both FIFO endpoints agree on
// every accepted byte and the guest has acknowledged host event publication.
// External unread host stdin remains source-owned, outside the captured cut.
type checkpointTransportCut struct {
	Version     int
	Processes   []vm.TransportProcessIO
	EventCursor uint64
	ResumeTasks []string
}

func checkpointProcessStreams(cut *checkpointTransportCut, taskID, execID string) ([]vm.TransportStreamIO, bool) {
	if cut == nil {
		return nil, false
	}
	for _, p := range cut.Processes {
		if p.TaskID == taskID && p.ExecID == execID {
			return p.Streams, true
		}
	}
	return nil, false
}

func validateCheckpointTransport(cut *checkpointTransportCut) error {
	if cut == nil || cut.Version != 1 {
		return errors.New("checkpoint lacks an upstream-compatible sealed transport cut")
	}
	wire := make([]*ioproxy.TransportProcess, 0, len(cut.Processes))
	for _, p := range cut.Processes {
		wire = append(wire, &ioproxy.TransportProcess{TaskID: p.TaskID, ExecID: p.ExecID, Streams: transportStreamsToWire(p.Streams)})
	}
	_, err := transportProcessesFromWire(wire)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, id := range cut.ResumeTasks {
		if _, ok := checkpointProcessStreams(cut, id, ""); !ok || seen[id] {
			return errors.New("invalid checkpoint task thaw set")
		}
		seen[id] = true
	}
	return nil
}

func (s *service) sealIOTransport(ctx context.Context) (*checkpointTransportCut, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	processes, err := s.prepareIOTransport(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.pauseEventForwarding(ctx); err != nil {
		return nil, err
	}
	cursor := uint64(0)
	if s.eventForwarder != nil {
		cursor = s.eventForwarder.Cursor()
	}
	return &checkpointTransportCut{Version: 1, Processes: processes, EventCursor: cursor}, nil
}

func (s *service) resumeSnapshotTransport(ctx context.Context) error {
	return errors.Join(s.resumeIOTransport(ctx), s.resumeEventForwarding(ctx))
}
