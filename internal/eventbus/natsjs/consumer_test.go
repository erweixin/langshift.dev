package natsjs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type fakeMessage struct {
	mu                     sync.Mutex
	body                   []byte
	subject                string
	acked, naked, terminal bool
	nakDelay               time.Duration
	progress               int
}

func (message *fakeMessage) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{}, nil
}
func (message *fakeMessage) Data() []byte         { return message.body }
func (message *fakeMessage) Headers() nats.Header { return nil }
func (message *fakeMessage) Subject() string      { return message.subject }
func (message *fakeMessage) Reply() string        { return "" }
func (message *fakeMessage) Ack() error           { return message.DoubleAck(context.Background()) }
func (message *fakeMessage) DoubleAck(context.Context) error {
	message.mu.Lock()
	defer message.mu.Unlock()
	message.acked = true
	return nil
}
func (message *fakeMessage) Nak() error { return message.NakWithDelay(0) }
func (message *fakeMessage) NakWithDelay(delay time.Duration) error {
	message.mu.Lock()
	defer message.mu.Unlock()
	message.naked, message.nakDelay = true, delay
	return nil
}
func (message *fakeMessage) InProgress() error {
	message.mu.Lock()
	defer message.mu.Unlock()
	message.progress++
	return nil
}
func (message *fakeMessage) Term() error { return message.TermWithReason("") }
func (message *fakeMessage) TermWithReason(string) error {
	message.mu.Lock()
	defer message.mu.Unlock()
	message.terminal = true
	return nil
}

func testMessage(t *testing.T) *fakeMessage {
	t.Helper()
	body, subject, err := Encode(testPublishedCommand())
	if err != nil {
		t.Fatal(err)
	}
	return &fakeMessage{body: body, subject: subject}
}

func testConsumer(handler Handler) Consumer {
	return Consumer{Handle: handler, HeartbeatInterval: 5 * time.Millisecond, BusyDelay: 20 * time.Millisecond, RetryDelay: 40 * time.Millisecond, AckTimeout: 100 * time.Millisecond}
}

func TestProcessAcknowledgesOnlyAfterHandlerCompletes(t *testing.T) {
	message := testMessage(t)
	handled := false
	consumer := testConsumer(func(_ context.Context, command eventpostgres.DeliveredCommand) error {
		time.Sleep(12 * time.Millisecond)
		handled = true
		if command.CommandID != testPublishedCommand().CommandID {
			t.Fatal("command identity changed")
		}
		return nil
	})
	consumer.process(context.Background(), message)
	if !handled || !message.acked || message.naked || message.terminal || message.progress == 0 {
		t.Fatalf("unexpected ack state: %#v", message)
	}
}

func TestProcessMapsBusyTransientAndConflict(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		nakDelay time.Duration
		terminal bool
	}{
		{name: "busy", err: eventpostgres.ErrDeliveryBusy, nakDelay: 20 * time.Millisecond},
		{name: "transient", err: errors.New("dependency unavailable"), nakDelay: 40 * time.Millisecond},
		{name: "worker epoch snapshot stale", err: eventpostgres.ErrWorkerStoreEpochStale, nakDelay: 40 * time.Millisecond},
		{name: "stale epoch", err: eventpostgres.ErrStaleStoreEpoch, terminal: true},
		{name: "payload conflict", err: eventpostgres.ErrDeliveryConflict, terminal: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := testMessage(t)
			consumer := testConsumer(func(context.Context, eventpostgres.DeliveredCommand) error { return test.err })
			consumer.process(context.Background(), message)
			if message.acked || message.terminal != test.terminal || message.nakDelay != test.nakDelay {
				t.Fatalf("unexpected delivery disposition: %#v", message)
			}
		})
	}
}

func TestProcessTerminatesInvalidEnvelopeWithoutCallingHandler(t *testing.T) {
	message := &fakeMessage{body: []byte(`{"version":1,"secret":"plaintext"}`), subject: "lites.commands.events.publish"}
	called := false
	consumer := testConsumer(func(context.Context, eventpostgres.DeliveredCommand) error { called = true; return nil })
	consumer.process(context.Background(), message)
	if called || !message.terminal || message.acked || message.naked {
		t.Fatalf("unexpected invalid-envelope disposition: %#v", message)
	}
}
