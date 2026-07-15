package toolworker

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

type toolStoreStub struct {
	mu               sync.Mutex
	claim            executionpostgres.ToolClaim
	claimErr         error
	heartbeatErr     error
	heartbeats       int
	claimCommand     executionpostgres.ClaimToolCommand
	readCompletion   executionpostgres.CompleteToolCommand
	effectCompletion executionpostgres.CompleteToolCommand
	effect           executionpostgres.EffectCompletion
}

func (store *toolStoreStub) ClaimTool(_ context.Context, command executionpostgres.ClaimToolCommand) (executionpostgres.ToolClaim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.claimCommand = command
	return store.claim, store.claimErr
}

func (store *toolStoreStub) HeartbeatTool(_ context.Context, claim executionpostgres.ToolClaim) (executionpostgres.ToolClaim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.heartbeats++
	if store.heartbeatErr != nil {
		return executionpostgres.ToolClaim{}, store.heartbeatErr
	}
	claim.LeaseExpiresAt = claim.LeaseExpiresAt.Add(time.Minute)
	return claim, nil
}

func (store *toolStoreStub) CompleteReadOnlyTool(_ context.Context, command executionpostgres.CompleteToolCommand) (executionpostgres.CompletedTool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.readCompletion = command
	return executionpostgres.CompletedTool{}, nil
}

func (store *toolStoreStub) CompleteEffectTool(_ context.Context, command executionpostgres.CompleteToolCommand, effect executionpostgres.EffectCompletion) (executionpostgres.CompletedTool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.effectCompletion = command
	store.effect = effect
	return executionpostgres.CompletedTool{}, nil
}

type policyFunc func(context.Context, ProposedExecution) (PolicyDecision, error)

func (function policyFunc) Evaluate(ctx context.Context, execution ProposedExecution) (PolicyDecision, error) {
	return function(ctx, execution)
}

type executorFunc func(context.Context, Execution) (Outcome, error)

func (function executorFunc) Execute(ctx context.Context, execution Execution) (Outcome, error) {
	return function(ctx, execution)
}

func TestHandlerBindsPolicyClaimsHeartbeatsAndCompletesRead(t *testing.T) {
	payloads := &memoryPayloads{}
	delivered, command := putExecution(t, payloads, "read_only")
	claim := validClaim(command)
	tools := &toolStoreStub{claim: claim}
	handler := validHandler(payloads, tools, policyFunc(func(_ context.Context, proposed ProposedExecution) (PolicyDecision, error) {
		if proposed.Payload.DescriptorHash != "descriptor-hash" || string(proposed.Input) != `{"query":"status"}` {
			t.Fatal("policy did not receive the bound execution")
		}
		return allowedPolicy(), nil
	}), executorFunc(func(ctx context.Context, execution Execution) (Outcome, error) {
		if execution.Claim.ToolCallID != delivered.AggregateID || execution.Policy.SnapshotID != "overlay-production" {
			t.Fatal("executor did not receive the claimed policy-bound execution")
		}
		select {
		case <-time.After(18 * time.Millisecond):
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		}
		return Outcome{State: statemachine.ToolCallSucceeded, Result: json.RawMessage(`{"ok":true}`)}, nil
	}))
	if err := handler.Handle(t.Context(), delivered); err != nil {
		t.Fatal(err)
	}
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if tools.heartbeats < 1 || tools.claimCommand.ExpectedBinding == nil || *tools.claimCommand.ExpectedBinding != claim.Binding {
		t.Fatalf("claim was not bound or heartbeated: heartbeats=%d command=%#v", tools.heartbeats, tools.claimCommand)
	}
	completion := tools.readCompletion
	if completion.TargetState != statemachine.ToolCallSucceeded || completion.ResultHash != plaintextHash([]byte(`{"ok":true}`)) || !completion.Claim.LeaseExpiresAt.After(claim.LeaseExpiresAt) || completion.ToolCompletedEvent.Hash == "" || completion.ResumeCommand.Hash == "" {
		t.Fatalf("completion was not evidence-backed and fenced: %#v", completion)
	}
	if tools.effectCompletion.Claim.ToolCallID != "" {
		t.Fatal("read-only result crossed the effect-ledger boundary")
	}
}

func TestHandlerCancelsExecutorWhenHeartbeatIsLost(t *testing.T) {
	payloads := &memoryPayloads{}
	delivered, command := putExecution(t, payloads, "read_only")
	tools := &toolStoreStub{claim: validClaim(command), heartbeatErr: executionpostgres.ErrExecutionRightConflict}
	cancelled := make(chan struct{})
	handler := validHandler(payloads, tools, policyFunc(func(context.Context, ProposedExecution) (PolicyDecision, error) {
		return allowedPolicy(), nil
	}), executorFunc(func(ctx context.Context, _ Execution) (Outcome, error) {
		<-ctx.Done()
		close(cancelled)
		return Outcome{}, ctx.Err()
	}))
	err := handler.Handle(t.Context(), delivered)
	if !errors.Is(err, ErrHeartbeatLost) {
		t.Fatalf("error=%v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("executor did not observe fence-loss cancellation")
	}
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if tools.readCompletion.Claim.ToolCallID != "" || tools.effectCompletion.Claim.ToolCallID != "" {
		t.Fatal("lease-losing worker committed a result")
	}
}

func TestHandlerFailsClosedOnCurrentOverlayDenial(t *testing.T) {
	payloads := &memoryPayloads{}
	delivered, command := putExecution(t, payloads, "idempotent_write")
	tools := &toolStoreStub{claim: validClaim(command)}
	handler := validHandler(payloads, tools, policyFunc(func(context.Context, ProposedExecution) (PolicyDecision, error) {
		return PolicyDecision{Allowed: false, SnapshotID: "overlay-production", SnapshotHash: "overlay-hash", ReasonCode: "emergency_revocation", OverlayVersion: 12}, nil
	}), executorFunc(func(context.Context, Execution) (Outcome, error) {
		t.Fatal("denied execution reached the tool handler")
		return Outcome{}, nil
	}))
	if err := handler.Handle(t.Context(), delivered); err != nil {
		t.Fatal(err)
	}
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if tools.effectCompletion.TargetState != statemachine.ToolCallFailed || tools.effect.ExternalResourceRef != "" || tools.claimCommand.ToolStartedEvent.Hash == "" {
		t.Fatalf("deny was not committed as a no-side-effect failure: command=%#v effect=%#v", tools.effectCompletion, tools.effect)
	}
}

func TestHandlerPersistsOutcomeUnknownAndSchedulesReconciliation(t *testing.T) {
	payloads := &memoryPayloads{}
	delivered, command := putExecution(t, payloads, "reconcilable_write")
	claim := validClaim(command)
	tools := &toolStoreStub{claim: claim}
	now := time.Date(2026, time.July, 16, 3, 0, 0, 0, time.UTC)
	handler := validHandler(payloads, tools, policyFunc(func(context.Context, ProposedExecution) (PolicyDecision, error) {
		return allowedPolicy(), nil
	}), executorFunc(func(context.Context, Execution) (Outcome, error) {
		return Outcome{State: statemachine.ToolCallOutcomeUnknown, Result: json.RawMessage(`{"status":"timeout_after_send"}`), ExternalResourceRef: "provider://request/42", EffectDisposition: EffectUnknown}, nil
	}))
	handler.Now = func() time.Time { return now }
	if err := handler.Handle(t.Context(), delivered); err != nil {
		t.Fatal(err)
	}
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if tools.effectCompletion.TargetState != statemachine.ToolCallOutcomeUnknown || tools.effect.ExternalResourceRef != "provider://request/42" || !tools.effect.ReconciliationDueAt.Equal(now.Add(handler.ReconcileDelay)) || tools.effect.ReconcileCommand.Hash == "" || tools.effect.ReconcileResource != handler.Reconcile.ResourceClass {
		t.Fatalf("unknown outcome did not produce reconciliation: completion=%#v effect=%#v", tools.effectCompletion, tools.effect)
	}
}

func TestHandlerRejectsInputSubstitutionBeforePolicyAndClaim(t *testing.T) {
	payloads := &memoryPayloads{}
	delivered, command := putExecution(t, payloads, "read_only")
	command.NormalizedInputHash = plaintextHash([]byte(`{"query":"different"}`))
	encoded, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := payloads.Put(t.Context(), payload.Descriptor{TenantID: delivered.TenantID, ObjectID: delivered.CommandID, Class: "tool-execute-command", ContentType: "application/json"}, encoded)
	if err != nil {
		t.Fatal(err)
	}
	delivered.PayloadRef, delivered.PayloadHash = manifest.Ref, manifest.Hash
	tools := &toolStoreStub{claim: validClaim(command)}
	handler := validHandler(payloads, tools, policyFunc(func(context.Context, ProposedExecution) (PolicyDecision, error) {
		t.Fatal("substituted input reached policy")
		return PolicyDecision{}, nil
	}), executorFunc(func(context.Context, Execution) (Outcome, error) {
		t.Fatal("substituted input reached executor")
		return Outcome{}, nil
	}))
	err = handler.Handle(t.Context(), delivered)
	if !errors.Is(err, eventpostgres.ErrDeliveryConflict) || !errors.Is(err, ErrCommand) {
		t.Fatalf("error=%v", err)
	}
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if tools.claimCommand.Command.CommandID != "" {
		t.Fatal("substituted input acquired a claim")
	}
}

func putExecution(t *testing.T, store payload.Store, effectClass string) (eventpostgres.DeliveredCommand, CommandPayload) {
	t.Helper()
	delivered := eventpostgres.DeliveredCommand{TenantID: "tenant-1", StoreEpoch: "epoch-1", CommandID: "command-1", CommandType: "ExecuteToolCall", AggregateKind: "tool_call", AggregateID: "tool-1", QueueGeneration: 2, DispatchVersion: 3}
	input := []byte(`{"query":"status"}`)
	inputManifest, err := store.Put(t.Context(), payload.Descriptor{TenantID: delivered.TenantID, ObjectID: delivered.AggregateID, Class: "tool-normalized-input", ContentType: "application/json"}, input)
	if err != nil {
		t.Fatal(err)
	}
	command := CommandPayload{SchemaVersion: 1, ToolCallID: delivered.AggregateID, RunID: "run-1", UserID: "user-1", PermissionSnapshot: "membership:m1:v1:role:member", CorrelationID: "correlation-1", ToolName: "provider_status", DescriptorSnapshotID: "provider_status@v1", DescriptorHash: "descriptor-hash", Input: inputManifest, NormalizedInputHash: plaintextHash(input), RequestHash: "request-hash", EffectClass: effectClass}
	if effectClass != "read_only" {
		command.EffectKey, command.EffectScope, command.ProviderID = "effect-key", "tenant:provider", "provider"
	}
	encoded, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := store.Put(t.Context(), payload.Descriptor{TenantID: delivered.TenantID, ObjectID: delivered.CommandID, Class: "tool-execute-command", ContentType: "application/json"}, encoded)
	if err != nil {
		t.Fatal(err)
	}
	delivered.PayloadRef, delivered.PayloadHash = manifest.Ref, manifest.Hash
	return delivered, command
}

func validClaim(command CommandPayload) executionpostgres.ToolClaim {
	binding := executionpostgres.ToolBinding{ToolName: command.ToolName, DescriptorSnapshotID: command.DescriptorSnapshotID, NormalizedInputRef: command.Input.Ref, RequestHash: command.RequestHash, EffectClass: command.EffectClass, EffectKey: command.EffectKey, EffectScope: command.EffectScope, ProviderID: command.ProviderID}
	claim := executionpostgres.ToolClaim{ToolCallID: command.ToolCallID, RunID: command.RunID, GroupID: "group-1", TenantID: "tenant-1", UserID: "user-1", StoreEpoch: "epoch-1", ToolCallVersion: 2, EffectClass: command.EffectClass, CommandID: "command-1", ConsumerName: "tool-worker", RequestHash: "command-payload-hash", JobID: "job-1", InboxID: "inbox-1", AttemptID: "attempt-1", Fence: 1, LeaseToken: "lease-token", LeaseExpiresAt: time.Now().Add(time.Minute), Binding: binding}
	if command.EffectClass != "read_only" {
		claim.EffectID, claim.ProviderRequestID = "effect-1", "effect-1"
	}
	return claim
}

func allowedPolicy() PolicyDecision {
	return PolicyDecision{Allowed: true, SnapshotID: "overlay-production", SnapshotHash: "overlay-hash", OverlayVersion: 11}
}

func validHandler(payloads payload.Store, tools ToolStore, policy PolicyEvaluator, executor Executor) Handler {
	return Handler{
		Payloads: payloads, Tools: tools, Policy: policy, Executor: executor,
		ConsumerName: "tool-worker", WorkerID: "tool-worker-1",
		Actor: json.RawMessage(`{"kind":"service","name":"tool-worker"}`),
		IDKey: []byte("0123456789abcdef0123456789abcdef"), HeartbeatInterval: 5 * time.Millisecond,
		Resume:         Schedule{QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 4, MaxAttempts: 5},
		Reconcile:      Schedule{QueueClass: "background", ResourceClass: "tool-reconciliation", Priority: 40, CostUnits: 1, MaxAttempts: 8},
		ReconcileDelay: time.Minute,
	}
}
