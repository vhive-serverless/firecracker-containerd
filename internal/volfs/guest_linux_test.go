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
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func requireGuestMountTests(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 || os.Getenv("VOLFS_MOUNT_TESTS") != "1" {
		t.Skip("set VOLFS_MOUNT_TESTS=1 and run as root in a private mount namespace")
	}
}

func testGuestMount(t *testing.T, descriptor Descriptor, dial DialFunc) *GuestMount {
	t.Helper()
	path := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mount, err := MountGuest(ctx, context.Background(), path, descriptor, dial)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mount.Close()) })
	return mount
}

func TestGuestFilesystemLifecycleAndHostCoherence(t *testing.T) {
	requireGuestMountTests(t)
	_, descriptor, dial, root := rpcServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "seed"), []byte("first"), 0644))
	require.NoError(t, unix.Mkfifo(filepath.Join(root, "pipe"), 0600))
	mount := testGuestMount(t, descriptor, dial)
	_, err := os.OpenFile(filepath.Join(mount.Source(), "pipe"), os.O_RDONLY|unix.O_NONBLOCK, 0)
	require.ErrorIs(t, err, unix.EOPNOTSUPP)
	require.NoError(t, unix.Mknod(filepath.Join(mount.Source(), "plain"), unix.S_IFREG|0600, 0))
	seed, err := os.OpenFile(filepath.Join(mount.Source(), "seed"), os.O_RDWR, 0)
	require.NoError(t, err)
	defer seed.Close()
	data, err := io.ReadAll(seed)
	require.NoError(t, err)
	require.Equal(t, "first", string(data))
	// The open guest description sees a same-size host rewrite immediately,
	// even on an ext3 backing with whole-second mtime granularity.
	require.NoError(t, os.WriteFile(filepath.Join(root, "seed"), []byte("other"), 0644))
	data = make([]byte, 5)
	_, err = seed.ReadAt(data, 0)
	require.NoError(t, err)
	require.Equal(t, "other", string(data))
	_, err = seed.WriteAt([]byte("guest"), 0)
	require.NoError(t, err)
	data, err = os.ReadFile(filepath.Join(root, "seed"))
	require.NoError(t, err)
	require.Equal(t, "guest", string(data))
	require.NoError(t, os.Mkdir(filepath.Join(mount.Source(), "before"), 0750))
	directory, err := os.Open(filepath.Join(mount.Source(), "before"))
	require.NoError(t, err)
	require.NoError(t, directory.Sync())
	require.NoError(t, directory.Close())
	name := string([]byte{'v', 0xff})
	path := filepath.Join(mount.Source(), "before", name)
	// #nosec G302 -- Exercise preservation of an explicit guest group-read mode.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0640)
	require.NoError(t, err)
	defer file.Close()
	_, err = file.WriteString("preserve inode")
	require.NoError(t, err)
	require.NoError(t, file.Sync())
	alias := filepath.Join(mount.Source(), "before", "alias")
	require.NoError(t, os.Link(path, alias))
	first, err := os.Stat(path)
	require.NoError(t, err)
	second, err := os.Stat(alias)
	require.NoError(t, err)
	require.Equal(t, first.Sys().(*syscall.Stat_t).Ino, second.Sys().(*syscall.Stat_t).Ino)
	require.NoError(t, os.Rename(filepath.Join(mount.Source(), "before"), filepath.Join(mount.Source(), "after")))
	require.NoError(t, os.Remove(filepath.Join(mount.Source(), "after", name)))
	require.NoError(t, os.Remove(filepath.Join(mount.Source(), "after", "alias")))
	info, err := file.Stat()
	require.NoError(t, err)
	require.Zero(t, info.Sys().(*syscall.Stat_t).Nlink)
	data = make([]byte, len("preserve inode"))
	_, err = file.ReadAt(data, 0)
	require.NoError(t, err)
	require.Equal(t, "preserve inode", string(data))
	require.NoError(t, file.Truncate(3))
	info, err = file.Stat()
	require.NoError(t, err)
	require.EqualValues(t, 3, info.Size())
	require.NoError(t, os.Symlink("seed", filepath.Join(mount.Source(), "link")))
	target, err := os.Readlink(filepath.Join(root, "link"))
	require.NoError(t, err)
	require.Equal(t, "seed", target)
	data, err = os.ReadFile(filepath.Join(mount.Source(), "link"))
	require.NoError(t, err)
	require.Equal(t, "guest", string(data))
	var stat unix.Statfs_t
	require.NoError(t, unix.Statfs(mount.Source(), &stat))
	require.Positive(t, stat.Bsize)
}

func TestGuestFilesystemDirectoryCookiesAndReconnect(t *testing.T) {
	requireGuestMountTests(t)
	_, descriptor, dial, root := rpcServer(t)
	want := make([]string, 0, 1000)
	for i := 0; i < 1000; i++ {
		name := fmt.Sprintf("file-%04d", i)
		want = append(want, name)
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("contents"), 0600))
	}
	var drop atomic.Bool
	mount := testGuestMount(t, descriptor, func(ctx context.Context) (net.Conn, error) {
		conn, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		return &lostReplyConn{Conn: conn, drop: &drop}, nil
	})
	dir, err := os.Open(mount.Source())
	require.NoError(t, err)
	defer dir.Close()
	var names []string
	for {
		batch, err := dir.Readdirnames(7)
		names = append(names, batch...)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}
	sort.Strings(names)
	require.Equal(t, want, names)
	file, err := os.OpenFile(filepath.Join(mount.Source(), want[0]), os.O_RDWR, 0)
	require.NoError(t, err)
	defer file.Close()
	drop.Store(true)
	_, err = file.WriteAt([]byte("replayed"), 0)
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(root, want[0]))
	require.NoError(t, err)
	require.Equal(t, "replayed", string(data))
	data = make([]byte, 8)
	_, err = file.ReadAt(data, 0)
	require.NoError(t, err)
	require.Equal(t, "replayed", string(data))
}

func TestGuestSingleFileAndReadOnlyMount(t *testing.T) {
	requireGuestMountTests(t)
	path := filepath.Join(t.TempDir(), "termination-log")
	require.NoError(t, os.WriteFile(path, []byte("initial"), 0600))
	_, descriptor, dial, _ := rpcSourceServer(t, path, false)
	mount := testGuestMount(t, descriptor, dial)
	require.NoError(t, os.WriteFile(mount.Source(), []byte("terminated"), 0600))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "terminated", string(data))
	_, readonly, readDial, _ := rpcSourceServer(t, path, true)
	readMount := testGuestMount(t, readonly, readDial)
	data, err = os.ReadFile(readMount.Source())
	require.NoError(t, err)
	require.Equal(t, "terminated", string(data))
	err = os.WriteFile(readMount.Source(), []byte("bad"), 0600)
	require.ErrorIs(t, err, unix.EROFS)
	// #nosec G302 -- This operation must fail without changing the inode mode.
	require.ErrorIs(t, os.Chmod(readMount.Source(), 0777), unix.EROFS)
}

func TestGuestKernelPermissionsAndOwnership(t *testing.T) {
	requireGuestMountTests(t)
	_, descriptor, dial, root := rpcServer(t)
	require.NoError(t, os.Chmod(root, os.ModeSticky|0777))
	require.NoError(t, os.WriteFile(filepath.Join(root, "private"), []byte("private"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "public"), []byte("public"), 0644))
	mount := testGuestMount(t, descriptor, dial)
	// Permit traversal of this test's own temporary parent so permission
	// failures below are attributable to the FUSE inode rather than its parent.
	// #nosec G302 -- Permit traversal of this test's temporary parent only.
	require.NoError(t, os.Chmod(filepath.Dir(mount.Source()), 0755))
	command := func(path string, args ...string) ([]byte, error) {
		cmd := exec.Command(path, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
		return cmd.CombinedOutput()
	}
	data, err := command("/bin/cat", filepath.Join(mount.Source(), "public"))
	require.NoError(t, err, "%s", data)
	require.Equal(t, "public", string(data))
	data, err = command("/bin/cat", filepath.Join(mount.Source(), "private"))
	require.Error(t, err)
	require.Contains(t, string(data), "Permission denied")
	data, err = command("/bin/sh", "-c", "umask 077; printf child > \"$1\"", "volume-test", filepath.Join(mount.Source(), "child"))
	require.NoError(t, err, "%s", data)
	info, err := os.Stat(filepath.Join(root, "child"))
	require.NoError(t, err)
	require.EqualValues(t, 65534, info.Sys().(*syscall.Stat_t).Uid)
	require.EqualValues(t, 65534, info.Sys().(*syscall.Stat_t).Gid)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	data, err = command("/bin/rm", filepath.Join(mount.Source(), "public"))
	require.Error(t, err)
	require.Contains(t, string(data), "Operation not permitted")
}
