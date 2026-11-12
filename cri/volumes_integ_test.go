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
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// The driver runs this against its exclusive containerd daemon and thin pool.
// All host paths refer to that test container, never a host CRI service.
func TestPodVolumes(t *testing.T) {
	endpoint := os.Getenv("FICD_CRI_RESOURCE_ENDPOINT")
	if endpoint == "" {
		t.Skip("requires the isolated CRI worker driver")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	t.Cleanup(cancel)
	connection, err := grpc.NewClient((&url.URL{Scheme: "unix", Path: endpoint}).String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	runtime := runtimeapi.NewRuntimeServiceClient(connection)
	images := runtimeapi.NewImageServiceClient(connection)
	podConfig := &runtimeapi.PodSandboxConfig{Metadata: &runtimeapi.PodSandboxMetadata{
		Name: "volumes", Namespace: "fcmod-tests", Uid: fmt.Sprintf("volumes-%d", time.Now().UnixNano()),
	}, LogDirectory: t.TempDir(), Linux: &runtimeapi.LinuxPodSandboxConfig{SecurityContext: &runtimeapi.LinuxSandboxSecurityContext{
		NamespaceOptions: &runtimeapi.NamespaceOption{Network: runtimeapi.NamespaceMode_POD, Pid: runtimeapi.NamespaceMode_CONTAINER, Ipc: runtimeapi.NamespaceMode_POD},
	}}}
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
	image := &runtimeapi.ImageSpec{Image: "registry.k8s.io/e2e-test-images/busybox:1.29-2@sha256:c318242786b139d18676b1c09a0ad7f15fc17f8f16a5b2e625cd0dc8c9703daf"}
	_, err = images.PullImage(ctx, &runtimeapi.PullImageRequest{Image: image, SandboxConfig: podConfig})
	require.NoError(t, err)
	writable, readonly := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(writable, "seed"), []byte("host-initial"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(readonly, "seed"), []byte("readonly"), 0600))
	link := filepath.Join(t.TempDir(), "source-link")
	require.NoError(t, os.Symlink(writable, link))
	termination := filepath.Join(t.TempDir(), "termination-log")
	require.NoError(t, os.WriteFile(termination, nil, 0600))
	mounts := []*runtimeapi.Mount{
		{ContainerPath: "/data", HostPath: link},
		{ContainerPath: "/readonly", HostPath: readonly, Readonly: true},
		{ContainerPath: "/dev/termination-log", HostPath: termination},
	}
	create := func(name, cwd string) string {
		created, err := runtime.CreateContainer(ctx, &runtimeapi.CreateContainerRequest{PodSandboxId: pod.PodSandboxId, SandboxConfig: podConfig,
			Config: &runtimeapi.ContainerConfig{Metadata: &runtimeapi.ContainerMetadata{Name: name}, Image: image,
				Command: []string{"/bin/sh", "-ec", "sleep 600"}, WorkingDir: cwd, Mounts: mounts, LogPath: name + ".log"},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer stop()
			_, err := runtime.StopContainer(cleanup, &runtimeapi.StopContainerRequest{ContainerId: created.ContainerId})
			require.NoError(t, err)
			_, err = runtime.RemoveContainer(cleanup, &runtimeapi.RemoveContainerRequest{ContainerId: created.ContainerId})
			require.NoError(t, err)
		})
		return created.ContainerId
	}
	container := create("volume-check", "/")
	_, err = runtime.StartContainer(ctx, &runtimeapi.StartContainerRequest{ContainerId: container})
	require.NoError(t, err)
	exec := func(command string) string {
		reply, err := runtime.ExecSync(ctx, &runtimeapi.ExecSyncRequest{ContainerId: container, Timeout: 10, Cmd: []string{"/bin/sh", "-ec", command}})
		require.NoError(t, err)
		require.Zero(t, reply.ExitCode, string(reply.Stderr))
		return string(reply.Stdout)
	}
	require.Equal(t, "host-initial", exec("cat /data/seed"))
	require.NoError(t, os.WriteFile(filepath.Join(writable, "seed"), []byte("host-updated"), 0600))
	require.Equal(t, "host-updated", exec("cat /data/seed"))
	info := exec("printf guest > /data/from-guest; printf terminated > /dev/termination-log; test \"$(cat /readonly/seed)\" = readonly; if touch /readonly/forbidden; then exit 77; fi; grep 'fuse.fcvolume' /proc/self/mountinfo")
	require.Contains(t, info, " /data ")
	require.Contains(t, info, " /readonly ")
	t.Logf("actual guest FUSE bind mounts:\n%s", info)
	data, err := os.ReadFile(filepath.Join(writable, "from-guest"))
	require.NoError(t, err)
	require.Equal(t, "guest", string(data))
	data, err = os.ReadFile(termination)
	require.NoError(t, err)
	require.Equal(t, "terminated", string(data))
	_, err = os.Stat(filepath.Join(readonly, "forbidden"))
	require.ErrorIs(t, err, os.ErrNotExist)
	exec("exec 3<>/data/from-guest; mv /data/from-guest /data/renamed; ln /data/renamed /data/alias; rm /data/renamed /data/alias; printf alive >&3; test \"$(cat /proc/self/fd/3)\" = alive")
	// Exceed the VM's spare drive slots with failed creates. A leaked guest
	// mount or host drive reservation must not be hidden by replacing the VM:
	// the existing workload must still execute afterwards.
	for attempt := 0; attempt < 10; attempt++ {
		failed := create(fmt.Sprintf("failed-%d", attempt), "relative")
		_, err := runtime.StartContainer(ctx, &runtimeapi.StartContainerRequest{ContainerId: failed})
		require.ErrorContains(t, err, "Cwd must be an absolute path")
		_, err = runtime.RemoveContainer(ctx, &runtimeapi.RemoveContainerRequest{ContainerId: failed})
		require.NoError(t, err)
	}
	require.Equal(t, "host-updated", exec("cat /data/seed"))
}
