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
	"fmt"
	"io"
	"os"
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
			artifact := filepath.Join(t.TempDir(), "checkpoint")
			_, err = control.CheckpointVM(ctx, &proto.CheckpointVMRequest{VMID: id, Directory: artifact})
			require.NoError(t, err)
			for index, process := range tasks {
				value := nativeExec(ctx, t, process, probe)
				require.Equal(t, strings.TrimSuffix(initial[index], "41")+"42", value)
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
			t.Logf("source_vmm=%d restored_vmm=%d tasks=%d source_stopped=true", old.VMM.PID, current.VMM.PID, len(tasks))
		})
	}
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
