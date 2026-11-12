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
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	crimeta "github.com/firecracker-microvm/firecracker-containerd/internal/cri"
	"github.com/firecracker-microvm/firecracker-containerd/internal/integtest"
	"github.com/firecracker-microvm/firecracker-containerd/internal/volfs"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNativeCheckpoint_Isolated(t *testing.T) {
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
			id := fmt.Sprintf("native-capture-%v", jailed)
			request := &proto.CreateVMRequest{VMID: id, ContainerCount: 2,
				MachineCfg: &proto.FirecrackerMachineConfiguration{MemSizeMib: 512}}
			if jailed {
				request.JailerConfig = &proto.JailerConfig{UID: 300001, GID: 300001}
			}
			_, err = control.CreateVM(ctx, request)
			require.NoError(t, err)
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
				defer stop()
				_, err := control.StopVM(cleanup, &proto.StopVMRequest{VMID: id})
				require.NoError(t, err)
			})
			image, err := alpineImage(ctx, client, defaultSnapshotterName)
			require.NoError(t, err)
			tasks := make([]containerd.Task, 0, 2)
			shared := t.TempDir()
			for index := range 2 {
				name := fmt.Sprintf("%s-%d", id, index)
				container, err := client.NewContainer(ctx, name,
					containerd.WithSnapshotter(defaultSnapshotterName),
					containerd.WithNewSnapshot(name, image),
					containerd.WithNewSpec(firecrackeroci.WithVMID(id), oci.WithDefaultPathEnv,
						oci.WithAnnotations(map[string]string{crimeta.ContainerTypeAnnotation: "container", crimeta.SandboxIDAnnotation: id}),
						oci.WithMounts([]specs.Mount{{Type: "bind", Source: shared, Destination: "/shared", Options: []string{"rbind", "rw"}}}),
						oci.WithProcessArgs("/bin/sh", "-ec", "echo before >/state; echo shared-before >/shared/state; exec sleep 200")))
				require.NoError(t, err)
				t.Cleanup(func() {
					require.NoError(t, container.Delete(context.WithoutCancel(ctx), containerd.WithSnapshotCleanup))
				})
				process, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, io.Discard, io.Discard)))
				require.NoError(t, err)
				t.Cleanup(func() {
					cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
					defer stop()
					wait, err := process.Wait(cleanup)
					require.NoError(t, err)
					require.NoError(t, process.Kill(cleanup, syscall.SIGKILL, containerd.WithKillAll))
					select {
					case <-wait:
					case <-cleanup.Done():
						t.Error(cleanup.Err())
					}
					_, err = process.Delete(cleanup)
					require.NoError(t, err)
				})
				require.NoError(t, process.Start(ctx))
				require.Equal(t, "before", nativeExec(ctx, t, process, "while ! test -e /state; do sleep .01; done; sync; cat /state"))
				tasks = append(tasks, process)
			}
			artifact := filepath.Join(t.TempDir(), "checkpoint")
			resumeCount := 2
			if jailed {
				require.NoError(t, tasks[1].Pause(ctx))
				resumeCount = 1
			}
			_, err = control.CheckpointVM(ctx, &proto.CheckpointVMRequest{VMID: id, Directory: artifact})
			require.NoError(t, err)
			data, err := os.ReadFile(filepath.Join(artifact, "manifest.json"))
			require.NoError(t, err)
			var manifest nativeCheckpoint
			require.NoError(t, json.Unmarshal(data, &manifest))
			require.Equal(t, id, manifest.VMID)
			require.Len(t, manifest.Processes, 2)
			require.Len(t, manifest.Tasks, 2)
			require.Len(t, manifest.ContainerSlots, 2)
			require.Len(t, manifest.ResumeTasks, resumeCount)
			require.Len(t, manifest.Drives, 3)
			require.Positive(t, manifest.VsockPortCount, "restore must retain the allocator offset to allocate beyond captured listeners")
			if jailed {
				state, err := tasks[1].Status(ctx)
				require.NoError(t, err)
				require.Equal(t, containerd.Paused, state.Status, "capture must preserve an already-paused container")
				require.NoError(t, tasks[1].Resume(ctx))
			}
			for _, process := range tasks {
				require.Equal(t, "after", nativeExec(ctx, t, process, "echo after >/state; echo shared-after >/shared/state; sync; cat /state"), "source must retain exec, I/O and independent writes after capture")
			}
			for _, task := range manifest.Tasks {
				require.Len(t, task.VolumeTokens, 1)
			}
			checkNativeVolumes(ctx, t, artifact, "shared-before\n")
			files := make([]snapshotFile, 0, 2+len(manifest.Drives))
			files = append(files, manifest.Memory, manifest.State)
			for _, drive := range manifest.Drives {
				files = append(files, drive.File)
			}
			for _, saved := range files {
				input, err := os.Open(filepath.Join(artifact, saved.Path))
				require.NoError(t, err)
				hash := sha256.New()
				size, err := io.Copy(hash, input)
				require.NoError(t, err)
				require.NoError(t, input.Close())
				require.Equal(t, saved.Size, size)
				require.Equal(t, saved.SHA256, hex.EncodeToString(hash.Sum(nil)), "source writes must not advance checkpoint files")
			}
			for _, drive := range manifest.Drives {
				if drive.ReadOnly {
					continue
				}
				output, err := exec.CommandContext(ctx, "debugfs", "-R", "cat /state", filepath.Join(artifact, drive.File.Path)).Output()
				require.NoError(t, err)
				require.Equal(t, "before\n", string(output), "captured ext4 data must remain at the checkpoint even after source sync")
			}
			repeated := filepath.Join(filepath.Dir(artifact), "repeated")
			_, err = control.CheckpointVM(ctx, &proto.CheckpointVMRequest{VMID: id, Directory: repeated})
			require.NoError(t, err)
			checkNativeVolumes(ctx, t, repeated, "shared-after\n")
			for _, drive := range manifest.Drives {
				if drive.ReadOnly {
					continue
				}
				output, err := exec.CommandContext(ctx, "debugfs", "-R", "cat /state", filepath.Join(repeated, drive.File.Path)).Output()
				require.NoError(t, err)
				require.Equal(t, "after\n", string(output), "a fresh capture must reflect the advanced source")
			}
			_, err = control.CheckpointVM(ctx, &proto.CheckpointVMRequest{VMID: id, Directory: artifact})
			require.Equal(t, codes.AlreadyExists, status.Code(err))
			require.NotEmpty(t, manifest.Tasks[0].BundlePath)
			_, err = control.CheckpointVM(ctx, &proto.CheckpointVMRequest{VMID: id, Directory: filepath.Join(manifest.Tasks[0].BundlePath, "invalid-checkpoint")})
			require.Equal(t, codes.InvalidArgument, status.Code(err), "a checkpoint must survive source task bundle cleanup")
			cancelled, stop := context.WithCancel(ctx)
			stop()
			_, err = control.CheckpointVM(cancelled, &proto.CheckpointVMRequest{VMID: id, Directory: filepath.Join(filepath.Dir(artifact), "cancelled")})
			require.Error(t, err)
			for _, process := range tasks {
				require.Equal(t, "after", nativeExec(ctx, t, process, "cat /state"))
			}
		})
	}
}

func checkNativeVolumes(ctx context.Context, t *testing.T, artifact, expected string) {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "restored")
	server, err := volfs.RestoreServer(ctx, filepath.Join(artifact, "volumes"), workspace, volfs.CheckpointLimits{})
	require.NoError(t, err)
	defer server.Close()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	defer root.Close()
	found := 0
	require.NoError(t, filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "state" && entry.Type().IsRegular() {
			relative, err := filepath.Rel(workspace, path)
			if err != nil {
				return err
			}
			data, err := root.ReadFile(relative)
			if err != nil {
				return err
			}
			require.Equal(t, expected, string(data))
			found++
		}
		return nil
	}))
	require.Positive(t, found, "the captured shared volume must be reconstructed independently of its advanced source")
}

func nativeExec(ctx context.Context, t *testing.T, task containerd.Task, command string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	process, err := task.Exec(ctx, fmt.Sprintf("capture-probe-%d", time.Now().UnixNano()), &specs.Process{
		Args: []string{"/bin/sh", "-ec", command}, Cwd: "/",
	}, cio.NewCreator(cio.WithStreams(nil, &stdout, &stderr)))
	require.NoError(t, err)
	wait, err := process.Wait(ctx)
	require.NoError(t, err)
	require.NoError(t, process.Start(ctx))
	select {
	case result := <-wait:
		require.NoError(t, result.Error())
		require.Zero(t, result.ExitCode(), stderr.String())
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err = process.Delete(ctx)
	require.NoError(t, err)
	return string(bytes.TrimSpace(stdout.Bytes()))
}
