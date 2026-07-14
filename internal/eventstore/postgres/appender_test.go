package postgres

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestAppenderRejectsIncompleteOrNonObjectEvents(t *testing.T) {
	input := Input{Event: Event{ID: "e", TenantID: "t", UserID: "u", EventType: "Event", SchemaVersion: 1, AggregateKind: "user", AggregateID: "a", AggregateVersion: 1, StoreEpoch: "s", OccurredAt: time.Now(), Actor: json.RawMessage(`[]`), CorrelationID: "c", PayloadRef: "encrypted://event", PayloadHash: "hash"}, Command: OutboxCommand{ID: "o", CommandID: "c", CommandType: "publish", PayloadRef: "encrypted://command", PayloadHash: "hash"}}
	if _, err := (Appender{}).Append(t.Context(), nil, input); !errors.Is(err, ErrInvalidAppend) {
		t.Fatalf("error=%v", err)
	}
	input.Event.Actor = json.RawMessage(`{"kind":"user"}`)
	input.Command.CommandID = ""
	if validInput(input) {
		t.Fatal("missing command id accepted")
	}
}
