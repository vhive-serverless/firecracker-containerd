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
	"encoding/json"

	"github.com/containerd/log"
	crimeta "github.com/firecracker-microvm/firecracker-containerd/internal/cri"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-go-sdk/vsock"
	"github.com/opencontainers/runtime-spec/specs-go"
)

func (ts *TaskService) ensureCRIPortForward(specData []byte) error {
	var spec specs.Spec
	if err := json.Unmarshal(specData, &spec); err != nil {
		return err
	}
	_, isCRI, err := crimeta.Parse(&spec)
	if err != nil || !isCRI {
		return err
	}
	ts.criForwardOnce.Do(func() {
		logger := log.G(ts.shimCtx)
		listener, err := vsock.Listener(ts.shimCtx, logger, vm.PodForwardPort)
		if err != nil {
			ts.criForwardErr = err
			return
		}
		go func() {
			<-ts.shimCtx.Done()
			_ = listener.Close()
		}()
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					if ts.shimCtx.Err() == nil {
						logger.WithError(err).Error("guest port-forward listener stopped")
					}
					return
				}
				go func() {
					if err := vm.ServeGuestTCP(ts.shimCtx, conn); err != nil {
						logger.WithError(err).Debug("guest port-forward closed")
					}
				}()
			}
		}()
	})
	return ts.criForwardErr
}
