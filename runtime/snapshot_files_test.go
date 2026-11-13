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
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestSnapshotFileCopy(t *testing.T) {
	mixed := make([]byte, 3<<20+7)
	copy(mixed, "memory prefix")
	copy(mixed[len(mixed)-7:], "trailer")
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "empty"},
		{name: "zeroes", data: make([]byte, 2<<20+3)},
		{name: "mixed", data: mixed},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source")
			require.NoError(t, os.WriteFile(source, test.data, 0600))
			destination := t.TempDir()
			saved, err := copySnapshotFile(context.Background(), source, destination, "copy")
			require.NoError(t, err)
			digest := sha256.Sum256(test.data)
			require.Equal(t, snapshotFile{Path: "copy", Size: int64(len(test.data)), SHA256: hex.EncodeToString(digest[:])}, saved)
			copied, err := os.ReadFile(filepath.Join(destination, saved.Path))
			require.NoError(t, err)
			require.Equal(t, string(test.data), string(copied))
			_, err = copySnapshotFile(context.Background(), source, destination, "copy")
			require.ErrorIs(t, err, os.ErrExist, "copy must not replace an existing artifact")
		})
	}
}

func TestSnapshotFileCopyRejectsInvalidSourceAndCancellation(t *testing.T) {
	_, err := copySnapshotFile(context.Background(), t.TempDir(), t.TempDir(), "copy")
	require.ErrorIs(t, err, unix.EINVAL)
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.WriteFile(source, []byte("payload"), 0600))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = copySnapshotFile(ctx, source, t.TempDir(), "copy")
	require.ErrorIs(t, err, context.Canceled)
}

func TestSnapshotFilePublication(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	destination := filepath.Join(directory, "published")
	require.NoError(t, os.WriteFile(source, []byte("snapshot payload"), 0600))
	require.NoError(t, publishSnapshotFile(context.Background(), source, destination))
	published, err := os.ReadFile(destination)
	require.NoError(t, err)
	require.Equal(t, "snapshot payload", string(published))
	require.NoError(t, syncSnapshotDirectory(directory))
	require.ErrorIs(t, syncSnapshotDirectory(filepath.Join(directory, "missing")), os.ErrNotExist)
}
