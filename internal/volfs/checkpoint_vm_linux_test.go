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

package volfs

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	fcvsock "github.com/firecracker-microvm/firecracker-go-sdk/vsock"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestVolumeCheckpointRealVM(t *testing.T) {
	if os.Getenv("FICD_VOLFS_VM_TEST") != "1" {
		t.Skip("run tools/modernize/test-native-volumes.sh on the worker")
	}
	binary, kernel, initrd := os.Getenv("FC_NATIVE_VMM"), os.Getenv("FC_NATIVE_KERNEL"), os.Getenv("FC_NATIVE_INITRD")
	require.True(t, filepath.IsAbs(binary), "the worker harness must select an absolute VMM path")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	base, err := os.MkdirTemp("", "fc-vol-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	sourceTree := filepath.Join(base, "source-tree")
	require.NoError(t, os.Mkdir(sourceTree, 0700))
	server := NewServer()
	t.Cleanup(func() { _ = server.Close() })
	descriptors := make([]Descriptor, 2)
	for index := range descriptors {
		export, err := OpenExport(sourceTree, false, Limits{})
		require.NoError(t, err)
		descriptors[index], err = server.Register(export)
		require.NoError(t, err)
	}
	serve := func(server *Server, path string) {
		listener, err := ListenHost(path, 0, 0)
		require.NoError(t, err)
		go func() { _ = server.Serve(ctx, listener) }()
	}
	launch := func(name string, cfg fc.Config, opts ...fc.Opt) *fc.Machine {
		cfg.SocketPath = filepath.Join(base, name+".api")
		cfg.VMID = name
		logFile, err := os.Create(filepath.Join(base, name+".log"))
		require.NoError(t, err)
		t.Cleanup(func() {
			if t.Failed() {
				data, _ := os.ReadFile(logFile.Name())
				t.Logf("%s:\n%s", name, data)
			}
			_ = logFile.Close()
		})
		cmd := exec.Command(binary, "--api-sock", cfg.SocketPath, "--id", name, "--no-seccomp") // #nosec G702 -- Opt-in worker test intentionally executes the harness-selected pinned VMM.
		cmd.Stdout, cmd.Stderr = logFile, logFile
		logger := logrus.New()
		logger.Out = logFile
		opts = append(opts, fc.WithProcessRunner(cmd), fc.WithLogger(logrus.NewEntry(logger)))
		machine, err := fc.NewMachine(ctx, cfg, opts...)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, machine.StopVMM()) })
		require.NoError(t, machine.Start(ctx))
		return machine
	}
	sourceVsock := filepath.Join(base, "source.vsock")
	serve(server, sourceVsock)
	source := launch("volume-source", fc.Config{KernelImagePath: kernel, InitrdPath: initrd,
		KernelArgs:   "console=ttyS0 panic=1 reboot=k init=/init volfs.tokens=" + descriptors[0].Token + "," + descriptors[1].Token,
		MachineCfg:   models.MachineConfiguration{VcpuCount: fc.Int64(1), MemSizeMib: fc.Int64(256)},
		VsockDevices: []fc.VsockDevice{{ID: "volume", CID: 3, Path: sourceVsock}}})
	connection, err := fcvsock.DialContext(ctx, sourceVsock, 1234)
	require.NoError(t, err)
	defer connection.Close()
	call := func(conn net.Conn, op string) vmVolumeReply {
		require.NoError(t, conn.SetDeadline(time.Now().Add(15*time.Second)))
		require.NoError(t, json.NewEncoder(conn).Encode(op))
		var reply vmVolumeReply
		require.NoError(t, json.NewDecoder(conn).Decode(&reply))
		require.Empty(t, reply.Error)
		return reply
	}
	initial := call(connection, "init")
	require.Equal(t, 41, initial.Counter)
	require.Equal(t, "41", initial.State)
	require.Equal(t, "41", initial.Shared)
	require.Equal(t, "orphan41", initial.Orphan)
	require.NoError(t, source.PauseVM(ctx))
	frozen, err := server.Freeze(ctx)
	require.NoError(t, err)
	artifact := filepath.Join(base, "volumes")
	err = frozen.Save(ctx, artifact, CheckpointLimits{})
	if err != nil {
		_ = frozen.Close()
		require.NoError(t, err)
	}
	memory, state := filepath.Join(base, "memory"), filepath.Join(base, "state")
	err = source.CreateSnapshot(ctx, memory, state, fc.WithRetainedSourceVsock(true))
	require.NoError(t, frozen.Close())
	require.NoError(t, err)
	require.NoError(t, source.ResumeVM(ctx))
	advanced := call(connection, "advance")
	require.Equal(t, 42, advanced.Counter)
	require.Equal(t, "42", advanced.Shared)
	require.Equal(t, "orphan42", advanced.Orphan)
	oldPID, err := source.PID()
	require.NoError(t, err)
	require.NoError(t, source.StopVMM())
	_, err = os.Stat(filepath.Join("/proc", strconv.Itoa(oldPID)))
	require.True(t, os.IsNotExist(err))
	require.NoError(t, server.Close())
	require.NoError(t, os.RemoveAll(sourceTree))
	restoredServer, err := RestoreServer(ctx, artifact, filepath.Join(base, "restored-volumes"), CheckpointLimits{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = restoredServer.Close() })
	restoredVsock := filepath.Join(base, "restored.vsock")
	serve(restoredServer, restoredVsock)
	restored := launch("volume-restored", fc.Config{}, fc.WithSnapshot(memory, state,
		fc.WithSnapshotVsockReset(true), func(cfg *fc.SnapshotConfig) {
			cfg.VsockOverride = &models.VsockOverride{UdsPath: fc.String(restoredVsock)}
		}))
	newPID, err := restored.PID()
	require.NoError(t, err)
	require.NotEqual(t, oldPID, newPID)
	require.NoError(t, restored.ResumeVM(ctx))
	reconnected, err := fcvsock.DialContext(ctx, restoredVsock, 1234)
	require.NoError(t, err)
	defer reconnected.Close()
	reply := call(reconnected, "get")
	require.Equal(t, initial, reply, "captured guest memory, FUSE inode IDs, shared data and open unlinked file must survive a new host server")
	reply = call(reconnected, "advance")
	require.Equal(t, 42, reply.Counter)
	require.Equal(t, "42", reply.Shared)
	require.Equal(t, "orphan42", reply.Orphan)
	t.Logf("source_pid=%d restored_pid=%d captured_counter=41 restored_counter=%d inode=%d shared=%s orphan=%s", oldPID, newPID, reply.Counter, reply.Inode, reply.Shared, reply.Orphan)
}

type vmVolumeReply struct {
	Counter               int
	State, Shared, Orphan string
	Inode                 uint64
	Error                 string
}
