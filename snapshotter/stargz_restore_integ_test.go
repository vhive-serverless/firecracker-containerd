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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/containerd/v2/pkg/ttrpcutil"
	"github.com/containerd/typeurl/v2"
	crimeta "github.com/firecracker-microvm/firecracker-containerd/internal/cri"
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
	stargzRestoreGuestStore       = "/var/lib/containerd-stargz-grpc"
	stargzRestoreGuestOverlay     = "/rom/overlay/stargz"
	stargzRestoreGuestDiskSizeMiB = uint32(128)
	stargzRestoreBridgeName       = "fc-sgz0"
	stargzRestoreHostIP           = "198.18.42.1"
	stargzRestoreGuestIP          = "198.18.42.2/24"
	stargzRestoreSubnet           = "198.18.42.0/24"
)

type stargzRestoreProfile struct {
	name     string
	rootExt4 bool
	pmem     bool
	reclaim  bool
}

type stargzRestoreFile struct {
	Path   string
	SHA256 string
	Size   int64
}

type stargzRestoreDrive struct {
	ID       string
	ReadOnly bool
	File     stargzRestoreFile
}

type stargzRestoreManifest struct {
	Version   int
	VMID      string
	Namespace string
	Drives    []stargzRestoreDrive
	Networks  []json.RawMessage
}

// TestStargzNativeRestore_Isolated exercises a retained stargz workload over
// native capture/restore. The checks cover the persisted guest stargz upper,
// restored task memory, replacement-VMM networking and host snapshotter RPC.
// This does not claim that the post-restore lower-file read is cold: the image
// handler intentionally prefetches 10 MiB when the image is unpacked.
func TestStargzNativeRestore_Isolated(t *testing.T) {
	integtest.Prepare(t, integtest.WithDefaultNetwork())
	nameservers := stargzRestoreCreateBridge(t)
	profiles := []stargzRestoreProfile{
		{name: "disk"},
		{name: "ext4-block", rootExt4: true},
		{name: "pmem", rootExt4: true, pmem: true},
		{name: "reclaim", reclaim: true},
		{name: "combined", rootExt4: true, pmem: true, reclaim: true},
		{name: "ext4-reclaim", rootExt4: true, reclaim: true},
	}
	for profileIndex, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			for jailerIndex, jailed := range []bool{false, true} {
				t.Run(fmt.Sprintf("jailed=%t", jailed), func(t *testing.T) {
					stargzRestoreCase(t, profileIndex, jailerIndex, profile, jailed, nameservers)
				})
			}
		})
	}
}

func stargzRestoreCase(t *testing.T, profileIndex, jailerIndex int, profile stargzRestoreProfile, jailed bool, nameservers []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 420*time.Second)
	t.Cleanup(cancel)
	id := fmt.Sprintf("sgzR-%d-%d", profileIndex, jailerIndex)
	ctx = namespaces.WithNamespace(ctx, id)
	netNS := fmt.Sprintf("/proc/%d/ns/net", os.Getpid())

	client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(integtest.FirecrackerRuntime))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = control.Close() })

	rootImage := "../tools/image-builder/rootfs-stargz.img"
	var rootDigest string
	if profile.rootExt4 {
		rootImage = "../tools/image-builder/rootfs-stargz-dax.img"
	}
	rootImage, err = filepath.Abs(rootImage)
	require.NoError(t, err)
	require.FileExists(t, rootImage)
	if profile.rootExt4 {
		rootDigest = stargzRestoreHash(t, rootImage)
	}
	tap := fmt.Sprintf("sgr%02d%d", profileIndex, jailerIndex)
	mac := fmt.Sprintf("02:fc:42:00:%02x:%02x", profileIndex, jailerIndex)
	stargzRestoreCreateTap(t, tap)
	staticNetwork := &proto.FirecrackerNetworkInterface{
		AllowMMDS: true,
		StaticConfig: &proto.StaticNetworkConfiguration{
			MacAddress:  mac,
			HostDevName: tap,
			IPConfig: &proto.IPConfiguration{
				PrimaryAddr: stargzRestoreGuestIP,
				GatewayAddr: stargzRestoreHostIP,
				Nameservers: nameservers,
			},
		},
	}

	request := &proto.CreateVMRequest{
		VMID: id, ContainerCount: 1,
		NetNS:             netNS,
		RootDrive:         &proto.FirecrackerRootDrive{HostPath: rootImage, ImageSha256: rootDigest, UsePmem: profile.pmem},
		GuestRoot:         &proto.GuestRootStorage{Mode: "disk", SizeMib: stargzRestoreGuestDiskSizeMiB},
		Reclamation:       &proto.GuestReclamation{Enabled: profile.reclaim},
		MachineCfg:        &proto.FirecrackerMachineConfiguration{VcpuCount: 1, MemSizeMib: 1024},
		NetworkInterfaces: []*proto.FirecrackerNetworkInterface{staticNetwork},
		TimeoutSeconds:    60,
	}
	if jailed {
		request.JailerConfig = &proto.JailerConfig{UID: 300001, GID: 300001}
	}
	_, err = control.CreateVM(ctx, request)
	require.NoError(t, err)
	vmStopped := false
	stopVM := func() {
		if vmStopped {
			return
		}
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stop()
		_, stopErr := control.StopVM(cleanup, &proto.StopVMRequest{VMID: id})
		if stopErr != nil && !strings.Contains(stopErr.Error(), "not found") {
			t.Errorf("stop VM %s: %v", id, stopErr)
		}
		vmStopped = true
	}
	t.Cleanup(stopVM)

	_, err = control.SetVMMetadata(ctx, &proto.SetVMMetadataRequest{
		VMID:     id,
		Metadata: fmt.Sprintf(dockerMetadataTemplate, "ghcr.io", noAuth, noAuth),
	})
	require.NoError(t, err)
	image, err := client.Pull(ctx, al2stargz,
		containerd.WithPullUnpack,
		containerd.WithPullSnapshotter(snapshotterName),
		containerd.WithImageHandlerWrapper(source.AppendDefaultLabelsHandlerWrapper(al2stargz, 10*mib)),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stop()
		if deleteErr := client.ImageService().Delete(cleanup, image.Name()); deleteErr != nil {
			t.Errorf("delete stargz image: %v", deleteErr)
		}
	})

	containerID := id + "-task"
	snapshotKey := id + "-snapshot"
	const app = `counter=40; token=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n'); while :; do if test -f /request; then rm /request; counter=$((counter+1)); printf '%s:%s\n' "$token" "$counter" >/response.next; mv /response.next /response; fi; sleep .02; done`
	container, err := client.NewContainer(ctx, containerID,
		containerd.WithSnapshotter(snapshotterName),
		containerd.WithNewSnapshot(snapshotKey, image),
		containerd.WithNewSpec(
			firecrackeroci.WithVMLocalImageConfig(image),
			firecrackeroci.WithVMID(id),
			firecrackeroci.WithVMNetwork,
			oci.WithDefaultPathEnv,
			oci.WithAnnotations(map[string]string{crimeta.ContainerTypeAnnotation: "container", crimeta.SandboxIDAnnotation: id}),
			oci.WithMounts([]specs.Mount{
				{Destination: "/rom", Type: "bind", Source: "/rom", Options: []string{"bind", "ro"}},
				{Destination: "/guest-stargz-upper", Type: "bind", Source: stargzRestoreGuestOverlay, Options: []string{"rbind", "ro"}},
				{Destination: "/stargz-store", Type: "bind", Source: stargzRestoreGuestStore, Options: []string{"rbind", "rw"}},
			}),
			oci.WithProcessArgs("/bin/sh", "-ec", app),
		),
	)
	require.NoError(t, err)
	containerDeleted := false
	t.Cleanup(func() {
		if containerDeleted {
			return
		}
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer stop()
		if deleteErr := container.Delete(cleanup, containerd.WithSnapshotCleanup); deleteErr != nil {
			t.Errorf("delete stargz task container: %v", deleteErr)
		}
	})
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, io.Discard, io.Discard)))
	require.NoError(t, err)
	require.NoError(t, task.Start(ctx))
	const probe = "rm -f /response; touch /request; while ! test -f /response; do sleep .01; done; cat /response"
	initial := stargzRestoreExec(ctx, t, task, probe)
	require.True(t, strings.HasSuffix(initial, ":41"), initial)
	upperBefore := stargzRestoreExec(ctx, t, task, `
set -eu
test "$(stat -f -c %t /stargz-store)" = ef53
test "$(stat -c '%d:%i' /stargz-store)" = "$(stat -c '%d:%i' /guest-stargz-upper)"
echo checkpoint-upper >/stargz-persist
sync
cat /stargz-persist
`)
	require.Equal(t, "checkpoint-upper", upperBefore)
	_, err = client.SnapshotService(snapshotterName).Stat(ctx, snapshotKey)
	require.NoError(t, err, "host demux snapshotter must resolve the source VM")

	artifact := filepath.Join(t.TempDir(), "native-checkpoint")
	_, err = control.CheckpointVM(ctx, &proto.CheckpointVMRequest{VMID: id, Directory: artifact})
	require.NoError(t, err)
	manifestBytes, err := os.ReadFile(filepath.Join(artifact, "manifest.json"))
	require.NoError(t, err)
	var manifest stargzRestoreManifest
	require.NoError(t, json.Unmarshal(manifestBytes, &manifest))
	require.Equal(t, 1, manifest.Version)
	require.Equal(t, id, manifest.VMID)
	require.Equal(t, id, manifest.Namespace)
	require.Len(t, manifest.Networks, 1)
	var overlay stargzRestoreDrive
	for _, drive := range manifest.Drives {
		if drive.ID == "guest_overlay" {
			overlay = drive
			break
		}
	}
	require.Equal(t, "guest_overlay", overlay.ID, "native artifact must contain the private guest upper used by stargz")
	artifactUpper := filepath.Join(artifact, overlay.File.Path)
	artifactUpperHash := stargzRestoreHash(t, artifactUpper)
	require.Equal(t, overlay.File.SHA256, artifactUpperHash)

	postCapture1 := stargzRestoreExec(ctx, t, task, probe)
	postCapture2 := stargzRestoreExec(ctx, t, task, probe)
	require.Equal(t, strings.TrimSuffix(initial, "41")+"42", postCapture1)
	require.Equal(t, strings.TrimSuffix(initial, "41")+"43", postCapture2)
	stargzRestoreExec(ctx, t, task, `echo source-after-checkpoint >/stargz-persist; sync; test "$(cat /stargz-persist)" = source-after-checkpoint`)

	sourceDir, err := vm.ShimDir(integtest.ShimBaseDir(), id, id)
	require.NoError(t, err)
	old, err := vm.ReadRecoveryState(sourceDir)
	require.NoError(t, err)
	_, err = control.StopVM(ctx, &proto.StopVMRequest{VMID: id})
	require.NoError(t, err)
	vmStopped = true
	_, err = os.Stat(fmt.Sprintf("/proc/%d", old.VMM.PID))
	require.True(t, os.IsNotExist(err), "the source VMM must be gone before restore")
	_, err = os.Stat(sourceDir.RootPath())
	require.True(t, os.IsNotExist(err), "native restore must not use source VM directories")

	stargzRestoreDeleteTap(t, tap)
	restoredTap := fmt.Sprintf("sgrR%02d%d", profileIndex, jailerIndex)
	stargzRestoreCreateTap(t, restoredTap)
	restoredNetwork := &proto.FirecrackerNetworkInterface{
		AllowMMDS: true,
		StaticConfig: &proto.StaticNetworkConfiguration{
			MacAddress:  mac,
			HostDevName: restoredTap,
			IPConfig: &proto.IPConfiguration{
				PrimaryAddr: stargzRestoreGuestIP,
				GatewayAddr: stargzRestoreHostIP,
				Nameservers: nameservers,
			},
		},
	}
	cloneRequest := &proto.CreateVMRequest{
		VMID: id, NativeCheckpointDirectory: artifact, TimeoutSeconds: 120, NetNS: netNS,
		NetworkInterfaces: []*proto.FirecrackerNetworkInterface{restoredNetwork},
	}
	if jailed {
		cloneRequest.JailerConfig = &proto.JailerConfig{UID: 300001, GID: 300001}
	}
	_, err = control.CreateVM(ctx, cloneRequest)
	require.NoError(t, err)
	vmStopped = false
	cloneDir, err := vm.ShimDir(integtest.ShimBaseDir(), id, id)
	require.NoError(t, err)
	current, err := vm.ReadRecoveryState(cloneDir)
	require.NoError(t, err)
	require.NotEqual(t, old.VMM.PID, current.VMM.PID)
	cloneRoot := cloneDir.RootPath()
	if jailed {
		cloneRoot = filepath.Join(cloneRoot, "rootfs")
	}
	cloneUpper := filepath.Join(cloneRoot, "native-restore", overlay.File.Path)
	cloneInfo, err := os.Stat(cloneUpper)
	require.NoError(t, err)
	capturedInfo, err := os.Stat(artifactUpper)
	require.NoError(t, err)
	require.False(t, os.SameFile(capturedInfo, cloneInfo), "restored guest upper must be a private copy")
	cloneUpperBefore := stargzRestoreHash(t, cloneUpper)
	var cloneFS unix.Statfs_t
	require.NoError(t, unix.Statfs(cloneUpper, &cloneFS))
	require.Contains(t, []int64{unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC}, int64(cloneFS.Type), "restored upper backing must be on a disk filesystem")

	address, err := os.ReadFile(cloneDir.AddrFilePath())
	require.NoError(t, err)
	connection, err := ttrpcutil.NewClient(string(address))
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	rpc, err := connection.Client()
	require.NoError(t, err)
	restored := taskAPI.NewTTRPCTaskClient(rpc)
	state, err := restored.State(ctx, &taskAPI.StateRequest{ID: containerID})
	require.NoError(t, err)
	require.Equal(t, uint32(task.Pid()), state.Pid)

	resumed := stargzRestoreExecTaskAPI(ctx, t, restored, containerID, probe)
	require.Equal(t, strings.TrimSuffix(initial, "41")+"42", resumed, "guest memory must resume at its checkpoint state")
	require.Equal(t, "checkpoint-upper", stargzRestoreExecTaskAPI(ctx, t, restored, containerID, "cat /stargz-persist"), "restored guest upper must contain its capture cutoff")
	require.NotEmpty(t, stargzRestoreExecTaskAPI(ctx, t, restored, containerID, `test -s /etc/system-release; cat /etc/system-release`), "restored task must be able to read a lower image file")
	require.Eventually(t, func() bool {
		_, err := client.SnapshotService(snapshotterName).Stat(ctx, snapshotKey)
		return err == nil
	}, 90*time.Second, time.Second, "cached host demux snapshotter RPC must reconnect to the restored VM")
	stargzRestoreCheckTCP(t, ctx, restored, containerID)
	stargzRestoreExecTaskAPI(ctx, t, restored, containerID, `echo clone-write >/stargz-persist; sync; test "$(cat /stargz-persist)" = clone-write`)
	require.Equal(t, artifactUpperHash, stargzRestoreHash(t, artifactUpper), "writes in restored stargz upper must not mutate the checkpoint")
	require.NotEqual(t, cloneUpperBefore, stargzRestoreHash(t, cloneUpper), "restored guest upper must receive the clone's stargz writes")

	_, err = restored.Kill(ctx, &taskAPI.KillRequest{ID: containerID, Signal: uint32(syscall.SIGKILL), All: true})
	require.NoError(t, err)
	_, err = restored.Wait(ctx, &taskAPI.WaitRequest{ID: containerID})
	require.NoError(t, err)
	_, err = restored.Delete(ctx, &taskAPI.DeleteRequest{ID: containerID})
	require.NoError(t, err)
	cleanup, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cleanupCancel()
	require.NoError(t, container.Delete(cleanup, containerd.WithSnapshotCleanup))
	containerDeleted = true
	_, err = client.SnapshotService(snapshotterName).Stat(ctx, snapshotKey)
	require.Error(t, err, "deleting the task snapshot must complete while the restored VM is still available")
	_, err = control.StopVM(cleanup, &proto.StopVMRequest{VMID: id})
	require.NoError(t, err)
	vmStopped = true
}

func stargzRestoreCreateBridge(t *testing.T) []string {
	t.Helper()
	previousForward, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", previousForward, 0600); err != nil {
			t.Errorf("restore ip_forward setting: %v", err)
		}
	})
	output, err := exec.Command("ip", "link", "add", stargzRestoreBridgeName, "type", "bridge").CombinedOutput()
	require.NoError(t, err, "create isolated test bridge: %s", output)
	uplink := ""
	t.Cleanup(func() {
		if uplink != "" {
			_ = stargzRestoreIPTables("-t", "nat", "-D", "POSTROUTING", "-s", stargzRestoreSubnet, "-o", uplink, "-j", "MASQUERADE")
			_ = stargzRestoreIPTables("-D", "FORWARD", "-i", stargzRestoreBridgeName, "-o", uplink, "-s", stargzRestoreSubnet, "-j", "ACCEPT")
			_ = stargzRestoreIPTables("-D", "FORWARD", "-i", uplink, "-o", stargzRestoreBridgeName, "-d", stargzRestoreSubnet, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT")
		}
		output, err := exec.Command("ip", "link", "del", stargzRestoreBridgeName).CombinedOutput()
		if err != nil && !strings.Contains(string(output), "Cannot find device") {
			t.Errorf("delete test bridge: %v: %s", err, output)
		}
	})
	for _, args := range [][]string{
		{"addr", "add", stargzRestoreHostIP + "/24", "dev", stargzRestoreBridgeName},
		{"link", "set", "dev", stargzRestoreBridgeName, "up"},
	} {
		output, err := exec.Command("ip", args...).CombinedOutput()
		require.NoError(t, err, "ip %v: %s", args, output)
	}
	require.NoError(t, os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0600))
	output, err = exec.Command("ip", "route", "get", "1.1.1.1").CombinedOutput()
	require.NoError(t, err, "find isolated Docker network uplink: %s", output)
	uplink = stargzRestoreParseUplink(t, string(output))
	rules := [][]string{
		{"-I", "FORWARD", "1", "-i", stargzRestoreBridgeName, "-o", uplink, "-s", stargzRestoreSubnet, "-j", "ACCEPT"},
		{"-I", "FORWARD", "1", "-i", uplink, "-o", stargzRestoreBridgeName, "-d", stargzRestoreSubnet, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		{"-t", "nat", "-I", "POSTROUTING", "1", "-s", stargzRestoreSubnet, "-o", uplink, "-j", "MASQUERADE"},
	}
	for _, rule := range rules {
		output, err = exec.Command("iptables", rule...).CombinedOutput()
		require.NoError(t, err, "iptables %v: %s", rule, output)
	}
	nameservers := stargzRestoreNameservers(t)
	t.Logf("isolated stargz network bridge=%s subnet=%s uplink=%s nameservers=%v", stargzRestoreBridgeName, stargzRestoreSubnet, uplink, nameservers)
	return nameservers
}

func stargzRestoreParseUplink(t *testing.T, route string) string {
	t.Helper()
	fields := strings.Fields(route)
	if len(fields) < 2 {
		t.Fatalf("default route has no uplink device: %q", route)
	}
	for index, field := range fields[:len(fields)-1] {
		if field == "dev" {
			return fields[index+1]
		}
	}
	t.Fatalf("default route has no uplink device: %q", route)
	return ""
}

func stargzRestoreIPTables(args ...string) error {
	return exec.Command("iptables", args...).Run()
}

func stargzRestoreNameservers(t *testing.T) []string {
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

func stargzRestoreCreateTap(t *testing.T, tap string) {
	t.Helper()
	for _, args := range [][]string{
		{"tuntap", "add", "dev", tap, "mode", "tap", "user", "300001"},
		{"link", "set", "dev", tap, "master", stargzRestoreBridgeName},
		{"link", "set", "dev", tap, "up"},
	} {
		output, err := exec.Command("ip", args...).CombinedOutput()
		require.NoError(t, err, "ip %v: %s", args, output)
	}
	t.Cleanup(func() {
		stargzRestoreDeleteTap(t, tap)
	})
}

func stargzRestoreDeleteTap(t *testing.T, tap string) {
	t.Helper()
	output, err := exec.Command("ip", "link", "del", tap).CombinedOutput()
	if err != nil && !strings.Contains(string(output), "Cannot find device") {
		t.Errorf("delete test TAP %s: %v: %s", tap, err, output)
	}
}

func stargzRestoreExec(ctx context.Context, t *testing.T, process containerd.Task, command string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	processID := fmt.Sprintf("stargz-exec-%d", time.Now().UnixNano())
	execProcess, err := process.Exec(ctx, processID, &specs.Process{Args: []string{"/bin/sh", "-ec", command}, Cwd: "/"}, cio.NewCreator(cio.WithStreams(nil, &stdout, &stderr)))
	require.NoError(t, err)
	wait, err := execProcess.Wait(ctx)
	require.NoError(t, err)
	require.NoError(t, execProcess.Start(ctx))
	select {
	case status := <-wait:
		require.NoError(t, status.Error())
		require.Zero(t, status.ExitCode(), stderr.String())
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err = execProcess.Delete(ctx)
	require.NoError(t, err)
	return strings.TrimSpace(stdout.String())
}

func stargzRestoreExecTaskAPI(ctx context.Context, t *testing.T, backend taskAPI.TTRPCTaskService, id, command string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	execID := fmt.Sprintf("stargz-native-exec-%d", time.Now().UnixNano())
	streams, err := cio.NewCreator(cio.WithStreams(nil, &stdout, &stderr))(execID)
	require.NoError(t, err)
	defer streams.Close()
	config := streams.Config()
	spec, err := typeurl.MarshalAnyToProto(&specs.Process{Args: []string{"/bin/sh", "-ec", command}, Cwd: "/"})
	require.NoError(t, err)
	_, err = backend.Exec(ctx, &taskAPI.ExecProcessRequest{ID: id, ExecID: execID, Spec: spec, Stdin: config.Stdin, Stdout: config.Stdout, Stderr: config.Stderr})
	require.NoError(t, err)
	_, err = backend.Start(ctx, &taskAPI.StartRequest{ID: id, ExecID: execID})
	require.NoError(t, err)
	result, err := backend.Wait(ctx, &taskAPI.WaitRequest{ID: id, ExecID: execID})
	require.NoError(t, err)
	require.Zero(t, result.ExitStatus, stderr.String())
	_, err = backend.Delete(ctx, &taskAPI.DeleteRequest{ID: id, ExecID: execID})
	require.NoError(t, err)
	streams.Wait()
	return strings.TrimSpace(stdout.String())
}

func stargzRestoreCheckTCP(t *testing.T, ctx context.Context, backend taskAPI.TTRPCTaskService, id string) {
	t.Helper()
	listener, err := net.Listen("tcp4", net.JoinHostPort(stargzRestoreHostIP, "0"))
	require.NoError(t, err)
	defer listener.Close()
	if tcp, ok := listener.(*net.TCPListener); ok {
		require.NoError(t, tcp.SetDeadline(time.Now().Add(20*time.Second)))
	}
	serverResult := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer connection.Close()
		_, err = io.WriteString(connection, "guest-network-ok\n")
		serverResult <- err
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	command := fmt.Sprintf(`/usr/bin/timeout 10 /bin/bash -ec 'exec 3<>/dev/tcp/%s/%d; IFS= read -r line <&3; printf "%%s" "$line"'`, stargzRestoreHostIP, port)
	require.Equal(t, "guest-network-ok", stargzRestoreExecTaskAPI(ctx, t, backend, id, command))
	require.NoError(t, <-serverResult, "restored guest must connect to the host over the replacement VMM network")
}

func stargzRestoreHash(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	require.NoError(t, err)
	return hex.EncodeToString(hash.Sum(nil))
}
