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
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuestOverlayAllocation(t *testing.T) {
	const sizeMiB = 64
	path := filepath.Join(t.TempDir(), guestOverlayFile)
	uuid, err := createGuestOverlay(context.Background(), path, sizeMiB)
	require.NoError(t, err)
	output, err := exec.Command("blkid", "-p", "-s", "UUID", "-o", "value", path).Output()
	require.NoError(t, err)
	require.Equal(t, uuid+"\n", string(output))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.EqualValues(t, sizeMiB<<20, info.Size())
	require.GreaterOrEqual(t, info.Sys().(*syscall.Stat_t).Blocks*512, info.Size(), "full guest capacity must consume reserved host disk blocks")
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	_, err = createGuestOverlay(context.Background(), path, sizeMiB)
	require.ErrorIs(t, err, os.ErrExist, "must never overwrite an existing writable disk")
}

func TestGuestOverlayRejectsRAMAndCleansFailure(t *testing.T) {
	root, err := os.MkdirTemp("/dev/shm", "fc-overlay-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, guestOverlayFile)
	_, err = createGuestOverlay(context.Background(), path, 64)
	require.ErrorContains(t, err, "requires ext4/ext3")
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)

	path = filepath.Join(t.TempDir(), guestOverlayFile)
	_, err = createGuestOverlay(context.Background(), path, 64, func(*os.File) error { return syscall.EPERM })
	require.ErrorIs(t, err, syscall.EPERM)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist, "failure after formatting must release the allocated capacity")
}
