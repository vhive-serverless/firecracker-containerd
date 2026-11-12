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

	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCRIVolumePolicy(t *testing.T) {
	readOnly, recursive, err := criVolumePolicy(specs.Mount{Source: "/data", Options: []string{"rbind", "rprivate", "ro"}})
	require.NoError(t, err)
	require.True(t, readOnly)
	require.True(t, recursive)
	readOnly, recursive, err = criVolumePolicy(specs.Mount{Source: "/data", Options: []string{"rbind", "bind", "ro", "rw"}})
	require.NoError(t, err)
	require.False(t, readOnly)
	require.False(t, recursive)
	_, _, err = criVolumePolicy(specs.Mount{Source: "relative"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	for _, propagation := range []string{"rshared", "rslave"} {
		_, _, err = criVolumePolicy(specs.Mount{Source: "/data", Options: []string{propagation}})
		require.Equal(t, codes.Unimplemented, status.Code(err))
	}
	_, _, err = criVolumePolicy(specs.Mount{Source: "/data", UIDMappings: []specs.LinuxIDMapping{{ContainerID: 0, HostID: 1000, Size: 1}}})
	require.Equal(t, codes.Unimplemented, status.Code(err))
}
