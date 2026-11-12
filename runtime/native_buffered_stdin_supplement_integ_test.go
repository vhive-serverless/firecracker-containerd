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
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/containerd/v2/pkg/protobuf/types"
	"github.com/containerd/ttrpc"
	crimeta "github.com/firecracker-microvm/firecracker-containerd/internal/cri"
	"github.com/firecracker-microvm/firecracker-containerd/internal/integtest"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	ioproxy "github.com/firecracker-microvm/firecracker-containerd/proto/service/ioproxy/ttrpc"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"
	"github.com/firecracker-microvm/firecracker-go-sdk/vsock"
	"github.com/stretchr/testify/require"
)

const bufferedStdinGate = "/tmp/native-buffered-stdin-hold"

// The hold record is emitted only after the gate exists. Until the test removes
// that private guest file, this process cannot enter read(0). Capture therefore
// seals a known accepted-but-unconsumed stdin payload, rather than guessing
// whether traffic happened to be in a pipe at the snapshot instant.
const bufferedStdinApp = `
token=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')
counter=0
printf 'ready|%s|%s\n' "$token" "$$"
printf 'ready|%s|%s\n' "$token" "$$" >&2
: >/tmp/native-buffered-stdin-hold
printf 'held|%s|%s\n' "$token" "$$"
printf 'held|%s|%s\n' "$token" "$$" >&2
while test -f /tmp/native-buffered-stdin-hold; do sleep .01; done
while IFS= read -r record; do
 counter=$((counter+1))
 printf 'out|%s|%s|%s|%s\n' "$token" "$$" "$counter" "$record"
 printf 'err|%s|%s|%s|%s\n' "$token" "$$" "$counter" "$record" >&2
done
exit 23
`

func TestNativeBufferedStdinSnapshot_Isolated(t *testing.T) {
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
			id := fmt.Sprintf("native-buffered-stdin-%t", jailed)
			request := &proto.CreateVMRequest{VMID: id, ContainerCount: 1, MachineCfg: &proto.FirecrackerMachineConfiguration{MemSizeMib: 512}}
			if jailed {
				request.JailerConfig = &proto.JailerConfig{UID: 300001, GID: 300001}
			}
			_, err = control.CreateVM(ctx, request)
			require.NoError(t, err)
			stopped := make(map[string]bool)
			stop := func(id string) {
				cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer done()
				_, err := control.StopVM(cleanup, &proto.StopVMRequest{VMID: id, TimeoutSeconds: 20})
				require.NoError(t, err)
				stopped[id] = true
			}
			registerStop := func(id string) {
				t.Cleanup(func() {
					if !stopped[id] {
						stop(id)
					}
				})
			}
			registerStop(id)
			source := nativeForkConnect(ctx, t, id)
			image, err := alpineImage(ctx, client, defaultSnapshotterName)
			require.NoError(t, err)
			taskID := id + "-task"
			container, err := client.NewContainer(ctx, taskID, containerd.WithSnapshotter(defaultSnapshotterName), containerd.WithNewSnapshot(taskID, image),
				containerd.WithNewSpec(firecrackeroci.WithVMID(id), oci.WithDefaultPathEnv,
					oci.WithAnnotations(map[string]string{crimeta.ContainerTypeAnnotation: "container", crimeta.SandboxIDAnnotation: id}), oci.WithProcessArgs("/bin/sh", "-ec", bufferedStdinApp)))
			require.NoError(t, err)
			t.Cleanup(func() { _ = container.Delete(context.WithoutCancel(ctx), containerd.WithSnapshotCleanup) })
			fifos, err := cio.NewFIFOSetInDir(t.TempDir(), taskID, false)
			require.NoError(t, err)
			original := nativeForkOpen(ctx, t, taskID, "", fifos)
			process, err := container.NewTask(ctx, func(string) (cio.IO, error) { return original.io, nil })
			require.NoError(t, err)
			require.NoError(t, process.Start(ctx))
			nativeForkReady(ctx, t, original, process.Pid())
			source.streams = append(source.streams, original)
			held := fmt.Sprintf("held|%s|%d", original.token, original.namespacePID)
			for _, lines := range []<-chan nativeForkLine{original.stdout, original.stderr} {
				line, ok := nativeForkRead(ctx, t, lines)
				require.True(t, ok)
				require.Equal(t, held, line)
			}
			require.Equal(t, "held", nativeRestoredExec(ctx, t, source.backend, taskID, "test -f "+bufferedStdinGate+" && echo held"))
			const sentinel = "accepted-before-cut-held-guest-reader-6d6cedf3"
			written, err := io.WriteString(original.io.Stdin, sentinel+"\n")
			require.NoError(t, err)
			require.Equal(t, len(sentinel)+1, written)
			info, err := control.GetVMInfo(ctx, &proto.GetVMInfoRequest{VMID: id})
			require.NoError(t, err)
			bufferedStdinAccepted(ctx, t, info.VSockPath, taskID, uint64(len(sentinel)+1))
			artifact := filepath.Join(t.TempDir(), "buffered-input")
			_, err = control.CheckpointVM(ctx, &proto.CheckpointVMRequest{VMID: id, Directory: artifact})
			require.NoError(t, err)
			data, err := os.ReadFile(filepath.Join(artifact, "manifest.json"))
			require.NoError(t, err)
			var manifest nativeCheckpoint
			require.NoError(t, json.Unmarshal(data, &manifest))
			require.NotNil(t, manifest.Transport)
			require.Len(t, manifest.Transport.Processes, 1)
			captured := manifest.Transport.Processes[0]
			require.Equal(t, taskID, captured.TaskID)
			require.Empty(t, captured.ExecID)
			require.Len(t, captured.Streams, 3)
			ready := fmt.Sprintf("ready|%s|%d\n", original.token, original.namespacePID)
			seen := make(map[string]bool)
			for _, stream := range captured.Streams {
				seen[stream.Name] = true
				require.False(t, stream.Cursor.FIN)
				if stream.Name == "stdin" {
					require.Equal(t, uint64(len(sentinel)+1), stream.Cursor.Offset, "sentinel must be ACKed into the guest FIFO at the captured cut")
				} else {
					require.Equal(t, uint64(len(ready)+len(held)+1), stream.Cursor.Offset, "the ready/hold prefix must be fully acknowledged before the cut")
				}
			}
			require.Equal(t, map[string]bool{"stdin": true, "stdout": true, "stderr": true}, seen)
			// The accepted offset is not application consumption: its gate remains
			// present, and no record can have been consumed before this explicit release.
			require.Equal(t, "released", nativeRestoredExec(ctx, t, source.backend, taskID, "test -f "+bufferedStdinGate+" && rm "+bufferedStdinGate+" && echo released"))
			bufferedStdinExpect(ctx, t, original, sentinel)
			nativeForkExchange(ctx, t, original, "source-post-cut", 2)
			nativeForkIdentity(t, source)

			cloneID := id + "-clone"
			response, err := control.CreateVM(ctx, &proto.CreateVMRequest{VMID: cloneID, NativeCheckpointDirectory: artifact, TimeoutSeconds: 120})
			require.NoError(t, err)
			registerStop(cloneID)
			clone := nativeForkConnect(ctx, t, cloneID)
			require.NotEqual(t, source.vmm, clone.vmm)
			require.Len(t, response.RestoredIO, 1)
			restored := response.RestoredIO[0]
			require.Equal(t, taskID, restored.ID)
			require.Empty(t, restored.ExecID)
			config := cio.Config{Stdin: restored.Stdin, Stdout: restored.Stdout, Stderr: restored.Stderr, Terminal: restored.Terminal}
			require.False(t, config.Terminal)
			for _, pair := range [][2]string{{fifos.Stdin, config.Stdin}, {fifos.Stdout, config.Stdout}, {fifos.Stderr, config.Stderr}} {
				require.NotEmpty(t, pair[1])
				require.NotEqual(t, pair[0], pair[1])
				info, err := os.Stat(pair[1])
				require.NoError(t, err)
				require.NotZero(t, info.Mode()&os.ModeNamedPipe)
			}
			cloneStream := nativeForkOpen(ctx, t, taskID, "", cio.NewFIFOSet(config, nil))
			cloneStream.token, cloneStream.pid, cloneStream.namespacePID = original.token, original.pid, original.namespacePID
			// The clone was captured at counter0, before the source consumed its sentinel.
			require.Zero(t, cloneStream.count)
			clone.streams = append(clone.streams, cloneStream)
			state, err := clone.backend.State(ctx, &taskAPI.StateRequest{ID: taskID})
			require.NoError(t, err)
			require.Equal(t, original.pid, state.Pid)
			require.Equal(t, "held", nativeRestoredExec(ctx, t, clone.backend, taskID, "test -f "+bufferedStdinGate+" && echo held"), "source release must not release the captured clone's gate")
			require.Equal(t, "released", nativeRestoredExec(ctx, t, clone.backend, taskID, "test -f "+bufferedStdinGate+" && rm "+bufferedStdinGate+" && echo released"))
			bufferedStdinExpect(ctx, t, cloneStream, sentinel)
			nativeForkExchange(ctx, t, cloneStream, "clone-post-cut", 2)
			nativeForkFinish(ctx, t, clone)
			stop(cloneID)
			nativeForkIdentity(t, source)
			nativeForkExchange(ctx, t, original, "source-survives-clone", 2)
			nativeForkFinish(ctx, t, source)
			stop(id)
			t.Logf("buffered_stdin_snapshot sentinel_bytes=%d captured_stdin_offset=%d source_once=true clone_once=true captured_counter=0 stdout_stderr_exact=true eof_no_tail=true", len(sentinel)+1, len(sentinel)+1)
		})
	}
}

func bufferedStdinExpect(ctx context.Context, t *testing.T, stream *nativeForkStream, record string) {
	t.Helper()
	for _, output := range []struct {
		name  string
		lines <-chan nativeForkLine
	}{{"out", stream.stdout}, {"err", stream.stderr}} {
		line, ok := nativeForkRead(ctx, t, output.lines)
		require.True(t, ok)
		require.Equal(t, fmt.Sprintf("%s|%s|%d|%d|%s", output.name, stream.token, stream.namespacePID, stream.count+1, record), line, "captured stdin must be consumed exactly once without prefix replay or counter restart")
	}
	stream.count++
}

// Observe destination acceptance through the existing guest transport endpoint.
// Prepare only gates guest output senders; stdin remains a live receiver. This
// is not a claimed global cut: Resume releases those temporary gates before the
// ordinary CheckpointVM coordinator performs its own full preparation/capture.
func bufferedStdinAccepted(ctx context.Context, t *testing.T, path, taskID string, expected uint64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := vsock.DialContext(ctx, path, defaultVsockPort)
	require.NoError(t, err)
	client := ttrpc.NewClient(conn)
	defer client.Close()
	peer := ioproxy.NewIOProxyClient(client)
	// Even a failed Prepare may have gated a subset of guest senders.
	prepared := true
	defer func() {
		if prepared {
			cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer done()
			_, err := peer.Resume(cleanup, &types.Empty{})
			require.NoError(t, err, "release pre-capture acceptance-observation gates")
		}
	}()
	_, err = peer.Prepare(ctx, &types.Empty{})
	require.NoError(t, err)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := peer.Describe(ctx, &types.Empty{})
		require.NoError(t, err)
		matches := 0
		accepted := uint64(0)
		for _, process := range response.Processes {
			if process.TaskID != taskID || process.ExecID != "" {
				continue
			}
			for _, stream := range process.Streams {
				if stream.Name != "stdin" {
					continue
				}
				matches++
				require.False(t, stream.Sender)
				require.NotNil(t, stream.Cursor)
				require.False(t, stream.Cursor.FIN)
				accepted = stream.Cursor.Offset
			}
		}
		require.Equal(t, 1, matches, "one captured input receiver must witness acceptance")
		// This is one sub-PIPE_BUF write into an empty held reader's FIFO.
		// Either the complete record has arrived, or none of it has arrived yet.
		require.True(t, accepted == 0 || accepted == expected, "unexpected accepted stdin offset: %d", accepted)
		if accepted == expected {
			t.Logf("buffered_stdin_precapture guest_receiver_accepted=%d application_held=true", accepted)
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("sentinel never reached the held guest FIFO: ", ctx.Err())
		}
	}
	_, err = peer.Resume(ctx, &types.Empty{})
	require.NoError(t, err)
	prepared = false
}
