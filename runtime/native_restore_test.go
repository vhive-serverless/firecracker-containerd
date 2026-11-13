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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/firecracker-microvm/firecracker-containerd/config"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNativeRestoreExecStateIdentity(t *testing.T) {
	svc, _ := nativeCloneFixture(t, t.TempDir())
	manifest := svc.nativeRestore.manifest
	manifest.Tasks = []nativeTask{{ID: "task", Spec: json.RawMessage(`{}`)}}
	manifest.Processes = []nativeProcess{
		{TaskID: "task", State: &taskAPI.StateResponse{ID: "task", Pid: 10}},
		{TaskID: "task", ExecID: "exec", State: &taskAPI.StateResponse{ID: "exec", Pid: 11}},
	}
	manifest.Transport.Processes = []vm.TransportProcessIO{{TaskID: "task"}, {TaskID: "task", ExecID: "exec"}}
	require.NoError(t, validateNativeRestore(manifest), "containerd identifies exec state by exec ID")
	manifest.Processes[1].State.ID = "task"
	require.Error(t, validateNativeRestore(manifest), "task ID must not impersonate exec state")
	manifest.Processes[1].State.ID = "exec"
	manifest.Processes[0].State.ID = "exec"
	require.Error(t, validateNativeRestore(manifest), "init state still requires task ID")
}

func nativeCloneFixture(t *testing.T, destination string) (*service, *proto.CreateVMRequest) {
	t.Helper()
	return nativeCloneFixtureWithDrives(t, destination, &proto.CreateVMRequest{ContainerCount: 1}, []nativeDrive{
		{ID: "root_drive", ReadOnly: true, File: snapshotFile{Path: "drives/00000000", Size: 64}},
		{ID: "container_0", File: snapshotFile{Path: "drives/00000001", Size: 1 << 20}},
	})
}

func nativeCloneFixtureWithDrives(t *testing.T, destination string, request *proto.CreateVMRequest, drives []nativeDrive) (*service, *proto.CreateVMRequest) {
	t.Helper()
	artifact := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(artifact, "drives"), 0700))
	file := func(name string, size int64) snapshotFile {
		path := filepath.Join(artifact, name)
		output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		require.NoError(t, err)
		require.NoError(t, output.Truncate(size))
		_, err = output.WriteAt([]byte(name), 0)
		require.NoError(t, err)
		require.NoError(t, output.Close())
		return snapshotFile{Path: name, Size: size, SHA256: hashNativeFile(t, path)}
	}
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	manifest := &nativeCheckpoint{Version: 2, Transport: &checkpointTransportCut{Version: 1}, RuntimeConfig: &config.Config{}, CreateRequest: encoded,
		Memory: file("memory", 64), State: file("state", 64), VMConfig: &models.FullVMConfiguration{}}
	manifest.Drives = drives
	for index, drive := range manifest.Drives {
		manifest.Drives[index].File = file(drive.File.Path, drive.File.Size)
	}
	for index, drive := range manifest.Drives {
		manifest.VMConfig.Drives = append(manifest.VMConfig.Drives, &models.Drive{
			DriveID: fc.String(drive.ID), IsReadOnly: drive.ReadOnly, IsRootDevice: fc.Bool(index == 0),
		})
	}
	require.NoError(t, validateNativeRestore(manifest))
	return &service{jailer: newNoopJailer(context.Background(), logrus.NewEntry(logrus.New()), vm.Dir(destination)),
		nativeRestore: &nativeRestore{artifact: artifact, manifest: manifest, files: make(map[string]string)}}, request
}

type nativeCloneFileOptionJailer struct {
	jailer
	option FileOpt
}

func (j nativeCloneFileOptionJailer) StubDrivesOptions() []FileOpt {
	return []FileOpt{j.option}
}

func TestNativeRestoreCopyFailureCleanup(t *testing.T) {
	for _, failure := range []string{"after-copy", "cancel-after-memory"} {
		t.Run(failure, func(t *testing.T) {
			svc, request := nativeCloneFixture(t, t.TempDir())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			observed := false
			svc.jailer = nativeCloneFileOptionJailer{jailer: svc.jailer, option: func(file *os.File) error {
				if failure == "cancel-after-memory" && filepath.Base(file.Name()) == "memory" {
					observed = true
					cancel()
				}
				if failure == "after-copy" && filepath.Base(file.Name()) == "00000001" {
					info, err := file.Stat()
					if err != nil {
						return err
					}
					observed = info.Size() > 0
					return syscall.EPERM
				}
				return nil
			}}
			err := svc.cloneNativeRestore(ctx, request)
			if failure == "after-copy" {
				require.ErrorIs(t, err, syscall.EPERM)
			} else {
				require.ErrorIs(t, err, context.Canceled)
			}
			require.True(t, observed, "failure must occur after the intended copy stage")
			_, err = os.Stat(svc.nativeRestore.workspace)
			require.ErrorIs(t, err, os.ErrNotExist, "failed restores must release all partial copies ")
			require.Empty(t, svc.nativeRestore.files)
			for _, drive := range svc.nativeRestore.manifest.Drives {
				require.Equal(t, drive.File.SHA256, hashNativeFile(t, filepath.Join(svc.nativeRestore.artifact, drive.File.Path)), "failed restore must not alter artifact data")
			}
		})
	}
}

func hashNativeFile(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	require.NoError(t, err)
	return hex.EncodeToString(hash.Sum(nil))
}

func TestNativeRestoreCopiesPrivateArtifacts(t *testing.T) {
	svc, request := nativeCloneFixture(t, t.TempDir())
	require.NoError(t, svc.cloneNativeRestore(context.Background(), request))
	for _, saved := range append([]snapshotFile{svc.nativeRestore.manifest.Memory, svc.nativeRestore.manifest.State}, svc.nativeRestore.manifest.Drives[0].File, svc.nativeRestore.manifest.Drives[1].File) {
		cloned := svc.nativeRestore.files[saved.Path]
		require.Equal(t, saved.SHA256, hashNativeFile(t, cloned))
		before, err := os.Stat(filepath.Join(svc.nativeRestore.artifact, saved.Path))
		require.NoError(t, err)
		after, err := os.Stat(cloned)
		require.NoError(t, err)
		require.False(t, os.SameFile(before, after))
	}
}

func TestNativeRestoreCreateVMSelectsArtifact(t *testing.T) {
	// A restore request must fail at artifact validation before cold-boot
	// configuration is consulted. This fixture deliberately has no boot config.
	svc := &service{logger: logrus.NewEntry(logrus.New())}
	_, err := svc.CreateVM(context.Background(), &proto.CreateVMRequest{VMID: "restore", NativeCheckpointDirectory: "relative"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.ErrorContains(t, err, "native restore requires an absolute artifact directory")
}

func TestNativeRestoreCreateVMDeadlinePreservesRequestIdentity(t *testing.T) {
	fixture, _ := nativeCloneFixture(t, t.TempDir())
	manifest, err := json.Marshal(fixture.nativeRestore.manifest)
	require.NoError(t, err)
	artifact := fixture.nativeRestore.artifact
	require.NoError(t, os.WriteFile(filepath.Join(artifact, "manifest.json"), manifest, 0600))
	svc := &service{logger: logrus.NewEntry(logrus.New())}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = svc.CreateVM(ctx, &proto.CreateVMRequest{VMID: "restore-deadline", NativeCheckpointDirectory: artifact})
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
	require.ErrorContains(t, err, `VM "restore-deadline" didn't start`)
}
