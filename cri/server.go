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

// Package cri supplies Firecracker-specific operations around containerd's CRI.
package cri

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/firecracker-microvm/firecracker-containerd/config"
	"github.com/firecracker-microvm/firecracker-containerd/internal"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-go-sdk/vsock"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	"k8s.io/cri-streaming/pkg/streaming"
	"k8s.io/cri-streaming/pkg/streaming/remotecommand"
)

const criNamespace = "k8s.io"

// Options select local endpoints and the same runtime configuration as the shim.
type Options struct {
	Socket        string
	BackendSocket string
	StreamAddress string
	RuntimeConfig string
}

type gateway struct {
	*runtimeForwarder
	streams streaming.Server
	ctx     context.Context
	cfg     *config.Config
}

// Run serves CRI on an owned Unix socket and streaming on the selected address.
// An existing Unix socket is never removed or replaced.
func Run(ctx context.Context, options Options) error {
	if !filepath.IsAbs(options.Socket) || !filepath.IsAbs(options.BackendSocket) || options.Socket == options.BackendSocket {
		return errors.New("CRI and backend sockets must be distinct absolute paths")
	}
	if _, err := os.Lstat(options.Socket); !os.IsNotExist(err) {
		return fmt.Errorf("CRI socket path already exists or is inaccessible: %s", options.Socket)
	}
	cfg, err := config.LoadConfig(options.RuntimeConfig)
	if err != nil {
		return err
	}
	target := (&url.URL{Scheme: "unix", Path: options.BackendSocket}).String()
	backend, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16<<20), grpc.MaxCallSendMsgSize(16<<20)))
	if err != nil {
		return err
	}
	defer backend.Close()
	if err := os.MkdirAll(filepath.Dir(options.Socket), 0750); err != nil {
		return err
	}
	listener, err := net.Listen("unix", options.Socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(options.Socket, 0600); err != nil {
		return err
	}
	streamListener, err := net.Listen("tcp", options.StreamAddress)
	if err != nil {
		return err
	}
	defer streamListener.Close()
	group, serverCtx := errgroup.WithContext(ctx)
	runtime := &gateway{
		runtimeForwarder: &runtimeForwarder{backend: runtimeapi.NewRuntimeServiceClient(backend)},
		ctx:              serverCtx, cfg: cfg,
	}
	streamConfig := streaming.DefaultConfig
	streamConfig.Addr = streamListener.Addr().String()
	runtime.streams, err = streaming.NewServer(streamConfig, &streamRuntime{gateway: runtime})
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer(grpc.MaxRecvMsgSize(16<<20), grpc.MaxSendMsgSize(16<<20))
	runtimeapi.RegisterRuntimeServiceServer(grpcServer, runtime)
	runtimeapi.RegisterImageServiceServer(grpcServer, &imageForwarder{backend: runtimeapi.NewImageServiceClient(backend)})
	httpServer := &http.Server{Handler: runtime.streams, ReadHeaderTimeout: 10 * time.Second}
	group.Go(func() error {
		err := grpcServer.Serve(listener)
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	})
	group.Go(func() error {
		err := httpServer.Serve(streamListener)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
	group.Go(func() error {
		<-serverCtx.Done()
		grpcServer.Stop()
		return httpServer.Close()
	})
	return group.Wait()
}

func (g *gateway) pod(ctx context.Context, id string) (*runtimeapi.PodSandboxStatus, bool, error) {
	response, err := g.backend.PodSandboxStatus(backendContext(ctx), &runtimeapi.PodSandboxStatusRequest{PodSandboxId: id, Verbose: true})
	if err != nil {
		return nil, false, err
	}
	if response.Status == nil || response.Status.State != runtimeapi.PodSandboxState_SANDBOX_READY {
		return nil, false, status.Error(codes.FailedPrecondition, "Pod sandbox is not ready")
	}
	// This is containerd's version-pinned verbose SandboxInfo, obtained through
	// its public CRI service rather than its private in-process stores.
	var info struct {
		RuntimeType string `json:"runtimeType"`
	}
	if err := json.Unmarshal([]byte(response.Info["info"]), &info); err != nil {
		return nil, false, status.Errorf(codes.Internal, "decode backend Pod runtime: %v", err)
	}
	if info.RuntimeType == "" {
		return nil, false, status.Error(codes.Internal, "backend omitted the Pod runtime type")
	}
	return response.Status, info.RuntimeType == "aws.firecracker", nil
}

func (g *gateway) PortForward(ctx context.Context, request *runtimeapi.PortForwardRequest) (*runtimeapi.PortForwardResponse, error) {
	pod, firecracker, err := g.pod(ctx, request.GetPodSandboxId())
	if err != nil {
		return nil, err
	}
	if !firecracker {
		return forwardUnary(ctx, request, g.backend.PortForward)
	}
	for _, port := range request.Port {
		if port < 1 || port > 65535 {
			return nil, status.Errorf(codes.InvalidArgument, "invalid Pod TCP port %d", port)
		}
	}
	return g.streams.GetPortForward(&runtimeapi.PortForwardRequest{PodSandboxId: pod.Id, Port: request.Port})
}

type streamRuntime struct{ gateway *gateway }

func (s *streamRuntime) Exec(context.Context, string, []string, io.Reader, io.WriteCloser, io.WriteCloser, bool, <-chan remotecommand.TerminalSize) error {
	return status.Error(codes.Unimplemented, "exec streams are served by containerd")
}

func (s *streamRuntime) Attach(context.Context, string, io.Reader, io.WriteCloser, io.WriteCloser, bool, <-chan remotecommand.TerminalSize) error {
	return status.Error(codes.Unimplemented, "attach streams are served by containerd")
}

func (s *streamRuntime) PortForward(ctx context.Context, id string, port int32, stream io.ReadWriteCloser) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.gateway.ctx, cancel)
	defer stop()
	pod, firecracker, err := s.gateway.pod(ctx, id)
	if err != nil {
		return err
	}
	if !firecracker {
		return status.Error(codes.FailedPrecondition, "Pod runtime changed before port-forward connected")
	}
	dir, err := vm.ShimDir(s.gateway.cfg.ShimBaseDir, criNamespace, pod.Id)
	if err != nil {
		return err
	}
	root, err := os.Open(dir.RootPath())
	if err != nil {
		return err
	}
	// A directory FD keeps the socket address below AF_UNIX's length limit,
	// without changing process-wide cwd while serving different Pods.
	path := filepath.Join("/proc/self/fd", strconv.FormatUint(uint64(root.Fd()), 10), internal.FirecrackerVSockName)
	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	conn, err := vsock.DialContext(dialCtx, path, vm.PodForwardPort)
	dialCancel()
	_ = root.Close()
	if err != nil {
		return err
	}
	defer conn.Close()
	stopConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopConn()
	if err := vm.OpenGuestTCP(conn, port); err != nil {
		return err
	}
	return vm.RelayStreams(ctx, stream, conn)
}
