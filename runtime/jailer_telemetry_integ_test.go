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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/firecracker-microvm/firecracker-containerd/internal/integtest"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/stretchr/testify/require"
)

func TestJailerTelemetryCrossFilesystem_Isolated(t *testing.T) {
	integtest.Prepare(t)
	ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), defaultNamespace), 60*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("/dev/shm", "fc-jailer-telemetry-")
	require.NoError(t, err)
	defer os.RemoveAll(root)
	control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
	require.NoError(t, err)
	defer control.Close()
	const id = "jailer-telemetry-cross-filesystem"
	logs, metrics := filepath.Join(root, "logs.fifo"), filepath.Join(root, "metrics.fifo")
	telemetry := watchVMFifos(ctx, logs, metrics)
	_, createErr := control.CreateVM(ctx, &proto.CreateVMRequest{VMID: id, LogFifoPath: logs, MetricsFifoPath: metrics,
		JailerConfig: &proto.JailerConfig{UID: 300001, GID: 300001}})
	if createErr == nil {
		defer func() {
			_, err := control.StopVM(context.WithoutCancel(ctx), &proto.StopVMRequest{VMID: id})
			require.NoError(t, err)
		}()
	}
	require.NoError(t, createErr)
	require.NoError(t, telemetry.Wait(), "both streams must reach their original cross-filesystem readers")
	t.Log("jailed VM started; external log and metric FIFOs both delivered telemetry")
}
