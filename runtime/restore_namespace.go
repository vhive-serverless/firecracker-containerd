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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"golang.org/x/sys/unix"
)

const restoreNamespaceArg = "__firecracker_restore_namespace"
const restoreNamespaceMountArg = "__firecracker_restore_mount"
const restoreExecutable = "/.firecracker-restore-vmm"
const (
	restoreKVMDevice     = "/dev/kvm"
	restoreTunDevice     = "/dev/net/tun"
	restoreNullDevice    = "/dev/null"
	restoreZeroDevice    = "/dev/zero"
	restoreRandomDevice  = "/dev/random"
	restoreURandomDevice = "/dev/urandom"
	restoreProcDirectory = "/proc"
)

type restoreNamespacePlan struct {
	Root            string         `json:"root"`
	SharedDirectory string         `json:"shared_directory"`
	Binary          string         `json:"binary"`
	Args            []string       `json:"args"`
	Mounts          []restoreMount `json:"mounts"`
	Inputs          []string       `json:"inputs"`
}

// The helper execs the VMM in-place: the PID and process start time observed by
// crash recovery remain the VMM's identity throughout its lifetime.
func init() {
	if len(os.Args) != 3 {
		return
	}
	var err error
	switch os.Args[1] {
	case restoreNamespaceArg:
		err = enterRestoreNamespace(os.Args[2])
	case restoreNamespaceMountArg:
		err = installRestoreMount(os.Args[2])
	default:
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "prepare private restore namespace:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func restoreNamespaceCommand(ctx context.Context, shared, binary string, args []string, mounts []restoreMount, inputs []string) (*exec.Cmd, error) {
	shared, err := filepath.Abs(shared)
	if err != nil {
		return nil, err
	}
	binary, err = exec.LookPath(binary)
	if err != nil {
		return nil, err
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, err
	}
	if err := validateRestoreMounts(mounts, shared); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(shared, ".restore-root-")
	if err != nil {
		return nil, err
	}
	plan := restoreNamespacePlan{Root: root, SharedDirectory: shared, Binary: binary, Args: args, Mounts: mounts, Inputs: inputs}
	file, err := os.CreateTemp(shared, ".restore-namespace-")
	if err != nil {
		_ = os.Remove(root)
		return nil, err
	}
	err = json.NewEncoder(file).Encode(plan)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(file.Name())
		_ = os.Remove(root)
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, executable, restoreNamespaceArg, file.Name())
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNS}
	return cmd, nil
}

func enterRestoreNamespace(configPath string) error {
	runtime.LockOSThread()
	// A direct invocation of this private helper must never operate in the
	// caller's mount namespace either. Do this before any mount or path write.
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		return err
	}
	// #nosec G703 -- Internal self-exec argument names the caller-created plan; absolute plan paths are intentional and O_NOFOLLOW rejects a symlink at the final component.
	file, err := os.OpenFile(configPath, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	var plan restoreNamespacePlan
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&plan)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if !filepath.IsAbs(plan.Root) || filepath.Dir(plan.Root) != plan.SharedDirectory || filepath.Clean(plan.Root) != plan.Root {
		return fmt.Errorf("invalid private restore root")
	}
	if err := validateRestoreMounts(plan.Mounts, plan.SharedDirectory); err != nil {
		return err
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", plan.Root, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0700"); err != nil {
		return err
	}
	// All placeholders are created on this private tmpfs BEFORE host directory
	// binds. An absent source VM directory is never recreated on the host.
	type binding struct {
		source, target      string
		directory, readOnly bool
	}
	bindings := []binding{{restoreProcDirectory, restoreProcDirectory, true, true}, {plan.SharedDirectory, plan.SharedDirectory, true, false}, {plan.Binary, restoreExecutable, false, true}}
	for _, device := range []string{restoreKVMDevice, restoreTunDevice, restoreNullDevice, restoreZeroDevice, restoreRandomDevice, restoreURandomDevice} {
		if _, err := os.Stat(device); err == nil {
			bindings = append(bindings, binding{device, device, false, false})
		}
	}
	for _, mount := range plan.Mounts {
		bindings = append(bindings, binding{mount.Source, mount.Target, false, mount.ReadOnly})
	}
	seen := map[string]bool{restoreExecutable: true}
	for _, mount := range plan.Mounts {
		seen[mount.Target] = true
	}
	for _, input := range plan.Inputs {
		if !filepath.IsAbs(input) || filepath.Clean(input) != input {
			return fmt.Errorf("invalid restore input path")
		}
		if pathWithin(input, plan.SharedDirectory) {
			continue
		}
		if seen[input] {
			return fmt.Errorf("restore input overlaps device or executable")
		}
		for _, reserved := range []string{restoreKVMDevice, restoreTunDevice, restoreNullDevice, restoreZeroDevice, restoreRandomDevice, restoreURandomDevice, restoreProcDirectory} {
			if pathWithin(input, reserved) || pathWithin(reserved, input) {
				return fmt.Errorf("restore input overlaps system mount")
			}
		}
		seen[input] = true
		bindings = append(bindings, binding{input, input, false, true})
	}
	root, err := os.OpenRoot(plan.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, bind := range bindings {
		target := bind.target[1:]
		if err := root.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if bind.directory {
			if err := root.MkdirAll(target, 0755); err != nil {
				return err
			}
		} else {
			file, err := root.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		}
	}
	for _, bind := range bindings {
		target := filepath.Join(plan.Root, bind.target[1:])
		if err := unix.Mount(bind.source, target, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind %s at %s: %w", bind.source, bind.target, err)
		}
		if bind.readOnly {
			if err := unix.Mount("", target, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID, ""); err != nil {
				return err
			}
		}
	}
	if err := unix.Chroot(plan.Root); err != nil {
		return err
	}
	if err := unix.Chdir(plan.SharedDirectory); err != nil {
		return err
	}
	return unix.Exec(restoreExecutable, append([]string{restoreExecutable}, plan.Args...), os.Environ())
}

type restoreMountPlan struct {
	Process         vm.ProcessIdentity `json:"process"`
	SharedDirectory string             `json:"shared_directory"`
	Mount           restoreMount       `json:"mount"`
}

func addRestoreMount(ctx context.Context, process vm.ProcessIdentity, shared string, mount restoreMount) error {
	file, err := os.CreateTemp(shared, ".restore-add-mount-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(restoreMountPlan{process, shared, mount})
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	output, err := exec.CommandContext(ctx, executable, restoreNamespaceMountArg, file.Name()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("expose new disk to restored VMM: %s: %w", output, err)
	}
	return nil
}

func installRestoreMount(path string) (retErr error) {
	runtime.LockOSThread()
	// #nosec G703 -- Internal self-exec argument names the caller-created plan; decoded mounts and the target process are validated below.
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	var plan restoreMountPlan
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&plan)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := validateRestoreMounts([]restoreMount{plan.Mount}, plan.SharedDirectory); err != nil {
		return err
	}
	identity, err := vm.ReadProcessIdentity(plan.Process.PID)
	if err != nil || identity != plan.Process {
		return fmt.Errorf("restore VMM process identity changed")
	}
	prefix := "/proc/" + strconv.Itoa(plan.Process.PID)
	namespace, err := os.Open(prefix + "/ns/mnt")
	if err != nil {
		return err
	}
	defer namespace.Close()
	root, err := os.Open(prefix + "/root")
	if err != nil {
		return err
	}
	defer root.Close()
	ownNamespace, err := os.Stat("/proc/self/ns/mnt")
	if err != nil {
		return err
	}
	targetNamespace, err := namespace.Stat()
	if err != nil {
		return err
	}
	if os.SameFile(ownNamespace, targetNamespace) {
		return fmt.Errorf("refusing to install restore disk in host mount namespace")
	}
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(int(root.Fd()), &filesystem); err != nil {
		return err
	}
	if filesystem.Type != unix.TMPFS_MAGIC {
		return fmt.Errorf("restore mount target is not a private tmpfs root")
	}
	source, err := os.OpenFile(plan.Mount.Source, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer source.Close()
	// A normal bind from /proc/self/fd after setns is rejected when the
	// source belongs to the host mount namespace. Clone a detached mount
	// while still in that namespace, then move it into the private root.
	tree, err := unix.OpenTree(int(source.Fd()), "", unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH)
	if err != nil {
		return fmt.Errorf("pin detached new disk mount: %w", err)
	}
	defer unix.Close(tree)
	identity, err = vm.ReadProcessIdentity(plan.Process.PID)
	if err != nil || identity != plan.Process {
		return fmt.Errorf("restore VMM process identity changed")
	}
	if err := unix.Unshare(unix.CLONE_FS); err != nil {
		return fmt.Errorf("unshare restore helper filesystem: %w", err)
	}
	if err := unix.Setns(int(namespace.Fd()), unix.CLONE_NEWNS); err != nil {
		return fmt.Errorf("enter restored VMM mount namespace: %w", err)
	}
	if err := unix.Fchdir(int(root.Fd())); err != nil {
		return fmt.Errorf("enter restored VMM root directory: %w", err)
	}
	if err := unix.Chroot("."); err != nil {
		return fmt.Errorf("chroot to restored VMM: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return err
	}
	private, err := os.OpenRoot("/")
	if err != nil {
		return err
	}
	defer private.Close()
	target := plan.Mount.Target[1:]
	if err := private.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	placeholder, err := private.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if err := placeholder.Close(); err != nil {
		return err
	}
	mounted := false
	defer func() {
		if retErr != nil {
			if mounted {
				_ = unix.Unmount(plan.Mount.Target, unix.MNT_DETACH)
			}
			_ = os.Remove(plan.Mount.Target)
		}
	}()
	if err := unix.MoveMount(tree, "", unix.AT_FDCWD, plan.Mount.Target, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return fmt.Errorf("move pinned new disk mount at %s: %w", plan.Mount.Target, err)
	}
	mounted = true
	if plan.Mount.ReadOnly {
		return unix.Mount("", plan.Mount.Target, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID, "")
	}
	return nil
}
