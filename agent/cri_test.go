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

	crimeta "github.com/firecracker-microvm/firecracker-containerd/internal/cri"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
)

func TestCRIFilesStayInsideGuestBundle(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "host-file")
	require.NoError(t, os.WriteFile(original, []byte("unchanged"), 0600))
	spec := specs.Spec{
		Annotations: map[string]string{crimeta.ContainerTypeAnnotation: "sandbox", crimeta.SandboxIDAnnotation: "pod-one"},
		Mounts:      []specs.Mount{{Type: "bind", Source: original, Destination: "/etc/resolv.conf"}},
	}
	data, err := json.Marshal(spec)
	require.NoError(t, err)
	bundle := filepath.Join(dir, "bundle")
	updated, err := prepareCRIFiles(data, []*proto.CRIFile{{Source: original, Contents: []byte("guest-config"), Mode: 0644}}, bundle, nil)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(updated, &spec))
	require.Equal(t, filepath.Join(bundle, "cri-files", "0"), spec.Mounts[0].Source)
	contents, err := os.ReadFile(spec.Mounts[0].Source)
	require.NoError(t, err)
	require.Equal(t, "guest-config", string(contents))
	contents, err = os.ReadFile(original)
	require.NoError(t, err)
	require.Equal(t, "unchanged", string(contents))
	_, err = prepareCRIFiles(data, nil, t.TempDir(), nil)
	require.ErrorContains(t, err, "missing CRI generated configuration")
}

func TestCRINetworkSysctlPath(t *testing.T) {
	for _, key := range []string{"net.ipv4.ip_unprivileged_port_start", "net/ipv4/ip_unprivileged_port_start"} {
		path, err := criNetworkSysctlPath(key)
		require.NoError(t, err)
		require.Equal(t, "/proc/sys/net/ipv4/ip_unprivileged_port_start", path)
	}
	for _, key := range []string{"kernel.hostname", "net/../../kernel/hostname", "net..ipv4.key", "net/ipv4/key\x00"} {
		_, err := criNetworkSysctlPath(key)
		require.Error(t, err)
	}
}
