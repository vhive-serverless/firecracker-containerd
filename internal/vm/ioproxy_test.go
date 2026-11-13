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

package vm

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fileConnector(path string, flag int) IOConnector {
	return func(_ context.Context, _ *logrus.Entry) <-chan IOConnectorResult {
		returnCh := make(chan IOConnectorResult, 1)
		defer close(returnCh)

		file, err := os.OpenFile(path, flag, 0600)
		returnCh <- IOConnectorResult{
			ReadWriteCloser: file,
			Err:             err,
		}

		return returnCh
	}
}

func TestProxy(t *testing.T) {
	dir := t.TempDir()

	ctx := context.Background()
	content := "hello world"

	err := os.WriteFile(filepath.Join(dir, "input"), []byte(content), 0600)
	require.NoError(t, err)

	pair := &IOConnectorPair{
		ReadConnector:  fileConnector(filepath.Join(dir, "input"), os.O_RDONLY),
		WriteConnector: fileConnector(filepath.Join(dir, "output"), os.O_CREATE|os.O_WRONLY),
	}
	initCh, copyCh := pair.proxy(ctx, context.Background(), logrus.WithFields(logrus.Fields{}), 0)

	assert.Nil(t, <-initCh)
	assert.Nil(t, <-copyCh)

	bytes, err := os.ReadFile(filepath.Join(dir, "output"))
	require.NoError(t, err)
	assert.Equal(t, content, string(bytes))
}

func TestProxyCloseDrainsBlockedOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reader, source := net.Pipe()
	writer, sink := net.Pipe()
	for _, conn := range []net.Conn{reader, source, writer, sink} {
		t.Cleanup(func() { _ = conn.Close() })
	}
	proxy := NewIOConnectorProxy(nil, &IOConnectorPair{
		ReadConnector: mockIOConnector(reader), WriteConnector: mockIOConnector(writer),
	}, nil)
	initDone, copyDone := proxy.start(&vmProc{ctx: ctx, logger: logrus.NewEntry(logrus.New())})
	require.NoError(t, <-initDone)
	proxy.Close()
	select {
	case <-copyDone:
	case <-time.After(time.Second):
		t.Fatal("closed proxy retained its blocked reader; replacing it can leave competing output consumers")
	}
	require.False(t, proxy.IsOpen())
}

func TestTaskManagerReattachDrainsOldProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	path := filepath.Join(t.TempDir(), "stdout")
	require.NoError(t, syscall.Mkfifo(path, 0600))
	output, err := os.OpenFile(path, os.O_RDWR, 0600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = output.Close() })
	newOutput := func() (IOProxy, net.Conn) {
		writer, sink := net.Pipe()
		t.Cleanup(func() { _ = writer.Close(); _ = sink.Close() })
		return NewIOConnectorProxy(nil, &IOConnectorPair{
			ReadConnector: ReadFIFOConnector(path), WriteConnector: mockIOConnector(writer),
		}, nil), sink
	}
	manager := NewTaskManager(ctx, logrus.NewEntry(logrus.New()))
	backend := &mockTaskService{}
	wait := make(chan struct{})
	backend.SetWaitCh("running", "", wait)
	t.Cleanup(func() { close(wait) })
	proxy, sink := newOutput()
	_, err = manager.CreateTask(ctx, &taskAPI.CreateTaskRequest{ID: "running"}, backend, proxy)
	require.NoError(t, err)
	for generation := 0; generation < 4; generation++ {
		require.NoError(t, sink.SetReadDeadline(time.Now().Add(time.Second)))
		_, err = output.Write([]byte("still-running"))
		require.NoError(t, err)
		data := make([]byte, len("still-running"))
		_, err = io.ReadFull(sink, data)
		require.NoError(t, err)
		require.Equal(t, "still-running", string(data))
		nextProxy, nextSink := newOutput()
		require.NoError(t, manager.AttachIO(ctx, "running", "", nextProxy))
		require.False(t, proxy.IsOpen(), "the old FIFO reader must be retired before replacement")
		proxy, sink = nextProxy, nextSink
	}
	proxy.Close()
}
