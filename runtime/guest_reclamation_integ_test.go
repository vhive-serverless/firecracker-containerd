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
    "io"
    "path/filepath"
    "strconv"
    "strings"
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

func TestGuestReclamation_Isolated(t *testing.T) {
    integtest.Prepare(t)
    ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), defaultNamespace), 180*time.Second)
    defer cancel()
    client, err := containerd.New(integtest.ContainerdSockPath, containerd.WithDefaultRuntime(firecrackerRuntime))
    require.NoError(t, err)
    defer client.Close()
    control, err := integtest.NewFCControlClient(integtest.ContainerdSockPath)
    require.NoError(t, err)
    defer control.Close()
    const id = "damon-reclamation"
    metrics := &reportingMetrics{}
    metricsPath := filepath.Join(t.TempDir(), "metrics.fifo")
    go metrics.read(ctx, metricsPath)
    response, err := control.CreateVM(ctx, &proto.CreateVMRequest{VMID: id, MetricsFifoPath: metricsPath,
        MachineCfg: &proto.FirecrackerMachineConfiguration{MemSizeMib: 512},
        Reclamation: &proto.GuestReclamation{Enabled: true}})
    require.NoError(t, err)
    defer func() { _, err := control.StopVM(context.WithoutCancel(ctx), &proto.StopVMRequest{VMID: id}); require.NoError(t, err) }()
    balloon, err := control.GetBalloonConfig(ctx, &proto.GetBalloonConfigRequest{VMID: id})
    require.NoError(t, err)
    require.True(t, balloon.BalloonConfig.FreePageReporting)
    require.Zero(t, balloon.BalloonConfig.AmountMib)
    directory, err := vm.ShimDir(integtest.ShimBaseDir(), defaultNamespace, id)
    require.NoError(t, err)
    state, err := vm.ReadRecoveryState(directory)
    require.NoError(t, err)
    api := fc.NewClient(response.SocketPath, logrus.NewEntry(logrus.New()), false)
    image, err := alpineImage(ctx, client, defaultSnapshotterName)
    require.NoError(t, err)
    container, err := client.NewContainer(ctx, id+"-task", containerd.WithSnapshotter(defaultSnapshotterName),
        containerd.WithNewSnapshot(id+"-task", image),
        containerd.WithNewSpec(firecrackeroci.WithVMID(id), oci.WithDefaultPathEnv,
            oci.WithProcessArgs("/bin/sh", "-ec", `export RAM_TOKEN=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n'); exec sleep 200`)))
    require.NoError(t, err)
    defer container.Delete(ctx, containerd.WithSnapshotCleanup)
    process, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, io.Discard, io.Discard)))
    require.NoError(t, err)
    require.NoError(t, process.Start(ctx))
    defer func() {
        stopped, err := process.Wait(ctx); require.NoError(t, err)
        require.NoError(t, process.Kill(ctx, syscall.SIGKILL, containerd.WithKillAll))
        select { case <-stopped: case <-ctx.Done(): t.Error(ctx.Err()) }
        _, err = process.Delete(ctx); require.NoError(t, err)
    }()
    const module = "/sys/module/damon_reclaim/parameters/"
    require.Equal(t, "Y", nativeExec(ctx, t, process, "cat "+module+"enabled"))
    require.Equal(t, "Y", nativeExec(ctx, t, process, "cat "+module+"skip_anon"))
    require.Equal(t, "5000000", nativeExec(ctx, t, process, "cat "+module+"min_age"))
    require.Equal(t, "9", nativeExec(ctx, t, process, "cat /sys/module/page_reporting/parameters/page_reporting_order"))
    daemon, err := strconv.ParseInt(nativeExec(ctx, t, process, "cat "+module+"kdamond_pid"), 10, 64)
    require.NoError(t, err); require.Positive(t, daemon)
    const canaryCommand = `tr '\000' '\n' </proc/1/environ | grep '^RAM_TOKEN='`
    canary := nativeExec(ctx, t, process, canaryCommand)
    require.True(t, strings.HasPrefix(canary, "RAM_TOKEN="))
    require.Greater(t, len(canary), len("RAM_TOKEN="))
    checksum := nativeExec(ctx, t, process, "dd if=/dev/urandom of=/state bs=1M count=128 2>/dev/null; sync; sha256sum /state")
    filled, err := vmmRSSKiB(state.VMM.PID)
    require.NoError(t, err)
    before := guestReclaimedBytes(ctx, t, process)
    deadline := time.Now().Add(60*time.Second)
    var idle int64
    var reclaimed, reports, freed, failures uint64
    for {
        reclaimed = guestReclaimedBytes(ctx, t, process)
        _, err = api.CreateSyncAction(ctx, &models.InstanceActionInfo{ActionType: fc.String(models.InstanceActionInfoActionTypeFlushMetrics)})
        require.NoError(t, err)
        idle, err = vmmRSSKiB(state.VMM.PID)
        require.NoError(t, err)
        var readErr error
        reports, freed, failures, readErr = metrics.snapshot()
        require.NoError(t, readErr)
        require.Zero(t, failures)
        if reclaimed >= before+(32<<20) && filled-idle > 32*1024 && reports > 0 && freed > 0 { break }
        if time.Now().After(deadline) { t.Fatalf("cold file was retained but reclaim/release did not converge: reclaimed_before=%d reclaimed=%d rss_filled=%d rss_idle=%d reports=%d freed=%d", before,reclaimed,filled,idle,reports,freed) }
        time.Sleep(time.Second)
    }
    // File still exists throughout idle; reading it back must fault data in.
    require.Equal(t, checksum, nativeExec(ctx, t, process, "sha256sum /state"))
    reloaded, err := vmmRSSKiB(state.VMM.PID)
    require.NoError(t, err)
    require.Greater(t, reloaded-idle, int64(32*1024), "retained cold data must be read back into resident guest memory")
    nativeExec(ctx, t, process, "dd if=/dev/urandom of=/reuse bs=1M count=64 2>/dev/null; rm /reuse; sync")
    require.Equal(t, canary, nativeExec(ctx, t, process, canaryCommand), "owned anonymous process memory must survive reclamation and page reuse")
    require.Equal(t, checksum, nativeExec(ctx, t, process, "sha256sum /state"))
    stats, err := control.GetBalloonStats(ctx, &proto.GetBalloonStatsRequest{VMID: id})
    require.NoError(t, err); require.Zero(t, stats.ActualMib)
    t.Logf("damon_enabled=true skip_anon=true report_order=9 target_mib=0 filled_rss_kib=%d idle_rss_kib=%d reloaded_rss_kib=%d reclaimed_delta_bytes=%d reports=%d reported_bytes=%d failures=%d", filled,idle,reloaded,reclaimed-before,reports,freed,failures)
}

func guestReclaimedBytes(ctx context.Context, t *testing.T, process containerd.Task) uint64 {
    t.Helper()
    value := nativeExec(ctx, t, process, "cat /sys/module/damon_reclaim/parameters/bytes_reclaimed_regions")
    result, err := strconv.ParseUint(value, 10, 64)
    require.NoError(t, err, fmt.Sprintf("invalid DAMON byte counter %q", value))
    return result
}
