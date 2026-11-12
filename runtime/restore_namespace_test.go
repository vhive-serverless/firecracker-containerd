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
	"debug/elf"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/firecracker-microvm/firecracker-containerd/internal"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// Run on an isolated privileged worker with CGO_ENABLED=0. It exercises the
// actual self-exec/mount/chroot path with two simultaneous independent roots.
func TestRestoreNamespaceIsolation(t *testing.T) {
	internal.RequiresRoot(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	binary, err := elf.Open(executable)
	require.NoError(t, err)
	dynamic := binary.Section(".interp") != nil
	require.NoError(t, binary.Close())
	if dynamic {
		t.Skip("namespace probe requires a static test binary; run CGO_ENABLED=0")
	}
	formerSource := filepath.Join(t.TempDir(), "deleted-source")
	writableTarget, readonlyTarget := filepath.Join(formerSource, "disk"), filepath.Join(formerSource, "root")
	dynamicTarget := filepath.Join(formerSource, "new-directory", "future-disk")
	type instance struct {
		shared, clone, root, dynamic, content string
		cmd                                   *exec.Cmd
		output                                *bytes.Buffer
	}
	instances := make([]instance, 0, 2)
	for index := 0; index < 2; index++ {
		shared := t.TempDir()
		clone, root := filepath.Join(shared, "clone"), filepath.Join(shared, "readonly")
		future := filepath.Join(t.TempDir(), "future-backing")
		require.NoError(t, os.WriteFile(clone, []byte("captured"), 0600))
		require.NoError(t, os.WriteFile(root, []byte("immutable-root"), 0600))
		require.NoError(t, os.WriteFile(future, []byte("future-content"), 0600))
		cmd, err := restoreNamespaceCommand(context.Background(), shared, executable, []string{"-test.run=^TestRestoreNamespaceProbe$", "-test.v"}, []restoreMount{{Source: clone, Target: writableTarget}, {Source: root, Target: readonlyTarget, ReadOnly: true}}, nil)
		require.NoError(t, err)
		content := fmt.Sprintf("restore-%d", index)
		cmd.Env = append(os.Environ(), "FC_RESTORE_NAMESPACE_PROBE=1", "FC_RESTORE_PROBE_SHARED="+shared, "FC_RESTORE_PROBE_WRITABLE="+writableTarget, "FC_RESTORE_PROBE_READONLY="+readonlyTarget, "FC_RESTORE_PROBE_DYNAMIC="+dynamicTarget, "FC_RESTORE_PROBE_CONTENT="+content)
		output := &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = output, output
		require.NoError(t, cmd.Start())
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		instances = append(instances, instance{shared, clone, root, future, content, cmd, output})
	}
	for _, instance := range instances {
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(instance.shared, "probe.sock")); return err == nil }, 10*time.Second, 10*time.Millisecond)
		process, err := vm.ReadProcessIdentity(instance.cmd.Process.Pid)
		require.NoError(t, err)
		require.NoError(t, addRestoreMount(context.Background(), process, instance.shared, restoreMount{Source: instance.dynamic, Target: dynamicTarget}))
		conn, err := net.DialTimeout("unix", filepath.Join(instance.shared, "probe.sock"), time.Second)
		require.NoError(t, err, "API sockets in the shared directory must be reachable from the host")
		_, err = conn.Write([]byte("continue"))
		require.NoError(t, err)
		require.NoError(t, conn.Close())
	}
	for _, instance := range instances {
		require.NoError(t, instance.cmd.Wait(), "%s", instance.output.String())
		data, err := os.ReadFile(instance.clone)
		require.NoError(t, err)
		require.Equal(t, instance.content, string(data), "each restored VMM must write only its own clone")
		data, err = os.ReadFile(instance.root)
		require.NoError(t, err)
		require.Equal(t, "immutable-root", string(data))
		data, err = os.ReadFile(instance.dynamic)
		require.NoError(t, err)
		require.Equal(t, "updated-"+instance.content, string(data))
	}
	_, err = os.Stat(formerSource)
	require.True(t, os.IsNotExist(err), "source paths must remain absent on the host")
}

func TestRestoreNamespaceProbe(t *testing.T) {
	if os.Getenv("FC_RESTORE_NAMESPACE_PROBE") != "1" {
		t.Skip("private namespace subprocess")
	}
	writable, readonly := os.Getenv("FC_RESTORE_PROBE_WRITABLE"), os.Getenv("FC_RESTORE_PROBE_READONLY")
	data, err := os.ReadFile(writable)
	require.NoError(t, err)
	require.Equal(t, "captured", string(data))
	file, err := os.OpenFile(readonly, os.O_WRONLY, 0)
	if file != nil {
		_ = file.Close()
	}
	require.Error(t, err, "read-only bind must reject writes even as root")
	shared := os.Getenv("FC_RESTORE_PROBE_SHARED")
	listener, err := net.Listen("unix", filepath.Join(shared, "probe.sock"))
	require.NoError(t, err)
	defer listener.Close()
	require.NoError(t, listener.(*net.UnixListener).SetDeadline(time.Now().Add(20*time.Second)))
	conn, err := listener.Accept()
	require.NoError(t, err)
	defer conn.Close()
	token := make([]byte, len("continue"))
	_, err = io.ReadFull(conn, token)
	require.NoError(t, err)
	dynamic := os.Getenv("FC_RESTORE_PROBE_DYNAMIC")
	data, err = os.ReadFile(dynamic)
	require.NoError(t, err)
	require.Equal(t, "future-content", string(data))
	content := os.Getenv("FC_RESTORE_PROBE_CONTENT")
	require.NoError(t, os.WriteFile(writable, []byte(content), 0600))
	require.NoError(t, os.WriteFile(dynamic, []byte("updated-"+content), 0600))
}

// Optional component validation against the checksum-pinned official release;
// configures a disk at an absent original path without starting a guest.
func TestRestoreNamespaceReleaseAPI(t *testing.T) {
	internal.RequiresRoot(t)
	binary := os.Getenv("FC_TEST_BIN")
	if binary == "" {
		t.Skip("set FC_TEST_BIN to the official Firecracker v1.18.0 binary")
	}
	shared := t.TempDir()
	backing := filepath.Join(shared, "backing.img")
	file, err := os.OpenFile(backing, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(1<<20))
	require.NoError(t, file.Close())
	target := filepath.Join(t.TempDir(), "deleted-source", "disk.img")
	cmd, err := restoreNamespaceCommand(context.Background(), shared, binary, []string{"--api-sock", "api.sock"}, []restoreMount{{Source: backing, Target: target, ReadOnly: true}}, nil)
	require.NoError(t, err)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(output.String())
		}
	})
	socket := filepath.Join(shared, "api.sock")
	require.Eventually(t, func() bool { _, err := os.Stat(socket); return err == nil }, 10*time.Second, 10*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	api := fc.NewClient(socket, logrus.NewEntry(logrus.New()), false)
	version, err := api.GetFirecrackerVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, "1.18.0", fc.StringValue(version.Payload.FirecrackerVersion))
	_, err = api.PutGuestDriveByID(ctx, "root", &models.Drive{DriveID: fc.String("root"), PathOnHost: target, IsRootDevice: fc.Bool(true), IsReadOnly: true})
	require.NoError(t, err, "official VMM must open the cloned file at its original private pathname")
	_, err = os.Stat(target)
	require.True(t, os.IsNotExist(err), "official VMM device path must remain absent on host")
}
