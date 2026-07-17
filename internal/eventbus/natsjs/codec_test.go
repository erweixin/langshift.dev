package natsjs

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/nats-io/nats.go/jetstream"
)

func testPublishedCommand() eventpostgres.PublishedCommand {
	return eventpostgres.PublishedCommand{OutboxID: "10000000-0000-0000-0000-000000000001", TenantID: "10000000-0000-0000-0000-000000000002", StoreEpoch: "10000000-0000-0000-0000-000000000003", CommandID: "10000000-0000-0000-0000-000000000004", CommandType: "identity.invitation_import.process", AggregateKind: "invitation_import", AggregateID: "10000000-0000-0000-0000-000000000005", PayloadRef: "tenant/command/ciphertext", PayloadHash: "0123456789abcdef"}
}

func TestCodecRoundTripPreservesDeliveryIdentity(t *testing.T) {
	published := testPublishedCommand()
	body, subject, err := Encode(published)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(published.OutboxID)) || bytes.Contains(body, []byte("lease")) {
		t.Fatal("transport envelope leaked internal outbox state")
	}
	delivered, err := Decode(body, subject)
	if err != nil {
		t.Fatal(err)
	}
	if delivered != published.Delivered() {
		t.Fatalf("delivery identity changed: %#v", delivered)
	}
}

func TestDecodeRejectsUnknownFieldsTrailingDataAndSubjectConfusion(t *testing.T) {
	published := testPublishedCommand()
	body, subject, err := Encode(published)
	if err != nil {
		t.Fatal(err)
	}
	cases := [][]byte{
		append(append([]byte(nil), body...), []byte(` {}`)...),
		bytes.Replace(body, []byte(`"version":1`), []byte(`"version":1,"secret":"no"`), 1),
	}
	for _, candidate := range cases {
		if _, err = Decode(candidate, subject); !errors.Is(err, ErrEnvelope) {
			t.Fatalf("expected envelope rejection, got %v", err)
		}
	}
	if _, err = Decode(body, "lites.commands.events.publish"); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("expected subject binding rejection, got %v", err)
	}
}

type fakePublisher struct {
	subject string
	body    []byte
	ack     *jetstream.PubAck
	err     error
}

func (publisher *fakePublisher) Publish(_ context.Context, subject string, body []byte, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	publisher.subject = subject
	publisher.body = append([]byte(nil), body...)
	return publisher.ack, publisher.err
}

func TestBrokerRequiresMatchingDurableAck(t *testing.T) {
	publisher := &fakePublisher{ack: &jetstream.PubAck{Stream: "LITES_COMMANDS", Sequence: 4}}
	broker := Broker{Publisher: publisher, Stream: "LITES_COMMANDS", RetryWait: 10 * time.Millisecond, RetryAttempts: 2}
	if err := broker.Publish(context.Background(), testPublishedCommand()); err != nil {
		t.Fatal(err)
	}
	if publisher.subject != "lites.commands.identity.invitation-import.process" {
		t.Fatalf("unexpected subject %q", publisher.subject)
	}
	if _, err := Decode(publisher.body, publisher.subject); err != nil {
		t.Fatal(err)
	}
	publisher.ack.Stream = "OTHER"
	if err := broker.Publish(context.Background(), testPublishedCommand()); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("expected ack stream rejection, got %v", err)
	}
}

func TestDispatchEnvelopeBindsBothSchedulerFences(t *testing.T) {
	command := testPublishedCommand()
	command.CommandType = "StartAgentRun"
	command.AggregateKind = "run"
	command.QueueGeneration = 7
	command.DispatchVersion = 11
	body, subject, err := EncodeDispatch(command)
	if err != nil {
		t.Fatal(err)
	}
	if subject != "lites.commands.dispatch.start-agent-run" {
		t.Fatalf("unexpected dispatch subject %q", subject)
	}
	delivered, err := DecodeDispatch(body, subject)
	if err != nil || delivered != command.Delivered() {
		t.Fatalf("dispatch=%#v error=%v", delivered, err)
	}
	if _, err = Decode(body, subject); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("direct decoder accepted dispatch envelope: %v", err)
	}
	command.QueueGeneration = 0
	if _, _, err = EncodeDispatch(command); !errors.Is(err, ErrEnvelope) {
		t.Fatalf("zero queue generation accepted: %v", err)
	}
}

func TestDispatchBrokerUsesWorkerVisibleSubject(t *testing.T) {
	command := testPublishedCommand()
	command.CommandType = "ExecuteToolCall"
	command.AggregateKind = "tool_call"
	command.QueueGeneration = 2
	command.DispatchVersion = 3
	publisher := &fakePublisher{ack: &jetstream.PubAck{Stream: "LITES_COMMANDS", Sequence: 9}}
	broker := DispatchBroker{Publisher: publisher, Stream: "LITES_COMMANDS", RetryWait: time.Millisecond, RetryAttempts: 1}
	if err := broker.Publish(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if publisher.subject != "lites.commands.dispatch.execute-tool-call" {
		t.Fatalf("unexpected subject %q", publisher.subject)
	}
	if _, err := DecodeDispatch(publisher.body, publisher.subject); err != nil {
		t.Fatal(err)
	}
}

func TestProductCommandsHaveStableDistinctSubjects(t *testing.T) {
	expected := map[string]string{
		"DeliverReminder":        "lites.commands.product.deliver-reminder",
		"GenerateMissionRoute":   "lites.commands.product.generate-mission-route",
		"RoutePlanningRequested": "lites.commands.product.route-planning-requested",
		"GenerateDailyTask":      "lites.commands.product.generate-daily-task",
	}
	seen := map[string]struct{}{}
	for commandType, want := range expected {
		subject, err := SubjectFor(commandType)
		if err != nil || subject != want {
			t.Fatalf("SubjectFor(%q) = %q, %v", commandType, subject, err)
		}
		if _, duplicate := seen[subject]; duplicate {
			t.Fatalf("duplicate product command subject %q", subject)
		}
		seen[subject] = struct{}{}
	}
}
