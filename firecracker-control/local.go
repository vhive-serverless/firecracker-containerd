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

package service

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd/identifiers"
	"github.com/containerd/containerd/log"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/plugin"
	"github.com/containerd/containerd/runtime/v2/shim"
	"github.com/containerd/containerd/sys"
	"github.com/gogo/protobuf/types"
	"github.com/hashicorp/go-multierror"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/firecracker-microvm/firecracker-containerd/config"
	fcclient "github.com/firecracker-microvm/firecracker-containerd/firecracker-control/client"
	"github.com/firecracker-microvm/firecracker-containerd/internal"
	fcShim "github.com/firecracker-microvm/firecracker-containerd/internal/shim"
	"github.com/firecracker-microvm/firecracker-containerd/internal/vm"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	fccontrolTtrpc "github.com/firecracker-microvm/firecracker-containerd/proto/service/fccontrol/ttrpc"
)

var (
	_               fccontrolTtrpc.FirecrackerService = (*local)(nil)
	ttrpcAddressEnv                                   = "TTRPC_ADDRESS"
	stopVMInterval                                    = 10 * time.Millisecond
)

func init() {
	plugin.Register(&plugin.Registration{
		Type: plugin.ServicePlugin,
		ID:   localPluginID,
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			log.G(ic.Context).Debugf("initializing %s plugin (root: %q)", localPluginID, ic.Root)
			return newLocal(ic)
		},
	})
}

type local struct {
	containerdAddress string
	logger            *logrus.Entry
	config            *config.Config

	processesMu sync.Mutex
	processes   map[string]int32
}

// ShimResources holds resources allocated during shim preparation
type ShimResources struct {
	VMID              string
	Namespace         string
	ShimSocketAddress string
	ShimSocket        *net.UnixListener
	FCSocketAddress   string
	FCSocket          *net.UnixListener
	ShimDir           *vm.Dir
	Cmd               *exec.Cmd
}

func newLocal(ic *plugin.InitContext) (*local, error) {
	if err := os.MkdirAll(ic.Root, 0750); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("failed to create root directory: %s: %w", ic.Root, err)
	}

	cfg, err := config.LoadConfig("")
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return &local{
		containerdAddress: ic.Address,
		logger:            log.G(ic.Context),
		config:            cfg,
		processes:         make(map[string]int32),
	}, nil
}

// PrepareShim is a gRPC method that prepares all resources needed for a shim without creating the VM.
// This allows the shim to be created in advance before the CreateVM call.
func (s *local) PrepareShim(requestCtx context.Context, req *proto.PrepareShimRequest) (*proto.PrepareShimResponse, error) {
	resources, err := s.prepareShim(requestCtx, req.VMID)
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			if removeErr := s.removeShim(resources); removeErr != nil {
				s.logger.WithError(removeErr).Warn("failed to cleanup shim resources after prepare error")
			}
		}
	}()

	client, err := s.shimFirecrackerClient(requestCtx, req.VMID)
	if err != nil {
		err = fmt.Errorf("failed to create firecracker shim client for prepare: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	_, err = client.PrepareShim(requestCtx, req)
	client.Close()
	if err != nil {
		err = fmt.Errorf("shim PrepareShim returned error: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	// Register the shim process for tracking so it can be killed later
	s.addShim(resources.ShimSocketAddress, resources.Cmd)

	return &proto.PrepareShimResponse{
		VMID:              resources.VMID,
		Namespace:         resources.Namespace,
		ShimSocketAddress: resources.ShimSocketAddress,
		FCSocketAddress:   resources.FCSocketAddress,
	}, nil
}

// RemoveShim is a gRPC method that cleans up a prepared shim that will not be used.
func (s *local) RemoveShim(requestCtx context.Context, req *proto.RemoveShimRequest) (*types.Empty, error) {
	// s.logger.Debugf("removing shim for VM %s", req.VMID)
	if err := identifiers.Validate(req.VMID); err != nil {
		s.logger.WithError(err).Error()
		return nil, err
	}

	// ns, err := namespaces.NamespaceRequired(requestCtx)
	// if err != nil {
	// 	err = fmt.Errorf("error retrieving namespace of request: %w", err)
	// 	s.logger.WithError(err).Error()
	// 	return nil, err
	// }
	ns := req.VMID

	// Get socket addresses
	shimSocketAddress, err := shim.SocketAddress(requestCtx, s.containerdAddress, req.VMID)
	if err != nil {
		err = fmt.Errorf("failed to obtain shim socket address: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	fcSocketAddress, err := fcShim.FCControlSocketAddress(requestCtx, s.containerdAddress, req.VMID)
	if err != nil {
		err = fmt.Errorf("failed to obtain fccontrol socket address: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	shimDir, err := vm.ShimDir(s.config.ShimBaseDir, ns, req.VMID)
	if err != nil {
		err = fmt.Errorf("failed to build shim path: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	// Create resources struct for cleanup
	resources := &ShimResources{
		VMID:              req.VMID,
		Namespace:         ns,
		ShimSocketAddress: shimSocketAddress,
		FCSocketAddress:   fcSocketAddress,
		ShimDir:           &shimDir,
	}

	// Find and kill the shim process
	s.processesMu.Lock()
	pid, ok := s.processes[shimSocketAddress]
	if ok {
		delete(s.processes, shimSocketAddress)
	}
	s.processesMu.Unlock()

	if ok {
		// Kill the process
		if err := syscall.Kill(int(pid), syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			s.logger.WithError(err).Warn("failed to kill shim process")
		}
	}

	if err := s.removeShim(resources); err != nil {
		return nil, err
	}

	return &types.Empty{}, nil
}

// shimExists checks if a shim is already running for the given VMID
func (s *local) shimExists(requestCtx context.Context, vmID string) (bool, error) {
	shimSocketAddress, err := shim.SocketAddress(requestCtx, s.containerdAddress, vmID)
	if err != nil {
		return false, fmt.Errorf("failed to obtain shim socket address: %w", err)
	}

	// Try to create a listener on the socket address
	// If it succeeds, no shim exists (we close it immediately)
	// If we get EADDRINUSE, a shim already exists
	listener, err := shim.NewSocket(shimSocketAddress)
	if err == nil {
		// No shim exists, close the listener we just created
		listener.Close()
		shim.RemoveSocket(shimSocketAddress)
		return false, nil
	}

	if shim.SocketEaddrinuse(err) {
		// A shim already exists
		return true, nil
	}

	// Some other error occurred
	return false, fmt.Errorf("failed to check for existing shim: %w", err)
}

// prepareShim prepares all resources needed for a shim without creating the VM.
// This is the internal implementation called by both PrepareShim gRPC method and CreateVM.
func (s *local) prepareShim(requestCtx context.Context, vmID string) (*ShimResources, error) {
	var err error

	if err := identifiers.Validate(vmID); err != nil {
		s.logger.WithError(err).Error()
		return nil, err
	}

	// ns, err := namespaces.NamespaceRequired(requestCtx)
	// if err != nil {
	// 	err = fmt.Errorf("error retrieving namespace of request: %w", err)
	// 	s.logger.WithError(err).Error()
	// 	return nil, err
	// }
	ns := vmID

	s.logger.Debugf("preparing shim for VM %s in namespace: %s", vmID, ns)

	// We determine if there is already a shim managing a VM with the current VMID by attempting
	// to listen on the abstract socket address (which is parameterized by VMID). If we get
	// EADDRINUSE, then we assume there is already a shim for the VM and return an AlreadyExists error.
	shimSocketAddress, err := shim.SocketAddress(requestCtx, s.containerdAddress, vmID)
	if err != nil {
		err = fmt.Errorf("failed to obtain shim socket address: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	shimSocket, err := shim.NewSocket(shimSocketAddress)
	if shim.SocketEaddrinuse(err) {
		return nil, status.Errorf(codes.AlreadyExists, "VM with ID %q already exists (socket: %q)", vmID, shimSocketAddress)
	} else if err != nil {
		err = fmt.Errorf("failed to open shim socket at address %q: %w", shimSocketAddress, err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	resources := &ShimResources{
		VMID:              vmID,
		Namespace:         ns,
		ShimSocketAddress: shimSocketAddress,
		ShimSocket:        shimSocket,
	}

	// Cleanup on error
	defer func() {
		if err != nil {
			s.cleanupShimResources(resources)
		}
	}()

	// Create shim base directory
	if err = os.Mkdir(s.config.ShimBaseDir, 0700); err != nil && !os.IsExist(err) {
		s.logger.WithError(err).Error()
		return nil, fmt.Errorf("failed to make shim base directory: %s: %w", s.config.ShimBaseDir, err)
	}

	shimDir, err := vm.ShimDir(s.config.ShimBaseDir, ns, vmID)
	if err != nil {
		err = fmt.Errorf("failed to build shim path: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}
	resources.ShimDir = &shimDir

	err = shimDir.Mkdir()
	if err != nil {
		err = fmt.Errorf("failed to create VM dir %q: %w", shimDir.RootPath(), err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	// Create fccontrol socket
	fcSocketAddress, err := fcShim.FCControlSocketAddress(requestCtx, s.containerdAddress, vmID)
	if err != nil {
		err = fmt.Errorf("failed to obtain shim socket address: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}
	resources.FCSocketAddress = fcSocketAddress

	fcSocket, err := shim.NewSocket(fcSocketAddress)
	if err != nil {
		err = fmt.Errorf("failed to open fccontrol socket at address %q: %w", fcSocketAddress, err)
		s.logger.WithError(err).Error()
		return nil, err
	}
	resources.FCSocket = fcSocket

	// Start the shim process
	cmd, err := s.newShim(ns, vmID, s.containerdAddress, shimSocket, fcSocket)
	if err != nil {
		return nil, err
	}
	resources.Cmd = cmd

	s.logger.Debugf("shim prepared successfully for VM %s", vmID)
	return resources, nil
}

// RemoveShim cleans up a prepared shim that will not be used.
// This is the internal implementation.
func (s *local) removeShim(resources *ShimResources) error {
	if resources == nil {
		return nil
	}

	s.logger.Debugf("removing unused shim for VM %s", resources.VMID)

	var result *multierror.Error

	// Kill the shim process if it was started
	if resources.Cmd != nil && resources.Cmd.Process != nil {
		if err := resources.Cmd.Process.Kill(); err != nil {
			s.logger.WithError(err).Warn("failed to kill shim process")
			result = multierror.Append(result, fmt.Errorf("failed to kill shim process: %w", err))
		}
	}

	// Clean up all resources
	if err := s.cleanupShimResources(resources); err != nil {
		result = multierror.Append(result, err)
	}

	return result.ErrorOrNil()
}

// cleanupShimResources is a helper function to clean up shim resources
func (s *local) cleanupShimResources(resources *ShimResources) error {
	if resources == nil {
		return nil
	}

	var result *multierror.Error

	// Remove sockets
	if resources.ShimSocket != nil {
		if err := resources.ShimSocket.Close(); err != nil {
			s.logger.WithError(err).Warn("failed to close shim socket")
			result = multierror.Append(result, err)
		}
		if resources.ShimSocketAddress != "" {
			if err := shim.RemoveSocket(resources.ShimSocketAddress); err != nil {
				s.logger.WithError(err).Warn("failed to remove shim socket")
				result = multierror.Append(result, err)
			}
		}
	}

	if resources.FCSocket != nil {
		if err := resources.FCSocket.Close(); err != nil {
			s.logger.WithError(err).Warn("failed to close fccontrol socket")
			result = multierror.Append(result, err)
		}
		if resources.FCSocketAddress != "" {
			if err := shim.RemoveSocket(resources.FCSocketAddress); err != nil {
				s.logger.WithError(err).Warn("failed to remove fccontrol socket")
				result = multierror.Append(result, err)
			}
		}
	}

	// Remove shim directory
	if resources.ShimDir != nil {
		if err := os.RemoveAll(resources.ShimDir.RootPath()); err != nil {
			s.logger.WithError(err).WithField("path", resources.ShimDir.RootPath()).Warn("failed to remove VM dir")
			result = multierror.Append(result, err)
		}
	}

	return result.ErrorOrNil()
}

// CreateVM creates new Firecracker VM instance. It creates a runtime shim for the VM and the forwards
// the CreateVM request to that shim. If there is already a VM created with the provided VMID, then
// AlreadyExists is returned. If a shim was pre-created using PrepareShim, it will be used.
func (s *local) CreateVM(requestCtx context.Context, req *proto.CreateVMRequest) (*proto.CreateVMResponse, error) {
	var err error

	id := req.GetVMID()
	if err := identifiers.Validate(id); err != nil {
		s.logger.WithError(err).Error()
		return nil, err
	}

	// Check if a shim was already prepared for this VMID
	shimExists, err := s.shimExists(requestCtx, id)
	if err != nil {
		return nil, err
	}

	var resources *ShimResources

	if !shimExists {
		// No pre-created shim, so prepare a new one
		resources, err = s.prepareShim(requestCtx, id)
		if err != nil {
			return nil, err
		}

		// Ensure cleanup on error (only if we created the shim)
		defer func() {
			if err != nil {
				s.removeShim(resources)
			}
		}()
	} else {
		s.logger.Debugf("using pre-created shim for VM %s", id)
	}

	// Create firecracker client to communicate with the shim
	client, err := s.shimFirecrackerClient(requestCtx, id)
	if err != nil {
		err = fmt.Errorf("failed to create firecracker shim client: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	defer client.Close()

	// Forward the CreateVM request to the shim
	resp, err := client.CreateVM(requestCtx, req)
	if err != nil {
		s.logger.WithError(err).Error("shim CreateVM returned error")
		return nil, err
	}

	// Register the shim process for tracking (only if we created it)
	if resources != nil {
		s.addShim(resources.ShimSocketAddress, resources.Cmd)
	}

	return resp, nil
}

func (s *local) addShim(address string, cmd *exec.Cmd) {
	s.processesMu.Lock()
	defer s.processesMu.Unlock()
	s.processes[address] = int32(cmd.Process.Pid)
}

func (s *local) shimFirecrackerClient(requestCtx context.Context, vmID string) (*fcclient.Client, error) {
	if err := identifiers.Validate(vmID); err != nil {
		return nil, fmt.Errorf("invalid id: %w", err)
	}

	socketAddr, err := fcShim.FCControlSocketAddress(requestCtx, s.containerdAddress, vmID)
	if err != nil {
		err = fmt.Errorf("failed to get shim's fccontrol socket address: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	return fcclient.New(socketAddr)
}

// StopVM stops running VM instance by VM ID. This stops the VM, all tasks within the VM and the runtime shim
// managing the VM.
func (s *local) StopVM(requestCtx context.Context, req *proto.StopVMRequest) (*types.Empty, error) {
	client, err := s.shimFirecrackerClient(requestCtx, req.VMID)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	resp, shimErr := client.StopVM(requestCtx, req)
	waitErr := s.waitForShimToExit(requestCtx, req.VMID)

	// Assuming the shim is returning containerd's error code, return the error as is if possible.
	if waitErr == nil {
		return resp, shimErr
	}
	return resp, multierror.Append(shimErr, waitErr).ErrorOrNil()
}

// PauseVM pauses a VM
func (s *local) PauseVM(ctx context.Context, req *proto.PauseVMRequest) (*types.Empty, error) {
	client, err := s.shimFirecrackerClient(ctx, req.VMID)
	if err != nil {
		return nil, err
	}

	defer client.Close()

	resp, err := client.PauseVM(ctx, req)
	if err != nil {
		s.logger.WithError(err).Error()
		return nil, err
	}

	return resp, nil
}

// CreateSnapshot creates a snapshot of a VM.
func (s *local) CreateSnapshot(ctx context.Context, req *proto.CreateSnapshotRequest) (*types.Empty, error) {
	client, err := s.shimFirecrackerClient(ctx, req.VMID)
	if err != nil {
		return nil, err
	}

	defer client.Close()

	resp, err := client.CreateSnapshot(ctx, req)
	if err != nil {
		s.logger.WithError(err).Error()
		return nil, err
	}

	return resp, nil
}

// ResumeVM resumes a VM
func (s *local) ResumeVM(ctx context.Context, req *proto.ResumeVMRequest) (*types.Empty, error) {
	client, err := s.shimFirecrackerClient(ctx, req.VMID)
	if err != nil {
		return nil, err
	}

	defer client.Close()

	resp, err := client.ResumeVM(ctx, req)
	if err != nil {
		s.logger.WithError(err).Error()
		return nil, err
	}

	return resp, nil
}

func (s *local) waitForShimToExit(ctx context.Context, vmID string) error {
	socketAddr, err := shim.SocketAddress(ctx, s.containerdAddress, vmID)
	if err != nil {
		return err
	}

	s.processesMu.Lock()
	pid, ok := s.processes[socketAddr]
	if !ok {
		s.processesMu.Unlock()
		return fmt.Errorf("failed to find a shim process for %q", socketAddr)
	}
	delete(s.processes, socketAddr)
	s.processesMu.Unlock()

	return internal.WaitForPidToExit(ctx, stopVMInterval, pid)
}

// GetVMInfo returns metadata for the VM with the given VMID.
func (s *local) GetVMInfo(requestCtx context.Context, req *proto.GetVMInfoRequest) (*proto.GetVMInfoResponse, error) {
	client, err := s.shimFirecrackerClient(requestCtx, req.VMID)
	if err != nil {
		return nil, err
	}

	defer client.Close()

	resp, err := client.GetVMInfo(requestCtx, req)
	if err != nil {
		err = fmt.Errorf("shim client failed to get vm info: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	return resp, nil
}

// SetVMMetadata sets Firecracker instance metadata for the VM with the given VMID.
func (s *local) SetVMMetadata(requestCtx context.Context, req *proto.SetVMMetadataRequest) (*types.Empty, error) {
	client, err := s.shimFirecrackerClient(requestCtx, req.VMID)
	if err != nil {
		return nil, err
	}

	defer client.Close()

	resp, err := client.SetVMMetadata(requestCtx, req)
	if err != nil {
		err = fmt.Errorf("shim client failed to set vm metadata: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	return resp, nil
}

// UpdateVMMetadata updates Firecracker instance metadata for the VM with the given VMID.
func (s *local) UpdateVMMetadata(requestCtx context.Context, req *proto.UpdateVMMetadataRequest) (*types.Empty, error) {
	client, err := s.shimFirecrackerClient(requestCtx, req.VMID)
	if err != nil {
		return nil, err
	}

	defer client.Close()

	resp, err := client.UpdateVMMetadata(requestCtx, req)
	if err != nil {
		err = fmt.Errorf("shim client failed to update vm metadata: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	return resp, nil
}

// GetVMMetadata returns the Firecracker instance metadata for the VM with the given VMID.
func (s *local) GetVMMetadata(requestCtx context.Context, req *proto.GetVMMetadataRequest) (*proto.GetVMMetadataResponse, error) {
	client, err := s.shimFirecrackerClient(requestCtx, req.VMID)
	if err != nil {
		return nil, err
	}

	defer client.Close()
	resp, err := client.GetVMMetadata(requestCtx, req)
	if err != nil {
		err = fmt.Errorf("shim client failed to get vm metadata: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	return resp, nil
}

// GetBalloonConfig get balloon configuration, before or after machine startup.
func (s *local) GetBalloonConfig(requestCtx context.Context, req *proto.GetBalloonConfigRequest) (*proto.GetBalloonConfigResponse, error) {
	client, err := s.shimFirecrackerClient(requestCtx, req.VMID)
	if err != nil {
		return nil, err
	}

	defer client.Close()
	resp, err := client.GetBalloonConfig(requestCtx, req)
	if err != nil {
		err = fmt.Errorf("shim client failed to get balloon config: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	return resp, nil
}

// UpdateBalloon updates memory size for an existing balloon device, before or after machine startup.
func (s *local) UpdateBalloon(requestCtx context.Context, req *proto.UpdateBalloonRequest) (*types.Empty, error) {
	client, err := s.shimFirecrackerClient(requestCtx, req.VMID)
	if err != nil {
		return nil, err
	}

	defer client.Close()
	resp, err := client.UpdateBalloon(requestCtx, req)
	if err != nil {
		err = fmt.Errorf("shim client failed to update balloon: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	return resp, nil
}

// GetBalloonStats will return the latest balloon device statistics, only if enabled pre-boot.
func (s *local) GetBalloonStats(requestCtx context.Context, req *proto.GetBalloonStatsRequest) (*proto.GetBalloonStatsResponse, error) {
	client, err := s.shimFirecrackerClient(requestCtx, req.VMID)
	if err != nil {
		return nil, err
	}

	defer client.Close()
	resp, err := client.GetBalloonStats(requestCtx, req)
	if err != nil {
		err = fmt.Errorf("shim client failed to get balloon statistics: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	return resp, nil
}

// UpdateBalloonStats updates an existing balloon device statistics interval, before or after machine startup.
func (s *local) UpdateBalloonStats(requestCtx context.Context, req *proto.UpdateBalloonStatsRequest) (*types.Empty, error) {
	client, err := s.shimFirecrackerClient(requestCtx, req.VMID)
	if err != nil {
		return nil, err
	}

	defer client.Close()
	resp, err := client.UpdateBalloonStats(requestCtx, req)
	if err != nil {
		err = fmt.Errorf("shim client failed to update balloon interval: %w", err)
		s.logger.WithError(err).Error()
		return nil, err
	}

	return resp, nil
}

func (s *local) newShim(ns, vmID, containerdAddress string, shimSocket *net.UnixListener, fcSocket *net.UnixListener) (*exec.Cmd, error) {
	logger := s.logger.WithField("vmID", vmID)

	args := []string{
		"-namespace", ns,
		"-address", containerdAddress,
	}

	cmd := exec.Command(internal.ShimBinaryName, args...)

	shimDir, err := vm.ShimDir(s.config.ShimBaseDir, ns, vmID)
	if err != nil {
		err = fmt.Errorf("failed to create shim dir: %w", err)
		logger.WithError(err).Error()
		return nil, err
	}

	// note: The working dir of the shim has an effect on the length of the path
	// needed to specify various unix sockets that the shim uses to communicate
	// with the firecracker VMM and guest agent within. The length of that path
	// has a relatively low limit (usually 108 chars), so modifying the working
	// dir should be done with caution. See internal/vm/dir.go for the path
	// definitions.
	cmd.Dir = shimDir.RootPath()

	shimSocketFile, err := shimSocket.File()
	if err != nil {
		err = fmt.Errorf("failed to get shim socket fd: %w", err)
		logger.WithError(err).Error()
		return nil, err
	}

	fcSocketFile, err := fcSocket.File()
	if err != nil {
		err = fmt.Errorf("failed to get shim fccontrol socket fd: %w", err)
		logger.WithError(err).Error()
		return nil, err
	}

	cmd.ExtraFiles = append(cmd.ExtraFiles, shimSocketFile, fcSocketFile)
	fcSocketFDNum := 2 + len(cmd.ExtraFiles) // "2 +" because ExtraFiles come after stderr (fd #2)

	ttrpc := containerdAddress + ".ttrpc"
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("%s=%s", ttrpcAddressEnv, ttrpc),
		fmt.Sprintf("%s=%s", internal.VMIDEnvVarKey, vmID),
		fmt.Sprintf("%s=%s", internal.FCSocketFDEnvKey, strconv.Itoa(fcSocketFDNum))) // TODO remove after containerd is updated to expose ttrpc server to shim

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	// shim stderr is just raw text, so pass it through our logrus formatter first
	cmd.Stderr = logger.WithField("shim_stream", "stderr").WriterLevel(logrus.ErrorLevel)
	// shim stdout on the other hand is already formatted by logrus, so pass that transparently through to containerd logs
	cmd.Stdout = logger.Logger.Out

	logger.Debugf("starting %s", internal.ShimBinaryName)

	err = cmd.Start()
	if err != nil {
		err = fmt.Errorf("failed to start shim child process: %w", err)
		logger.WithError(err).Error()
		return nil, err
	}

	// make sure to wait after start
	go func() {
		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				// shim is usually terminated by cancelling the context
				logger.WithError(exitErr).Debug("shim has been terminated")
			} else {
				logger.WithError(err).Error("shim has been unexpectedly terminated")
			}
		}

		// Close all Unix sockets.
		if err := shimSocketFile.Close(); err != nil {
			logger.WithError(err).Errorf("failed to close %q", shimSocketFile.Name())
		}
		if err := fcSocketFile.Close(); err != nil {
			logger.WithError(err).Errorf("failed to close %q", fcSocketFile.Name())
		}

		if err := s.removeSockets(ns, vmID); err != nil {
			logger.WithError(err).Errorf("failed to remove sockets")
		}

		if err := os.RemoveAll(shimDir.RootPath()); err != nil {
			logger.WithError(err).Errorf("failed to remove %q", shimDir.RootPath())
		}
	}()

	err = setShimOOMScore(cmd.Process.Pid)
	if err != nil {
		logger.WithError(err).Error()
		return nil, err
	}

	return cmd, nil
}

func (s *local) removeSockets(ns string, vmID string) error {
	var result *multierror.Error

	// This context is only used for passing the namespace.
	ctx := namespaces.WithNamespace(context.Background(), ns)

	address, err := shim.SocketAddress(ctx, s.containerdAddress, vmID)
	if err != nil {
		result = multierror.Append(result, err)
	} else {
		err := shim.RemoveSocket(address)
		if err != nil {
			result = multierror.Append(result, err)
		}
	}

	address, err = fcShim.FCControlSocketAddress(ctx, s.containerdAddress, vmID)
	if err != nil {
		result = multierror.Append(result, err)
	} else {
		err = shim.RemoveSocket(address)
		if err != nil {
			result = multierror.Append(result, err)
		}
	}

	return result.ErrorOrNil()
}

func setShimOOMScore(shimPid int) error {
	containerdPid := os.Getpid()

	score, err := sys.GetOOMScoreAdj(containerdPid)
	if err != nil {
		return fmt.Errorf("failed to get OOM score for containerd: %w", err)
	}

	shimScore := score + 1
	if err := sys.SetOOMScore(shimPid, shimScore); err != nil {
		return fmt.Errorf("failed to set OOM score on shim: %w", err)
	}

	return nil
}
