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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/firecracker-microvm/firecracker-containerd/internal/integtest"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"
	fc "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestFreePageReporting_Isolated(t *testing.T) {
	integtest.Prepare(t)
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			ctx = namespaces.WithNamespace(ctx, defaultNamespace)
			client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(firecrackerRuntime))
			require.NoError(t, err)
			defer client.Close()
			control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
			require.NoError(t, err)
			defer control.Close()
			id := fmt.Sprintf("reporting-%v", enabled)
			metricsPath := filepath.Join(t.TempDir(), "metrics.fifo")
			metrics := &reportingMetrics{}
			go metrics.read(ctx, metricsPath)
			response, err := control.CreateVM(ctx, &proto.CreateVMRequest{
				VMID: id, MetricsFifoPath: metricsPath,
				MachineCfg:    &proto.FirecrackerMachineConfiguration{MemSizeMib: 512},
				BalloonDevice: &proto.FirecrackerBalloonDevice{AmountMib: 0, StatsPollingIntervals: 1, FreePageReporting: enabled}})
			require.NoError(t, err)
			defer func() {
				_, err := control.StopVM(context.WithoutCancel(ctx), &proto.StopVMRequest{VMID: id})
				require.NoError(t, err)
			}()
			balloon, err := control.GetBalloonConfig(ctx, &proto.GetBalloonConfigRequest{VMID: id})
			require.NoError(t, err)
			require.Equal(t, enabled, balloon.BalloonConfig.FreePageReporting)
			require.Zero(t, balloon.BalloonConfig.AmountMib, "reporting must not require balloon inflation")
			directory, err := vm.ShimDir(integtest.ShimBaseDir(), defaultNamespace, id)
			require.NoError(t, err)
			state, err := vm.ReadRecoveryState(directory)
			require.NoError(t, err)
			api := fc.NewClient(response.SocketPath, logrus.NewEntry(logrus.New()), false)
			image, err := alpineImage(ctx, client, defaultSnapshotterName)
			require.NoError(t, err)
			container, err := client.NewContainer(ctx, id+"-task", containerd.WithSnapshotter(defaultSnapshotterName),
				containerd.WithNewSnapshot(id+"-task", image),
				containerd.WithNewSpec(firecrackeroci.WithVMID(id), oci.WithDefaultPathEnv, oci.WithProcessArgs("sleep", "200")))
			require.NoError(t, err)
			defer container.Delete(ctx, containerd.WithSnapshotCleanup)
			process, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, io.Discard, io.Discard)))
			require.NoError(t, err)
			require.NoError(t, process.Start(ctx))
			defer func() {
				stopped, err := process.Wait(ctx)
				require.NoError(t, err)
				require.NoError(t, process.Kill(ctx, syscall.SIGKILL, containerd.WithKillAll))
				select {
				case <-stopped:
				case <-ctx.Done():
					t.Error(ctx.Err())
				}
				_, err = process.Delete(ctx)
				require.NoError(t, err)
			}()
			checksum := nativeExec(ctx, t, process, "dd if=/dev/urandom of=/state bs=1M count=16 2>/dev/null; sync; sha256sum /state")
			nativeExec(ctx, t, process, "dd if=/dev/zero of=/cache bs=1M count=64 2>/dev/null; sync")
			filled, err := vmmRSSKiB(state.VMM.PID)
			require.NoError(t, err)
			nativeExec(ctx, t, process, "rm /cache; sync")
			var idle int64
			require.Eventually(t, func() bool {
				_, err := api.CreateSyncAction(ctx, &models.InstanceActionInfo{ActionType: fc.String(models.InstanceActionInfoActionTypeFlushMetrics)})
				require.NoError(t, err)
				require.NoError(t, metrics.awaitFlushed(ctx))
				idle, err = vmmRSSKiB(state.VMM.PID)
				require.NoError(t, err)
				reports, freed, failures, readErr := metrics.snapshot()
				require.NoError(t, readErr)
				require.Zero(t, failures)
				if !enabled {
					return metrics.ready() && reports == 0 && freed == 0
				}
				return reports > 0 && freed > 0 && filled-idle > 32*1024
			}, 30*time.Second, 500*time.Millisecond)
			// Exercise fresh allocations after discard, then verify retained data.
			nativeExec(ctx, t, process, "dd if=/dev/urandom of=/reuse bs=1M count=64 2>/dev/null; rm /reuse; sync")
			require.Equal(t, checksum, nativeExec(ctx, t, process, "sha256sum /state"))
			stats, err := control.GetBalloonStats(ctx, &proto.GetBalloonStatsRequest{VMID: id})
			require.NoError(t, err)
			require.Zero(t, stats.ActualMib)
			reports, freed, failures, readErr := metrics.snapshot()
			require.NoError(t, readErr)
			t.Logf("reporting=%v target_mib=0 filled_rss_kib=%d idle_rss_kib=%d reports=%d reported_bytes=%d failures=%d", enabled, filled, idle, reports, freed, failures)
		})
	}
}

func vmmRSSKiB(pid int) (int64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", pid))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "Rss:" {
			return strconv.ParseInt(fields[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("missing VMM RSS")
}

type reportingMetrics struct {
	mu                       sync.Mutex
	reports, freed, failures uint64
	seen                     bool
	err                      error
	fifo                     *reportingMetricsFIFO
	parsedBytes              uint64
	changed                  chan struct{}
	beforeAggregate          func([]byte) // Optional deterministic reader gate in unit tests.
}

func (m *reportingMetrics) ready() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.seen }

func (m *reportingMetrics) snapshot() (uint64, uint64, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reports, m.freed, m.failures, m.err
}

// awaitFlushed must be called after a successful synchronous FlushMetrics API
// response. That response guarantees its complete newline-terminated record was
// written. Count bytes already read (including scanner buffering) plus bytes
// still queued in the FIFO, then wait until aggregation has consumed that prefix.
func (m *reportingMetrics) awaitFlushed(ctx context.Context) error {
	var fifo *reportingMetricsFIFO
	for fifo == nil {
		m.mu.Lock()
		err := m.err
		fifo = m.fifo
		changed := m.changedLocked()
		m.mu.Unlock()
		if err != nil {
			return err
		}
		if fifo == nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-changed:
			}
		}
	}
	target, err := fifo.watermark()
	if err != nil {
		return err
	}
	if target == 0 {
		return errors.New("successful metrics flush produced no bytes")
	}
	for {
		m.mu.Lock()
		err := m.err
		complete := m.parsedBytes >= target
		changed := m.changedLocked()
		m.mu.Unlock()
		if err != nil {
			return err
		}
		if complete {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (m *reportingMetrics) changedLocked() <-chan struct{} {
	if m.changed == nil {
		m.changed = make(chan struct{})
	}
	return m.changed
}

func (m *reportingMetrics) notifyLocked() {
	if m.changed != nil {
		close(m.changed)
		m.changed = nil
	}
}

// The raw descriptor stays nonblocking. Never hold mu across a poll or a Go
// File.Read, which can wait for more bytes while a flush needs its watermark.
type reportingMetricsFIFO struct {
	mu        sync.Mutex
	fd        int
	readBytes uint64
	closed    bool
	ctx       context.Context
}

func (f *reportingMetricsFIFO) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	for {
		if err := f.ctx.Err(); err != nil {
			return 0, err
		}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return 0, os.ErrClosed
		}
		n, err := unix.Read(f.fd, buffer)
		if n > 0 {
			f.readBytes += uint64(n)
		}
		f.mu.Unlock()
		if n > 0 {
			return n, err
		}
		if err == nil {
			return 0, io.EOF
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return 0, err
		}
		// A bounded readiness poll allows cancellation without sleeping to guess
		// whether the metrics producer or parser has caught up.
		fd := f.fd
		if fd < 0 || fd > math.MaxInt32 {
			return 0, errors.New("metrics FIFO descriptor exceeds poll range")
		}
		_, err = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 50)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return 0, err
		}
	}
}

func (f *reportingMetricsFIFO) watermark() (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	// TIOCINQ is Linux FIONREAD. The same lock covers every nonblocking read
	// and its byte count, so no byte can fall between these two quantities.
	pending, err := unix.IoctlGetInt(f.fd, unix.TIOCINQ)
	if err != nil {
		return 0, err
	}
	if pending < 0 {
		return 0, errors.New("negative metrics FIFO byte count")
	}
	return f.readBytes + uint64(pending), nil
}

func (f *reportingMetricsFIFO) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		_ = unix.Close(f.fd)
	}
}

func (m *reportingMetrics) read(ctx context.Context, path string) {
	err := m.readFIFO(ctx, path)
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() == nil {
		if err == nil {
			err = io.EOF
		}
		m.err = err
	}
	m.notifyLocked()
}

func (m *reportingMetrics) readFIFO(ctx context.Context, path string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == nil {
			file := &reportingMetricsFIFO{fd: fd, ctx: ctx}
			defer file.close()
			stop := context.AfterFunc(ctx, file.close)
			defer stop()
			m.mu.Lock()
			m.fifo = file
			m.notifyLocked()
			m.mu.Unlock()
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 8192), 1<<20)
			// Retain the newline so parsedBytes measures the raw stream exactly.
			scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
				if index := bytes.IndexByte(data, '\n'); index >= 0 {
					return index + 1, data[:index+1], nil
				}
				if atEOF && len(data) > 0 {
					return 0, nil, io.ErrUnexpectedEOF
				}
				return 0, nil, nil
			})
			for scanner.Scan() {
				var item struct {
					Balloon struct {
						Reports  uint64 `json:"free_page_report_count"`
						Freed    uint64 `json:"free_page_report_freed"`
						Failures uint64 `json:"free_page_report_fails"`
					} `json:"balloon"`
				}
				if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
					return err
				}
				if m.beforeAggregate != nil {
					m.beforeAggregate(scanner.Bytes())
				}
				m.mu.Lock()
				m.seen = true
				m.reports += item.Balloon.Reports
				m.freed += item.Balloon.Freed
				m.failures += item.Balloon.Failures
				m.parsedBytes += uint64(len(scanner.Bytes()))
				m.notifyLocked()
				m.mu.Unlock()
			}
			return scanner.Err()
		}
		if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
