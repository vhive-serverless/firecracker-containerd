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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestCheckpointRestoresSharedTreeHandlesAndReplay(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	source := filepath.Join(base, "source")
	require.NoError(t, os.Mkdir(source, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "state"), []byte("checkpoint"), 0640))
	require.NoError(t, os.Link(filepath.Join(source, "state"), filepath.Join(source, "hardlink")))
	require.NoError(t, os.Symlink("state", filepath.Join(source, "symlink")))
	require.NoError(t, os.WriteFile(filepath.Join(source, string([]byte{'x', 0xff})), []byte("bytes"), 0600))
	one, err := OpenExport(source, false, Limits{})
	require.NoError(t, err)
	two, err := OpenExport(source, false, Limits{})
	require.NoError(t, err)
	server := NewServer()
	t.Cleanup(func() { _ = server.Close() })
	descriptor, err := server.Register(one)
	require.NoError(t, err)
	other, err := server.Register(two)
	require.NoError(t, err)
	entry, handle, err := one.Create(Root, []byte("orphan"), unix.O_RDWR, 0600, Credentials{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())})
	require.NoError(t, err)
	_, err = one.Write(handle, 0, []byte("unlinked-memory"))
	require.NoError(t, err)
	require.NoError(t, one.Unlink(Root, []byte("orphan"), false))
	session, err := randomToken()
	require.NoError(t, err)
	request := Request{Version: ProtocolVersion, Token: descriptor.Token, Session: session, ID: 1, Operation: opCreate, Parent: Root, Name: []byte("replayed"), Flags: unix.O_RDWR | unix.O_EXCL, Mode: 0600, Cred: Credentials{UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}}
	frame, err := json.Marshal(request)
	require.NoError(t, err)
	reply := server.handle(frame)
	frozen, err := server.Freeze(ctx)
	require.NoError(t, err)
	artifact := filepath.Join(base, "snapshot")
	require.NoError(t, frozen.Save(ctx, artifact, CheckpointLimits{}))
	require.NoError(t, frozen.Close())
	require.NoError(t, os.WriteFile(filepath.Join(source, "state"), []byte("source-advanced"), 0640))
	require.NoError(t, server.Close())
	require.NoError(t, os.RemoveAll(source))
	restored, err := RestoreServer(ctx, artifact, filepath.Join(base, "restored"), CheckpointLimits{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = restored.Close() })
	require.Equal(t, reply, restored.handle(frame), "the exact cached response survives host reconstruction")
	first := restored.exports[descriptor.Token].export
	second := restored.exports[other.Token].export
	data, err := first.Read(handle, 0, 100)
	require.NoError(t, err)
	require.Equal(t, "unlinked-memory", string(data))
	attr, err := first.Getattr(entry.Node)
	require.NoError(t, err)
	require.Zero(t, attr.Links)
	state, err := first.Lookup(Root, []byte("state"))
	require.NoError(t, err)
	hardlink, err := first.Lookup(Root, []byte("hardlink"))
	require.NoError(t, err)
	require.Equal(t, state.Node, hardlink.Node)
	require.Equal(t, uint64(2), hardlink.Attr.Links)
	file, err := first.Open(state.Node, unix.O_RDWR)
	require.NoError(t, err)
	data, err = first.Read(file, 0, 100)
	require.NoError(t, err)
	require.Equal(t, "checkpoint", string(data))
	_, err = first.Write(file, 0, []byte("RESTORED!!"))
	require.NoError(t, err)
	shared, err := second.Lookup(Root, []byte("state"))
	require.NoError(t, err)
	otherFile, err := second.Open(shared.Node, unix.O_RDONLY)
	require.NoError(t, err)
	data, err = second.Read(otherFile, 0, 100)
	require.NoError(t, err)
	require.Equal(t, "RESTORED!!", string(data))
	symlink, err := first.Lookup(Root, []byte("symlink"))
	require.NoError(t, err)
	target, err := first.Readlink(symlink.Node)
	require.NoError(t, err)
	require.Equal(t, []byte("state"), target)
	_, err = first.Lookup(Root, []byte{'x', 0xff})
	require.NoError(t, err)
	again, err := RestoreServer(ctx, artifact, filepath.Join(base, "another-restore"), CheckpointLimits{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = again.Close() })
	independent := again.exports[descriptor.Token].export
	state, err = independent.Lookup(Root, []byte("state"))
	require.NoError(t, err)
	file, err = independent.Open(state.Node, unix.O_RDONLY)
	require.NoError(t, err)
	data, err = independent.Read(file, 0, 100)
	require.NoError(t, err)
	require.Equal(t, "checkpoint", string(data), "restoring and modifying a clone cannot advance the artifact")
}

func TestCheckpointFreezeAndCaptureFailure(t *testing.T) {
	server := NewServer()
	t.Cleanup(func() { _ = server.Close() })
	directory := t.TempDir()
	export, err := OpenExport(directory, false, Limits{})
	require.NoError(t, err)
	_, err = server.Register(export)
	require.NoError(t, err)
	frozen, err := server.Freeze(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = frozen.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = server.Freeze(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	output := filepath.Join(directory, "recursive-output")
	require.Error(t, frozen.Save(context.Background(), output, CheckpointLimits{}))
	_, err = os.Stat(output)
	require.True(t, os.IsNotExist(err))
	require.NoError(t, frozen.Close())
	require.ErrorIs(t, frozen.Save(context.Background(), filepath.Join(t.TempDir(), "late"), CheckpointLimits{}), unix.EBADF)
}

func TestCheckpointSingleFileAppendAndCorruptData(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	path := filepath.Join(base, "source")
	require.NoError(t, os.WriteFile(path, []byte("kept"), 0600))
	writer, err := OpenExport(path, false, Limits{})
	require.NoError(t, err)
	entry, err := writer.Lookup(Root, []byte("file"))
	require.NoError(t, err)
	appendHandle, err := writer.Open(entry.Node, unix.O_WRONLY|unix.O_APPEND)
	require.NoError(t, err)
	reader, err := OpenExport(path, true, Limits{})
	require.NoError(t, err)
	server := NewServer()
	t.Cleanup(func() { _ = server.Close() })
	writeDescriptor, err := server.Register(writer)
	require.NoError(t, err)
	readDescriptor, err := server.Register(reader)
	require.NoError(t, err)
	frozen, err := server.Freeze(ctx)
	require.NoError(t, err)
	artifact := filepath.Join(base, "snapshot")
	require.NoError(t, frozen.Save(ctx, artifact, CheckpointLimits{}))
	require.NoError(t, frozen.Close())
	require.NoError(t, server.Close())
	require.NoError(t, os.Remove(path))
	restored, err := RestoreServer(ctx, artifact, filepath.Join(base, "workspace"), CheckpointLimits{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = restored.Close() })
	_, err = restored.exports[writeDescriptor.Token].export.Write(appendHandle, 0, []byte("!"))
	require.NoError(t, err)
	readonly := restored.exports[readDescriptor.Token].export
	entry, err = readonly.Lookup(Root, []byte("file"))
	require.NoError(t, err)
	_, err = readonly.Open(entry.Node, unix.O_WRONLY)
	require.ErrorIs(t, err, unix.EROFS)
	handle, err := readonly.Open(entry.Node, unix.O_RDONLY)
	require.NoError(t, err)
	data, err := readonly.Read(handle, 0, 100)
	require.NoError(t, err)
	require.Equal(t, "kept!", string(data))
	require.NoError(t, os.WriteFile(checkpointDataPath(artifact, 1), []byte("bad!"), 0600))
	badWorkspace := filepath.Join(base, "bad")
	_, err = RestoreServer(ctx, artifact, badWorkspace, CheckpointLimits{})
	require.Error(t, err)
	_, err = os.Stat(badWorkspace)
	require.True(t, os.IsNotExist(err), "a failed restore must remove its private partial workspace")
	require.NoError(t, os.Remove(checkpointDataPath(artifact, 1)))
	require.NoError(t, unix.Mkfifo(checkpointDataPath(artifact, 1), 0600))
	_, err = RestoreServer(ctx, artifact, badWorkspace, CheckpointLimits{})
	require.Error(t, err, "a non-regular artifact must fail without blocking for a FIFO writer")
	_, err = os.Stat(badWorkspace)
	require.True(t, os.IsNotExist(err))
}
