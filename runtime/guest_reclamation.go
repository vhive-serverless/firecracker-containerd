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
	"fmt"
	"runtime"
	"strings"

	"github.com/firecracker-microvm/firecracker-containerd/proto"
	gproto "google.golang.org/protobuf/proto"
)

func (s *service) configureGuestReclamation(request *proto.CreateVMRequest) error {
	if s.nativeRestore != nil {
		return nil
	} // Restored kernel/device state wins.
	selected := request.Reclamation
	if selected == nil {
		selected = s.config.GuestReclamation
	}
	if selected == nil || !selected.Enabled {
		return nil
	}
	for _, argument := range strings.Fields(s.machineConfig.KernelArgs) {
		if strings.HasPrefix(argument, "damon_reclaim.") || strings.HasPrefix(argument, "page_reporting.") {
			return fmt.Errorf("conflicting reclamation kernel arguments; use Reclamation options")
		}
	}
	switch s.config.GuestReclamationProfile {
	case "agentenv":
		if selected.MinAgeSeconds != 0 || selected.QuotaMibPerSecond != 0 || selected.QuotaTimeMs != 0 || selected.ReportingOrder != nil {
			return fmt.Errorf("agentenv reclaim profile conflicts with individual reclamation options")
		}
		if b := request.BalloonDevice; b != nil && (b.AmountMib != 0 || b.StatsPollingIntervals != 0) {
			return fmt.Errorf("agentenv reclaim profile requires zero balloon target and stats polling interval")
		}
		start, end, err := agentENVMonitorRegion(runtime.GOARCH, *s.machineConfig.MachineCfg.MemSizeMib)
		if err != nil {
			return err
		}
		s.machineConfig.KernelArgs += fmt.Sprintf(" damon_reclaim.monitor_region_start=%d damon_reclaim.monitor_region_end=%d", start, end) +
			" damon_reclaim.enabled=Y damon_reclaim.skip_anon=Y damon_reclaim.min_age=100000" +
			" damon_reclaim.quota_ms=20 damon_reclaim.quota_sz=1073741824 damon_reclaim.quota_reset_interval_ms=500" +
			" damon_reclaim.wmarks_high=990 damon_reclaim.wmarks_mid=990 damon_reclaim.wmarks_low=200" +
			" damon_reclaim.wmarks_interval=1000000 page_reporting.page_reporting_order=6"
		if request.BalloonDevice == nil {
			request.BalloonDevice = &proto.FirecrackerBalloonDevice{}
		}
		request.BalloonDevice.FreePageReporting = true
		request.BalloonDevice.DeflateOnOom = true
		request.Reclamation = selected
		return nil
	case "":
		// Use the original reclaim policy.
	default:
		return fmt.Errorf("unknown guest reclamation profile %q", s.config.GuestReclamationProfile)
	}
	options := gproto.Clone(selected).(*proto.GuestReclamation)
	if options.MinAgeSeconds == 0 {
		options.MinAgeSeconds = 5
	}
	if options.QuotaMibPerSecond == 0 {
		options.QuotaMibPerSecond = 16
	}
	if options.QuotaTimeMs == 0 {
		options.QuotaTimeMs = 5
	}
	if options.ReportingOrder == nil {
		order := uint32(9)
		options.ReportingOrder = &order
	}
	if options.MinAgeSeconds > 3600 || options.QuotaMibPerSecond > 1024 || options.QuotaTimeMs > 100 || *options.ReportingOrder > 9 {
		return fmt.Errorf("reclamation requires age 1..3600 s, quota 1..1024 MiB/s, action time 1..100 ms/s, reporting order 0..9")
	}
	if request.BalloonDevice == nil {
		request.BalloonDevice = &proto.FirecrackerBalloonDevice{StatsPollingIntervals: 1}
	}
	request.BalloonDevice.FreePageReporting = true
	request.Reclamation = options
	// Cold-page selection and quotas remain active even when the guest has ample
	// free RAM. Anonymous folios are excluded; shmem has no separate filter.
	s.machineConfig.KernelArgs += fmt.Sprintf(
		" damon_reclaim.enabled=Y damon_reclaim.skip_anon=Y damon_reclaim.min_age=%d"+
			" damon_reclaim.sample_interval=5000 damon_reclaim.aggr_interval=100000"+
			" damon_reclaim.quota_ms=%d damon_reclaim.quota_sz=%d damon_reclaim.quota_reset_interval_ms=1000"+
			" damon_reclaim.quota_mem_pressure_us=0 damon_reclaim.quota_autotune_feedback=0"+
			" damon_reclaim.wmarks_high=1000 damon_reclaim.wmarks_mid=1000 damon_reclaim.wmarks_low=0"+
			" page_reporting.page_reporting_order=%d",
		uint64(options.MinAgeSeconds)*1000000, options.QuotaTimeMs, uint64(options.QuotaMibPerSecond)<<20, *options.ReportingOrder)
	return nil
}

// AgentENV 2b981b3 accounts for the holes in Firecracker's guest physical address space.
func agentENVMonitorRegion(arch string, memoryMiB int64) (int64, int64, error) {
	const gib = 1 << 30
	size := memoryMiB << 20
	switch arch {
	case "amd64":
		switch {
		case size > 255*gib:
			return 4096, size + 257*gib, nil
		case size > 3*gib:
			return 4096, size + gib, nil
		default:
			return 4096, size, nil
		}
	case "arm64":
		if size > 254*gib {
			return 2 * gib, size + 258*gib, nil
		}
		return 2 * gib, size + 2*gib, nil
	default:
		return 0, 0, fmt.Errorf("unsupported AgentENV guest architecture %s", arch)
	}
}
