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
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/sys/reaper"
	"github.com/containerd/log"
	"github.com/containerd/ttrpc"
	"github.com/firecracker-microvm/firecracker-go-sdk/vsock"
	"github.com/opencontainers/runc/libcontainer/system"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"

	"github.com/firecracker-microvm/firecracker-containerd/eventbridge"

	drivemount "github.com/firecracker-microvm/firecracker-containerd/proto/service/drivemount/ttrpc"
	ioproxy "github.com/firecracker-microvm/firecracker-containerd/proto/service/ioproxy/ttrpc"
)

const (
	defaultPort      = 10789
	defaultNamespace = namespaces.Default

	// Restored guests may retain an RPC connection to the source host. Do not
	// let an unreachable peer prevent agent exit and the guest reboot action.
	agentRPCShutdownGrace = time.Second

	// per prctl(2), we must provide a non-zero arg when calling prctl with
	// PR_SET_CHILD_SUBREAPER in order to enable subreaping (0 disables it)
	enableSubreaper = 1
)

var (
	revision string
)

func main() {
	var (
		port    int
		debug   bool
		version bool
	)

	flag.IntVar(&port, "port", defaultPort, "Vsock port to listen to")
	flag.BoolVar(&debug, "debug", false, "Turn on debug mode")
	flag.BoolVar(&version, "version", false, "Show the version")
	flag.Parse()

	if debug {
		logrus.SetLevel(logrus.DebugLevel)
	}

	if version {
		showVersion()
		return
	}

	signals := make(chan os.Signal, 32)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, unix.SIGCHLD)

	shimCtx, shimCancel := context.WithCancel(namespaces.WithNamespace(context.Background(), defaultNamespace))
	group, shimCtx := errgroup.WithContext(shimCtx)

	// Ensure this process is a subreaper or else containers created via runc will
	// not be its children.
	if err := system.SetSubreaper(enableSubreaper); err != nil {
		log.G(shimCtx).WithError(err).Fatal("failed to set shim as subreaper")
	}

	// Create a runc task service that can be used via GRPC.
	// This can be wrapped to add missing functionality (like
	// running multiple containers inside one Firecracker VM)

	log.G(shimCtx).Info("creating task service")

	server, err := ttrpc.NewServer()
	if err != nil {
		log.G(shimCtx).WithError(err).Fatal("failed to create ttrpc server")
	}

	eventJournal := eventbridge.NewJournal(8 << 20)
	eventbridge.RegisterJournalService(server, eventJournal)

	taskService, err := NewTaskService(shimCtx, shimCancel, eventJournal)
	if err != nil {
		log.G(shimCtx).WithError(err).Fatal("failed to create task service")
	}
	taskAPI.RegisterTTRPCTaskService(server, taskService)

	dh, err := newDriveHandler(blockPath, drivePath)
	if err != nil {
		log.G(shimCtx).WithError(err).Fatal("failed to create drive handler")
	}
	drivemount.RegisterDriveMounterService(server, dh)

	ioproxy.RegisterIOProxyService(server, &ioProxyHandler{
		runcService: taskService.runcService,
		taskManager: taskService.taskManager,
	})

	// Run ttrpc over vsock

	vsockLogger := log.G(shimCtx).WithField("port", port)
	listener, err := vsock.Listener(shimCtx, vsockLogger, uint32(port))
	if err != nil {
		log.G(shimCtx).WithError(err).Fatalf("failed to listen to vsock on port %d", port)
	}

	shutdownTrace("main RPC ready revision=%s", revision)
	group.Go(func() error {
		shutdownTrace("Serve begin")
		err := server.Serve(shimCtx, listener)
		shutdownTrace("Serve returned err=%v", err)
		if err == ttrpc.ErrServerClosed {
			// Calling server.Shutdown() from another goroutine will cause ErrServerClosed, which is fine.
			return nil
		}
		return err
	})

	group.Go(func() error {
		defer func() {
			shutdownTrace("shutdown loop defer before stopping log")
			log.G(shimCtx).Info("stopping ttrpc server")
			shutdownTrace("shutdown loop before drain")
			// Flush responses on live sessions, but bound the drain independently
			// of the canceled shim context. A snapshot-captured stale connection
			// can otherwise keep this goroutine, and therefore the agent, alive.
			if err := shutdownAgentServer(server, agentRPCShutdownGrace); err != nil {
				shutdownTrace("shutdown loop drain error=%v before warning log", err)
				log.G(shimCtx).WithError(err).Warn("agent RPC drain did not finish; closing server")
				shutdownTrace("shutdown loop warning log returned")
			}
			shutdownTrace("shutdown loop defer returned")
		}()

		for {
			select {
			case s := <-signals:
				switch s {
				case unix.SIGCHLD:
					shutdownTrace("SIGCHLD reaper.Reap begin")
					if err := reaper.Reap(); err != nil {
						log.G(shimCtx).WithError(err).Error("reap error")
					}
					shutdownTrace("SIGCHLD reaper.Reap returned")
				case syscall.SIGINT, syscall.SIGTERM:
					shimCancel()
					return nil
				}
			case <-shimCtx.Done():
				shutdownTrace("shutdown loop observed cancellation err=%v", shimCtx.Err())
				return shimCtx.Err()
			}
		}
	})

	shutdownTrace("main before group.Wait")
	err = group.Wait()
	shutdownTrace("main group.Wait returned err=%v before final log", err)
	log.G(shimCtx).Info("shutting down agent")
	shutdownTrace("main final log returned")

	if err != nil && err != context.Canceled {
		log.G(shimCtx).WithError(err).Error("shim error")
		shutdownTrace("main about to panic err=%v", err)
		panic(err)
	}
	shutdownTrace("main returning success")
}

func showVersion() {
	// Once https://github.com/golang/go/issues/29814 is resolved,
	// we can use runtime/debug.BuildInfo instead of calling git(1) from Makefile
	fmt.Printf("containerd Firecracker agent (git commit: %s)\n", revision)
}

// shutdownAgentServer lets live RPCs flush before agent exit. ttrpc Shutdown
// waits for all connections, including captured peers that may never make
// progress after restore. Close stops accepting work when the grace expires;
// process exit then releases those stale transports and triggers guest reboot.
func shutdownAgentServer(server *ttrpc.Server, grace time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	shutdownTrace("drain Shutdown begin grace=%s", grace)
	if err := server.Shutdown(ctx); err != nil {
		shutdownTrace("drain Shutdown returned err=%v before Close", err)
		closeErr := server.Close()
		shutdownTrace("drain Close returned err=%v", closeErr)
		return errors.Join(err, closeErr)
	}
	shutdownTrace("drain Shutdown returned success")
	return nil
}
