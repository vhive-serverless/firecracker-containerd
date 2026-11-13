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

package eventbridge

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	apievents "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/ttrpc"
	"google.golang.org/protobuf/proto"
)

func TestJournalRPCReconnectReplaysUnacknowledgedRecord(t *testing.T) {
	ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), "test"), 5*time.Second)
	defer cancel()
	journal := NewJournal(4096)
	if err := journal.Publish(ctx, "/tasks/exit", &apievents.TaskExit{ID: "exit-during-reset", ExitStatus: 23}); err != nil {
		t.Fatal(err)
	}
	server, err := ttrpc.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	RegisterJournalService(server, journal)
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer server.Close()
	go func() { _ = server.Serve(ctx, listener) }()
	dial := func() *ttrpc.Client {
		connection, err := net.Dial("unix", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		return ttrpc.NewClient(connection)
	}
	connection := dial()
	client := JournalClient{Client: func(context.Context) (*ttrpc.Client, error) { return connection, nil }}
	first, err := client.Next(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	connection = dial()
	defer connection.Close()
	replay, err := client.Next(ctx, 0)
	if err != nil || !proto.Equal(first, replay) {
		t.Fatalf("unacknowledged record lost across connection replacement: %v", err)
	}
	if err := client.Ack(ctx, 1); err != nil {
		t.Fatal(err)
	}
	cursor, err := client.Cursor(ctx)
	if err != nil || cursor != 1 {
		t.Fatalf("wrong clone initialization cursor %d: %v", cursor, err)
	}
	if err := client.Ack(ctx, 1); err != nil {
		t.Fatal(err)
	}
}
