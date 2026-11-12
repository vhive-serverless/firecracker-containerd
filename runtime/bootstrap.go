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
	"io"
	"time"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	apiTypes "github.com/containerd/containerd/api/types"
	"github.com/containerd/containerd/v2/pkg/shim"
	"github.com/containerd/containerd/v2/pkg/shutdown"
	"github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
	"github.com/containerd/ttrpc"
	"golang.org/x/sys/unix"
)

// The bootstrap helper locates or starts the VM's shared shim. The long-lived
// task service is initialized separately by containerd's shim plugin registry.
type shimManager struct{}

func (*shimManager) Name() string { return shimID }

func (*shimManager) Info(context.Context, io.Reader) (*apiTypes.RuntimeInfo, error) {
	return &apiTypes.RuntimeInfo{
		Name:    shimID,
		Version: &apiTypes.RuntimeVersion{Version: "2.3.7", Revision: revision},
	}, nil
}

func (*shimManager) Stop(ctx context.Context, _ string) (shim.StopStatus, error) {
	if err := recoverBundle(ctx); err != nil {
		return shim.StopStatus{}, err
	}
	return shim.StopStatus{ExitStatus: 128 + int(unix.SIGKILL), ExitedAt: time.Now()}, nil
}

func (s *service) RegisterTTRPC(server *ttrpc.Server) error {
	taskAPI.RegisterTTRPCTaskService(server, s)
	return nil
}

func init() {
	registry.Register(&plugin.Registration{
		Type:     plugins.TTRPCPlugin,
		ID:       "firecracker-task",
		Requires: []plugin.Type{plugins.EventPlugin, plugins.InternalPlugin},
		InitFn: func(ic *plugin.InitContext) (any, error) {
			publisher, err := ic.GetByID(plugins.EventPlugin, "publisher")
			if err != nil {
				return nil, err
			}
			shutdownService, err := ic.GetByID(plugins.InternalPlugin, "shutdown")
			if err != nil {
				return nil, err
			}
			return NewService(ic.Context, "", publisher.(shim.Publisher), shutdownService.(shutdown.Service).Shutdown)
		},
	})
}
