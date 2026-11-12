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
	"time"

	"github.com/containerd/containerd/v2/pkg/identifiers"
	"github.com/containerd/containerd/v2/pkg/protobuf/types"
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

func transportProcessesFromWire(processes []*ioproxy.TransportProcess) ([]vm.TransportProcessIO, error) {
	if len(processes) > 4096 {
		return nil, errors.New("oversized transport process registry")
	}
	result := make([]vm.TransportProcessIO, 0, len(processes))
	seen := make(map[string]bool)
	for _, p := range processes {
		if p == nil || identifiers.Validate(p.TaskID) != nil || (p.ExecID != "" && identifiers.Validate(p.ExecID) != nil) || len(p.Streams) > 3 {
			return nil, errors.New("invalid transport process identity")
		}
		key := p.TaskID + "/" + p.ExecID
		if seen[key] {
			return nil, errors.New("duplicate transport process")
		}
		seen[key] = true
		entry := vm.TransportProcessIO{TaskID: p.TaskID, ExecID: p.ExecID}
		names := make(map[string]bool)
		for _, stream := range p.Streams {
			if stream == nil || stream.Cursor == nil || names[stream.Name] || (stream.Name != "stdin" && stream.Name != "stdout" && stream.Name != "stderr") {
				return nil, errors.New("invalid transport stream")
			}
			c := stream.Cursor
			if c.StreamID != key+"/"+stream.Name || c.SessionID == "" || len(c.SessionID) > 128 || stream.Sender != (stream.Name != "stdin") {
				return nil, errors.New("transport stream identity or direction mismatch")
			}
			names[stream.Name] = true
			entry.Streams = append(entry.Streams, vm.TransportStreamIO{Name: stream.Name, Sender: stream.Sender, Cursor: vm.TransportCursor{StreamID: c.StreamID, SessionID: c.SessionID, Offset: c.Offset, FIN: c.FIN}})
		}
		result = append(result, entry)
	}
	return result, nil
}

func transportStreamsToWire(streams []vm.TransportStreamIO) []*ioproxy.TransportStream {
	result := make([]*ioproxy.TransportStream, 0, len(streams))
	for _, s := range streams {
		result = append(result, &ioproxy.TransportStream{Name: s.Name, Sender: s.Sender, Cursor: &ioproxy.TransportCursor{StreamID: s.Cursor.StreamID, SessionID: s.Cursor.SessionID, Offset: s.Cursor.Offset, FIN: s.Cursor.FIN}})
	}
	return result
}

func matchTransportCuts(host, guest []vm.TransportProcessIO) error {
	if len(host) != len(guest) {
		return errors.New("host and guest transport process registries differ")
	}
	byID := make(map[string]vm.TransportProcessIO)
	for _, p := range host {
		byID[p.TaskID+"/"+p.ExecID] = p
	}
	for _, p := range guest {
		h, ok := byID[p.TaskID+"/"+p.ExecID]
		if !ok || len(h.Streams) != len(p.Streams) {
			return fmt.Errorf("host and guest stream registry differs for %s/%s", p.TaskID, p.ExecID)
		}
		for _, g := range p.Streams {
			found := false
			for _, s := range h.Streams {
				if s.Name == g.Name {
					found = s.Sender != g.Sender && s.Cursor == g.Cursor
					break
				}
			}
			if !found {
				return fmt.Errorf("unacknowledged or mismatched stream cut %s", g.Cursor.StreamID)
			}
		}
	}
	return nil
}

// prepareIOTransport must run while workload cgroups are frozen and host task
// admission is closed. The peers keep receivers alive while senders drain.
// On error, the caller must resume both sides before thawing workload tasks.
func (s *service) prepareIOTransport(ctx context.Context) (*checkpointTransportCut, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- s.taskManager.PrepareIO(ctx) }()
	go func() { _, err := s.ioProxyClient.Prepare(ctx, &types.Empty{}); done <- err }()
	var combined error
	for range 2 {
		err := <-done
		combined = errors.Join(combined, err)
		if err != nil {
			cancel()
		}
	}
	if combined != nil {
		return nil, combined
	}
	host, err := s.taskManager.DescribeIO()
	if err != nil {
		return nil, err
	}
	response, err := s.ioProxyClient.Describe(ctx, &types.Empty{})
	if err != nil {
		return nil, err
	}
	guest, err := transportProcessesFromWire(response.Processes)
	if err != nil {
		return nil, err
	}
	if err := matchTransportCuts(host, guest); err != nil {
		return nil, err
	}
	if err := s.pauseEventForwarding(ctx); err != nil {
		return nil, err
	}
	cursor := uint64(0)
	if s.eventForwarder != nil {
		cursor = s.eventForwarder.Cursor()
	}
	return &checkpointTransportCut{Version: 1, Processes: guest, EventCursor: cursor}, nil
}

func (s *service) resumeIOTransport(ctx context.Context) error {
	_, err := s.ioProxyClient.Resume(ctx, &types.Empty{})
	// Even on a guest RPC failure, release the host read gate on an aborted
	// preparation. Retained frames remain bounded and reconnect without loss.
	s.taskManager.ResumeIO()
	return errors.Join(err, s.resumeEventForwarding(ctx))
}
