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
	for _, argument := range strings.Fields(s.machineConfig.KernelArgs) {
		if strings.HasPrefix(argument, "damon_reclaim.") || strings.HasPrefix(argument, "page_reporting.") {
			return fmt.Errorf("conflicting reclamation kernel arguments; use Reclamation options")
		}
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
