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
	"strconv"
	"testing"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/firecracker-microvm/firecracker-containerd/internal/integtest"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"
	"github.com/firecracker-microvm/firecracker-containerd/snapshotter/internal/integtest/stargz/fs/source"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

const (
	snapshotterName = "demux"

	al2stargz = "ghcr.io/firecracker-microvm/firecracker-containerd/amazonlinux@sha256:3df2da42bfeb79cd5ef93bb7eff5630a2634b3688659e8bef83e981ed8083ccb"

	noAuth = ""

	dockerMetadataTemplate = `
        {
            "docker-credentials": {
                "%s": {
                    "username": "%s",
                    "password": "%s"
                }
            }
        }`
)

func TestLaunchContainerWithRemoteSnapshotter_Isolated(t *testing.T) {
	integtest.Prepare(t, integtest.WithDefaultNetwork())

	vmID := 0
	err := launchContainerWithRemoteSnapshotterInVM(context.Background(), t, strconv.Itoa(vmID))
	require.NoError(t, err)
}

func TestLaunchMultipleContainersWithRemoteSnapshotter_Isolated(t *testing.T) {
	integtest.Prepare(t, integtest.WithDefaultNetwork())

	eg, ctx := errgroup.WithContext(context.Background())

	numberOfVms := integtest.NumberOfVms
	for vmID := 0; vmID < numberOfVms; vmID++ {
		ctx := ctx
		id := vmID
		eg.Go(func() error {
			return launchContainerWithRemoteSnapshotterInVM(ctx, t, strconv.Itoa(id))
		})
	}
	err := eg.Wait()
	require.NoError(t, err)
}

func launchContainerWithRemoteSnapshotterInVM(ctx context.Context, t testing.TB, vmID string) error {
	// For integration testing, assume the namespace is same as the VM ID.
	namespace := vmID

	ctx = namespaces.WithNamespace(ctx, namespace)

	client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(integtest.FirecrackerRuntime))
	if err != nil {
		return fmt.Errorf("Unable to create client to containerd service at %s, is containerd running? [%v]", integtest.ContainerdSockPath, err)
	}

	defer client.Close()
	fcClient, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
	if err != nil {
		return fmt.Errorf("Failed to create fccontrol client. [%v]", err)
	}

	defer fcClient.Close()

	// Disable 8250 serial device and lessen the number of log messages written to the serial console.
	// https://github.com/firecracker-microvm/firecracker/blob/v1.1.0/docs/prod-host-setup.md
	kernelArgs := integtest.DefaultRuntimeConfig.KernelArgs + " 8250.nr_uarts=0 quiet loglevel=1"

	_, err = fcClient.CreateVM(ctx, &proto.CreateVMRequest{
		VMID:       vmID,
		KernelArgs: kernelArgs,
		RootDrive: &proto.FirecrackerRootDrive{
			HostPath: "/var/lib/firecracker-containerd/runtime/rootfs-stargz.img",
		},
		NetworkInterfaces: []*proto.FirecrackerNetworkInterface{
			{
				AllowMMDS: true,
				CNIConfig: &proto.CNIConfiguration{
					NetworkName:   "fcnet",
					InterfaceName: "veth0",
				},
			},
		},
		MachineCfg: &proto.FirecrackerMachineConfiguration{
			VcpuCount:  1,
			MemSizeMib: 1024,
		},
		TimeoutSeconds: 60,
		ContainerCount: 1,
	})
	if err != nil {
		return fmt.Errorf("Failed to create VM[%s]: %w", vmID, err)
	}
	defer fcClient.StopVM(ctx, &proto.StopVMRequest{VMID: vmID})

	_, err = fcClient.SetVMMetadata(ctx, &proto.SetVMMetadataRequest{
		VMID:     vmID,
		Metadata: fmt.Sprintf(dockerMetadataTemplate, "ghcr.io", noAuth, noAuth),
	})
	if err != nil {
		return fmt.Errorf("Failed to configure VM metadata for registry resolution: %w", err)
	}

	image, err := client.Pull(ctx, al2stargz,
		containerd.WithPullUnpack,
		containerd.WithPullSnapshotter(snapshotterName),
		containerd.WithImageHandlerWrapper(source.AppendDefaultLabelsHandlerWrapper(al2stargz, 10*mib)),
	)
	if err != nil {
		return fmt.Errorf("Failed to pull image for VM[%s]: %w", vmID, err)
	}
	defer client.ImageService().Delete(ctx, image.Name())

	container, err := client.NewContainer(ctx, fmt.Sprintf("container-%s", vmID),
		containerd.WithSnapshotter(snapshotterName),
		containerd.WithNewSnapshot("snapshot", image),
		containerd.WithNewSpec(
			firecrackeroci.WithVMLocalImageConfig(image),
			oci.WithDefaultPathEnv,
			oci.WithProcessArgs("/bin/sh", "-ec", "echo stargz-write > /stargz-write; test \"$(cat /stargz-write)\" = stargz-write; cat /proc/mounts >&2; echo hello"),
			firecrackeroci.WithVMID(vmID),
			firecrackeroci.WithVMNetwork,
		),
	)
	if err != nil {
		return fmt.Errorf("Failed to create container in VM[%s]: %w", vmID, err)
	}
	defer container.Delete(ctx, containerd.WithSnapshotCleanup)

	result, err := integtest.RunTask(ctx, container)
	if err != nil {
		return fmt.Errorf("Failed to run task in VM[%s]: %w", vmID, err)
	}
	t.Logf("namespace=%s remote snapshotter mounts:\n%s", namespace, result.Stderr)
	if result.ExitCode != 0 || result.Stdout != "hello\n" {
		return fmt.Errorf("remote snapshotter workload exit=%d stdout=%q stderr=%q", result.ExitCode, result.Stdout, result.Stderr)
	}
	return nil
}
