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

// native-volume-guest exercises a captured FUSE client's real kernel state.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/firecracker-microvm/firecracker-containerd/internal/volfs"
	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"
)

var counter int
var dataFile, orphan *os.File

type reply struct {
	Counter               int
	State, Shared, Orphan string
	Inode                 uint64
	Error                 string
}

func main() {
	for _, filesystem := range []string{"devtmpfs", "proc"} {
		path := "/dev"
		if filesystem == "proc" {
			path = "/proc"
		}
		if err := unix.Mount(filesystem, path, filesystem, 0, ""); err != nil {
			log.Fatal(err)
		}
	}
	args, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		log.Fatal(err)
	}
	var tokens []string
	for _, arg := range strings.Fields(string(args)) {
		if strings.HasPrefix(arg, "volfs.tokens=") {
			tokens = strings.Split(strings.TrimPrefix(arg, "volfs.tokens="), ",")
		}
	}
	if len(tokens) != 2 {
		log.Fatal("missing two volume capabilities")
	}
	for index, path := range []string{"/first", "/second"} {
		if err := os.Mkdir(path, 0700); err != nil {
			log.Fatal(err)
		}
		if _, err := volfs.MountGuest(context.Background(), context.Background(), path, volfs.Descriptor{Token: tokens[index]}, volfs.DialHost); err != nil {
			log.Fatal(err)
		}
	}
	listener, err := vsock.Listen(1234, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("NATIVE_VOLUME_GUEST_READY")
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Fatal(err)
		}
		decoder, encoder := json.NewDecoder(conn), json.NewEncoder(conn)
		for {
			var operation string
			if err := decoder.Decode(&operation); err != nil {
				break
			}
			result, err := command(operation)
			if err != nil {
				result.Error = err.Error()
			}
			if err := encoder.Encode(result); err != nil {
				break
			}
		}
		_ = conn.Close()
	}
}

func command(operation string) (reply, error) {
	if operation == "init" {
		counter = 41
		var err error
		dataFile, err = os.OpenFile("/first/state", os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return reply{}, err
		}
		if _, err = dataFile.WriteAt([]byte("41"), 0); err != nil {
			return reply{}, err
		}
		if err = os.Link("/first/state", "/first/link"); err != nil {
			return reply{}, err
		}
		orphan, err = os.OpenFile("/first/orphan", os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return reply{}, err
		}
		if _, err = orphan.WriteAt([]byte("orphan41"), 0); err != nil {
			return reply{}, err
		}
		if err = os.Remove("/first/orphan"); err != nil {
			return reply{}, err
		}
	}
	if operation == "advance" {
		counter++
		if _, err := dataFile.WriteAt([]byte(fmt.Sprint(counter)), 0); err != nil {
			return reply{}, err
		}
		if _, err := orphan.WriteAt([]byte(fmt.Sprintf("orphan%d", counter)), 0); err != nil {
			return reply{}, err
		}
	}
	state := make([]byte, 2)
	if _, err := dataFile.ReadAt(state, 0); err != nil {
		return reply{}, err
	}
	lost := make([]byte, 8)
	if _, err := orphan.ReadAt(lost, 0); err != nil {
		return reply{}, err
	}
	shared, err := os.ReadFile("/second/link")
	if err != nil {
		return reply{}, err
	}
	var stat unix.Stat_t
	if err = unix.Fstat(int(dataFile.Fd()), &stat); err != nil {
		return reply{}, err
	}
	return reply{Counter: counter, State: string(state), Shared: string(shared), Orphan: string(lost), Inode: stat.Ino}, nil
}
