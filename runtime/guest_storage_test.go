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
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/firecracker-microvm/firecracker-containerd/config"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/sirupsen/logrus"

	"github.com/stretchr/testify/require"
)

func TestGuestOverlayAllocation(t *testing.T) {
	const sizeMiB = 64
	path := filepath.Join(t.TempDir(), guestOverlayFile)
	uuid, err := createGuestOverlay(context.Background(), path, sizeMiB)
	require.NoError(t, err)
	output, err := exec.Command("blkid", "-p", "-s", "UUID", "-o", "value", path).Output()
	require.NoError(t, err)
	require.Equal(t, uuid+"\n", string(output))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.EqualValues(t, sizeMiB<<20, info.Size())
	require.GreaterOrEqual(t, info.Sys().(*syscall.Stat_t).Blocks*512, info.Size(), "full guest capacity must consume reserved host disk blocks")
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	_, err = createGuestOverlay(context.Background(), path, sizeMiB)
	require.ErrorIs(t, err, os.ErrExist, "must never overwrite an existing writable disk")
}

func TestGuestOverlayRejectsRAMAndCleansFailure(t *testing.T) {
	root, err := os.MkdirTemp("/dev/shm", "fc-overlay-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, guestOverlayFile)
	_, err = createGuestOverlay(context.Background(), path, 64)
	require.ErrorContains(t, err, "requires ext4/ext3")
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)

	path = filepath.Join(t.TempDir(), guestOverlayFile)
	_, err = createGuestOverlay(context.Background(), path, 64, func(*os.File) error { return syscall.EPERM })
	require.ErrorIs(t, err, syscall.EPERM)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist, "failure after formatting must release the allocated capacity")
}

func TestGuestOverlayHostENOSPC(t *testing.T) {
	root := os.Getenv("FICD_OVERLAY_ENOSPC_DIR")
	if root == "" {
		t.Skip("test-storage.sh supplies a private, disk-backed 32 MiB filesystem")
	}
	path := filepath.Join(root, guestOverlayFile)
	_, err := createGuestOverlay(context.Background(), path, 64)
	require.ErrorIs(t, err, syscall.ENOSPC)
	fixture, err := os.OpenRoot(root)
	require.NoError(t, err)
	defer fixture.Close()
	_, err = fixture.Stat(guestOverlayFile)
	require.ErrorIs(t, err, os.ErrNotExist, "failed allocation must remove its partial image")
}

func TestConfigureGuestRootDiskAndLegacyDefaults(t *testing.T) {
	for _, mode := range []string{"", "ram", "disk"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			svc := &service{config: &config.Config{GuestRootMode: mode, GuestRootSizeMiB: 64},
				machineConfig: &fc.Config{KernelArgs: "ro", Drives: []models.Drive{{DriveID: fc.String("root"), IsReadOnly: true}}},
				jailer:        &noopJailer{shimDir: vm.Dir(root), logger: logrus.NewEntry(logrus.New())}}
			require.NoError(t, svc.configureGuestRoot(context.Background(), &proto.CreateVMRequest{}))
			if mode == "disk" {
				require.Len(t, svc.machineConfig.Drives, 2)
				require.Equal(t, guestOverlayDrive, *svc.machineConfig.Drives[1].DriveID)
				require.Contains(t, svc.machineConfig.KernelArgs, "overlay_root=UUID=")
				require.NotContains(t, svc.machineConfig.KernelArgs, "damon")
			} else {
				require.Len(t, svc.machineConfig.Drives, 1)
			}
		})
	}
}

func TestRestoredGuestUpperIsIndependentAndVerified(t *testing.T) {
	captured := t.TempDir()
	state := filepath.Join(captured, "state")
	original := filepath.Join(t.TempDir(), guestOverlayFile)
	_, err := createGuestOverlay(context.Background(), original, 64)
	require.NoError(t, err)
	saved, err := copySnapshotFile(context.Background(), original, captured, filepath.Base(state)+"."+guestOverlayFile)
	require.NoError(t, err)
	saved.Path = guestOverlayFile
	require.NoError(t, os.Remove(original))
	restored := func() *service {
		return &service{shimCtx: context.Background(), jailer: &noopJailer{shimDir: vm.Dir(t.TempDir()), logger: logrus.NewEntry(logrus.New())}}
	}
	first, err := restored().restoreGuestOverlay(state, &saved)
	require.NoError(t, err)
	file, err := os.OpenFile(first, os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteAt([]byte("independent mutation"), 0)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	second, err := restored().restoreGuestOverlay(state, &saved)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	data := make([]byte, 20)
	file, err = os.Open(second)
	require.NoError(t, err)
	_, err = file.ReadAt(data, 0)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	require.NotContains(t, string(data), "independent mutation")
	saved.SHA256 = "incorrect"
	svc := restored()
	_, err = svc.restoreGuestOverlay(state, &saved)
	require.ErrorContains(t, err, "checksum/size mismatch")
	_, err = os.Stat(filepath.Join(svc.jailer.JailPath().RootPath(), guestOverlayFile))
	require.ErrorIs(t, err, os.ErrNotExist)
}
