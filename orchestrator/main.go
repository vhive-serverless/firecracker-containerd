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
	"fmt"
	_ "io/ioutil"
	"log"
	"net"
	"syscall"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/pkg/errors"

	fcclient "github.com/firecracker-microvm/firecracker-containerd/firecracker-control/client"
	"github.com/firecracker-microvm/firecracker-containerd/proto"
	"github.com/firecracker-microvm/firecracker-containerd/runtime/firecrackeroci"

        "google.golang.org/grpc"
	pb "github.com/firecracker-microvm/firecracker-containerd/proto_orch"
	//pb "orchestrator"
)

const (
	containerdAddress      = "/run/firecracker-containerd/containerd.sock"
	containerdTTRPCAddress = containerdAddress + ".ttrpc"
	namespaceName          = "firecracker-containerd"

        port = ":3333"
)

type VM struct {
    Ctx context.Context
    VMID string
}

//var active_vms = []VM

func main() {
	var snapshotter = flag.String("ss", "devmapper", "snapshotter")

	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	flag.Parse()

	if err := taskWorkflow(*snapshotter); err != nil {
		log.Fatal(err)
	}
}

type server struct {
	pb.UnimplementedOrchestratorServer
}

func (s *server) StartVM(ctx context.Context, in *pb.StartVMReq) (*pb.Status, error) {
	log.Printf("Received: %v", in.GetImage())
	return &pb.Status{Message: true}, nil
}

func taskWorkflow(snapshotter string) (err error) {
	log.Println("Creating containerd client")
	client, err := containerd.New(containerdAddress)
	if err != nil {
		return errors.Wrapf(err, "creating client")
	}

	defer client.Close()
	log.Println("Created containerd client")

	ctx := namespaces.WithNamespace(context.Background(), namespaceName)
	//image, err := client.Pull(ctx, "docker.io/library/nginx:1.17-alpine",
        image, err := client.Pull(ctx, "docker.io/library/alpine:latest",
		containerd.WithPullUnpack,
		containerd.WithPullSnapshotter(snapshotter),
	)
	if err != nil {
		return errors.Wrapf(err, "creating container")
	}

	fcClient, err := fcclient.New(containerdTTRPCAddress)
	if err != nil {
		return err
	}

	defer fcClient.Close()

        lis, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	s := grpc.NewServer()
	pb.RegisterOrchestratorServer(s, &server{})
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}

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

	log.Printf("Successfully pulled %s image with %s\n", image.Name(), snapshotter)
	container, err := client.NewContainer(
		ctx,
		"demo"+id,
		containerd.WithSnapshotter(snapshotter),
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

        //*
        start := time.Now()
        for i := 0; i < 1; i++ {
            id := fmt.Sprintf("id-%d", i)
            container, err := client.NewContainer(ctx, id,
	    containerd.WithSnapshotter(snapshotter),
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
        //*/

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
