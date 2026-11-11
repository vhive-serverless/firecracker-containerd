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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/firecracker-microvm/firecracker-containerd/config"
	crimeta "github.com/firecracker-microvm/firecracker-containerd/internal/cri"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPrepareCRISpec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolv.conf")
	require.NoError(t, os.WriteFile(path, []byte("nameserver 192.0.2.1\n"), 0644))
	spec := specs.Spec{
		Annotations: map[string]string{crimeta.ContainerTypeAnnotation: "sandbox", crimeta.SandboxIDAnnotation: "pod-one"},
		Linux: &specs.Linux{Namespaces: []specs.LinuxNamespace{
			{Type: specs.NetworkNamespace, Path: "/host/netns"}, {Type: specs.PIDNamespace},
		}},
		Mounts: []specs.Mount{
			{Type: "bind", Source: path, Destination: "/etc/resolv.conf"},
			{Type: "bind", Source: "/host/shm", Destination: "/dev/shm"},
		},
	}
	data, err := json.Marshal(spec)
	require.NoError(t, err)
	extra := &proto.ExtraData{JsonSpec: data}
	require.NoError(t, prepareCRISpec(extra))
	spec = specs.Spec{}
	require.NoError(t, json.Unmarshal(extra.JsonSpec, &spec))
	require.Equal(t, []specs.LinuxNamespace{{Type: specs.PIDNamespace}}, spec.Linux.Namespaces)
	require.Equal(t, "/run/firecracker-pods/pod-one/shm", spec.Mounts[1].Source)
	require.Len(t, extra.CriFiles, 1)
	require.Equal(t, "nameserver 192.0.2.1\n", string(extra.CriFiles[0].Contents))
	spec.Mounts = []specs.Mount{{Type: "bind", Source: "/host/data", Destination: "/data"}}
	extra.JsonSpec, err = json.Marshal(spec)
	require.NoError(t, err)
	require.Equal(t, codes.Unimplemented, status.Code(prepareCRISpec(extra)))
}

func TestCRISpecLeavesOrdinaryOCIUnchanged(t *testing.T) {
	data := []byte(`{"linux":{"namespaces":[{"type":"network","path":"/host/netns"}]}}`)
	extra := &proto.ExtraData{JsonSpec: data}
	require.NoError(t, prepareCRISpec(extra))
	require.Equal(t, data, extra.JsonSpec)
}

func TestCRIRejectsHostNamespaces(t *testing.T) {
	for _, missing := range []specs.LinuxNamespaceType{specs.NetworkNamespace, specs.PIDNamespace, specs.IPCNamespace} {
		spec := &specs.Spec{
			Annotations: map[string]string{crimeta.ContainerTypeAnnotation: "sandbox", crimeta.SandboxIDAnnotation: "pod-one"},
			Linux:       &specs.Linux{},
		}
		for _, typ := range []specs.LinuxNamespaceType{specs.NetworkNamespace, specs.PIDNamespace, specs.IPCNamespace} {
			if typ != missing {
				spec.Linux.Namespaces = append(spec.Linux.Namespaces, specs.LinuxNamespace{Type: typ, Path: "/host/netns"})
			}
		}
		_, err := criVMRequest(spec, &config.Config{CRIContainerCount: 8})
		require.Equal(t, codes.Unimplemented, status.Code(err))
	}
}
