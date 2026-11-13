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
	"math"
	"strconv"

	"github.com/firecracker-microvm/firecracker-containerd/config"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// These are containerd 2.3.7's standard sandbox annotations, populated from
// LinuxPodSandboxConfig.Resources in internal/cri/server/podsandbox/sandbox_run_linux.go.
// They describe the Pod envelope, rather than the pause container's own cgroup.
const (
	criSandboxCPUPeriod = "io.kubernetes.cri.sandbox-cpu-period"
	criSandboxCPUQuota  = "io.kubernetes.cri.sandbox-cpu-quota"
	criSandboxCPUShares = "io.kubernetes.cri.sandbox-cpu-shares"
	criSandboxMemory    = "io.kubernetes.cri.sandbox-memory"
	bytesPerMiB         = uint64(1024 * 1024)
	// The selected Firecracker KVM backend's MAX_SUPPORTED_KVM_VCPUS is 32.
	maxCRIKVMVCPUs      = uint64(32)
	criDefaultCPUPeriod = int64(100000)
)

func criMachineConfiguration(annotations map[string]string, cfg *config.Config) (*proto.FirecrackerMachineConfiguration, error) {
	values := make(map[string]int64, 4)
	for _, key := range []string{criSandboxCPUPeriod, criSandboxCPUQuota, criSandboxCPUShares, criSandboxMemory} {
		raw, present := annotations[key]
		if !present {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid CRI resource annotation %s: %q", key, raw)
		}
		values[key] = value
	}
	period, quota, shares, memory := values[criSandboxCPUPeriod], values[criSandboxCPUQuota], values[criSandboxCPUShares], values[criSandboxMemory]
	if period < 0 || quota < -1 || shares < 0 || memory < -1 {
		return nil, status.Error(codes.InvalidArgument, "CRI resource annotations contain a negative resource value")
	}
	cpus := uint64(1)
	if quota > 0 {
		if period == 0 {
			// CRI's zero means unspecified; Linux CFS defaults to 100 ms.
			period = criDefaultCPUPeriod
		}
		cpus = ceilResource(uint64(quota), uint64(period))
	} else if shares > 0 {
		// Kubernetes converts CPU requests to 1024 shares per core. A positive
		// quota takes precedence, including requests made directly through CRI.
		cpus = ceilResource(uint64(shares), 1024)
	}
	if cfg.SmtEnabled && cpus > 1 && cpus%2 != 0 {
		cpus++
	}
	if cpus > maxCRIKVMVCPUs {
		return nil, status.Errorf(codes.ResourceExhausted, "Pod requires %d vCPUs; selected Firecracker KVM backend supports at most %d", cpus, maxCRIKVMVCPUs)
	}
	capacity, overhead := cfg.CRIDefaultMemoryMiB, cfg.CRIMemoryOverheadMiB
	if capacity == 0 {
		capacity = config.DefaultCRIMemoryMiB
	}
	if overhead == 0 {
		overhead = config.DefaultCRIMemoryOverheadMiB
	}
	memoryMiB := uint64(capacity)
	if memory > 0 {
		memoryMiB = ceilResource(uint64(memory), bytesPerMiB) + uint64(overhead)
	}
	if memoryMiB > math.MaxUint32 {
		return nil, status.Error(codes.ResourceExhausted, "Pod memory envelope exceeds the VM request's MiB capacity")
	}
	return &proto.FirecrackerMachineConfiguration{
		VcpuCount: uint32(cpus), MemSizeMib: uint32(memoryMiB), HtEnabled: cfg.SmtEnabled,
	}, nil
}

// Division and remainder avoid overflowing when a limit approaches MaxInt64.
func ceilResource(value, unit uint64) uint64 {
	result := value / unit
	if value%unit != 0 {
		result++
	}
	return result
}
