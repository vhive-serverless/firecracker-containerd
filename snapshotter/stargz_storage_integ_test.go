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
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	fccontrol "github.com/firecracker-microvm/firecracker-containerd/firecracker-control/client"
	"github.com/firecracker-microvm/firecracker-containerd/internal/integtest"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"
	"github.com/firecracker-microvm/firecracker-containerd/snapshotter/internal/integtest/stargz/fs/source"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const (
	stargzStorageGuestOverlayFile = "guest-overlay.ext4"
	stargzStorageGuestStore       = "/var/lib/containerd-stargz-grpc"
	stargzStorageGuestSource      = "/rom/overlay/stargz"
	stargzStorageGuestOverlaySize = uint32(128)
	stargzStorageBridgeName       = "fc-sgs0"
	stargzStorageBridgeMAC        = "02:fc:43:ff:ff:01"
	stargzStorageHostIP           = "198.18.43.1"
	stargzStorageSubnet           = "198.18.43.0/24"
)

type stargzStorageMode struct {
	name    string
	pmem    bool
	reclaim bool
}

type stargzStorageVM struct {
	id      string
	jailed  bool
	dir     vm.Dir
	disk    string
	stopped bool
}

// This runs the retained guest stargz service on each Step 4 storage arm.
// Each arm uses two VMs with the same guest path; one is jailed to verify both
// host ownership/cleanup modes and that their private ext4 backings do not alias.
func TestStargzGuestDiskStorage_Isolated(t *testing.T) {
	integtest.Prepare(t, integtest.WithDefaultNetwork())
	nameservers := stargzStorageCreateBridge(t)
	for _, mode := range []stargzStorageMode{
		{name: "disk"},
		{name: "reclaim", reclaim: true},
		{name: "pmem", pmem: true},
		{name: "combined", pmem: true, reclaim: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			stargzStorageTestPair(t, mode, nameservers)
		})
	}
}

func stargzStorageTestPair(t *testing.T, mode stargzStorageMode, nameservers []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	t.Cleanup(cancel)

	client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(integtest.FirecrackerRuntime))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, control.Close()) })

	rootImage := "../tools/image-builder/rootfs-stargz.img"
	if mode.pmem {
		rootImage = "../tools/image-builder/rootfs-stargz-dax.img"
	}
	rootImage, err = filepath.Abs(rootImage)
	require.NoError(t, err)
	if _, err := os.Stat(rootImage); err != nil {
		t.Fatalf("required %s image for the %s stargz storage case: %v", rootImage, mode.name, err)
	}
	rootDigest := ""
	if mode.pmem {
		rootDigest = stargzStorageHash(t, rootImage)
	}

	instances := []*stargzStorageVM{
		stargzStorageCreateVM(ctx, t, control, mode, rootImage, rootDigest, stargzStorageVMID(mode, false), false, nameservers),
		stargzStorageCreateVM(ctx, t, control, mode, rootImage, rootDigest, stargzStorageVMID(mode, true), true, nameservers),
	}

	firstInfo := stargzStorageCheckHostDisk(t, instances[0])
	secondInfo := stargzStorageCheckHostDisk(t, instances[1])
	require.False(t, os.SameFile(firstInfo, secondInfo), "VMs using the same stargz path must have distinct guest overlay inodes")
	firstStat := firstInfo.Sys().(*syscall.Stat_t)
	secondStat := secondInfo.Sys().(*syscall.Stat_t)
	require.NotEqual(t, firstStat.Ino, secondStat.Ino)

	for _, instance := range instances {
		vmCtx := namespaces.WithNamespace(ctx, instance.id)
		_, err := control.SetVMMetadata(vmCtx, &proto.SetVMMetadataRequest{
			VMID:     instance.id,
			Metadata: fmt.Sprintf(dockerMetadataTemplate, "ghcr.io", noAuth, noAuth),
		})
		require.NoError(t, err)
	}

	for index, instance := range instances {
		vmCtx := namespaces.WithNamespace(ctx, instance.id)
		image, err := client.Pull(vmCtx, al2stargz,
			containerd.WithPullUnpack,
			containerd.WithPullSnapshotter(snapshotterName),
			containerd.WithImageHandlerWrapper(source.AppendDefaultLabelsHandlerWrapper(al2stargz, 10*mib)),
		)
		require.NoError(t, err)
		imageDeleted := false
		t.Cleanup(func() {
			if imageDeleted {
				return
			}
			cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
			defer stop()
			cleanup = namespaces.WithNamespace(cleanup, instance.id)
			deleteErr := client.ImageService().Delete(cleanup, image.Name())
			require.NoError(t, deleteErr)
		})
		otherID := instances[1-index].id
		stargzStorageRunWorkload(vmCtx, t, client, image, instance, otherID, mode)
		err = client.ImageService().Delete(vmCtx, image.Name())
		require.NoError(t, err)
		imageDeleted = true
	}

	// Hold each disk inode across StopVM, which removes the VM directory. After
	// shutdown, debugfs independently reads the sync'd marker from the ext4 image.
	retained := make(map[string]string, len(instances))
	for _, instance := range instances {
		retainedPath := filepath.Join(filepath.Dir(instance.dir.RootPath()), instance.id+"-guest-overlay-audit.ext4")
		require.NoError(t, os.Link(instance.disk, retainedPath))
		retained[instance.id] = retainedPath
		t.Cleanup(func() { _ = os.Remove(retainedPath) })
	}

	for _, instance := range instances {
		stargzStorageStopVM(t, control, instance)
		marker := "stargz-storage-" + instance.id
		cmd := exec.CommandContext(ctx, "debugfs", "-R", "cat /stargz/"+marker, retained[instance.id])
		output, err := cmd.Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				t.Logf("debugfs stderr: %s", exitErr.Stderr)
			}
		}
		require.NoError(t, err, "read synced stargz marker from stopped guest disk")
		require.Equal(t, "synced-"+instance.id+"\n", string(output), "guest sync must persist stargz store data to the private disk")
	}

}

func stargzStorageCreateVM(ctx context.Context, t *testing.T, control *fccontrol.Client, mode stargzStorageMode, rootImage, rootDigest, id string, jailed bool, nameservers []string) *stargzStorageVM {
	t.Helper()
	tap := stargzStorageTapName(mode, jailed)
	stargzStorageCreateTap(t, tap, jailed)
	profileIndex := stargzStorageProfileIndex(mode)
	jailerIndex := 0
	if jailed {
		jailerIndex = 1
	}
	guestIP := fmt.Sprintf("198.18.43.%d/24", 2+profileIndex*2+jailerIndex)
	request := &proto.CreateVMRequest{
		VMID:           id,
		ContainerCount: 1,
		NetNS:          fmt.Sprintf("/proc/%d/ns/net", os.Getpid()),
		RootDrive: &proto.FirecrackerRootDrive{
			HostPath:    rootImage,
			UsePmem:     mode.pmem,
			ImageSha256: rootDigest,
		},
		GuestRoot:  &proto.GuestRootStorage{Mode: "disk", SizeMib: stargzStorageGuestOverlaySize},
		MachineCfg: &proto.FirecrackerMachineConfiguration{VcpuCount: 1, MemSizeMib: 1024},
		NetworkInterfaces: []*proto.FirecrackerNetworkInterface{{
			AllowMMDS: true,
			StaticConfig: &proto.StaticNetworkConfiguration{
				MacAddress:  fmt.Sprintf("02:fc:43:00:%02x:%02x", profileIndex, jailerIndex),
				HostDevName: tap,
				IPConfig: &proto.IPConfiguration{
					PrimaryAddr: guestIP,
					GatewayAddr: stargzStorageHostIP,
					Nameservers: nameservers,
				},
			},
		}},
		Reclamation:    &proto.GuestReclamation{Enabled: mode.reclaim},
		TimeoutSeconds: 60,
	}
	if jailed {
		request.JailerConfig = &proto.JailerConfig{UID: 300001, GID: 300001}
	}
	vmCtx := namespaces.WithNamespace(ctx, id)
	created, err := control.CreateVM(vmCtx, request)
	require.NoError(t, err)
	vmInfo, err := control.GetVMInfo(vmCtx, &proto.GetVMInfoRequest{VMID: id})
	require.NoError(t, err)
	directory, err := vm.ShimDir(integtest.ShimBaseDir(), id, id)
	require.NoError(t, err)
	root := directory.RootPath()
	apiSocketName := "firecracker.sock"
	if jailed {
		root = filepath.Join(root, "rootfs")
		apiSocketName = "api.socket"
	}
	require.Equal(t, filepath.Join(root, apiSocketName), vmInfo.SocketPath)
	require.Equal(t, vmInfo.SocketPath, created.SocketPath)
	require.Equal(t, filepath.Join(root, "firecracker.vsock"), vmInfo.VSockPath)
	for _, socketPath := range []string{vmInfo.SocketPath, vmInfo.VSockPath} {
		info, err := os.Stat(socketPath)
		require.NoError(t, err, "GetVMInfo socket must exist: %s", socketPath)
		require.NotZero(t, info.Mode()&os.ModeSocket, "GetVMInfo path must be a Unix socket: %s", socketPath)
	}
	instance := &stargzStorageVM{
		id: id, jailed: jailed, dir: directory,
		disk: filepath.Join(root, stargzStorageGuestOverlayFile),
	}
	t.Cleanup(func() {
		if instance.stopped {
			return
		}
		stargzStorageStopVM(t, control, instance)
	})
	return instance
}

func stargzStorageCheckHostDisk(t *testing.T, instance *stargzStorageVM) os.FileInfo {
	t.Helper()
	info, err := os.Stat(instance.disk)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	require.EqualValues(t, stargzStorageGuestOverlaySize<<20, info.Size())
	stat := info.Sys().(*syscall.Stat_t)
	require.NotZero(t, stat.Ino, "guest overlay must be a stable host inode")
	expectedUID := uint32(0)
	if instance.jailed {
		expectedUID = 300001
	}
	require.Equal(t, expectedUID, stat.Uid, "jailer mode must set the guest overlay ownership")
	require.GreaterOrEqual(t, uint64(stat.Blocks*512), uint64(info.Size()), "host backing must reserve the configured guest disk capacity")
	var backing unix.Statfs_t
	require.NoError(t, unix.Statfs(instance.disk, &backing))
	require.Contains(t, []int64{unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC}, backing.Type, "guest overlay backing must be on a disk filesystem")
	t.Logf("guest overlay id=%s jailed=%t host_fs=%#x uid=%d inode=%d blocks=%d size=%d", instance.id, instance.jailed, backing.Type, stat.Uid, stat.Ino, stat.Blocks, info.Size())
	return info
}

func stargzStorageRunWorkload(ctx context.Context, t *testing.T, client *containerd.Client, image containerd.Image, instance *stargzStorageVM, otherID string, mode stargzStorageMode) {
	t.Helper()
	containerID := instance.id + "-stargz"
	container, err := client.NewContainer(ctx, containerID,
		containerd.WithSnapshotter(snapshotterName),
		containerd.WithNewSnapshot(containerID+"-snapshot", image),
		containerd.WithNewSpec(
			firecrackeroci.WithVMLocalImageConfig(image),
			firecrackeroci.WithVMID(instance.id),
			firecrackeroci.WithVMNetwork,
			oci.WithDefaultPathEnv,
			oci.WithMounts([]specs.Mount{
				{Destination: "/rom", Type: "bind", Source: "/rom", Options: []string{"bind", "ro"}},
				{Destination: "/guest-upper", Type: "bind", Source: "/rom/overlay", Options: []string{"bind", "ro"}},
				{Destination: "/guest-stargz-upper", Type: "bind", Source: stargzStorageGuestSource, Options: []string{"bind", "rw"}},
				{Destination: "/stargz-store", Type: "bind", Source: stargzStorageGuestStore, Options: []string{"rbind", "rw"}},
			}),
			oci.WithProcessArgs("/bin/sh", "-ec", stargzStorageWorkloadScript, "--", instance.id, otherID, stargzStorageBool(mode.reclaim), stargzStorageBool(mode.pmem)),
		),
	)
	require.NoError(t, err)
	containerDeleted := false
	t.Cleanup(func() {
		if containerDeleted {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		cleanup = namespaces.WithNamespace(cleanup, instance.id)
		require.NoError(t, container.Delete(cleanup, containerd.WithSnapshotCleanup))
	})

	result, err := integtest.RunTask(ctx, container)
	require.NoError(t, err)
	require.Zero(t, result.ExitCode, "stargz storage workload failed: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	t.Logf("stargz storage VM=%s jailed=%t stdout:\n%s\nstderr:\n%s", instance.id, instance.jailed, result.Stdout, result.Stderr)
	require.Contains(t, result.Stdout, "stargz_store_magic=ef53")
	require.Contains(t, result.Stdout, "guest_upper_magic=ef53")
	require.Contains(t, result.Stdout, "stargz_upper_magic=ef53")
	require.Contains(t, result.Stdout, "stargz_overlay_upper_magic=ef53")
	require.Contains(t, result.Stdout, "stargz_overlay_work_magic=ef53")
	require.Contains(t, result.Stdout, "stargz-storage-copyup-rename-delete-enospc=passed")
	require.Contains(t, result.Stdout, "stargz-storage-sync-readback=passed")
	require.Contains(t, result.Stdout, fmt.Sprintf("damon_reclaim_enabled=%s", stargzStorageBool(mode.reclaim)))
	if mode.pmem {
		require.Contains(t, result.Stdout, " /rom ext4 ")
		require.Contains(t, result.Stdout, `"statx_dax":true`)
	} else {
		require.Contains(t, result.Stdout, " /rom squashfs ")
		require.Contains(t, result.Stdout, `"statx_dax":false`)
	}

	cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	cleanup = namespaces.WithNamespace(cleanup, instance.id)
	require.NoError(t, container.Delete(cleanup, containerd.WithSnapshotCleanup))
	containerDeleted = true
}

const stargzStorageWorkloadScript = `
set -eu
vm_id=$1
other_id=$2
reclaim_expected=$3
pmem_expected=$4
store=/stargz-store
marker="stargz-storage-$vm_id"

store_magic=$(stat -f -c %t "$store")
guest_upper_magic=$(stat -f -c %t /guest-upper)
stargz_upper_magic=$(stat -f -c %t /guest-stargz-upper)
printf 'stargz_store_magic=%s\n' "$store_magic"
printf 'guest_upper_magic=%s\n' "$guest_upper_magic"
printf 'stargz_upper_magic=%s\n' "$stargz_upper_magic"
test "$store_magic" = ef53
test "$guest_upper_magic" = ef53
test "$stargz_upper_magic" = ef53
test "$(stat -c '%d:%i' "$store")" = "$(stat -c '%d:%i' /guest-stargz-upper)"

lower_source=
upper_source=
work_source=
for options in $(awk '$3 == "overlay" {print $4}' /proc/mounts); do
    candidate_lower=$(printf '%s\n' "$options" | tr ',' '\n' | sed -n 's/^lowerdir=//p' | cut -d: -f1)
    candidate_upper=$(printf '%s\n' "$options" | tr ',' '\n' | sed -n 's/^upperdir=//p')
    candidate_work=$(printf '%s\n' "$options" | tr ',' '\n' | sed -n 's/^workdir=//p')
    case "$candidate_lower" in
        /var/lib/containerd-stargz-grpc/*|/rom/overlay/stargz/*) ;;
        *) continue ;;
    esac
    case "$candidate_upper" in
        /var/lib/containerd-stargz-grpc/*|/rom/overlay/stargz/*) ;;
        *) continue ;;
    esac
    case "$candidate_work" in
        /var/lib/containerd-stargz-grpc/*|/rom/overlay/stargz/*) ;;
        *) continue ;;
    esac
    lower_source=$candidate_lower
    upper_source=$candidate_upper
    work_source=$candidate_work
    break
done
test -n "$lower_source"
test -n "$upper_source"
test -n "$work_source"
map_stargz_path() {
    case "$1" in
        /var/lib/containerd-stargz-grpc/*) printf '/stargz-store/%s\n' "${1#/var/lib/containerd-stargz-grpc/}" ;;
        /rom/overlay/stargz/*) printf '/stargz-store/%s\n' "${1#/rom/overlay/stargz/}" ;;
        *) return 1 ;;
    esac
}
upper_path=$(map_stargz_path "$upper_source")
work_path=$(map_stargz_path "$work_source")
test -d "$upper_path"
test -d "$work_path"
upper_magic=$(stat -f -c %t "$upper_path")
work_magic=$(stat -f -c %t "$work_path")
printf 'stargz_overlay_upper_magic=%s\n' "$upper_magic"
printf 'stargz_overlay_work_magic=%s\n' "$work_magic"
printf 'stargz_overlay_upper_source=%s\n' "$upper_source"
printf 'stargz_overlay_work_source=%s\n' "$work_source"
test "$upper_magic" = ef53
test "$work_magic" = ef53

reclaim_enabled=$(cat /sys/module/damon_reclaim/parameters/enabled)
printf 'damon_reclaim_enabled=%s\n' "$reclaim_enabled"
test "$reclaim_enabled" = "$reclaim_expected"
if test "$reclaim_expected" = Y; then
    test "$(cat /sys/module/damon_reclaim/parameters/skip_anon)" = Y
fi

if test -e "$store/stargz-storage-$other_id"; then
    echo "store contains other VM's marker" >&2
    exit 41
fi
printf 'synced-%s\n' "$vm_id" >"$store/$marker"
sync
test "$(cat "$store/$marker")" = "synced-$vm_id"

lower_file=$(readlink -f /etc/os-release)
test -f "$lower_file"
test ! -L "$lower_file"
lower_snapshot=$(map_stargz_path "$lower_source")
lower_file_in_snapshot="$lower_snapshot$lower_file"
test -f "$lower_file_in_snapshot"
test ! -L "$lower_file_in_snapshot"
cmp "$lower_file" "$lower_file_in_snapshot"
printf 'stargz-lower-file=%s\n' "$lower_file"
cp "$lower_file_in_snapshot" /tmp/stargz-lower-before
printf '\nstargz-copy-up-%s\n' "$vm_id" >>"$lower_file"
cmp /tmp/stargz-lower-before "$lower_file" && exit 42
if printf 'forbidden\n' >>"$lower_file_in_snapshot" 2>/dev/shm/stargz-lower-write-error; then
    echo 'immutable stargz lower accepted a write' >&2
    exit 43
fi
cmp /tmp/stargz-lower-before "$lower_file_in_snapshot"
mv "$lower_file" "$lower_file.renamed"
test -s "$lower_file.renamed"
rm "$lower_file.renamed"
test ! -e "$lower_file"
printf 'stargz-copy-up-rename-delete=passed\n'

if dd if=/dev/zero of="$store/fcmod-enospc-fill" bs=1M count=256 2>/dev/shm/stargz-enospc-error; then
    echo 'stargz store exceeded its configured guest disk capacity' >&2
    exit 44
fi
grep -q 'No space left on device' /dev/shm/stargz-enospc-error
rm "$store/fcmod-enospc-fill"
sync
printf 'recovered-%s\n' "$vm_id" >"$store/fcmod-after-enospc"
sync
test "$(cat "$store/fcmod-after-enospc")" = "recovered-$vm_id"
printf 'stargz-enospc-recovery=passed\n'
printf 'stargz-storage-copyup-rename-delete-enospc=passed\n'
printf 'stargz-storage-sync-readback=passed\n'

/rom/usr/local/bin/fc-memory-probe -file /rom/usr/local/bin/agent -stat-only
cat /proc/mounts
`

func stargzStorageStopVM(t *testing.T, control *fccontrol.Client, instance *stargzStorageVM) {
	t.Helper()
	cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cleanup = namespaces.WithNamespace(cleanup, instance.id)
	_, err := control.StopVM(cleanup, &proto.StopVMRequest{VMID: instance.id})
	require.NoError(t, err)
	instance.stopped = true
	require.Eventually(t, func() bool {
		_, err := os.Stat(instance.disk)
		return os.IsNotExist(err)
	}, 10*time.Second, 100*time.Millisecond, "StopVM must clean the guest overlay for %s", instance.id)
}

func stargzStorageCreateBridge(t *testing.T) []string {
	t.Helper()
	previousForward, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	require.NoError(t, err)
	// Keep the gateway MAC stable as VM TAPs are added and removed.
	output, err := exec.Command("ip", "link", "add", stargzStorageBridgeName, "address", stargzStorageBridgeMAC, "type", "bridge").CombinedOutput()
	require.NoError(t, err, "create storage test bridge: %s", output)
	require.Equal(t, stargzStorageBridgeMAC, stargzStorageBridgeMACAddress(t))
	uplink := ""
	t.Cleanup(func() {
		if uplink != "" {
			_ = exec.Command("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", stargzStorageSubnet, "-o", uplink, "-j", "MASQUERADE").Run()
			_ = exec.Command("iptables", "-D", "FORWARD", "-i", stargzStorageBridgeName, "-o", uplink, "-s", stargzStorageSubnet, "-j", "ACCEPT").Run()
			_ = exec.Command("iptables", "-D", "FORWARD", "-i", uplink, "-o", stargzStorageBridgeName, "-d", stargzStorageSubnet, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT").Run()
		}
		output, err := exec.Command("ip", "link", "del", stargzStorageBridgeName).CombinedOutput()
		if err != nil && !strings.Contains(string(output), "Cannot find device") {
			t.Errorf("delete storage test bridge: %v: %s", err, output)
		}
		value := []byte("0\n")
		if strings.TrimSpace(string(previousForward)) == "1" {
			value = []byte("1\n")
		}
		if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", value, 0600); err != nil {
			t.Errorf("restore ip_forward setting: %v", err)
		}
	})
	for _, args := range [][]string{
		{"addr", "add", stargzStorageHostIP + "/24", "dev", stargzStorageBridgeName},
		{"link", "set", "dev", stargzStorageBridgeName, "up"},
	} {
		output, err := exec.Command("ip", args...).CombinedOutput()
		require.NoError(t, err, "ip %v: %s", args, output)
	}
	require.NoError(t, os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0600))
	output, err = exec.Command("ip", "route", "get", "1.1.1.1").CombinedOutput()
	require.NoError(t, err, "find storage test network uplink: %s", output)
	uplink = stargzStorageParseUplink(t, string(output))
	for _, rule := range [][]string{
		{"-I", "FORWARD", "1", "-i", stargzStorageBridgeName, "-o", uplink, "-s", stargzStorageSubnet, "-j", "ACCEPT"},
		{"-I", "FORWARD", "1", "-i", uplink, "-o", stargzStorageBridgeName, "-d", stargzStorageSubnet, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		{"-t", "nat", "-I", "POSTROUTING", "1", "-s", stargzStorageSubnet, "-o", uplink, "-j", "MASQUERADE"},
	} {
		output, err = exec.Command("iptables", rule...).CombinedOutput()
		require.NoError(t, err, "iptables %v: %s", rule, output)
	}
	nameservers := stargzStorageNameservers(t)
	t.Logf("isolated storage network bridge=%s subnet=%s uplink=%s nameservers=%v", stargzStorageBridgeName, stargzStorageSubnet, uplink, nameservers)
	return nameservers
}

func stargzStorageParseUplink(t *testing.T, route string) string {
	t.Helper()
	fields := strings.Fields(route)
	for index, field := range fields[:max(len(fields)-1, 0)] {
		if field == "dev" {
			return fields[index+1]
		}
	}
	t.Fatalf("default route has no uplink device: %q", route)
	return ""
}

func stargzStorageNameservers(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("/etc/resolv.conf")
	require.NoError(t, err)
	var nameservers []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "nameserver" {
			continue
		}
		ip := net.ParseIP(fields[1])
		if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		nameservers = append(nameservers, ip.String())
		if len(nameservers) == 2 {
			break
		}
	}
	if len(nameservers) == 0 {
		nameservers = []string{"1.1.1.1"}
	}
	return nameservers
}

func stargzStorageCreateTap(t *testing.T, tap string, jailed bool) {
	t.Helper()
	uid := "0"
	if jailed {
		uid = "300001"
	}
	output, err := exec.Command("ip", "tuntap", "add", "dev", tap, "mode", "tap", "user", uid).CombinedOutput()
	require.NoError(t, err, "create storage TAP %s: %s", tap, output)
	t.Cleanup(func() {
		output, err := exec.Command("ip", "link", "del", tap).CombinedOutput()
		if err != nil && !strings.Contains(string(output), "Cannot find device") {
			t.Errorf("delete storage TAP %s: %v: %s", tap, err, output)
		}
	})
	for _, args := range [][]string{
		{"link", "set", "dev", tap, "master", stargzStorageBridgeName},
		{"link", "set", "dev", tap, "up"},
	} {
		output, err := exec.Command("ip", args...).CombinedOutput()
		require.NoError(t, err, "ip %v: %s", args, output)
	}
	require.Equal(t, stargzStorageBridgeMAC, stargzStorageBridgeMACAddress(t), "attaching a TAP must not change the other guest's gateway MAC")
	t.Logf("storage TAP %s attached; bridge %s MAC=%s", tap, stargzStorageBridgeName, stargzStorageBridgeMACAddress(t))
}

func stargzStorageBridgeMACAddress(t *testing.T) string {
	t.Helper()
	address, err := os.ReadFile(filepath.Join("/sys/class/net", stargzStorageBridgeName, "address"))
	require.NoError(t, err, "read storage test bridge MAC")
	return strings.TrimSpace(string(address))
}

func stargzStorageProfileIndex(mode stargzStorageMode) int {
	return map[string]int{"disk": 0, "reclaim": 1, "pmem": 2, "combined": 3}[mode.name]
}

func stargzStorageTapName(mode stargzStorageMode, jailed bool) string {
	index := 0
	if jailed {
		index = 1
	}
	return fmt.Sprintf("sgs%d%d", stargzStorageProfileIndex(mode), index)
}

func stargzStorageVMID(mode stargzStorageMode, jailed bool) string {
	profile := map[string]string{
		"disk":     "d",
		"reclaim":  "r",
		"pmem":     "p",
		"combined": "c",
	}[mode.name]
	index := 0
	if jailed {
		index = 1
	}
	return fmt.Sprintf("sgz-%s-%d", profile, index)
}

func stargzStorageBool(enabled bool) string {
	if enabled {
		return "Y"
	}
	return "N"
}

func stargzStorageHash(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	require.NoError(t, err)
	return hex.EncodeToString(hash.Sum(nil))
}
