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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const checkpointVersion = 1

// CheckpointLimits bound one VM's volume snapshot, including all exports.
// Zero fields select the defaults. Checkpoint directories are private runtime
// artifacts; callers quiesce all writers, including writers outside the VM.
type CheckpointLimits struct {
	Objects int
	Bytes   int64
}

func (l CheckpointLimits) defaults() CheckpointLimits {
	if l.Objects <= 0 {
		l.Objects = 1 << 20
	}
	if l.Bytes <= 0 {
		l.Bytes = 64 << 30
	}
	return l
}

// FrozenServer excludes registration, revocation and RPC execution until Close.
// Transport connections remain alive so the source can continue after capture.
type FrozenServer struct {
	server *Server
	once   sync.Once
	mu     sync.Mutex
	closed bool
}

// Freeze drains active RPCs and is cancellable while waiting for them. Pause
// guest execution before capture; freezing the server alone cannot stop guest
// memory or unrelated host writers from changing.
func (s *Server) Freeze(ctx context.Context) (*FrozenServer, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.checkpoint.TryLock() {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				s.checkpoint.Unlock()
				return nil, unix.EBADF
			}
			return &FrozenServer{server: s}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Close permits the source's pending requests to continue. It does not revoke
// exports or close any source descriptors or connections.
func (f *FrozenServer) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.once.Do(func() { f.server.checkpoint.Unlock() })
	return nil
}

type volumeCheckpoint struct {
	Version int
	Objects []checkpointObject
	Exports []checkpointExport
}

type checkpointObject struct {
	ID       uint64
	Attr     Attr
	Target   []byte
	Children []checkpointName
	Digest   string
}

type checkpointName struct {
	Name   []byte
	Object uint64
}
type checkpointNode struct {
	ID, Object, Lookups, Opens uint64
	Pinned                     bool
}
type checkpointFile struct {
	ID, Node uint64
	Flags    int
}
type checkpointExport struct {
	Token, Session     string
	LastID             uint64
	LastHash           [sha256.Size]byte
	LastReply          []byte
	ReadOnly, FileOnly bool
	Limits             Limits
	NextNode, NextFile uint64
	RootAttr           Attr
	Nodes              []checkpointNode
	Files              []checkpointFile
}

type volumeCapture struct {
	ctx       context.Context
	directory string
	limits    CheckpointLimits
	bytes     int64
	keys      map[identity]uint64
	visiting  map[identity]bool
	state     volumeCheckpoint
}

func checkpointDataPath(root string, id uint64) string {
	return filepath.Join(root, "data", fmt.Sprintf("%016x", id))
}

// Save creates a new self-contained volume artifact. Directory names are stored
// as bytes, hard links and overlapping export roots share one captured object,
// and referenced unlinked inodes are captured through their retained descriptors.
// A failed capture removes only the directory this call created.
func (f *FrozenServer) Save(ctx context.Context, directory string, limits CheckpointLimits) (err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return unix.EBADF
	}
	if !filepath.IsAbs(directory) {
		return unix.EINVAL
	}
	if err = os.Mkdir(directory, 0700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(directory))
		}
	}()
	if err = os.Mkdir(filepath.Join(directory, "data"), 0700); err != nil {
		return err
	}
	capture := &volumeCapture{ctx: ctx, directory: directory, limits: limits.defaults(), keys: make(map[identity]uint64), visiting: make(map[identity]bool), state: volumeCheckpoint{Version: checkpointVersion}}
	f.server.mu.Lock()
	tokens := make([]string, 0, len(f.server.exports))
	for token := range f.server.exports {
		tokens = append(tokens, token)
	}
	f.server.mu.Unlock()
	sort.Strings(tokens)
	for _, token := range tokens {
		entry := f.server.exports[token]
		if err = capture.export(token, entry); err != nil {
			return err
		}
	}
	if err = validateVolumeCheckpoint(&capture.state, capture.limits); err != nil {
		return err
	}
	data, err := json.Marshal(capture.state)
	if err != nil {
		return err
	}
	if len(data) > 128<<20 {
		return unix.E2BIG
	}
	file, err := os.OpenFile(filepath.Join(directory, "state.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	for _, path := range []string{filepath.Join(directory, "data"), directory, filepath.Dir(directory)} {
		if err = syncDirectory(path); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func syncDirectory(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return unix.Fsync(fd)
}

func (c *volumeCapture) export(token string, served *servedExport) error {
	served.mu.Lock()
	defer served.mu.Unlock()
	e := served.export
	e.mu.Lock()
	defer e.mu.Unlock()
	if served.closed || e.closed || e.pendingFiles != 0 {
		return unix.EBUSY
	}
	state := checkpointExport{Token: token, Session: served.session, LastID: served.lastID, LastHash: served.lastHash, LastReply: append([]byte(nil), served.lastReply...), ReadOnly: e.readOnly, FileOnly: e.fileOnly, Limits: e.limits, NextNode: e.nextNode, NextFile: e.nextFile, RootAttr: e.rootAttr}
	ids := make([]uint64, 0, len(e.nodes))
	for id := range e.nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		n := e.nodes[id]
		object := uint64(0)
		if n.fd >= 0 {
			var err error
			object, err = c.object(n.fd, 0)
			if err != nil {
				return err
			}
		}
		state.Nodes = append(state.Nodes, checkpointNode{ID: id, Object: object, Lookups: n.lookups, Opens: n.opens, Pinned: n.pinned})
	}
	for id, file := range e.files {
		state.Files = append(state.Files, checkpointFile{ID: id, Node: file.node, Flags: file.flags})
	}
	sort.Slice(state.Files, func(i, j int) bool { return state.Files[i].ID < state.Files[j].ID })
	c.state.Exports = append(c.state.Exports, state)
	return nil
}

// containsOutput detects even an export of an ancestor of the artifact store.
// Following source symlinks is never needed to perform this check.
func (c *volumeCapture) containsOutput(key identity) (bool, error) {
	path := c.directory
	for {
		var st unix.Stat_t
		if err := unix.Stat(path, &st); err != nil {
			return false, err
		}
		if (identity{st.Dev, st.Ino}) == key {
			return true, nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false, nil
		}
		path = parent
	}
}

func (c *volumeCapture) object(fd, depth int) (uint64, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if depth > 256 {
		return 0, unix.ELOOP
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return 0, err
	}
	key := identity{st.Dev, st.Ino}
	if c.visiting[key] {
		return 0, unix.ELOOP
	}
	if id := c.keys[key]; id != 0 {
		return id, nil
	}
	if len(c.state.Objects) >= c.limits.Objects {
		return 0, unix.ENFILE
	}
	id := uint64(len(c.state.Objects) + 1)
	object := checkpointObject{ID: id, Attr: fromStat(id, &st)}
	c.keys[key] = id
	c.state.Objects = append(c.state.Objects, object)
	c.visiting[key] = true
	defer delete(c.visiting, key)
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		if st.Size < 0 || st.Size > c.limits.Bytes-c.bytes {
			return 0, unix.EFBIG
		}
		c.bytes += st.Size
		inputFD, err := reopenFile(fd, unix.O_RDONLY)
		if err != nil {
			return 0, err
		}
		input := os.NewFile(uintptr(inputFD), "checkpoint-source")
		defer input.Close()
		output, err := os.OpenFile(checkpointDataPath(c.directory, id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return 0, err
		}
		hash := sha256.New()
		err = copyCheckpointFile(c.ctx, output, io.TeeReader(input, hash), st.Size)
		if err == nil {
			err = output.Sync()
		}
		err = errors.Join(err, output.Close())
		if err != nil {
			return 0, err
		}
		object.Digest = hex.EncodeToString(hash.Sum(nil))
	case unix.S_IFDIR:
		inside, err := c.containsOutput(key)
		if err != nil {
			return 0, err
		}
		if inside {
			return 0, fmt.Errorf("snapshot output is inside an exported directory: %w", unix.EINVAL)
		}
		dirFD, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return 0, err
		}
		dir := os.NewFile(uintptr(dirFD), "checkpoint-directory")
		entries, err := dir.ReadDir(-1)
		if closeErr := dir.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return 0, err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			name := []byte(entry.Name())
			if !validName(name) {
				return 0, unix.EINVAL
			}
			childFD, err := unix.Openat(fd, string(name), unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return 0, err
			}
			child, err := c.object(childFD, depth+1)
			_ = unix.Close(childFD)
			if err != nil {
				return 0, err
			}
			object.Children = append(object.Children, checkpointName{Name: name, Object: child})
		}
	case unix.S_IFLNK:
		buffer := make([]byte, 4096)
		n, err := unix.Readlinkat(fd, "", buffer)
		if err != nil {
			return 0, err
		}
		if n == len(buffer) {
			return 0, unix.ENAMETOOLONG
		}
		object.Target = append([]byte(nil), buffer[:n]...)
	default:
		return 0, unix.EOPNOTSUPP
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return 0, err
	}
	if after.Size != st.Size || after.Mtim != st.Mtim || after.Ctim != st.Ctim || after.Nlink != st.Nlink {
		return 0, unix.EBUSY
	}
	c.state.Objects[id-1] = object
	return id, nil
}

func copyCheckpointFile(ctx context.Context, output *os.File, input io.Reader, size int64) error {
	buffer := make([]byte, MaxIO)
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := int64(len(buffer))
		if size-offset < chunk {
			chunk = size - offset
		}
		n, err := io.ReadFull(input, buffer[:chunk])
		if err != nil {
			return err
		}
		zero := true
		for _, b := range buffer[:n] {
			if b != 0 {
				zero = false
				break
			}
		}
		if !zero {
			if _, err := output.WriteAt(buffer[:n], offset); err != nil {
				return err
			}
		}
		offset += int64(n)
	}
	return output.Truncate(size)
}

// RestoreServer reconstructs capabilities, sessions, replay records, node IDs
// and open handles into a new server. Every writable object is copied into a
// fresh private workspace, preserving hard links shared by multiple exports.
// The returned server owns descriptors; its caller owns the workspace lifetime.
func RestoreServer(ctx context.Context, artifact, workspace string, limits CheckpointLimits) (_ *Server, err error) {
	if !filepath.IsAbs(artifact) || !filepath.IsAbs(workspace) {
		return nil, unix.EINVAL
	}
	file, err := os.OpenFile(filepath.Join(artifact, "state.json"), os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128<<20 {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, unix.E2BIG
	}
	decoder := json.NewDecoder(io.LimitReader(file, 128<<20+1))
	decoder.DisallowUnknownFields()
	var state volumeCheckpoint
	err = decoder.Decode(&state)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = unix.EINVAL
		}
	}
	_ = file.Close()
	if err != nil {
		return nil, err
	}
	limits = limits.defaults()
	if err := validateVolumeCheckpoint(&state, limits); err != nil {
		return nil, err
	}
	if err = os.Mkdir(workspace, 0700); err != nil {
		return nil, err
	}
	server := NewServer()
	defer func() {
		if err != nil {
			_ = server.Close()
			err = errors.Join(err, os.RemoveAll(workspace))
		}
	}()
	paths, err := restoreObjects(ctx, artifact, workspace, &state)
	if err != nil {
		return nil, err
	}
	for _, saved := range state.Exports {
		if err = server.restoreExport(saved, paths); err != nil {
			return nil, err
		}
	}
	// Canonical file links are private construction scaffolding. Drop them once
	// all retained (including unlinked) node/open descriptors have been rebuilt.
	if err = os.Mkdir(filepath.Join(workspace, "refs"), 0700); err != nil {
		return nil, err
	}
	for _, object := range state.Objects {
		if object.Attr.Mode&unix.S_IFMT == unix.S_IFDIR {
			if object.Attr.Links == 0 {
				if err = os.Remove(paths[object.ID]); err != nil {
					return nil, err
				}
			}
			continue
		}
		var st unix.Stat_t
		if err = unix.Lstat(paths[object.ID], &st); err != nil {
			return nil, err
		}
		if object.Attr.Links < st.Nlink-1 {
			return nil, unix.EINVAL
		}
		extra := object.Attr.Links - (st.Nlink - 1)
		keep := extra > 0
		for i := uint64(1); i < extra; i++ {
			if err = os.Link(paths[object.ID], filepath.Join(workspace, "refs", fmt.Sprintf("%016x-%016x", object.ID, i))); err != nil {
				return nil, err
			}
		}
		if !keep {
			if err = os.Remove(paths[object.ID]); err != nil {
				return nil, err
			}
		}
	}
	return server, nil
}

func validateVolumeCheckpoint(state *volumeCheckpoint, limits CheckpointLimits) error {
	if state.Version != checkpointVersion || len(state.Objects) > limits.Objects || len(state.Exports) > maxExports {
		return unix.EINVAL
	}
	var bytes int64
	var links uint64
	parents := make(map[uint64]uint64)
	for index, object := range state.Objects {
		if object.ID != uint64(index+1) {
			return unix.EINVAL
		}
		switch object.Attr.Mode & unix.S_IFMT {
		case unix.S_IFREG:
			if len(object.Children) != 0 || len(object.Target) != 0 {
				return unix.EINVAL
			}
			if object.Attr.Size > uint64(limits.Bytes-bytes) || len(object.Digest) != sha256.Size*2 {
				return unix.EFBIG
			}
			bytes += int64(object.Attr.Size)
		case unix.S_IFDIR:
			if len(object.Target) != 0 || object.Digest != "" {
				return unix.EINVAL
			}
			names := make(map[string]bool)
			for _, child := range object.Children {
				if !validName(child.Name) || child.Object == 0 || child.Object > uint64(len(state.Objects)) || names[string(child.Name)] {
					return unix.EINVAL
				}
				names[string(child.Name)] = true
				if state.Objects[child.Object-1].Attr.Mode&unix.S_IFMT == unix.S_IFDIR {
					if parents[child.Object] != 0 {
						return unix.EOPNOTSUPP
					}
					parents[child.Object] = object.ID
				}
			}
		case unix.S_IFLNK:
			if len(object.Children) != 0 || object.Digest != "" || len(object.Target) == 0 || len(object.Target) >= 4096 {
				return unix.EINVAL
			}
		default:
			return unix.EOPNOTSUPP
		}
		if object.Attr.Mode&unix.S_IFMT != unix.S_IFDIR {
			if object.Attr.Links > uint64(limits.Objects)-links {
				return unix.ENFILE
			}
			links += object.Attr.Links
		}
	}
	for id := range parents {
		seen := make(map[uint64]bool)
		for parent := id; parent != 0; parent = parents[parent] {
			if seen[parent] || len(seen) > 256 {
				return unix.ELOOP
			}
			seen[parent] = true
		}
	}
	tokens := make(map[string]bool)
	for _, export := range state.Exports {
		if !validToken(export.Token) || tokens[export.Token] || export.NextNode < Root || export.Limits.Nodes <= 0 || export.Limits.OpenFiles <= 0 || export.Limits.Identities <= 0 || len(export.Nodes) > export.Limits.Nodes || len(export.Files) > export.Limits.OpenFiles {
			return unix.EINVAL
		}
		tokens[export.Token] = true
		if export.LastID > 0 && (!validToken(export.Session) || len(export.LastReply) == 0 || len(export.LastReply) > MaxFrame) {
			return unix.EINVAL
		}
		nodes := make(map[uint64]checkpointNode)
		for _, node := range export.Nodes {
			if node.ID == 0 || node.ID > export.NextNode || nodes[node.ID].ID != 0 || node.Object > uint64(len(state.Objects)) || (node.Object == 0 && (!export.FileOnly || node.ID != Root)) {
				return unix.EINVAL
			}
			nodes[node.ID] = node
		}
		rootID := Root
		if export.FileOnly {
			rootID++
		}
		if nodes[Root].ID != Root || !nodes[Root].Pinned || !nodes[rootID].Pinned || nodes[rootID].Object == 0 {
			return unix.EINVAL
		}
		kind := state.Objects[nodes[rootID].Object-1].Attr.Mode & unix.S_IFMT
		if (export.FileOnly && kind != unix.S_IFREG) || (!export.FileOnly && kind != unix.S_IFDIR) {
			return unix.EINVAL
		}
		files := make(map[uint64]bool)
		opens := make(map[uint64]uint64)
		for _, file := range export.Files {
			if file.ID == 0 || file.ID > export.NextFile || files[file.ID] || nodes[file.Node].ID == 0 || file.Flags&(unix.O_PATH|unix.O_TMPFILE) != 0 {
				return unix.EINVAL
			}
			files[file.ID] = true
			opens[file.Node]++
		}
		for id, n := range nodes {
			if n.Opens != opens[id] {
				return unix.EBUSY
			}
		}
	}
	return nil
}

func restoreObjects(ctx context.Context, artifact, workspace string, state *volumeCheckpoint) (map[uint64]string, error) {
	for _, directory := range []string{"files", "roots"} {
		if err := os.Mkdir(filepath.Join(workspace, directory), 0700); err != nil {
			return nil, err
		}
	}
	paths := make(map[uint64]string)
	parents := make(map[uint64]checkpointName)
	parentIDs := make(map[uint64]uint64)
	for _, object := range state.Objects {
		for _, child := range object.Children {
			if state.Objects[child.Object-1].Attr.Mode&unix.S_IFMT == unix.S_IFDIR {
				parents[child.Object] = child
				parentIDs[child.Object] = object.ID
			}
		}
	}
	var directoryPath func(uint64) string
	directoryPath = func(id uint64) string {
		if path := paths[id]; path != "" {
			return path
		}
		path := filepath.Join(workspace, "roots", fmt.Sprintf("%016x", id))
		if parent := parentIDs[id]; parent != 0 {
			path = filepath.Join(directoryPath(parent), string(parents[id].Name))
		}
		paths[id] = path
		return path
	}
	for _, object := range state.Objects {
		if object.Attr.Mode&unix.S_IFMT == unix.S_IFDIR {
			directoryPath(object.ID)
		}
	}
	directories := make([]string, 0)
	for _, path := range paths {
		directories = append(directories, path)
	}
	sort.Slice(directories, func(i, j int) bool { return len(directories[i]) < len(directories[j]) })
	for _, path := range directories {
		if err := os.Mkdir(path, 0700); err != nil {
			return nil, err
		}
	}
	for _, object := range state.Objects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if object.Attr.Mode&unix.S_IFMT == unix.S_IFDIR {
			continue
		}
		path := filepath.Join(workspace, "files", fmt.Sprintf("%016x", object.ID))
		paths[object.ID] = path
		if object.Attr.Mode&unix.S_IFMT == unix.S_IFLNK {
			if err := os.Symlink(string(object.Target), path); err != nil {
				return nil, err
			}
			continue
		}
		input, err := os.OpenFile(checkpointDataPath(artifact, object.ID), os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return nil, err
		}
		info, err := input.Stat()
		if err != nil || !info.Mode().IsRegular() || uint64(info.Size()) != object.Attr.Size {
			_ = input.Close()
			if err != nil {
				return nil, err
			}
			return nil, unix.EINVAL
		}
		output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			_ = input.Close()
			return nil, err
		}
		hash := sha256.New()
		err = copyCheckpointFile(ctx, output, io.TeeReader(input, hash), int64(object.Attr.Size))
		if err == nil && hex.EncodeToString(hash.Sum(nil)) != object.Digest {
			err = fmt.Errorf("volume data checksum: %w", unix.EIO)
		}
		if err == nil {
			err = output.Sync()
		}
		err = errors.Join(err, input.Close(), output.Close())
		if err != nil {
			return nil, err
		}
	}
	for _, object := range state.Objects {
		for _, child := range object.Children {
			if state.Objects[child.Object-1].Attr.Mode&unix.S_IFMT == unix.S_IFDIR {
				continue
			}
			if err := os.Link(paths[child.Object], filepath.Join(paths[object.ID], string(child.Name))); err != nil {
				return nil, err
			}
		}
	}
	// Apply metadata only after constructing all children; chown clears setid
	// bits, so chmod follows it. Directory timestamps are restored last.
	for i := len(state.Objects) - 1; i >= 0; i-- {
		object := state.Objects[i]
		path := paths[object.ID]
		if err := os.Lchown(path, int(object.Attr.UID), int(object.Attr.GID)); err != nil {
			return nil, err
		}
		if object.Attr.Mode&unix.S_IFMT != unix.S_IFLNK {
			if err := unix.Chmod(path, object.Attr.Mode&07777); err != nil {
				return nil, err
			}
		}
		if err := unix.UtimesNanoAt(unix.AT_FDCWD, path, []unix.Timespec{object.Attr.ATime, object.Attr.MTime}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func (s *Server) restoreExport(saved checkpointExport, paths map[uint64]string) (err error) {
	rootID := Root
	if saved.FileOnly {
		rootID++
	}
	var source string
	for _, n := range saved.Nodes {
		if n.ID == rootID {
			source = paths[n.Object]
		}
	}
	e, err := OpenExport(source, saved.ReadOnly, saved.Limits)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = e.Close()
		}
	}()
	for _, n := range e.nodes {
		if n.fd >= 0 {
			_ = unix.Close(n.fd)
		}
	}
	e.nodes = make(map[uint64]*node)
	e.ids = make(map[identity]uint64)
	e.nextNode, e.nextFile, e.rootAttr = saved.NextNode, saved.NextFile, saved.RootAttr
	for _, n := range saved.Nodes {
		entry := &node{fd: -1, lookups: n.Lookups, opens: n.Opens, pinned: n.Pinned}
		if n.Object != 0 {
			entry.fd, err = unix.Open(paths[n.Object], unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			var st unix.Stat_t
			if err = unix.Fstat(entry.fd, &st); err != nil {
				_ = unix.Close(entry.fd)
				return err
			}
			entry.key = identity{st.Dev, st.Ino}
			if e.ids[entry.key] != 0 {
				_ = unix.Close(entry.fd)
				return unix.EINVAL
			}
			e.ids[entry.key] = n.ID
		}
		e.nodes[n.ID] = entry
	}
	for _, file := range saved.Files {
		fd, err := reopenFile(e.nodes[file.Node].fd, file.Flags&^(unix.O_CREAT|unix.O_EXCL|unix.O_TRUNC))
		if err != nil {
			return err
		}
		e.files[file.ID] = openFile{fd: fd, node: file.Node, flags: file.Flags}
	}
	s.exports[saved.Token] = &servedExport{export: e, session: saved.Session, lastID: saved.LastID, lastHash: saved.LastHash, lastReply: append([]byte(nil), saved.LastReply...)}
	return nil
}
