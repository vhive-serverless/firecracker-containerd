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

package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/firecracker-microvm/firecracker-containerd/snapshotter/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const signalHelperEnv = "FC_DEMUX_SIGNAL_HELPER"

func TestRunIgnoresSIGPIPEAndStopsOnSIGTERM(t *testing.T) {
	if os.Getenv(signalHelperEnv) == "1" {
		runSignalHandlingHelper(t)
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestRunIgnoresSIGPIPEAndStopsOnSIGTERM$")
	cmd.Env = append(os.Environ(), signalHelperEnv+"=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("signal helper failed: %v\n%s", err, output)
	}
}

func runSignalHandlingHelper(t *testing.T) {
	t.Helper()

	socketPath := filepath.Join(t.TempDir(), "snapshotter.sock")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configText := fmt.Sprintf(`[snapshotter.listener]
network = "unix"
address = %q

[snapshotter.proxy.address.resolver]
type = "http"
address = "http://127.0.0.1:1"

[snapshotter.dialer]
timeout = "1s"
retry_interval = "10ms"

[snapshotter.cache]
evict_on_connection_failure = false
`, socketPath)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}

	runResult := make(chan error, 1)
	go func() { runResult <- Run(cfg) }()
	stopped := false
	defer func() {
		if stopped {
			return
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		select {
		case <-runResult:
		case <-time.After(5 * time.Second):
		}
	}()

	if err := waitForSnapshotter(socketPath, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	// Observe delivery of the kernel signal raised by a real EPIPE before
	// checking that the service remains available.
	sigpipe := make(chan os.Signal, 1)
	signal.Notify(sigpipe, syscall.SIGPIPE)
	defer signal.Stop(sigpipe)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	_, writeErr := writer.Write([]byte("broken pipe probe"))
	_ = writer.Close()
	if !errors.Is(writeErr, syscall.EPIPE) {
		t.Fatalf("write to closed non-stdio pipe returned %v, want EPIPE", writeErr)
	}
	select {
	case got := <-sigpipe:
		if got != syscall.SIGPIPE {
			t.Fatalf("received signal %v, want SIGPIPE", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the EPIPE write did not produce an observable SIGPIPE")
	}

	// Let Run's signal loop process the already-observed notification, then
	// require both that Run remains active and that it still serves gRPC.
	select {
	case err := <-runResult:
		stopped = true
		t.Fatalf("demux stopped after SIGPIPE: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	if err := probeSnapshotter(socketPath, 2*time.Second); err != nil {
		t.Fatalf("demux did not remain available after SIGPIPE: %v", err)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-runResult:
		stopped = true
		if err != nil {
			t.Fatalf("Run returned error after SIGTERM: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("demux did not stop after SIGTERM")
	}
}

func waitForSnapshotter(socketPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		err := probeSnapshotter(socketPath, 250*time.Millisecond)
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("snapshotter did not become available: %w", lastErr)
}

func probeSnapshotter(socketPath string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := grpc.NewClient("passthrough:///signal-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}),
	)
	if err != nil {
		return err
	}
	defer conn.Close()
	var response emptypb.Empty
	err = conn.Invoke(ctx, "/signal.Test/Probe", &emptypb.Empty{}, &response)
	if status.Code(err) != codes.Unimplemented {
		return fmt.Errorf("gRPC server probe returned %v, want Unimplemented", err)
	}
	return nil
}
