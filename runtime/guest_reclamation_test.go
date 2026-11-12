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
