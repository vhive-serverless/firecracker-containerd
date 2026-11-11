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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/firecracker-microvm/firecracker-containerd/internal/integtest"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

var evaluationBuildRevision = "unrecorded"
var evaluationBuildSourceDigest = "unrecorded"
var evaluationBuildDirty = "unrecorded"

func TestMemoryEvaluationBuildInfo(t *testing.T) {
	if os.Getenv("FICD_EVAL_PROVENANCE") != "1" {
		t.Skip("worker build provenance only")
	}
	value := map[string]any{"source_revision": evaluationBuildRevision, "source_digest": evaluationBuildSourceDigest, "source_dirty": evaluationBuildDirty != "false"}
	data, err := json.Marshal(value)
	require.NoError(t, err)
	fmt.Printf("EVAL_BUILD_INFO %s\n", data)
}

type memoryEvaluationArm struct {
	Name, Upper         string
	Ext4, Pmem, Reclaim bool
}

func memoryEvaluationArms() map[string]memoryEvaluationArm {
	return map[string]memoryEvaluationArm{
		"original":     {"original", "ram", false, false, false},
		"disk":         {"disk", "disk", false, false, false},
		"pmem":         {"pmem", "disk", true, true, false},
		"reclaim":      {"reclaim", "disk", false, false, true},
		"combined":     {"combined", "disk", true, true, true},
		"ext4-block":   {"ext4-block", "disk", true, false, false},
		"ext4-reclaim": {"ext4-reclaim", "disk", true, false, true},
	}
}

func evaluationMonotonicNS() int64 {
	var now unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &now); err != nil {
		panic(err)
	}
	return now.Nano()
}

func evaluationJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	file, err := os.OpenFile(path+".tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	require.NoError(t, err)
	_, err = file.Write(append(data, '\n'))
	require.NoError(t, err)
	require.NoError(t, file.Close())
	require.NoError(t, os.Link(path+".tmp", path))
	require.NoError(t, os.Remove(path+".tmp"))
}

type evaluationOutput struct {
	mu      sync.Mutex
	file    *os.File
	pending []byte
	idle    chan struct{}
	once    sync.Once
	records []map[string]any
}

func (o *evaluationOutput) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.file.Write(data)
	if err != nil {
		return n, err
	}
	o.pending = append(o.pending, data...)
	for {
		line, remaining, found := bytes.Cut(o.pending, []byte{'\n'})
		if !found {
			break
		}
		o.pending = remaining
		var record map[string]any
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		record["received_host_monotonic_ns"] = evaluationMonotonicNS()
		o.records = append(o.records, record)
		if record["phase"] == "idle_start" {
			o.once.Do(func() { close(o.idle) })
		}
	}
	return n, nil
}

func (o *evaluationOutput) snapshot() []map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]map[string]any(nil), o.records...)
}

// TestMemoryEvaluation_Isolated is run only by the dedicated host controller.
// The controller builds this binary before sampling and owns all trial resources.
func TestMemoryEvaluation_Isolated(t *testing.T) {
	name := os.Getenv("FICD_EVAL_ARM")
	if name == "" {
		t.Skip("evaluate-memory.sh supplies one explicit trial configuration")
	}
	arm, ok := memoryEvaluationArms()[name]
	require.True(t, ok, "unknown evaluation arm")
	token := os.Getenv("FICD_EVAL_TOKEN")
	require.True(t, len(token) == 12 && strings.Trim(token, "0123456789abcdef") == "")
	const out = "/eval"
	const source = "/inputs/root.img"
	idle, err := time.ParseDuration(os.Getenv("FICD_EVAL_IDLE"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, idle, 10*time.Second)
	require.LessOrEqual(t, idle, 120*time.Second)
	integtest.Prepare(t)
	ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), defaultNamespace), 240*time.Second)
	t.Cleanup(cancel)
	events, err := os.OpenFile(filepath.Join(out, "phases.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = events.Close() })
	emit := func(phase string, fields map[string]any) {
		fields["phase"], fields["monotonic_ns"], fields["utc"] = phase, evaluationMonotonicNS(), time.Now().UTC().Format(time.RFC3339Nano)
		require.NoError(t, json.NewEncoder(events).Encode(fields))
	}
	emit("start", map[string]any{"arm": arm, "vms": 2, "vcpus_each": 1, "memory_mib_each": 512, "upper_capacity_mib": 128, "guest_upper_payload_mib": 64, "workload_payload_mib": 128, "anonymous_mib_each": 32, "idle_seconds": idle.Seconds(), "jailer": false})
	client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(firecrackerRuntime))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	require.Eventually(t, func() bool {
		attempt, stop := context.WithTimeout(ctx, 500*time.Millisecond)
		defer stop()
		_, err := client.Version(attempt)
		return err == nil
	}, 15*time.Second, 100*time.Millisecond)
	control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = control.Close() })
	input := filepath.Join(out, "input")
	require.NoError(t, os.Mkdir(input, 0755))
	copied, err := copyNativeFile(ctx, source, input, "root.img")
	require.NoError(t, err)
	inputImage := filepath.Join(input, "root.img")
	// #nosec G302 -- This per-trial immutable input must be readable across VM identities.
	require.NoError(t, os.Chmod(inputImage, 0444))
	probeDigest := hashPmemImage(t, "../tools/memory-probe/fc-memory-probe")
	emit("input_ready", map[string]any{"image": copied, "probe_sha256": probeDigest})
	type runningVM struct {
		id            string
		root          containerd.Container
		workload      containerd.Container
		probe, writer containerd.Task
		wait          <-chan containerd.ExitStatus
		output        *evaluationOutput
		metrics       *reportingMetrics
		api           *fc.Client
		pid           int
	}
	instances := make([]runningVM, 0, 2)
	for index := range 2 {
		id := fmt.Sprintf("me-%s-%d", token[:8], index)
		metrics := &reportingMetrics{}
		metricsPath := filepath.Join(out, fmt.Sprintf("vm%d-metrics.fifo", index))
		go metrics.read(ctx, metricsPath)
		started := time.Now()
		response, err := control.CreateVM(ctx, &proto.CreateVMRequest{VMID: id, ContainerCount: 2, MetricsFifoPath: metricsPath,
			RootDrive:   &proto.FirecrackerRootDrive{HostPath: inputImage, UsePmem: arm.Pmem, ImageSha256: copied.SHA256},
			GuestRoot:   &proto.GuestRootStorage{Mode: arm.Upper, SizeMib: 128},
			MachineCfg:  &proto.FirecrackerMachineConfiguration{MemSizeMib: 512, VcpuCount: 1},
			Reclamation: &proto.GuestReclamation{Enabled: arm.Reclaim}})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = control.StopVM(context.WithoutCancel(ctx), &proto.StopVMRequest{VMID: id}) })
		directory, err := vm.ShimDir(integtest.ShimBaseDir(), defaultNamespace, id)
		require.NoError(t, err)
		state, err := vm.ReadRecoveryState(directory)
		require.NoError(t, err)
		api := fc.NewClient(response.SocketPath, logrus.NewEntry(logrus.New()), false)
		emit("vm_ready", map[string]any{"vm": index, "pid": state.VMM.PID, "create_ns": time.Since(started).Nanoseconds()})
		instances = append(instances, runningVM{id: id, metrics: metrics, api: api, pid: state.VMM.PID})
	}
	// Cache publication is complete; this copy no longer backs either VM.
	require.NoError(t, os.Remove(inputImage))
	pids := []int{instances[0].pid, instances[1].pid}
	evaluationJSON(t, filepath.Join(out, "vmm-pids.json"), pids)
	image, err := alpineImage(ctx, client, defaultSnapshotterName)
	require.NoError(t, err)
	for index := range instances {
		instance := &instances[index]
		id := instance.id
		workloadStarted := time.Now()
		emit("vm_workload_start", map[string]any{"vm": index})
		instance.workload, err = client.NewContainer(ctx, id+"-workload", containerd.WithSnapshotter(defaultSnapshotterName),
			containerd.WithNewSnapshot(id+"-workload", image), containerd.WithNewSpec(firecrackeroci.WithVMID(id), oci.WithDefaultPathEnv,
				oci.WithProcessArgs("/bin/sh", "-ec", fmt.Sprintf(`tr '\000' '%c' </dev/zero | head -c 134217728 >/state; sync; sha256sum /state >/expected; touch /ready; exec sleep 200`, 'B'+index*2))))
		require.NoError(t, err)
		workloadContainer := instance.workload
		t.Cleanup(func() { _ = workloadContainer.Delete(context.WithoutCancel(ctx), containerd.WithSnapshotCleanup) })
		instance.writer, err = instance.workload.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, io.Discard, io.Discard)))
		require.NoError(t, err)
		writer := instance.writer
		t.Cleanup(func() {
			wait, _ := writer.Wait(context.WithoutCancel(ctx))
			_ = writer.Kill(context.WithoutCancel(ctx), syscall.SIGKILL, containerd.WithKillAll)
			select {
			case <-wait:
			case <-time.After(5 * time.Second):
			}
			_, _ = writer.Delete(context.WithoutCancel(ctx))
		})
		require.NoError(t, instance.writer.Start(ctx))
		stdout, err := os.OpenFile(filepath.Join(out, fmt.Sprintf("vm%d-guest.log", index)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		require.NoError(t, err)
		t.Cleanup(func() { _ = stdout.Close() })
		stderr, err := os.OpenFile(filepath.Join(out, fmt.Sprintf("vm%d-guest-stderr.log", index)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		require.NoError(t, err)
		t.Cleanup(func() { _ = stderr.Close() })
		instance.output = &evaluationOutput{file: stdout, idle: make(chan struct{})}
		script := fmt.Sprintf(`
            if test %s = ram; then mount -o remount,size=128m /upper; fi
            stat -f -c 'upper_magic=%%t upper_block_size=%%S upper_blocks=%%b' /upper
            cat /proc/mounts
            /usr/local/bin/fc-memory-probe -file /rom/usr/local/bin/agent -stat-only
            sha256sum /usr/local/bin/fc-memory-probe
            test ! -e /guest-upper-payload
            tr '\000' '%c' </dev/zero | head -c 67108864 >/guest-upper-payload
            cp /etc/debian_version /version-copy; echo copyup >>/etc/debian_version
            mv /version-copy /version-renamed; rm /version-renamed
            sync; sha256sum /guest-upper-payload >/upper-expected
            touch /upper-ready
            while ! test -e /probe-go; do sleep .02; done
            /usr/local/bin/fc-memory-probe -file /rom/usr/local/bin/agent -anon-mib 32 -idle %s
            sha256sum -c /upper-expected
            test "$(cat /sys/module/damon_reclaim/parameters/enabled)" = %s
            echo guest-upper-readback=passed
        `, arm.Upper, 'A'+index*2, idle.String(), map[bool]string{false: "N", true: "Y"}[arm.Reclaim])
		instance.root, err = client.NewContainer(ctx, id+"-root", containerd.WithNewSpec(firecrackeroci.WithVMID(id), oci.WithDefaultPathEnv,
			oci.WithAddedCapabilities([]string{"CAP_SYS_ADMIN"}), oci.WithMounts([]specs.Mount{
				{Destination: "/rom", Type: "bind", Source: "/rom", Options: []string{"bind", "ro"}},
				{Destination: "/upper", Type: "bind", Source: "/rom/overlay", Options: []string{"bind", "rw"}},
			}), oci.WithProcessArgs("/bin/sh", "-ec", script)))
		require.NoError(t, err)
		rootContainer := instance.root
		t.Cleanup(func() { _ = rootContainer.Delete(context.WithoutCancel(ctx)) })
		instance.probe, err = instance.root.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, instance.output, stderr)), containerd.WithRootFS([]mount.Mount{
			vm.AddLocalMountIdentifier(mount.Mount{Type: "bind", Source: "/", Options: []string{"bind"}})}))
		require.NoError(t, err)
		probe := instance.probe
		t.Cleanup(func() {
			_ = probe.Kill(context.WithoutCancel(ctx), syscall.SIGKILL, containerd.WithKillAll)
			_, _ = probe.Delete(context.WithoutCancel(ctx))
		})
		instance.wait, err = instance.probe.Wait(ctx)
		require.NoError(t, err)
		require.NoError(t, instance.probe.Start(ctx))
		require.Contains(t, nativeExec(ctx, t, instance.writer, "while ! test -e /ready; do sleep .02; done; cat /expected"), "/state")
		require.Equal(t, "ready", nativeExec(ctx, t, instance.probe, "while ! test -e /upper-ready; do sleep .02; done; echo ready"))
		emit("vm_workload_ready", map[string]any{"vm": index, "duration_ns": time.Since(workloadStarted).Nanoseconds()})
	}
	emit("payloads_created", map[string]any{})
	for _, instance := range instances {
		require.Contains(t, nativeExec(ctx, t, instance.writer, "sha256sum -c /expected"), "/state: OK")
		require.Contains(t, nativeExec(ctx, t, instance.probe, "sha256sum -c /upper-expected"), "/guest-upper-payload: OK")
	}
	emit("workload_ready", map[string]any{})
	for _, instance := range instances {
		nativeExec(ctx, t, instance.probe, "touch /probe-go")
	}
	for _, instance := range instances {
		select {
		case <-instance.output.idle:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	emit("idle_start", map[string]any{})
	for index := range instances {
		instance := &instances[index]
		select {
		case status := <-instance.wait:
			require.NoError(t, status.Error())
			require.Zero(t, status.ExitCode())
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		_, err := instance.probe.Delete(ctx)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			for _, r := range instance.output.snapshot() {
				if r["phase"] == "complete" {
					return true
				}
			}
			return false
		}, 3*time.Second, 10*time.Millisecond)
		log, err := os.ReadFile(instance.output.file.Name())
		require.NoError(t, err)
		require.Contains(t, string(log), probeDigest+"  /usr/local/bin/fc-memory-probe")
		if arm.Ext4 {
			require.Contains(t, string(log), " /rom ext4 ")
		} else {
			require.Contains(t, string(log), " /rom squashfs ")
		}
		if arm.Upper == "ram" {
			require.Contains(t, string(log), "upper_magic=1021994")
		} else {
			require.Contains(t, string(log), "upper_magic=ef53")
		}
		records := instance.output.snapshot()
		evaluationJSON(t, filepath.Join(out, fmt.Sprintf("vm%d-probe.json", index)), records)
		daxFound, verified := false, false
		for _, record := range records {
			if dax, ok := record["statx_dax"].(bool); ok {
				require.Equal(t, arm.Pmem, dax)
				daxFound = true
			}
			if record["phase"] == "complete" {
				require.Equal(t, true, record["verified"])
				verified = true
			}
		}
		require.True(t, daxFound && verified)
	}
	emit("probes_complete", map[string]any{})
	for index, instance := range instances {
		started := time.Now()
		result := nativeExec(ctx, t, instance.writer, "sha256sum -c /expected")
		require.Contains(t, result, "/state: OK")
		emit("workload_readback", map[string]any{"vm": index, "duration_ns": time.Since(started).Nanoseconds(), "result": result})
		_, err := instance.api.CreateSyncAction(ctx, &models.InstanceActionInfo{ActionType: fc.String(models.InstanceActionInfoActionTypeFlushMetrics)})
		require.NoError(t, err)
		require.Eventually(t, instance.metrics.ready, 3*time.Second, 10*time.Millisecond)
		reports, freed, failures, err := instance.metrics.snapshot()
		require.NoError(t, err)
		require.Zero(t, failures)
		if arm.Reclaim {
			balloon, err := control.GetBalloonConfig(ctx, &proto.GetBalloonConfigRequest{VMID: instance.id})
			require.NoError(t, err)
			require.True(t, balloon.BalloonConfig.FreePageReporting)
			require.Zero(t, balloon.BalloonConfig.AmountMib)
			stats, err := control.GetBalloonStats(ctx, &proto.GetBalloonStatsRequest{VMID: instance.id})
			require.NoError(t, err)
			require.Zero(t, stats.ActualMib)
		} else {
			require.Zero(t, reports)
		}
		emit("reporting", map[string]any{"vm": index, "descriptors": reports, "traffic_bytes": freed, "failures": failures, "reclamation_enabled": arm.Reclaim})
	}
	allocations := make(map[string]map[string]int64)
	require.NoError(t, filepath.WalkDir(integtest.ShimBaseDir(), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() && (entry.Name() == guestOverlayFile || strings.HasSuffix(path, copied.SHA256+".img")) {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			allocations[path] = map[string]int64{"logical_bytes": info.Size(), "allocated_bytes": info.Sys().(*syscall.Stat_t).Blocks * 512}
		}
		return nil
	}))
	evaluationJSON(t, filepath.Join(out, "disk-allocation.json"), allocations)
	emit("workload_complete", map[string]any{"correctness": true})
	// Normal cleanup runs through registered owned-task/VM callbacks. The
	// controller samples cgroup teardown separately after the test exits.
}
