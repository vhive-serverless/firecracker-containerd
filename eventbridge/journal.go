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
	"errors"
	"fmt"
	"sync"
	"time"

	apitypes "github.com/containerd/containerd/api/types"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/protobuf"
	"github.com/containerd/typeurl/v2"
	"google.golang.org/protobuf/proto"
)

var ErrJournalClosed = errors.New("event journal closed")

// Journal retains events until the host acknowledges successful publication.
// It lives in guest memory, so a VM snapshot captures its records and cursor.
// Unlike a subscription channel, reading a record never consumes it.
type Journal struct {
	mu          sync.Mutex
	changed     chan struct{}
	closed      bool
	limit, used int
	ack         uint64
	records     []*apitypes.Envelope
}

func NewJournal(limitBytes int) *Journal {
	if limitBytes <= 0 {
		panic("event journal requires a positive byte limit")
	}
	return &Journal{limit: limitBytes, changed: make(chan struct{})}
}

func (j *Journal) notify() { close(j.changed); j.changed = make(chan struct{}) }

// Publish applies backpressure when unacknowledged records fill the journal.
// Successful return means an immutable record is retained, not yet delivered.
func (j *Journal) Publish(ctx context.Context, topic string, event events.Event) error {
	ns, err := namespaces.NamespaceRequired(ctx)
	if err != nil {
		return err
	}
	value, err := typeurl.MarshalAny(event)
	if err != nil {
		return err
	}
	record := &apitypes.Envelope{Timestamp: protobuf.ToTimestamp(time.Now()), Namespace: ns, Topic: topic, Event: typeurl.MarshalProto(value)}
	record = proto.Clone(record).(*apitypes.Envelope)
	size := proto.Size(record)
	if size > j.limit {
		return fmt.Errorf("event size %d exceeds journal capacity %d", size, j.limit)
	}
	for {
		j.mu.Lock()
		if err := ctx.Err(); err != nil {
			j.mu.Unlock()
			return err
		}
		if j.closed {
			j.mu.Unlock()
			return ErrJournalClosed
		}
		if size <= j.limit-j.used {
			j.records = append(j.records, record)
			j.used += size
			j.notify()
			j.mu.Unlock()
			return nil
		}
		changed := j.changed
		j.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// Next returns sequence after+1, or waits for it. The returned record is a copy.
// A caller reconnecting after a lost response repeats the same cursor.
func (j *Journal) Next(ctx context.Context, after uint64) (*apitypes.Envelope, error) {
	for {
		j.mu.Lock()
		if err := ctx.Err(); err != nil {
			j.mu.Unlock()
			return nil, err
		}
		if after < j.ack || after-j.ack > uint64(len(j.records)) {
			j.mu.Unlock()
			return nil, fmt.Errorf("event cursor %d outside retained journal", after)
		}
		index := after - j.ack
		if index < uint64(len(j.records)) {
			record := proto.Clone(j.records[index]).(*apitypes.Envelope)
			j.mu.Unlock()
			return record, nil
		}
		if j.closed {
			j.mu.Unlock()
			return nil, ErrJournalClosed
		}
		changed := j.changed
		j.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// Ack releases records only after successful publication to the host publisher.
// Retrying an acknowledgement whose response was lost is harmless.
func (j *Journal) Ack(sequence uint64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if sequence <= j.ack {
		return nil
	}
	count := sequence - j.ack
	if count > uint64(len(j.records)) {
		return fmt.Errorf("cannot acknowledge unpublished event sequence %d", sequence)
	}
	for _, record := range j.records[:count] {
		j.used -= proto.Size(record)
	}
	copy(j.records, j.records[count:])
	clear(j.records[len(j.records)-int(count):])
	j.records = j.records[:len(j.records)-int(count)]
	j.ack = sequence
	j.notify()
	return nil
}

// Cursor is the last acknowledged sequence, used when attaching a fresh clone.
func (j *Journal) Cursor() uint64 { j.mu.Lock(); defer j.mu.Unlock(); return j.ack }

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.closed {
		j.closed = true
		j.notify()
	}
	return nil
}
