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
	"path/filepath"
	"testing"

	"github.com/firecracker-microvm/firecracker-containerd/proto"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/stretchr/testify/require"
)

func TestBalloonBootOptionsForNativeRestore(t *testing.T) {
	for _, test := range []struct {
		name                          string
		native, snapshot, wantBalloon bool
	}{
		{name: "cold-boot", wantBalloon: true},
		{name: "native-snapshot", native: true, snapshot: true},
		{name: "legacy-vhive-unchanged", snapshot: true, wantBalloon: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := &service{}
			dir := t.TempDir()
			memory, state := filepath.Join(dir, "memory"), filepath.Join(dir, "state")
			if test.native {
				svc.nativeRestore = &nativeRestore{files: map[string]string{"memory": memory, "state": state}}
			}
			balloon := fc.NewBalloonDevice(0, false, fc.WithStatsPollingIntervals(1)).Build()
			balloon.FreePageReporting = true
			opts, err := svc.buildBalloonDeviceOpt(balloon)
			require.NoError(t, err)
			// Match runtime option ordering: install the custom balloon handler
			// first, then apply the SDK snapshot option and its handler removals.
			cfg := fc.Config{VMID: test.name, SocketPath: filepath.Join(dir, "api.sock")}
			if test.native {
				opts = append(opts, svc.nativeLoadOpt())
			} else if test.snapshot {
				cfg.Drives = []models.Drive{{DriveID: fc.String("root_drive"), PathOnHost: filepath.Join(dir, "root.img"), IsReadOnly: true}}
				cfg.VsockDevices = []fc.VsockDevice{{Path: filepath.Join(dir, "vsock")}}
				opts = append(opts, snapshotRestoreOpt(&cfg, &proto.CreateVMRequest{
					LoadSnapshot: true, MemFilePath: memory, SnapshotPath: state, ContainerSnapshotPath: filepath.Join(dir, "workload.img"),
				}))
			}
			// NewMachine composes the real SDK handlers but does not start a
			// process or connect to an API; Machine.Start would do that.
			machine, err := fc.NewMachine(context.Background(), cfg, opts...)
			require.NoError(t, err)
			require.Equal(t, test.snapshot, machine.Handlers.FcInit.Has(fc.LoadSnapshotHandlerName))
			require.Equal(t, test.wantBalloon, machine.Handlers.FcInit.Has(fc.CreateBalloonHandlerName),
				"native restore must load the saved balloon without PUT /balloon before snapshot load")
			if test.native {
				require.Equal(t, memory, machine.Cfg.Snapshot.MemFilePath)
				require.Equal(t, state, machine.Cfg.Snapshot.SnapshotPath)
			}
		})
	}
}
