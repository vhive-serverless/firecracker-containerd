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

package volfs

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func caller() Credentials { return Credentials{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())} }

func testExport(t *testing.T, source string, readOnly bool, limits Limits) *Export {
	t.Helper()
	e, err := OpenExport(source, readOnly, limits)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, e.Close()) })
	return e
}

func TestRenameAndUnlinkPreserveOpenInode(t *testing.T) {
	root := t.TempDir()
	e := testExport(t, root, false, Limits{})
	dir, err := e.Mkdir(Root, []byte("original"), 0750, caller())
	require.NoError(t, err)
	require.EqualValues(t, 0750, dir.Attr.Mode&07777)
	name := []byte{'f', 0xff}
	entry, handle, err := e.Create(dir.Node, name, unix.O_RDWR|unix.O_EXCL, 0640, caller())
	require.NoError(t, err)
	require.Equal(t, caller().UID, entry.Attr.UID)
	require.Equal(t, caller().GID, entry.Attr.GID)
	require.EqualValues(t, 0640, entry.Attr.Mode&07777)
	n, err := e.Write(handle, 0, []byte("guest writes"))
	require.NoError(t, err)
	require.Equal(t, len("guest writes"), n)
	hostPath := filepath.Join(root, "original", string(name))
	data, err := os.ReadFile(hostPath)
	require.NoError(t, err)
	require.Equal(t, "guest writes", string(data))
	require.NoError(t, os.WriteFile(hostPath, []byte("host changes"), 0600))
	data, err = e.Read(handle, 0, MaxIO)
	require.NoError(t, err)
	require.Equal(t, "host changes", string(data))
	lookup, err := e.Lookup(dir.Node, name)
	require.NoError(t, err)
	require.Equal(t, entry.Node, lookup.Node)
	entries, err := e.ReadDir(dir.Node)
	require.NoError(t, err)
	require.Equal(t, []Dirent{{Name: name, Mode: unix.S_IFREG}}, entries)
	require.NoError(t, e.Rename(Root, []byte("original"), Root, []byte("renamed"), 0))
	require.NoError(t, e.Rename(dir.Node, name, dir.Node, []byte("moved"), unix.RENAME_NOREPLACE))
	require.NoError(t, e.Unlink(dir.Node, []byte("moved"), false))
	attr, err := e.Getattr(entry.Node)
	require.NoError(t, err)
	require.Zero(t, attr.Links)
	require.NoError(t, e.Sync(handle, false))
	require.NoError(t, e.Sync(handle, true))
	e.Forget(entry.Node, 2)
	data, err = e.Read(handle, 0, 100)
	require.NoError(t, err)
	require.Equal(t, "host changes", string(data))
	require.NoError(t, e.Release(handle))
	_, err = e.Getattr(entry.Node)
	require.ErrorIs(t, err, unix.ESTALE)
	require.NoError(t, e.Unlink(Root, []byte("renamed"), true))
	stat, err := e.Statfs()
	require.NoError(t, err)
	require.Positive(t, stat.Bsize)
}

func TestNamesAndSymlinksStayInGuestNamespace(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "volume"), filepath.Join(base, "outside")
	require.NoError(t, os.Mkdir(root, 0700))
	require.NoError(t, os.WriteFile(outside, []byte("outside secret"), 0600))
	e := testExport(t, root, false, Limits{})
	for _, name := range [][]byte{nil, []byte("."), []byte(".."), []byte("../outside"), []byte("/outside"), []byte("a/b"), {'a', 0, 'b'}, []byte(strings.Repeat("x", 256))} {
		_, err := e.Lookup(Root, name)
		require.ErrorIs(t, err, unix.EINVAL)
		_, _, err = e.Create(Root, name, unix.O_RDWR, 0600, caller())
		require.ErrorIs(t, err, unix.EINVAL)
		require.ErrorIs(t, e.Rename(Root, name, Root, []byte("new"), 0), unix.EINVAL)
		require.ErrorIs(t, e.Unlink(Root, name, false), unix.EINVAL)
	}
	link, err := e.Symlink(Root, []byte("escape"), []byte(outside), caller())
	require.NoError(t, err)
	require.EqualValues(t, unix.S_IFLNK, link.Attr.Mode&unix.S_IFMT)
	target, err := e.Readlink(link.Node)
	require.NoError(t, err)
	require.Equal(t, []byte(outside), target)
	_, err = e.Open(link.Node, unix.O_RDWR|unix.O_TRUNC)
	require.ErrorIs(t, err, unix.ELOOP)
	_, _, err = e.Create(Root, []byte("escape"), unix.O_RDWR|unix.O_TRUNC, 0600, caller())
	require.ErrorIs(t, err, unix.ELOOP)
	_, err = e.Lookup(link.Node, []byte("anything"))
	require.ErrorIs(t, err, unix.ENOTDIR)
	size := uint64(0)
	_, err = e.Setattr(link.Node, 0, SetAttr{Size: &size})
	require.ErrorIs(t, err, unix.ELOOP)
	mode := uint32(0777)
	_, err = e.Setattr(link.Node, 0, SetAttr{Mode: &mode})
	require.ErrorIs(t, err, unix.EOPNOTSUPP)
	data, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "outside secret", string(data))
	info, err := os.Stat(outside)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	// A trusted source symlink is resolved once, matching a host bind source.
	sourceLink := filepath.Join(base, "source-link")
	require.NoError(t, os.Symlink(root, sourceLink))
	throughLink := testExport(t, sourceLink, false, Limits{})
	_, err = throughLink.Lookup(Root, []byte("escape"))
	require.NoError(t, err)
	// Non-UTF-8 symlink targets are preserved as bytes too.
	rawTarget := []byte{'a', 0xff, '/', 'b'}
	raw, err := e.Symlink(Root, []byte("raw"), rawTarget, caller())
	require.NoError(t, err)
	target, err = e.Readlink(raw.Node)
	require.NoError(t, err)
	require.Equal(t, rawTarget, target)
}

func TestHandleLimitPrecedesTruncationOrCreation(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "file"), []byte("keep contents"), 0600))
	e := testExport(t, root, false, Limits{OpenFiles: 1})
	entry, err := e.Lookup(Root, []byte("file"))
	require.NoError(t, err)
	handle, err := e.Open(entry.Node, unix.O_RDONLY|unix.O_NOFOLLOW)
	require.NoError(t, err)
	_, err = e.Open(entry.Node, unix.O_WRONLY|unix.O_TRUNC)
	require.ErrorIs(t, err, unix.EMFILE)
	_, _, err = e.Create(Root, []byte("file"), unix.O_WRONLY|unix.O_TRUNC, 0777, caller())
	require.ErrorIs(t, err, unix.EMFILE)
	_, _, err = e.Create(Root, []byte("new"), unix.O_WRONLY, 0600, caller())
	require.ErrorIs(t, err, unix.EMFILE)
	_, err = os.Lstat(filepath.Join(root, "new"))
	require.ErrorIs(t, err, os.ErrNotExist)
	data, err := e.Read(handle, 0, 100)
	require.NoError(t, err)
	require.Equal(t, "keep contents", string(data))
	require.NoError(t, e.Release(handle))
	entry, handle, err = e.Create(Root, []byte("file"), unix.O_WRONLY|unix.O_APPEND, 0777, caller())
	require.NoError(t, err)
	require.EqualValues(t, 0600, entry.Attr.Mode&07777)
	_, err = e.Write(handle, 0, []byte("+append"))
	require.NoError(t, err)
	require.NoError(t, e.Release(handle))
	data, err = os.ReadFile(filepath.Join(root, "file"))
	require.NoError(t, err)
	require.Equal(t, "keep contents+append", string(data))
}

func TestSpecialFileCannotBlockOrOpenOnHost(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pipe")
	require.NoError(t, unix.Mkfifo(path, 0600))
	e := testExport(t, root, false, Limits{})
	entry, err := e.Lookup(Root, []byte("pipe"))
	require.NoError(t, err)
	require.EqualValues(t, unix.S_IFIFO, entry.Attr.Mode&unix.S_IFMT)
	_, err = e.Open(entry.Node, unix.O_RDONLY)
	require.ErrorIs(t, err, unix.EOPNOTSUPP)
	_, _, err = e.Create(Root, []byte("pipe"), unix.O_WRONLY, 0600, caller())
	require.ErrorIs(t, err, unix.EOPNOTSUPP)
	_, err = OpenExport(path, false, Limits{})
	require.ErrorIs(t, err, unix.EOPNOTSUPP)
}

func TestGuestOwnershipAndSetgidInheritance(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("host-service ownership checks run in the root worker invocation")
	}
	e := testExport(t, t.TempDir(), false, Limits{})
	parent, err := e.Mkdir(Root, []byte("shared"), 0770, caller())
	require.NoError(t, err)
	gid, mode := uint32(23456), uint32(02770)
	_, err = e.Setattr(parent.Node, 0, SetAttr{GID: &gid, Mode: &mode})
	require.NoError(t, err)
	guest := Credentials{UID: 12345, GID: 34567}
	entry, handle, err := e.Create(parent.Node, []byte("file"), unix.O_RDWR, 0660, guest)
	require.NoError(t, err)
	require.NoError(t, e.Release(handle))
	require.Equal(t, guest.UID, entry.Attr.UID)
	require.Equal(t, gid, entry.Attr.GID)
	require.EqualValues(t, 0660, entry.Attr.Mode&07777)
	dir, err := e.Mkdir(parent.Node, []byte("nested"), 0750, guest)
	require.NoError(t, err)
	require.Equal(t, guest.UID, dir.Attr.UID)
	require.Equal(t, gid, dir.Attr.GID)
	require.EqualValues(t, 02750, dir.Attr.Mode&07777)
}

func TestSingleFileExportAndReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	require.NoError(t, os.WriteFile(path, []byte("initial"), 0600))
	e := testExport(t, path, false, Limits{})
	entries, err := e.ReadDir(Root)
	require.NoError(t, err)
	require.Equal(t, []Dirent{{Name: []byte("file"), Mode: unix.S_IFREG}}, entries)
	_, err = e.Lookup(Root, []byte("other"))
	require.ErrorIs(t, err, unix.ENOENT)
	entry, err := e.Lookup(Root, []byte("file"))
	require.NoError(t, err)
	handle, err := e.Open(entry.Node, unix.O_RDWR|unix.O_TRUNC)
	require.NoError(t, err)
	_, err = e.Write(handle, 0, []byte("container terminated"))
	require.NoError(t, err)
	require.NoError(t, e.Release(handle))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "container terminated", string(data))
	_, _, err = e.Create(Root, []byte("other"), unix.O_RDWR, 0600, caller())
	require.ErrorIs(t, err, unix.EPERM)
	ro := testExport(t, path, true, Limits{})
	entry, err = ro.Lookup(Root, []byte("file"))
	require.NoError(t, err)
	handle, err = ro.Open(entry.Node, unix.O_RDONLY)
	require.NoError(t, err)
	_, err = ro.Write(handle, 0, []byte("bad"))
	require.ErrorIs(t, err, unix.EROFS)
	_, err = ro.Open(entry.Node, unix.O_RDONLY|unix.O_TRUNC)
	require.ErrorIs(t, err, unix.EROFS)
	_, _, err = ro.Create(Root, []byte("bad"), unix.O_RDONLY, 0600, caller())
	require.ErrorIs(t, err, unix.EROFS)
	_, err = ro.Mkdir(Root, []byte("bad"), 0700, caller())
	require.ErrorIs(t, err, unix.EROFS)
	_, err = ro.Symlink(Root, []byte("bad"), []byte("target"), caller())
	require.ErrorIs(t, err, unix.EROFS)
	_, err = ro.Link(entry.Node, Root, []byte("bad"))
	require.ErrorIs(t, err, unix.EROFS)
	require.ErrorIs(t, ro.Rename(Root, []byte("file"), Root, []byte("bad"), 0), unix.EROFS)
	require.ErrorIs(t, ro.Unlink(Root, []byte("file"), false), unix.EROFS)
	size := uint64(0)
	_, err = ro.Setattr(entry.Node, handle, SetAttr{Size: &size})
	require.ErrorIs(t, err, unix.EROFS)
	_, err = ro.Statfs()
	require.NoError(t, err)
}

func TestMetadataAndHardLinkIdentity(t *testing.T) {
	e := testExport(t, t.TempDir(), false, Limits{})
	entry, handle, err := e.Create(Root, []byte("file"), unix.O_RDWR, 0600, caller())
	require.NoError(t, err)
	defer e.Release(handle)
	_, err = e.Write(handle, 0, []byte("content"))
	require.NoError(t, err)
	size, mode := uint64(3), uint32(0644)
	mtime := unix.Timespec{Sec: 1234567890, Nsec: 123456000}
	attr, err := e.Setattr(entry.Node, handle, SetAttr{Size: &size, Mode: &mode, MTime: &mtime})
	require.NoError(t, err)
	require.Equal(t, size, attr.Size)
	require.Equal(t, mode, attr.Mode&07777)
	// ext3 has whole-second timestamp precision; other backings may retain ns.
	require.Equal(t, mtime.Sec, attr.MTime.Sec)
	data, err := e.Read(handle, 0, 100)
	require.NoError(t, err)
	require.Equal(t, "con", string(data))
	other, otherHandle, err := e.Create(Root, []byte("other"), unix.O_RDWR, 0600, caller())
	require.NoError(t, err)
	defer e.Release(otherHandle)
	_, err = e.Setattr(other.Node, handle, SetAttr{Size: &size})
	require.ErrorIs(t, err, unix.EBADF)
	if os.Geteuid() != 0 {
		t.Log("AT_EMPTY_PATH hard-link check requires the host service's CAP_DAC_READ_SEARCH; covered by the root worker run")
		return
	}
	linked, err := e.Link(entry.Node, Root, []byte("alias"))
	require.NoError(t, err)
	require.Equal(t, entry.Node, linked.Node)
	require.EqualValues(t, 2, linked.Attr.Links)
	require.NoError(t, e.Unlink(Root, []byte("file"), false))
	lookup, err := e.Lookup(Root, []byte("alias"))
	require.NoError(t, err)
	require.Equal(t, entry.Node, lookup.Node)
}

func TestDescriptorLimitsAndForget(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one", "two"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), nil, 0600))
	}
	e := testExport(t, root, false, Limits{Nodes: 2, Identities: 2})
	one, err := e.Lookup(Root, []byte("one"))
	require.NoError(t, err)
	_, err = e.Lookup(Root, []byte("two"))
	require.ErrorIs(t, err, unix.ENFILE)
	e.Forget(one.Node, 1)
	two, err := e.Lookup(Root, []byte("two"))
	require.NoError(t, err)
	require.NotEqual(t, one.Node, two.Node)
	_, err = e.Open(one.Node, unix.O_RDONLY)
	require.ErrorIs(t, err, unix.ESTALE)
	_, err = e.Open(two.Node, unix.O_CREAT)
	require.ErrorIs(t, err, unix.EINVAL)
	_, err = e.Read(100, 0, MaxIO+1)
	require.ErrorIs(t, err, unix.EINVAL)
	_, err = e.Read(100, -1, 1)
	require.ErrorIs(t, err, unix.EINVAL)
	_, err = e.Write(100, 0, make([]byte, MaxIO+1))
	require.ErrorIs(t, err, unix.EINVAL)
	e.Forget(two.Node, ^uint64(0))
	_, err = e.Getattr(two.Node)
	require.ErrorIs(t, err, unix.ESTALE)
	entries, err := e.ReadDir(Root)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, string(entry.Name))
	}
	sort.Strings(names)
	require.Equal(t, []string{"one", "two"}, names)
}

func TestConcurrentCloseAndForgetReleaseDescriptors(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "file"), []byte("data"), 0600))
	e := testExport(t, root, false, Limits{})
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 32; i++ {
		ready.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			entry, err := e.Lookup(Root, []byte("file"))
			ready.Done()
			require.NoError(t, err)
			<-start
			for n := 0; n < 20; n++ {
				handle, err := e.Open(entry.Node, unix.O_RDWR)
				if err != nil {
					require.True(t, errors.Is(err, unix.EBADF) || errors.Is(err, unix.ESTALE), "%v", err)
					break
				}
				_, err = e.Write(handle, 0, []byte("same"))
				if err != nil {
					require.ErrorIs(t, err, unix.EBADF)
				}
				err = e.Release(handle)
				if err != nil {
					require.ErrorIs(t, err, unix.EBADF)
				}
			}
			e.Forget(entry.Node, 1)
		}()
	}
	ready.Wait()
	close(start)
	require.NoError(t, e.Close())
	done.Wait()
	require.NoError(t, e.Close())
	_, err := e.Lookup(Root, []byte("file"))
	require.ErrorIs(t, err, unix.EBADF)
	fds, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
		if err == nil {
			require.False(t, strings.HasPrefix(target, root), "leaked descriptor %s -> %s", fd.Name(), target)
		}
	}
}
