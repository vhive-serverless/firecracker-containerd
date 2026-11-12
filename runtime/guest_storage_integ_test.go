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
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/firecracker-microvm/firecracker-containerd/internal/integtest"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
)

func TestGuestDiskStorage_Isolated(t *testing.T) {
	integtest.Prepare(t)
	for _, jailed := range []bool{false, true} {
		t.Run(fmt.Sprintf("jailed=%v", jailed), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			ctx = namespaces.WithNamespace(ctx, defaultNamespace)
			client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(firecrackerRuntime))
			require.NoError(t, err)
			defer client.Close()
			control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
			require.NoError(t, err)
			defer control.Close()
			id := fmt.Sprintf("guest-disk-%v", jailed)
			request := &proto.CreateVMRequest{VMID: id, ContainerCount: 1,
				GuestRoot:  &proto.GuestRootStorage{Mode: "disk", SizeMib: 64},
				MachineCfg: &proto.FirecrackerMachineConfiguration{MemSizeMib: 512}}
			if jailed {
				request.JailerConfig = &proto.JailerConfig{UID: 300001, GID: 300001}
			}
			_, err = control.CreateVM(ctx, request)
			require.NoError(t, err)
			root := filepath.Join(integtest.ShimBaseDir(), defaultNamespace+"#"+id)
			if jailed {
				root = filepath.Join(root, "rootfs")
			}
			disk := filepath.Join(root, guestOverlayFile)
			defer func() {
				_, err := control.StopVM(context.WithoutCancel(ctx), &proto.StopVMRequest{VMID: id})
				require.NoError(t, err)
				require.Eventually(t, func() bool { _, err := os.Stat(disk); return os.IsNotExist(err) }, 5*time.Second, 10*time.Millisecond)
			}()
			info, err := os.Stat(disk)
			require.NoError(t, err)
			require.EqualValues(t, 64<<20, info.Size())
			require.GreaterOrEqual(t, info.Sys().(*syscall.Stat_t).Blocks*512, info.Size())
			if jailed {
				require.EqualValues(t, 300001, info.Sys().(*syscall.Stat_t).Uid)
			}
			output, err := exec.Command("blkid", "-p", disk).CombinedOutput()
			require.NoError(t, err, string(output))
			t.Logf("Host private disk: %s", output)
			container, err := client.NewContainer(ctx, id+"-root", containerd.WithNewSpec(
				firecrackeroci.WithVMID(id), oci.WithDefaultPathEnv,
				oci.WithMounts([]specs.Mount{{Destination: "/overlay", Type: "bind", Source: "/rom/overlay", Options: []string{"bind", "ro"}}}),
				oci.WithProcessArgs("/bin/sh", "-ec", `
                    printf 'guest_upper_magic=%s\n' "$(stat -f -c %t /overlay)"
                    cat /proc/self/mountinfo
                    cat /proc/cmdline
                    test -f /etc/debian_version
                    cp /etc/debian_version /original-version
                    printf 'copy-up\n' >>/etc/debian_version
                    cmp /etc/debian_version /original-version && exit 41
                    mv /original-version /renamed-version
                    test -s /renamed-version
                    rm /renamed-version
                    mkdir /write-test; echo retained >/write-test/state; sync
                    if fill_error=$(dd if=/dev/zero of=/write-test/fill bs=1M count=128 2>&1); then exit 42; fi
                    printf '%s\\n' "$fill_error" | grep -q 'No space left on device'
                    rm /write-test/fill
                    sync
                    test "$(cat /write-test/state)" = retained
                    echo after-reclaim >/write-test/state; sync
                    test "$(cat /write-test/state)" = after-reclaim
                    echo disk-write-copyup-rename-delete-enospc=passed
                `)))
			require.NoError(t, err)
			defer container.Delete(ctx)
			result := runStorageAudit(ctx, t, container, containerd.WithRootFS([]mount.Mount{
				vm.AddLocalMountIdentifier(mount.Mount{Type: "bind", Source: "/", Options: []string{"bind"}})}))
			t.Logf("Guest inspection: %s", result)
			require.Contains(t, result, "guest_upper_magic=ef53")
			require.Contains(t, result, "overlay_root=UUID=")
			require.Contains(t, result, "disk-write-copyup-rename-delete-enospc=passed")
		})
	}
}
