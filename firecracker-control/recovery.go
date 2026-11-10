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

package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/shim"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
)

func waitOwnedShim(ctx context.Context, identity vm.ProcessIdentity) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		alive, err := vm.IsProcessAlive(identity)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *local) forgetShim(namespace, id string, pid int32) {
	address, err := shim.SocketAddress(namespaces.WithNamespace(context.Background(), namespace), s.containerdAddress, id, false)
	if err != nil {
		return
	}
	s.processesMu.Lock()
	defer s.processesMu.Unlock()
	if s.processes[address] == pid {
		delete(s.processes, address)
	}
}

func (s *local) cleanupExitedVM(namespace, id string, dir vm.Dir) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	logger := s.logger.WithField("vmID", id)
	err := vm.RecoverVM(ctx, dir)
	if err != nil {
		logger.WithError(err).Error("VM recovery failed; retaining recovery metadata")
		return
	}
	if err := os.RemoveAll(dir.RootPath()); err != nil {
		logger.WithError(err).Error("failed to remove VM directory")
		return
	}
	// Reserve sockets until the old VM directory is completely removed.
	if socketErr := s.removeSockets(namespace, id); socketErr != nil {
		logger.WithError(socketErr).Error("failed to remove shim sockets")
	}
}

// reconnectShims restores control bookkeeping and exit monitoring after the
// controller restarts. Existing shims and their VMs keep running independently.
func (s *local) reconnectShims(ctx context.Context) error {
	entries, err := os.ReadDir(s.config.ShimBaseDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := vm.Dir(filepath.Join(s.config.ShimBaseDir, entry.Name()))
		state, err := vm.ReadRecoveryState(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read VM recovery metadata: %w", err)
		}
		shimCtx := namespaces.WithNamespace(context.Background(), state.Namespace)
		address, err := shim.SocketAddress(shimCtx, s.containerdAddress, state.VMID, false)
		if err != nil {
			return err
		}
		s.processesMu.Lock()
		s.processes[address] = int32(state.Shim.PID)
		s.processesMu.Unlock()
		go func() {
			if err := waitOwnedShim(ctx, state.Shim); err != nil {
				if ctx.Err() == nil {
					s.logger.WithError(err).Error("watch recovered shim")
				}
				return
			}
			s.forgetShim(state.Namespace, state.VMID, int32(state.Shim.PID))
			s.cleanupExitedVM(state.Namespace, state.VMID, dir)
		}()
	}
	return nil
}
