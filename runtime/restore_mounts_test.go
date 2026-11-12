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
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
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

func TestNativeRestoreMountPlan(t *testing.T) {
	for _, jailed := range []bool{false, true} {
		t.Run(fmt.Sprintf("jailed=%v", jailed), func(t *testing.T) {
			shared := t.TempDir()
			clone := filepath.Join(shared, "private-clone")
			require.NoError(t, os.WriteFile(clone, []byte("independent"), 0600))
			captured := "/deleted-source/disks/workload"
			if jailed {
				captured = "workload"
			}
			restore := &nativeRestore{manifest: &nativeCheckpoint{
				VMConfig: &models.FullVMConfiguration{Drives: []*models.Drive{{DriveID: fc.String("workload"), PathOnHost: captured}}},
				Drives:   []nativeDrive{{ID: "workload", File: snapshotFile{Path: "drives/0"}}},
			}, files: map[string]string{"drives/0": clone, "memory": filepath.Join(shared, "memory"), "state": filepath.Join(shared, "state")}}
			svc := &service{nativeRestore: restore, jailer: &noopJailer{shimDir: vm.Dir(shared), logger: logrus.NewEntry(logrus.New())}}
			if jailed {
				svc.jailer = &runcJailer{Config: runcJailerConfig{OCIBundlePath: shared}}
			}
			require.NoError(t, svc.prepareSnapshotMounts(&proto.CreateVMRequest{}))
			path, ok := restoreBackingPath(svc.jailer, captured)
			require.True(t, ok)
			require.Equal(t, clone, path)
		})
	}
}

func TestRestoreBackingRejectsFileAliasesAndSpecialFiles(t *testing.T) {
	directory := t.TempDir()
	regular := filepath.Join(directory, "regular")
	require.NoError(t, os.WriteFile(regular, []byte("private backing"), 0600))
	fifo := filepath.Join(directory, "fifo")
	require.NoError(t, unix.Mkfifo(fifo, 0600))
	link := func(name, target string) string {
		path := filepath.Join(directory, name)
		require.NoError(t, os.Symlink(target, path))
		return path
	}
	cycle := link("cycle", "cycle")
	for _, path := range []string{link("file-alias", regular), fifo, link("fifo-alias", fifo),
		"/dev/null", link("char-alias", "/dev/null"), directory,
		link("missing-alias", filepath.Join(directory, "missing")), cycle, "relative", directory + "/../other"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			mounts := []restoreMount{{Source: path, Target: "/captured/workload"}}
			require.Error(t, validateRestoreMounts(mounts, t.TempDir()))
		})
	}
	mounts := []restoreMount{{Source: regular, Target: "/captured/workload"}}
	require.NoError(t, validateRestoreMounts(mounts, t.TempDir()))
	require.Equal(t, regular, mounts[0].Source, "ordinary backing paths must remain unchanged")
	file, source, err := openRestoreBacking(regular)
	require.NoError(t, err)
	require.Equal(t, regular, source)
	require.NoError(t, file.Close())
}

// Metadata inspection does not read from or modify a block device. Full open
// and mount-plan checks below run only when a device is readable by this user.
func restoreBlockNode(t *testing.T) string {
	t.Helper()
	for _, pattern := range []string{"/dev/loop*", "/dev/dm-*", "/dev/mapper/*", "/dev/sd*", "/dev/vd*"} {
		paths, err := filepath.Glob(pattern)
		require.NoError(t, err)
		for _, path := range paths {
			info, err := os.Stat(path)
			if err == nil && info.Mode()&os.ModeDevice != 0 && info.Mode()&os.ModeCharDevice == 0 {
				canonical, err := filepath.EvalSymlinks(path)
				require.NoError(t, err)
				return canonical
			}
		}
	}
	t.Skip("no existing block-device node available for read-only metadata inspection")
	return ""
}

func restoreBlockAlias(t *testing.T, node string) string {
	t.Helper()
	directory := t.TempDir()
	require.NoError(t, os.Symlink(node, filepath.Join(directory, "device")))
	require.NoError(t, os.Mkdir(filepath.Join(directory, "mapper"), 0700))
	alias := filepath.Join(directory, "mapper", "workload")
	require.NoError(t, os.Symlink("../device", alias))
	return alias
}

func TestRestoreBackingResolvesBlockAliases(t *testing.T) {
	node := restoreBlockNode(t)
	alias := restoreBlockAlias(t, node)
	source, wasAlias, err := resolveRestoreBacking(alias)
	require.NoError(t, err)
	require.True(t, wasAlias)
	require.Equal(t, node, source, "the full alias chain must resolve to the actual block node")
	source, wasAlias, err = resolveRestoreBacking(node)
	require.NoError(t, err)
	require.False(t, wasAlias)
	require.Equal(t, node, source)

	logger := logrus.NewEntry(logrus.New())
	jail := newNoopJailer(context.Background(), logger, vm.Dir(t.TempDir()))
	jail.restoreMounts = []restoreMount{{Source: node, Target: alias}}
	require.NoError(t, jail.ExposeFileToJail(alias), "a previously exposed alias must match its canonical backing without installing another mount")
	require.Equal(t, []restoreMount{{Source: node, Target: alias}}, jail.restoreMounts)
}

func TestRestoreBlockAliasMountConsumers(t *testing.T) {
	node := restoreBlockNode(t)
	alias := restoreBlockAlias(t, node)
	file, _, err := openRestoreBacking(alias)
	if err != nil && (errors.Is(err, os.ErrPermission) || errors.Is(err, unix.ENXIO) || errors.Is(err, unix.ENODEV)) {
		t.Skipf("existing block node is not readable: %v; metadata-only alias policy is covered separately", err)
	}
	require.NoError(t, err)
	require.NoError(t, file.Close())
	captured := "/deleted-source/workload"
	shared := t.TempDir()
	mounts := []restoreMount{{Source: alias, Target: captured}}
	require.NoError(t, validateRestoreMounts(mounts, shared))
	require.Equal(t, node, mounts[0].Source)
	require.Equal(t, captured, mounts[0].Target)

	// The unjailed launcher serializes normalized sources before any helper
	// starts; constructing this command creates no namespace, mount or VM.
	executable, err := os.Executable()
	require.NoError(t, err)
	mounts[0].Source = alias
	command, err := restoreNamespaceCommand(context.Background(), shared, executable, nil, mounts, nil)
	require.NoError(t, err)
	data, err := os.ReadFile(command.Args[2])
	require.NoError(t, err)
	var plan restoreNamespacePlan
	require.NoError(t, json.Unmarshal(data, &plan))
	require.Equal(t, node, plan.Mounts[0].Source)
	require.Equal(t, captured, plan.Mounts[0].Target)

	jailed := &runcJailer{Config: runcJailerConfig{OCIBundlePath: t.TempDir(), UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}, restoreMounts: []restoreMount{{Source: alias, Target: captured}}}
	require.NoError(t, os.Mkdir(jailed.RootPath(), 0700))
	require.NoError(t, jailed.prepareRestoreMounts())
	require.Equal(t, node, jailed.configSpec.Mounts[0].Source)
	require.Equal(t, captured, jailed.configSpec.Mounts[0].Destination)
	_, err = os.Stat(captured)
	require.True(t, os.IsNotExist(err), "the captured pathname must remain absent on the host")
}

func TestRestoreBackingDescriptorSurvivesSourceReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "disk")
	require.NoError(t, os.WriteFile(path, []byte("captured inode"), 0600))
	original, err := os.Stat(path)
	require.NoError(t, err)
	backing, source, err := openRestoreBacking(path)
	require.NoError(t, err)
	defer backing.Close()
	require.Equal(t, path, source)
	require.NoError(t, os.Rename(path, filepath.Join(directory, "old-disk")))
	require.NoError(t, os.Symlink("/dev/null", path))
	pinned, err := os.Stat(fmt.Sprintf("/proc/self/fd/%d", backing.Fd()))
	require.NoError(t, err)
	require.True(t, os.SameFile(original, pinned), "private binds must use the checked inode even after its old pathname changes")
	data, err := io.ReadAll(backing)
	require.NoError(t, err)
	require.Equal(t, "captured inode", string(data))
	_, _, err = openRestoreBacking(path)
	require.Error(t, err, "a later character-device replacement must not become a new backing")
}

func TestSameIDRestoreMountPlan(t *testing.T) {
	shared := t.TempDir()
	backing := filepath.Join(shared, "private-clone")
	require.NoError(t, os.WriteFile(backing, []byte("private"), 0600))
	mounts := []restoreMount{{Source: backing, Target: filepath.Join(shared, "ctrstub0")}}
	// The old absolute path belongs to tmpfs, while live shim files are at
	// the reserved alias. Same-ID capture paths must remain unchanged.
	require.NoError(t, validateRestoreMounts(mounts, restoreSharedDirectory))
	require.Equal(t, filepath.Join(shared, "ctrstub0"), mounts[0].Target)
	for _, target := range []string{restoreSharedDirectory, restoreSharedDirectory + "/ctrstub0", "/"} {
		require.Error(t, validateRestoreMounts([]restoreMount{{Source: backing, Target: target}}, restoreSharedDirectory))
	}
	for _, input := range []string{restoreSharedDirectory + "/state", mounts[0].Target, shared, mounts[0].Target + "/state"} {
		require.Error(t, validateRestoreInputs([]string{input}, mounts))
	}
	require.NoError(t, validateRestoreInputs([]string{filepath.Join(shared, "state"), filepath.Join(shared, "memory")}, mounts))
}

func TestRestoreAPIPath(t *testing.T) {
	shared := t.TempDir()
	j := &noopJailer{shimDir: vm.Dir(shared)}
	stage := filepath.Join(shared, ".state-stage", "memory")
	require.Equal(t, stage, restoreAPIPath(j, stage), "ordinary unjailed VM has its original cwd")
	j.restoreMounts = []restoreMount{{Target: "/old/disk"}}
	require.Equal(t, restoreSharedDirectory+"/.state-stage/memory", restoreAPIPath(j, stage))
	require.Equal(t, "/outside/state", restoreAPIPath(j, "/outside/state"))
	require.Equal(t, shared+"-sibling/state", restoreAPIPath(j, shared+"-sibling/state"))
	require.Equal(t, stage, restoreAPIPath(&runcJailer{}, stage), "jailed API translation remains separate")
}
