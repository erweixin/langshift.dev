package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCommandRedeliveryRequestValidationAndBackoff(t *testing.T) {
	request := RequestCommandRedelivery{TenantID: "tenant", StoreEpoch: "epoch", CommandID: "command", ExpectedPayloadHash: "hash", ExpectedQueueGeneration: 1, ReasonCode: RedeliveryReasonClaimSLOElapsed, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", RequestedEvent: PayloadPointer{Ref: "encrypted://redelivery", Hash: "redelivery"}}
	if !validRedeliveryRequest(request) {
		t.Fatal("valid request rejected")
	}
	invalid := request
	invalid.ExpectedQueueGeneration = 0
	if validRedeliveryRequest(invalid) {
		t.Fatal("generation-less request accepted")
	}
	invalid = request
	invalid.ReasonCode = "operator_guess"
	if validRedeliveryRequest(invalid) {
		t.Fatal("unbounded reason accepted")
	}
	store := CommandReconcilerStore{IDKey: bytes.Repeat([]byte{1}, 32), ClaimSLO: time.Minute, BackoffBase: time.Second, BackoffLimit: 4 * time.Second}
	if got := store.backoff(1); got != time.Second {
		t.Fatalf("first backoff=%s", got)
	}
	if got := store.backoff(4); got != 4*time.Second {
		t.Fatalf("capped backoff=%s", got)
	}
	first, _, _, err := store.redeliveryEventIDs("job", 2)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := store.redeliveryEventIDs("job", 2)
	if err != nil || first != second {
		t.Fatalf("deterministic event ids first=%q second=%q err=%v", first, second, err)
	}
}

func TestCommandRedeliveryStateRejectsLiveOwnerAndUnsafeEffect(t *testing.T) {
	now := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
	published := now.Add(-2 * time.Minute)
	due := now.Add(-time.Minute)
	command, attempt := "command", "attempt"
	request := RequestCommandRedelivery{CommandID: command, ExpectedQueueGeneration: 1, ReasonCode: RedeliveryReasonClaimSLOElapsed}
	outbox := redeliveryOutbox{commandType: "ExecuteToolCall", status: "published", publishedAt: &published}
	job := redeliveryJob{status: "running", commandID: command, queueGeneration: 1, maxAttempts: 5, deliveryDueAt: &due}
	target := redeliveryTarget{status: "executing", activeCommandID: &command, activeAttemptID: &attempt, leaseExpiresAt: &due, effectClass: "reconcilable_write"}
	inbox := redeliveryInbox{attemptID: attempt, leaseExpiresAt: due}
	if err := validateRedeliveryState(outbox, target, job, inbox, true, request, now, time.Minute); !errors.Is(err, ErrCommandNeedsRepair) {
		t.Fatalf("unsafe effect error=%v", err)
	}
	live := now.Add(time.Minute)
	inbox.leaseExpiresAt = live
	target.leaseExpiresAt = &live
	target.effectClass = "idempotent_write"
	if err := validateRedeliveryState(outbox, target, job, inbox, true, request, now, time.Minute); !errors.Is(err, ErrCommandNotRedeliverable) {
		t.Fatalf("live target error=%v", err)
	}
}
