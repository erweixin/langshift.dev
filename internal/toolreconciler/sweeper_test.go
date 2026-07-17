package toolreconciler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/payload"
)

type storeStub struct {
	tenants    []string
	candidates map[string][]executionpostgres.ExpiredToolEffectCandidate
	commands   []executionpostgres.SweepExpiredToolEffectCommand
}

func (store *storeStub) ListExpiredEffectTenantIDs(_ context.Context, _ string, after string, limit, shardIndex, _ int) ([]string, error) {
	if shardIndex != 0 {
		return nil, nil
	}
	result := make([]string, 0, limit)
	for _, tenantID := range store.tenants {
		if tenantID > after && len(result) < limit {
			result = append(result, tenantID)
		}
	}
	return result, nil
}

func (store *storeStub) ListExpiredEffectCandidates(_ context.Context, tenantID, _ string, after string, limit int) ([]executionpostgres.ExpiredToolEffectCandidate, error) {
	result := make([]executionpostgres.ExpiredToolEffectCandidate, 0, limit)
	for _, candidate := range store.candidates[tenantID] {
		if candidate.ToolCallID > after && len(result) < limit {
			result = append(result, candidate)
		}
	}
	return result, nil
}

func (store *storeStub) SweepExpiredToolEffect(_ context.Context, command executionpostgres.SweepExpiredToolEffectCommand) (executionpostgres.SweptToolEffect, error) {
	store.commands = append(store.commands, command)
	return executionpostgres.SweptToolEffect{ToolCallID: command.Candidate.ToolCallID}, nil
}

type capturedPayload struct {
	descriptor payload.Descriptor
	value      []byte
}

type payloadStub struct {
	values []capturedPayload
	source []byte
}

func (store *payloadStub) Put(_ context.Context, descriptor payload.Descriptor, value []byte) (payload.Manifest, error) {
	copyValue := append([]byte(nil), value...)
	store.values = append(store.values, capturedPayload{descriptor: descriptor, value: copyValue})
	hash := sha256.Sum256(append([]byte(descriptor.ObjectID+"\x00"), value...))
	encoded := hex.EncodeToString(hash[:])
	return payload.Manifest{Ref: "encrypted://" + descriptor.ObjectID + "/" + encoded, Hash: encoded}, nil
}

func (store *payloadStub) Get(context.Context, payload.Descriptor, payload.Manifest) ([]byte, error) {
	return append([]byte(nil), store.source...), nil
}

type lockStub struct {
	contendedTenant string
	contendedShard  int
}

func (locker lockStub) WithTenantLock(ctx context.Context, tenantID string, operation func(context.Context) error) (bool, error) {
	if tenantID == locker.contendedTenant {
		return false, nil
	}
	return true, operation(ctx)
}

func (locker lockStub) WithShardLock(ctx context.Context, index, _ int, operation func(context.Context) error) (bool, error) {
	if index == locker.contendedShard {
		return false, nil
	}
	return true, operation(ctx)
}

func TestSweeperProducesStableEncryptedEvidenceAndReconciliationCommand(t *testing.T) {
	at := time.Date(2026, 7, 16, 8, 0, 0, 123456000, time.UTC)
	candidate := testCandidate(at, "00000000-0000-4000-8000-000000000101", automaticallyReconcilableEffectClass)
	firstStore, firstPayloads := &storeStub{tenants: []string{candidate.TenantID}, candidates: map[string][]executionpostgres.ExpiredToolEffectCandidate{candidate.TenantID: {candidate}}}, payloadFor(candidate)
	result, err := testSweeper(at, firstStore, firstPayloads, lockStub{contendedShard: -1}).RunOnce(context.Background())
	if err != nil || result != (Result{Tenants: 1, Swept: 1}) || len(firstStore.commands) != 1 || len(firstPayloads.values) != 3 {
		t.Fatalf("RunOnce()=%#v err=%v commands=%d payloads=%d", result, err, len(firstStore.commands), len(firstPayloads.values))
	}
	command := firstStore.commands[0]
	if command.Candidate != candidate || command.CorrelationID != "00000000-0000-4000-8000-000000000009" || !bytes.Equal(command.Actor, []byte(`{"kind":"system","component":"tool-effect-sweeper"}`)) || !command.Reconciliation.ReconciliationDueAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("sweep command=%#v", command)
	}
	expectedCommandID, idErr := executionpostgres.ReconcileToolEffectCommandID(testIDKey(), candidate.EffectID, candidate.ToolVersion+1)
	if idErr != nil || firstPayloads.values[2].descriptor.ObjectID != expectedCommandID || firstPayloads.values[2].descriptor.Class != "tool-reconciliation-command" {
		t.Fatalf("reconcile descriptor=%#v expected=%s err=%v", firstPayloads.values[2].descriptor, expectedCommandID, idErr)
	}
	var reconcile CommandPayload
	if err = json.Unmarshal(firstPayloads.values[2].value, &reconcile); err != nil || reconcile.ToolName != candidate.ToolName || reconcile.DescriptorSnapshotID != candidate.DescriptorSnapshotID || reconcile.ProviderRequestID != candidate.ProviderRequestID || reconcile.TerminalToolVersion != candidate.ToolVersion+1 || reconcile.Input.Ref != "encrypted://normalized-input" {
		t.Fatalf("reconcile payload=%#v err=%v", reconcile, err)
	}

	secondStore, secondPayloads := &storeStub{tenants: []string{candidate.TenantID}, candidates: map[string][]executionpostgres.ExpiredToolEffectCandidate{candidate.TenantID: {candidate}}}, payloadFor(candidate)
	if _, err = testSweeper(at, secondStore, secondPayloads, lockStub{contendedShard: -1}).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for index := range firstPayloads.values {
		if firstPayloads.values[index].descriptor != secondPayloads.values[index].descriptor || !bytes.Equal(firstPayloads.values[index].value, secondPayloads.values[index].value) {
			t.Fatalf("payload %d was not stable", index)
		}
	}
}

func TestSweeperTransitionsManualEffectClassesWithoutAutomaticCommand(t *testing.T) {
	at := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	classes := []string{"idempotent_write", "compensatable_write", "irreversible_write"}
	for index, effectClass := range classes {
		candidate := testCandidate(at, fmt.Sprintf("00000000-0000-4000-8000-%012d", 101+index), effectClass)
		store, payloads := &storeStub{tenants: []string{candidate.TenantID}, candidates: map[string][]executionpostgres.ExpiredToolEffectCandidate{candidate.TenantID: {candidate}}}, payloadFor(candidate)
		result, err := testSweeper(at, store, payloads, lockStub{contendedShard: -1}).RunOnce(context.Background())
		if err != nil || result != (Result{Tenants: 1, ManualReviewRequired: 1}) || len(store.commands) != 1 || len(payloads.values) != 2 || store.commands[0].Reconciliation.ReconcileCommand != (executionpostgres.PayloadPointer{}) {
			t.Fatalf("class=%s RunOnce()=%#v err=%v commands=%#v payloads=%d", effectClass, result, err, store.commands, len(payloads.values))
		}
		var evidence outcomeUnknownEvidence
		if err = json.Unmarshal(payloads.values[0].value, &evidence); err != nil || !evidence.ManualReviewRequired {
			t.Fatalf("class=%s evidence=%#v err=%v", effectClass, evidence, err)
		}
	}
}

func TestSweeperSkipsTenantOwnedByAnotherReplica(t *testing.T) {
	at := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	candidate := testCandidate(at, "00000000-0000-4000-8000-000000000101", automaticallyReconcilableEffectClass)
	store, payloads := &storeStub{tenants: []string{candidate.TenantID}, candidates: map[string][]executionpostgres.ExpiredToolEffectCandidate{candidate.TenantID: {candidate}}}, payloadFor(candidate)
	result, err := testSweeper(at, store, payloads, lockStub{contendedTenant: candidate.TenantID, contendedShard: -1}).RunOnce(context.Background())
	if err != nil || result != (Result{Contended: 1}) || len(store.commands) != 0 || len(payloads.values) != 0 {
		t.Fatalf("RunOnce()=%#v err=%v", result, err)
	}
}

func TestCoordinatorUsesShardLocks(t *testing.T) {
	at := time.Date(2026, 7, 16, 11, 0, 0, 0, time.UTC)
	candidate := testCandidate(at, "00000000-0000-4000-8000-000000000101", automaticallyReconcilableEffectClass)
	store, payloads := &storeStub{tenants: []string{candidate.TenantID}, candidates: map[string][]executionpostgres.ExpiredToolEffectCandidate{candidate.TenantID: {candidate}}}, payloadFor(candidate)
	base := testSweeper(at, store, payloads, lockStub{contendedShard: 1})
	result, err := (Coordinator{Store: base.Store, Payloads: base.Payloads, Locker: lockStub{contendedShard: 1}, IDKey: base.IDKey, StoreEpoch: base.StoreEpoch, Now: base.Now, ReconcileDelay: base.ReconcileDelay, Reconcile: base.Reconcile, MaximumCommand: base.MaximumCommand, ShardCount: 2, TenantPage: base.TenantPage, EffectPage: base.EffectPage}).RunOnce(context.Background())
	if err != nil || result.Shards != 1 || result.Contended != 1 || result.Sweep.Swept != 1 || len(store.commands) != 1 {
		t.Fatalf("Coordinator.RunOnce()=%#v err=%v commands=%d", result, err, len(store.commands))
	}
}

func TestPostgresLockerRejectsInvalidBoundaries(t *testing.T) {
	locker := PostgresLocker{}
	if locked, err := locker.WithTenantLock(context.Background(), "", func(context.Context) error { return nil }); err != ErrConfiguration || locked {
		t.Fatalf("empty tenant lock=(%v,%v)", locked, err)
	}
	if locked, err := locker.WithShardLock(context.Background(), 2, 2, func(context.Context) error { return nil }); err != ErrConfiguration || locked {
		t.Fatalf("invalid shard lock=(%v,%v)", locked, err)
	}
}

func testSweeper(at time.Time, store Store, payloads payload.Store, locker TenantLocker) Sweeper {
	return Sweeper{Store: store, Payloads: payloads, Locker: locker, IDKey: testIDKey(), StoreEpoch: "00000000-0000-4000-8000-000000000010", Now: func() time.Time { return at }, ReconcileDelay: time.Minute, Reconcile: Schedule{QueueClass: "background", ResourceClass: "tool-reconciliation", Priority: 40, CostUnits: 1, MaxAttempts: 20}, MaximumCommand: 1 << 20, ShardCount: 1, TenantPage: 100, EffectPage: 100}
}

func testCandidate(at time.Time, toolCallID, effectClass string) executionpostgres.ExpiredToolEffectCandidate {
	return executionpostgres.ExpiredToolEffectCandidate{
		TenantID: "00000000-0000-4000-8000-000000000001", UserID: "00000000-0000-4000-8000-000000000002", StoreEpoch: "00000000-0000-4000-8000-000000000010",
		ToolCallID: toolCallID, RunID: "00000000-0000-4000-8000-000000000003", EffectID: "00000000-0000-4000-8000-000000000004", CommandID: "00000000-0000-4000-8000-000000000005", AttemptID: "00000000-0000-4000-8000-000000000006", InboxID: "00000000-0000-4000-8000-000000000007", ConsumerName: "tool-worker", RequestHash: "source-command-hash", SourceCommandRef: "encrypted://source-command", SourceCommandHash: "source-command-hash", JobID: "00000000-0000-4000-8000-000000000008",
		ToolName: "workspace_publish", DescriptorSnapshotID: "workspace_publish@1.2.3", ToolRequestHash: "tool-request-hash", EffectClass: effectClass, EffectKey: "workspace:project:42", EffectScope: "tenant:workspace:42", ProviderID: "workspace", ProviderRequestID: "provider-request-42", ToolVersion: 2, EffectVersion: 2, AttemptVersion: 1, Fence: 7, LeaseExpiresAt: at.Add(-time.Second),
	}
}

func testIDKey() []byte { return bytes.Repeat([]byte{0x42}, 32) }

func payloadFor(candidate executionpostgres.ExpiredToolEffectCandidate) *payloadStub {
	encoded, err := json.Marshal(sourceCommandBinding{SchemaVersion: 1, ToolCallID: candidate.ToolCallID, RunID: candidate.RunID, UserID: candidate.UserID, CorrelationID: "00000000-0000-4000-8000-000000000009", ToolName: candidate.ToolName, DescriptorSnapshotID: candidate.DescriptorSnapshotID, DescriptorHash: string(bytes.Repeat([]byte{'a'}, 64)), Input: payload.Manifest{Ref: "encrypted://normalized-input", Hash: string(bytes.Repeat([]byte{'b'}, 64))}, RequestHash: candidate.ToolRequestHash, EffectClass: candidate.EffectClass, EffectKey: candidate.EffectKey, EffectScope: candidate.EffectScope, ProviderID: candidate.ProviderID})
	if err != nil {
		panic(err)
	}
	return &payloadStub{source: encoded}
}
