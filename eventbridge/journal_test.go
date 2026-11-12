package eventbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	apievents "github.com/containerd/containerd/api/events"
	apitypes "github.com/containerd/containerd/api/types"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/typeurl/v2"
	"google.golang.org/protobuf/proto"
)

func TestJournalReplaysLostResponseUntilAcknowledged(t *testing.T) {
	ctx := namespaces.WithNamespace(context.Background(), "test")
	j := NewJournal(4096)
	original := &apievents.TaskExit{ContainerID: "task", ID: "task", ExitStatus: 17}
	if err := j.Publish(ctx, "/tasks/exit", original); err != nil {
		t.Fatal(err)
	}
	original.ExitStatus = 99
	first, err := j.Next(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a response lost after the guest fetched the event, then reconnect.
	again, err := j.Next(ctx, 0)
	if err != nil || !proto.Equal(first, again) {
		t.Fatalf("replay changed: %v", err)
	}
	var exit apievents.TaskExit
	if err := proto.Unmarshal(again.Event.Value, &exit); err != nil || exit.ExitStatus != 17 {
		t.Fatalf("mutable journal entry: %v %v", &exit, err)
	}
	first.Topic = "corrupted by caller"
	again, err = j.Next(ctx, 0)
	if err != nil || again.Topic != "/tasks/exit" {
		t.Fatalf("returned record aliases journal: %v", err)
	}
	if err := j.Ack(2); err == nil {
		t.Fatal("accepted future acknowledgement")
	}
	if err := j.Ack(1); err != nil {
		t.Fatal(err)
	}
	if err := j.Ack(1); err != nil {
		t.Fatal(err)
	} // lost ACK response
	if j.Cursor() != 1 {
		t.Fatal("acknowledged cursor lost")
	}
	if _, err := j.Next(ctx, 0); err == nil {
		t.Fatal("accepted expired cursor")
	}
	if err := j.Publish(ctx, "/tasks/exit", &apievents.TaskExit{ID: "second"}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Next(ctx, 1); err != nil {
		t.Fatal(err)
	}
}

type receiptFaultReader struct {
	journal *Journal
	failAck bool
}

func (r *receiptFaultReader) Next(ctx context.Context, after uint64) (*apitypes.Envelope, error) {
	return r.journal.Next(ctx, after)
}

func (r *receiptFaultReader) Ack(_ context.Context, sequence uint64) error {
	if err := r.journal.Ack(sequence); err != nil {
		return err
	}
	if sequence > 0 && r.failAck {
		r.failAck = false
		return errors.New("connection reset after ACK applied")
	}
	return nil
}

type recordingPublisher struct {
	ids  []string
	fail bool
}

func (p *recordingPublisher) Publish(_ context.Context, _ string, event events.Event) error {
	if p.fail {
		p.fail = false
		return errors.New("publisher unavailable")
	}
	p.ids = append(p.ids, event.(*apievents.TaskExit).ID)
	return nil
}

func TestJournalForwarderLostReceiptDoesNotRepublish(t *testing.T) {
	ctx := namespaces.WithNamespace(context.Background(), "test")
	j := NewJournal(4096)
	for _, id := range []string{"first", "second"} {
		if err := j.Publish(ctx, "/tasks/exit", &apievents.TaskExit{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	source := &receiptFaultReader{journal: j, failAck: true}
	destination := &recordingPublisher{}
	f := NewJournalForwarder(0)
	if err := f.Step(ctx, source, destination); err == nil {
		t.Fatal("fault not injected")
	}
	if f.Cursor() != 1 {
		t.Fatal("successful remote publication forgotten")
	}
	if err := f.Step(ctx, source, destination); err != nil {
		t.Fatal(err)
	}
	if len(destination.ids) != 2 || destination.ids[0] != "first" || destination.ids[1] != "second" {
		t.Fatalf("lost or duplicated publication: %v", destination.ids)
	}
	if j.Cursor() != 2 {
		t.Fatal("guest receipts not synchronized")
	}
}

func TestJournalForwarderPublisherFailureRetainsEvent(t *testing.T) {
	ctx := namespaces.WithNamespace(context.Background(), "test")
	j := NewJournal(4096)
	if err := j.Publish(ctx, "/tasks/exit", &apievents.TaskExit{ID: "retained"}); err != nil {
		t.Fatal(err)
	}
	source := &receiptFaultReader{journal: j}
	destination := &recordingPublisher{fail: true}
	f := NewJournalForwarder(0)
	if err := f.Step(ctx, source, destination); err == nil {
		t.Fatal("fault not injected")
	}
	if j.Cursor() != 0 || f.Cursor() != 0 {
		t.Fatal("failed publication acknowledged")
	}
	envelope, err := j.Next(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := typeurl.UnmarshalAny(envelope.Event); err != nil {
		t.Fatal(err)
	}
	if err := f.Step(ctx, source, destination); err != nil {
		t.Fatal(err)
	}
	if len(destination.ids) != 1 || destination.ids[0] != "retained" {
		t.Fatalf("retry lost event: %v", destination.ids)
	}
}

func TestJournalBackpressureAndCancellationPreserveRecords(t *testing.T) {
	ctx := namespaces.WithNamespace(context.Background(), "test")
	j := NewJournal(4096)
	if err := j.Publish(ctx, "/tasks/exit", &apievents.TaskExit{ID: "first"}); err != nil {
		t.Fatal(err)
	}
	// Exactly fill the budget without depending on a timestamp's encoded size.
	j.mu.Lock()
	j.limit = j.used
	j.mu.Unlock()
	timed, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := j.Publish(timed, "/tasks/exit", &apievents.TaskExit{ID: "next"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected backpressure: %v", err)
	}
	first, err := j.Next(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var exit apievents.TaskExit
	if err := proto.Unmarshal(first.Event.Value, &exit); err != nil || exit.ID != "first" {
		t.Fatal("backpressure lost first event")
	}
	if err := j.Ack(1); err != nil {
		t.Fatal(err)
	}
	if err := j.Publish(ctx, "/tasks/exit", &apievents.TaskExit{ID: "next"}); err != nil {
		t.Fatal(err)
	}
	if j.Cursor() != 1 {
		t.Fatal("cancelled publication advanced sequence")
	}
}

func TestJournalCloseUnblocksReaders(t *testing.T) {
	j := NewJournal(4096)
	done := make(chan error, 1)
	go func() { _, err := j.Next(context.Background(), 0); done <- err }()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrJournalClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader remained blocked")
	}
}
