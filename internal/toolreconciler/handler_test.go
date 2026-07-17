package toolreconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/toolregistry"
)

type memoryPayloads struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (store *memoryPayloads) Put(_ context.Context, descriptor payload.Descriptor, value []byte) (payload.Manifest, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.objects == nil {
		store.objects = map[string][]byte{}
	}
	digest := sha256.Sum256(value)
	hash := hex.EncodeToString(digest[:])
	ref := descriptor.TenantID + "/" + descriptor.Class + "/" + descriptor.ObjectID + "/" + hash
	store.objects[ref] = append([]byte(nil), value...)
	return payload.Manifest{Ref: ref, Hash: hash}, nil
}

func (store *memoryPayloads) Get(_ context.Context, descriptor payload.Descriptor, manifest payload.Manifest) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	prefix := descriptor.TenantID + "/" + descriptor.Class + "/" + descriptor.ObjectID + "/"
	if !strings.HasPrefix(manifest.Ref, prefix) {
		return nil, payload.ErrIntegrity
	}
	value, ok := store.objects[manifest.Ref]
	if !ok {
		return nil, errors.New("missing payload")
	}
	digest := sha256.Sum256(value)
	if hex.EncodeToString(digest[:]) != manifest.Hash {
		return nil, payload.ErrIntegrity
	}
	return append([]byte(nil), value...), nil
}

type reconciliationStoreStub struct {
	mu           sync.Mutex
	claim        executionpostgres.ReconciliationClaim
	claimErr     error
	heartbeatErr error
	heartbeats   int
	claimed      executionpostgres.ClaimReconciliationCommand
	completed    *executionpostgres.CompleteReconciliationCommand
	deferred     *executionpostgres.DeferReconciliationCommand
	escalated    *executionpostgres.EscalateReconciliationCommand
}

func (store *reconciliationStoreStub) ClaimReconciliation(_ context.Context, command executionpostgres.ClaimReconciliationCommand) (executionpostgres.ReconciliationClaim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.claimed = command
	return store.claim, store.claimErr
}

func (store *reconciliationStoreStub) HeartbeatReconciliation(_ context.Context, claim executionpostgres.ReconciliationClaim) (executionpostgres.ReconciliationClaim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.heartbeats++
	if store.heartbeatErr != nil {
		return executionpostgres.ReconciliationClaim{}, store.heartbeatErr
	}
	claim.LeaseExpiresAt = claim.LeaseExpiresAt.Add(time.Minute)
	return claim, nil
}

func (store *reconciliationStoreStub) CompleteReconciliation(_ context.Context, command executionpostgres.CompleteReconciliationCommand) (executionpostgres.CompletedTool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.completed = &command
	return executionpostgres.CompletedTool{}, nil
}

func (store *reconciliationStoreStub) DeferReconciliation(_ context.Context, command executionpostgres.DeferReconciliationCommand) (executionpostgres.DeferredReconciliation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.deferred = &command
	return executionpostgres.DeferredReconciliation{}, nil
}

func (store *reconciliationStoreStub) EscalateReconciliation(_ context.Context, command executionpostgres.EscalateReconciliationCommand) (executionpostgres.EscalatedReconciliation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.escalated = &command
	return executionpostgres.EscalatedReconciliation{}, nil
}

type lookupExecutorFunc func(context.Context, LookupRequest) (LookupResult, error)

func (function lookupExecutorFunc) Execute(ctx context.Context, request LookupRequest) (LookupResult, error) {
	return function(ctx, request)
}

type effectLookupFunc func(context.Context, LookupRequest) (LookupResult, error)

func (function effectLookupFunc) Lookup(ctx context.Context, request LookupRequest) (LookupResult, error) {
	return function(ctx, request)
}

type reconciliationMetricsStub struct {
	heartbeats int
	outcomes   []string
	converged  []bool
}

func (metrics *reconciliationMetricsStub) AddLeaseHeartbeats(_ context.Context, count int64, leaseKind string) {
	if leaseKind == "reconciliation" {
		metrics.heartbeats += int(count)
	}
}

func (metrics *reconciliationMetricsStub) AddToolCall(_ context.Context, outcome string, effectful bool) {
	if effectful {
		metrics.outcomes = append(metrics.outcomes, outcome)
	}
}

func (metrics *reconciliationMetricsStub) AddToolUnknown(_ context.Context, converged bool) {
	metrics.converged = append(metrics.converged, converged)
}

func TestHandlerCompletesConfirmedProviderLookup(t *testing.T) {
	at := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	payloads := &memoryPayloads{}
	delivered, command := putReconciliation(t, payloads, at, 1)
	store := &reconciliationStoreStub{claim: reconciliationClaim(command, delivered, at)}
	handler := validReconciliationHandler(at, payloads, store, lookupExecutorFunc(func(_ context.Context, request LookupRequest) (LookupResult, error) {
		if request.Command.ProviderRequestID != "provider-request-42" || request.Claim.EffectKey != "workspace:42" {
			t.Fatalf("unbound request=%#v", request)
		}
		return LookupResult{Disposition: LookupConfirmed, Evidence: []byte(`{"schema_version":1,"provider_status":"succeeded"}`), ExternalResourceRef: "provider://workspace/42"}, nil
	}))
	metrics := &reconciliationMetricsStub{}
	handler.Metrics = metrics
	if err := handler.Handle(t.Context(), delivered); err != nil {
		t.Fatal(err)
	}
	if store.completed == nil || store.deferred != nil || store.escalated != nil || store.completed.TargetState != statemachine.ToolCallSucceeded || store.completed.ExternalResourceRef != "provider://workspace/42" || store.completed.ResumeCommand.Ref == "" {
		t.Fatalf("completed=%#v deferred=%#v escalated=%#v", store.completed, store.deferred, store.escalated)
	}
	if len(metrics.outcomes) != 0 || len(metrics.converged) != 1 || !metrics.converged[0] {
		t.Fatalf("metrics=%#v", metrics)
	}
}

func TestHandlerDefersInconclusiveLookupWithBoundedBackoff(t *testing.T) {
	at := time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC)
	payloads := &memoryPayloads{}
	delivered, command := putReconciliation(t, payloads, at, 2)
	store := &reconciliationStoreStub{claim: reconciliationClaim(command, delivered, at)}
	handler := validReconciliationHandler(at, payloads, store, lookupExecutorFunc(func(context.Context, LookupRequest) (LookupResult, error) {
		return LookupResult{Disposition: LookupInconclusive, Evidence: []byte(`{"schema_version":1,"provider_status":"pending"}`)}, nil
	}))
	if err := handler.Handle(t.Context(), delivered); err != nil {
		t.Fatal(err)
	}
	if store.deferred == nil || store.completed != nil || store.escalated != nil || !store.deferred.NextDueAt.Equal(at.Add(2*time.Minute)) || store.deferred.NextReconcileCommand.Ref == "" {
		t.Fatalf("deferred=%#v", store.deferred)
	}
	nextID, err := executionpostgres.ReconcileToolEffectRetryCommandID(handler.IDKey, command.EffectID, store.claim.EffectVersion+1)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := payloads.Get(t.Context(), payload.Descriptor{TenantID: command.TenantID, ObjectID: nextID, Class: "tool-reconciliation-command", ContentType: "application/json"}, payload.Manifest{Ref: store.deferred.NextReconcileCommand.Ref, Hash: store.deferred.NextReconcileCommand.Hash})
	var next CommandPayload
	if err != nil || json.Unmarshal(encoded, &next) != nil || next.ReconciliationRound != 3 || !next.ReconciliationDueAt.Equal(at.Add(2*time.Minute)) {
		t.Fatalf("next=%#v err=%v payload=%s", next, err, encoded)
	}
}

func TestHandlerEscalatesAfterMaximumAutomaticRounds(t *testing.T) {
	at := time.Date(2026, 7, 16, 14, 0, 0, 0, time.UTC)
	payloads := &memoryPayloads{}
	delivered, command := putReconciliation(t, payloads, at, 3)
	store := &reconciliationStoreStub{claim: reconciliationClaim(command, delivered, at)}
	handler := validReconciliationHandler(at, payloads, store, lookupExecutorFunc(func(context.Context, LookupRequest) (LookupResult, error) {
		return LookupResult{Disposition: LookupInconclusive, Evidence: []byte(`{"schema_version":1,"provider_status":"unknown"}`)}, nil
	}))
	if err := handler.Handle(t.Context(), delivered); err != nil {
		t.Fatal(err)
	}
	if store.escalated == nil || store.completed != nil || store.deferred != nil || store.escalated.ExpectedEffectVersion != store.claim.EffectVersion {
		t.Fatalf("escalated=%#v", store.escalated)
	}
}

func TestHandlerDoesNotCommitAfterHeartbeatLoss(t *testing.T) {
	at := time.Date(2026, 7, 16, 15, 0, 0, 0, time.UTC)
	payloads := &memoryPayloads{}
	delivered, command := putReconciliation(t, payloads, at, 1)
	store := &reconciliationStoreStub{claim: reconciliationClaim(command, delivered, at), heartbeatErr: executionpostgres.ErrExecutionRightConflict}
	handler := validReconciliationHandler(at, payloads, store, lookupExecutorFunc(func(ctx context.Context, _ LookupRequest) (LookupResult, error) {
		<-ctx.Done()
		return LookupResult{}, ctx.Err()
	}))
	if err := handler.Handle(t.Context(), delivered); !errors.Is(err, ErrHeartbeatLost) || store.completed != nil || store.deferred != nil || store.escalated != nil {
		t.Fatalf("err=%v completed=%#v deferred=%#v escalated=%#v", err, store.completed, store.deferred, store.escalated)
	}
}

func TestRegistryLookupRequiresExactCoverageAndRejectsDrift(t *testing.T) {
	registry, snapshot := reconciliationRegistry(t)
	if _, err := NewRegistryLookup(&registry, nil); !errors.Is(err, ErrLookupConfiguration) {
		t.Fatalf("missing adapter error=%v", err)
	}
	called := 0
	executor, err := NewRegistryLookup(&registry, []LookupRegistration{{Name: "provider_lookup", Lookup: effectLookupFunc(func(_ context.Context, request LookupRequest) (LookupResult, error) {
		called++
		if request.Snapshot.Hash != snapshot.Hash {
			t.Fatal("snapshot was not pinned")
		}
		return LookupResult{Disposition: LookupNotApplied, Evidence: []byte(`{"status":"missing"}`)}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	command := validCommandPayload(time.Now().UTC(), 1)
	command.DescriptorSnapshotID, command.DescriptorHash = snapshot.SnapshotID, snapshot.Hash
	claim := reconciliationClaim(command, eventpostgres.DeliveredCommand{TenantID: command.TenantID, AggregateID: command.ToolCallID, CommandID: "command", PayloadHash: "hash"}, command.ReconciliationDueAt)
	if _, err = executor.Execute(t.Context(), LookupRequest{Command: command, Claim: claim}); err != nil || called != 1 {
		t.Fatalf("execute called=%d err=%v", called, err)
	}
	command.ProviderRequestID = "tampered"
	if _, err = executor.Execute(t.Context(), LookupRequest{Command: command, Claim: claim}); !errors.Is(err, ErrLookupBinding) || called != 1 {
		t.Fatalf("drift reached adapter called=%d err=%v", called, err)
	}
}

func validReconciliationHandler(at time.Time, payloads payload.Store, store ReconciliationStore, lookup LookupExecutor) Handler {
	return Handler{Payloads: payloads, Store: store, Lookup: lookup, ConsumerName: "tool-reconciliation-worker", WorkerID: "worker-1", Actor: json.RawMessage(`{"kind":"service","component":"tool-reconciliation-worker"}`), IDKey: testIDKey(), HeartbeatInterval: 5 * time.Millisecond, MaximumCommand: 1 << 20, MaximumEvidence: 1 << 20, MaximumRounds: 3, RetryDelay: time.Minute, MaximumRetryDelay: 10 * time.Minute, Resume: Schedule{QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 4, MaxAttempts: 5}, Retry: Schedule{QueueClass: "background", ResourceClass: "tool-reconciliation", Priority: 40, CostUnits: 1, MaxAttempts: 20}, Now: func() time.Time { return at }}
}

func putReconciliation(t *testing.T, payloads payload.Store, at time.Time, round int) (eventpostgres.DeliveredCommand, CommandPayload) {
	t.Helper()
	command := validCommandPayload(at, round)
	delivered := eventpostgres.DeliveredCommand{TenantID: command.TenantID, StoreEpoch: "00000000-0000-4000-8000-000000000010", CommandID: "00000000-0000-4000-8000-000000000020", CommandType: "ReconcileToolEffect", AggregateKind: "tool_call", AggregateID: command.ToolCallID, QueueGeneration: 1, DispatchVersion: 1}
	encoded, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := payloads.Put(t.Context(), payload.Descriptor{TenantID: delivered.TenantID, ObjectID: delivered.CommandID, Class: "tool-reconciliation-command", ContentType: "application/json"}, encoded)
	if err != nil {
		t.Fatal(err)
	}
	delivered.PayloadRef, delivered.PayloadHash = manifest.Ref, manifest.Hash
	return delivered, command
}

func validCommandPayload(at time.Time, round int) CommandPayload {
	return CommandPayload{SchemaVersion: 1, TenantID: "00000000-0000-4000-8000-000000000001", RunID: "00000000-0000-4000-8000-000000000002", ToolCallID: "00000000-0000-4000-8000-000000000003", EffectID: "00000000-0000-4000-8000-000000000004", ToolName: "provider_lookup", DescriptorSnapshotID: "provider_lookup@1.0.0", DescriptorHash: strings.Repeat("a", 64), Input: payload.Manifest{Ref: "encrypted://tool-input", Hash: strings.Repeat("c", 64)}, EffectClass: automaticallyReconcilableEffectClass, EffectKey: "workspace:42", EffectScope: "tenant:workspace:42", ProviderID: "workspace", ProviderRequestID: "provider-request-42", RequestHash: strings.Repeat("b", 64), TerminalToolVersion: 3, ReconciliationDueAt: at, OutcomeUnknownAt: at.Add(-time.Minute), ReconciliationRound: round, CorrelationID: "00000000-0000-4000-8000-000000000009"}
}

func reconciliationClaim(command CommandPayload, delivered eventpostgres.DeliveredCommand, at time.Time) executionpostgres.ReconciliationClaim {
	return executionpostgres.ReconciliationClaim{ToolCallID: command.ToolCallID, RunID: command.RunID, GroupID: "00000000-0000-4000-8000-000000000005", EffectID: command.EffectID, TenantID: command.TenantID, UserID: "00000000-0000-4000-8000-000000000006", StoreEpoch: delivered.StoreEpoch, ToolCallVersion: command.TerminalToolVersion, EffectVersion: 4, EffectClass: command.EffectClass, EffectScope: command.EffectScope, EffectKey: command.EffectKey, ProviderID: command.ProviderID, ProviderRequestID: command.ProviderRequestID, CommandID: delivered.CommandID, ConsumerName: "tool-reconciliation-worker", RequestHash: delivered.PayloadHash, JobID: "00000000-0000-4000-8000-000000000007", InboxID: "00000000-0000-4000-8000-000000000008", AttemptID: "00000000-0000-4000-8000-000000000011", Fence: 1, LeaseToken: "lease", LeaseExpiresAt: at.Add(time.Minute), ReconciliationDueAt: command.ReconciliationDueAt}
}

func reconciliationRegistry(t *testing.T) (toolregistry.Registry, toolregistry.Snapshot) {
	t.Helper()
	descriptor := toolregistry.Descriptor{SchemaVersion: 1, Name: "provider_lookup", Version: "1.0.0", DisplayName: "Provider lookup", Description: "Reconcile provider state.", Category: "provider", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`), EffectClass: automaticallyReconcilableEffectClass, RequiresEffectKey: true, SupportsReconcile: true, MaxAttempts: 3, ReconcileAfter: "17s", ApprovalMode: toolregistry.ApprovalNone, ExecutionKind: toolregistry.ExecutionWorker, Handler: "provider_lookup", TrustTier: "trusted", RuntimeImage: "registry.example/tools/provider@sha256:" + strings.Repeat("a", 64), Resources: toolregistry.ResourceLimits{CPUMillis: 250, MemoryBytes: 128 << 20, DiskBytes: 64 << 20, Timeout: "30s", MaximumInput: 64 << 10, MaximumOutput: 1 << 20, MaximumLogBytes: 1 << 20}, Scheduling: toolregistry.Scheduling{QueueClass: "interactive", ResourceClass: "tool-network", Priority: 50, CostUnits: 2}, NetworkEgressPolicy: "deny_all", Source: "platform", Changelog: "Initial production descriptor."}
	registry, err := toolregistry.New([]toolregistry.Descriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	return registry, registry.Snapshots()[0]
}
