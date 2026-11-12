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
	"testing"

	"github.com/firecracker-microvm/firecracker-containerd/config"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCRIMachineConfiguration(t *testing.T) {
	for _, test := range []struct {
		name         string
		annotations  map[string]string
		cfg          config.Config
		cpus, memory uint32
	}{
		{name: "unbounded fallback", cpus: 1, memory: 512},
		{name: "limits round up", annotations: map[string]string{
			criSandboxCPUPeriod: "100000", criSandboxCPUQuota: "150000",
			criSandboxMemory: strconv.FormatUint(256*bytesPerMiB+1, 10),
		}, cpus: 2, memory: 385},
		{name: "unspecified period uses Linux default", annotations: map[string]string{
			criSandboxCPUQuota: "150000",
		}, cpus: 2, memory: 512},
		{name: "unlimited CPU uses requests", annotations: map[string]string{
			criSandboxCPUQuota: "-1", criSandboxCPUPeriod: "100000", criSandboxCPUShares: "2048", criSandboxMemory: "-1",
		}, cpus: 2, memory: 512},
		{name: "CPU limit takes precedence", annotations: map[string]string{
			criSandboxCPUQuota: "100000", criSandboxCPUPeriod: "100000", criSandboxCPUShares: "8192",
		}, cpus: 1, memory: 512},
		{name: "SMT rounds topology", annotations: map[string]string{
			criSandboxCPUQuota: "250000", criSandboxCPUPeriod: "100000",
		}, cfg: config.Config{SmtEnabled: true}, cpus: 4, memory: 512},
		{name: "administrator capacity", cfg: config.Config{CRIDefaultMemoryMiB: 1024}, cpus: 1, memory: 1024},
		{name: "administrator headroom", annotations: map[string]string{criSandboxMemory: "268435456"},
			cfg: config.Config{CRIMemoryOverheadMiB: 64}, cpus: 1, memory: 320},
	} {
		t.Run(test.name, func(t *testing.T) {
			machine, err := criMachineConfiguration(test.annotations, &test.cfg)
			require.NoError(t, err)
			require.Equal(t, test.cpus, machine.VcpuCount)
			require.Equal(t, test.memory, machine.MemSizeMib)
			require.Equal(t, test.cfg.SmtEnabled, machine.HtEnabled)
		})
	}
}

func TestCRIMachineConfigurationRejectsInvalidEnvelopes(t *testing.T) {
	for name, annotations := range map[string]map[string]string{
		"malformed memory": {criSandboxMemory: "256Mi"},
		"empty annotation": {criSandboxCPUQuota: ""},
		"negative period":  {criSandboxCPUPeriod: "-1"},
		"negative shares":  {criSandboxCPUShares: "-1"},
		"negative quota":   {criSandboxCPUQuota: "-2"},
		"negative memory":  {criSandboxMemory: "-2"},
		"integer overflow": {criSandboxMemory: "9223372036854775808"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := criMachineConfiguration(annotations, &config.Config{})
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
	for name, annotations := range map[string]map[string]string{
		"KVM CPU ceiling":     {criSandboxCPUPeriod: "100000", criSandboxCPUQuota: "3300000"},
		"request CPU ceiling": {criSandboxCPUShares: "33792"},
		"memory MiB overflow": {criSandboxMemory: strconv.FormatInt(math.MaxInt64, 10)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := criMachineConfiguration(annotations, &config.Config{})
			require.Equal(t, codes.ResourceExhausted, status.Code(err))
		})
	}
}
