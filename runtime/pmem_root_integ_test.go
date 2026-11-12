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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func TestPmemRoot_Isolated(t *testing.T) {
	integtest.Prepare(t)
	image, err := filepath.Abs("../tools/image-builder/rootfs-dax.img")
	require.NoError(t, err)
	digest := hashPmemImage(t, image)
	probeDigest := hashPmemImage(t, "../tools/memory-probe/fc-memory-probe")
	for _, pmem := range []bool{false, true} {
		for _, jailed := range []bool{false, true} {
			t.Run(fmt.Sprintf("pmem=%v/jailed=%v", pmem, jailed), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), defaultNamespace), 120*time.Second)
				t.Cleanup(cancel)
				client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(firecrackerRuntime))
				require.NoError(t, err)
				t.Cleanup(func() { _ = client.Close() })
				control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
				require.NoError(t, err)
				t.Cleanup(func() { _ = control.Close() })
				var shared os.FileInfo
				for index := range 2 {
					id := fmt.Sprintf("pmem-%v-%v-%d", pmem, jailed, index)
					request := &proto.CreateVMRequest{VMID: id, ContainerCount: 1,
						RootDrive:  &proto.FirecrackerRootDrive{HostPath: image, UsePmem: pmem, ImageSha256: digest},
						GuestRoot:  &proto.GuestRootStorage{Mode: "disk", SizeMib: 64},
						MachineCfg: &proto.FirecrackerMachineConfiguration{MemSizeMib: 512}}
					if jailed {
						request.JailerConfig = &proto.JailerConfig{UID: 300001, GID: 300001}
					}
					_, err = control.CreateVM(ctx, request)
					require.NoError(t, err)
					t.Cleanup(func() {
						_, err := control.StopVM(context.WithoutCancel(ctx), &proto.StopVMRequest{VMID: id})
						require.NoError(t, err)
					})
					directory, err := vm.ShimDir(integtest.ShimBaseDir(), defaultNamespace, id)
					require.NoError(t, err)
					state, err := vm.ReadRecoveryState(directory)
					require.NoError(t, err)
					cache := filepath.Join(integtest.ShimBaseDir(), ".pmem-images", digest+".img")
					backing, err := os.Stat(cache)
					require.NoError(t, err)
					if shared == nil {
						shared = backing
					} else {
						require.True(t, os.SameFile(shared, backing))
					}
					if jailed {
						exposed, err := os.Stat(fmt.Sprintf("/proc/%d/root/%s.img", state.VMM.PID, digest))
						require.NoError(t, err)
						require.True(t, os.SameFile(backing, exposed), "jails must bind the shared immutable inode")
					}
					if pmem {
						maps, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", state.VMM.PID))
						require.NoError(t, err)
						found := false
						for _, line := range strings.Split(string(maps), "\n") {
							fields := strings.Fields(line)
							if len(fields) < 6 || !strings.HasSuffix(fields[5], digest+".img") {
								continue
							}
							inode, err := strconv.ParseUint(fields[4], 10, 64)
							require.NoError(t, err)
							require.Equal(t, backing.Sys().(*syscall.Stat_t).Ino, inode)
							require.NotContains(t, fields[1], "w")
							require.Contains(t, fields[1], "s")
							found = true
						}
						require.True(t, found, "VMM must map the actual shared pmem backing")
					}
					container, err := client.NewContainer(ctx, id+"-root", containerd.WithNewSpec(
						firecrackeroci.WithVMID(id), oci.WithDefaultPathEnv,
						oci.WithMounts([]specs.Mount{{Destination: "/rom", Type: "bind", Source: "/rom", Options: []string{"bind", "ro"}}}),
						oci.WithProcessArgs("/bin/sh", "-ec", `
                            cat /proc/mounts
                            sha256sum /usr/local/bin/fc-memory-probe
                            test ! -e /vm-private-state
                            echo private >/vm-private-state
                            cp /etc/debian_version /version-original
                            echo copyup >>/etc/debian_version
                            cmp /etc/debian_version /version-original && exit 42
                            mv /version-original /renamed-version; rm /renamed-version
                            if echo forbidden >/rom/immutable-write-test; then exit 43; fi
                            /usr/local/bin/fc-memory-probe -file /rom/usr/local/bin/agent -stat-only
                            /usr/local/bin/fc-memory-probe -file /rom/usr/local/bin/agent -file-bytes 8388608 -anon-mib 8 -idle 1s
                            sync
                            test "$(cat /vm-private-state)" = private
                            echo pmem-root-correctness=passed
                        `)))
					require.NoError(t, err)
					t.Cleanup(func() { require.NoError(t, container.Delete(context.WithoutCancel(ctx))) })
					result := runStorageAudit(ctx, t, container, containerd.WithRootFS([]mount.Mount{
						vm.AddLocalMountIdentifier(mount.Mount{Type: "bind", Source: "/", Options: []string{"bind"}})}))
					t.Logf("Guest verification: %s", result)
					require.Contains(t, result, "pmem-root-correctness=passed")
					require.Contains(t, result, probeDigest+"  /usr/local/bin/fc-memory-probe")
					daxFound, complete := false, false
					for _, line := range strings.Split(result, "\n") {
						if !strings.HasPrefix(line, "{") {
							continue
						}
						var record map[string]any
						require.NoError(t, json.Unmarshal([]byte(line), &record))
						if dax, ok := record["statx_dax"].(bool); ok {
							require.Equal(t, pmem, dax, "STATX_ATTR_DAX must match the selected device")
							require.Equal(t, true, record["statx_dax_supported"])
							daxFound = true
						}
						if record["phase"] == "complete" {
							complete = true
						}
					}
					require.True(t, daxFound, "guest probe must report file DAX state")
					require.True(t, complete, "mmap/refault and anonymous canary must complete")
				}
				require.Equal(t, digest, hashPmemImage(t, image), "VM writes must not advance the source image")
			})
		}
	}
}

func hashPmemImage(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	require.NoError(t, err)
	return hex.EncodeToString(hash.Sum(nil))
}
