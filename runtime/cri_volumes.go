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
	"errors"
	"os"
	"path/filepath"

	"github.com/firecracker-microvm/firecracker-containerd/internal/volfs"
	"github.com/opencontainers/runtime-spec/specs-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const bindMountType = "bind"

func criVolumePolicy(mount specs.Mount) (readOnly, recursive bool, err error) {
	if !filepath.IsAbs(mount.Source) {
		return false, false, status.Error(codes.InvalidArgument, "CRI volume source must be absolute")
	}
	if len(mount.UIDMappings) > 0 || len(mount.GIDMappings) > 0 {
		return false, false, status.Error(codes.Unimplemented, "CRI volume ID mapping is not implemented")
	}
	for _, option := range mount.Options {
		switch option {
		case "ro":
			readOnly = true
		case "rw":
			readOnly = false
		case bindMountType:
			recursive = false
		case "rbind":
			recursive = true
		case "shared", "rshared", "slave", "rslave", "unbindable", "runbindable":
			return false, false, status.Errorf(codes.Unimplemented, "CRI volume mount propagation %q is not implemented", option)
		}
	}
	return readOnly, recursive, nil
}

func (s *service) ensureVolumeServer() (*volfs.Server, error) {
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	if s.volumesClosed {
		return nil, status.Error(codes.FailedPrecondition, "VM volume service is closed")
	}
	if s.volumeServer != nil {
		return s.volumeServer, nil
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if jailed, ok := s.jailer.(*runcJailer); ok {
		uid, gid = int(jailed.Config.UID), int(jailed.Config.GID)
	}
	listener, err := volfs.ListenHost(s.jailer.JailPath().FirecrackerVSockPath(), uid, gid)
	if err != nil {
		return nil, err
	}
	server := volfs.NewServer()
	s.volumeServer = server
	go func() {
		if err := server.Serve(s.shimCtx, listener); err != nil && s.shimCtx.Err() == nil {
			s.logger.WithError(err).Error("CRI volume service stopped")
		}
	}()
	return server, nil
}

func (s *service) registerCRIVolume(mount specs.Mount) (volfs.Descriptor, error) {
	readOnly, recursive, err := criVolumePolicy(mount)
	if err != nil {
		return volfs.Descriptor{}, err
	}
	server, err := s.ensureVolumeServer()
	if err != nil {
		return volfs.Descriptor{}, err
	}
	export, err := volfs.OpenMountExport(mount.Source, readOnly, recursive, volfs.Limits{})
	if err != nil {
		return volfs.Descriptor{}, err
	}
	descriptor, err := server.Register(export)
	if err != nil {
		_ = export.Close()
	}
	return descriptor, err
}

func (s *service) revokeCRIVolumes(tokens []string) error {
	s.volumeMu.Lock()
	server := s.volumeServer
	s.volumeMu.Unlock()
	if server == nil {
		return nil
	}
	var result error
	for _, token := range tokens {
		result = errors.Join(result, server.Unregister(token))
	}
	return result
}

func (s *service) closeCRIVolumes() error {
	s.volumeMu.Lock()
	s.volumesClosed = true
	server := s.volumeServer
	s.volumeMu.Unlock()
	if server == nil {
		return nil
	}
	return server.Close()
}
