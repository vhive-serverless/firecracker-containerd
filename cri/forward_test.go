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

package cri

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

type testBackend struct {
	runtimeapi.UnimplementedRuntimeServiceServer
	streamCanceled chan struct{}
}

func (b *testBackend) Version(ctx context.Context, _ *runtimeapi.VersionRequest) (*runtimeapi.VersionResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get("test-trace")) == 0 {
		return nil, status.Error(codes.InvalidArgument, "missing trace")
	}
	_ = grpc.SendHeader(ctx, metadata.Pairs("test-header", "present"))
	grpc.SetTrailer(ctx, metadata.Pairs("test-trailer", "present"))
	return &runtimeapi.VersionResponse{RuntimeName: "backend"}, nil
}

func (b *testBackend) StopPodSandbox(context.Context, *runtimeapi.StopPodSandboxRequest) (*runtimeapi.StopPodSandboxResponse, error) {
	return nil, status.Error(codes.FailedPrecondition, "backend-state-error")
}

func (b *testBackend) GetContainerEvents(_ *runtimeapi.GetEventsRequest, stream grpc.ServerStreamingServer[runtimeapi.ContainerEventResponse]) error {
	if err := stream.Send(&runtimeapi.ContainerEventResponse{ContainerId: "container-one"}); err != nil {
		return err
	}
	<-stream.Context().Done()
	close(b.streamCanceled)
	return stream.Context().Err()
}

func testConnection(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	register(server)
	go func() { _ = server.Serve(listener) }()
	connection, err := grpc.NewClient("passthrough:///test", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close(); server.Stop(); _ = listener.Close() })
	return connection
}

func TestForwardPreservesMetadataErrorsAndStreamCancellation(t *testing.T) {
	backend := &testBackend{streamCanceled: make(chan struct{})}
	upstream := testConnection(t, func(server *grpc.Server) { runtimeapi.RegisterRuntimeServiceServer(server, backend) })
	proxy := testConnection(t, func(server *grpc.Server) {
		runtimeapi.RegisterRuntimeServiceServer(server, &runtimeForwarder{backend: runtimeapi.NewRuntimeServiceClient(upstream)})
	})
	client := runtimeapi.NewRuntimeServiceClient(proxy)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("test-trace", "forwarded"))
	var headers, trailers metadata.MD
	version, err := client.Version(ctx, &runtimeapi.VersionRequest{}, grpc.Header(&headers), grpc.Trailer(&trailers))
	require.NoError(t, err)
	require.Equal(t, "backend", version.RuntimeName)
	require.Equal(t, []string{"present"}, headers.Get("test-header"))
	require.Equal(t, []string{"present"}, trailers.Get("test-trailer"))
	_, err = client.StopPodSandbox(ctx, &runtimeapi.StopPodSandboxRequest{})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, "backend-state-error", status.Convert(err).Message())
	streamCtx, streamCancel := context.WithCancel(ctx)
	stream, err := client.GetContainerEvents(streamCtx, &runtimeapi.GetEventsRequest{})
	require.NoError(t, err)
	event, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "container-one", event.ContainerId)
	streamCancel()
	_, err = stream.Recv()
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
	select {
	case <-backend.streamCanceled:
	case <-time.After(time.Second):
		t.Fatal("backend event stream leaked after client cancellation")
	}
}

func TestGeneratedForwardersCoverPinnedCRI(t *testing.T) {
	for name, data := range map[string]struct {
		generated  []string
		descriptor grpc.ServiceDesc
	}{
		"runtime": {generatedRuntimeServiceMethods, runtimeapi.RuntimeService_ServiceDesc},
		"images":  {generatedImageServiceMethods, runtimeapi.ImageService_ServiceDesc},
	} {
		var expected []string
		for _, method := range data.descriptor.Methods {
			expected = append(expected, method.MethodName)
		}
		for _, method := range data.descriptor.Streams {
			expected = append(expected, method.StreamName)
		}
		actual := append([]string{}, data.generated...)
		sort.Strings(expected)
		sort.Strings(actual)
		require.Equal(t, expected, actual, name)
	}
}

func TestRunPreservesExistingSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.sock")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	defer listener.Close()
	err = Run(context.Background(), Options{Socket: path, BackendSocket: filepath.Join(dir, "backend.sock")})
	require.ErrorContains(t, err, "already exists")
	connection, err := net.Dial("unix", path)
	require.NoError(t, err)
	defer connection.Close()
	accepted, err := listener.Accept()
	require.NoError(t, err)
	require.NoError(t, accepted.Close())
}
