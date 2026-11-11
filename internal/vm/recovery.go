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

package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/containerd/v2/pkg/atomicfile"
	runc "github.com/containerd/go-runc"
	"github.com/containernetworking/cni/libcni"
	"golang.org/x/sys/unix"
)

// RecoveryFileName holds the private per-VM crash recovery metadata.
const RecoveryFileName = "recovery.json"

// ProcessIdentity distinguishes an owned process from a reused numeric PID.
type ProcessIdentity struct {
	PID        int    `json:"pid"`
	StartTicks string `json:"start_ticks"`
}

// ReadProcessIdentity reads the PID and kernel start time from procfs.
func ReadProcessIdentity(pid int) (ProcessIdentity, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ProcessIdentity{}, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return ProcessIdentity{}, fmt.Errorf("invalid process stat for %d", pid)
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return ProcessIdentity{}, fmt.Errorf("short process stat for %d", pid)
	}
	return ProcessIdentity{PID: pid, StartTicks: fields[19]}, nil
}

// RecoveryNetwork retains the exact inputs required for an idempotent CNI DEL.
type RecoveryNetwork struct {
	Config   json.RawMessage    `json:"config"`
	BinPath  []string           `json:"bin_path"`
	CacheDir string             `json:"cache_dir"`
	Runtime  libcni.RuntimeConf `json:"runtime"`
}

// RecoveryState is private host metadata, written before launching the VMM and
// updated after network setup and process creation. It survives a shim crash.
type RecoveryState struct {
	Version    int               `json:"version"`
	VMID       string            `json:"vm_id"`
	Namespace  string            `json:"namespace"`
	Shim       ProcessIdentity   `json:"shim"`
	VMM        ProcessIdentity   `json:"vmm"`
	RuncBinary string            `json:"runc_binary,omitempty"`
	Networks   []RecoveryNetwork `json:"networks,omitempty"`
	NetNS      string            `json:"netns,omitempty"`
	OwnNetNS   bool              `json:"own_netns"`
}

// WriteRecoveryState atomically replaces a VM's private recovery metadata.
func WriteRecoveryState(dir Dir, state RecoveryState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := atomicfile.New(filepath.Join(dir.RootPath(), RecoveryFileName), 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Cancel()
		return err
	}
	return f.Close()
}

// ReadRecoveryState validates metadata against its containing VM directory.
func ReadRecoveryState(dir Dir) (RecoveryState, error) {
	var state RecoveryState
	data, err := os.ReadFile(filepath.Join(dir.RootPath(), RecoveryFileName))
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Version != 1 {
		return state, fmt.Errorf("unsupported recovery version %d", state.Version)
	}
	expected, err := ShimDir(filepath.Dir(dir.RootPath()), state.Namespace, state.VMID)
	if err != nil || expected != dir {
		return state, fmt.Errorf("recovery identity does not match VM directory %q", dir)
	}
	if state.Shim.PID <= 1 || state.Shim.StartTicks == "" || state.VMM.PID < 0 || state.VMM.PID == 1 {
		return state, fmt.Errorf("invalid recovery process identity")
	}
	return state, nil
}

// IsProcessAlive checks identity and treats a zombie as exited.
func IsProcessAlive(identity ProcessIdentity) (bool, error) {
	actual, err := ReadProcessIdentity(identity.PID)
	if os.IsNotExist(err) || (err == nil && actual != identity) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	fd, err := unix.PidfdOpen(identity.PID, 0)
	if err != nil {
		// Some kernels return EINVAL for a reaped thread-group leader.
		if _, statErr := ReadProcessIdentity(identity.PID); os.IsNotExist(statErr) {
			return false, nil
		}
	}
	if errors.Is(err, unix.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	actual, err = ReadProcessIdentity(identity.PID)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if actual != identity {
		return false, nil
	}
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := unix.Poll(poll, 0)
	return n == 0, err
}

// WaitProcessExit waits for the recorded process identity to exit.
func WaitProcessExit(ctx context.Context, identity ProcessIdentity) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		alive, err := IsProcessAlive(identity)
		if err != nil || !alive {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// StopProcess signals the recorded process and waits up to five seconds for
// exit. A pidfd prevents PID reuse from redirecting the signal.
func StopProcess(identity ProcessIdentity, signal unix.Signal) error {
	if identity.PID == 0 {
		return nil
	}
	actual, err := ReadProcessIdentity(identity.PID)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if actual != identity {
		return fmt.Errorf("refusing to signal reused PID %d", identity.PID)
	}
	fd, err := unix.PidfdOpen(identity.PID, 0)
	if err != nil {
		if _, statErr := ReadProcessIdentity(identity.PID); os.IsNotExist(statErr) {
			return nil
		}
	}
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	actual, err = ReadProcessIdentity(identity.PID)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if actual != identity {
		return fmt.Errorf("refusing to signal reused PID %d", identity.PID)
	}
	if err := unix.PidfdSendSignal(fd, signal, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	if n, err := unix.Poll(poll, 5000); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("process %d did not exit", identity.PID)
	}
	return nil
}

// RecoverVM is idempotent and scoped to one VM. Call it only after the shim is
// known to have exited. Failed cleanup retains metadata for a subsequent retry.
func RecoverVM(ctx context.Context, dir Dir) error {
	lock, err := os.OpenFile(filepath.Join(dir.RootPath(), "recovery.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	state, err := ReadRecoveryState(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if alive, err := IsProcessAlive(state.Shim); err != nil {
		return err
	} else if alive {
		return fmt.Errorf("shim %d is still alive", state.Shim.PID)
	}
	if err := StopProcess(state.VMM, unix.SIGKILL); err != nil {
		return err
	}
	if state.RuncBinary != "" {
		client := runc.Runc{Command: state.RuncBinary}
		if err := DeleteRuncBundle(ctx, &client, state.VMID, dir.RootPath()); err != nil {
			return err
		}
	}
	for _, network := range state.Networks {
		conf, err := libcni.ConfListFromBytes(network.Config)
		if err != nil {
			return err
		}
		client := libcni.NewCNIConfigWithCacheDir(network.BinPath, network.CacheDir, nil)
		if err := client.DelNetworkList(ctx, conf, &network.Runtime); err != nil {
			return err
		}
		// Only the SDK's default cache is private to this VM. Preserve shared
		// or custom caches and any data that remains after CNI DEL.
		if network.CacheDir == filepath.Join("/var/lib/cni", state.VMID) {
			for _, path := range []string{filepath.Join(network.CacheDir, "results"), network.CacheDir} {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) && !errors.Is(err, unix.ENOTEMPTY) {
					return err
				}
			}
		}
	}
	if state.OwnNetNS && state.NetNS != "" {
		if err := unix.Unmount(state.NetNS, unix.MNT_DETACH); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
			return err
		}
		if err := os.Remove(state.NetNS); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Remove(filepath.Join(dir.RootPath(), RecoveryFileName))
}

// OwnedRuncContainer refuses to operate on an ID belonging to another bundle.
// Explicit VM IDs must be globally unique, including across namespaces.
func OwnedRuncContainer(ctx context.Context, client *runc.Runc, id, bundle string) (*runc.Container, error) {
	containers, err := client.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, container := range containers {
		if container.ID != id {
			continue
		}
		if filepath.Clean(container.Bundle) != filepath.Clean(bundle) {
			return nil, fmt.Errorf("runc ID %q belongs to another bundle %q", id, container.Bundle)
		}
		return container, nil
	}
	return nil, nil
}

// DeleteRuncBundle deletes only the jail owned by this VM directory.
func DeleteRuncBundle(ctx context.Context, client *runc.Runc, id, bundle string) error {
	container, err := OwnedRuncContainer(ctx, client, id, bundle)
	if err != nil || container == nil {
		return err
	}
	return client.Delete(ctx, id, &runc.DeleteOpts{Force: true})
}

// BundleVMDir reads only the VM identifier written by the bootstrap helper;
// paths are reconstructed through the normal identifier validation.
func BundleVMDir(shimBase, namespace, bundlePath string) (Dir, error) {
	data, err := os.ReadFile(filepath.Join(bundlePath, "firecracker-vm-id"))
	if err != nil {
		return "", err
	}
	return ShimDir(shimBase, namespace, strings.TrimSpace(string(data)))
}
