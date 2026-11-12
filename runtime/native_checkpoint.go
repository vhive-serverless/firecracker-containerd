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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/containerd/api/types/task"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/protobuf/types"
	"github.com/firecracker-microvm/firecracker-containerd/config"
	"github.com/firecracker-microvm/firecracker-containerd/internal/volfs"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	ops "github.com/firecracker-microvm/firecracker-go-sdk/client/operations"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type nativeDrive struct {
	ID       string
	ReadOnly bool
	File     snapshotFile
}

type nativeProcess struct {
	TaskID, ExecID string
	State          *taskAPI.StateResponse
	FIFOs          cio.Config
}

type nativeTask struct {
	ID           string
	BundlePath   string
	Spec         json.RawMessage
	BlockDevice  bool
	VolumeTokens []string
}

type nativeSlot struct {
	DriveID string
	Mount   *proto.FirecrackerDriveMount
}

type nativeCheckpoint struct {
	Version        int
	VMID           string
	Namespace      string
	CreateRequest  json.RawMessage
	RuntimeConfig  *config.Config
	VMConfig       *models.FullVMConfiguration
	Instance       models.InstanceInfo
	Networks       []fc.StaticNetworkConfiguration
	NetNS          string
	Metadata       json.RawMessage
	Processes      []nativeProcess
	Tasks          []nativeTask
	ContainerSlots map[string]nativeSlot
	FreeSlots      []nativeSlot
	MountSlots     []nativeSlot
	ResumeTasks    []string
	VsockPortCount uint32
	Drives         []nativeDrive
	Memory, State  snapshotFile
	Transport      *checkpointTransportCut
}

// CheckpointVM coordinates a native VM capture. All external writers of exported
// volumes must also be quiescent; the VM-scoped gate cannot stop those writers.
// Publication happens only after source execution has been resumed successfully.
func (s *service) CheckpointVM(ctx context.Context, req *proto.CheckpointVMRequest) (_ *types.Empty, retErr error) {
	if req.VMID != s.vmID || !filepath.IsAbs(req.Directory) || filepath.Clean(req.Directory) == "/" {
		return nil, status.Error(codes.InvalidArgument, "checkpoint requires this VM's ID and an absolute new directory")
	}
	requestedDirectory := filepath.Clean(req.Directory)
	if err := s.waitVMReady(); err != nil {
		return nil, err
	}
	if err := s.lockSnapshotTransition(ctx); err != nil {
		return nil, err
	}
	defer s.snapshotMu.Unlock()
	if s.pendingPause != nil {
		return nil, status.Error(codes.FailedPrecondition, "previous pause recovery must finish before checkpoint")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(requestedDirectory); !os.IsNotExist(err) {
		if err == nil {
			return nil, status.Error(codes.AlreadyExists, "checkpoint destination exists")
		}
		return nil, err
	}
	paused, err := s.isPaused(ctx)
	if err != nil {
		return nil, err
	}
	if paused {
		return nil, status.Error(codes.FailedPrecondition, "native capture requires a running VM")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(requestedDirectory))
	if err != nil {
		return nil, err
	}
	destination := filepath.Join(parent, filepath.Base(requestedDirectory))
	if err := rejectNativeOwnedDirectory(parent, s.shimDir.RootPath()); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(parent, ".fc-checkpoint-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if stage != "" {
			retErr = errors.Join(retErr, os.RemoveAll(stage))
		}
	}()
	manifest, err := s.nativeMetadata(ctx)
	if err != nil {
		return nil, err
	}
	for _, task := range manifest.Tasks {
		if err := rejectNativeOwnedDirectory(parent, task.BundlePath); err != nil {
			return nil, err
		}
	}
	var frozen *volfs.FrozenServer
	pending := &pendingVMPause{}
	s.pendingPause = pending
	thaw := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		var cleanupErr error
		if frozen != nil {
			cleanupErr = errors.Join(cleanupErr, frozen.Close())
			frozen = nil
		}
		if s.pendingPause != nil {
			cleanupErr = errors.Join(cleanupErr, s.resumePreparedVM(cleanupCtx))
		}
		return cleanupErr
	}
	defer func() { retErr = errors.Join(retErr, thaw()) }()
	for _, process := range manifest.Processes {
		if process.ExecID == "" && process.State.Status == task.Status_RUNNING {
			pending.Tasks = append(pending.Tasks, process.TaskID)
			if _, err := s.agentClient.Pause(ctx, &taskAPI.PauseRequest{ID: process.TaskID}); err != nil {
				return nil, err
			}
		}
	}
	manifest.ResumeTasks = append([]string(nil), pending.Tasks...)
	pending.IOPrepared = true
	manifest.Transport, err = s.sealIOTransport(ctx)
	if err != nil {
		return nil, err
	}
	pending.Cut = manifest.Transport
	manifest.Transport.ResumeTasks = append([]string(nil), pending.Tasks...)
	pending.VMPaused = true
	if err := s.machine.PauseVM(ctx); err != nil {
		return nil, err
	}
	s.volumeMu.Lock()
	server := s.volumeServer
	s.volumeMu.Unlock()
	if server != nil {
		frozen, err = server.Freeze(ctx)
		if err != nil {
			return nil, err
		}
		if err := frozen.Save(ctx, filepath.Join(stage, "volumes"), volfs.CheckpointLimits{}); err != nil {
			return nil, err
		}
	}
	pending.MayReset = true
	if err := s.nativeVMFiles(ctx, stage, manifest); err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(stage, "drives"), 0700); err != nil {
		return nil, err
	}
	for index, drive := range manifest.VMConfig.Drives {
		if drive == nil || drive.DriveID == nil {
			return nil, fmt.Errorf("missing live drive identity")
		}
		path, remapped := restoreBackingPath(s.jailer, drive.PathOnHost)
		if !remapped {
			path = drive.PathOnHost
			if _, jailed := s.jailer.(*runcJailer); jailed {
				path = filepath.Join(s.jailer.JailPath().RootPath(), strings.TrimPrefix(path, "/"))
			}
		}
		file, err := copySnapshotFile(ctx, path, stage, fmt.Sprintf("drives/%08x", index))
		if err != nil {
			return nil, fmt.Errorf("capture drive %s: %w", *drive.DriveID, err)
		}
		manifest.Drives = append(manifest.Drives, nativeDrive{ID: *drive.DriveID, ReadOnly: drive.IsReadOnly, File: file})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	if len(data) > 128<<20 {
		return nil, unix.E2BIG
	}
	file, err := os.OpenFile(filepath.Join(stage, "manifest.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return nil, err
	}
	for _, path := range []string{filepath.Join(stage, "drives"), stage} {
		if err := syncSnapshotDirectory(path); err != nil {
			return nil, err
		}
	}
	if err := thaw(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil {
		return nil, err
	}
	stage = ""
	if err := syncSnapshotDirectory(parent); err != nil {
		return nil, err
	}
	return &types.Empty{}, nil
}

func (s *service) nativeMetadata(ctx context.Context) (*nativeCheckpoint, error) {
	client := fc.NewClient(s.apiSocketPath, s.logger, false)
	response, err := client.GetExportVMConfig(func(params *ops.GetExportVMConfigParams) { params.SetContext(ctx) })
	if err != nil {
		return nil, err
	}
	manifest := &nativeCheckpoint{Version: 2, VMID: s.vmID, Namespace: s.namespace, CreateRequest: append(json.RawMessage(nil), s.vmRequest...), RuntimeConfig: s.config,
		VMConfig: response.Payload, NetNS: s.machine.Cfg.NetNS, ContainerSlots: make(map[string]nativeSlot)}
	for _, network := range s.machine.Cfg.NetworkInterfaces {
		if network.StaticConfiguration == nil {
			return nil, fmt.Errorf("VM network configuration has not been resolved")
		}
		manifest.Networks = append(manifest.Networks, *network.StaticConfiguration)
	}
	manifest.Instance, err = s.machine.DescribeInstanceInfo(ctx)
	if err != nil {
		return nil, err
	}
	s.vsockPortMu.Lock()
	manifest.VsockPortCount = s.vsockIOPortCount
	s.vsockPortMu.Unlock()
	if err := s.machine.GetMetadata(ctx, &manifest.Metadata); err != nil {
		var absent *ops.GetMmdsDefault
		if !errors.As(err, &absent) || absent.Code() != 400 || absent.Payload == nil || absent.Payload.FaultMessage != "MMDS error: The MMDS data store is not initialized." {
			return nil, err
		}
	}
	s.fifosMu.Lock()
	for id, execs := range s.fifos {
		for execID, fifos := range execs {
			manifest.Processes = append(manifest.Processes, nativeProcess{TaskID: id, ExecID: execID, FIFOs: fifos})
		}
	}
	s.fifosMu.Unlock()
	sort.Slice(manifest.Processes, func(i, j int) bool {
		a, b := manifest.Processes[i], manifest.Processes[j]
		if a.TaskID == b.TaskID {
			return a.ExecID < b.ExecID
		}
		return a.TaskID < b.TaskID
	})
	for index := range manifest.Processes {
		process := &manifest.Processes[index]
		process.State, err = s.agentClient.State(ctx, &taskAPI.StateRequest{ID: process.TaskID, ExecID: process.ExecID})
		if err != nil {
			return nil, err
		}
		process.FIFOs.Terminal = process.State.Terminal
		if process.State.Status != task.Status_RUNNING && process.State.Status != task.Status_PAUSED && process.State.Status != task.Status_CREATED && process.State.Status != task.Status_STOPPED {
			return nil, fmt.Errorf("task %s/%s is transitioning", process.TaskID, process.ExecID)
		}
		if process.ExecID != "" {
			continue
		}
		bundle, err := s.shimDir.BundleLink(process.TaskID)
		if err != nil {
			return nil, err
		}
		spec, err := bundle.OCIConfig().Bytes()
		if err != nil {
			return nil, err
		}
		_, block := s.blockDeviceTasks.Load(process.TaskID)
		bundlePath, err := filepath.EvalSymlinks(bundle.RootPath())
		if err != nil {
			return nil, err
		}
		entry := nativeTask{ID: process.TaskID, BundlePath: bundlePath, Spec: spec, BlockDevice: block}
		if tokens, ok := s.volumeTasks.Load(process.TaskID); ok {
			entry.VolumeTokens = append([]string(nil), tokens.([]string)...)
		}
		manifest.Tasks = append(manifest.Tasks, entry)
	}
	s.containerStubHandler.mu.Lock()
	for id, slot := range s.containerStubHandler.usedDrives {
		manifest.ContainerSlots[id] = nativeSlot{DriveID: slot.driveID, Mount: slot.driveMount}
	}
	for _, slot := range s.containerStubHandler.freeDrives {
		manifest.FreeSlots = append(manifest.FreeSlots, nativeSlot{DriveID: slot.driveID, Mount: slot.driveMount})
	}
	s.containerStubHandler.mu.Unlock()
	for _, drive := range s.driveMountStubs {
		slot, ok := drive.(stubDrive)
		if !ok {
			return nil, fmt.Errorf("unsupported fixed drive implementation %T", drive)
		}
		manifest.MountSlots = append(manifest.MountSlots, nativeSlot{DriveID: slot.driveID, Mount: slot.driveMount})
	}
	return manifest, nil
}

func (s *service) nativeVMFiles(ctx context.Context, stage string, manifest *nativeCheckpoint) error {
	directory, err := os.MkdirTemp(s.jailer.JailPath().RootPath(), ".native-state-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	for _, option := range s.jailer.StubDrivesOptions() {
		if err = option(file); err != nil {
			break
		}
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	apiDirectory := directory
	if _, jailed := s.jailer.(*runcJailer); jailed {
		rel, err := filepath.Rel(s.jailer.JailPath().RootPath(), directory)
		if err != nil {
			return err
		}
		apiDirectory = "/" + rel
	}
	if err := s.machine.CreateSnapshot(ctx, filepath.Join(apiDirectory, "memory"), filepath.Join(apiDirectory, "state")); err != nil {
		return err
	}
	manifest.Memory, err = copySnapshotFile(ctx, filepath.Join(directory, "memory"), stage, "memory")
	if err != nil {
		return err
	}
	manifest.State, err = copySnapshotFile(ctx, filepath.Join(directory, "state"), stage, "state")
	return err
}

func rejectNativeOwnedDirectory(parent, owned string) error {
	root, err := filepath.EvalSymlinks(owned)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, parent)
	if err != nil {
		return err
	}
	if relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return status.Error(codes.InvalidArgument, "checkpoint destination must outlive VM and task cleanup directories")
	}
	return nil
}
