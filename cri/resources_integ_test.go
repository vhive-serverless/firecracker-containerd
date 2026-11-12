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

package cri

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/firecracker-microvm/firecracker-containerd/config"
	"github.com/firecracker-microvm/firecracker-containerd/internal"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// This test is run by the worker driver in its private CRI daemon and thin pool.
// Normal unit tests never discover or connect to a host CRI service.
func TestPodVMResources(t *testing.T) {
	endpoint := os.Getenv("FICD_CRI_RESOURCE_ENDPOINT")
	if endpoint == "" {
		t.Skip("requires the isolated CRI worker driver")
	}
	const mib = int64(1024 * 1024)
	for _, test := range []struct {
		name         string
		resources    *runtimeapi.LinuxContainerResources
		cpus, memory uint32
	}{
		{name: "declared limits", resources: &runtimeapi.LinuxContainerResources{
			CpuPeriod: 100000, CpuQuota: 150000, CpuShares: 1536, MemoryLimitInBytes: 256 * mib,
		}, cpus: 2, memory: 384},
		{name: "unspecified CPU period", resources: &runtimeapi.LinuxContainerResources{
			CpuQuota: 150000, MemoryLimitInBytes: 128 * mib,
		}, cpus: 2, memory: 256},
		{name: "unbounded uses requests and fallback", resources: &runtimeapi.LinuxContainerResources{
			CpuPeriod: 100000, CpuQuota: -1, CpuShares: 2048,
		}, cpus: 2, memory: 512},
	} {
		t.Run(test.name, func(t *testing.T) {
			testPodVMResources(t, endpoint, test.resources, test.cpus, test.memory)
		})
	}
}

func testPodVMResources(t *testing.T, endpoint string, resources *runtimeapi.LinuxContainerResources, expectedCPUs, expectedMemory uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	connection, err := grpc.NewClient((&url.URL{Scheme: "unix", Path: endpoint}).String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	runtime := runtimeapi.NewRuntimeServiceClient(connection)
	images := runtimeapi.NewImageServiceClient(connection)
	const mib = int64(1024 * 1024)
	podConfig := &runtimeapi.PodSandboxConfig{
		Metadata: &runtimeapi.PodSandboxMetadata{
			Name: "vm-resources", Namespace: "fcmod-tests", Uid: fmt.Sprintf("resources-%d", time.Now().UnixNano()),
		},
		LogDirectory: t.TempDir(),
		Annotations:  map[string]string{"example.test/metadata": "preserved"},
		Linux: &runtimeapi.LinuxPodSandboxConfig{
			Resources: resources,
			SecurityContext: &runtimeapi.LinuxSandboxSecurityContext{NamespaceOptions: &runtimeapi.NamespaceOption{
				Network: runtimeapi.NamespaceMode_POD, Pid: runtimeapi.NamespaceMode_CONTAINER, Ipc: runtimeapi.NamespaceMode_POD,
			}},
		},
	}
	pod, err := runtime.RunPodSandbox(ctx, &runtimeapi.RunPodSandboxRequest{Config: podConfig, RuntimeHandler: "firecracker"})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stop()
		_, err := runtime.StopPodSandbox(cleanup, &runtimeapi.StopPodSandboxRequest{PodSandboxId: pod.PodSandboxId})
		require.NoError(t, err)
		_, err = runtime.RemovePodSandbox(cleanup, &runtimeapi.RemovePodSandboxRequest{PodSandboxId: pod.PodSandboxId})
		require.NoError(t, err)
	})
	state, err := runtime.PodSandboxStatus(ctx, &runtimeapi.PodSandboxStatusRequest{PodSandboxId: pod.PodSandboxId})
	require.NoError(t, err)
	require.Equal(t, podConfig.Annotations, state.Status.Annotations)

	cfg, err := config.LoadConfig("")
	require.NoError(t, err)
	directory, err := vm.ShimDir(cfg.ShimBaseDir, criNamespace, pod.PodSandboxId)
	require.NoError(t, err)
	root, err := os.Open(directory.RootPath())
	require.NoError(t, err)
	defer root.Close()
	socket := filepath.Join("/proc/self/fd", strconv.FormatUint(uint64(root.Fd()), 10), internal.FirecrackerSockName)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://vmm/machine-config", nil)
	require.NoError(t, err)
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var machine struct {
		CPUs   uint32 `json:"vcpu_count"`
		Memory uint32 `json:"mem_size_mib"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&machine))
	require.EqualValues(t, expectedCPUs, machine.CPUs)
	require.EqualValues(t, expectedMemory, machine.Memory)
	t.Logf("actual VMM capacity: %d vCPUs, %d MiB", machine.CPUs, machine.Memory)

	image := &runtimeapi.ImageSpec{Image: "registry.k8s.io/e2e-test-images/busybox:1.29-2@sha256:c318242786b139d18676b1c09a0ad7f15fc17f8f16a5b2e625cd0dc8c9703daf"}
	_, err = images.PullImage(ctx, &runtimeapi.PullImageRequest{Image: image, SandboxConfig: podConfig})
	require.NoError(t, err)
	containerMemory := resources.MemoryLimitInBytes
	if containerMemory <= 0 {
		containerMemory = 128 * mib
	}
	container, err := runtime.CreateContainer(ctx, &runtimeapi.CreateContainerRequest{
		PodSandboxId: pod.PodSandboxId, SandboxConfig: podConfig,
		Config: &runtimeapi.ContainerConfig{
			Metadata: &runtimeapi.ContainerMetadata{Name: "resource-check"}, Image: image,
			Command: []string{"/bin/sh", "-ec", "sleep 600"}, LogPath: "resource-check.log",
			Linux: &runtimeapi.LinuxContainerConfig{Resources: &runtimeapi.LinuxContainerResources{MemoryLimitInBytes: containerMemory}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stop()
		_, err := runtime.StopContainer(cleanup, &runtimeapi.StopContainerRequest{ContainerId: container.ContainerId})
		require.NoError(t, err)
		_, err = runtime.RemoveContainer(cleanup, &runtimeapi.RemoveContainerRequest{ContainerId: container.ContainerId})
		require.NoError(t, err)
	})
	_, err = runtime.StartContainer(ctx, &runtimeapi.StartContainerRequest{ContainerId: container.ContainerId})
	require.NoError(t, err)
	exec, err := runtime.ExecSync(ctx, &runtimeapi.ExecSyncRequest{
		ContainerId: container.ContainerId, Timeout: 10,
		Cmd: []string{"/bin/sh", "-ec", "grep -c '^processor' /proc/cpuinfo; awk '/^MemTotal:/{print $2}' /proc/meminfo"},
	})
	require.NoError(t, err)
	require.Zero(t, exec.ExitCode, string(exec.Stderr))
	values := strings.Fields(string(exec.Stdout))
	require.Len(t, values, 2)
	require.Equal(t, strconv.FormatUint(uint64(expectedCPUs), 10), values[0])
	memoryKiB, err := strconv.ParseInt(values[1], 10, 64)
	require.NoError(t, err)
	require.Greater(t, memoryKiB, int64(expectedMemory)*1024*3/4)
	require.Less(t, memoryKiB, int64(expectedMemory)*1024)
	t.Logf("guest reports %s processors and %d KiB MemTotal", values[0], memoryKiB)
}
