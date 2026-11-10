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
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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
	require.ErrorContains(t, killOwnedProcess(stale), "reused PID")
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
