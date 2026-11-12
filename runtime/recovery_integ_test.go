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
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/awslabs/tc-redirect-tap/cmd/tc-redirect-tap/args"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/firecracker-microvm/firecracker-containerd/config"
	"github.com/firecracker-microvm/firecracker-containerd/internal/integtest"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"
)

// The default SDK binary path must retain the VMM identity for forced StopVM.
func TestForceStopDefaultFirecracker_Isolated(t *testing.T) {
	integtest.Prepare(t, func(cfg *config.Config) { cfg.FirecrackerBinaryPath = "" })
	ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), defaultNamespace), time.Minute)
	defer cancel()
	control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
	require.NoError(t, err)
	defer control.Close()
	const id = "force-stop-default"
	_, err = control.CreateVM(ctx, &proto.CreateVMRequest{VMID: id})
	require.NoError(t, err)
	directory, err := vm.ShimDir(integtest.ShimBaseDir(), defaultNamespace, id)
	require.NoError(t, err)
	state, err := vm.ReadRecoveryState(directory)
	require.NoError(t, err)
	require.NotZero(t, state.VMM.PID)
	_, err = control.PauseVM(ctx, &proto.PauseVMRequest{VMID: id})
	require.NoError(t, err)
	_, err = control.StopVM(ctx, &proto.StopVMRequest{VMID: id})
	require.Equal(t, codes.Internal, status.Code(err), "StopVM returned %v", err)
	require.ErrorContains(t, err, "forcefully terminated")
	alive, err := vm.IsProcessAlive(state.VMM)
	require.NoError(t, err)
	require.False(t, alive, "VMM must be gone when StopVM returns")
	require.NoDirExists(t, directory.RootPath())
}

func TestCrashRecovery_Isolated(t *testing.T) {
	testVMRecovery(t, true)
}

func TestCNIGracefulCleanup_Isolated(t *testing.T) {
	testVMRecovery(t, false)
}

func testVMRecovery(t *testing.T, crash bool) {
	t.Helper()
	integtest.Prepare(t)
	ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), defaultNamespace), 120*time.Second)
	defer cancel()
	client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(firecrackerRuntime))
	require.NoError(t, err)
	defer client.Close()
	control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
	require.NoError(t, err)
	defer control.Close()
	image, err := alpineImage(ctx, client, defaultSnapshotterName)
	require.NoError(t, err)
	const network = "crash-recovery"
	require.NoError(t, writeCNIConf("/etc/cni/conf.d/"+network+".conflist", "tc-redirect-tap", network, "192.0.2.1"))
	before, err := net.Interfaces()
	require.NoError(t, err)
	for iteration := range 3 {
		t.Run(fmt.Sprintf("iteration-%d", iteration), func(t *testing.T) {
			id := fmt.Sprintf("crash-recovery-%d", iteration)
			response, err := control.CreateVM(ctx, &proto.CreateVMRequest{
				VMID:         id,
				JailerConfig: &proto.JailerConfig{UID: jailerUID, GID: jailerGID},
				NetworkInterfaces: []*proto.FirecrackerNetworkInterface{{CNIConfig: &proto.CNIConfiguration{NetworkName: network, InterfaceName: "veth0", Args: []*proto.CNIConfiguration_CNIArg{
					{Key: "IgnoreUnknown", Value: "true"},
					{Key: args.TCRedirectTapUID, Value: fmt.Sprint(jailerUID)},
					{Key: args.TCRedirectTapGID, Value: fmt.Sprint(jailerGID)},
				}}}},
			})
			require.NoError(t, err)
			container, err := client.NewContainer(ctx, id,
				containerd.WithSnapshotter(defaultSnapshotterName),
				containerd.WithNewSnapshot(id, image),
				containerd.WithNewSpec(oci.WithProcessArgs("/bin/sleep", "60"), firecrackeroci.WithVMID(id), firecrackeroci.WithVMNetwork))
			require.NoError(t, err)
			task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, nil, nil)))
			require.NoError(t, err)
			require.NoError(t, task.Start(ctx))
			directory, err := vm.ShimDir(integtest.ShimBaseDir(), defaultNamespace, id)
			require.NoError(t, err)
			state, err := vm.ReadRecoveryState(directory)
			require.NoError(t, err)
			require.NotZero(t, state.VMM.PID)
			require.True(t, state.OwnNetNS)
			if crash {
				// Acquire the process handle before checking identity and signalling.
				fd, err := unix.PidfdOpen(state.Shim.PID, 0)
				require.NoError(t, err)
				actual, err := vm.ReadProcessIdentity(state.Shim.PID)
				require.NoError(t, err)
				require.Equal(t, state.Shim, actual)
				require.NoError(t, unix.PidfdSendSignal(fd, syscall.SIGKILL, nil, 0))
				require.NoError(t, unix.Close(fd))
			} else {
				exit, err := task.Wait(ctx)
				require.NoError(t, err)
				require.NoError(t, task.Kill(ctx, syscall.SIGKILL))
				select {
				case status := <-exit:
					code, _, err := status.Result()
					require.NoError(t, err)
					require.Equal(t, uint32(128+syscall.SIGKILL), code)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				_, err = task.Delete(ctx)
				require.NoError(t, err)
				_, err = control.StopVM(ctx, &proto.StopVMRequest{VMID: id, TimeoutSeconds: 30})
				require.NoError(t, err)
			}
			require.Eventually(t, func() bool {
				_, statErr := os.Stat(directory.RootPath())
				alive, aliveErr := vm.IsProcessAlive(state.VMM)
				return os.IsNotExist(statErr) && aliveErr == nil && !alive
			}, 30*time.Second, 100*time.Millisecond, "orphan VM and jail must be removed")
			require.NoFileExists(t, state.NetNS)
			require.NoDirExists(t, filepath.Join("/var/lib/cni", id), "private CNI cache must be removed")
			require.False(t, cgroupExists(response.CgroupPath), "host jail cgroup must be removed")
			allocations, err := os.ReadDir(filepath.Join("/var/lib/cni/networks", network))
			require.NoError(t, err)
			for _, allocation := range allocations {
				require.Nil(t, net.ParseIP(allocation.Name()), "IPAM allocation leaked")
			}
			if crash {
				// Containerd's dead-shim cleanup publishes task exit and deletes its bundle.
				require.Eventually(t, func() bool {
					_, err := task.Delete(ctx, containerd.WithProcessKill)
					return err == nil || strings.Contains(err.Error(), "not found")
				}, 10*time.Second, 100*time.Millisecond)
			}
			require.NoError(t, container.Delete(ctx, containerd.WithSnapshotCleanup))
			taskBundle := filepath.Join("/run/containerd/io.containerd.runtime.v2.task", defaultNamespace, id)
			require.Eventually(t, func() bool {
				_, err := os.Stat(taskBundle)
				return os.IsNotExist(err)
			}, 10*time.Second, 100*time.Millisecond, "containerd task bundle leaked")
			after, err := net.Interfaces()
			require.NoError(t, err)
			require.Len(t, after, len(before), "host network interfaces leaked")
		})
	}
}
