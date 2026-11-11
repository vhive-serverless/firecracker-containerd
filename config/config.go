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

// Package config defines configuration structures and helpers used by
// firecracker-containerd, including loading and validating config files.
package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/firecracker-microvm/firecracker-containerd/internal"
	"github.com/firecracker-microvm/firecracker-containerd/internal/debug"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	models "github.com/firecracker-microvm/firecracker-go-sdk/client/models"
)

const (
	// DefaultCRIMemoryMiB is the total guest capacity when no Pod memory limit is supplied.
	DefaultCRIMemoryMiB uint32 = 512
	// DefaultCRIMemoryOverheadMiB reserves guest capacity for its kernel and agent.
	DefaultCRIMemoryOverheadMiB uint32 = 128

	// ConfigPathEnvName is the name of the environment variable used to
	// overwrite the default runtime config path
	ConfigPathEnvName  = "FIRECRACKER_CONTAINERD_RUNTIME_CONFIG_PATH"
	defaultConfigPath  = "/etc/containerd/firecracker-runtime.json"
	defaultKernelArgs  = "ro console=ttyS0 reboot=k panic=1 pci=off nomodules systemd.unified_cgroup_hierarchy=1 systemd.unit=firecracker.target init=/sbin/overlay-init"
	defaultFilesPath   = "/var/lib/firecracker-containerd/runtime/"
	defaultKernelPath  = defaultFilesPath + "default-vmlinux.bin"
	defaultRootfsPath  = defaultFilesPath + "default-rootfs.img"
	defaultCPUTemplate = models.CPUTemplateT2
	defaultShimBaseDir = "/var/lib/firecracker-containerd/shim-base"
	runcConfigPath     = "/etc/containerd/firecracker-runc-config.json"
)

// Config represents runtime configuration parameters
type Config struct {
	FirecrackerBinaryPath string                  `json:"firecracker_binary_path"`
	KernelImagePath       string                  `json:"kernel_image_path"`
	KernelArgs            string                  `json:"kernel_args"`
	RootDrive             string                  `json:"root_drive"`
	GuestRootMode         string                  `json:"guest_root_mode"`
	GuestRootSizeMiB      uint32                  `json:"guest_root_size_mib"`
	GuestReclamation      *proto.GuestReclamation `json:"guest_reclamation,omitempty"`
	CPUTemplate           string                  `json:"cpu_template"`
	LogLevels             []string                `json:"log_levels"`
	SmtEnabled            bool                    `json:"smt_enabled"`
	// If a CreateVM call specifies no network interfaces and DefaultNetworkInterfaces is non-empty,
	// the VM will default to using the network interfaces as specified here. This is especially
	// useful when a CNI-based network interface is provided in DefaultNetworkInterfaces.
	DefaultNetworkInterfaces []proto.FirecrackerNetworkInterface `json:"default_network_interfaces"`
	// CRI uses containerd's completed CNI allocation and its tc-redirect-tap result.
	CRICNIConfDir        string `json:"cri_cni_conf_dir"`
	CRICNICacheDir       string `json:"cri_cni_cache_dir"`
	CRIContainerCount    int32  `json:"cri_container_count"`
	CRIDefaultMemoryMiB  uint32 `json:"cri_default_memory_mib"`
	CRIMemoryOverheadMiB uint32 `json:"cri_memory_overhead_mib"`
	// ShimBaseDir is the base directory which shim dirs will be created for each
	// VM. In addition to this if jailing is enabled the jail will also use this
	// directory.
	ShimBaseDir  string       `json:"shim_base_dir"`
	JailerConfig JailerConfig `json:"jailer"`

	DebugHelper *debug.Helper `json:"-"`
}

// JailerConfig houses a set of configurable values for jailing
// TODO: Add netns field
type JailerConfig struct {
	RuncBinaryPath string `json:"runc_binary_path"`
	RuncConfigPath string `json:"runc_config_path"`
}

// LoadConfig loads configuration from JSON file at 'path'
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv(ConfigPathEnvName)
	}

	if path == "" {
		path = defaultConfigPath
	}

	// #nosec G703 -- path is the explicitly selected administrator runtime configuration.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config from %q: %w", path, err)
	}

	cfg := &Config{
		CRICNIConfDir:        "/etc/cni/net.d",
		CRICNICacheDir:       "/var/lib/cni",
		CRIContainerCount:    8,
		CRIDefaultMemoryMiB:  DefaultCRIMemoryMiB,
		CRIMemoryOverheadMiB: DefaultCRIMemoryOverheadMiB,
		KernelArgs:           defaultKernelArgs,
		KernelImagePath:      defaultKernelPath,
		RootDrive:            defaultRootfsPath,
		GuestRootMode:        "disk",
		GuestRootSizeMiB:     512,
		ShimBaseDir:          defaultShimBaseDir,
		JailerConfig: JailerConfig{
			RuncConfigPath: runcConfigPath,
		},
	}

	flag, err := internal.SupportCPUTemplate()
	if err != nil {
		return nil, err
	}
	if flag {
		cfg.CPUTemplate = string(defaultCPUTemplate)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config from %q: %w", path, err)
	}

	cfg.DebugHelper, err = debug.New(cfg.LogLevels...)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}
