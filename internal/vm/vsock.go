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

package vm

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/firecracker-microvm/firecracker-go-sdk/vsock"
	"github.com/sirupsen/logrus"
)

// VSockTransportAcceptConnector retains its guest listen socket between network
// generations. The supplied context owns the port; cancelling a connection's
// Serve must not cancel this listener context. Rebinding to another port cancels
// that generation context explicitly.
func VSockTransportAcceptConnector(port uint32) IOConnector {
	var mu sync.Mutex
	var listener net.Listener
	return func(ctx context.Context, logger *logrus.Entry) <-chan IOConnectorResult {
		result := make(chan IOConnectorResult, 1)
		mu.Lock()
		if listener == nil {
			var err error
			listener, err = vsock.Listener(ctx, logger, port)
			if err != nil {
				mu.Unlock()
				result <- IOConnectorResult{Err: err}
				close(result)
				return result
			}
			owned := listener
			context.AfterFunc(ctx, func() { _ = owned.Close() })
		}
		owned := listener
		mu.Unlock()
		go func() {
			defer close(result)
			conn, err := owned.Accept()
			result <- IOConnectorResult{ReadWriteCloser: conn, Err: err}
		}()
		return result
	}
}

// VSockDialConnector returns an IOConnector for establishing vsock connections
// that are dialed from the host to a guest listener.
func VSockDialConnector(timeout time.Duration, udsPath string, port uint32) IOConnector {
	return func(procCtx context.Context, logger *logrus.Entry) <-chan IOConnectorResult {
		returnCh := make(chan IOConnectorResult, 1)

		go func() {
			defer close(returnCh)
			timeoutCtx, cancel := context.WithTimeout(procCtx, timeout)
			defer cancel()

			conn, err := vsock.DialContext(timeoutCtx, udsPath, port, vsock.WithLogger(logger))
			returnCh <- IOConnectorResult{
				ReadWriteCloser: conn,
				Err:             err,
			}
		}()

		return returnCh
	}
}

// VSockAcceptConnector provides an IOConnector that establishes the connection by listening
// on the provided guest-side vsock port and accepting the first connection that comes in.
func VSockAcceptConnector(port uint32) IOConnector {
	return func(procCtx context.Context, logger *logrus.Entry) <-chan IOConnectorResult {
		returnCh := make(chan IOConnectorResult, 1)

		listener, err := vsock.Listener(procCtx, logger, port)
		if err != nil {
			returnCh <- IOConnectorResult{
				Err: err,
			}
			close(returnCh)
			return returnCh
		}
		stopClose := context.AfterFunc(procCtx, func() { _ = listener.Close() })

		go func() {
			defer stopClose()
			defer close(returnCh)
			defer listener.Close()

			conn, err := listener.Accept()
			returnCh <- IOConnectorResult{
				ReadWriteCloser: conn,
				Err:             err,
			}
		}()

		return returnCh
	}
}
