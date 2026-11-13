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
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/stretchr/testify/require"
)

func nativeCloneStorageFixture(t *testing.T, destination string) (*service, *proto.CreateVMRequest) {
	t.Helper()
	request := &proto.CreateVMRequest{ContainerCount: 1, GuestRoot: &proto.GuestRootStorage{Mode: "disk", SizeMib: 64}}
	return nativeCloneFixtureWithDrives(t, destination, request, []nativeDrive{
		{ID: "root_drive", ReadOnly: true, File: snapshotFile{Path: "drives/00000000", Size: 64}},
		{ID: guestOverlayDrive, File: snapshotFile{Path: "drives/00000001", Size: 64 << 20}},
		{ID: "container_0", File: snapshotFile{Path: "drives/00000002", Size: 64 << 20}},
	})
}

func TestNativeRestoreDiskBackingAndReservation(t *testing.T) {
	svc, request := nativeCloneStorageFixture(t, t.TempDir())
	require.NoError(t, svc.cloneNativeRestore(context.Background(), request))
	for _, drive := range svc.nativeRestore.manifest.Drives {
		path := svc.nativeRestore.files[drive.File.Path]
		require.Equal(t, drive.File.SHA256, hashNativeFile(t, path), "reservation must not alter captured bytes")
		if drive.ReadOnly {
			continue
		}
		file, err := os.Open(path)
		require.NoError(t, err)
		require.NoError(t, requireDiskBacking(file))
		info, err := file.Stat()
		require.NoError(t, err)
		require.NoError(t, file.Close())
		allocated := info.Sys().(*syscall.Stat_t).Blocks * 512
		if drive.ID == guestOverlayDrive {
			require.GreaterOrEqual(t, allocated, info.Size(), "the entire private guest upper must be reserved")
		} else {
			require.Less(t, allocated, info.Size(), "thin workload disks must retain sparse allocation")
		}
	}
}

func TestNativeRestoreRejectsRAMBacking(t *testing.T) {
	ram, err := os.MkdirTemp("/dev/shm", "fc-native-clone-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(ram) })
	for _, upper := range []string{"disk", "ram"} {
		t.Run(upper, func(t *testing.T) {
			svc, request := nativeCloneStorageFixture(t, filepath.Join(ram, upper))
			if upper == "ram" {
				manifest := svc.nativeRestore.manifest
				manifest.Drives = append(manifest.Drives[:1], manifest.Drives[2:]...)
				manifest.VMConfig.Drives = append(manifest.VMConfig.Drives[:1], manifest.VMConfig.Drives[2:]...)
				request.GuestRoot.Mode = "ram"
				manifest.CreateRequest, err = json.Marshal(request)
				require.NoError(t, err)
				require.NoError(t, validateNativeRestore(manifest))
			}
			// A disk-storage rejection must precede even opening artifact inputs.
			svc.nativeRestore.artifact = filepath.Join(t.TempDir(), "missing-artifact")
			err := svc.cloneNativeRestore(context.Background(), request)
			require.ErrorContains(t, err, "requires ext4/ext3")
			_, err = os.Stat(svc.nativeRestore.workspace)
			require.ErrorIs(t, err, os.ErrNotExist)
			require.Empty(t, svc.nativeRestore.files)
		})
	}
}

func TestNativeRestoreStorageFailureCleanup(t *testing.T) {
	svc, request := nativeCloneStorageFixture(t, t.TempDir())
	observed := false
	svc.jailer = nativeCloneFileOptionJailer{jailer: svc.jailer, option: func(file *os.File) error {
		if filepath.Base(file.Name()) != "00000001" {
			return nil
		}
		info, err := file.Stat()
		if err != nil {
			return err
		}
		observed = info.Sys().(*syscall.Stat_t).Blocks*512 >= info.Size()
		return syscall.EPERM
	}}
	err := svc.cloneNativeRestore(context.Background(), request)
	require.ErrorIs(t, err, syscall.EPERM)
	require.True(t, observed, "failure must occur after guest upper reservation")
	_, err = os.Stat(svc.nativeRestore.workspace)
	require.ErrorIs(t, err, os.ErrNotExist, "failed restores must release all partial copies and reserved blocks")
	require.Empty(t, svc.nativeRestore.files)
	for _, drive := range svc.nativeRestore.manifest.Drives {
		require.Equal(t, drive.File.SHA256, hashNativeFile(t, filepath.Join(svc.nativeRestore.artifact, drive.File.Path)), "failed restore must not alter artifact data")
	}
}
