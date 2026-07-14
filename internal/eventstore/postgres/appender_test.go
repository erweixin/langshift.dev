package postgres

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestAppenderRejectsIncompleteOrNonObjectEvents(t *testing.T) {
	input := Input{Event: Event{ID: "e", TenantID: "t", UserID: "u", EventType: "Event", SchemaVersion: 1, AggregateKind: "user", AggregateID: "a", AggregateVersion: 1, StoreEpoch: "s", OccurredAt: time.Now(), Actor: json.RawMessage(`[]`), CorrelationID: "c", PayloadRef: "encrypted://event", PayloadHash: "hash"}, Commands: []OutboxCommand{{ID: "o", CommandID: "c", CommandType: "publish", PayloadRef: "encrypted://command", PayloadHash: "hash"}}}
	if _, err := (Appender{}).Append(t.Context(), nil, input); !errors.Is(err, ErrInvalidAppend) {
		t.Fatalf("error=%v", err)
	}
	input.Event.Actor = json.RawMessage(`{"kind":"user"}`)
	input.Commands[0].CommandID = ""
	if validInput(input) {
		t.Fatal("missing command id accepted")
	}
	input.Commands = []OutboxCommand{{ID: "same", CommandID: "one", CommandType: "publish", PayloadRef: "encrypted://one", PayloadHash: "one"}, {ID: "same", CommandID: "two", CommandType: "publish", PayloadRef: "encrypted://two", PayloadHash: "two"}}
	if validInput(input) {
		t.Fatal("duplicate outbox id accepted")
	}
	input.Commands = []OutboxCommand{{ID: "o", CommandID: "c", CommandType: "publish", TargetAggregateKind: "tool_call", PayloadRef: "encrypted://command", PayloadHash: "hash"}}
	if validInput(input) {
		t.Fatal("partial target aggregate accepted")
	}
	input.Commands[0].TargetAggregateID = "tool"
	if !validInput(input) {
		t.Fatal("explicit target aggregate rejected")
	}
	kind, id := commandTarget(input.Event, input.Commands[0])
	if kind != "tool_call" || id != "tool" {
		t.Fatalf("target=%s/%s", kind, id)
	}
}
