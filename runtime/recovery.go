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
	"os"
	"path/filepath"

	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containernetworking/cni/libcni"
	"github.com/firecracker-microvm/firecracker-containerd/config"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-go-sdk"
)

func (s *service) recordRecoveryState(ctx context.Context, ownNetNS bool) error {
	identity, err := vm.ReadProcessIdentity(os.Getpid())
	if err != nil {
		return err
	}
	state := vm.RecoveryState{Version: 1, VMID: s.vmID, Namespace: s.namespace, Shim: identity, NetNS: s.machine.Cfg.NetNS, OwnNetNS: ownNetNS}
	if jailer, ok := s.jailer.(*runcJailer); ok {
		state.RuncBinary = jailer.Config.RuncBinPath
	}
	if pid, err := s.machine.PID(); err == nil {
		if jailer, ok := s.jailer.(*runcJailer); ok {
			actual, err := jailer.runcClient.State(ctx, s.vmID)
			if err != nil {
				return err
			}
			pid = actual.Pid
			if filepath.Clean(actual.Bundle) != filepath.Clean(jailer.OCIBundlePath()) {
				return fmt.Errorf("runc ID %q belongs to another bundle %q", s.vmID, actual.Bundle)
			}
			state.RuncBinary = jailer.Config.RuncBinPath
		}
		state.VMM, err = vm.ReadProcessIdentity(pid)
		if err != nil {
			return err
		}
	}
	for _, iface := range s.machine.Cfg.NetworkInterfaces {
		conf := iface.CNIConfiguration
		if conf == nil {
			continue
		}
		bins := conf.BinPath
		if len(bins) == 0 {
			bins = []string{"/opt/cni/bin"}
		}
		cache := conf.CacheDir
		if cache == "" {
			cache = filepath.Join("/var/lib/cni", s.vmID)
		}
		list := conf.NetworkConfig
		if list == nil {
			dir := conf.ConfDir
			if dir == "" {
				dir = "/etc/cni/conf.d"
			}
			list, err = libcni.LoadConfList(dir, conf.NetworkName)
			if err != nil {
				return err
			}
		}
		state.Networks = append(state.Networks, vm.RecoveryNetwork{Config: list.Bytes, BinPath: bins, CacheDir: cache, Runtime: libcni.RuntimeConf{ContainerID: s.vmID, NetNS: state.NetNS, IfName: conf.IfName, Args: conf.Args}})
	}
	return vm.WriteRecoveryState(s.shimDir, state)
}

func (s *service) configureRecovery() error {
	_, err := os.Stat(s.machine.Cfg.NetNS)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	ownNetNS := s.machine.Cfg.NetNS != "" && os.IsNotExist(err)
	write := firecracker.Handler{Name: "firecracker-containerd-record-recovery", Fn: func(ctx context.Context, _ *firecracker.Machine) error { return s.recordRecoveryState(ctx, ownNetNS) }}
	if err := s.recordRecoveryState(s.shimCtx, ownNetNS); err != nil {
		return err
	}
	s.machine.Handlers.FcInit = s.machine.Handlers.FcInit.AppendAfter(firecracker.SetupNetworkHandlerName, write)
	s.machine.Handlers.FcInit = s.machine.Handlers.FcInit.AppendAfter(firecracker.StartVMMHandlerName, write)
	return nil
}

func recoverBundle(ctx context.Context) error {
	cfg, err := config.LoadConfig("")
	if err != nil {
		return err
	}
	opts, err := shimOpts(ctx)
	if err != nil {
		return err
	}
	bundlePath := opts.BundlePath
	if bundlePath == "" {
		bundlePath, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	namespace, err := namespaces.NamespaceRequired(ctx)
	if err != nil {
		return err
	}
	dir, err := vm.BundleVMDir(cfg.ShimBaseDir, namespace, bundlePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return vm.RecoverVM(ctx, dir)
}
