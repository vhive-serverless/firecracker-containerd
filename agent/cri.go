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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	crimeta "github.com/firecracker-microvm/firecracker-containerd/internal/cri"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
)

var criShmMu sync.Mutex

// prepareCRIFiles materializes generated configuration in this task's bundle.
// A payload's host source is only a lookup key, never a guest output path.
func prepareCRIFiles(specData []byte, files []*proto.CRIFile, bundlePath string) ([]byte, error) {
	var spec specs.Spec
	if err := json.Unmarshal(specData, &spec); err != nil {
		return nil, err
	}
	pod, isCRI, err := crimeta.Parse(&spec)
	if err != nil || !isCRI {
		return specData, err
	}
	staged := make(map[string]string, len(files))
	if len(files) > 3 {
		return nil, fmt.Errorf("too many CRI generated configuration files")
	}
	for i, file := range files {
		if file == nil || len(file.Contents) > 65536 {
			return nil, fmt.Errorf("invalid CRI generated configuration file")
		}
		dir := filepath.Join(bundlePath, "cri-files")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, strconv.Itoa(i))
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(file.Mode)&0777)
		if err != nil {
			return nil, err
		}
		_, writeErr := out.Write(file.Contents)
		closeErr := out.Close()
		if writeErr != nil {
			return nil, writeErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		staged[file.Source] = path
	}
	for i := range spec.Mounts {
		mnt := &spec.Mounts[i]
		if mnt.Type != "bind" {
			continue
		}
		switch mnt.Destination {
		case "/dev/shm":
			if mnt.Source != pod.ShmPath() {
				return nil, fmt.Errorf("invalid guest Pod shared memory path")
			}
			if err := prepareCRIShm(pod.ShmPath()); err != nil {
				return nil, err
			}
		case "/etc/resolv.conf", "/etc/hosts", "/etc/hostname":
			path, ok := staged[mnt.Source]
			if !ok {
				return nil, fmt.Errorf("missing CRI generated configuration for %s", mnt.Destination)
			}
			mnt.Source = path
		default:
			return nil, fmt.Errorf("unsupported guest CRI bind mount %q", mnt.Destination)
		}
	}
	if err := applyCRINetworkSysctls(&spec); err != nil {
		return nil, err
	}
	return json.Marshal(&spec)
}

// The VM is the Pod's network namespace. Apply these settings there before
// runc creates a nested container; runc correctly refuses them when its own
// OCI spec uses the surrounding (guest) network namespace.
func applyCRINetworkSysctls(spec *specs.Spec) error {
	if spec.Linux == nil {
		return nil
	}
	keys := make([]string, 0, len(spec.Linux.Sysctl))
	for key := range spec.Linux.Sysctl {
		if strings.HasPrefix(key, "net.") || strings.HasPrefix(key, "net/") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		path, err := criNetworkSysctlPath(key)
		if err != nil {
			return err
		}
		// #nosec G703 -- path is confined to guest /proc/sys/net by the validated key.
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return fmt.Errorf("open Pod network sysctl %q: %w", key, err)
		}
		_, writeErr := file.WriteString(spec.Linux.Sysctl[key])
		closeErr := file.Close()
		if writeErr != nil {
			return fmt.Errorf("set Pod network sysctl %q: %w", key, writeErr)
		}
		if closeErr != nil {
			return closeErr
		}
		delete(spec.Linux.Sysctl, key)
	}
	return nil
}

func criNetworkSysctlPath(key string) (string, error) {
	name := strings.ReplaceAll(key, ".", "/")
	if !strings.HasPrefix(name, "net/") {
		return "", fmt.Errorf("invalid Pod network sysctl %q", key)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" {
			return "", fmt.Errorf("invalid Pod network sysctl %q", key)
		}
		for _, char := range part {
			valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
				char >= '0' && char <= '9' || char == '_' || char == '-'
			if !valid {
				return "", fmt.Errorf("invalid Pod network sysctl %q", key)
			}
		}
	}
	return "/proc/sys/" + name, nil
}

func prepareCRIShm(path string) error {
	criShmMu.Lock()
	defer criShmMu.Unlock()
	if err := os.MkdirAll(path, 0755); err != nil {
		return err
	}
	var dir, parent unix.Stat_t
	if err := unix.Stat(path, &dir); err != nil {
		return err
	}
	if err := unix.Stat(filepath.Dir(path), &parent); err != nil {
		return err
	}
	if dir.Dev != parent.Dev {
		return nil // the Pod's tmpfs already exists; preserve its contents
	}
	return unix.Mount("shm", path, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC,
		"mode=1777,size=65536k")
}
