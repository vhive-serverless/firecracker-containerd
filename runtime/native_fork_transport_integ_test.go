// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	apievents "github.com/containerd/containerd/api/events"
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
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
)

// Keep the token and counter in shell memory: restarting the command cannot
// reconstruct either. Every input produces a numbered record on BOTH outputs.
const nativeForkApp = `
token=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')
counter=0
printf 'ready|%s|%s\n' "$token" "$$"
printf 'ready|%s|%s\n' "$token" "$$" >&2
while IFS= read -r record; do
    counter=$((counter+1))
    printf 'out|%s|%s|%s|%s\n' "$token" "$$" "$counter" "$record"
    printf 'err|%s|%s|%s|%s\n' "$token" "$$" "$counter" "$record" >&2
done
exit 23
`

type nativeForkLine struct {
	text string
	err  error
}

type nativeForkStream struct {
	taskID, execID, token string
	pid                   uint32
	count                 int
	io                    *cio.DirectIO
	stdout, stderr        <-chan nativeForkLine
}

type nativeForkInstance struct {
	id      string
	dir     vm.Dir
	vmm     vm.ProcessIdentity
	backend taskAPI.TTRPCTaskService
	streams []*nativeForkStream
}

// This is direct runtime acceptance, without containerd/CRI ownership adoption.
// All three VMs coexist; original host FIFO descriptors stay open at each cut.
func TestNativeForkTransport_Isolated(t *testing.T) {
	integtest.Prepare(t)
	for _, jailed := range []bool{false, true} {
		t.Run(fmt.Sprintf("jailed=%v", jailed), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), defaultNamespace), 600*time.Second)
			t.Cleanup(cancel)
			client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(firecrackerRuntime))
			require.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })
			control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
			require.NoError(t, err)
			t.Cleanup(func() { _ = control.Close() })
			id := fmt.Sprintf("native-fork-%v", jailed)
			request := &proto.CreateVMRequest{VMID: id, ContainerCount: 2,
				MachineCfg: &proto.FirecrackerMachineConfiguration{MemSizeMib: 512},
				GuestRoot:  &proto.GuestRootStorage{Mode: "disk", SizeMib: 128}}
			if jailed {
				request.JailerConfig = &proto.JailerConfig{UID: 300001, GID: 300001}
			}
			_, err = control.CreateVM(ctx, request)
			require.NoError(t, err)
			stopped := make(map[string]bool)
			stop := func(vmID string) {
				cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer done()
				_, err := control.StopVM(cleanup, &proto.StopVMRequest{VMID: vmID})
				require.NoError(t, err)
				stopped[vmID] = true
			}
			registerStop := func(vmID string) {
				t.Cleanup(func() {
					if !stopped[vmID] {
						stop(vmID)
					}
				})
			}
			registerStop(id)
			source := nativeForkConnect(ctx, t, id)
			image, err := alpineImage(ctx, client, defaultSnapshotterName)
			require.NoError(t, err)
			shared := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(shared, "state"), []byte("captured"), 0600))
			for index := range 2 {
				taskID := fmt.Sprintf("%s-%d", id, index)
				container, err := client.NewContainer(ctx, taskID,
					containerd.WithSnapshotter(defaultSnapshotterName), containerd.WithNewSnapshot(taskID, image),
					containerd.WithNewSpec(firecrackeroci.WithVMID(id), oci.WithDefaultPathEnv,
						oci.WithAnnotations(map[string]string{crimeta.ContainerTypeAnnotation: "container", crimeta.SandboxIDAnnotation: id}),
						oci.WithMounts([]specs.Mount{{Type: "bind", Source: shared, Destination: "/shared", Options: []string{"rbind", "rw"}}}),
						oci.WithProcessArgs("/bin/sh", "-ec", nativeForkApp)))
				require.NoError(t, err)
				t.Cleanup(func() { _ = container.Delete(context.WithoutCancel(ctx), containerd.WithSnapshotCleanup) })
				fifos, err := cio.NewFIFOSetInDir(t.TempDir(), taskID, false)
				require.NoError(t, err)
				stream := nativeForkOpen(ctx, t, taskID, "", fifos)
				process, err := container.NewTask(ctx, func(string) (cio.IO, error) { return stream.io, nil })
				require.NoError(t, err)
				require.NoError(t, process.Start(ctx))
				nativeForkReady(ctx, t, stream, process.Pid())
				source.streams = append(source.streams, stream)
				require.Equal(t, "captured", nativeExec(ctx, t, process, "echo captured >/fork-state; sync; cat /fork-state"))
				if index == 0 {
					const execID = "preexisting-stream"
					fifos, err := cio.NewFIFOSetInDir(t.TempDir(), execID, false)
					require.NoError(t, err)
					execStream := nativeForkOpen(ctx, t, taskID, execID, fifos)
					exec, err := process.Exec(ctx, execID, &specs.Process{Args: []string{"/bin/sh", "-ec", nativeForkApp}, Cwd: "/"},
						func(string) (cio.IO, error) { return execStream.io, nil })
					require.NoError(t, err)
					require.NoError(t, exec.Start(ctx))
					nativeForkReady(ctx, t, execStream, exec.Pid())
					source.streams = append(source.streams, execStream)
				}
			}

			// Subscribe before capture and retain counts through both restores.
			// Unique exec IDs distinguish VM events even though captured task IDs match.
			eventIDs := []string{id + "-event-before", id + "-event-source", id + "-event-a", id + "-event-b"}
			taskIDs := make([]string, 0, 2)
			for _, stream := range source.streams {
				if stream.execID == "" {
					taskIDs = append(taskIDs, stream.taskID)
				}
			}
			events := nativeForkSubscribe(ctx, t, client, source.streams[0].taskID, eventIDs, taskIDs)
			nativeForkEventProbe(ctx, t, source.backend, source.streams[0].taskID, eventIDs[0])
			events.check(t, eventIDs[:1])
			for _, stream := range source.streams {
				nativeForkExchange(ctx, t, stream, "prefix", 16)
			}

			// Exercise a real cut while numbered stdin/output traffic is running.
			// Bound outstanding stdin to one sub-PIPE_BUF record: preparation
			// freezes the consumer, so a full guest pipe cannot legitimately drain.
			// Drain it completely before taking the fork artifact, giving clones an
			// unambiguous acknowledged prefix rather than guessing an in-flight cut.
			activeDone := make(chan error, 1)
			activeArtifact := filepath.Join(t.TempDir(), "active")
			go func() {
				_, err := control.CheckpointVM(ctx, &proto.CheckpointVMRequest{VMID: id, Directory: activeArtifact})
				activeDone <- err
			}()
			for batch := 0; ; batch++ {
				nativeForkExchange(ctx, t, source.streams[0], fmt.Sprintf("during-%d", batch), 1)
				select {
				case err := <-activeDone:
					require.NoError(t, err)
					goto activeComplete
				default:
				}
			}
		activeComplete:
			artifact := filepath.Join(t.TempDir(), "fork")
			_, err = control.CheckpointVM(ctx, &proto.CheckpointVMRequest{VMID: id, Directory: artifact})
			require.NoError(t, err)
			data, err := os.ReadFile(filepath.Join(artifact, "manifest.json"))
			require.NoError(t, err)
			var manifest nativeCheckpoint
			require.NoError(t, json.Unmarshal(data, &manifest))
			require.Len(t, manifest.Processes, len(source.streams), "capture must include the pre-existing exec")
			nativeForkIdentity(t, source)

			instances := []*nativeForkInstance{source}
			var backingSets []map[string]string
			for _, suffix := range []string{"a", "b"} {
				cloneID := id + "-" + suffix
				response, err := control.CreateVM(ctx, &proto.CreateVMRequest{VMID: cloneID, NativeCheckpointDirectory: artifact, TimeoutSeconds: 120})
				require.NoError(t, err)
				registerStop(cloneID)
				clone := nativeForkConnect(ctx, t, cloneID)
				require.NotEqual(t, source.vmm, clone.vmm)
				require.Len(t, response.RestoredIO, len(source.streams))
				for _, original := range source.streams {
					var captured *proto.RestoredProcessIO
					for _, restored := range response.RestoredIO {
						if restored.ID == original.taskID && restored.ExecID == original.execID {
							require.Nil(t, captured, "restored process must appear exactly once")
							captured = restored
						}
					}
					require.NotNil(t, captured, "missing restored IO for %s/%s", original.taskID, original.execID)
					config := cio.Config{Stdin: captured.Stdin, Stdout: captured.Stdout, Stderr: captured.Stderr}
					for _, path := range []string{config.Stdin, config.Stdout, config.Stderr} {
						require.NotEmpty(t, path)
						info, err := os.Stat(path)
						require.NoError(t, err)
						require.NotZero(t, info.Mode()&os.ModeNamedPipe)
					}
					stream := nativeForkOpen(ctx, t, original.taskID, original.execID, cio.NewFIFOSet(config, nil))
					stream.token, stream.pid, stream.count = original.token, original.pid, original.count
					state, err := clone.backend.State(ctx, &taskAPI.StateRequest{ID: stream.taskID, ExecID: stream.execID})
					require.NoError(t, err)
					require.Equal(t, original.pid, state.Pid)
					clone.streams = append(clone.streams, stream)
				}
				backingSets = append(backingSets, checkNativeRestorePrivateBackings(t, &manifest, artifact, clone.dir, clone.vmm.PID, jailed))
				instances = append(instances, clone)
			}
			for driveID, path := range backingSets[0] {
				left, err := os.Stat(path)
				require.NoError(t, err)
				right, err := os.Stat(backingSets[1][driveID])
				require.NoError(t, err)
				require.False(t, os.SameFile(left, right), "clones must have different writable backing inodes for %s", driveID)
			}
			mountNamespaces := make(map[string]string)
			for _, instance := range instances {
				mountNS, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/mnt", instance.vmm.PID))
				require.NoError(t, err)
				require.Empty(t, mountNamespaces[mountNS], "VM %s shares a mount namespace with %s", instance.id, mountNamespaces[mountNS])
				mountNamespaces[mountNS] = instance.id
				for _, stream := range instance.streams {
					nativeForkExchange(ctx, t, stream, instance.id, 128)
				}
				for _, stream := range instance.streams {
					if stream.execID == "" {
						require.Equal(t, "captured\ncaptured", nativeRestoredExec(ctx, t, instance.backend, stream.taskID, "cat /fork-state; cat /shared/state"))
					}
				}
				for _, stream := range instance.streams {
					if stream.execID == "" {
						nativeRestoredExec(ctx, t, instance.backend, stream.taskID, fmt.Sprintf("echo %s >/fork-state; echo %s >/shared/state; sync", instance.id, instance.id))
					}
				}
			}
			for index, instance := range instances {
				nativeForkEventProbe(ctx, t, instance.backend, source.streams[0].taskID, eventIDs[index+1])
				for _, stream := range instance.streams {
					if stream.execID == "" {
						require.Equal(t, instance.id+"\n"+instance.id, nativeRestoredExec(ctx, t, instance.backend, stream.taskID, "cat /fork-state; cat /shared/state"), "disk and volume writes must remain private to %s", instance.id)
					}
				}
				nativeForkIdentity(t, instance)
			}
			events.check(t, eventIDs)
			for _, drive := range manifest.Drives {
				require.Equal(t, drive.File.SHA256, hashPmemImage(t, filepath.Join(artifact, drive.File.Path)), "live VM writes must leave the artifact unchanged")
			}
			checkNativeVolumes(ctx, t, artifact, "captured")

			// EOF is part of the stream contract. Drain through real FIFO EOF before
			// Delete; this also catches trailing duplicates after the last expected line.
			// Init IDs are shared across clones. Sequential teardown provides phase
			// correlation: one exit per task after A, then source, then B. This does
			// not establish VM attribution for simultaneous exits with the same ID.
			events.checkInitExits(t, 0)
			events.beginInitTeardown(t, 1)
			nativeForkFinish(ctx, t, instances[1])
			events.checkInitExits(t, 1)
			stop(instances[1].id)
			_, err = os.Stat(instances[1].dir.RootPath())
			require.True(t, os.IsNotExist(err))
			for index, survivor := range []*nativeForkInstance{source, instances[2]} {
				nativeForkIdentity(t, survivor)
				for _, stream := range survivor.streams {
					nativeForkExchange(ctx, t, stream, "survived-"+survivor.id, 32)
				}
				require.Equal(t, survivor.id+"\n"+survivor.id, nativeRestoredExec(ctx, t, survivor.backend, survivor.streams[0].taskID, "cat /fork-state; cat /shared/state"))
				events.beginInitTeardown(t, index+2)
				nativeForkFinish(ctx, t, survivor)
				events.checkInitExits(t, index+2)
			}
			// Continue observing after teardown, so delayed replay cannot hide behind
			// the first successful receipt of the expected event count.
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			events.check(t, eventIDs)
			events.checkInitExits(t, 3)
		})
	}
}

func nativeForkConnect(ctx context.Context, t *testing.T, id string) *nativeForkInstance {
	t.Helper()
	require.NoError(t, ctx.Err())
	dir, err := vm.ShimDir(integtest.ShimBaseDir(), defaultNamespace, id)
	require.NoError(t, err)
	recovery, err := vm.ReadRecoveryState(dir)
	require.NoError(t, err)
	address, err := os.ReadFile(dir.AddrFilePath())
	require.NoError(t, err)
	connection, err := ttrpcutil.NewClient(string(address))
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	rpc, err := connection.Client()
	require.NoError(t, err)
	return &nativeForkInstance{id: id, dir: dir, vmm: recovery.VMM, backend: taskAPI.NewTTRPCTaskClient(rpc)}
}

func nativeForkIdentity(t *testing.T, instance *nativeForkInstance) {
	t.Helper()
	actual, err := vm.ReadProcessIdentity(instance.vmm.PID)
	require.NoError(t, err)
	require.Equal(t, instance.vmm, actual, "VMM must continue without restart: %s", instance.id)
}

func nativeForkOpen(ctx context.Context, t *testing.T, taskID, execID string, fifos *cio.FIFOSet) *nativeForkStream {
	t.Helper()
	direct, err := cio.NewDirectIO(ctx, fifos)
	require.NoError(t, err)
	t.Cleanup(func() { _ = direct.Close() })
	read := func(reader io.Reader) <-chan nativeForkLine {
		lines := make(chan nativeForkLine, 8)
		go func() {
			defer close(lines)
			scanner := bufio.NewScanner(reader)
			for scanner.Scan() {
				select {
				case lines <- nativeForkLine{text: scanner.Text()}:
				case <-ctx.Done():
					return
				}
			}
			if err := scanner.Err(); err != nil {
				select {
				case lines <- nativeForkLine{err: err}:
				case <-ctx.Done():
				}
			}
		}()
		return lines
	}
	return &nativeForkStream{taskID: taskID, execID: execID, io: direct, stdout: read(direct.Stdout), stderr: read(direct.Stderr)}
}

func nativeForkRead(ctx context.Context, t *testing.T, lines <-chan nativeForkLine) (string, bool) {
	t.Helper()
	select {
	case line, ok := <-lines:
		require.NoError(t, line.err)
		return line.text, ok
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return "", false
	}
}

func nativeForkReady(ctx context.Context, t *testing.T, stream *nativeForkStream, pid uint32) {
	t.Helper()
	ready, ok := nativeForkRead(ctx, t, stream.stdout)
	require.True(t, ok, "stdout closed before readiness")
	parts := strings.Split(ready, "|")
	require.Len(t, parts, 3)
	require.Equal(t, "ready", parts[0])
	require.Len(t, parts[1], 32)
	require.Equal(t, strconv.FormatUint(uint64(pid), 10), parts[2])
	other, ok := nativeForkRead(ctx, t, stream.stderr)
	require.True(t, ok, "stderr closed before readiness")
	require.Equal(t, ready, other)
	stream.token, stream.pid = parts[1], pid
}

func nativeForkExchange(ctx context.Context, t *testing.T, stream *nativeForkStream, label string, count int) {
	t.Helper()
	// More than a FIFO buffer, with a small bounded output reader queue. The
	// writer must run concurrently with reads to exercise real backpressure.
	payload := strings.Repeat("0123456789abcdef", 64)
	records := make([]string, count)
	for index := range records {
		records[index] = fmt.Sprintf("%s:%04d:%s", label, index, payload)
	}
	written := make(chan error, 1)
	go func() {
		for _, record := range records {
			if _, err := io.WriteString(stream.io.Stdin, record+"\n"); err != nil {
				written <- err
				return
			}
		}
		written <- nil
	}()
	for index, record := range records {
		for _, output := range []struct {
			name  string
			lines <-chan nativeForkLine
		}{{"out", stream.stdout}, {"err", stream.stderr}} {
			line, ok := nativeForkRead(ctx, t, output.lines)
			require.True(t, ok, "%s closed for %s/%s", output.name, stream.taskID, stream.execID)
			require.Equal(t, fmt.Sprintf("%s|%s|%d|%d|%s", output.name, stream.token, stream.pid, stream.count+index+1, record), line,
				"stream lost, duplicated, reordered, replayed input or restarted: %s/%s", stream.taskID, stream.execID)
		}
	}
	select {
	case err := <-written:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stream.count += count
}

func nativeForkFinish(ctx context.Context, t *testing.T, instance *nativeForkInstance) {
	t.Helper()
	// Delete execs first: deleting their init task would terminate them instead
	// of demonstrating restored stdin EOF and normal process exit.
	for _, execOnly := range []bool{true, false} {
		for _, stream := range instance.streams {
			if (stream.execID != "") != execOnly {
				continue
			}
			require.NoError(t, stream.io.Stdin.Close())
			result, err := instance.backend.Wait(ctx, &taskAPI.WaitRequest{ID: stream.taskID, ExecID: stream.execID})
			require.NoError(t, err)
			require.EqualValues(t, 23, result.ExitStatus)
			for _, lines := range []<-chan nativeForkLine{stream.stdout, stream.stderr} {
				line, ok := nativeForkRead(ctx, t, lines)
				require.False(t, ok, "unexpected trailing output before FIFO EOF: %q", line)
			}
			_, err = instance.backend.Delete(ctx, &taskAPI.DeleteRequest{ID: stream.taskID, ExecID: stream.execID})
			require.NoError(t, err)
		}
	}
}

type nativeForkEventLog struct {
	mu        sync.Mutex
	actual    map[string][]string
	initExits map[string][]uint32
	initLimit int
	err       error
	deadline  context.Context
}

func nativeForkSubscribe(ctx context.Context, t *testing.T, client *containerd.Client, taskID string, ids, initTaskIDs []string) *nativeForkEventLog {
	t.Helper()
	log := &nativeForkEventLog{actual: make(map[string][]string), initExits: make(map[string][]uint32), deadline: ctx}
	for _, id := range ids {
		log.actual[id] = nil
	}
	for _, id := range initTaskIDs {
		log.initExits[id] = nil
	}
	events, failures := client.Subscribe(ctx, `topic~="/tasks/"`)
	go func() {
		for {
			select {
			case envelope, ok := <-events:
				if !ok {
					return
				}
				value, err := typeurl.UnmarshalAny(envelope.Event)
				var id, containerID string
				var status uint32
				switch event := value.(type) {
				case *apievents.TaskExecAdded:
					id, containerID = event.ExecID, event.ContainerID
				case *apievents.TaskExecStarted:
					id, containerID = event.ExecID, event.ContainerID
				case *apievents.TaskExit:
					id, containerID, status = event.ID, event.ContainerID, event.ExitStatus
				}
				log.mu.Lock()
				if err != nil {
					log.err = err
				}
				if _, wanted := log.actual[id]; wanted {
					if containerID != taskID || (envelope.Topic == "/tasks/exit" && status != 37) {
						log.err = fmt.Errorf("incorrect event payload: topic=%s id=%s container=%s status=%d", envelope.Topic, id, containerID, status)
					}
					log.actual[id] = append(log.actual[id], envelope.Topic)
				}
				if exits, wanted := log.initExits[id]; wanted && id == containerID && envelope.Topic == "/tasks/exit" {
					log.initExits[id] = append(exits, status)
					if status != 23 || len(exits) >= log.initLimit {
						log.err = fmt.Errorf("premature, duplicate or incorrect init exit: task=%s status=%d count=%d allowed=%d", id, status, len(exits)+1, log.initLimit)
					}
				}
				log.mu.Unlock()
			case err := <-failures:
				if ctx.Err() == nil {
					log.mu.Lock()
					log.err = fmt.Errorf("event subscription ended: %v", err)
					log.mu.Unlock()
				}
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return log
}

func (log *nativeForkEventLog) beginInitTeardown(t *testing.T, count int) {
	t.Helper()
	log.checkInitExits(t, count-1)
	log.mu.Lock()
	defer log.mu.Unlock()
	require.NoError(t, log.err)
	require.Equal(t, log.initLimit+1, count, "init exits must be correlated by sequential teardown")
	log.initLimit = count
}

func (log *nativeForkEventLog) checkInitExits(t *testing.T, count int) {
	t.Helper()
	require.Eventually(t, func() bool {
		log.mu.Lock()
		defer log.mu.Unlock()
		if log.err != nil || log.deadline.Err() != nil {
			return true
		}
		for _, exits := range log.initExits {
			if len(exits) < count {
				return false
			}
		}
		return true
	}, 15*time.Second, 10*time.Millisecond, "init exits must reach the subscriber after each VM teardown")
	log.mu.Lock()
	defer log.mu.Unlock()
	require.NoError(t, log.err)
	for taskID, exits := range log.initExits {
		require.Len(t, exits, count, "init exit count for %s after teardown phase %d", taskID, count)
		for _, status := range exits {
			require.EqualValues(t, 23, status, "init exit status for %s", taskID)
		}
	}
}

func (log *nativeForkEventLog) check(t *testing.T, ids []string) {
	t.Helper()
	wanted := []string{"/tasks/exec-added", "/tasks/exec-started", "/tasks/exit"}
	require.Eventually(t, func() bool {
		log.mu.Lock()
		defer log.mu.Unlock()
		if log.err != nil || log.deadline.Err() != nil {
			return true
		}
		for _, id := range ids {
			if len(log.actual[id]) < len(wanted) {
				return false
			}
		}
		return true
	}, 15*time.Second, 10*time.Millisecond, "lifecycle events must reach the containerd subscriber")
	log.mu.Lock()
	defer log.mu.Unlock()
	require.NoError(t, log.err)
	for _, id := range ids {
		require.Equal(t, wanted, log.actual[id], "missing, duplicated or reordered lifecycle events for %s", id)
	}
}

func nativeForkEventProbe(ctx context.Context, t *testing.T, backend taskAPI.TTRPCTaskService, taskID, execID string) {
	t.Helper()
	spec, err := typeurl.MarshalAnyToProto(&specs.Process{Args: []string{"/bin/sh", "-c", "exit 37"}, Cwd: "/"})
	require.NoError(t, err)
	_, err = backend.Exec(ctx, &taskAPI.ExecProcessRequest{ID: taskID, ExecID: execID, Spec: spec})
	require.NoError(t, err)
	_, err = backend.Start(ctx, &taskAPI.StartRequest{ID: taskID, ExecID: execID})
	require.NoError(t, err)
	result, err := backend.Wait(ctx, &taskAPI.WaitRequest{ID: taskID, ExecID: execID})
	require.NoError(t, err)
	require.EqualValues(t, 37, result.ExitStatus)
	_, err = backend.Delete(ctx, &taskAPI.DeleteRequest{ID: taskID, ExecID: execID})
	require.NoError(t, err)
}
