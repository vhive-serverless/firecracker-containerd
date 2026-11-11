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
	"syscall"
	"testing"
	"time"

	"github.com/containerd/cgroups/v3/cgroup2/stats"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"

	"github.com/firecracker-microvm/firecracker-containerd/internal/integtest"
)

func TestResourceAccounting_Isolated(t *testing.T) {
	integtest.Prepare(t)
	ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), defaultNamespace), 60*time.Second)
	defer cancel()
	client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(firecrackerRuntime))
	require.NoError(t, err)
	defer client.Close()
	image, err := alpineImage(ctx, client, defaultSnapshotterName)
	require.NoError(t, err)
	const name = "resource-accounting"
	container, err := client.NewContainer(ctx, name,
		containerd.WithSnapshotter(defaultSnapshotterName), containerd.WithNewSnapshot(name, image),
		containerd.WithNewSpec(
			oci.WithMemoryLimit(64*1024*1024), oci.WithCPUCFS(50000, 100000),
			oci.WithLinuxNamespace(specs.LinuxNamespace{Type: specs.CgroupNamespace}),
			oci.WithMounts([]specs.Mount{{Destination: "/sys/fs/cgroup", Type: "cgroup", Source: "cgroup", Options: []string{"ro", "nosuid", "noexec", "nodev"}}}),
			oci.WithProcessArgs("/bin/sh", "-ec", `
                test "$(stat -f -c %t /sys/fs/cgroup)" = 63677270
                test "$(cat /sys/fs/cgroup/memory.max)" = 67108864
                test "$(cat /sys/fs/cgroup/cpu.max)" = "50000 100000"
                echo RESOURCE_LIMITS_OK
                while :; do :; done`)))
	require.NoError(t, err)
	defer func() { _ = container.Delete(ctx, containerd.WithSnapshotCleanup) }()
	var stdout, stderr bytes.Buffer
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, &stdout, &stderr)))
	require.NoError(t, err)
	defer func() {
		_ = task.Kill(ctx, syscall.SIGKILL)
		_, _ = task.Delete(ctx, containerd.WithProcessKill)
		t.Logf("resource test stdout=%s stderr=%s", stdout.String(), stderr.String())
	}()
	exited, err := task.Wait(ctx)
	require.NoError(t, err)
	require.NoError(t, task.Start(ctx))
	// Accumulate several quota periods so throttling and CPU usage are measurable.
	time.Sleep(time.Second)
	metric, err := task.Metrics(ctx)
	require.NoError(t, err)
	var accounting stats.Metrics
	require.NoError(t, metric.Data.UnmarshalTo(&accounting))
	t.Logf("guest cgroup v2 accounting: %s", accounting.String())
	require.Positive(t, accounting.GetCPU().GetUsageUsec())
	require.Positive(t, accounting.GetCPU().GetNrThrottled())
	require.Positive(t, accounting.GetMemory().GetUsage())
	require.Less(t, accounting.GetMemory().GetUsage(), uint64(64*1024*1024))
	require.Positive(t, accounting.GetPids().GetCurrent())
	require.NoError(t, task.Kill(ctx, syscall.SIGKILL))
	select {
	case status := <-exited:
		require.NoError(t, status.Error())
		require.EqualValues(t, 137, status.ExitCode())
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err = task.Delete(ctx)
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "RESOURCE_LIMITS_OK", stderr.String())
	require.NoError(t, container.Delete(ctx, containerd.WithSnapshotCleanup))
}

// delayedCapture exercises output backpressure before the task exits.
type delayedCapture struct{ bytes.Buffer }

func (w *delayedCapture) Write(data []byte) (int, error) {
	time.Sleep(time.Millisecond)
	return w.Buffer.Write(data)
}

func TestBinaryOutput_Isolated(t *testing.T) {
	integtest.Prepare(t)
	ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), defaultNamespace), 60*time.Second)
	defer cancel()
	client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(firecrackerRuntime))
	require.NoError(t, err)
	defer client.Close()
	image, err := alpineImage(ctx, client, defaultSnapshotterName)
	require.NoError(t, err)
	const name = "binary-output"
	container, err := client.NewContainer(ctx, name,
		containerd.WithSnapshotter(defaultSnapshotterName), containerd.WithNewSnapshot(name, image),
		containerd.WithNewSpec(oci.WithProcessArgs("/bin/sh", "-ec", `
printf 'begin\n' >&2
dd if=/dev/zero bs=65536 count=64 2>/dev/null
printf 'end\n' >&2`)))
	require.NoError(t, err)
	defer func() { _ = container.Delete(ctx, containerd.WithSnapshotCleanup) }()
	var output delayedCapture
	var stderr bytes.Buffer
	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, &output, &stderr)))
	require.NoError(t, err)
	exited, err := task.Wait(ctx)
	require.NoError(t, err)
	require.NoError(t, task.Start(ctx))
	select {
	case status := <-exited:
		require.NoError(t, status.Error())
		require.Zero(t, status.ExitCode())
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitTaskOutput(ctx, t, task)
	_, err = task.Delete(ctx)
	require.NoError(t, err)
	require.Equal(t, 4*1024*1024, output.Len(), "binary output truncated: %s", stderr.String())
	require.Equal(t, sha256.Sum256(make([]byte, 4*1024*1024)), sha256.Sum256(output.Bytes()))
	require.Equal(t, "begin\nend\n", stderr.String())
	t.Logf("slow consumer received %d bytes without corruption", output.Len())
}
