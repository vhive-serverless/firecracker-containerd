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
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/mdlayher/socket"
	"golang.org/x/sys/unix"
)

type vmAddress struct{ cid, port uint32 }

func (a vmAddress) Network() string { return "vsock" }
func (a vmAddress) String() string  { return fmt.Sprintf("%d:%d", a.cid, a.port) }

type guestConnection struct {
	*socket.Conn
	local, remote net.Addr
}

func (c *guestConnection) LocalAddr() net.Addr  { return c.local }
func (c *guestConnection) RemoteAddr() net.Addr { return c.remote }

// DialHost connects a guest to this VM's host volume service. Connect uses the
// mount client's attempt context so shutdown also interrupts connection setup.
func DialHost(ctx context.Context) (net.Conn, error) {
	conn, err := socket.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0, "volume-vsock", nil)
	if err != nil {
		return nil, err
	}
	remote := &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: VolumePort}
	if _, err := conn.Connect(ctx, remote); err != nil {
		_ = conn.Close()
		return nil, err
	}
	address, err := conn.Getsockname()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	local, ok := address.(*unix.SockaddrVM)
	if !ok {
		_ = conn.Close()
		return nil, unix.EPROTO
	}
	return &guestConnection{Conn: conn, local: vmAddress{local.CID, local.Port}, remote: vmAddress{remote.CID, remote.Port}}, nil
}

type directoryListener struct {
	*net.UnixListener
	directory *os.File
	name      string
	once      sync.Once
	err       error
}

func (l *directoryListener) Close() error {
	l.once.Do(func() {
		l.err = l.UnixListener.Close()
		if err := unix.Unlinkat(int(l.directory.Fd()), l.name, 0); err != nil && err != unix.ENOENT && l.err == nil {
			l.err = err
		}
		if err := l.directory.Close(); l.err == nil {
			l.err = err
		}
	})
	return l.err
}

// ListenHost creates the Unix endpoint used by Firecracker's guest-initiated
// vsock backend. A retained directory FD avoids Unix socket path length limits
// and lets cleanup unlink the exact socket without re-resolving a long path.
func ListenHost(vsockPath string, uid, gid int) (net.Listener, error) {
	directory, err := os.Open(filepath.Dir(vsockPath))
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("%s_%d", filepath.Base(vsockPath), VolumePort)
	address := fmt.Sprintf("/proc/self/fd/%d/%s", directory.Fd(), name)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	owned := &directoryListener{UnixListener: listener, directory: directory, name: name}
	if err := unix.Fchownat(int(directory.Fd()), name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = owned.Close()
		return nil, err
	}
	if err := unix.Fchmodat(int(directory.Fd()), name, 0600, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = owned.Close()
		return nil, err
	}
	return owned, nil
}
