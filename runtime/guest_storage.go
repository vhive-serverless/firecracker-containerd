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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/firecracker-microvm/firecracker-containerd/proto"
	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"golang.org/x/sys/unix"
)

const (
	guestOverlayDrive = "guest_overlay"
	guestOverlayFile  = "guest-overlay.ext4"
	guestRootRAMMode  = "ram"
)

// configureGuestRoot owns only the guest OS writable upper. Workload root disks
// continue to be managed by the disk-backed snapshotter and its drive slots.
func (s *service) configureGuestRoot(ctx context.Context, request *proto.CreateVMRequest) error {
	if s.nativeRestore != nil {
		// The captured layout wins over current defaults. Never format or attach
		// a fresh upper to a restored memory image. All saved drives are cloned
		// by cloneNativeRestore and passed through nativeLoadHandler overrides.
		return nil
	}
	mode, size := s.config.GuestRootMode, s.config.GuestRootSizeMiB
	if request.GuestRoot != nil {
		if request.GuestRoot.Mode != "" {
			mode = request.GuestRoot.Mode
		}
		if request.GuestRoot.SizeMib != 0 {
			size = request.GuestRoot.SizeMib
		}
	}
	if mode == "" {
		mode = "disk"
	}
	if size == 0 {
		size = 512
	}
	if mode != "disk" && mode != guestRootRAMMode {
		return fmt.Errorf("guest root mode must be disk or ram")
	}
	if size < 64 || size > 65536 {
		return fmt.Errorf("guest root capacity must be between 64 and 65536 MiB")
	}
	for _, argument := range strings.Fields(s.machineConfig.KernelArgs) {
		if strings.HasPrefix(argument, "overlay_root=") {
			return fmt.Errorf("configure the guest upper through GuestRoot, not overlay_root in kernel arguments")
		}
	}
	if request.LoadSnapshot && mode != guestRootRAMMode {
		return fmt.Errorf("legacy vHive restore only supports the explicit original ram layout")
	}
	request.GuestRoot = &proto.GuestRootStorage{Mode: mode, SizeMib: size}
	if mode == guestRootRAMMode {
		s.machineConfig.KernelArgs += " overlay_root=ram"
		return nil
	}
	if request.GetRootDrive().GetIsWritable() {
		return fmt.Errorf("disk overlay requires an immutable read-only root drive")
	}
	path := filepath.Join(s.jailer.JailPath().RootPath(), guestOverlayFile)
	uuid, err := createGuestOverlay(ctx, path, size, s.jailer.StubDrivesOptions()...)
	if err != nil {
		return err
	}
	s.machineConfig.Drives = append(s.machineConfig.Drives, models.Drive{
		DriveID: firecracker.String(guestOverlayDrive), PathOnHost: path,
		IsRootDevice: firecracker.Bool(false), IsReadOnly: false,
		CacheType: firecracker.String(models.DriveCacheTypeWriteback),
	})
	// UUID selection survives changes to virtio-blk enumeration (including a
	// future pmem root); block names such as vdb are not a stable contract.
	s.machineConfig.KernelArgs += " overlay_root=UUID=" + uuid
	return nil
}

func createGuestOverlay(ctx context.Context, path string, sizeMiB uint32, options ...FileOpt) (_ string, err error) {
	if sizeMiB < 64 || sizeMiB > 65536 {
		return "", fmt.Errorf("invalid guest root capacity")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	defer root.Close()
	name := filepath.Base(path)
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil {
			_ = root.Remove(name)
		}
	}()
	var backing unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &backing); err != nil {
		return "", err
	}
	// Require a directly identifiable disk filesystem. Overlay, network and
	// unknown mounts cannot prove that the actual upper avoids RAM backing.
	switch backing.Type {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC:
	default:
		return "", fmt.Errorf("guest upper requires ext4/ext3, XFS or Btrfs host storage (filesystem magic %#x)", backing.Type)
	}
	capacity := int64(sizeMiB) << 20
	if err := file.Truncate(capacity); err != nil {
		return "", err
	}
	var identifier [16]byte
	if _, err := rand.Read(identifier[:]); err != nil {
		return "", err
	}
	identifier[6] = (identifier[6] & 0x0f) | 0x40
	identifier[8] = (identifier[8] & 0x3f) | 0x80
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x", identifier[:4], identifier[4:6], identifier[6:8], identifier[8:10], identifier[10:])
	// Avoid discard operations. Eager metadata/journal initialization
	// keeps first-boot writes and benchmark timing out of lazy format workers.
	command := exec.CommandContext(ctx, "mkfs.ext4", "-q", "-F", "-m", "0", "-U", uuid,
		"-E", "nodiscard,lazy_itable_init=0,lazy_journal_init=0", "/proc/self/fd/3")
	// Format the already-owned inode; a replacement pathname cannot redirect mkfs.
	command.ExtraFiles = []*os.File{file}
	if output, err := command.CombinedOutput(); err != nil {
		return "", fmt.Errorf("format guest upper: %w: %s", err, output)
	}
	// Reserve after formatting: mkfs can punch zero ranges even with nodiscard.
	// Publishing a sparse disk would defer host ENOSPC until arbitrary guest I/O.
	if err := reserveGuestDisk(ctx, file, capacity); err != nil {
		return "", err
	}
	for _, option := range options {
		if err := option(file); err != nil {
			return "", err
		}
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	return uuid, nil
}

func reserveGuestDisk(ctx context.Context, file *os.File, capacity int64) error {
	if err := unix.Fallocate(int(file.Fd()), 0, 0, capacity); err == nil {
		return ctx.Err()
	} else if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOSYS) {
		return fmt.Errorf("reserve guest disk capacity: %w", err)
	}
	// ext3 cannot reserve unwritten extents. Rewrite the formatted contents to
	// materialize every block without changing the filesystem's bytes.
	buffer := make([]byte, 128<<10)
	for offset := int64(0); offset < capacity; offset += int64(len(buffer)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := file.ReadAt(buffer, offset); err != nil {
			return err
		}
		if _, err := file.WriteAt(buffer, offset); err != nil {
			return err
		}
	}
	return nil
}
