package eventbridge

import (
	"context"
	"sync"

	apitypes "github.com/containerd/containerd/api/types"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/pkg/protobuf/types"
	"github.com/containerd/ttrpc"
	"github.com/containerd/typeurl/v2"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const journalServiceName = "aws.firecracker.containerd.eventbridge.journal.v1"

// RegisterJournalService exposes non-destructive reads and explicit receipts.
// The wire sequence of Next(after) is always after+1; no sequence is skipped.
func RegisterJournalService(server *ttrpc.Server, journal *Journal) {
	server.Register(journalServiceName, map[string]ttrpc.Method{
		"Next": func(ctx context.Context, unmarshal func(any) error) (any, error) {
			var request wrapperspb.UInt64Value
			if err := unmarshal(&request); err != nil {
				return nil, err
			}
			return journal.Next(ctx, request.Value)
		},
		"Ack": func(_ context.Context, unmarshal func(any) error) (any, error) {
			var request wrapperspb.UInt64Value
			if err := unmarshal(&request); err != nil {
				return nil, err
			}
			return &types.Empty{}, journal.Ack(request.Value)
		},
		"Cursor": func(_ context.Context, unmarshal func(any) error) (any, error) {
			var request types.Empty
			if err := unmarshal(&request); err != nil {
				return nil, err
			}
			return wrapperspb.UInt64(journal.Cursor()), nil
		},
	})
}

// JournalClient resolves a live RPC client for every operation. The caller owns
// reconnect coordination; failed calls leave the journal cursor unchanged.
type JournalClient struct {
	Client func(context.Context) (*ttrpc.Client, error)
}

func (c JournalClient) Next(ctx context.Context, after uint64) (*apitypes.Envelope, error) {
	client, err := c.Client(ctx)
	if err != nil {
		return nil, err
	}
	var response apitypes.Envelope
	if err := client.Call(ctx, journalServiceName, "Next", wrapperspb.UInt64(after), &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c JournalClient) Ack(ctx context.Context, sequence uint64) error {
	client, err := c.Client(ctx)
	if err != nil {
		return err
	}
	return client.Call(ctx, journalServiceName, "Ack", wrapperspb.UInt64(sequence), &types.Empty{})
}

func (c JournalClient) Cursor(ctx context.Context) (uint64, error) {
	client, err := c.Client(ctx)
	if err != nil {
		return 0, err
	}
	var response wrapperspb.UInt64Value
	err = client.Call(ctx, journalServiceName, "Cursor", &types.Empty{}, &response)
	return response.Value, err
}

type JournalReader interface {
	Next(context.Context, uint64) (*apitypes.Envelope, error)
	Ack(context.Context, uint64) error
}

// JournalForwarder retains the host publication cursor across reconnects.
// It acknowledges the guest only after the real publisher returns success,
// never merely after insertion into an intermediate local exchange.
// This covers transport resets with a live host; it is not a host-crash log.
type JournalForwarder struct {
	mu     sync.Mutex
	cursor uint64
}

func NewJournalForwarder(initialCursor uint64) *JournalForwarder {
	return &JournalForwarder{cursor: initialCursor}
}

func (f *JournalForwarder) Cursor() uint64 { f.mu.Lock(); defer f.mu.Unlock(); return f.cursor }

// Step forwards one event. Cancel its context to stop a long poll before sealing
// a snapshot cut; then acknowledge Cursor on the guest as part of the barrier.
func (f *JournalForwarder) Step(ctx context.Context, source JournalReader, destination events.Publisher) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// A prior publication may have succeeded while its ACK response was lost.
	if err := source.Ack(ctx, f.cursor); err != nil {
		return err
	}
	envelope, err := source.Next(ctx, f.cursor)
	if err != nil {
		return err
	}
	decoded, err := typeurl.UnmarshalAny(envelope.Event)
	if err != nil {
		return err
	}
	if err := destination.Publish(ctx, envelope.Topic, decoded); err != nil {
		return err
	}
	f.cursor++
	return source.Ack(ctx, f.cursor)
}
