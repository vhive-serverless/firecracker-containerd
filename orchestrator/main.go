// Copyright 2018-2019 Amazon.com, Inc. or its affiliates. All Rights Reserved.
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
	"flag"
	_ "fmt"
	_ "io/ioutil"
	"log"
	"net"
	"syscall"
	_ "time"

	"github.com/containerd/containerd"
	_ "github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/pkg/errors"

	fcclient "github.com/firecracker-microvm/firecracker-containerd/firecracker-control/client"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"

        "google.golang.org/grpc"
	pb "github.com/firecracker-microvm/firecracker-containerd/proto_orch"
	"os"
        "os/signal"
        "sync"
)

const (
	containerdAddress      = "/run/firecracker-containerd/containerd.sock"
	containerdTTRPCAddress = containerdAddress + ".ttrpc"
	namespaceName          = "firecracker-containerd"

        port = ":3333"
)

type VM struct {
    Ctx context.Context
    Image containerd.Image
    Container containerd.Container
    VMID string
}

var active_vms []VM
var snapshotter *string
var client *containerd.Client
var fcClient *fcclient.Client
var ctx context.Context
var g_err error
var mu = &sync.Mutex{}

func main() {
    snapshotter = flag.String("ss", "devmapper", "snapshotter")

    log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
    flag.Parse()

    setupCloseHandler()
    log.Println("Creating containerd client")
    client, g_err = containerd.New(containerdAddress)
    if g_err != nil {
        log.Fatalf("Failed to start containerd client", g_err)
    }
    log.Println("Created containerd client")

    ctx = namespaces.WithNamespace(context.Background(), namespaceName)

    fcClient, g_err = fcclient.New(containerdTTRPCAddress)
    if g_err != nil {
        log.Fatalf("Failed to start firecracker client", g_err)
    }

    lis, err := net.Listen("tcp", port)
    if err != nil {
        log.Fatalf("failed to listen: %v", err)
    }
    s := grpc.NewServer()
    pb.RegisterOrchestratorServer(s, &server{})
    log.Println("Listening on port" + port)
    if err := s.Serve(lis); err != nil {
        log.Fatalf("failed to serve: %v", err)
    }
}

type server struct {
	pb.UnimplementedOrchestratorServer
}

func (s *server) StartVM(ctx_ context.Context, in *pb.StartVMReq) (*pb.Status, error) {
    log.Printf("Received: %v", in.GetImage())
    //image, err := client.Pull(ctx, "docker.io/library/ustiugov/helloworld:runner_workload",
    image, err := client.Pull(ctx, "docker.io/" + in.GetImage(),
                              containerd.WithPullUnpack,
                              containerd.WithPullSnapshotter(*snapshotter),
                             )
    if err != nil {
        return &pb.Status{Message: "Starting VM failed"}, errors.Wrapf(err, "creating container")
    }

    vmID := in.GetId()
    createVMRequest := &proto.CreateVMRequest{
        VMID: vmID,
        MachineCfg: &proto.FirecrackerMachineConfiguration{
            VcpuCount:  1,
            MemSizeMib: 512,
        },
        NetworkInterfaces: []*proto.FirecrackerNetworkInterface{{
            CNIConfig: &proto.CNIConfiguration{
                NetworkName: "fcnet",
                InterfaceName: "veth0",
            },
        }},
    }

    _, err = fcClient.CreateVM(ctx, createVMRequest)
    if err != nil {
        return &pb.Status{Message: "Failed to start VM"}, errors.Wrap(err, "failed to create the VM")
    }
    container, err := client.NewContainer(
                                          ctx,
                                          in.GetId(),
                                          containerd.WithSnapshotter(*snapshotter),
                                          containerd.WithNewSnapshot(in.GetId(), image),
                                          containerd.WithNewSpec(
                                                                 oci.WithImageConfig(image),
                                                                 firecrackeroci.WithVMID(vmID),
                                                                 firecrackeroci.WithVMNetwork,
                                                                 //oci.WithProcessArgs("head", "/proc/meminfo"),
                                                                ),
                                          containerd.WithRuntime("aws.firecracker", nil),
                                         )
    if err != nil {
        return &pb.Status{Message: "Failed to start container for the VM" + in.GetId() }, err
    }

    mu.Lock()
    active_vms = append(active_vms, VM{Ctx: ctx, Image: image, Container: container, VMID: vmID})
    mu.Unlock()
    //TODO: set up port forwarding to a private IP

    return &pb.Status{Message: "started VM " + in.GetId() }, nil
}

func (s *server) StopVMs(ctx_ context.Context, in *pb.StopVMsReq) (*pb.Status, error) {
    log.Printf("Received StopVMs request")
    err := stopActiveVMs()
    if err != nil {
        log.Printf("Failed to stop VMs, err: %v\n", err)
        return &pb.Status{Message: "Failed to stop VMs"}, err
    }
    os.Exit(0)
    return &pb.Status{Message: "Stopped VMs"}, nil
}

func stopActiveVMs() error {
    mu.Lock()
    for _, vm := range active_vms {
        log.Println("Deleting container for the VM" + vm.VMID)
        err := vm.Container.Delete(vm.Ctx, containerd.WithSnapshotCleanup)
        if err != nil {
            log.Printf("failed to delete container for the VM, err: %v\n", err)
            return err
        }

        log.Println("Stopping the VM" + vm.VMID)
        _, err = fcClient.StopVM(vm.Ctx, &proto.StopVMRequest{VMID: vm.VMID})
        if err != nil {
            log.Printf("failed to stop the VM, err: %v\n", err)
            return err
        }
    }
    log.Println("Closing fcClient")
    fcClient.Close()
    log.Println("Closing containerd client")
    client.Close()
    mu.Unlock()
    return nil
}

func setupCloseHandler() {
    c := make(chan os.Signal, 2)
    signal.Notify(c, os.Interrupt, syscall.SIGTERM)
    go func() {
        <-c
        log.Println("\r- Ctrl+C pressed in Terminal")
        stopActiveVMs()
        os.Exit(0)
    }()
}

/*
func taskWorkflow() (err error) {
        id := "0"
	vmID := "fc-example"+id
	createVMRequest := &proto.CreateVMRequest{
		VMID: vmID,
		// Enabling Go Race Detector makes in-microVM binaries heavy in terms of CPU and memory.
                MachineCfg: &proto.FirecrackerMachineConfiguration{
                    VcpuCount:  1,
                    MemSizeMib: 512,
                },
                NetworkInterfaces: []*proto.FirecrackerNetworkInterface{{
                    CNIConfig: &proto.CNIConfiguration{
                        NetworkName: "fcnet",
                        InterfaceName: "veth0",
                    },
                }},
	}

	_, err = fcClient.CreateVM(ctx, createVMRequest)
	if err != nil {
		return errors.Wrap(err, "failed to create VM")
	}

	defer func() {
		_, stopErr := fcClient.StopVM(ctx, &proto.StopVMRequest{VMID: vmID})
		if stopErr != nil {
			log.Printf("failed to stop VM, err: %v\n", stopErr)
		}
		if err == nil {
			err = stopErr
		}
	}()

	log.Printf("Successfully pulled %s image with %s\n", image.Name(), *snapshotter)
	container, err := client.NewContainer(
		ctx,
		"demo"+id,
		containerd.WithSnapshotter(*snapshotter),
		containerd.WithNewSnapshot("demo-snapshot"+id, image),
		containerd.WithNewSpec(
			oci.WithImageConfig(image),
			firecrackeroci.WithVMID(vmID),
			firecrackeroci.WithVMNetwork,
			//oci.WithProcessArgs("head", "/proc/meminfo"),
		),
		containerd.WithRuntime("aws.firecracker", nil),
	)
	if err != nil {
		return err
	}
	defer container.Delete(ctx, containerd.WithSnapshotCleanup)

        start := time.Now()
        for i := 0; i < 1; i++ {
            id := fmt.Sprintf("id-%d", i)
            container, err := client.NewContainer(ctx, id,
	    containerd.WithSnapshotter(*snapshotter),
            containerd.WithNewSnapshotView(id, image),
            containerd.WithNewSpec(oci.WithImageConfig(image)),
            )
            if err != nil {
                return errors.Wrapf(err, "creating container ")

            }
            if i % 100 == 0 {
                log.Printf("Spanned %d VMs", i)
            }
            defer container.Delete(ctx, containerd.WithSnapshotCleanup)
        }
        elapsed := time.Since(start)
        log.Printf("Spanning VMs took %s", elapsed)

	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
	if err != nil {
		return errors.Wrapf(err, "creating task")

	}
	defer task.Delete(ctx)

	log.Printf("Successfully created task: %s for the container\n", task.ID())
	exitStatusC, err := task.Wait(ctx)
	if err != nil {
		return errors.Wrapf(err, "waiting for task")

	}

	log.Println("Completed waiting for the container task")
	if err := task.Start(ctx); err != nil {
		return errors.Wrapf(err, "starting task")

	}

	log.Println("Successfully started the container task")
	time.Sleep(3 * time.Second)

	if err := task.Kill(ctx, syscall.SIGTERM); err != nil {
		return errors.Wrapf(err, "killing task")
	}

	status := <-exitStatusC
	code, _, err := status.Result()
	if err != nil {
		return errors.Wrapf(err, "getting task's exit code")
	}
	log.Printf("task exited with status: %d\n", code)
        return
}
*/
