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
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
)

func TestStorageBaseline_Isolated(t *testing.T) {
	integtest.Prepare(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ctx = namespaces.WithNamespace(ctx, defaultNamespace)
	client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(firecrackerRuntime))
	require.NoError(t, err)
	defer client.Close()
	control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
	require.NoError(t, err)
	defer control.Close()
	const id = "storage-baseline"
	_, err = control.CreateVM(ctx, &proto.CreateVMRequest{VMID: id, ContainerCount: 2, GuestRoot: &proto.GuestRootStorage{Mode: "ram"}, MachineCfg: &proto.FirecrackerMachineConfiguration{MemSizeMib: 512}})
	require.NoError(t, err)
	defer func() { _, err := control.StopVM(ctx, &proto.StopVMRequest{VMID: id}); require.NoError(t, err) }()
	guest, err := client.NewContainer(ctx, id+"-guest-root", containerd.WithNewSpec(firecrackeroci.WithVMID(id), oci.WithDefaultPathEnv,
		oci.WithMounts([]specs.Mount{{Destination: "/overlay", Type: "bind", Source: "/rom/overlay", Options: []string{"bind", "ro"}}}),
		oci.WithProcessArgs("/bin/sh", "-ec", `printf 'guest_root=%s\n' "$(stat -f -c %T /)"; printf 'guest_upper_magic=%s\n' "$(stat -f -c %t /overlay)"; cat /proc/self/mountinfo; cat /proc/cmdline; test -d /sys/kernel/mm/damon/admin; printf 'damon_sysfs=present\n'`)))
	require.NoError(t, err)
	defer guest.Delete(ctx)
	guestOutput := runStorageAudit(ctx, t, guest, containerd.WithRootFS([]mount.Mount{vm.AddLocalMountIdentifier(mount.Mount{Type: "bind", Source: "/", Options: []string{"bind"}})}))
	t.Logf("Guest root inspection:\n%s", guestOutput)
	require.Contains(t, guestOutput, "guest_upper_magic=1021994", "original VM writable upper must be measured as tmpfs")
	image, err := alpineImage(ctx, client, defaultSnapshotterName)
	require.NoError(t, err)
	workload, err := client.NewContainer(ctx, id+"-workload", containerd.WithSnapshotter(defaultSnapshotterName), containerd.WithNewSnapshot(id+"-workload", image),
		containerd.WithNewSpec(firecrackeroci.WithVMID(id), oci.WithDefaultPathEnv, oci.WithProcessArgs("/bin/sh", "-ec", `echo disk-write >/state; sync; printf 'workload_root_magic=%s\n' "$(stat -f -c %t /)"; cat /proc/self/mountinfo; test "$(cat /state)" = disk-write`)))
	require.NoError(t, err)
	defer workload.Delete(ctx, containerd.WithSnapshotCleanup)
	workloadOutput := runStorageAudit(ctx, t, workload)
	t.Logf("Workload root inspection:\n%s", workloadOutput)
	require.Contains(t, workloadOutput, "workload_root_magic=ef53", "container writable root already uses ext4")
}

func runStorageAudit(ctx context.Context, t *testing.T, container containerd.Container, options ...containerd.NewTaskOpts) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	process, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, &stdout, &stderr)), options...)
	require.NoError(t, err)
	status, err := process.Wait(ctx)
	require.NoError(t, err)
	require.NoError(t, process.Start(ctx))
	select {
	case result := <-status:
		require.NoError(t, result.Error())
		require.Zero(t, result.ExitCode(), stderr.String())
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err = process.Delete(ctx)
	require.NoError(t, err)
	return stdout.String()
}
