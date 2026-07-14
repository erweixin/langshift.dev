package postgres

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
)

func TestReconciliationClaimValidationAndIdentifiers(t *testing.T) {
	command := ClaimReconciliationCommand{Command: eventpostgres.DeliveredCommand{TenantID: "tenant", StoreEpoch: "epoch", CommandID: "command", CommandType: "ReconcileToolEffect", AggregateKind: "tool_call", AggregateID: "tool", PayloadRef: "encrypted://reconcile", PayloadHash: "reconcile"}, ConsumerName: "reconciliation-worker", WorkerID: "worker", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", AttemptStartedEvent: PayloadPointer{Ref: "encrypted://attempt-started", Hash: "attempt-started"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://attempt-expired", Hash: "attempt-expired"}}
	if !validClaimReconciliation(command) {
		t.Fatal("valid reconciliation command rejected")
	}
	invalid := command
	invalid.Command.CommandType = "ExecuteToolCall"
	if validClaimReconciliation(invalid) {
		t.Fatal("tool execution accepted as reconciliation")
	}
	claim := ReconciliationClaim{ToolCallID: "tool", RunID: "run", GroupID: "group", EffectID: "effect", TenantID: "tenant", UserID: "user", StoreEpoch: "epoch", ToolCallVersion: 3, EffectVersion: 3, EffectClass: "reconcilable_write", EffectScope: "scope", EffectKey: "key", ProviderID: "provider", ProviderRequestID: "provider-request", CommandID: "command", ConsumerName: "reconciliation-worker", RequestHash: "hash", JobID: "job", InboxID: "inbox", AttemptID: "attempt", Fence: 1, LeaseToken: "token", LeaseExpiresAt: time.Now().Add(time.Minute)}
	if !validReconciliationClaim(claim) {
		t.Fatal("valid reconciliation claim rejected")
	}
	store := RunStore{IDKey: bytes.Repeat([]byte{0xb7}, 32)}
	first, err := store.reconciliationClaimEventIdentifiers("attempt")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.reconciliationClaimEventIdentifiers("attempt")
	if err != nil || first != second || first.event == first.outbox || first.event == first.publish || first.outbox == first.publish {
		t.Fatalf("unstable reconciliation claim identifiers first=%#v second=%#v err=%v", first, second, err)
	}
	complete := CompleteReconciliationCommand{Claim: claim, ExpectedToolVersion: claim.ToolCallVersion, ExpectedEffectVersion: claim.EffectVersion, TargetState: statemachine.ToolCallSucceeded, ResultHash: "result", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", ToolCompletedEvent: PayloadPointer{Ref: "encrypted://tool-completed", Hash: "tool-completed"}, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://attempt-completed", Hash: "attempt-completed"}, GroupJoinedEvent: PayloadPointer{Ref: "encrypted://group-joined", Hash: "group-joined"}, RunResumeQueuedEvent: PayloadPointer{Ref: "encrypted://resume-queued", Hash: "resume-queued"}, ResumeCommand: PayloadPointer{Ref: "encrypted://resume", Hash: "resume"}, ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 1, ResumeMaxAttempts: 5}
	if !validCompleteReconciliation(complete) {
		t.Fatal("valid reconciliation completion rejected")
	}
	invalidComplete := complete
	invalidComplete.TargetState = statemachine.ToolCallFailed
	invalidComplete.ExternalResourceRef = "provider://contradiction"
	if validCompleteReconciliation(invalidComplete) {
		t.Fatal("failed reconciliation accepted an external resource")
	}
	completeFirst, err := store.reconciliationCompletionIdentifiers("attempt", statemachine.ToolCallSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	completeSecond, err := store.reconciliationCompletionIdentifiers("attempt", statemachine.ToolCallSucceeded)
	if err != nil || completeFirst != completeSecond || completeFirst.event == completeFirst.outbox || completeFirst.event == completeFirst.publish || completeFirst.outbox == completeFirst.publish {
		t.Fatalf("unstable completion identifiers first=%#v second=%#v err=%v", completeFirst, completeSecond, err)
	}
}
