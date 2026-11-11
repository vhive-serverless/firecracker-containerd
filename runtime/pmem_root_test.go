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
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/firecracker-microvm/firecracker-containerd/config"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// A superblock fixture exercises validation; real ext4/DAX is tested in a VM.
func pmemImageFixture(t *testing.T) (string, string) {
	t.Helper()
	image := make([]byte, 2<<20)
	super := image[1024:2048]
	binary.LittleEndian.PutUint16(super[56:58], 0xef53)
	binary.LittleEndian.PutUint32(super[24:28], 2)
	binary.LittleEndian.PutUint16(super[58:60], 1)
	path := filepath.Join(t.TempDir(), "root.img")
	require.NoError(t, os.WriteFile(path, image, 0600))
	sum := sha256.Sum256(image)
	return path, hex.EncodeToString(sum[:])
}

func TestImmutableRootCache(t *testing.T) {
	ctx := context.Background()
	source, digest := pmemImageFixture(t)
	directory := filepath.Join(t.TempDir(), "cache")
	path, err := cacheImmutableImage(ctx, source, directory, strings.ToUpper(digest))
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0444), info.Mode().Perm())
	require.NoError(t, os.WriteFile(source, []byte("advanced source"), 0600))
	again, err := cacheImmutableImage(ctx, source, directory, digest)
	require.NoError(t, err)
	againInfo, err := os.Stat(again)
	require.NoError(t, err)
	require.True(t, os.SameFile(info, againInfo), "explicit digest sharing must reuse the same inode")
	require.NoError(t, verifyImmutableImage(ctx, path, digest))
	require.NoError(t, os.Chmod(path, 0600))
	_, err = cacheImmutableImage(ctx, source, directory, digest)
	require.ErrorContains(t, err, "read-only")
	// #nosec G302 -- Restore the deliberately world-readable immutable cache fixture.
	require.NoError(t, os.Chmod(path, 0444))
	require.NoError(t, os.Rename(path, path+".target"))
	require.NoError(t, os.Symlink(path+".target", path))
	_, err = cacheImmutableImage(ctx, source, directory, digest)
	require.Error(t, err, "cache symlinks must not be followed")
}

func TestImmutableRootRejectsInvalidBacking(t *testing.T) {
	ctx := context.Background()
	source, digest := pmemImageFixture(t)
	_, err := cacheImmutableImage(ctx, source, t.TempDir(), strings.Repeat("0", 64))
	require.ErrorContains(t, err, "checksum mismatch")
	_, err = cacheImmutableImage(ctx, source, t.TempDir(), "bad")
	require.Error(t, err)
	ram, err := os.MkdirTemp("/dev/shm", "fc-pmem-cache-")
	require.NoError(t, err)
	defer os.RemoveAll(ram)
	_, err = cacheImmutableImage(ctx, source, ram, digest)
	require.ErrorContains(t, err, "host disk storage")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = cacheImmutableImage(cancelled, source, t.TempDir(), digest)
	require.ErrorIs(t, err, context.Canceled)
	image, err := os.ReadFile(source)
	require.NoError(t, err)
	root, err := os.OpenRoot(filepath.Dir(source))
	require.NoError(t, err)
	defer root.Close()
	for _, offset := range []int{1024 + 56, 1024 + 24, 1024 + 58, 1024 + 92} {
		altered := append([]byte(nil), image...)
		altered[offset] ^= 4
		require.NoError(t, root.WriteFile(filepath.Base(source), altered, 0600))
		require.Error(t, validateDaxRootImage(source))
	}
}

func TestImmutableRootConfiguration(t *testing.T) {
	ctx := context.Background()
	source, digest := pmemImageFixture(t)
	cfg := &config.Config{ShimBaseDir: t.TempDir(), RootDrive: source, RootDriveUsePmem: true, RootDriveSHA256: digest}
	svc := &service{config: cfg, machineConfig: &fc.Config{KernelArgs: "ro", Drives: []models.Drive{
		{DriveID: fc.String("root_drive"), PathOnHost: source, IsRootDevice: fc.Bool(true), IsReadOnly: true},
		{DriveID: fc.String("container_0"), PathOnHost: "stub", IsRootDevice: fc.Bool(false)},
	}}}
	request := &proto.CreateVMRequest{}
	require.NoError(t, svc.configureImmutableRoot(ctx, request))
	require.True(t, request.RootDrive.UsePmem)
	require.Equal(t, source, cfg.RootDrive, "per-VM resolution must not mutate administrator defaults")
	require.Len(t, svc.machineConfig.Drives, 1)
	require.Equal(t, "container_0", *svc.machineConfig.Drives[0].DriveID)
	require.Len(t, svc.machineConfig.PmemDevices, 1)
	require.Contains(t, svc.machineConfig.KernelArgs, "rootflags=dax=always,noload")
	require.True(t, svc.machineConfig.PmemDevices[0].ReadOnly)

	block := &service{config: cfg, machineConfig: &fc.Config{Drives: []models.Drive{{IsRootDevice: fc.Bool(true)}}}}
	request = &proto.CreateVMRequest{RootDrive: &proto.FirecrackerRootDrive{HostPath: source, ImageSha256: digest, Partuuid: "retained", CacheType: "Writeback"}}
	require.NoError(t, block.configureImmutableRoot(ctx, request))
	require.Equal(t, "retained", request.RootDrive.Partuuid)
	require.Equal(t, "Writeback", request.RootDrive.CacheType)
	require.Empty(t, block.machineConfig.PmemDevices)
	require.Equal(t, *svc.machineConfig.PmemDevices[0].PathOnHost, block.machineConfig.Drives[0].PathOnHost)

	conflict := &service{config: cfg, machineConfig: &fc.Config{KernelArgs: "rootflags=discard"}}
	require.ErrorContains(t, conflict.configureImmutableRoot(ctx, &proto.CreateVMRequest{}), "conflicts")
	require.Error(t, svc.configureImmutableRoot(ctx, &proto.CreateVMRequest{RootDrive: &proto.FirecrackerRootDrive{UsePmem: true, IsWritable: true}}))
}

func TestImmutableRootCaptureAndCloneFiles(t *testing.T) {
	ctx := context.Background()
	source, digest := pmemImageFixture(t)
	cfg := &config.Config{ShimBaseDir: t.TempDir(), RootDrive: source, RootDriveUsePmem: true, RootDriveSHA256: digest}
	svc := &service{config: cfg, machineConfig: &fc.Config{KernelArgs: "ro"}}
	request := &proto.CreateVMRequest{}
	require.NoError(t, svc.configureImmutableRoot(ctx, request))
	savedRequest, err := json.Marshal(request)
	require.NoError(t, err)
	live := svc.machineConfig.PmemDevices[0]
	manifest := &nativeCheckpoint{Version: 1, RuntimeConfig: cfg, CreateRequest: savedRequest, VMConfig: &models.FullVMConfiguration{Pmem: []*models.Pmem{&live}}}
	artifact := t.TempDir()
	require.NoError(t, svc.captureImmutableRoot(ctx, artifact, manifest))
	require.Len(t, manifest.Pmem, 1)
	for _, name := range []string{"memory", "state"} {
		require.NoError(t, os.WriteFile(filepath.Join(artifact, name), []byte(name), 0600))
		file, err := copyNativeFile(ctx, filepath.Join(artifact, name), t.TempDir(), name)
		require.NoError(t, err)
		if name == "memory" {
			manifest.Memory = file
		} else {
			manifest.State = file
		}
	}
	require.NoError(t, validateNativeRestore(manifest))
	manifest.Pmem[0].File.SHA256 = strings.Repeat("0", 64)
	require.Error(t, validateNativeRestore(manifest))
	manifest.Pmem[0].File.SHA256 = digest
	live.ReadOnly = false
	require.Error(t, validateNativeRestore(manifest))
	live.ReadOnly = true
	require.NoError(t, os.Remove(request.RootDrive.HostPath))
	clone := &service{config: cfg, jailer: newNoopJailer(ctx, logrus.NewEntry(logrus.New()), vm.Dir(t.TempDir())),
		nativeRestore: &nativeRestore{manifest: manifest, artifact: artifact, files: make(map[string]string)}, machineConfig: &fc.Config{KernelArgs: "ro"}}
	restored := &proto.CreateVMRequest{}
	require.NoError(t, clone.cloneNativeRestore(ctx, restored))
	require.True(t, restored.RootDrive.UsePmem)
	require.NoError(t, clone.configureImmutableRoot(ctx, restored))
	require.NoError(t, verifyImmutableImage(ctx, restored.RootDrive.HostPath, digest))
	require.NoError(t, os.WriteFile(clone.nativeRestore.files["memory"], []byte("changed clone"), 0600))
	data, err := os.ReadFile(filepath.Join(artifact, "memory"))
	require.NoError(t, err)
	require.Equal(t, "memory", string(data), "cloned files must not advance the artifact")
	live.PathOnHost = fc.String("/relocated/cache.img")
	require.ErrorContains(t, clone.configureImmutableRoot(ctx, restored), "captured immutable cache pathname")
}
