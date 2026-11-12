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
	"errors"
	"path/filepath"
	"sync"

	"github.com/hanwen/go-fuse/v2/fuse"
	"golang.org/x/sys/unix"
)

// GuestMount owns one guest FUSE mount and its reconnectable descriptor client.
// Directory cursors and client sequence state reside in guest memory.
type GuestMount struct {
	server *fuse.Server
	fs     *guestFS
	path   string
	once   sync.Once
	err    error
}

type guestFS struct {
	fuse.RawFileSystem
	client      *Client
	dirMu       sync.Mutex
	nextDir     uint64
	directories map[uint64][]fuse.DirEntry
	forgetMu    sync.Mutex
	forgotten   map[uint64]uint64
	forgetWake  chan struct{}
	forgetDone  chan struct{}
}

// MountGuest mounts a host export. ctx bounds setup; lifetime lasts until the
// task or agent is removed. Kernel default_permissions enforces guest DAC.
// User namespaces require ID translation before this interface can be used.
func MountGuest(ctx, lifetime context.Context, path string, descriptor Descriptor, dial DialFunc) (*GuestMount, error) {
	client, err := NewClient(lifetime, descriptor, dial)
	if err != nil {
		return nil, err
	}
	stopSetup := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stopSetup()
	_, err = client.Call(ctx, Request{Operation: opGetattr, Node: Root})
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	fs := &guestFS{RawFileSystem: fuse.NewDefaultRawFileSystem(), client: client,
		directories: make(map[uint64][]fuse.DirEntry), forgotten: make(map[uint64]uint64),
		forgetWake: make(chan struct{}, 1), forgetDone: make(chan struct{})}
	options := &fuse.MountOptions{AllowOther: true, Options: []string{"default_permissions"},
		FsName: "firecracker-volume", Name: "fcvolume", MaxWrite: MaxIO,
		DirectMountStrict: true, DisableReadDirPlus: true,
		DirectMountFlags: unix.MS_NODEV | unix.MS_NOSUID}
	if descriptor.ReadOnly {
		options.DirectMountFlags |= unix.MS_RDONLY
		options.Options = append(options.Options, "ro")
	}
	server, err := fuse.NewServer(fs, path, options)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	mount := &GuestMount{server: server, fs: fs, path: path}
	go fs.forgetLoop()
	go server.Serve()
	if err := server.WaitMount(); err != nil {
		_ = mount.Close()
		return nil, err
	}
	stopSetup()
	if err := ctx.Err(); err != nil {
		_ = mount.Close()
		return nil, err
	}
	return mount, nil
}

// Source returns the guest path to bind into the container. A single-file
// export has a synthetic directory root containing only its authorized file.
func (m *GuestMount) Source() string {
	if m.fs.client.descriptor.FileOnly {
		return filepath.Join(m.path, "file")
	}
	return m.path
}

// Close interrupts the client before unmounting so disconnected requests cannot
// hold cleanup forever. Host export revocation belongs to the owning shim.
func (m *GuestMount) Close() error {
	m.once.Do(func() {
		_ = m.fs.client.Close()
		m.err = m.server.Unmount()
		if m.err != nil {
			// A process may still hold a file or cwd after its owner's task is
			// removed. Revoke IO first, then detach this owned mount so bundle
			// cleanup cannot walk through a still-mounted host export.
			err := unix.Unmount(m.path, unix.MNT_DETACH|unix.UMOUNT_NOFOLLOW)
			if err == nil || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOENT) {
				m.err = nil
			} else {
				m.err = errors.Join(m.err, err)
			}
		}
		<-m.fs.forgetDone
	})
	return m.err
}

func (fs *guestFS) String() string { return "firecracker-volume" }

func (fs *guestFS) call(request Request) (Reply, fuse.Status) {
	reply, err := fs.client.Call(fs.client.ctx, request)
	if err == nil {
		return reply, fuse.OK
	}
	var errno unix.Errno
	if errors.As(err, &errno) {
		return reply, fuse.Status(errno)
	}
	return reply, fuse.EIO
}

func fillAttr(out *fuse.Attr, attr *Attr) {
	out.Ino, out.Size, out.Blocks = attr.Inode, attr.Size, attr.Blocks
	out.Mode, out.Nlink, out.Uid, out.Gid = attr.Mode, uint32(attr.Links), attr.UID, attr.GID
	out.Rdev, out.Blksize = uint32(attr.Device), attr.BlockSize
	out.Atime, out.Mtime, out.Ctime = uint64(attr.ATime.Sec), uint64(attr.MTime.Sec), uint64(attr.CTime.Sec)
	out.Atimensec, out.Mtimensec, out.Ctimensec = uint32(attr.ATime.Nsec), uint32(attr.MTime.Nsec), uint32(attr.CTime.Nsec)
}

func fillEntry(out *fuse.EntryOut, reply Reply, status fuse.Status) fuse.Status {
	if status != fuse.OK {
		return status
	}
	if reply.Entry == nil {
		return fuse.EIO
	}
	out.NodeId, out.Generation = reply.Entry.Node, 1
	fillAttr(&out.Attr, &reply.Entry.Attr)
	// Entry and attribute validity remain zero: host dentries and permissions
	// may change independently of this guest.
	return fuse.OK
}

func (fs *guestFS) Lookup(_ <-chan struct{}, header *fuse.InHeader, name string, out *fuse.EntryOut) fuse.Status {
	reply, status := fs.call(Request{Operation: "lookup", Parent: header.NodeId, Name: []byte(name)})
	if status == fuse.OK && reply.Entry != nil {
		switch reply.Entry.Attr.Mode & unix.S_IFMT {
		case unix.S_IFREG, unix.S_IFDIR, unix.S_IFLNK:
		default:
			// A special inode would acquire guest-local kernel behavior (for
			// example a FIFO), which would not represent the host object.
			fs.Forget(reply.Entry.Node, 1)
			return fuse.Status(unix.EOPNOTSUPP)
		}
	}
	return fillEntry(out, reply, status)
}

// Forget only queues kernel reference counts; it never waits for network IO.
func (fs *guestFS) Forget(id, count uint64) {
	fs.forgetMu.Lock()
	fs.forgotten[id] += count
	fs.forgetMu.Unlock()
	select {
	case fs.forgetWake <- struct{}{}:
	default:
	}
}

func (fs *guestFS) forgetLoop() {
	defer close(fs.forgetDone)
	for {
		select {
		case <-fs.client.ctx.Done():
			return
		case <-fs.forgetWake:
		}
		fs.forgetMu.Lock()
		forgotten := fs.forgotten
		fs.forgotten = make(map[uint64]uint64)
		fs.forgetMu.Unlock()
		for id, count := range forgotten {
			if _, err := fs.client.Call(fs.client.ctx, Request{Operation: "forget", Node: id, Count: count}); err != nil {
				_ = fs.client.Close()
				return
			}
		}
	}
}

func (fs *guestFS) OnUnmount() { _ = fs.client.Close() }

func (fs *guestFS) GetAttr(_ <-chan struct{}, input *fuse.GetAttrIn, out *fuse.AttrOut) fuse.Status {
	reply, status := fs.call(Request{Operation: opGetattr, Node: input.NodeId})
	if status != fuse.OK {
		return status
	}
	if reply.Attr == nil {
		return fuse.EIO
	}
	fillAttr(&out.Attr, reply.Attr)
	return fuse.OK
}

func (fs *guestFS) SetAttr(_ <-chan struct{}, input *fuse.SetAttrIn, out *fuse.AttrOut) fuse.Status {
	change := &SetAttr{}
	if value, ok := input.GetMode(); ok {
		change.Mode = &value
	}
	if value, ok := input.GetUID(); ok {
		change.UID = &value
	}
	if value, ok := input.GetGID(); ok {
		change.GID = &value
	}
	if value, ok := input.GetSize(); ok {
		change.Size = &value
	}
	if value, ok := input.GetATime(); ok {
		stamp := unix.Timespec{Sec: value.Unix(), Nsec: int64(value.Nanosecond())}
		change.ATime = &stamp
	}
	if value, ok := input.GetMTime(); ok {
		stamp := unix.Timespec{Sec: value.Unix(), Nsec: int64(value.Nanosecond())}
		change.MTime = &stamp
	}
	handle, _ := input.GetFh()
	reply, status := fs.call(Request{Operation: "setattr", Node: input.NodeId, Handle: handle, Change: change})
	if status != fuse.OK {
		return status
	}
	if reply.Attr == nil {
		return fuse.EIO
	}
	fillAttr(&out.Attr, reply.Attr)
	return fuse.OK
}

func guestCred(header *fuse.InHeader) Credentials {
	return Credentials{UID: header.Uid, GID: header.Gid}
}

func (fs *guestFS) Mkdir(_ <-chan struct{}, input *fuse.MkdirIn, name string, out *fuse.EntryOut) fuse.Status {
	reply, status := fs.call(Request{Operation: "mkdir", Parent: input.NodeId, Name: []byte(name), Mode: input.Mode, Cred: guestCred(&input.InHeader)})
	return fillEntry(out, reply, status)
}

func (fs *guestFS) Symlink(_ <-chan struct{}, header *fuse.InHeader, target, name string, out *fuse.EntryOut) fuse.Status {
	reply, status := fs.call(Request{Operation: "symlink", Parent: header.NodeId, Name: []byte(name), Target: []byte(target), Cred: guestCred(header)})
	return fillEntry(out, reply, status)
}

func (fs *guestFS) Readlink(_ <-chan struct{}, header *fuse.InHeader) ([]byte, fuse.Status) {
	reply, status := fs.call(Request{Operation: "readlink", Node: header.NodeId})
	return reply.Data, status
}

func (fs *guestFS) Link(_ <-chan struct{}, input *fuse.LinkIn, name string, out *fuse.EntryOut) fuse.Status {
	reply, status := fs.call(Request{Operation: "link", Node: input.Oldnodeid, Parent: input.NodeId, Name: []byte(name)})
	return fillEntry(out, reply, status)
}

func (fs *guestFS) Unlink(_ <-chan struct{}, header *fuse.InHeader, name string) fuse.Status {
	_, status := fs.call(Request{Operation: opUnlink, Parent: header.NodeId, Name: []byte(name)})
	return status
}

func (fs *guestFS) Rmdir(_ <-chan struct{}, header *fuse.InHeader, name string) fuse.Status {
	_, status := fs.call(Request{Operation: opUnlink, Parent: header.NodeId, Name: []byte(name), Directory: true})
	return status
}

func (fs *guestFS) Rename(_ <-chan struct{}, input *fuse.RenameIn, oldName, newName string) fuse.Status {
	_, status := fs.call(Request{Operation: "rename", Parent: input.NodeId, Name: []byte(oldName), NewParent: input.Newdir, NewName: []byte(newName), Flags: input.Flags})
	return status
}

func (fs *guestFS) Open(_ <-chan struct{}, input *fuse.OpenIn, out *fuse.OpenOut) fuse.Status {
	reply, status := fs.call(Request{Operation: "open", Node: input.NodeId, Flags: input.Flags})
	if status == fuse.OK {
		out.Fh, out.OpenFlags = reply.Handle, fuse.FOPEN_DIRECT_IO
	}
	return status
}

func (fs *guestFS) Create(_ <-chan struct{}, input *fuse.CreateIn, name string, out *fuse.CreateOut) fuse.Status {
	reply, status := fs.call(Request{Operation: opCreate, Parent: input.NodeId, Name: []byte(name), Flags: input.Flags, Mode: input.Mode, Cred: guestCred(&input.InHeader)})
	status = fillEntry(&out.EntryOut, reply, status)
	if status == fuse.OK {
		out.Fh, out.OpenFlags = reply.Handle, fuse.FOPEN_DIRECT_IO
	}
	return status
}

func (fs *guestFS) Mknod(_ <-chan struct{}, input *fuse.MknodIn, name string, out *fuse.EntryOut) fuse.Status {
	if kind := input.Mode & unix.S_IFMT; kind != 0 && kind != unix.S_IFREG {
		return fuse.Status(unix.EOPNOTSUPP)
	}
	reply, status := fs.call(Request{Operation: opCreate, Parent: input.NodeId, Name: []byte(name),
		Flags: unix.O_RDWR | unix.O_EXCL, Mode: input.Mode, Cred: guestCred(&input.InHeader)})
	if status != fuse.OK {
		return status
	}
	_, releaseStatus := fs.call(Request{Operation: opRelease, Handle: reply.Handle})
	if releaseStatus != fuse.OK {
		if reply.Entry != nil {
			fs.Forget(reply.Entry.Node, 1)
		}
		return releaseStatus
	}
	return fillEntry(out, reply, status)
}

func (fs *guestFS) Read(_ <-chan struct{}, input *fuse.ReadIn, _ []byte) (fuse.ReadResult, fuse.Status) {
	if input.Offset > 1<<63-1 {
		return nil, fuse.EINVAL
	}
	reply, status := fs.call(Request{Operation: "read", Handle: input.Fh, Offset: int64(input.Offset), Size: int(input.Size)})
	return fuse.ReadResultData(reply.Data), status
}

func (fs *guestFS) Write(_ <-chan struct{}, input *fuse.WriteIn, data []byte) (uint32, fuse.Status) {
	if input.Offset > 1<<63-1 {
		return 0, fuse.EINVAL
	}
	reply, status := fs.call(Request{Operation: "write", Handle: input.Fh, Offset: int64(input.Offset), Data: data})
	return uint32(reply.Written), status
}

func (fs *guestFS) Release(_ <-chan struct{}, input *fuse.ReleaseIn) {
	_, _ = fs.call(Request{Operation: opRelease, Handle: input.Fh})
}

func (fs *guestFS) Flush(_ <-chan struct{}, _ *fuse.FlushIn) fuse.Status { return fuse.OK }

func (fs *guestFS) Fsync(_ <-chan struct{}, input *fuse.FsyncIn) fuse.Status {
	_, status := fs.call(Request{Operation: "sync", Handle: input.Fh, DataOnly: input.FsyncFlags&1 != 0})
	return status
}

func (fs *guestFS) FsyncDir(_ <-chan struct{}, input *fuse.FsyncIn) fuse.Status {
	_, status := fs.call(Request{Operation: "syncdir", Node: input.NodeId})
	return status
}

func (fs *guestFS) OpenDir(_ <-chan struct{}, input *fuse.OpenIn, out *fuse.OpenOut) fuse.Status {
	reply, status := fs.call(Request{Operation: "readdir", Node: input.NodeId})
	if status != fuse.OK {
		return status
	}
	entries := make([]fuse.DirEntry, 0, len(reply.Entries)+2)
	entries = append(entries, fuse.DirEntry{Name: ".", Mode: unix.S_IFDIR, Ino: input.NodeId}, fuse.DirEntry{Name: "..", Mode: unix.S_IFDIR})
	for _, entry := range reply.Entries {
		entries = append(entries, fuse.DirEntry{Name: string(entry.Name), Mode: entry.Mode})
	}
	fs.dirMu.Lock()
	defer fs.dirMu.Unlock()
	if len(fs.directories) >= 4096 {
		return fuse.Status(unix.EMFILE)
	}
	fs.nextDir++
	fs.directories[fs.nextDir] = entries
	out.Fh = fs.nextDir
	return fuse.OK
}

func (fs *guestFS) ReadDir(_ <-chan struct{}, input *fuse.ReadIn, out *fuse.DirEntryList) fuse.Status {
	fs.dirMu.Lock()
	entries, found := fs.directories[input.Fh]
	fs.dirMu.Unlock()
	if !found {
		return fuse.EBADF
	}
	if input.Offset > uint64(len(entries)) {
		return fuse.EINVAL
	}
	for _, entry := range entries[input.Offset:] {
		if !out.AddDirEntry(entry) {
			break
		}
	}
	return fuse.OK
}

func (fs *guestFS) ReleaseDir(input *fuse.ReleaseIn) {
	fs.dirMu.Lock()
	delete(fs.directories, input.Fh)
	fs.dirMu.Unlock()
}

func (fs *guestFS) StatFs(_ <-chan struct{}, _ *fuse.InHeader, out *fuse.StatfsOut) fuse.Status {
	reply, status := fs.call(Request{Operation: "statfs"})
	if status != fuse.OK {
		return status
	}
	if reply.Statfs == nil {
		return fuse.EIO
	}
	stat := reply.Statfs
	out.Blocks, out.Bfree, out.Bavail, out.Files, out.Ffree = stat.Blocks, stat.Bfree, stat.Bavail, stat.Files, stat.Ffree
	out.Bsize, out.Frsize, out.NameLen = uint32(stat.Bsize), uint32(stat.Frsize), uint32(stat.Namelen)
	return fuse.OK
}
