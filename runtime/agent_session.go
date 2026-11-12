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

package main

import (
	"context"
	"fmt"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/ttrpc"
	"github.com/firecracker-microvm/firecracker-containerd/eventbridge"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	drivemount "github.com/firecracker-microvm/firecracker-containerd/proto/service/drivemount/ttrpc"
	ioproxy "github.com/firecracker-microvm/firecracker-containerd/proto/service/ioproxy/ttrpc"
	"github.com/firecracker-microvm/firecracker-go-sdk/vsock"
)

// connectAgent is called during startup or with ordinary task admission closed.
// Existing Wait monitors retain their wrapper and switch to the new generation;
// no mutating RPC is replayed as a side effect of reconnecting.
func (s *service) connectAgent(ctx context.Context) error {
	path, err := s.jailer.JailPath().FirecrackerVSockRelPath()
	if err != nil {
		return err
	}
	conn, err := vsock.DialContext(ctx, path, defaultVsockPort, vsock.WithLogger(s.logger), vsock.WithRetryTimeout(defaultCreateVMTimeout))
	if err != nil {
		return fmt.Errorf("dial agent vsock: %w", err)
	}
	client := ttrpc.NewClient(conn, ttrpc.WithOnClose(func() { _ = conn.Close() }))
	// Ensure the guest is serving the new protocol before replacing a live
	// connection. A failed handshake leaves the old generation intact.
	journal := eventbridge.JournalClient{Client: func(context.Context) (*ttrpc.Client, error) { return client, nil }}
	if _, err := journal.Cursor(ctx); err != nil {
		_ = client.Close()
		return fmt.Errorf("agent journal handshake: %w", err)
	}
	if stable, ok := s.agentClient.(*vm.ReconnectingTaskClient); ok {
		stable.Replace(taskAPI.NewTTRPCTaskClient(client))
	} else {
		s.agentClient = vm.NewReconnectingTaskClient(taskAPI.NewTTRPCTaskClient(client))
	}
	s.driveMountClient = drivemount.NewDriveMounterClient(client)
	s.ioProxyClient = ioproxy.NewIOProxyClient(client)
	s.agentRPCMu.Lock()
	old := s.agentRPC
	s.agentRPC = client
	s.agentRPCMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}
