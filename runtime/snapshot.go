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
	"strconv"
	"time"

	"github.com/containerd/containerd/v2/pkg/protobuf/types"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	ops "github.com/firecracker-microvm/firecracker-go-sdk/client/operations"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validateOrchestratorNetNS(req *proto.CreateVMRequest) error {
	if req.NetNS == "" {
		return nil
	}
	if other := req.GetJailerConfig().GetNetNS(); other != "" && other != req.NetNS {
		return status.Error(codes.InvalidArgument, "conflicting VM and jailer network namespaces")
	}
	if !filepath.IsAbs(req.NetNS) {
		return status.Error(codes.InvalidArgument, "VM network namespace path must be absolute")
	}
	netns, err := ns.GetNS(req.NetNS)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "open VM network namespace: %v", err)
	}
	return netns.Close()
}

func validateSnapshotRestore(req *proto.CreateVMRequest) error {
	if !req.LoadSnapshot {
		return nil
	}
	if req.JailerConfig != nil || len(req.DriveMounts) != 0 || req.ContainerCount > 1 ||
		req.GetRootDrive().GetIsWritable() || req.BalloonDevice != nil {
		return status.Error(codes.InvalidArgument, "vHive restore requires an unjailed VM with a read-only root and one workload drive")
	}
	if !filepath.IsAbs(req.SnapshotPath) || !filepath.IsAbs(req.ContainerSnapshotPath) {
		return status.Error(codes.InvalidArgument, "snapshot and workload drive paths must be absolute")
	}
	if backend := req.MemBackend; backend != nil {
		if req.MemFilePath != "" || !filepath.IsAbs(backend.BackendPath) ||
			(backend.BackendType != models.MemoryBackendBackendTypeFile && backend.BackendType != models.MemoryBackendBackendTypeUffd) {
			return status.Error(codes.InvalidArgument, "specify exactly one memory file or File/Uffd backend with an absolute path")
		}
	} else if !filepath.IsAbs(req.MemFilePath) {
		return status.Error(codes.InvalidArgument, "memory file path must be absolute")
	}
	return nil
}

// snapshotRestoreOpt translates the legacy vHive request to explicit drive IDs.
// The caller provides the restored workload backing; the guest root is immutable.
func snapshotRestoreOpt(cfg *firecracker.Config, req *proto.CreateVMRequest) firecracker.Opt {
	configure := func(snapshot *firecracker.SnapshotConfig) {
		snapshot.ResumeVM = true
		snapshot.EnableDiffSnapshots = req.EnableDiffSnapshots
		snapshot.VsockOverride = &models.VsockOverride{UdsPath: firecracker.String(cfg.VsockDevices[0].Path)}
	}
	if backend := req.MemBackend; backend != nil {
		return firecracker.WithSnapshot("", req.SnapshotPath, configure,
			firecracker.WithMemoryBackend(backend.BackendType, backend.BackendPath))
	}
	return firecracker.WithSnapshot(req.MemFilePath, req.SnapshotPath, configure)
}

// populateRestoreTaps runs after SDK network setup. The guest network identity
// stays in the snapshot; only the host TAP binding may change.
var populateRestoreTaps = firecracker.Handler{
	Name: "firecracker-containerd-restore-taps",
	Fn: func(_ context.Context, machine *firecracker.Machine) error {
		overrides := make([]*models.NetworkOverride, 0, len(machine.Cfg.NetworkInterfaces))
		for i, iface := range machine.Cfg.NetworkInterfaces {
			if iface.StaticConfiguration == nil || iface.StaticConfiguration.HostDevName == "" {
				return fmt.Errorf("missing TAP for snapshot network interface %d", i+1)
			}
			overrides = append(overrides, &models.NetworkOverride{
				IfaceID: firecracker.String(strconv.Itoa(i + 1)), HostDevName: firecracker.String(iface.StaticConfiguration.HostDevName),
			})
		}
		machine.Cfg.Snapshot.NetworkOverrides = overrides
		return nil
	},
}

// CreateSnapshot saves a paused vHive VM. vHive separately captures the
// devmapper disk and passes its independent backing on restore.
func (s *service) CreateSnapshot(ctx context.Context, req *proto.CreateSnapshotRequest) (_ *types.Empty, retErr error) {
	if err := s.lockSnapshotTransition(ctx); err != nil {
		return nil, err
	}
	defer s.snapshotMu.Unlock()
	defer func() {
		if retErr != nil && s.pendingPause != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			retErr = errors.Join(retErr, s.resumePreparedVM(cleanup))
		}
	}()
	if !filepath.IsAbs(req.MemFilePath) || !filepath.IsAbs(req.SnapshotPath) ||
		filepath.Clean(req.MemFilePath) != req.MemFilePath || filepath.Clean(req.SnapshotPath) != req.SnapshotPath ||
		req.MemFilePath == req.SnapshotPath || req.MemFilePath == req.SnapshotPath+snapshotDriveMetadataSuffix {
		return nil, status.Error(codes.InvalidArgument, "memory and snapshot paths must be distinct absolute paths")
	}
	if err := s.waitVMReady(); err != nil {
		return nil, err
	}
	if _, ok := s.jailer.(*noopJailer); !ok || len(s.machineConfig.PmemDevices) != 0 || len(s.machineConfig.Drives) != 2 || !s.machineConfig.Drives[0].IsReadOnly {
		return nil, status.Error(codes.FailedPrecondition, "snapshot requires an unjailed VM with a read-only root and one workload drive")
	}
	if s.pendingPause == nil || s.pendingPause.Cut == nil || !s.pendingPause.VMPaused {
		return nil, status.Error(codes.FailedPrecondition, "snapshot requires a prepared PauseVM transport barrier")
	}
	api := firecracker.NewClient(s.apiSocketPath, s.logger, false)
	exported, err := api.GetExportVMConfig(func(params *ops.GetExportVMConfigParams) { params.SetContext(ctx) })
	if err != nil {
		return nil, err
	}
	metadata, err := snapshotDrivePaths(exported.Payload, false)
	if err != nil {
		return nil, err
	}
	metadata.Transport = s.pendingPause.Cut
	if len(metadata.Drives) != 2 {
		return nil, status.Error(codes.FailedPrecondition, "legacy snapshot requires exactly two captured drives")
	}
	// Stage inside the per-VM directory, which remains writable and visible
	// even when this VMM was itself restored into a private filesystem root.
	stage, err := os.MkdirTemp(s.jailer.JailPath().RootPath(), ".legacy-state-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	s.pendingPause.MayReset = true
	if err := s.machine.CreateSnapshot(ctx, restoreAPIPath(s.jailer, filepath.Join(stage, "memory")), restoreAPIPath(s.jailer, filepath.Join(stage, "state"))); err != nil {
		return nil, err
	}
	// Invalidate any old publication marker before replacing either payload.
	// Failure leaves an explicitly unusable artifact, never a stale valid cut.
	if err := os.Remove(req.SnapshotPath + snapshotDriveMetadataSuffix); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := syncSnapshotDirectory(filepath.Dir(req.SnapshotPath)); err != nil {
		return nil, err
	}
	if err := publishSnapshotFile(ctx, filepath.Join(stage, "memory"), req.MemFilePath); err != nil {
		return nil, err
	}
	if err := publishSnapshotFile(ctx, filepath.Join(stage, "state"), req.SnapshotPath); err != nil {
		return nil, err
	}
	if err := publishSnapshotDriveMetadata(req.SnapshotPath, metadata); err != nil {
		return nil, err
	}
	return &types.Empty{}, nil
}

func publishSnapshotFile(ctx context.Context, source, destination string) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".snapshot-publish-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Remove(name); err != nil {
		return err
	}
	if _, err := copySnapshotFile(ctx, source, filepath.Dir(name), filepath.Base(name)); err != nil {
		return err
	}
	return os.Rename(name, destination)
}

// The sidecar is the publication marker and must travel with the state file.
// Older snapshots without one cannot safely infer original disk paths.
func publishSnapshotDriveMetadata(state string, metadata *snapshotDriveMetadata) error {
	file, err := os.CreateTemp(filepath.Dir(state), ".snapshot-drives-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(metadata)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), state+snapshotDriveMetadataSuffix)
}
