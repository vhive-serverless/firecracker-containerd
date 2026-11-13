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

	"github.com/containerd/containerd/v2/pkg/protobuf/types"
	"github.com/containerd/log"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	drivemount "github.com/firecracker-microvm/firecracker-containerd/proto/service/drivemount/ttrpc"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	ops "github.com/firecracker-microvm/firecracker-go-sdk/client/operations"
	"github.com/firecracker-microvm/firecracker-go-sdk/fctesting"
	"github.com/stretchr/testify/require"
)

type resetDriveMounter struct{ drivemount.DriveMounterService }

func (resetDriveMounter) UnmountDrive(context.Context, *drivemount.UnmountDriveRequest) (*types.Empty, error) {
	return &types.Empty{}, nil
}

func TestNoopResetStubRecapture(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "same-ID-restored"}[restored], func(t *testing.T) {
			ctx := context.Background()
			shared := t.TempDir()
			j := &noopJailer{shimDir: vm.Dir(shared), ctx: ctx, logger: log.G(ctx)}
			cfg := &fc.Config{}
			handler, err := CreateContainerStubs(cfg, j, 1, log.G(ctx))
			require.NoError(t, err)
			stub := handler.freeDrives[0]
			if restored {
				// An unused stub in a same-ID snapshot has this exact captured
				// target, backed by the new restore's independent copy.
				data, err := os.ReadFile(stub.stubPath)
				require.NoError(t, err)
				clone := filepath.Join(shared, "captured-stub")
				// #nosec G703 -- clone is the test-owned captured stub in t.TempDir.
				require.NoError(t, os.WriteFile(clone, data, 0600))
				j.restoreMounts = []restoreMount{{Source: clone, Target: stub.stubPath}}
			}
			var exportedPath string
			machine, err := fc.NewMachine(ctx, fc.Config{}, fc.WithClient(fc.NewClient("/not-used", nil, false, fc.WithOpsClient(&fctesting.MockClient{
				PatchGuestDriveByIDFn: func(params *ops.PatchGuestDriveByIDParams) (*ops.PatchGuestDriveByIDNoContent, error) {
					exportedPath = params.Body.PathOnHost
					return nil, nil
				},
			}))))
			require.NoError(t, err)
			for attempt := 0; attempt < 2; attempt++ {
				// Reserve's external-drive patch does not replace the captured
				// stub mapping. Exercise repeated reset of that same slot.
				handler.usedDrives["task"] = stub
				handler.freeDrives = nil
				require.NoError(t, handler.Release(ctx, "task", resetDriveMounter{}, machine))
				require.Equal(t, stub.stubPath, exportedPath)
				require.True(t, filepath.IsAbs(exportedPath))
				metadata, err := snapshotDrivePaths(&models.FullVMConfiguration{Drives: []*models.Drive{{DriveID: fc.String(stub.driveID), PathOnHost: exportedPath}}}, false)
				require.NoError(t, err, "release must leave a capturable absolute unjailed path")
				require.Equal(t, stub.stubPath, metadata.Drives[0].Path)
				if restored {
					backing, ok := restoreBackingPath(j, exportedPath)
					require.True(t, ok)
					require.Equal(t, filepath.Join(shared, "captured-stub"), backing)
				}
			}
			if restored {
				clone := j.restoreMounts[0].Source
				data, err := os.ReadFile(clone)
				require.NoError(t, err)
				data[len(data)-1] = 1 // Keep the header; corrupt only padding.
				// #nosec G703 -- clone is the test-owned captured stub in t.TempDir.
				require.NoError(t, os.WriteFile(clone, data, 0600))
				require.ErrorContains(t, j.exposeStubDrive(stub.stubPath, stub.driveID), "does not match")
				require.Error(t, j.exposeStubDrive(stub.stubPath, "wrong-slot"))
				require.ErrorContains(t, j.ExposeFileToJail(stub.stubPath), "overlaps captured backing", "generic exposure must not acquire the stub exception")
			}
		})
	}
}
