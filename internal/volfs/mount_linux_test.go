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
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestMountExportUsesPrivateView(t *testing.T) {
	requireGuestMountTests(t)
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	require.NoError(t, os.Mkdir(sub, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "data"), []byte("backing"), 0600))
	open := func(path string, recursive bool) *Export {
		export, err := OpenMountExport(path, false, recursive, Limits{})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, export.Close()) })
		return export
	}
	before := open(root, true)
	require.NoError(t, unix.Mount("tmpfs", sub, "tmpfs", 0, "size=4096k"))
	t.Cleanup(func() { require.NoError(t, unix.Unmount(sub, 0)) })
	require.NoError(t, os.WriteFile(filepath.Join(sub, "data"), []byte("mounted"), 0600))
	check := func(export *Export, want string) {
		parent, err := export.Lookup(Root, []byte("sub"))
		require.NoError(t, err)
		entry, err := export.Lookup(parent.Node, []byte("data"))
		require.NoError(t, err)
		handle, err := export.Open(entry.Node, unix.O_RDONLY)
		require.NoError(t, err)
		data, err := export.Read(handle, 0, 100)
		require.NoError(t, err)
		require.Equal(t, want, string(data))
		require.NoError(t, export.Release(handle))
	}
	check(before, "backing")
	check(open(root, true), "mounted")
	check(open(root, false), "backing")
	file := open(filepath.Join(sub, "data"), true)
	entry, err := file.Lookup(Root, []byte("file"))
	require.NoError(t, err)
	handle, err := file.Open(entry.Node, unix.O_RDONLY)
	require.NoError(t, err)
	data, err := file.Read(handle, 0, 100)
	require.NoError(t, err)
	require.Equal(t, "mounted", string(data))
}

func TestHostListenerSupportsLongDirectoryAndOwnedCleanup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("a", 80), strings.Repeat("b", 80))
	require.NoError(t, os.MkdirAll(dir, 0700))
	path := filepath.Join(dir, "vsock.sock")
	listener, err := ListenHost(path, os.Getuid(), os.Getgid())
	require.NoError(t, err)
	defer listener.Close()
	_, err = ListenHost(path, os.Getuid(), os.Getgid())
	require.Error(t, err) // it cannot replace a listener it does not own
	parent, err := os.Open(dir)
	require.NoError(t, err)
	defer parent.Close()
	address := filepath.Join("/proc/self/fd", strconv.Itoa(int(parent.Fd())), "vsock.sock_10901")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", address)
	require.NoError(t, err)
	defer conn.Close()
	accepted, err := listener.Accept()
	require.NoError(t, err)
	defer accepted.Close()
	require.NoError(t, accepted.SetDeadline(time.Now().Add(time.Second)))
	_, err = accepted.Write([]byte("connected"))
	require.NoError(t, err)
	require.NoError(t, accepted.Close())
	data, err := io.ReadAll(conn)
	require.NoError(t, err)
	require.Equal(t, "connected", string(data))
	require.NoError(t, listener.Close())
	_, err = os.Lstat(path + "_10901")
	require.ErrorIs(t, err, os.ErrNotExist)
}
