// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
// 	http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestSnapshotDriveMetadata(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	_, err := readSnapshotDriveMetadata(state)
	require.ErrorContains(t, err, "restore requires snapshot drive metadata")
	valid := snapshotDriveMetadata{Version: 2, Transport: &checkpointTransportCut{Version: 1}, Drives: []snapshotDrivePath{
		{ID: "root", Path: "/absent-source/root.img", ReadOnly: true, Root: true},
		{ID: "workload", Path: "/dev/mapper/absent-source-workload"},
	}}
	require.NoError(t, publishSnapshotDriveMetadata(state, &valid))
	actual, err := readSnapshotDriveMetadata(state)
	require.NoError(t, err)
	require.Equal(t, valid, *actual)
	for _, test := range []struct {
		name   string
		mutate func(*snapshotDriveMetadata)
	}{
		{"version", func(m *snapshotDriveMetadata) { m.Version = 1 }},
		{"traversal", func(m *snapshotDriveMetadata) { m.Drives[0].Path = "/source/../etc/passwd" }},
		{"relative", func(m *snapshotDriveMetadata) { m.Drives[0].Path = "source.img" }},
		{"reserved", func(m *snapshotDriveMetadata) { m.Drives[0].Path = "/dev/kvm" }},
		{"duplicate-id", func(m *snapshotDriveMetadata) { m.Drives[1].ID = m.Drives[0].ID }},
		{"duplicate-path", func(m *snapshotDriveMetadata) { m.Drives[1].Path = m.Drives[0].Path }},
		{"writable-root", func(m *snapshotDriveMetadata) { m.Drives[0].ReadOnly = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			altered := valid
			altered.Drives = append([]snapshotDrivePath(nil), valid.Drives...)
			test.mutate(&altered)
			require.NoError(t, publishSnapshotDriveMetadata(state, &altered))
			_, err := readSnapshotDriveMetadata(state)
			require.Error(t, err)
		})
	}
	require.NoError(t, os.WriteFile(state+snapshotDriveMetadataSuffix, []byte(`{"version":1,"drives":[],"unexpected":true}`), 0600))
	_, err = readSnapshotDriveMetadata(state)
	require.Error(t, err)
	require.NoError(t, os.Remove(state+snapshotDriveMetadataSuffix))
	target := filepath.Join(t.TempDir(), "metadata")
	require.NoError(t, os.WriteFile(target, []byte(`{}`), 0600))
	require.NoError(t, os.Symlink(target, state+snapshotDriveMetadataSuffix))
	_, err = readSnapshotDriveMetadata(state)
	require.Error(t, err)
}

func TestNativeRestoreMountPlan(t *testing.T) {
	for _, jailed := range []bool{false, true} {
		name := "unjailed"
		if jailed {
			name = "jailed"
		}
		t.Run(name, func(t *testing.T) {
			shared := t.TempDir()
			clone := filepath.Join(shared, "clone")
			pmem := filepath.Join(shared, "pmem")
			require.NoError(t, os.WriteFile(clone, []byte("independent"), 0600))
			require.NoError(t, os.WriteFile(pmem, []byte("verified-pmem"), 0400))
			captured, capturedPmem := "/deleted-source/disks/workload", "/deleted-source/pmem/root"
			if jailed {
				captured, capturedPmem = "workload", "root-pmem"
			}
			restore := &nativeRestore{
				manifest: &nativeCheckpoint{
					VMConfig: &models.FullVMConfiguration{
						Drives: []*models.Drive{{DriveID: fc.String("workload"), PathOnHost: captured}},
						Pmem:   []*models.Pmem{{ID: fc.String(pmemRootID), PathOnHost: fc.String(capturedPmem), ReadOnly: true}},
					},
					Drives: []nativeDrive{{ID: "workload", File: nativeFile{Path: "drives/0"}}},
					Pmem:   []nativePmem{{ID: pmemRootID, File: nativeFile{Path: "pmem/0"}}},
				},
				files: map[string]string{"drives/0": clone, "pmem/0": pmem, "memory": filepath.Join(shared, "memory"), "state": filepath.Join(shared, "state")},
			}
			svc := &service{nativeRestore: restore, machineConfig: &fc.Config{PmemDevices: []models.Pmem{{ID: fc.String(pmemRootID), PathOnHost: fc.String(pmem), ReadOnly: true}}}, jailer: &noopJailer{shimDir: vm.Dir(shared), logger: logrus.NewEntry(logrus.New())}}
			if jailed {
				svc.jailer = &runcJailer{Config: runcJailerConfig{OCIBundlePath: shared}}
			}
			require.NoError(t, svc.prepareSnapshotMounts(&proto.CreateVMRequest{}))
			path, ok := restoreBackingPath(svc.jailer, captured)
			require.True(t, ok)
			require.Equal(t, clone, path)
			path, ok = restoreBackingPath(svc.jailer, capturedPmem)
			require.True(t, ok)
			require.Equal(t, pmem, path)
		})
	}
}

func TestJailedRestoreMounts(t *testing.T) {
	shared := t.TempDir()
	clone := filepath.Join(shared, "clone")
	require.NoError(t, os.WriteFile(clone, []byte("independent"), 0600))
	original := filepath.Join(t.TempDir(), "removed", "nested", "disk")
	j := &runcJailer{Config: runcJailerConfig{OCIBundlePath: shared, UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}, restoreMounts: []restoreMount{{Source: clone, Target: original, ReadOnly: true}}}
	require.NoError(t, os.Mkdir(j.RootPath(), 0700))
	require.NoError(t, j.prepareRestoreMounts())
	require.Len(t, j.configSpec.Mounts, 1)
	mount := j.configSpec.Mounts[0]
	require.Equal(t, original, mount.Destination)
	require.Equal(t, clone, mount.Source)
	require.Contains(t, mount.Options, "ro")
	_, err := os.Stat(original)
	require.True(t, os.IsNotExist(err), "must not create original source path on host")
	placeholder := filepath.Join(j.RootPath(), original[1:])
	info, err := os.Stat(placeholder)
	require.NoError(t, err)
	require.Zero(t, info.Size(), "disk data is exposed only by private OCI mount")
	data, err := os.ReadFile(clone)
	require.NoError(t, err)
	require.Equal(t, "independent", string(data))
	j.started = true
	require.Error(t, j.prepareRestoreMounts())
}

func TestRestoreMountTargetConfinement(t *testing.T) {
	shared := t.TempDir()
	source := filepath.Join(t.TempDir(), "clone")
	require.NoError(t, os.WriteFile(source, []byte("clone"), 0600))
	require.Error(t, validateRestoreMounts([]restoreMount{{Source: source, Target: filepath.Join(shared, "socket")}}, shared))
	require.Error(t, validateRestoreMounts([]restoreMount{{Source: source, Target: "/proc/self/fd/3"}}, shared))
	require.Error(t, validateRestoreMounts([]restoreMount{{Source: source, Target: "/a"}, {Source: source, Target: "/a/b"}}, shared))
	jail := &runcJailer{Config: runcJailerConfig{OCIBundlePath: shared}}
	require.NoError(t, os.Mkdir(jail.RootPath(), 0700))
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(jail.RootPath(), "escape")))
	jail.restoreMounts = []restoreMount{{Source: source, Target: "/escape/disk"}}
	require.Error(t, jail.prepareRestoreMounts())
	_, err := os.Stat(filepath.Join(outside, "disk"))
	require.True(t, os.IsNotExist(err))
}

func TestLegacyRestoreMountPlan(t *testing.T) {
	shared := t.TempDir()
	state := filepath.Join(t.TempDir(), "state")
	root, workload := filepath.Join(shared, "root"), filepath.Join(shared, "workload")
	require.NoError(t, os.WriteFile(root, []byte("root"), 0400))
	require.NoError(t, os.WriteFile(workload, []byte("workload"), 0600))
	cfg := &fc.Config{Drives: []models.Drive{{DriveID: fc.String("root"), PathOnHost: root, IsReadOnly: true}}}
	metadata := &snapshotDriveMetadata{Version: 2, Transport: &checkpointTransportCut{Version: 1}, Drives: []snapshotDrivePath{{ID: "root", Path: "/deleted/root", Root: true, ReadOnly: true}, {ID: stubPathToDriveID("ctrstub0"), Path: "/dev/mapper/deleted-workload"}}}
	require.NoError(t, publishSnapshotDriveMetadata(state, metadata))
	jail := &noopJailer{shimDir: vm.Dir(shared), logger: logrus.NewEntry(logrus.New())}
	svc := &service{machineConfig: cfg, jailer: jail}
	require.NoError(t, svc.prepareSnapshotMounts(&proto.CreateVMRequest{LoadSnapshot: true, SnapshotPath: state, MemFilePath: "/snapshot/memory", ContainerSnapshotPath: workload}))
	require.Equal(t, []restoreMount{{Source: root, Target: "/deleted/root", ReadOnly: true}, {Source: workload, Target: "/dev/mapper/deleted-workload"}}, jail.restoreMounts)
	require.Equal(t, []string{state, "/snapshot/memory"}, jail.restoreInputs)
	data, err := json.Marshal(metadata)
	require.NoError(t, err)
	require.NotContains(t, string(data), "drive_overrides")
}
