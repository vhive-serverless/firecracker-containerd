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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/containerd/v2/pkg/ttrpcutil"
	"github.com/containerd/fifo"
	"github.com/containerd/typeurl/v2"
	crimeta "github.com/firecracker-microvm/firecracker-containerd/internal/cri"
	"github.com/firecracker-microvm/firecracker-containerd/internal/integtest"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
)

func TestNativeRestore_Isolated(t *testing.T) {
	testNativeRestore(t, nil)
}

type nativeRestoreStorageCase struct {
	name                string
	ext4, pmem, reclaim bool
}

// This exercises the runtime's captured private disks and guest process state.
// It does not recreate containerd or Kubernetes sandbox ownership.
func TestNativeRestoreStorage_Isolated(t *testing.T) {
	for _, storage := range []nativeRestoreStorageCase{
		{name: "disk"},
		{name: "ext4-block", ext4: true},
		{name: "pmem", ext4: true, pmem: true},
		{name: "reclaim", reclaim: true},
		{name: "combined", ext4: true, pmem: true, reclaim: true},
		{name: "ext4-reclaim", ext4: true, reclaim: true},
	} {
		t.Run(storage.name, func(t *testing.T) { testNativeRestore(t, &storage) })
	}
}

func testNativeRestore(t *testing.T, storage *nativeRestoreStorageCase) {
	t.Helper()
	integtest.Prepare(t)
	for _, jailed := range []bool{false, true} {
		t.Run(fmt.Sprintf("jailed=%v", jailed), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
			t.Cleanup(cancel)
			ctx = namespaces.WithNamespace(ctx, defaultNamespace)
			client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(firecrackerRuntime))
			require.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })
			control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
			require.NoError(t, err)
			t.Cleanup(func() { _ = control.Close() })
			id := fmt.Sprintf("native-restore-source-%v", jailed)
			request := &proto.CreateVMRequest{VMID: id, ContainerCount: 2, MachineCfg: &proto.FirecrackerMachineConfiguration{MemSizeMib: 512}}
			if storage != nil {
				id = fmt.Sprintf("nr-%s-%v", storage.name, jailed)
				request.VMID = id
				request.GuestRoot = &proto.GuestRootStorage{Mode: "disk", SizeMib: 128}
				request.Reclamation = &proto.GuestReclamation{Enabled: storage.reclaim}
				if storage.ext4 {
					path, err := filepath.Abs("../tools/image-builder/rootfs-dax.img")
					require.NoError(t, err)
					request.RootDrive = &proto.FirecrackerRootDrive{HostPath: path, UsePmem: storage.pmem, ImageSha256: hashPmemImage(t, path)}
				}
			}
			if jailed {
				request.JailerConfig = &proto.JailerConfig{UID: 300001, GID: 300001}
			}
			_, err = control.CreateVM(ctx, request)
			require.NoError(t, err)
			sourceStopped := false
			t.Cleanup(func() {
				if !sourceStopped {
					_, _ = control.StopVM(context.WithoutCancel(ctx), &proto.StopVMRequest{VMID: id})
				}
			})
			image, err := alpineImage(ctx, client, defaultSnapshotterName)
			require.NoError(t, err)
			shared := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(shared, "state"), []byte("volume-41"), 0600))
			tasks := make([]containerd.Task, 0, 2)
			initial := make([]string, 0, 2)
			const app = `counter=40; token=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n'); while :; do if test -f /request; then rm /request; counter=$((counter+1)); echo "$token:$counter" >/response; echo "$token:$counter"; fi; sleep .02; done`
			const probe = "rm -f /response; touch /request; while ! test -f /response; do sleep .01; done; cat /response"
			for index := range 2 {
				name := fmt.Sprintf("%s-%d", id, index)
				container, err := client.NewContainer(ctx, name, containerd.WithSnapshotter(defaultSnapshotterName), containerd.WithNewSnapshot(name, image),
					containerd.WithNewSpec(firecrackeroci.WithVMID(id), oci.WithDefaultPathEnv,
						oci.WithAnnotations(map[string]string{crimeta.ContainerTypeAnnotation: "container", crimeta.SandboxIDAnnotation: id}),
						oci.WithMounts([]specs.Mount{{Type: "bind", Source: shared, Destination: "/shared", Options: []string{"rbind", "rw"}}}),
						oci.WithProcessArgs("/bin/sh", "-ec", app)))
				require.NoError(t, err)
				t.Cleanup(func() { _ = container.Delete(context.WithoutCancel(ctx), containerd.WithSnapshotCleanup) })
				process, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, io.Discard, io.Discard)))
				require.NoError(t, err)
				require.NoError(t, process.Start(ctx))
				value := nativeExec(ctx, t, process, probe)
				require.True(t, strings.HasSuffix(value, ":41"), value)
				initial = append(initial, value)
				tasks = append(tasks, process)
			}
			if storage != nil {
				writeGuestUpperState(ctx, t, client, id, "before")
				for _, process := range tasks {
					require.Equal(t, "before", nativeExec(ctx, t, process, "echo before >/native-storage-state; sync; cat /native-storage-state"))
				}
			}
			artifact := filepath.Join(t.TempDir(), "checkpoint")
			_, err = control.CheckpointVM(ctx, &proto.CheckpointVMRequest{VMID: id, Directory: artifact})
			require.NoError(t, err)
			var manifest nativeCheckpoint
			var immutableBacking os.FileInfo
			if storage != nil {
				data, err := os.ReadFile(filepath.Join(artifact, "manifest.json"))
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(data, &manifest))
				require.NoError(t, validateNativeRestore(&manifest))
				writable, upper := 0, false
				for _, drive := range manifest.Drives {
					if !drive.ReadOnly {
						writable++
						upper = upper || drive.ID == guestOverlayDrive
					}
				}
				require.Equal(t, 3, writable, "capture must include two workload disks and the private guest upper")
				require.True(t, upper)
				if storage.pmem {
					require.Len(t, manifest.Pmem, 1)
					require.Equal(t, request.RootDrive.ImageSha256, manifest.Pmem[0].File.SHA256)
					cache := filepath.Join(integtest.ShimBaseDir(), ".pmem-images", request.RootDrive.ImageSha256+".img")
					immutableBacking, err = os.Stat(cache)
					require.NoError(t, err)
				} else {
					require.Empty(t, manifest.Pmem)
				}
				writeGuestUpperState(ctx, t, client, id, "after")
			}
			for index, process := range tasks {
				value := nativeExec(ctx, t, process, probe)
				require.Equal(t, strings.TrimSuffix(initial[index], "41")+"42", value)
				if storage != nil {
					require.Equal(t, "after", nativeExec(ctx, t, process, "echo after >/native-storage-state; sync; cat /native-storage-state"))
				}
			}
			require.NoError(t, os.WriteFile(filepath.Join(shared, "state"), []byte("source-volume-42"), 0600))
			sourceDir, err := vm.ShimDir(integtest.ShimBaseDir(), defaultNamespace, id)
			require.NoError(t, err)
			old, err := vm.ReadRecoveryState(sourceDir)
			require.NoError(t, err)
			_, err = control.StopVM(ctx, &proto.StopVMRequest{VMID: id})
			require.NoError(t, err)
			sourceStopped = true
			_, err = os.Stat(fmt.Sprintf("/proc/%d", old.VMM.PID))
			require.True(t, os.IsNotExist(err), "the original VMM must be gone before native restore")
			if storage != nil {
				_, err = os.Stat(sourceDir.RootPath())
				require.True(t, os.IsNotExist(err), "restore must not depend on the original VM's disks or bundle directory")
			}
			require.NoError(t, os.RemoveAll(shared))
			cloneID := id + "-clone"
			response, err := control.CreateVM(ctx, &proto.CreateVMRequest{VMID: cloneID, NativeCheckpointDirectory: artifact, TimeoutSeconds: 120})
			require.NoError(t, err)
			t.Cleanup(func() { _, _ = control.StopVM(context.WithoutCancel(ctx), &proto.StopVMRequest{VMID: cloneID}) })
			cloneDir, err := vm.ShimDir(integtest.ShimBaseDir(), defaultNamespace, cloneID)
			require.NoError(t, err)
			current, err := vm.ReadRecoveryState(cloneDir)
			require.NoError(t, err)
			require.NotEqual(t, old.VMM.PID, current.VMM.PID)
			var cloneDisks map[string]string
			if storage != nil {
				cloneDisks = checkNativeRestorePrivateBackings(t, &manifest, artifact, cloneDir, current.VMM.PID, jailed)
				checkNativeRestoreGuestUpper(ctx, t, client, cloneID, storage)
				if storage.pmem {
					checkNativeRestorePmemBacking(ctx, t, current.VMM.PID, jailed, request.RootDrive.ImageSha256, immutableBacking)
				}
				if storage.reclaim {
					balloon, err := control.GetBalloonConfig(ctx, &proto.GetBalloonConfigRequest{VMID: cloneID})
					require.NoError(t, err)
					require.True(t, balloon.BalloonConfig.FreePageReporting)
					require.Zero(t, balloon.BalloonConfig.AmountMib)
				}
			}
			address, err := os.ReadFile(cloneDir.AddrFilePath())
			require.NoError(t, err)
			connection, err := ttrpcutil.NewClient(string(address))
			require.NoError(t, err)
			defer connection.Close()
			rpc, err := connection.Client()
			require.NoError(t, err)
			restored := taskAPI.NewTTRPCTaskClient(rpc)
			require.Len(t, response.RestoredIO, 2)
			for index, process := range tasks {
				state, err := restored.State(ctx, &taskAPI.StateRequest{ID: process.ID()})
				require.NoError(t, err)
				require.Equal(t, process.Pid(), state.Pid)
				value := nativeRestoredExec(ctx, t, restored, process.ID(), probe)
				require.Equal(t, strings.TrimSuffix(initial[index], "41")+"42", value, "shell memory token and counter must resume rather than restart")
				for _, streams := range response.RestoredIO {
					if streams.ID == process.ID() && streams.ExecID == "" {
						require.NotEmpty(t, streams.Stdout)
						require.Equal(t, value, nativeRestoredLog(ctx, t, streams.Stdout))
					}
				}
				require.Equal(t, "volume-41", nativeRestoredExec(ctx, t, restored, process.ID(), "cat /shared/state"))
				if storage != nil {
					require.Equal(t, "before", nativeRestoredExec(ctx, t, restored, process.ID(), "cat /native-storage-state"))
					require.Equal(t, "clone", nativeRestoredExec(ctx, t, restored, process.ID(), "echo clone >/native-storage-state; sync; cat /native-storage-state"))
					if index == len(tasks)-1 {
						require.Equal(t, "clone-volume", nativeRestoredExec(ctx, t, restored, process.ID(), "echo clone-volume >/shared/state; sync; cat /shared/state"))
					}
				}
				stats, err := restored.Stats(ctx, &taskAPI.StatsRequest{ID: process.ID()})
				require.NoError(t, err)
				require.NotNil(t, stats.Stats)
				_, err = restored.Kill(ctx, &taskAPI.KillRequest{ID: process.ID(), Signal: uint32(syscall.SIGKILL), All: true})
				require.NoError(t, err)
				_, err = restored.Wait(ctx, &taskAPI.WaitRequest{ID: process.ID()})
				require.NoError(t, err)
				_, err = restored.Delete(ctx, &taskAPI.DeleteRequest{ID: process.ID()})
				require.NoError(t, err)
			}
			if storage != nil {
				for _, drive := range manifest.Drives {
					if drive.ReadOnly {
						continue
					}
					captured := filepath.Join(artifact, drive.File.Path)
					require.Equal(t, drive.File.SHA256, hashPmemImage(t, captured), "source and clone writes must leave captured disk %s unchanged", drive.ID)
					require.NotEqual(t, drive.File.SHA256, hashPmemImage(t, cloneDisks[drive.ID]), "clone disk %s must contain its own writes", drive.ID)
				}
				checkNativeVolumes(ctx, t, artifact, "volume-41")
				if storage.pmem {
					require.Equal(t, manifest.Pmem[0].File.SHA256, hashPmemImage(t, filepath.Join(artifact, manifest.Pmem[0].File.Path)))
				}
			}
			t.Logf("source_vmm=%d restored_vmm=%d tasks=%d source_stopped=true", old.VMM.PID, current.VMM.PID, len(tasks))
		})
	}
}

func checkNativeRestorePrivateBackings(t *testing.T, manifest *nativeCheckpoint, artifact string, cloneDir vm.Dir, pid int, jailed bool) map[string]string {
	t.Helper()
	root := cloneDir.RootPath()
	if jailed {
		root = filepath.Join(root, rootfsFolder)
	}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	require.NoError(t, err)
	paths := make(map[string]string)
	var backings []os.FileInfo
	for _, drive := range manifest.Drives {
		if drive.ReadOnly {
			continue
		}
		path := filepath.Join(root, "native-restore", drive.File.Path)
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.True(t, info.Mode().IsRegular())
		require.Equal(t, drive.File.Size, info.Size())
		require.NotZero(t, info.Mode().Perm()&0200, "clone disk must remain writable")
		captured, err := os.Stat(filepath.Join(artifact, drive.File.Path))
		require.NoError(t, err)
		require.False(t, os.SameFile(captured, info), "clone must copy captured disk %s rather than hard-link it", drive.ID)
		for _, other := range backings {
			require.False(t, os.SameFile(other, info), "every cloned writable drive must have a private inode")
		}
		backings = append(backings, info)
		opened := false
		for _, fd := range fds {
			actual, err := os.Stat(filepath.Join(fmt.Sprintf("/proc/%d/fd", pid), fd.Name()))
			if err == nil && os.SameFile(actual, info) {
				opened = true
				break
			}
		}
		require.True(t, opened, "restored VMM must use the private copy for drive %s", drive.ID)
		paths[drive.ID] = path
	}
	require.Len(t, paths, 3)
	return paths
}

func checkNativeRestoreGuestUpper(ctx context.Context, t *testing.T, client *containerd.Client, id string, storage *nativeRestoreStorageCase) {
	t.Helper()
	enabled := "N"
	if storage.reclaim {
		enabled = "Y"
	}
	container, err := client.NewContainer(ctx, id+"-root-check", containerd.WithNewSpec(
		firecrackeroci.WithVMID(id), oci.WithDefaultPathEnv,
		oci.WithMounts([]specs.Mount{
			{Destination: "/rom", Type: "bind", Source: "/rom", Options: []string{"bind", "ro"}},
			{Destination: "/upper", Type: "bind", Source: "/rom/overlay", Options: []string{"bind", "ro"}},
		}),
		oci.WithProcessArgs("/bin/sh", "-ec", `
            test "$(cat /native-upper-state)" = before
            echo clone >/native-upper-state
            sync
            test "$(cat /native-upper-state)" = clone
            test "$(cat /sys/module/damon_reclaim/parameters/enabled)" = "$1"
            if test "$1" = Y; then test "$(cat /sys/module/damon_reclaim/parameters/skip_anon)" = Y; fi
            stat -f -c 'guest_upper_magic=%t' /upper
            cat /proc/mounts
            /usr/local/bin/fc-memory-probe -file /rom/usr/local/bin/agent -stat-only
            if echo forbidden >/rom/native-restore-write-test; then exit 43; fi
            echo restored-upper-and-lower=passed
        `, "--", enabled)))
	require.NoError(t, err)
	defer func() { require.NoError(t, container.Delete(context.WithoutCancel(ctx))) }()
	output := runStorageAudit(ctx, t, container, containerd.WithRootFS([]mount.Mount{
		vm.AddLocalMountIdentifier(mount.Mount{Type: "bind", Source: "/", Options: []string{"bind"}})}))
	t.Logf("Restored guest root: %s", output)
	require.Contains(t, output, "guest_upper_magic=ef53")
	require.Contains(t, output, fmt.Sprintf(`"statx_dax":%t`, storage.pmem))
	require.Contains(t, output, "restored-upper-and-lower=passed")
	if storage.ext4 {
		require.Contains(t, output, " /rom ext4 ")
	} else {
		require.Contains(t, output, " /rom squashfs ")
	}
}

func checkNativeRestorePmemBacking(ctx context.Context, t *testing.T, pid int, jailed bool, digest string, original os.FileInfo) {
	t.Helper()
	cache := filepath.Join(integtest.ShimBaseDir(), ".pmem-images", digest+".img")
	require.NoError(t, verifyImmutableImage(ctx, cache, digest))
	info, err := os.Stat(cache)
	require.NoError(t, err)
	require.True(t, os.SameFile(original, info), "restore must retain the shared immutable cache inode")
	if jailed {
		exposed, err := os.Stat(fmt.Sprintf("/proc/%d/root/%s.img", pid, digest))
		require.NoError(t, err)
		require.True(t, os.SameFile(info, exposed))
	}
	maps, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	require.NoError(t, err)
	found := false
	for _, line := range strings.Split(string(maps), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || !strings.HasSuffix(fields[5], digest+".img") {
			continue
		}
		inode, err := strconv.ParseUint(fields[4], 10, 64)
		require.NoError(t, err)
		require.Equal(t, info.Sys().(*syscall.Stat_t).Ino, inode)
		require.NotContains(t, fields[1], "w")
		require.Contains(t, fields[1], "s")
		found = true
	}
	require.True(t, found, "restored VMM must map the actual immutable pmem cache read-only/shared")
}

func nativeRestoredExec(ctx context.Context, t *testing.T, backend taskAPI.TTRPCTaskService, id, command string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	execID := fmt.Sprintf("native-exec-%d", time.Now().UnixNano())
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

// Keep log attachment exercised with the same FIFO implementation clients use.
func nativeRestoredLog(ctx context.Context, t *testing.T, path string) string {
	t.Helper()
	stream, err := fifo.OpenFifo(ctx, path, syscall.O_RDONLY|syscall.O_NONBLOCK, 0600)
	require.NoError(t, err)
	defer stream.Close()
	line, err := bufio.NewReader(stream).ReadString('\n')
	require.NoError(t, err)
	return strings.TrimSpace(line)
}
