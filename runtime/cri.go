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
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/containernetworking/cni/libcni"
	"github.com/firecracker-microvm/firecracker-containerd/config"
	crimeta "github.com/firecracker-microvm/firecracker-containerd/internal/cri"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"
	"github.com/firecracker-microvm/firecracker-go-sdk/cni/vmconf"
	"github.com/opencontainers/runtime-spec/specs-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// criVMRequest adopts containerd's CNI namespace. CNI remains its owner; the
// shim neither allocates another Pod IP nor deletes the external namespace.
func criVMRequest(spec *specs.Spec, cfg *config.Config) (*proto.CreateVMRequest, error) {
	pod, isCRI, err := crimeta.Parse(spec)
	if err != nil || !isCRI {
		return nil, err
	}
	if id := spec.Annotations[firecrackeroci.VMIDAnnotationKey]; id != "" && id != pod.SandboxID {
		return nil, status.Error(codes.InvalidArgument, "CRI sandbox ID conflicts with explicit VM ID")
	}
	// x86 MMIO has 19 available GSIs. Reserve five for root, network,
	// vsock and the two ACPI devices before allocating workload drives.
	if cfg.CRIContainerCount < 2 || cfg.CRIContainerCount > 14 {
		return nil, status.Error(codes.InvalidArgument, "cri_container_count must be between 2 and 14, including the sandbox")
	}
	request := &proto.CreateVMRequest{
		VMID: pod.SandboxID, ContainerCount: cfg.CRIContainerCount,
		ExitAfterAllTasksDeleted: true,
	}
	if !pod.Sandbox {
		// The caller verifies the sandbox's existing VM and uses its shim
		// socket for the new workload task.
		return request, nil
	}
	if spec.Linux == nil {
		return nil, status.Error(codes.InvalidArgument, "CRI sandbox has no Linux configuration")
	}
	var podPID, podIPC bool
	for _, ns := range spec.Linux.Namespaces {
		switch ns.Type {
		case specs.NetworkNamespace:
			request.NetNS = ns.Path
		case specs.PIDNamespace:
			podPID = true
		case specs.IPCNamespace:
			podIPC = true
		}
	}
	if request.NetNS == "" {
		return nil, status.Error(codes.Unimplemented, "Firecracker CRI host-network Pods are not supported")
	}
	if !podPID || !podIPC {
		return nil, status.Error(codes.Unimplemented, "Firecracker CRI host-PID and host-IPC Pods are not supported")
	}
	files, err := libcni.ConfFiles(cfg.CRICNIConfDir, []string{".conf", ".conflist", ".json"})
	if err != nil {
		return nil, fmt.Errorf("read CRI CNI configuration: %w", err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no CNI configuration in %s", cfg.CRICNIConfDir)
	}
	network, err := libcni.NetworkConfFromFile(files[0])
	if err != nil {
		return nil, fmt.Errorf("load CRI CNI configuration: %w", err)
	}
	cni := libcni.NewCNIConfigWithCacheDir(nil, cfg.CRICNICacheDir, nil)
	result, err := cni.GetNetworkListCachedResult(network, &libcni.RuntimeConf{
		ContainerID: pod.SandboxID, NetNS: request.NetNS, IfName: "eth0",
	})
	if err != nil || result == nil {
		return nil, fmt.Errorf("CRI CNI allocation is unavailable for sandbox %s: %v", pod.SandboxID, err)
	}
	networkConfig, err := vmconf.StaticNetworkConfFrom(result, pod.SandboxID)
	if err != nil {
		return nil, fmt.Errorf("CRI CNI chain must end with tc-redirect-tap: %w", err)
	}
	if networkConfig.NetNSPath != request.NetNS || networkConfig.VMIPConfig.Address.IP.To4() == nil {
		return nil, status.Error(codes.Unimplemented, "CRI requires one IPv4 TAP in the sandbox network namespace")
	}
	request.NetworkInterfaces = []*proto.FirecrackerNetworkInterface{{
		StaticConfig: &proto.StaticNetworkConfiguration{
			HostDevName: networkConfig.TapName, MacAddress: networkConfig.VMMacAddr,
			IPConfig: &proto.IPConfiguration{
				PrimaryAddr: networkConfig.VMIPConfig.Address.String(),
				GatewayAddr: networkConfig.VMIPConfig.Gateway.String(),
				Nameservers: networkConfig.VMNameservers,
			},
		},
	}}
	return request, nil
}

// prepareCRISpec translates host-only namespace and generated-file references.
// Arbitrary host bind mounts need a coherent sharing transport and are rejected
// until implemented; copying writable volumes would violate their semantics.
func prepareCRISpec(extra *proto.ExtraData) error {
	var spec specs.Spec
	if err := json.Unmarshal(extra.JsonSpec, &spec); err != nil {
		return err
	}
	pod, isCRI, err := crimeta.Parse(&spec)
	if err != nil || !isCRI {
		return err
	}
	if spec.Linux == nil {
		return status.Error(codes.InvalidArgument, "CRI container has no Linux configuration")
	}
	namespaces := spec.Linux.Namespaces[:0]
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type != specs.NetworkNamespace {
			namespaces = append(namespaces, ns)
		}
	}
	spec.Linux.Namespaces = namespaces
	for i := range spec.Mounts {
		mnt := &spec.Mounts[i]
		if mnt.Type != "bind" {
			continue
		}
		switch mnt.Destination {
		case "/dev/shm":
			if mnt.Source == "/dev/shm" {
				return status.Error(codes.Unimplemented, "Firecracker CRI host-IPC Pods are not supported")
			}
			mnt.Source = pod.ShmPath()
		case "/etc/resolv.conf", "/etc/hosts", "/etc/hostname":
			file, err := readCRIFile(mnt.Source)
			if err != nil {
				return err
			}
			extra.CriFiles = append(extra.CriFiles, file)
		default:
			return status.Errorf(codes.Unimplemented, "Firecracker CRI bind mount %q is not yet supported", mnt.Destination)
		}
	}
	extra.JsonSpec, err = json.Marshal(&spec)
	return err
}

func readCRIFile(path string) (*proto.CRIFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, status.Error(codes.InvalidArgument, "CRI generated configuration must be a regular file")
	}
	contents, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return nil, err
	}
	if len(contents) > 65536 {
		return nil, status.Error(codes.ResourceExhausted, "CRI generated configuration exceeds 64 KiB")
	}
	return &proto.CRIFile{Source: path, Contents: contents, Mode: uint32(info.Mode().Perm())}, nil
}
