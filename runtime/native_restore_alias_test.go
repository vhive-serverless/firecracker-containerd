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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/log"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	ops "github.com/firecracker-microvm/firecracker-go-sdk/client/operations"
	"github.com/firecracker-microvm/firecracker-go-sdk/fctesting"
	"github.com/stretchr/testify/require"
)

func TestNativeResnapshotOutputAlias(t *testing.T) {
	ctx := context.Background()
	shared, stage := t.TempDir(), t.TempDir()
	j := &noopJailer{shimDir: vm.Dir(shared), logger: log.G(ctx), restoreMounts: []restoreMount{{Target: filepath.Join(shared, "ctrstub0")}}}
	called := false
	machine, err := fc.NewMachine(ctx, fc.Config{}, fc.WithClient(fc.NewClient("/not-used", nil, false, fc.WithOpsClient(&fctesting.MockClient{
		CreateSnapshotFn: func(params *ops.CreateSnapshotParams) (*ops.CreateSnapshotNoContent, error) {
			called = true
			for _, path := range []string{*params.Body.MemFilePath, *params.Body.SnapshotPath} {
				require.True(t, strings.HasPrefix(path, restoreSharedDirectory+"/.native-state-"), "snapshot output API path must use the alias, never the captured disk namespace")
				host := filepath.Join(shared, strings.TrimPrefix(path, restoreSharedDirectory+"/"))
				require.NoError(t, os.WriteFile(host, []byte(filepath.Base(path)), 0600))
			}
			return nil, nil
		},
	}))))
	require.NoError(t, err)
	svc := &service{jailer: j, machine: machine}
	manifest := &nativeCheckpoint{}
	require.NoError(t, svc.nativeVMFiles(ctx, stage, manifest))
	require.True(t, called)
	for _, file := range []snapshotFile{manifest.Memory, manifest.State} {
		data, err := os.ReadFile(filepath.Join(stage, file.Path))
		require.NoError(t, err)
		require.Equal(t, file.Path, string(data))
		digest := sha256.Sum256(data)
		require.Equal(t, snapshotFile{Path: file.Path, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}, file)
	}
}
