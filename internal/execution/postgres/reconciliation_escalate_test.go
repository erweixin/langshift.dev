package postgres

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEscalateReconciliationValidation(t *testing.T) {
	claim := ReconciliationClaim{ToolCallID: "tool", RunID: "run", GroupID: "group", EffectID: "effect", TenantID: "tenant", UserID: "user", StoreEpoch: "epoch", ToolCallVersion: 3, EffectVersion: 4, EffectClass: "reconcilable_write", EffectScope: "scope", EffectKey: "key", ProviderID: "provider", ProviderRequestID: "provider-request", CommandID: "command", ConsumerName: "reconciliation-worker", RequestHash: "hash", JobID: "job", InboxID: "inbox", AttemptID: "attempt", Fence: 1, LeaseToken: "token", LeaseExpiresAt: time.Now().Add(time.Minute), ReconciliationDueAt: time.Now()}
	command := EscalateReconciliationCommand{Claim: claim, ExpectedEffectVersion: claim.EffectVersion, ResultHash: "manual-review-required", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://manual-review", Hash: "manual-review"}}
	if !validEscalateReconciliation(command) {
		t.Fatal("valid escalation rejected")
	}
	command.ExpectedEffectVersion++
	if validEscalateReconciliation(command) {
		t.Fatal("mismatched effect version accepted")
	}
	command.ExpectedEffectVersion = command.Claim.EffectVersion
	command.CorrelationID = ""
	if validEscalateReconciliation(command) {
		t.Fatal("empty correlation id accepted")
	}
}

func TestReconciliationRetryCommandIDIsStableAndVersioned(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	first, err := ReconcileToolEffectRetryCommandID(key, "effect", 4)
	if err != nil {
		t.Fatal(err)
	}
	replay, _ := ReconcileToolEffectRetryCommandID(key, "effect", 4)
	next, _ := ReconcileToolEffectRetryCommandID(key, "effect", 5)
	if first == "" || first != replay || first == next {
		t.Fatalf("ids first=%q replay=%q next=%q", first, replay, next)
	}
}
