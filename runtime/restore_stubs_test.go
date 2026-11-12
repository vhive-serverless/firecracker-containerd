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
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func restoreStubFixture(t *testing.T) (*runcJailer, string, []byte) {
	t.Helper()
	j := &runcJailer{
		Config:            runcJailerConfig{OCIBundlePath: t.TempDir(), UID: uint32(os.Getuid()), GID: uint32(os.Getgid())},
		trackRestoreStubs: true,
	}
	require.NoError(t, os.Mkdir(j.RootPath(), 0700))
	t.Cleanup(func() { require.NoError(t, j.closeRestoreStubs()) })
	path := filepath.Join(j.RootPath(), "ctrstub0")
	stub, err := newStubDrive(path, j, true, nil, logrus.NewEntry(logrus.New()))
	require.NoError(t, err)
	require.Equal(t, stubPathToDriveID("ctrstub0"), stub.driveID)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	clone, err := os.CreateTemp(t.TempDir(), "restored-drive-")
	require.NoError(t, err)
	_, err = clone.Write(data)
	require.NoError(t, err)
	require.NoError(t, clone.Close())
	j.restoreMounts = []restoreMount{{Source: clone.Name(), Target: "/ctrstub0"}}
	return j, path, data
}

func TestRestoreMountReusesOnlyCurrentLaunchStub(t *testing.T) {
	j, path, data := restoreStubFixture(t)
	before, err := os.Stat(path)
	require.NoError(t, err)
	owned := j.restoreStubs["ctrstub0"]
	flags, err := unix.FcntlInt(owned.file.Fd(), unix.F_GETFD, 0)
	require.NoError(t, err)
	require.NotZero(t, flags&unix.FD_CLOEXEC)
	require.NoError(t, j.prepareRestoreMounts())
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	require.Equal(t, before.Mode(), after.Mode())
	current, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, data, current, "mount preparation must not truncate the generated stub")
	require.Len(t, j.configSpec.Mounts, 1)
	require.Equal(t, "/ctrstub0", j.configSpec.Mounts[0].Destination)
	require.Equal(t, j.restoreMounts[0].Source, j.configSpec.Mounts[0].Source)
	require.NoError(t, j.closeRestoreStubs())
	_, err = owned.file.Stat()
	require.ErrorIs(t, err, os.ErrClosed)
	require.Empty(t, j.restoreStubs)
}

func TestRestoreMountRejectsChangedOwnedStub(t *testing.T) {
	for _, change := range []string{"substituted-inode", "symlink", "permissions", "size", "extra-hardlink", "unowned-launch", "wrong-drive-id"} {
		t.Run(change, func(t *testing.T) {
			j, path, data := restoreStubFixture(t)
			switch change {
			case "substituted-inode":
				require.NoError(t, os.Remove(path))
				// Identical header, length and permissions do not grant ownership.
				require.NoError(t, os.WriteFile(path, data, 0600))
			case "symlink":
				require.NoError(t, os.Rename(path, path+"-original"))
				require.NoError(t, os.Symlink(path+"-original", path))
			case "permissions":
				require.NoError(t, os.Chmod(path, 0400))
			case "size":
				require.NoError(t, os.Truncate(path, int64(len(data)+1)))
			case "extra-hardlink":
				require.NoError(t, os.Link(path, filepath.Join(t.TempDir(), "other-link")))
			case "unowned-launch":
				require.NoError(t, j.closeRestoreStubs())
			case "wrong-drive-id":
				owned := j.restoreStubs["ctrstub0"]
				owned.driveID = "different"
				j.restoreStubs["ctrstub0"] = owned
			}
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Error(t, j.prepareRestoreMounts())
			require.Empty(t, j.configSpec.Mounts)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after, "rejected existing targets must remain untouched")
		})
	}
}

func TestRestoreMountKeepsExclusiveCreationForOtherTargets(t *testing.T) {
	j, _, _ := restoreStubFixture(t)
	foreign := filepath.Join(j.RootPath(), "foreign")
	require.NoError(t, os.WriteFile(foreign, []byte("unrelated existing contents"), 0600))
	j.restoreMounts[0].Target = "/foreign"
	require.ErrorIs(t, j.prepareRestoreMounts(), os.ErrExist)
	got, err := os.ReadFile(foreign)
	require.NoError(t, err)
	require.Equal(t, "unrelated existing contents", string(got))
	// Ordinary absent mount targets keep the existing O_EXCL creation path.
	j.restoreMounts[0].Target = "/other-drive"
	require.NoError(t, j.prepareRestoreMounts())
	info, err := os.Stat(filepath.Join(j.RootPath(), "other-drive"))
	require.NoError(t, err)
	require.Zero(t, info.Size())
}

func TestOrdinaryJailerDoesNotRetainStubDescriptors(t *testing.T) {
	j := &runcJailer{Config: runcJailerConfig{OCIBundlePath: t.TempDir(), UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}}
	require.NoError(t, os.Mkdir(j.RootPath(), 0700))
	_, err := newStubDrive(filepath.Join(j.RootPath(), "ctrstub0"), j, true, nil, logrus.NewEntry(logrus.New()))
	require.NoError(t, err)
	require.Empty(t, j.restoreStubs)
}
