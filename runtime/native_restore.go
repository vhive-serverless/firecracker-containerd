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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/identifiers"
	"github.com/firecracker-microvm/firecracker-containerd/internal/bundle"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/internal/volfs"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	ioproxy "github.com/firecracker-microvm/firecracker-containerd/proto/service/ioproxy/ttrpc"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type nativeRestore struct {
	manifest  *nativeCheckpoint
	artifact  string
	workspace string
	files     map[string]string
}

func (s *service) prepareNativeRestore(ctx context.Context, incoming *proto.CreateVMRequest) (*proto.CreateVMRequest, error) {
	if !filepath.IsAbs(incoming.NativeCheckpointDirectory) || incoming.LoadSnapshot {
		return nil, status.Error(codes.InvalidArgument, "native restore requires an absolute artifact directory and cannot combine vHive restore")
	}
	root, err := os.OpenRoot(incoming.NativeCheckpointDirectory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open("manifest.json")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 128<<20 {
		return nil, syscall.EINVAL
	}
	decoder := json.NewDecoder(io.LimitReader(file, 128<<20+1))
	decoder.DisallowUnknownFields()
	manifest := &nativeCheckpoint{}
	if err := decoder.Decode(manifest); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, syscall.EINVAL
	}
	if err := validateNativeRestore(manifest); err != nil {
		return nil, err
	}
	if manifest.Namespace != s.namespace {
		return nil, status.Error(codes.InvalidArgument, "native restore namespace must match the captured namespace")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request := &proto.CreateVMRequest{}
	if err := json.Unmarshal(manifest.CreateRequest, request); err != nil {
		return nil, err
	}
	request.VMID = incoming.VMID
	request.TimeoutSeconds = incoming.TimeoutSeconds
	request.NativeCheckpointDirectory = incoming.NativeCheckpointDirectory
	request.LoadSnapshot = false
	request.MemFilePath, request.SnapshotPath, request.ContainerSnapshotPath = "", "", ""
	request.MemBackend = nil
	request.NetNS = incoming.NetNS
	if incoming.JailerConfig != nil {
		request.JailerConfig = incoming.JailerConfig
	}
	if request.JailerConfig != nil {
		request.JailerConfig.NetNS = incoming.GetJailerConfig().GetNetNS()
	}
	if len(manifest.Networks) > 0 {
		if len(incoming.NetworkInterfaces) != len(manifest.Networks) {
			return nil, status.Error(codes.InvalidArgument, "restore must supply replacement TAPs with the captured network identity")
		}
		for index, iface := range incoming.NetworkInterfaces {
			actual, err := networkConfigFromProto(iface, incoming.VMID)
			if err != nil {
				return nil, err
			}
			original := manifest.Networks[index]
			if actual.StaticConfiguration == nil || actual.CNIConfiguration != nil || actual.StaticConfiguration.MacAddress != original.MacAddress {
				return nil, status.Error(codes.InvalidArgument, "restore network MAC must match the captured identity")
			}
			before, _ := json.Marshal(original.IPConfiguration)
			after, _ := json.Marshal(actual.StaticConfiguration.IPConfiguration)
			if string(before) != string(after) {
				return nil, status.Error(codes.InvalidArgument, "restore IP configuration must match the captured identity")
			}
		}
		request.NetworkInterfaces = incoming.NetworkInterfaces
	} else {
		if len(incoming.NetworkInterfaces) != 0 {
			return nil, status.Error(codes.InvalidArgument, "cannot add a network device to a native snapshot")
		}
		request.NetworkInterfaces = make([]*proto.FirecrackerNetworkInterface, 0)
	}
	s.nativeRestore = &nativeRestore{manifest: manifest, artifact: incoming.NativeCheckpointDirectory, files: make(map[string]string)}
	return request, nil
}

func validateNativeRestore(manifest *nativeCheckpoint) error {
	if manifest.Version != 2 || manifest.Transport == nil || manifest.Transport.Version != 1 || manifest.VMConfig == nil || manifest.RuntimeConfig == nil || len(manifest.CreateRequest) == 0 || len(manifest.Drives) != len(manifest.VMConfig.Drives) || len(manifest.Drives) > 256 || len(manifest.Pmem) != len(manifest.VMConfig.Pmem) || len(manifest.Pmem) > 1 {
		return syscall.EINVAL
	}
	if !slices.Equal(manifest.ResumeTasks, manifest.Transport.ResumeTasks) {
		return syscall.EINVAL
	}
	if err := validateCheckpointTransport(manifest.Transport); err != nil {
		return err
	}
	if len(manifest.Transport.Processes) != len(manifest.Processes) {
		return syscall.EINVAL
	}
	for _, process := range manifest.Processes {
		if _, ok := checkpointProcessStreams(manifest.Transport, process.TaskID, process.ExecID); !ok {
			return syscall.EINVAL
		}
	}
	files := []nativeFile{manifest.Memory, manifest.State}
	drives := make(map[string]bool)
	rootCount := 0
	for index, drive := range manifest.Drives {
		live := manifest.VMConfig.Drives[index]
		if live == nil || live.DriveID == nil || *live.DriveID != drive.ID || drives[drive.ID] || live.IsReadOnly != drive.ReadOnly {
			return syscall.EINVAL
		}
		drives[drive.ID] = true
		if live.IsRootDevice != nil && *live.IsRootDevice {
			rootCount++
		}
		files = append(files, drive.File)
	}
	if len(manifest.Pmem) != 0 {
		request := &proto.CreateVMRequest{}
		if err := json.Unmarshal(manifest.CreateRequest, request); err != nil {
			return syscall.EINVAL
		}
		root := request.GetRootDrive()
		device, saved := manifest.VMConfig.Pmem[0], manifest.Pmem[0]
		if device == nil || device.ID == nil || *device.ID != pmemRootID || saved.ID != pmemRootID || !device.ReadOnly || !device.RootDevice || device.PathOnHost == nil || device.RateLimiter != nil || !root.GetUsePmem() || root.GetIsWritable() || root.GetImageSha256() != saved.File.SHA256 || saved.File.Path != "pmem/00000000" || saved.File.Size < 2<<20 || saved.File.Size%(2<<20) != 0 {
			return syscall.EINVAL
		}
		rootCount++
		files = append(files, saved.File)
	}
	if rootCount != 1 || manifest.Memory.Path != "memory" || manifest.State.Path != "state" {
		return syscall.EINVAL
	}
	names := make(map[string]bool)
	for _, file := range files {
		digest, err := hex.DecodeString(file.SHA256)
		if err != nil || len(digest) != 32 || file.Size < 0 || file.Size > 1<<40 || filepath.IsAbs(file.Path) || filepath.Clean(file.Path) != file.Path || strings.HasPrefix(file.Path, "../") || file.Path == ".." || names[file.Path] {
			return syscall.EINVAL
		}
		names[file.Path] = true
	}
	tasks := make(map[string]bool)
	for _, task := range manifest.Tasks {
		if err := identifiers.Validate(task.ID); err != nil {
			return err
		}
		if tasks[task.ID] || !json.Valid(task.Spec) {
			return syscall.EINVAL
		}
		tasks[task.ID] = true
	}
	processes := make(map[string]bool)
	for _, process := range manifest.Processes {
		if process.ExecID != "" {
			if err := identifiers.Validate(process.ExecID); err != nil {
				return err
			}
		}
		key := process.TaskID + "/" + process.ExecID
		if !tasks[process.TaskID] || process.State == nil || process.State.ID != process.TaskID || process.State.Pid == 0 || processes[key] {
			return syscall.EINVAL
		}
		processes[key] = true
	}
	for id := range tasks {
		if !processes[id+"/"] {
			return syscall.EINVAL
		}
	}
	for _, id := range manifest.ResumeTasks {
		if !tasks[id] {
			return syscall.EINVAL
		}
	}
	for id, slot := range manifest.ContainerSlots {
		if !tasks[id] || !drives[slot.DriveID] || slot.Mount == nil {
			return syscall.EINVAL
		}
	}
	return nil
}

func (s *service) cloneNativeRestore(ctx context.Context, request *proto.CreateVMRequest) (retErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	restore := s.nativeRestore
	restore.workspace = filepath.Join(s.jailer.JailPath().RootPath(), "native-restore")
	if err := os.MkdirAll(s.jailer.JailPath().RootPath(), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(restore.workspace, 0700); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, os.RemoveAll(restore.workspace))
			restore.files = make(map[string]string)
		}
	}()
	workspace, err := os.Open(restore.workspace)
	if err != nil {
		return err
	}
	defer workspace.Close()
	upper := ""
	for _, drive := range restore.manifest.Drives {
		if !drive.ReadOnly {
			// Even a RAM-upper control has persistent workload disks. Reject
			// unsuitable clone storage before copying memory, state or disks.
			if err := requireDiskBacking(workspace); err != nil {
				return err
			}
			if drive.ID == guestOverlayDrive {
				upper = drive.File.Path
			}
		}
	}
	if err := os.Mkdir(filepath.Join(restore.workspace, "drives"), 0700); err != nil {
		return err
	}
	if len(restore.manifest.Pmem) > 0 {
		if err := os.Mkdir(filepath.Join(restore.workspace, "pmem"), 0700); err != nil {
			return err
		}
	}
	for _, path := range []string{restore.workspace, filepath.Join(restore.workspace, "drives")} {
		if err := s.nativeFileOwner(path); err != nil {
			return err
		}
	}
	root, err := os.OpenRoot(restore.artifact)
	if err != nil {
		return err
	}
	defer root.Close()
	files := []nativeFile{restore.manifest.Memory, restore.manifest.State}
	for _, drive := range restore.manifest.Drives {
		files = append(files, drive.File)
	}
	for _, device := range restore.manifest.Pmem {
		files = append(files, device.File)
	}
	for _, saved := range files {
		input, err := root.Open(saved.Path)
		if err != nil {
			return err
		}
		info, err := input.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != saved.Size {
			_ = input.Close()
			return errors.Join(err, syscall.EINVAL)
		}
		copied, err := copyNativeFile(ctx, fmt.Sprintf("/proc/self/fd/%d", input.Fd()), restore.workspace, saved.Path)
		_ = input.Close()
		if err != nil {
			return err
		}
		if copied.SHA256 != saved.SHA256 || copied.Size != saved.Size {
			return fmt.Errorf("native artifact checksum mismatch for %s: %w", saved.Path, syscall.EIO)
		}
		path := filepath.Join(restore.workspace, saved.Path)
		if saved.Path == upper {
			// copyNativeFile preserves sparse workload disks. Only the guest
			// upper must retain the full capacity promised by fresh creation.
			file, err := os.OpenFile(path, os.O_RDWR|unix.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			err = requireDiskBacking(file)
			if err == nil {
				err = reserveGuestDisk(ctx, file, saved.Size)
			}
			if err == nil {
				err = file.Sync()
			}
			err = errors.Join(err, file.Close())
			if err != nil {
				return fmt.Errorf("reserve restored guest upper: %w", err)
			}
		}
		if err := s.nativeFileOwner(path); err != nil {
			return err
		}
		restore.files[saved.Path] = path
	}
	for index, drive := range restore.manifest.Drives {
		live := restore.manifest.VMConfig.Drives[index]
		if live.IsRootDevice != nil && *live.IsRootDevice {
			request.RootDrive = &proto.FirecrackerRootDrive{HostPath: restore.files[drive.File.Path], IsWritable: !drive.ReadOnly}
		}
	}
	if len(restore.manifest.Pmem) > 0 {
		file := restore.manifest.Pmem[0].File
		request.RootDrive = &proto.FirecrackerRootDrive{HostPath: restore.files[file.Path], UsePmem: true, ImageSha256: file.SHA256}
	}
	if len(request.DriveMounts) != len(restore.manifest.MountSlots) {
		return syscall.EINVAL
	}
	for index, slot := range restore.manifest.MountSlots {
		request.DriveMounts[index].HostPath = restore.drivePath(slot.DriveID)
	}
	if _, err := root.Stat("volumes/state.json"); err == nil {
		server, err := volfs.RestoreServer(ctx, filepath.Join(restore.artifact, "volumes"), filepath.Join(restore.workspace, "volumes"), volfs.CheckpointLimits{})
		if err != nil {
			return err
		}
		uid, gid := os.Geteuid(), os.Getegid()
		if jailer, ok := s.jailer.(*runcJailer); ok {
			uid, gid = int(jailer.Config.UID), int(jailer.Config.GID)
		}
		listener, err := volfs.ListenHost(s.jailer.JailPath().FirecrackerVSockPath(), uid, gid)
		if err != nil {
			_ = server.Close()
			return err
		}
		s.volumeServer = server
		go func() { _ = server.Serve(s.shimCtx, listener) }()
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *service) nativeFileOwner(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, option := range s.jailer.StubDrivesOptions() {
		if err := option(file); err != nil {
			return err
		}
	}
	return nil
}

func (r *nativeRestore) drivePath(id string) string {
	for _, drive := range r.manifest.Drives {
		if drive.ID == id {
			return r.files[drive.File.Path]
		}
	}
	return ""
}

func (s *service) nativeLoadOpt() fc.Opt {
	restore := s.nativeRestore
	return fc.WithSnapshot(restore.files["memory"], restore.files["state"])
}

func (s *service) nativeLoadHandler() fc.Handler {
	return fc.Handler{Name: "firecracker-containerd-native-restore", Fn: func(_ context.Context, machine *fc.Machine) error {
		apiPath := func(path string) (string, error) {
			if _, jailed := s.jailer.(*runcJailer); !jailed {
				return path, nil
			}
			relative, err := filepath.Rel(s.jailer.JailPath().RootPath(), path)
			if err != nil || strings.HasPrefix(relative, "..") {
				return "", syscall.EINVAL
			}
			return "/" + filepath.ToSlash(relative), nil
		}
		var err error
		machine.Cfg.Snapshot.MemFilePath, err = apiPath(s.nativeRestore.files["memory"])
		if err != nil {
			return err
		}
		machine.Cfg.Snapshot.SnapshotPath, err = apiPath(s.nativeRestore.files["state"])
		if err != nil {
			return err
		}
		machine.Cfg.Snapshot.VsockOverride = &models.VsockOverride{UdsPath: fc.String(machine.Cfg.VsockDevices[0].Path)}
		for index, network := range machine.Cfg.NetworkInterfaces {
			original := s.nativeRestore.manifest.VMConfig.NetworkInterfaces[index]
			machine.Cfg.Snapshot.NetworkOverrides = append(machine.Cfg.Snapshot.NetworkOverrides, &models.NetworkOverride{IfaceID: original.IfaceID, HostDevName: fc.String(network.StaticConfiguration.HostDevName)})
		}
		return nil
	}}
}

func (s *service) adoptNativeProcesses(ctx context.Context) error {
	restore := s.nativeRestore
	s.vsockIOPortCount = restore.manifest.VsockPortCount
	available := make(map[string]*stubDrive)
	for _, slot := range s.containerStubHandler.freeDrives {
		available[slot.driveID] = slot
	}
	s.containerStubHandler.freeDrives = nil
	for id, saved := range restore.manifest.ContainerSlots {
		slot := available[saved.DriveID]
		if slot == nil {
			return fmt.Errorf("missing restored drive slot %s", saved.DriveID)
		}
		restoredSlot := slot.withMountConfig(restore.drivePath(saved.DriveID), saved.Mount.VMPath, saved.Mount.FilesystemType, saved.Mount.Options)
		s.containerStubHandler.usedDrives[id] = &restoredSlot
	}
	for _, saved := range restore.manifest.FreeSlots {
		slot := available[saved.DriveID]
		if slot == nil {
			return fmt.Errorf("missing free restored drive slot %s", saved.DriveID)
		}
		s.containerStubHandler.freeDrives = append(s.containerStubHandler.freeDrives, slot)
	}
	for _, saved := range restore.manifest.Tasks {
		dir := bundle.Dir(filepath.Join(s.shimDir.RootPath(), "native-bundles", saved.ID))
		if err := os.MkdirAll(dir.RootPath(), 0700); err != nil {
			return err
		}
		if err := dir.OCIConfig().Write(saved.Spec); err != nil {
			return err
		}
		if err := s.shimDir.CreateBundleLink(saved.ID, dir); err != nil {
			return err
		}
		if saved.BlockDevice {
			s.blockDeviceTasks.Store(saved.ID, struct{}{})
		}
		if len(saved.VolumeTokens) > 0 {
			s.volumeTasks.Store(saved.ID, append([]string(nil), saved.VolumeTokens...))
		}
	}
	cloneSession := rand.Text()
	for _, saved := range restore.manifest.Processes {
		streams, found := checkpointProcessStreams(restore.manifest.Transport, saved.TaskID, saved.ExecID)
		if !found {
			return fmt.Errorf("missing captured IO for %s/%s", saved.TaskID, saved.ExecID)
		}
		streams = append([]vm.TransportStreamIO(nil), streams...)
		for index := range streams {
			streams[index].Cursor.SessionID = cloneSession
		}
		base := filepath.Join(s.shimDir.RootPath(), "native-io", saved.TaskID, "init")
		if saved.ExecID != "" {
			base = filepath.Join(s.shimDir.RootPath(), "native-io", saved.TaskID, "exec-"+saved.ExecID)
		}
		if err := os.MkdirAll(base, 0700); err != nil {
			return err
		}
		fifos := cio.Config{Terminal: saved.FIFOs.Terminal}
		for _, stream := range []struct {
			original string
			target   *string
			name     string
		}{{saved.FIFOs.Stdin, &fifos.Stdin, "stdin"}, {saved.FIFOs.Stdout, &fifos.Stdout, "stdout"}, {saved.FIFOs.Stderr, &fifos.Stderr, "stderr"}} {
			if stream.original == "" {
				continue
			}
			*stream.target = filepath.Join(base, stream.name)
			if err := syscall.Mkfifo(*stream.target, 0600); err != nil {
				return err
			}
		}
		proxy := vm.NewNullIOProxy()
		if len(streams) > 0 {
			attach := &ioproxy.AttachRequest{ID: saved.TaskID, ExecID: saved.ExecID, StdinPort: s.nextVSockPort(), StdoutPort: s.nextVSockPort(), StderrPort: s.nextVSockPort(), Streams: transportStreamsToWire(streams)}
			if _, err := s.ioProxyClient.Attach(ctx, attach); err != nil {
				return err
			}
			var err error
			proxy, err = s.newIOProxyWithCut(s.logger, fifos.Stdin, fifos.Stdout, fifos.Stderr, &proto.ExtraData{StdinPort: attach.StdinPort, StdoutPort: attach.StdoutPort, StderrPort: attach.StderrPort}, streams)
			if err != nil {
				return err
			}
		}
		if _, err := s.taskManager.AdoptProcess(ctx, vm.RestoredProcess{TaskID: saved.TaskID, ExecID: saved.ExecID, PID: saved.State.Pid}, s.agentClient, proxy); err != nil {
			return err
		}
		if err := s.addFIFOs(saved.TaskID, saved.ExecID, fifos); err != nil {
			return err
		}
		s.restoredIO = append(s.restoredIO, &proto.RestoredProcessIO{ID: saved.TaskID, ExecID: saved.ExecID, Stdin: fifos.Stdin, Stdout: fifos.Stdout, Stderr: fifos.Stderr, Terminal: fifos.Terminal})
	}
	if len(restore.manifest.Metadata) > 0 && string(restore.manifest.Metadata) != "null" {
		if err := s.machine.SetMetadata(ctx, restore.manifest.Metadata); err != nil {
			return err
		}
	}
	if err := s.resumeIOTransport(ctx); err != nil {
		return err
	}
	for _, id := range restore.manifest.ResumeTasks {
		if _, err := s.agentClient.Resume(ctx, &taskAPI.ResumeRequest{ID: id}); err != nil {
			return err
		}
	}
	return nil
}
