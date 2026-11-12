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
	"testing"

	crimeta "github.com/firecracker-microvm/firecracker-containerd/internal/cri"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
)

func TestCRIVolumeDescriptorsCannotReplaceOtherMounts(t *testing.T) {
	spec := &specs.Spec{Annotations: map[string]string{crimeta.ContainerTypeAnnotation: "container", crimeta.SandboxIDAnnotation: "pod-one"}, Mounts: []specs.Mount{
		{Type: "bind", Source: "/host/data", Destination: "/data"}, {Type: "bind", Source: "/host/hosts", Destination: "/etc/hosts"},
	}}
	valid := &proto.CRIVolume{MountIndex: 0}
	require.NoError(t, validateCRIVolumes(spec, []*proto.CRIVolume{valid}))
	for _, volumes := range [][]*proto.CRIVolume{{nil}, {valid, valid}, {{MountIndex: 2}}, {{MountIndex: 1}}} {
		require.Error(t, validateCRIVolumes(spec, volumes))
	}
	spec.Annotations = nil
	require.ErrorContains(t, validateCRIVolumes(spec, []*proto.CRIVolume{valid}), "require a CRI task")
}
