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

// Command firecracker-cri serves the Firecracker CRI integration.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/firecracker-microvm/firecracker-containerd/cri"
)

var revision = "unknown"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var options cri.Options
	var version bool
	flag.StringVar(&options.Socket, "address", "/run/firecracker-containerd/cri.sock", "CRI Unix socket")
	flag.StringVar(&options.BackendSocket, "backend", "/run/firecracker-containerd/containerd.sock", "containerd Unix socket")
	flag.StringVar(&options.StreamAddress, "stream-address", "127.0.0.1:0", "Pod streaming listen address")
	flag.StringVar(&options.RuntimeConfig, "runtime-config", "", "same Firecracker runtime configuration as the shim")
	flag.BoolVar(&version, "version", false, "print source revision")
	flag.Parse()
	if version {
		fmt.Println("firecracker-cri", revision)
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := cri.Run(ctx, options); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
