package postgres

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSweepExpiredToolEffectValidation(t *testing.T) {
	candidate := ExpiredToolEffectCandidate{TenantID: "tenant", UserID: "user", StoreEpoch: "epoch", ToolCallID: "tool", RunID: "run", EffectID: "effect", CommandID: "command", AttemptID: "attempt", InboxID: "inbox", ConsumerName: "worker", RequestHash: "request", JobID: "job", EffectClass: "reconcilable_write", ProviderRequestID: "provider-request", ToolVersion: 2, EffectVersion: 2, AttemptVersion: 1, Fence: 1, LeaseExpiresAt: time.Now()}
	command := SweepExpiredToolEffectCommand{Candidate: candidate, ResultHash: "outcome-unknown", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", OutcomeUnknownEvent: PayloadPointer{Ref: "encrypted://unknown", Hash: "unknown"}, AttemptExpiredEvent: PayloadPointer{Ref: "encrypted://expired", Hash: "expired"}, Reconciliation: EffectCompletion{ReconciliationDueAt: time.Now().Add(time.Minute), ReconcileCommand: PayloadPointer{Ref: "encrypted://reconcile", Hash: "reconcile"}, ReconcileQueueClass: "background", ReconcileResource: "tool-reconciliation", ReconcilePriority: 10, ReconcileCostUnits: 1, ReconcileAttempts: 5}}
	if !validSweepExpiredToolEffect(command) {
		t.Fatal("valid sweep rejected")
	}
	command.Candidate.EffectClass = "idempotent_write"
	if validSweepExpiredToolEffect(command) {
		t.Fatal("replay-safe effect accepted by unknown sweeper")
	}
	for _, effectClass := range []string{"compensatable_write", "irreversible_write"} {
		command.Candidate.EffectClass = effectClass
		if validSweepExpiredToolEffect(command) {
			t.Fatalf("%s bypassed Repair boundary", effectClass)
		}
	}
}
