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

// Package volfs supplies descriptor-based access to explicitly exported Pod
// volumes. Descendant operations never resolve caller-provided host paths.
package volfs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	// Root identifies the export root. A single-file export exposes root/file.
	Root uint64 = 1
	// MaxIO bounds one file operation; the transport may split larger requests.
	MaxIO = 1 << 20
)

// Limits bound descriptors and identity metadata owned by one export.
type Limits struct {
	Nodes      int
	OpenFiles  int
	Identities int
}

// Credentials describe the caller in the guest's initial user namespace.
// Namespace ID mapping must be applied before calling these methods.
type Credentials struct{ UID, GID uint32 }

// Attr is filesystem metadata independent of host descriptor numbers.
type Attr struct {
	Inode, Size, Blocks, Links, Device uint64
	Mode, UID, GID, BlockSize          uint32
	ATime, MTime, CTime                unix.Timespec
}

// Entry binds a stable node identifier to the inode's current metadata.
type Entry struct {
	Node uint64
	Attr Attr
}

// Dirent contains a name and its Unix file type. Names remain byte sequences,
// including names that are not valid UTF-8.
type Dirent struct {
	Name []byte
	Mode uint32
}

type identity struct{ device, inode uint64 }
type node struct {
	fd      int
	key     identity
	lookups uint64
	opens   uint64
	pinned  bool
}
type openFile struct {
	fd    int
	node  uint64
	flags int
}

// Export owns the host descriptors for a directory or a single regular file.
// It does not mount a filesystem or make any path available to a VM by itself.
// The caller supplies guest VFS permission enforcement and namespace ID mapping;
// this layer enforces export boundaries, read-only policy and supported IO types.
type Export struct {
	mu           sync.Mutex
	readOnly     bool
	fileOnly     bool
	closed       bool
	limits       Limits
	nextNode     uint64
	nextFile     uint64
	nodes        map[uint64]*node
	ids          map[identity]uint64
	files        map[uint64]openFile
	pendingFiles int
	rootAttr     Attr
}

// OpenExport resolves the administrator-selected source once. Source symlinks
// are allowed, matching bind-mount semantics; subsequent symlinks are not followed.
func OpenExport(source string, readOnly bool, limits Limits) (*Export, error) {
	fd, err := unix.Open(source, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR && st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, unix.EOPNOTSUPP
	}
	if limits.Nodes <= 0 {
		limits.Nodes = 65536
	}
	if limits.OpenFiles <= 0 {
		limits.OpenFiles = 4096
	}
	if limits.Identities <= 0 {
		limits.Identities = 1 << 20
	}
	e := &Export{
		readOnly: readOnly, fileOnly: st.Mode&unix.S_IFMT == unix.S_IFREG,
		limits: limits, nextNode: Root, nodes: make(map[uint64]*node),
		ids: make(map[identity]uint64), files: make(map[uint64]openFile),
	}
	id := Root
	if e.fileOnly {
		if limits.Nodes < 2 {
			_ = unix.Close(fd)
			return nil, unix.ENFILE
		}
		e.rootAttr = Attr{Inode: Root, Mode: unix.S_IFDIR | 0555, Links: 2, UID: st.Uid, GID: st.Gid, BlockSize: 4096}
		e.nodes[Root] = &node{fd: -1, pinned: true}
		id++
		e.nextNode = id
	}
	key := identity{st.Dev, st.Ino}
	e.nodes[id] = &node{fd: fd, key: key, pinned: true}
	e.ids[key] = id
	return e, nil
}

func validName(name []byte) bool {
	s := string(name)
	return len(name) > 0 && len(name) <= 255 && s != "." && s != ".." && !strings.ContainsAny(s, "/\x00")
}

func dupFD(fd int) (int, error) {
	return unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
}

func (e *Export) nodeFD(id uint64) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return -1, unix.EBADF
	}
	n := e.nodes[id]
	if n == nil {
		return -1, unix.ESTALE
	}
	if n.fd < 0 {
		return -1, unix.ENOTDIR
	}
	return dupFD(n.fd)
}

func (e *Export) dirFD(id uint64) (int, error) {
	fd, err := e.nodeFD(id)
	if err != nil {
		return -1, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		if err != nil {
			return -1, err
		}
		return -1, unix.ENOTDIR
	}
	return fd, nil
}

func fromStat(id uint64, st *unix.Stat_t) Attr {
	return Attr{Inode: id, Size: uint64(st.Size), Blocks: uint64(st.Blocks), Links: st.Nlink,
		Device: st.Rdev, Mode: st.Mode, UID: st.Uid, GID: st.Gid, BlockSize: uint32(st.Blksize),
		ATime: st.Atim, MTime: st.Mtim, CTime: st.Ctim}
}

// adoptNode takes ownership of fd, including on failure.
func (e *Export) adoptNode(fd int) (Entry, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return Entry{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		_ = unix.Close(fd)
		return Entry{}, unix.EBADF
	}
	key := identity{st.Dev, st.Ino}
	id, known := e.ids[key]
	if !known {
		if len(e.ids) >= e.limits.Identities || len(e.nodes) >= e.limits.Nodes {
			_ = unix.Close(fd)
			return Entry{}, unix.ENFILE
		}
		e.nextNode++
		id = e.nextNode
		e.ids[key] = id
	}
	if n := e.nodes[id]; n != nil {
		_ = unix.Close(fd)
		n.lookups++
	} else {
		if len(e.nodes) >= e.limits.Nodes {
			_ = unix.Close(fd)
			return Entry{}, unix.ENFILE
		}
		e.nodes[id] = &node{fd: fd, key: key, lookups: 1}
	}
	return Entry{Node: id, Attr: fromStat(id, &st)}, nil
}

// Lookup resolves one name from an already authorized directory descriptor.
// Symlinks are returned as symlinks, for resolution in the guest namespace.
func (e *Export) Lookup(parent uint64, name []byte) (Entry, error) {
	if !validName(name) {
		return Entry{}, unix.EINVAL
	}
	if e.fileOnly && parent == Root {
		if string(name) != "file" {
			return Entry{}, unix.ENOENT
		}
		fd, err := e.nodeFD(2)
		if err != nil {
			return Entry{}, err
		}
		return e.adoptNode(fd)
	}
	parentFD, err := e.dirFD(parent)
	if err != nil {
		return Entry{}, err
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(parentFD, string(name), unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Entry{}, err
	}
	return e.adoptNode(fd)
}

// Getattr reads the inode, including an inode that has been renamed or unlinked.
func (e *Export) Getattr(id uint64) (Attr, error) {
	if e.fileOnly && id == Root {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.closed {
			return Attr{}, unix.EBADF
		}
		return e.rootAttr, nil
	}
	fd, err := e.nodeFD(id)
	if err != nil {
		return Attr{}, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return Attr{}, err
	}
	return fromStat(id, &st), nil
}

// Readlink reads the symlink itself and never resolves its target on the host.
func (e *Export) Readlink(id uint64) ([]byte, error) {
	fd, err := e.nodeFD(id)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	buf := make([]byte, 4096)
	n, err := unix.Readlinkat(fd, "", buf)
	if err != nil {
		return nil, err
	}
	if n == len(buf) {
		return nil, unix.ENAMETOOLONG
	}
	return buf[:n], nil
}

func writeFlags(flags int) bool {
	return flags&unix.O_ACCMODE != unix.O_RDONLY || flags&(unix.O_TRUNC|unix.O_CREAT) != 0
}

func (e *Export) reserveFile() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return unix.EBADF
	}
	if len(e.files)+e.pendingFiles >= e.limits.OpenFiles {
		return unix.EMFILE
	}
	e.pendingFiles++
	return nil
}

func (e *Export) cancelFileReservation() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pendingFiles--
}

func (e *Export) pinNode(id uint64) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return -1, unix.EBADF
	}
	n := e.nodes[id]
	if n == nil {
		return -1, unix.ESTALE
	}
	if n.fd < 0 {
		return -1, unix.EISDIR
	}
	fd, err := dupFD(n.fd)
	if err == nil {
		n.opens++
	}
	return fd, err
}

func (e *Export) unpinNode(id uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if n := e.nodes[id]; n != nil {
		n.opens--
		e.dropNode(id, n)
	}
}

// addFile consumes one reservation and takes ownership of fd.
func (e *Export) addFile(fd int, id uint64, flags int) (uint64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pendingFiles--
	if e.closed {
		_ = unix.Close(fd)
		return 0, unix.EBADF
	}
	n := e.nodes[id]
	if n == nil {
		_ = unix.Close(fd)
		return 0, unix.ESTALE
	}
	e.nextFile++
	e.files[e.nextFile] = openFile{fd: fd, node: id, flags: flags}
	n.opens++
	return e.nextFile, nil
}

// Open opens an authorized inode, rather than resolving a pathname again.
func (e *Export) Open(id uint64, flags int) (uint64, error) {
	if e.readOnly && writeFlags(flags) {
		return 0, unix.EROFS
	}
	// Creation is a separate operation; a caller cannot substitute a proc path
	// or ask open() to act on a directory, device, FIFO or symlink.
	if flags&(unix.O_CREAT|unix.O_EXCL|unix.O_TMPFILE|unix.O_PATH) != 0 {
		return 0, unix.EINVAL
	}
	if err := e.reserveFile(); err != nil {
		return 0, err
	}
	reserved := true
	defer func() {
		if reserved {
			e.cancelFileReservation()
		}
	}()
	fd, err := e.pinNode(id)
	if err != nil {
		return 0, err
	}
	defer unix.Close(fd)
	defer e.unpinNode(id)
	opened, err := reopenFile(fd, flags)
	if err != nil {
		return 0, err
	}
	reserved = false
	return e.addFile(opened, id, flags)
}

func procFD(fd int) string { return "/proc/self/fd/" + strconv.Itoa(fd) }

func reopenFile(fd, flags int) (int, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return -1, err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return -1, unix.ELOOP
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return -1, unix.EOPNOTSUPP
	}
	// The descriptor pins a verified regular inode. O_NOFOLLOW applies to the
	// original name, not to this internal proc descriptor link.
	return unix.Open(procFD(fd), (flags&^unix.O_NOFOLLOW)|unix.O_CLOEXEC, 0)
}

func initializeCreated(fd int, mode uint32, cred Credentials) error {
	if err := unix.Fchown(fd, int(cred.UID), int(cred.GID)); err != nil {
		return err
	}
	// Apply the guest's already-masked mode independently of the host umask.
	return unix.Fchmod(fd, mode&07777)
}

func (e *Export) creationParent(parent uint64, mode uint32, cred Credentials, directory bool) (int, uint32, Credentials, error) {
	if e.readOnly {
		return -1, 0, cred, unix.EROFS
	}
	if e.fileOnly {
		return -1, 0, cred, unix.EPERM
	}
	fd, err := e.dirFD(parent)
	if err != nil {
		return -1, 0, cred, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return -1, 0, cred, err
	}
	if st.Mode&unix.S_ISGID != 0 {
		cred.GID = st.Gid
		if directory {
			mode |= unix.S_ISGID
		}
	}
	return fd, mode, cred, nil
}

// Create creates a regular file and returns a lookup reference and open handle.
// Existing files are reopened by descriptor; their owner and mode are preserved.
func (e *Export) Create(parent uint64, name []byte, flags int, mode uint32, cred Credentials) (Entry, uint64, error) {
	if !validName(name) || flags&(unix.O_TMPFILE|unix.O_PATH) != 0 {
		return Entry{}, 0, unix.EINVAL
	}
	parentFD, mode, cred, err := e.creationParent(parent, mode, cred, false)
	if err != nil {
		return Entry{}, 0, err
	}
	defer unix.Close(parentFD)
	if err := e.reserveFile(); err != nil {
		return Entry{}, 0, err
	}
	reserved := true
	defer func() {
		if reserved {
			e.cancelFileReservation()
		}
	}()
	fd, err := unix.Openat(parentFD, string(name), flags|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if errors.Is(err, unix.EEXIST) && flags&unix.O_EXCL == 0 {
		pathFD, pathErr := unix.Openat(parentFD, string(name), unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if pathErr != nil {
			return Entry{}, 0, pathErr
		}
		fd, err = reopenFile(pathFD, flags&^(unix.O_CREAT|unix.O_EXCL))
		_ = unix.Close(pathFD)
	} else if err == nil {
		if initErr := initializeCreated(fd, mode, cred); initErr != nil {
			_ = unix.Close(fd)
			return Entry{}, 0, initErr
		}
	}
	if err != nil {
		return Entry{}, 0, err
	}
	pathFD, err := dupFD(fd)
	if err != nil {
		_ = unix.Close(fd)
		return Entry{}, 0, err
	}
	entry, err := e.adoptNode(pathFD)
	if err != nil {
		_ = unix.Close(fd)
		return Entry{}, 0, err
	}
	reserved = false
	handle, err := e.addFile(fd, entry.Node, flags)
	if err != nil {
		e.Forget(entry.Node, 1)
		return Entry{}, 0, err
	}
	return entry, handle, nil
}

// Mkdir creates a directory with the guest owner and mode.
func (e *Export) Mkdir(parent uint64, name []byte, mode uint32, cred Credentials) (Entry, error) {
	if !validName(name) {
		return Entry{}, unix.EINVAL
	}
	parentFD, mode, cred, err := e.creationParent(parent, mode, cred, true)
	if err != nil {
		return Entry{}, err
	}
	defer unix.Close(parentFD)
	if err := unix.Mkdirat(parentFD, string(name), 0700); err != nil {
		return Entry{}, err
	}
	fd, err := unix.Openat(parentFD, string(name), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Entry{}, err
	}
	if err := initializeCreated(fd, mode, cred); err != nil {
		_ = unix.Close(fd)
		return Entry{}, err
	}
	return e.adoptNode(fd)
}

// Symlink stores the target verbatim. Its resolution belongs to the guest VFS.
func (e *Export) Symlink(parent uint64, name, target []byte, cred Credentials) (Entry, error) {
	if !validName(name) || len(target) == 0 || len(target) >= 4096 || strings.IndexByte(string(target), 0) >= 0 {
		return Entry{}, unix.EINVAL
	}
	parentFD, _, cred, err := e.creationParent(parent, 0, cred, false)
	if err != nil {
		return Entry{}, err
	}
	defer unix.Close(parentFD)
	if err := unix.Symlinkat(string(target), parentFD, string(name)); err != nil {
		return Entry{}, err
	}
	fd, err := unix.Openat(parentFD, string(name), unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Entry{}, err
	}
	if err := unix.Fchownat(fd, "", int(cred.UID), int(cred.GID), unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = unix.Close(fd)
		return Entry{}, err
	}
	return e.adoptNode(fd)
}

// Link adds a hard link to an existing authorized inode.
func (e *Export) Link(id, parent uint64, name []byte) (Entry, error) {
	if !validName(name) {
		return Entry{}, unix.EINVAL
	}
	parentFD, _, _, err := e.creationParent(parent, 0, Credentials{}, false)
	if err != nil {
		return Entry{}, err
	}
	defer unix.Close(parentFD)
	fd, err := e.nodeFD(id)
	if err != nil {
		return Entry{}, err
	}
	defer unix.Close(fd)
	if err := unix.Linkat(fd, "", parentFD, string(name), unix.AT_EMPTY_PATH); err != nil {
		return Entry{}, err
	}
	linked, err := dupFD(fd)
	if err != nil {
		return Entry{}, err
	}
	return e.adoptNode(linked)
}

// ReadDir returns a bounded directory snapshot. No lookup references are added.
// The guest holds its own stream cursor across subsequent readdir requests.
func (e *Export) ReadDir(id uint64) ([]Dirent, error) {
	if e.fileOnly && id == Root {
		if _, err := e.Getattr(Root); err != nil {
			return nil, err
		}
		return []Dirent{{Name: []byte("file"), Mode: unix.S_IFREG}}, nil
	}
	pathFD, err := e.dirFD(id)
	if err != nil {
		return nil, err
	}
	defer unix.Close(pathFD)
	fd, err := unix.Open(procFD(pathFD), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), "volume directory")
	defer dir.Close()
	var result []Dirent
	total := 0
	for {
		entries, err := dir.ReadDir(256)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		for _, entry := range entries {
			total += len(entry.Name()) + 32
			if total > MaxIO || len(result) >= e.limits.Identities {
				return nil, unix.E2BIG
			}
			mode := uint32(unix.S_IFREG)
			switch entry.Type() & os.ModeType {
			case os.ModeDir:
				mode = unix.S_IFDIR
			case os.ModeSymlink:
				mode = unix.S_IFLNK
			case os.ModeNamedPipe:
				mode = unix.S_IFIFO
			case os.ModeSocket:
				mode = unix.S_IFSOCK
			case os.ModeDevice:
				mode = unix.S_IFBLK
			case os.ModeDevice | os.ModeCharDevice:
				mode = unix.S_IFCHR
			}
			result = append(result, Dirent{Name: []byte(entry.Name()), Mode: mode})
		}
		if errors.Is(err, io.EOF) {
			return result, nil
		}
	}
}

// SetAttr describes changes requested by the guest. Nil fields stay unchanged.
type SetAttr struct {
	Mode, UID, GID *uint32
	Size           *uint64
	ATime, MTime   *unix.Timespec
}

// Setattr changes an authorized inode without following its name or a symlink.
// A nonzero handle permits truncation using the existing open description.
func (e *Export) Setattr(id, handle uint64, change SetAttr) (Attr, error) {
	if e.readOnly {
		return Attr{}, unix.EROFS
	}
	fd, err := e.nodeFD(id)
	if err != nil {
		return Attr{}, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return Attr{}, err
	}
	if change.UID != nil || change.GID != nil {
		uid, gid := -1, -1
		if change.UID != nil {
			uid = int(*change.UID)
		}
		if change.GID != nil {
			gid = int(*change.GID)
		}
		if err := unix.Fchownat(fd, "", uid, gid, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return Attr{}, err
		}
	}
	if change.Mode != nil {
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			return Attr{}, unix.EOPNOTSUPP
		}
		if err := unix.Chmod(procFD(fd), *change.Mode&07777); err != nil {
			return Attr{}, err
		}
	}
	if change.Size != nil {
		if *change.Size > ^uint64(0)>>1 {
			return Attr{}, unix.EFBIG
		}
		var ioFD int
		if handle != 0 {
			ioFD, err = e.fileForNode(handle, id)
		} else {
			ioFD, err = reopenFile(fd, unix.O_WRONLY)
		}
		if err != nil {
			return Attr{}, err
		}
		err = unix.Ftruncate(ioFD, int64(*change.Size))
		_ = unix.Close(ioFD)
		if err != nil {
			return Attr{}, err
		}
	}
	if change.ATime != nil || change.MTime != nil {
		times := []unix.Timespec{{Nsec: unix.UTIME_OMIT}, {Nsec: unix.UTIME_OMIT}}
		if change.ATime != nil {
			times[0] = *change.ATime
		}
		if change.MTime != nil {
			times[1] = *change.MTime
		}
		if err := unix.UtimesNanoAt(fd, "", times, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return Attr{}, err
		}
	}
	if err := unix.Fstat(fd, &st); err != nil {
		return Attr{}, err
	}
	return fromStat(id, &st), nil
}

func (e *Export) fileForNode(handle, id uint64) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return -1, unix.EBADF
	}
	f, ok := e.files[handle]
	if !ok || f.node != id {
		return -1, unix.EBADF
	}
	return dupFD(f.fd)
}

// Statfs reports the host backing filesystem for capacity checks in the guest.
func (e *Export) Statfs() (unix.Statfs_t, error) {
	id := Root
	if e.fileOnly {
		id = 2
	}
	fd, err := e.nodeFD(id)
	if err != nil {
		return unix.Statfs_t{}, err
	}
	defer unix.Close(fd)
	var st unix.Statfs_t
	err = unix.Fstatfs(fd, &st)
	return st, err
}

func (e *Export) fileFD(id uint64) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return -1, unix.EBADF
	}
	f, ok := e.files[id]
	if !ok {
		return -1, unix.EBADF
	}
	return dupFD(f.fd)
}

// Read performs bounded, positional IO without changing the shared file offset.
func (e *Export) Read(handle uint64, offset int64, size int) ([]byte, error) {
	if size < 0 || size > MaxIO || offset < 0 {
		return nil, unix.EINVAL
	}
	fd, err := e.fileFD(handle)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	data := make([]byte, size)
	n, err := unix.Pread(fd, data, offset)
	if err != nil {
		return nil, err
	}
	return data[:n], nil
}

// Write preserves the open description's O_APPEND semantics on Linux.
func (e *Export) Write(handle uint64, offset int64, data []byte) (int, error) {
	if e.readOnly {
		return 0, unix.EROFS
	}
	if len(data) > MaxIO || offset < 0 {
		return 0, unix.EINVAL
	}
	fd, err := e.fileFD(handle)
	if err != nil {
		return 0, err
	}
	defer unix.Close(fd)
	return unix.Pwrite(fd, data, offset)
}

// Sync makes completed writes durable in the exported backing filesystem.
func (e *Export) Sync(handle uint64, dataOnly bool) error {
	fd, err := e.fileFD(handle)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if dataOnly {
		return unix.Fdatasync(fd)
	}
	return unix.Fsync(fd)
}

// Release closes one open description. Kernel lookup references remain valid.
func (e *Export) Release(handle uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	f, ok := e.files[handle]
	if !ok {
		return unix.EBADF
	}
	delete(e.files, handle)
	err := unix.Close(f.fd)
	n := e.nodes[f.node]
	n.opens--
	e.dropNode(f.node, n)
	return err
}

func (e *Export) dropNode(id uint64, n *node) {
	if !n.pinned && n.lookups == 0 && n.opens == 0 {
		_ = unix.Close(n.fd)
		delete(e.nodes, id)
		// No guest references remain. Dropping the identity also avoids treating
		// a later inode-number reuse as the original, already forgotten inode.
		delete(e.ids, n.key)
	}
}

// Forget drops kernel lookup references without invalidating open files.
func (e *Export) Forget(id, count uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if n := e.nodes[id]; n != nil {
		if count > n.lookups {
			count = n.lookups
		}
		n.lookups -= count
		e.dropNode(id, n)
	}
}

// Rename changes directory entries while preserving existing node/open handles.
func (e *Export) Rename(parent uint64, name []byte, newParent uint64, newName []byte, flags uint32) error {
	if e.readOnly {
		return unix.EROFS
	}
	if !validName(name) || !validName(newName) {
		return unix.EINVAL
	}
	if e.fileOnly {
		return unix.EPERM
	}
	if flags & ^uint32(unix.RENAME_NOREPLACE|unix.RENAME_EXCHANGE) != 0 {
		return unix.EINVAL
	}
	oldFD, err := e.dirFD(parent)
	if err != nil {
		return err
	}
	defer unix.Close(oldFD)
	newFD, err := e.dirFD(newParent)
	if err != nil {
		return err
	}
	defer unix.Close(newFD)
	return unix.Renameat2(oldFD, string(name), newFD, string(newName), uint(flags))
}

// Unlink removes one entry; open descriptions continue to refer to its inode.
func (e *Export) Unlink(parent uint64, name []byte, directory bool) error {
	if e.readOnly {
		return unix.EROFS
	}
	if !validName(name) {
		return unix.EINVAL
	}
	if e.fileOnly {
		return unix.EPERM
	}
	fd, err := e.dirFD(parent)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	flags := 0
	if directory {
		flags = unix.AT_REMOVEDIR
	}
	return unix.Unlinkat(fd, string(name), flags)
}

// Close releases every descriptor owned by the export. In-flight operations use
// duplicates and can finish safely; subsequent operations fail with EBADF.
func (e *Export) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	var result error
	for id, f := range e.files {
		result = errors.Join(result, unix.Close(f.fd))
		delete(e.files, id)
	}
	for id, n := range e.nodes {
		if n.fd >= 0 {
			result = errors.Join(result, unix.Close(n.fd))
		}
		delete(e.nodes, id)
	}
	e.ids = nil
	return result
}

// String identifies an export without disclosing host source paths.
func (e *Export) String() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return fmt.Sprintf("volume export (file=%t, readonly=%t, nodes=%d, handles=%d)", e.fileOnly, e.readOnly, len(e.nodes), len(e.files))
}
