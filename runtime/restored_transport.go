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
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	ioproxy "github.com/firecracker-microvm/firecracker-containerd/proto/service/ioproxy/ttrpc"
)

// adoptLegacyTransport reconnects already-existing guest processes without
// creating or restarting their workloads. It uses the shared legacy transport
// sidecar; it does not invoke the native CheckpointVM API.
func (s *service) adoptLegacyTransport(ctx context.Context) error {
	cut := s.legacyRestoreCut
	if err := validateCheckpointTransport(cut); err != nil {
		return err
	}
	// The captured workload already occupies the legacy profile's sole slot.
	// It must not be offered to a new Create while that workload is alive.
	s.containerStubHandler.freeDrives = nil
	session := rand.Text()
	for _, process := range cut.Processes {
		state, err := s.agentClient.State(ctx, &taskAPI.StateRequest{ID: process.TaskID, ExecID: process.ExecID})
		if err != nil {
			return err
		}
		base := filepath.Join(s.shimDir.RootPath(), "restored-io", process.TaskID, "init")
		if process.ExecID != "" {
			base = filepath.Join(s.shimDir.RootPath(), "restored-io", process.TaskID, "exec-"+process.ExecID)
		}
		if err := os.MkdirAll(base, 0700); err != nil {
			return err
		}
		fifos := cio.Config{Terminal: state.Terminal}
		streams := append([]vm.TransportStreamIO(nil), process.Streams...)
		for index := range streams {
			streams[index].Cursor.SessionID = session
			path := filepath.Join(base, streams[index].Name)
			if err := syscall.Mkfifo(path, 0600); err != nil {
				return err
			}
			switch streams[index].Name {
			case transportStdin:
				fifos.Stdin = path
			case transportStdout:
				fifos.Stdout = path
			case transportStderr:
				fifos.Stderr = path
			}
		}
		proxy := vm.NewNullIOProxy()
		if len(streams) > 0 {
			attach := &ioproxy.AttachRequest{ID: process.TaskID, ExecID: process.ExecID, StdinPort: s.nextVSockPort(), StdoutPort: s.nextVSockPort(), StderrPort: s.nextVSockPort(), Streams: transportStreamsToWire(streams)}
			if _, err := s.ioProxyClient.Attach(ctx, attach); err != nil {
				return err
			}
			proxy, err = s.newIOProxyWithCut(s.logger, fifos.Stdin, fifos.Stdout, fifos.Stderr, &proto.ExtraData{StdinPort: attach.StdinPort, StdoutPort: attach.StdoutPort, StderrPort: attach.StderrPort}, streams)
			if err != nil {
				return err
			}
		}
		if _, err := s.taskManager.AdoptProcess(ctx, vm.RestoredProcess{TaskID: process.TaskID, ExecID: process.ExecID, PID: state.Pid}, s.agentClient, proxy); err != nil {
			return err
		}
		if process.ExecID == "" {
			s.legacyRestoredTasks.Store(process.TaskID, struct{}{})
		}
		if err := s.addFIFOs(process.TaskID, process.ExecID, fifos); err != nil {
			return err
		}
		s.restoredIO = append(s.restoredIO, &proto.RestoredProcessIO{ID: process.TaskID, ExecID: process.ExecID, Stdin: fifos.Stdin, Stdout: fifos.Stdout, Stderr: fifos.Stderr, Terminal: fifos.Terminal})
	}
	if err := s.resumeIOTransport(ctx); err != nil {
		return err
	}
	for _, id := range cut.ResumeTasks {
		if _, err := s.agentClient.Resume(ctx, &taskAPI.ResumeRequest{ID: id}); err != nil {
			return err
		}
	}
	return nil
}

func (s *service) verifyRestoredEventCursor(ctx context.Context) error {
	cut := s.legacyRestoreCut
	if s.nativeRestore != nil {
		cut = s.nativeRestore.manifest.Transport
	}
	if cut == nil {
		return nil
	}
	cursor, err := s.journalClient().Cursor(ctx)
	if err != nil {
		return err
	}
	if cursor != cut.EventCursor {
		return fmt.Errorf("guest event cursor %d differs from captured cut %d", cursor, cut.EventCursor)
	}
	return nil
}
