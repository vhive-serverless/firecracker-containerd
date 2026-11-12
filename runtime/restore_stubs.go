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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// The retained descriptor pins the creator's inode until jail cleanup, so an
// unlink/replacement cannot regain ownership through inode-number reuse.
type ownedRestoreStub struct {
	file    *os.File
	info    os.FileInfo
	driveID string
}

func (j *runcJailer) recordRestoreStub(file *os.File, driveID string) error {
	if !j.trackRestoreStubs {
		return nil
	}
	target, err := filepath.Rel(j.RootPath(), file.Name())
	if err != nil || target != filepath.Base(target) || target == "." || target == ".." || driveID != stubPathToDriveID(target) {
		return fmt.Errorf("invalid owned restore stub path or drive identity")
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Nlink != 1 || stat.Uid != j.Config.UID || stat.Gid != j.Config.GID {
		return fmt.Errorf("invalid generated restore stub metadata")
	}
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return err
	}
	pinned := os.NewFile(uintptr(fd), file.Name())
	j.restoreStubMu.Lock()
	defer j.restoreStubMu.Unlock()
	if j.restoreStubs == nil {
		j.restoreStubs = make(map[string]ownedRestoreStub)
	}
	if _, exists := j.restoreStubs[target]; exists {
		return errors.Join(fmt.Errorf("restore stub ownership already recorded"), pinned.Close())
	}
	j.restoreStubs[target] = ownedRestoreStub{file: pinned, info: info, driveID: driveID}
	return nil
}

func (j *runcJailer) openOwnedRestoreStub(root *os.Root, target string) (*os.File, error) {
	j.restoreStubMu.Lock()
	defer j.restoreStubMu.Unlock()
	owned, exists := j.restoreStubs[target]
	if !j.trackRestoreStubs || !exists || target != filepath.Base(target) || owned.driveID != stubPathToDriveID(target) {
		return nil, fmt.Errorf("restore target lacks current-launch stub ownership: %w", os.ErrExist)
	}
	file, err := root.OpenFile(target, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil {
		pinned, pinErr := owned.file.Stat()
		stat, ok := info.Sys().(*syscall.Stat_t)
		original, originalOK := owned.info.Sys().(*syscall.Stat_t)
		if pinErr != nil || !ok || !originalOK || !info.Mode().IsRegular() || !os.SameFile(info, owned.info) || !os.SameFile(info, pinned) ||
			info.Mode() != owned.info.Mode() || info.Size() != owned.info.Size() ||
			stat.Nlink != 1 || stat.Uid != original.Uid || stat.Gid != original.Gid ||
			stat.Mtim != original.Mtim || stat.Ctim != original.Ctim {
			err = fmt.Errorf("restore target no longer matches the generated stub inode and metadata")
		}
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func (j *runcJailer) closeRestoreStubs() error {
	j.restoreStubMu.Lock()
	defer j.restoreStubMu.Unlock()
	var result error
	for target, owned := range j.restoreStubs {
		result = errors.Join(result, owned.file.Close())
		delete(j.restoreStubs, target)
	}
	return result
}
