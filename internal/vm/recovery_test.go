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

package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	runc "github.com/containerd/go-runc"
	"github.com/stretchr/testify/require"
)

func recoveryProcess(t *testing.T) (*exec.Cmd, ProcessIdentity) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	identity, err := ReadProcessIdentity(cmd.Process.Pid)
	require.NoError(t, err)
	return cmd, identity
}

func recoveryTestDir(t *testing.T) Dir {
	t.Helper()
	dir, err := ShimDir(t.TempDir(), "test", "recovery")
	require.NoError(t, err)
	require.NoError(t, dir.Mkdir())
	return dir
}

func TestRecoveryRefusesReusedPID(t *testing.T) {
	_, identity := recoveryProcess(t)
	stale := identity
	stale.StartTicks += "0"
	require.ErrorContains(t, StopProcess(stale, syscall.SIGKILL), "reused PID")
	alive, err := IsProcessAlive(identity)
	require.NoError(t, err)
	require.True(t, alive, "unrelated process must survive stale metadata")
}

func TestRecoverVMActiveShimAndCrash(t *testing.T) {
	shim, shimIdentity := recoveryProcess(t)
	_, vmmIdentity := recoveryProcess(t)
	dir := recoveryTestDir(t)
	state := RecoveryState{Version: 1, Namespace: "test", VMID: "recovery", Shim: shimIdentity, VMM: vmmIdentity}
	require.NoError(t, WriteRecoveryState(dir, state))
	require.ErrorContains(t, RecoverVM(context.Background(), dir), "still alive")
	require.FileExists(t, filepath.Join(dir.RootPath(), RecoveryFileName))
	alive, err := IsProcessAlive(vmmIdentity)
	require.NoError(t, err)
	require.True(t, alive)
	require.NoError(t, shim.Process.Kill())
	require.Error(t, shim.Wait())
	require.NoError(t, RecoverVM(context.Background(), dir))
	alive, err = IsProcessAlive(vmmIdentity)
	require.NoError(t, err)
	require.False(t, alive, "orphan VMM must exit")
	require.NoFileExists(t, filepath.Join(dir.RootPath(), RecoveryFileName))
	require.NoError(t, RecoverVM(context.Background(), dir), "cleanup is idempotent")
}

func TestRecoveryRejectsWrongVM(t *testing.T) {
	identity, err := ReadProcessIdentity(os.Getpid())
	require.NoError(t, err)
	dir := recoveryTestDir(t)
	state := RecoveryState{Version: 1, Namespace: "test", VMID: "another", Shim: identity}
	require.NoError(t, WriteRecoveryState(dir, state))
	require.ErrorContains(t, RecoverVM(context.Background(), dir), "does not match")
	require.FileExists(t, filepath.Join(dir.RootPath(), RecoveryFileName))
}

func TestDeleteRuncBundleOwnership(t *testing.T) {
	for _, test := range []struct {
		name, id, bundle string
		wantDelete       bool
		wantError        bool
	}{
		{name: "owned", id: "vm", bundle: "/owned", wantDelete: true},
		{name: "foreign", id: "vm", bundle: "/another", wantError: true},
		{name: "missing", id: "other", bundle: "/owned"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "deleted")
			binary := filepath.Join(dir, "runc")
			listing, err := json.Marshal([]runc.Container{{ID: test.id, Bundle: test.bundle}})
			require.NoError(t, err)
			script := fmt.Sprintf("#!/bin/sh\nfor arg do\nif [ \"$arg\" = list ]; then\ncat <<'JSON'\n%s\nJSON\nexit 0\nfi\ndone\nprintf deleted > '%s'\n", listing, marker)
			require.NoError(t, os.WriteFile(binary, []byte(script), 0700))
			client := runc.Runc{Command: binary}
			err = DeleteRuncBundle(context.Background(), &client, "vm", "/owned")
			if test.wantError {
				require.ErrorContains(t, err, "belongs to another bundle")
			} else {
				require.NoError(t, err)
			}
			if test.wantDelete {
				require.FileExists(t, marker)
			} else {
				require.NoFileExists(t, marker)
			}
		})
	}
}
