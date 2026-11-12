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
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// Diagnostic-only: write without logrus or journald locks. The harness enables
// the restored VMM console stream so these records remain visible after fork.
func shutdownTrace(format string, args ...any) {
	file, err := os.OpenFile("/dev/console", os.O_WRONLY|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	defer file.Close()
	data := []byte(fmt.Sprintf("\nSHUTDOWN_DIAGNOSTIC pid=%d at=%s %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano), fmt.Sprintf(format, args...)))
	// A diagnostic must never wait for a Go logger, pipe lock or writable console.
	for len(data) > 0 {
		chunk := data
		if len(chunk) > 4096 {
			chunk = chunk[:4096]
		}
		written, err := syscall.Write(int(file.Fd()), chunk)
		if err != nil || written == 0 {
			return
		}
		data = data[written:]
	}
}

var shutdownDiagnosticOnce sync.Once

func startShutdownDiagnostic() {
	shutdownDiagnosticOnce.Do(func() {
		go func() {
			time.Sleep(2 * time.Second)
			stack := make([]byte, 256<<10)
			count := runtime.Stack(stack, true)
			shutdownTrace("watchdog2s goroutine stack bytes=%d\n%s", count, stack[:count])
		}()
	})
}
