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
	"runtime"
	"strings"
	"testing"

	"github.com/firecracker-microvm/firecracker-containerd/config"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/stretchr/testify/require"
)

func TestGuestReclamationConfiguration(t *testing.T) {
	administrator := &proto.GuestReclamation{Enabled: true}
	svc := &service{config: &config.Config{GuestReclamation: administrator}, machineConfig: &fc.Config{KernelArgs: "ro"}}
	request := &proto.CreateVMRequest{}
	require.NoError(t, svc.configureGuestReclamation(request))
	require.True(t, request.BalloonDevice.FreePageReporting)
	require.Zero(t, request.BalloonDevice.AmountMib)
	require.Contains(t, svc.machineConfig.KernelArgs, "damon_reclaim.skip_anon=Y")
	require.Contains(t, svc.machineConfig.KernelArgs, "damon_reclaim.min_age=5000000")
	require.Contains(t, svc.machineConfig.KernelArgs, "page_reporting.page_reporting_order=9")
	require.Zero(t, administrator.MinAgeSeconds, "per-VM defaults must not mutate shared administrator configuration")
	require.Nil(t, administrator.ReportingOrder)

	disabled := &service{config: &config.Config{GuestReclamation: administrator}, machineConfig: &fc.Config{KernelArgs: "ro"}}
	request = &proto.CreateVMRequest{Reclamation: &proto.GuestReclamation{Enabled: false}}
	require.NoError(t, disabled.configureGuestReclamation(request))
	require.Equal(t, "ro", disabled.machineConfig.KernelArgs)
	require.Nil(t, request.BalloonDevice)
}

func TestGuestReclamationRejectsConflictingAndUnboundedInputs(t *testing.T) {
	order := uint32(10)
	for _, options := range []*proto.GuestReclamation{
		{Enabled: true, MinAgeSeconds: 3601},
		{Enabled: true, QuotaMibPerSecond: 1025},
		{Enabled: true, QuotaTimeMs: 101},
		{Enabled: true, ReportingOrder: &order},
	} {
		svc := &service{config: &config.Config{}, machineConfig: &fc.Config{KernelArgs: "ro"}}
		request := &proto.CreateVMRequest{Reclamation: options}
		require.Error(t, svc.configureGuestReclamation(request))
		require.Nil(t, request.BalloonDevice, "invalid options must not partially enable reporting")
		require.Equal(t, "ro", svc.machineConfig.KernelArgs)
	}
	svc := &service{config: &config.Config{}, machineConfig: &fc.Config{KernelArgs: "damon_reclaim.enabled=N"}}
	request := &proto.CreateVMRequest{Reclamation: &proto.GuestReclamation{Enabled: true}}
	require.ErrorContains(t, svc.configureGuestReclamation(request), "conflicting")
	require.Nil(t, request.BalloonDevice)
}

func TestGuestReclamationAgentENV(t *testing.T) {
	cfg := &config.Config{
		GuestReclamationProfile: "agentenv",
		GuestReclamation:        &proto.GuestReclamation{Enabled: true},
	}
	expected := strings.Fields("ro page_reporting.page_reporting_order=6 damon_reclaim.enabled=Y " +
		"damon_reclaim.min_age=100000 damon_reclaim.quota_ms=20 damon_reclaim.quota_sz=1073741824 " +
		"damon_reclaim.quota_reset_interval_ms=500 damon_reclaim.wmarks_high=990 " +
		"damon_reclaim.wmarks_mid=990 damon_reclaim.wmarks_low=200 damon_reclaim.skip_anon=Y " +
		"damon_reclaim.wmarks_interval=1000000")
	for _, size := range []uint32{512, 4096} {
		request := &proto.CreateVMRequest{MachineCfg: &proto.FirecrackerMachineConfiguration{MemSizeMib: size}}
		svc := &service{config: cfg, machineConfig: &fc.Config{
			KernelArgs: "ro", MachineCfg: machineConfigurationFromProto(cfg, request.MachineCfg),
		}}
		var start, end string
		switch runtime.GOARCH {
		case "amd64":
			start = "4096"
			end = map[uint32]string{512: "536870912", 4096: "5368709120"}[size]
		case "arm64":
			start = "2147483648"
			end = map[uint32]string{512: "2684354560", 4096: "6442450944"}[size]
		default:
			t.Skip("Firecracker supports amd64 and arm64 guests")
		}
		args := append(expected, "damon_reclaim.monitor_region_start="+start, "damon_reclaim.monitor_region_end="+end)
		require.NoError(t, svc.configureGuestReclamation(request))
		require.ElementsMatch(t, args, strings.Fields(svc.machineConfig.KernelArgs))
		require.NotNil(t, request.BalloonDevice)
		require.True(t, request.BalloonDevice.FreePageReporting)
		require.True(t, request.BalloonDevice.DeflateOnOom)
		require.Zero(t, request.BalloonDevice.AmountMib)
		require.Zero(t, request.BalloonDevice.StatsPollingIntervals)
		t.Logf("V5 %d MiB kernel args: %s; balloon: %s", size, svc.machineConfig.KernelArgs, request.BalloonDevice)
	}
}

func TestGuestReclamationAgentENVRejectsLegacyOverrides(t *testing.T) {
	order := uint32(9)
	for _, opts := range []*proto.GuestReclamation{
		{Enabled: true, MinAgeSeconds: 5},
		{Enabled: true, QuotaMibPerSecond: 16},
		{Enabled: true, QuotaTimeMs: 5},
		{Enabled: true, ReportingOrder: &order},
	} {
		cfg := &config.Config{GuestReclamationProfile: "agentenv"}
		svc := &service{config: cfg, machineConfig: &fc.Config{KernelArgs: "ro", MachineCfg: machineConfigurationFromProto(cfg, nil)}}
		request := &proto.CreateVMRequest{Reclamation: opts}
		require.Error(t, svc.configureGuestReclamation(request))
		require.Equal(t, "ro", svc.machineConfig.KernelArgs)
		require.Nil(t, request.BalloonDevice)
	}
	cfg := &config.Config{GuestReclamationProfile: "agentenv", GuestReclamation: &proto.GuestReclamation{Enabled: true}}
	for _, balloon := range []*proto.FirecrackerBalloonDevice{{AmountMib: 1}, {StatsPollingIntervals: 1}} {
		svc := &service{config: cfg, machineConfig: &fc.Config{KernelArgs: "ro", MachineCfg: machineConfigurationFromProto(cfg, nil)}}
		request := &proto.CreateVMRequest{BalloonDevice: balloon}
		require.Error(t, svc.configureGuestReclamation(request))
		require.Equal(t, "ro", svc.machineConfig.KernelArgs)
		require.False(t, balloon.FreePageReporting)
	}
}
