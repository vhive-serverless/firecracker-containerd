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

package cri

import (
	"testing"

	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
)

func TestPodIdentity(t *testing.T) {
	for _, typ := range []string{"sandbox", "container"} {
		pod, isCRI, err := Parse(&specs.Spec{Annotations: map[string]string{
			ContainerTypeAnnotation: typ, SandboxIDAnnotation: "pod-one",
		}})
		require.NoError(t, err)
		require.True(t, isCRI)
		require.Equal(t, "pod-one", pod.SandboxID)
		require.Equal(t, typ == "sandbox", pod.Sandbox)
	}
	_, isCRI, err := Parse(&specs.Spec{})
	require.NoError(t, err)
	require.False(t, isCRI)
	for _, annotations := range []map[string]string{
		{ContainerTypeAnnotation: "sandbox"},
		{SandboxIDAnnotation: "pod-one"},
		{ContainerTypeAnnotation: "sandbox", SandboxIDAnnotation: "../escape"},
	} {
		_, isCRI, err := Parse(&specs.Spec{Annotations: annotations})
		require.True(t, isCRI)
		require.Error(t, err)
	}
}
