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
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func reportingReaderFixture(t *testing.T, gate func([]byte)) (*reportingMetrics, *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "metrics.fifo")
	require.NoError(t, unix.Mkfifo(path, 0600))
	ctx, cancel := context.WithCancel(context.Background())
	m := &reportingMetrics{beforeAggregate: gate}
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.read(ctx, path)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("metrics reader did not stop after cancellation")
		}
	})
	writer, err := os.OpenFile(path, os.O_RDWR|unix.O_NONBLOCK, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.Close() })
	return m, writer
}

// Identical timestamps deliberately cannot distinguish these records.
func reportingReaderFrame(count uint64) []byte {
	return []byte(fmt.Sprintf("{\"utc_timestamp_ms\":0,\"balloon\":{\"free_page_report_count\":%d,\"free_page_report_freed\":%d}}\n", count, count*4096))
}

func TestReportingMetricsAwaitFlushedBuffered(t *testing.T) {
	blocked, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	m, writer := reportingReaderFixture(t, func(line []byte) {
		if bytes.Contains(line, []byte(`"free_page_report_count":7,`)) {
			close(blocked)
			<-release
		}
	})
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := writer.Write(reportingReaderFrame(1))
	require.NoError(t, err)
	require.NoError(t, m.awaitFlushed(ctx))
	require.True(t, m.ready(), "a startup frame has already been consumed")

	// This successful write models the API response: both complete frames are
	// already in the FIFO, while the first one's aggregation is held back.
	_, err = writer.Write(append(reportingReaderFrame(7), reportingReaderFrame(11)...))
	require.NoError(t, err)
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	short, stop := context.WithTimeout(ctx, 25*time.Millisecond)
	err = m.awaitFlushed(short)
	stop()
	require.ErrorIs(t, err, context.DeadlineExceeded, "already-seen or prefetched bytes cannot acknowledge unaggregated records")
	reports, _, _, err := m.snapshot()
	require.NoError(t, err)
	require.Equal(t, uint64(1), reports)
	unblock.Do(func() { close(release) })
	require.NoError(t, m.awaitFlushed(ctx))
	reports, freed, failures, err := m.snapshot()
	require.NoError(t, err)
	require.Equal(t, uint64(19), reports)
	require.Equal(t, uint64(19*4096), freed)
	require.Zero(t, failures)
}

func TestReportingMetricsAwaitFlushedPartial(t *testing.T) {
	m, writer := reportingReaderFixture(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	old, final := reportingReaderFrame(1), reportingReaderFrame(7)
	_, err := writer.Write(old)
	require.NoError(t, err)
	require.NoError(t, m.awaitFlushed(ctx))
	_, err = writer.Write(final[:len(final)-1])
	require.NoError(t, err)
	short, stop := context.WithTimeout(ctx, 25*time.Millisecond)
	err = m.awaitFlushed(short)
	stop()
	require.ErrorIs(t, err, context.DeadlineExceeded, "a complete JSON object without its newline must remain unacknowledged")
	_, err = writer.Write([]byte{'\n'})
	require.NoError(t, err)
	require.NoError(t, m.awaitFlushed(ctx))
	reports, freed, failures, err := m.snapshot()
	require.NoError(t, err)
	require.Equal(t, uint64(8), reports)
	require.Equal(t, uint64(8*4096), freed)
	require.Zero(t, failures)
	m.mu.Lock()
	parsed := m.parsedBytes
	m.mu.Unlock()
	require.Equal(t, uint64(len(old)+len(final)), parsed, "acknowledged offsets must include each newline exactly once")
}

func TestReportingMetricsAwaitFlushedCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := &reportingMetrics{}
	require.ErrorIs(t, m.awaitFlushed(ctx), context.Canceled, "cancellation must also work before the FIFO opens")
}
