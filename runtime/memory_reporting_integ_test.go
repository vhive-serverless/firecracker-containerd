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
	"context"
	"encoding/json"
	"fmt"
	"io"
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
}

func (m *reportingMetrics) ready() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.seen }

func (m *reportingMetrics) snapshot() (uint64, uint64, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reports, m.freed, m.failures, m.err
}

func (m *reportingMetrics) read(ctx context.Context, path string) {
	err := m.readFIFO(ctx, path)
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() == nil {
		m.err = err
	}
}

func (m *reportingMetrics) readFIFO(ctx context.Context, path string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err == nil {
			defer file.Close()
			stop := context.AfterFunc(ctx, func() { _ = file.Close() })
			defer stop()
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 8192), 1<<20)
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
				m.mu.Lock()
				m.seen = true
				m.reports += item.Balloon.Reports
				m.freed += item.Balloon.Freed
				m.failures += item.Balloon.Failures
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
