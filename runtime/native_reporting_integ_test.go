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
	"sync/atomic"
	"testing"
	"time"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/stretchr/testify/require"
)

// checkNativeRestoreReportingActivity exercises discard through the restored
// balloon queues, with the source VM already gone. The caller checks that the
// restored balloon target and actual size remain zero. RSS is an activity check,
// not a physical-memory census or a quantitative savings estimate.
func checkNativeRestoreReportingActivity(ctx context.Context, t *testing.T, backend taskAPI.TTRPCTaskService, id string, vmmPID int) {
	t.Helper()
	directory := fmt.Sprintf("/native-restored-reporting-%d", time.Now().UnixNano())
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		nativeRestoredExec(cleanupCtx, t, backend, id, fmt.Sprintf("rm -rf -- %s", directory))
	}()

	checksum := nativeRestoredExec(ctx, t, backend, id, fmt.Sprintf(`
        mkdir %[1]s
        dd if=/dev/urandom of=%[1]s/retained bs=1M count=16 2>/dev/null
        sync
        sha256sum %[1]s/retained
    `, directory))
	require.NotEmpty(t, checksum)
	baseline, err := vmmRSSKiB(vmmPID)
	require.NoError(t, err)
	nativeRestoredExec(ctx, t, backend, id, fmt.Sprintf(`
        dd if=/dev/zero of=%[1]s/cache bs=1M count=64 2>/dev/null
        sync
        cat %[1]s/cache >/dev/null
    `, directory))
	// The final read keeps cache pages hot if sync took longer than the DAMON
	// cold-age threshold. Do not require growth from baseline: snapshot restore
	// may already have resident free guest pages that the write can reuse.
	filled, err := vmmRSSKiB(vmmPID)
	require.NoError(t, err)

	var observed atomic.Int64
	observed.Store(filled)
	var readErrors atomic.Int64
	var lastReadError atomic.Value
	lastReadError.Store("")
	verified := false
	defer func() {
		lastRSS := observed.Load()
		t.Logf("restored_reporting_task=%s vmm_pid=%d baseline_rss_kib=%d filled_rss_kib=%d post_free_rss_kib=%d rss_drop_kib=%d rss_read_errors=%d last_rss_read_error=%q retained_checksum_verified=%v",
			id, vmmPID, baseline, filled, lastRSS, filled-lastRSS, readErrors.Load(), lastReadError.Load(), verified)
	}()
	nativeRestoredExec(ctx, t, backend, id, fmt.Sprintf("rm %s/cache; sync", directory))
	require.Eventually(t, func() bool {
		current, err := vmmRSSKiB(vmmPID)
		if err != nil {
			readErrors.Add(1)
			lastReadError.Store(err.Error())
			return false
		}
		observed.Store(current)
		lastReadError.Store("")
		return filled-current >= 32*1024
	}, 30*time.Second, 500*time.Millisecond,
		"restored balloon must discard at least 32 MiB of resident guest cache without inflation; filled_rss_kib=%d", filled)

	// Fresh nonzero writes exercise reused memory after discard. The independent
	// retained file and the parent's resumed shell token/counter must survive.
	require.Equal(t, checksum, nativeRestoredExec(ctx, t, backend, id, fmt.Sprintf(`
        dd if=/dev/urandom of=%[1]s/reuse bs=1M count=64 2>/dev/null
        sync
        sha256sum %[1]s/retained
    `, directory)))
	verified = true
}
