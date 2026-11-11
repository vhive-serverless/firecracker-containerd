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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	crimeta "github.com/firecracker-microvm/firecracker-containerd/internal/cri"
	"github.com/firecracker-microvm/firecracker-containerd/internal/volfs"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/opencontainers/runtime-spec/specs-go"
)

func validateCRIVolumes(spec *specs.Spec, volumes []*proto.CRIVolume) error {
	if len(volumes) == 0 {
		return nil
	}
	if len(volumes) > 64 {
		return fmt.Errorf("too many guest CRI volumes")
	}
	_, isCRI, err := crimeta.Parse(spec)
	if err != nil {
		return err
	}
	if !isCRI {
		return fmt.Errorf("volume exports require a CRI task")
	}
	if spec.Linux != nil && (len(spec.Linux.UIDMappings) > 0 || len(spec.Linux.GIDMappings) > 0) {
		return fmt.Errorf("CRI volume user namespace ID mapping is not implemented")
	}
	seen := make(map[uint32]bool, len(volumes))
	for _, volume := range volumes {
		if volume == nil || uint64(volume.MountIndex) >= uint64(len(spec.Mounts)) || seen[volume.MountIndex] {
			return fmt.Errorf("invalid or duplicate CRI volume mount index")
		}
		seen[volume.MountIndex] = true
		mount := spec.Mounts[volume.MountIndex]
		if mount.Type != "bind" {
			return fmt.Errorf("CRI volume export requires a bind mount")
		}
		switch mount.Destination {
		case "/dev/shm", "/etc/hosts", "/etc/hostname", "/etc/resolv.conf":
			return fmt.Errorf("CRI volume export cannot replace a generated mount")
		}
	}
	return nil
}

func (ts *TaskService) prepareCRIVolumes(ctx context.Context, taskExecID string, specData []byte, volumes []*proto.CRIVolume, bundlePath string) (map[int]string, error) {
	if len(volumes) == 0 {
		return nil, nil
	}
	var spec specs.Spec
	if err := json.Unmarshal(specData, &spec); err != nil {
		return nil, err
	}
	if err := validateCRIVolumes(&spec, volumes); err != nil {
		return nil, err
	}
	directory := filepath.Join(bundlePath, "cri-volumes")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	sources := make(map[int]string, len(volumes))
	for _, volume := range volumes {
		path := filepath.Join(directory, strconv.FormatUint(uint64(volume.MountIndex), 10))
		if err := os.Mkdir(path, 0755); err != nil {
			return nil, err
		}
		mount, err := volfs.MountGuest(ctx, ts.shimCtx, path, volfs.Descriptor{Token: volume.Token, FileOnly: volume.FileOnly, ReadOnly: volume.ReadOnly}, volfs.DialHost)
		if err != nil {
			return nil, fmt.Errorf("mount CRI volume %d: %w", volume.MountIndex, err)
		}
		ts.addCleanup(taskExecID, mount.Close)
		sources[int(volume.MountIndex)] = mount.Source()
	}
	return sources, nil
}
