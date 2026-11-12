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
	"sync"
	"syscall"

	"github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/sirupsen/logrus"

	"github.com/firecracker-microvm/firecracker-containerd/config"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
)

// noopJailer is a jailer that returns only successful responses and performs
// no operations during calls
type noopJailer struct {
	logger        *logrus.Entry
	shimDir       vm.Dir
	ctx           context.Context
	process       vm.ProcessIdentity
	restoreMu     sync.RWMutex
	restoreMounts []restoreMount
	restoreInputs []string
}

func newNoopJailer(ctx context.Context, logger *logrus.Entry, shimDir vm.Dir) *noopJailer {
	return &noopJailer{
		logger:  logger,
		shimDir: shimDir,
		ctx:     ctx,
	}
}

func (j *noopJailer) BuildJailedMachine(cfg *config.Config, machineConfig *firecracker.Config, vmID string) ([]firecracker.Opt, error) {
	pidHandler := firecracker.Handler{
		Name: "firecracker-containerd-jail-pid-handler",
		Fn: func(_ context.Context, m *firecracker.Machine) error {
			pid, err := m.PID()
			if err != nil {
				return err
			}
			j.process, err = vm.ReadProcessIdentity(pid)
			return err
		},
	}

	trackProcess := func(m *firecracker.Machine) {
		m.Handlers.FcInit = m.Handlers.FcInit.AppendAfter(firecracker.StartVMMHandlerName, pidHandler)
	}
	if cfg.FirecrackerBinaryPath == "" && len(j.restoreMounts) == 0 {
		return []firecracker.Opt{trackProcess}, nil
	}
	relSocketPath, err := j.shimDir.FirecrackerSockRelPath()
	if err != nil {
		return nil, err
	}

	cmd := firecracker.VMCommandBuilder{}.
		WithBin(cfg.FirecrackerBinaryPath).
		WithSocketPath(relSocketPath).
		WithArgs([]string{"--id", vmID}).
		Build(j.ctx)

	if len(j.restoreMounts) > 0 {
		binary := cfg.FirecrackerBinaryPath
		if binary == "" {
			binary = "firecracker"
		}
		// Keep the SDK's host-side socket path. The VMM starts with its
		// private cwd at the shared VM directory, so short names also retain
		// support for host VM directories longer than sockaddr_un.sun_path.
		for index := range machineConfig.VsockDevices {
			machineConfig.VsockDevices[index].Path = filepath.Base(j.shimDir.FirecrackerVSockPath())
		}
		inputs := append([]string(nil), j.restoreInputs...)
		for _, path := range []*string{&machineConfig.LogPath, &machineConfig.MetricsPath} {
			if *path != "" {
				*path, err = filepath.Abs(*path)
				if err != nil {
					return nil, err
				}
				inputs = append(inputs, *path)
			}
		}
		args := []string{"--api-sock", filepath.Base(j.shimDir.FirecrackerSockPath()), "--id", vmID}
		cmd, err = restoreNamespaceCommand(j.ctx, j.shimDir.RootPath(), binary, args, j.restoreMounts, inputs)
		if err != nil {
			return nil, fmt.Errorf("build restore namespace: %w", err)
		}
	}
	if cfg.DebugHelper.LogFirecrackerOutput() {
		cmd.Stdout = j.logger.WithField("vmm_stream", "stdout").WriterLevel(logrus.DebugLevel)
		cmd.Stderr = j.logger.WithField("vmm_stream", "stderr").WriterLevel(logrus.DebugLevel)
	}

	return []firecracker.Opt{trackProcess, firecracker.WithProcessRunner(cmd)}, nil
}

func (j *noopJailer) JailPath() vm.Dir {
	j.logger.Debug("noop operation returning shim dir for JailPath")
	return j.shimDir
}

func (j *noopJailer) ExposeFileToJail(path string) error {
	j.restoreMu.Lock()
	defer j.restoreMu.Unlock()
	if len(j.restoreMounts) == 0 {
		return nil
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	shared, err := filepath.Abs(j.shimDir.RootPath())
	if err != nil {
		return err
	}
	if pathWithin(path, shared) {
		return nil
	}
	mount := restoreMount{Source: path, Target: path}
	for _, existing := range j.restoreMounts {
		if existing.Target == path && existing.Source == path {
			return nil
		}
		if pathWithin(existing.Target, path) || pathWithin(path, existing.Target) {
			return fmt.Errorf("new disk overlaps captured backing %s", path)
		}
	}
	if err := validateRestoreMounts([]restoreMount{mount}, shared); err != nil {
		return err
	}
	if err := addRestoreMount(j.ctx, j.process, shared, mount); err != nil {
		return err
	}
	j.restoreMounts = append(j.restoreMounts, mount)
	return nil
}

func (j *noopJailer) StubDrivesOptions() []FileOpt {
	j.logger.Debug("noop operation for StubDrivesOptions")
	return []FileOpt{}
}

func (j *noopJailer) Stop(force bool) error {
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	return vm.StopProcess(j.process, signal)
}

func (j *noopJailer) Close() error {
	return os.RemoveAll(j.shimDir.RootPath())
}
