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
	"fmt"
	"os"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/stretchr/testify/require"

	"github.com/firecracker-microvm/firecracker-containerd/internal/integtest"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"
)

// A failed runc create must release its root drive even when an explicit VM
// remains alive. A successful task must then be able to reuse the same slot.
func TestFailedCreateReleasesDrive_Isolated(t *testing.T) {
	integtest.Prepare(t)
	ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), defaultNamespace), 90*time.Second)
	t.Cleanup(cancel)
	client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(firecrackerRuntime))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = control.Close() })
	image, err := alpineImage(ctx, client, defaultSnapshotterName)
	require.NoError(t, err)
	const id = "failed-create-reuse"
	_, err = control.CreateVM(ctx, &proto.CreateVMRequest{VMID: id, ContainerCount: 1})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, stopErr := control.StopVM(ctx, &proto.StopVMRequest{VMID: id, TimeoutSeconds: 10})
		t.Logf("explicit VM cleanup: %v", stopErr)
	})
	directory, err := vm.ShimDir(integtest.ShimBaseDir(), defaultNamespace, id)
	require.NoError(t, err)
	original, err := vm.ReadRecoveryState(directory)
	require.NoError(t, err)

	newContainer := func(name, cwd string) containerd.Container {
		container, createErr := client.NewContainer(ctx, name,
			containerd.WithSnapshotter(defaultSnapshotterName), containerd.WithNewSnapshot(name, image),
			containerd.WithNewSpec(oci.WithProcessArgs("/bin/sh", "-ec", "echo DRIVE_REUSED"),
				oci.WithProcessCwd(cwd), firecrackeroci.WithVMID(id)))
		require.NoError(t, createErr)
		t.Cleanup(func() { _ = container.Delete(context.WithoutCancel(ctx), containerd.WithSnapshotCleanup) })
		return container
	}
	for attempt := range 3 {
		bad := newContainer(fmt.Sprintf("bad-create-%d", attempt), "relative-path")
		_, err := bad.NewTask(ctx, cio.NullIO)
		require.Error(t, err)
		require.ErrorContains(t, err, "Cwd must be an absolute path", "expected a guest runc validation error")
		t.Logf("expected create failure: %v", err)
		current, err := vm.ReadRecoveryState(directory)
		require.NoError(t, err)
		require.Equal(t, original.VMM, current.VMM, "failed create must not replace the VM")

		good := newContainer(fmt.Sprintf("good-create-%d", attempt), "/")
		var output, stderr bytes.Buffer
		task, err := good.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, &output, &stderr)))
		require.NoError(t, err, "a failed create must not consume the only drive slot")
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
		require.Equal(t, "DRIVE_REUSED\n", output.String())
		require.Empty(t, stderr.String())
		link, err := directory.BundleLink(bad.ID())
		require.NoError(t, err)
		_, err = os.Lstat(link.RootPath())
		require.True(t, os.IsNotExist(err), "failed create left its bundle symlink: %v", err)
		require.NoError(t, good.Delete(ctx, containerd.WithSnapshotCleanup))
		require.NoError(t, bad.Delete(ctx, containerd.WithSnapshotCleanup))
	}
}
