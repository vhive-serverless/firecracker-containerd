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
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type snapshotFile struct {
	Path, SHA256 string
	Size         int64
}

func copySnapshotFile(ctx context.Context, source, directory, name string) (_ snapshotFile, err error) {
	input, err := os.OpenFile(source, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return snapshotFile{}, err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return snapshotFile{}, err
	}
	if !info.Mode().IsRegular() && (info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice != 0) {
		return snapshotFile{}, unix.EINVAL
	}
	size, err := input.Seek(0, io.SeekEnd)
	if err != nil {
		return snapshotFile{}, err
	}
	if size < 0 || size > 1<<40 {
		return snapshotFile{}, unix.EFBIG
	}
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return snapshotFile{}, err
	}
	output, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return snapshotFile{}, err
	}
	defer func() { err = errors.Join(err, output.Close()) }()
	hash := sha256.New()
	buffer := make([]byte, 1<<20)
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return snapshotFile{}, err
		}
		chunk := min(int64(len(buffer)), size-offset)
		if _, err := io.ReadFull(input, buffer[:chunk]); err != nil {
			return snapshotFile{}, err
		}
		_, _ = hash.Write(buffer[:chunk])
		if !allZeroBytes(buffer[:chunk]) {
			if _, err := output.WriteAt(buffer[:chunk], offset); err != nil {
				return snapshotFile{}, err
			}
		}
		offset += chunk
	}
	if err := output.Truncate(size); err != nil {
		return snapshotFile{}, err
	}
	if err := output.Sync(); err != nil {
		return snapshotFile{}, err
	}
	return snapshotFile{Path: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}, nil
}

func allZeroBytes(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

func syncSnapshotDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
