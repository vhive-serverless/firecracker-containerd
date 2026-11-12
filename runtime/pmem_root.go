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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/firecracker-microvm/firecracker-containerd/proto"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"golang.org/x/sys/unix"
	gproto "google.golang.org/protobuf/proto"
)

const pmemRootID = "root_pmem"

func (s *service) captureImmutableRoot(ctx context.Context, stage string, manifest *nativeCheckpoint) error {
	if len(manifest.VMConfig.Pmem) == 0 {
		return nil
	}
	request := &proto.CreateVMRequest{}
	if err := json.Unmarshal(manifest.CreateRequest, request); err != nil {
		return err
	}
	root := request.GetRootDrive()
	if len(manifest.VMConfig.Pmem) != 1 || !root.GetUsePmem() || root.GetIsWritable() {
		return fmt.Errorf("unsupported pmem capture layout")
	}
	device := manifest.VMConfig.Pmem[0]
	expected := root.HostPath
	if _, jailed := s.jailer.(*runcJailer); jailed {
		expected = filepath.Base(expected)
	}
	if device == nil || device.ID == nil || *device.ID != pmemRootID || !device.ReadOnly || !device.RootDevice || device.PathOnHost == nil {
		return fmt.Errorf("pmem configuration no longer matches immutable root")
	}
	backing, remapped := restoreBackingPath(s.jailer, *device.PathOnHost)
	if !remapped {
		if *device.PathOnHost != expected {
			return fmt.Errorf("pmem configuration no longer matches immutable root path")
		}
		backing = root.HostPath
	}
	if err := verifyImmutableImage(ctx, backing, root.ImageSha256); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(stage, "pmem"), 0700); err != nil {
		return err
	}
	saved, err := copyNativeFile(ctx, backing, stage, "pmem/00000000")
	if err != nil {
		return err
	}
	if saved.SHA256 != root.ImageSha256 {
		return fmt.Errorf("pmem backing changed during capture")
	}
	manifest.Pmem = []nativePmem{{ID: pmemRootID, File: saved}}
	return syncNativeDirectory(filepath.Join(stage, "pmem"))
}

func (s *service) configureImmutableRoot(ctx context.Context, request *proto.CreateVMRequest) error {
	input := request.RootDrive
	if input == nil {
		input = &proto.FirecrackerRootDrive{HostPath: s.config.RootDrive, UsePmem: s.config.RootDriveUsePmem, ImageSha256: s.config.RootDriveSHA256}
	}
	if !input.UsePmem && input.ImageSha256 == "" {
		return nil
	}
	if input.IsWritable || (input.UsePmem && (input.Partuuid != "" || input.CacheType != "" || input.RateLimiter != nil || request.LoadSnapshot)) {
		return fmt.Errorf("immutable pmem roots require read-only whole images without block cache/rate settings or legacy restore")
	}
	cache := filepath.Join(s.config.ShimBaseDir, ".pmem-images")
	path, err := cacheImmutableImage(ctx, input.HostPath, cache, input.ImageSha256)
	if err != nil {
		return err
	}
	resolved := gproto.Clone(input).(*proto.FirecrackerRootDrive)
	resolved.HostPath, resolved.ImageSha256 = path, strings.ToLower(input.ImageSha256)
	request.RootDrive = resolved
	if !input.UsePmem {
		for index := range s.machineConfig.Drives {
			drive := &s.machineConfig.Drives[index]
			if drive.IsRootDevice != nil && *drive.IsRootDevice {
				drive.PathOnHost = path
			}
		}
		return nil
	}
	if err := validateDaxRootImage(path); err != nil {
		return err
	}
	// Restore mount mappings expose the verified cache inode at the captured
	// VMM path; the destination cache pathname need not match the source host.
	for _, argument := range strings.Fields(s.machineConfig.KernelArgs) {
		if strings.HasPrefix(argument, "rootfstype=") || strings.HasPrefix(argument, "rootflags=") || strings.HasPrefix(argument, "root=") {
			return fmt.Errorf("pmem root conflicts with explicit root kernel arguments")
		}
	}
	drives := s.machineConfig.Drives[:0]
	for _, drive := range s.machineConfig.Drives {
		if drive.IsRootDevice == nil || !*drive.IsRootDevice {
			drives = append(drives, drive)
		}
	}
	s.machineConfig.Drives = drives
	s.machineConfig.PmemDevices = []models.Pmem{{ID: fc.String(pmemRootID), PathOnHost: fc.String(path), ReadOnly: true, RootDevice: true}}
	s.machineConfig.KernelArgs += " rootfstype=ext4 rootflags=dax=always,noload"
	return nil
}

// cacheImmutableImage publishes verified bytes once, outside every VM-owned
// directory. Only read-only content is shared; writable uppers remain per-VM.
func cacheImmutableImage(ctx context.Context, source, directory, digest string) (string, error) {
	expected, err := hex.DecodeString(digest)
	if err != nil || len(expected) != sha256.Size || !filepath.IsAbs(source) || !filepath.IsAbs(directory) {
		return "", fmt.Errorf("immutable image needs absolute paths and a SHA256 digest")
	}
	digest = strings.ToLower(digest)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(directory, digest+".img")
	if err := verifyImmutableImage(ctx, path, digest); err == nil {
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	info, statErr := input.Stat()
	_ = input.Close()
	if statErr != nil {
		return "", statErr
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("pmem backing must be a regular image")
	}
	stage, err := os.MkdirTemp(directory, ".image-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	saved, err := copyNativeFile(ctx, source, stage, "image")
	if err != nil {
		return "", err
	}
	if saved.SHA256 != digest {
		return "", fmt.Errorf("immutable image checksum mismatch")
	}
	temporary := filepath.Join(stage, "image")
	// #nosec G302 -- Immutable bytes are shared across distinct jail UIDs; no writes are permitted.
	if err := os.Chmod(temporary, 0444); err != nil {
		return "", err
	}
	file, err := os.Open(temporary)
	if err != nil {
		return "", err
	}
	err = errors.Join(file.Sync(), file.Close())
	if err != nil {
		return "", err
	}
	if err := os.Link(temporary, path); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err := verifyImmutableImage(ctx, path, digest); err != nil {
		return "", err
	}
	if err := syncNativeDirectory(directory); err != nil {
		return "", err
	}
	return path, nil
}

func verifyImmutableImage(ctx context.Context, path, digest string) error {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0222 != 0 || info.Size() < 2<<20 || info.Size() > 1<<40 || info.Size()%(2<<20) != 0 {
		return fmt.Errorf("immutable image must be read-only, regular and 2 MiB aligned")
	}
	var backing unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &backing); err != nil {
		return err
	}
	switch backing.Type {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC:
	default:
		return fmt.Errorf("immutable image cache requires directly identified host disk storage")
	}
	hash := sha256.New()
	buffer := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := file.Read(buffer)
		if count > 0 {
			_, _ = hash.Write(buffer[:count])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("immutable cached image checksum mismatch")
	}
	return nil
}

func validateDaxRootImage(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	var super [1024]byte
	if _, err := file.ReadAt(super[:], 1024); err != nil {
		return err
	}
	if binary.LittleEndian.Uint16(super[56:58]) != 0xef53 || binary.LittleEndian.Uint32(super[24:28]) != 2 ||
		binary.LittleEndian.Uint16(super[58:60]) != 1 || binary.LittleEndian.Uint32(super[92:96])&4 != 0 {
		return fmt.Errorf("pmem root requires clean 4 KiB-block ext4 without a journal")
	}
	return nil
}
