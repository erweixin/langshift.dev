package postgres

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

func TestToolClaimValidationAndIdentifiers(t *testing.T) {
	command := ClaimToolCommand{
		Command:             eventpostgres.DeliveredCommand{TenantID: "tenant", StoreEpoch: "epoch", CommandID: "command", CommandType: "ExecuteToolCall", AggregateKind: "tool_call", AggregateID: "tool", PayloadRef: "encrypted://command", PayloadHash: "command-hash"},
		ConsumerName:        "tool-worker",
		WorkerID:            "worker-one",
		Actor:               json.RawMessage(`{"kind":"service"}`),
		CorrelationID:       "correlation",
		ToolStartedEvent:    PayloadPointer{Ref: "encrypted://tool-started", Hash: "tool-started"},
		AttemptStartedEvent: PayloadPointer{Ref: "encrypted://attempt-started", Hash: "attempt-started"},
		AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://attempt-expired", Hash: "attempt-expired"},
	}
	if !validClaimTool(command) {
		t.Fatal("valid tool claim rejected")
	}
	invalid := command
	invalid.Command.CommandType = "StartAgentRun"
	if validClaimTool(invalid) {
		t.Fatal("run command accepted as tool claim")
	}
	claim := ToolClaim{ToolCallID: "tool", RunID: "run", GroupID: "group", TenantID: "tenant", UserID: "user", StoreEpoch: "epoch", ToolCallVersion: 2, CommandID: "command", ConsumerName: "tool-worker", RequestHash: "hash", JobID: "job", InboxID: "inbox", AttemptID: "attempt", Fence: 1, LeaseToken: "token", LeaseExpiresAt: time.Now().Add(time.Minute)}
	if !validToolClaim(claim) {
		t.Fatal("valid durable tool claim rejected")
	}
	store := RunStore{IDKey: bytes.Repeat([]byte{0xa1}, 32)}
	first, err := store.toolClaimEventIdentifiers("attempt")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.toolClaimEventIdentifiers("attempt")
	if err != nil || first != second {
		t.Fatalf("unstable ids first=%#v second=%#v err=%v", first, second, err)
	}
	seen := map[string]bool{}
	for _, value := range []string{first.toolEvent, first.toolOutbox, first.toolPublish, first.attemptEvent, first.attemptOutbox, first.attemptPublish} {
		if seen[value] {
			t.Fatalf("identifier collision: %s", value)
		}
		seen[value] = true
	}
}
