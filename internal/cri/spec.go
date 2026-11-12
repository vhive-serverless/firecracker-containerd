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

// Package cri contains the OCI metadata shared by the host shim and guest agent.
package cri

import (
	"fmt"
	"path/filepath"

	"github.com/containerd/containerd/v2/pkg/identifiers"
	"github.com/opencontainers/runtime-spec/specs-go"
)

const (
	// ContainerTypeAnnotation distinguishes CRI sandboxes from workload containers.
	ContainerTypeAnnotation = "io.kubernetes.cri.container-type"
	// SandboxIDAnnotation groups every container of a Pod into the same VM.
	SandboxIDAnnotation = "io.kubernetes.cri.sandbox-id"
)

// Pod identifies the CRI sandbox owning an OCI container.
type Pod struct {
	SandboxID string
	Sandbox   bool
}

// Parse reads the annotations emitted by containerd's CRI implementation.
// Incomplete CRI metadata is rejected rather than creating an unrelated VM.
func Parse(spec *specs.Spec) (Pod, bool, error) {
	typ := spec.Annotations[ContainerTypeAnnotation]
	id := spec.Annotations[SandboxIDAnnotation]
	if typ == "" && id == "" {
		return Pod{}, false, nil
	}
	if typ != "sandbox" && typ != "container" {
		return Pod{}, true, fmt.Errorf("invalid CRI container type %q", typ)
	}
	if err := identifiers.Validate(id); err != nil {
		return Pod{}, true, fmt.Errorf("invalid CRI sandbox ID: %w", err)
	}
	return Pod{SandboxID: id, Sandbox: typ == "sandbox"}, true, nil
}

// ShmPath is private to the Pod's VM and shared by its guest containers.
func (p Pod) ShmPath() string {
	return filepath.Join("/run/firecracker-pods", p.SandboxID, "shm")
}
