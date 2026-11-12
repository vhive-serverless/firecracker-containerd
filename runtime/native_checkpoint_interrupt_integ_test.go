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
	crimeta "github.com/firecracker-microvm/firecracker-containerd/internal/cri"
	"github.com/firecracker-microvm/firecracker-containerd/internal/integtest"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
)

// TestNativeCheckpointInterrupt_Isolated disconnects after the staged memory
// image proves native capture passed VM pause and volume freeze.
func TestNativeCheckpointInterrupt_Isolated(t *testing.T) {
	integtest.Prepare(t)
	for _, jailed := range []bool{false, true} {
		t.Run(fmt.Sprintf("jailed=%v", jailed), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), defaultNamespace), 300*time.Second)
			t.Cleanup(cancel)
			client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(firecrackerRuntime))
			require.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })
			control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
			require.NoError(t, err)
			t.Cleanup(func() { _ = control.Close() })

			id := fmt.Sprintf("native-interrupt-%v", jailed)
			request := &proto.CreateVMRequest{VMID: id, ContainerCount: 1,
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
			shared := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(shared, "state"), []byte("before"), 0600))
			const app = `counter=40; token=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n'); while :; do if test -f /request; then rm /request; counter=$((counter+1)); echo "$token:$counter" >/response.next; mv /response.next /response; fi; sleep .02; done`
			const probe = "rm -f /response; touch /request; while ! test -f /response; do sleep .01; done; cat /response"
			container, err := client.NewContainer(ctx, id+"-task",
				containerd.WithSnapshotter(defaultSnapshotterName),
				containerd.WithNewSnapshot(id+"-task", image),
				containerd.WithNewSpec(firecrackeroci.WithVMID(id), oci.WithDefaultPathEnv,
					oci.WithAnnotations(map[string]string{crimeta.ContainerTypeAnnotation: "container", crimeta.SandboxIDAnnotation: id}),
					oci.WithMounts([]specs.Mount{{Type: "bind", Source: shared, Destination: "/shared", Options: []string{"rbind", "rw"}}}),
					oci.WithProcessArgs("/bin/sh", "-ec", app)))
			require.NoError(t, err)
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
				defer stop()
				require.NoError(t, container.Delete(cleanup, containerd.WithSnapshotCleanup))
			})
			process, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, io.Discard, io.Discard)))
			require.NoError(t, err)
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
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
			initial := nativeExec(ctx, t, process, probe)
			require.True(t, strings.HasSuffix(initial, ":41"), initial)
			require.Equal(t, "before", nativeExec(ctx, t, process, "cat /shared/state"))
			sourceDir, err := vm.ShimDir(integtest.ShimBaseDir(), defaultNamespace, id)
			require.NoError(t, err)
			before, err := vm.ReadRecoveryState(sourceDir)
			require.NoError(t, err)

			interruptClient, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
			require.NoError(t, err)
			t.Cleanup(func() { _ = interruptClient.Close() })
			artifact := filepath.Join(t.TempDir(), "interrupted")
			callDone := make(chan error, 1)
			go func() {
				_, err := interruptClient.CheckpointVM(ctx, &proto.CheckpointVMRequest{VMID: id, Directory: artifact})
				callDone <- err
			}()

			var finishedEarly error
			finishedBeforeSnapshot := false
			require.Eventually(t, func() bool {
				select {
				case finishedEarly = <-callDone:
					finishedBeforeSnapshot = true
					return true
				default:
				}
				return checkpointStageHasSnapshot(artifact)
			}, 120*time.Second, time.Millisecond, "capture must publish a nonempty staged memory snapshot after quiescing")
			if finishedBeforeSnapshot || !checkpointStageHasSnapshot(artifact) {
				t.Fatalf("capture completed before staged memory snapshot was observed (early error=%v, destination exists=%v); refusing retry-to-green", finishedEarly, !checkpointPathAbsent(artifact))
			}

			// Closing the caller's ttrpc connection cancels the server request; a
			// context cancellation alone only stops the local unary wait.
			require.NoError(t, interruptClient.Close())
			select {
			case err := <-callDone:
				require.Error(t, err, "disconnected capture must not report success")
			case <-ctx.Done():
				t.Fatal("timed out waiting for disconnected capture to return")
			}

			require.Eventually(t, func() bool {
				state, err := process.Status(ctx)
				return err == nil && state.Status == containerd.Running && checkpointPathAbsent(artifact)
			}, 60*time.Second, 100*time.Millisecond, "interrupted capture must thaw the task and remove staged artifacts")
			require.Equal(t, strings.TrimSuffix(initial, "41")+"42", nativeExec(ctx, t, process, probe), "interrupted capture must retain shell memory")
			after, err := vm.ReadRecoveryState(sourceDir)
			require.NoError(t, err)
			require.Equal(t, before.VMM, after.VMM, "source VMM must not restart")
			require.Equal(t, "after", nativeExec(ctx, t, process, "echo after >/shared/state; sync; cat /shared/state"), "interruption must thaw shared-volume writes")
			contents, err := os.ReadFile(filepath.Join(shared, "state"))
			require.NoError(t, err)
			require.Equal(t, "after\n", string(contents))

			subsequent := filepath.Join(t.TempDir(), "successful")
			_, err = control.CheckpointVM(ctx, &proto.CheckpointVMRequest{VMID: id, Directory: subsequent})
			require.NoError(t, err, "a later capture must succeed after interruption cleanup")
			require.FileExists(t, filepath.Join(subsequent, "manifest.json"))
			require.Equal(t, strings.TrimSuffix(initial, "41")+"43", nativeExec(ctx, t, process, probe), "source must also resume after the next successful capture")
			cloneID := id + "-clone"
			_, err = control.CreateVM(ctx, &proto.CreateVMRequest{VMID: cloneID, NativeCheckpointDirectory: subsequent, TimeoutSeconds: 120})
			require.NoError(t, err, "capture following interruption must be restorable")
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
				defer stop()
				_, err := control.StopVM(cleanup, &proto.StopVMRequest{VMID: cloneID})
				require.NoError(t, err)
			})
			cloneDir, err := vm.ShimDir(integtest.ShimBaseDir(), defaultNamespace, cloneID)
			require.NoError(t, err)
			address, err := os.ReadFile(cloneDir.AddrFilePath())
			require.NoError(t, err)
			connection, err := ttrpcutil.NewClient(string(address))
			require.NoError(t, err)
			defer connection.Close()
			rpc, err := connection.Client()
			require.NoError(t, err)
			restored := taskAPI.NewTTRPCTaskClient(rpc)
			require.Equal(t, strings.TrimSuffix(initial, "41")+"43", nativeRestoredExec(ctx, t, restored, process.ID(), probe))
			require.Equal(t, "after", nativeRestoredExec(ctx, t, restored, process.ID(), "cat /shared/state"))
			require.NoError(t, os.RemoveAll(subsequent))
		})
	}
}

func checkpointPathAbsent(path string) bool {
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return false
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if len(entry.Name()) >= len(".fc-checkpoint-") && entry.Name()[:len(".fc-checkpoint-")] == ".fc-checkpoint-" {
			return false
		}
	}
	return true
}

func checkpointStageHasSnapshot(path string) bool {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) < len(".fc-checkpoint-") || entry.Name()[:len(".fc-checkpoint-")] != ".fc-checkpoint-" {
			continue
		}
		info, err := os.Stat(filepath.Join(filepath.Dir(path), entry.Name(), "memory"))
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return true
		}
	}
	return false
}
