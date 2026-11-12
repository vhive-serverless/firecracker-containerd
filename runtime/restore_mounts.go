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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd/v2/pkg/identifiers"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
)

const snapshotDriveMetadataSuffix = ".drives.json"

type snapshotDriveMetadata struct {
	Transport *checkpointTransportCut `json:"transport"`
	Version   int                     `json:"version"`
	Drives    []snapshotDrivePath     `json:"drives"`
}

type snapshotDrivePath struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only"`
	Root     bool   `json:"root"`
}

// restoreMount maps a VMM-visible captured filename to this restore's backing.
// Targets are interpreted only inside the new VMM's private filesystem.
type restoreMount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

func capturedDevicePath(path string, jailed bool) (string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || filepath.Clean(path) != path || path == "." || path == "/" || path == ".." || strings.HasPrefix(path, "../") {
		return "", fmt.Errorf("invalid captured device path %q", path)
	}
	if !filepath.IsAbs(path) {
		if !jailed {
			return "", fmt.Errorf("unjailed captured device path must be absolute: %q", path)
		}
		path = "/" + path
	}
	for _, reserved := range []string{restoreKVMDevice, restoreTunDevice, restoreNullDevice, restoreZeroDevice, restoreRandomDevice, restoreURandomDevice, restoreProcDirectory, "/sys", restoreExecutable, "/firecracker", "/api.socket", "/fc-vsock.sock"} {
		if pathWithin(path, reserved) || pathWithin(reserved, path) {
			return "", fmt.Errorf("captured device path overlaps reserved VMM path %q", path)
		}
	}
	return path, nil
}

func snapshotDrivePaths(config *models.FullVMConfiguration, jailed bool) (*snapshotDriveMetadata, error) {
	metadata := &snapshotDriveMetadata{Version: 2}
	ids, paths := make(map[string]bool), make(map[string]bool)
	if config == nil || len(config.Drives) == 0 || len(config.Drives) > 256 {
		return nil, fmt.Errorf("missing or oversized captured drive configuration")
	}
	for _, drive := range config.Drives {
		if drive == nil || drive.DriveID == nil || identifiers.Validate(*drive.DriveID) != nil || ids[*drive.DriveID] {
			return nil, fmt.Errorf("invalid or duplicate captured drive identity")
		}
		path, err := capturedDevicePath(drive.PathOnHost, jailed)
		if err != nil {
			return nil, err
		}
		if paths[path] {
			return nil, fmt.Errorf("duplicate captured drive path %q", path)
		}
		ids[*drive.DriveID], paths[path] = true, true
		metadata.Drives = append(metadata.Drives, snapshotDrivePath{ID: *drive.DriveID, Path: path, ReadOnly: drive.IsReadOnly, Root: drive.IsRootDevice != nil && *drive.IsRootDevice})
	}
	return metadata, nil
}

func readSnapshotDriveMetadata(state string) (*snapshotDriveMetadata, error) {
	path := state + snapshotDriveMetadataSuffix
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("restore requires snapshot drive metadata %s: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("invalid snapshot drive metadata file")
	}
	metadata := &snapshotDriveMetadata{}
	decoder := json.NewDecoder(io.LimitReader(file, (1<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(metadata); err != nil {
		return nil, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || metadata.Version != 2 || len(metadata.Drives) != 2 {
		return nil, fmt.Errorf("unsupported snapshot drive metadata")
	}
	if err := validateCheckpointTransport(metadata.Transport); err != nil {
		return nil, err
	}
	ids, paths := make(map[string]bool), make(map[string]bool)
	roots, workloads := 0, 0
	for _, drive := range metadata.Drives {
		path, err := capturedDevicePath(drive.Path, false)
		if err != nil {
			return nil, err
		}
		if identifiers.Validate(drive.ID) != nil || ids[drive.ID] || paths[path] {
			return nil, fmt.Errorf("duplicate or invalid snapshot drive identity/path")
		}
		ids[drive.ID], paths[path] = true, true
		switch {
		case drive.Root && drive.ReadOnly:
			roots++
		case !drive.Root && !drive.ReadOnly:
			workloads++
		default:
			return nil, fmt.Errorf("legacy snapshot requires a read-only root and writable workload")
		}
	}
	if roots != 1 || workloads != 1 {
		return nil, fmt.Errorf("legacy snapshot requires exactly one root and workload drive")
	}
	return metadata, nil
}

// prepareSnapshotMounts must run after clone/configuration and before jailer
// options are built. No mounts or original-path filesystem changes happen here.
func (s *service) prepareSnapshotMounts(request *proto.CreateVMRequest) error {
	if !request.LoadSnapshot {
		return nil
	}
	var mounts []restoreMount
	var inputs []string
	metadata, err := readSnapshotDriveMetadata(request.SnapshotPath)
	if err != nil {
		return err
	}
	s.legacyRestoreCut = metadata.Transport
	for _, drive := range metadata.Drives {
		source := request.ContainerSnapshotPath
		expectedID := stubPathToDriveID("ctrstub0")
		if drive.Root {
			if len(s.machineConfig.Drives) == 0 || s.machineConfig.Drives[0].DriveID == nil {
				return fmt.Errorf("missing restore root drive")
			}
			source, expectedID = s.machineConfig.Drives[0].PathOnHost, *s.machineConfig.Drives[0].DriveID
		}
		if drive.ID != expectedID {
			return fmt.Errorf("captured drive ID %q differs from restore device %q", drive.ID, expectedID)
		}
		mounts = append(mounts, restoreMount{Source: source, Target: drive.Path, ReadOnly: drive.ReadOnly})
	}
	inputs = append(inputs, request.SnapshotPath)
	if request.MemBackend != nil {
		inputs = append(inputs, request.MemBackend.BackendPath)
	} else {
		inputs = append(inputs, request.MemFilePath)
	}
	if err := validateRestoreMounts(mounts, s.jailer.JailPath().RootPath()); err != nil {
		return err
	}
	switch jailer := s.jailer.(type) {
	case *noopJailer:
		jailer.restoreMounts, jailer.restoreInputs = mounts, inputs
	case *runcJailer:
		jailer.restoreMounts = mounts
	default:
		return fmt.Errorf("unsupported restore jailer %T", s.jailer)
	}
	return nil
}

// validateRestoreMounts validates private targets and normalizes only backing
// sources, replacing block-device aliases with their canonical device nodes.
func validateRestoreMounts(mounts []restoreMount, shared string) error {
	shared, err := filepath.Abs(shared)
	if err != nil {
		return err
	}
	targets := make(map[string]bool)
	for index := range mounts {
		mount := &mounts[index]
		target, err := capturedDevicePath(mount.Target, false)
		if err != nil {
			return err
		}
		if targets[target] || pathWithin(target, shared) || pathWithin(shared, target) {
			return fmt.Errorf("restore device target overlaps another mount or live shim files: %s", target)
		}
		for other := range targets {
			if pathWithin(other, target) || pathWithin(target, other) {
				return fmt.Errorf("overlapping restore device targets")
			}
		}
		targets[target] = true
		file, source, err := openRestoreBacking(mount.Source)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		mount.Source = source
	}
	return nil
}

// resolveRestoreBacking permits aliases such as /dev/mapper/name -> ../dm-N,
// but never follows a final symlink to an ordinary file or a special non-block
// device. Canonicalizing the backing must not change the captured guest path.
func resolveRestoreBacking(source string) (string, bool, error) {
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return "", false, fmt.Errorf("restore backing must be an absolute clean path")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return "", false, err
	}
	alias := info.Mode()&os.ModeSymlink != 0
	if alias {
		source, err = filepath.EvalSymlinks(source)
		if err != nil {
			return "", false, err
		}
		info, err = os.Lstat(source)
		if err != nil {
			return "", false, err
		}
	}
	block := info.Mode()&os.ModeDevice != 0 && info.Mode()&os.ModeCharDevice == 0
	if alias && !block {
		return "", false, fmt.Errorf("restore backing symlink must resolve to a block device")
	}
	if !info.Mode().IsRegular() && !block {
		return "", false, fmt.Errorf("restore backing must be a regular file or block device")
	}
	return source, alias, nil
}

// openRestoreBacking keeps the final-component guard after resolution and
// verifies the actual opened inode, including a raced alias-to-file replacement.
// Callers that pin a detached mount must use this descriptor rather than reopen
// an unvalidated source path.
func openRestoreBacking(source string) (*os.File, string, error) {
	source, alias, err := resolveRestoreBacking(source)
	if err != nil {
		return nil, "", err
	}
	file, err := os.OpenFile(source, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, "", err
	}
	block := info.Mode()&os.ModeDevice != 0 && info.Mode()&os.ModeCharDevice == 0
	if (alias && !block) || (!info.Mode().IsRegular() && !block) {
		_ = file.Close()
		return nil, "", fmt.Errorf("restore backing must be a regular file or block device; aliases require a block device")
	}
	return file, source, nil
}

func pathWithin(path, parent string) bool {
	return path == parent || strings.HasPrefix(path, parent+"/")
}

// restoreBackingPath resolves exported VMM paths to their actual host clone for
// a later checkpoint. Bind mount placeholders must never be copied as disks.
func restoreBackingPath(jailer jailer, path string) (string, bool) {
	var mounts []restoreMount
	switch j := jailer.(type) {
	case *noopJailer:
		j.restoreMu.RLock()
		defer j.restoreMu.RUnlock()
		mounts = j.restoreMounts
	case *runcJailer:
		mounts = j.restoreMounts
		path = "/" + strings.TrimPrefix(path, "/")
	}
	for _, mount := range mounts {
		if mount.Target == path {
			return mount.Source, true
		}
	}
	return "", false
}

func (j *runcJailer) prepareRestoreMounts() error {
	if j.started {
		return fmt.Errorf("cannot add restore mounts after VMM startup")
	}
	if err := validateRestoreMounts(j.restoreMounts, j.RootPath()); err != nil {
		return err
	}
	root, err := os.OpenRoot(j.RootPath())
	if err != nil {
		return err
	}
	defer root.Close()
	for _, mount := range j.restoreMounts {
		target := strings.TrimPrefix(mount.Target, "/")
		if err := root.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		file, err := root.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
		if err != nil {
			return err
		}
		err = file.Chown(int(j.Config.UID), int(j.Config.GID))
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		options := []string{"bind", "nosuid"}
		if mount.ReadOnly {
			options = append(options, "ro")
		}
		j.configSpec.Mounts = append(j.configSpec.Mounts, specs.Mount{Destination: mount.Target, Source: mount.Source, Type: "none", Options: options})
	}
	return nil
}
