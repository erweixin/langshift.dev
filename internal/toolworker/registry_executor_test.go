package toolworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/toolregistry"
)

type toolHandlerFunc func(context.Context, Invocation) (HandlerResult, error)

func (function toolHandlerFunc) Invoke(ctx context.Context, invocation Invocation) (HandlerResult, error) {
	return function(ctx, invocation)
}

func TestRegistryExecutorRevalidatesBindingsInputAndOutput(t *testing.T) {
	registry, snapshot := workerRegistry(t, "read_only")
	called := 0
	executor := newWorkerExecutor(t, &registry, toolHandlerFunc(func(_ context.Context, invocation Invocation) (HandlerResult, error) {
		called++
		if invocation.Snapshot.Hash != snapshot.Hash || string(invocation.Input) != `{"query":"status"}` || invocation.TenantID != "tenant-1" {
			t.Fatalf("invocation is not immutable: %#v", invocation)
		}
		return HandlerResult{Status: ResultSucceeded, Output: json.RawMessage(`{"ok":true}`)}, nil
	}))
	execution := registryExecution(t, registry, snapshot)
	outcome, err := executor.Execute(t.Context(), execution)
	if err != nil || called != 1 || outcome.State != statemachine.ToolCallSucceeded || string(outcome.Result) != `{"ok":true}` || outcome.EffectDisposition != "" {
		t.Fatalf("outcome=%#v called=%d error=%v", outcome, called, err)
	}

	tampered := execution
	tampered.Payload.RequestHash = strings.Repeat("f", 64)
	if _, err = executor.Execute(t.Context(), tampered); !errors.Is(err, ErrExecutionBinding) || called != 1 {
		t.Fatalf("tampered binding reached handler: called=%d error=%v", called, err)
	}
}

func TestRegistryExecutorRejectsOutputOutsideDescriptorSchema(t *testing.T) {
	registry, snapshot := workerRegistry(t, "read_only")
	executor := newWorkerExecutor(t, &registry, toolHandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		return HandlerResult{Status: ResultSucceeded, Output: json.RawMessage(`{"ok":"yes"}`)}, nil
	}))
	if _, err := executor.Execute(t.Context(), registryExecution(t, registry, snapshot)); !errors.Is(err, ErrHandlerResult) || !errors.Is(err, toolregistry.ErrOutputInvalid) {
		t.Fatalf("error=%v", err)
	}
}

func TestRegistryExecutorTranslatesKnownFailureWithoutLeakingHandlerPayload(t *testing.T) {
	registry, snapshot := workerRegistry(t, "reconcilable_write")
	executor := newWorkerExecutor(t, &registry, toolHandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		return HandlerResult{Status: ResultFailed, Failure: Failure{Code: "provider_rejected", SafeMessage: "The provider rejected the request."}}, nil
	}))
	outcome, err := executor.Execute(t.Context(), registryExecution(t, registry, snapshot))
	if err != nil || outcome.State != statemachine.ToolCallFailed || outcome.EffectDisposition != EffectNotApplied || outcome.ExternalResourceRef != "" {
		t.Fatalf("outcome=%#v error=%v", outcome, err)
	}
	var envelope map[string]any
	if json.Unmarshal(outcome.Result, &envelope) != nil || envelope["status"] != "failed" || envelope["code"] != "provider_rejected" || envelope["provider_request_id"] != "effect-1" {
		t.Fatalf("failure envelope=%s", outcome.Result)
	}
}

func TestRegistryExecutorFailsUnclassifiedWriteErrorToOutcomeUnknown(t *testing.T) {
	registry, snapshot := workerRegistry(t, "reconcilable_write")
	executor := newWorkerExecutor(t, &registry, toolHandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		return HandlerResult{}, errors.New("connection reset after send")
	}))
	outcome, err := executor.Execute(t.Context(), registryExecution(t, registry, snapshot))
	if err != nil || outcome.State != statemachine.ToolCallOutcomeUnknown || outcome.EffectDisposition != EffectUnknown || outcome.ReconcileAfter != 17*time.Second || strings.Contains(string(outcome.Result), "connection reset") {
		t.Fatalf("outcome=%#v error=%v", outcome, err)
	}
}

func TestRegistryExecutorOnlyRetriesWriteErrorMarkedBeforeEffect(t *testing.T) {
	registry, snapshot := workerRegistry(t, "reconcilable_write")
	root := errors.New("secret broker unavailable")
	executor := newWorkerExecutor(t, &registry, toolHandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		return HandlerResult{}, BeforeEffect(root)
	}))
	if _, err := executor.Execute(t.Context(), registryExecution(t, registry, snapshot)); !errors.Is(err, root) {
		t.Fatalf("error=%v", err)
	}
}

func TestRegistryExecutorRecoversWriteHandlerPanicAsUnknown(t *testing.T) {
	registry, snapshot := workerRegistry(t, "reconcilable_write")
	executor := newWorkerExecutor(t, &registry, toolHandlerFunc(func(context.Context, Invocation) (HandlerResult, error) {
		panic("credential material must not escape")
	}))
	outcome, err := executor.Execute(t.Context(), registryExecution(t, registry, snapshot))
	if err != nil || outcome.State != statemachine.ToolCallOutcomeUnknown || strings.Contains(string(outcome.Result), "credential material") || !strings.Contains(string(outcome.Result), "handler_panic_after_effect_boundary") {
		t.Fatalf("outcome=%s error=%v", outcome.Result, err)
	}
}

func TestRegistryExecutorEnforcesDescriptorTimeout(t *testing.T) {
	registry, snapshot := workerRegistryWithTimeout(t, "reconcilable_write", "1s")
	executor := newWorkerExecutor(t, &registry, toolHandlerFunc(func(ctx context.Context, _ Invocation) (HandlerResult, error) {
		<-ctx.Done()
		return HandlerResult{}, ctx.Err()
	}))
	started := time.Now()
	outcome, err := executor.Execute(t.Context(), registryExecution(t, registry, snapshot))
	if err != nil || outcome.State != statemachine.ToolCallOutcomeUnknown || !strings.Contains(string(outcome.Result), "handler_timeout_after_effect_boundary") || time.Since(started) > 2*time.Second {
		t.Fatalf("outcome=%s elapsed=%s error=%v", outcome.Result, time.Since(started), err)
	}
}

func TestRegistryExecutorRequiresExactHandlerCoverage(t *testing.T) {
	registry, _ := workerRegistry(t, "read_only")
	if _, err := NewRegistryExecutor(&registry, nil, time.Minute); !errors.Is(err, ErrExecutorConfiguration) {
		t.Fatalf("missing handler error=%v", err)
	}
	if _, err := NewRegistryExecutor(&registry, []HandlerRegistration{{Name: "unexpected", Handler: toolHandlerFunc(func(context.Context, Invocation) (HandlerResult, error) { return HandlerResult{}, nil })}}, time.Minute); !errors.Is(err, ErrExecutorConfiguration) {
		t.Fatalf("unexpected handler error=%v", err)
	}
}

func newWorkerExecutor(t *testing.T, registry *toolregistry.Registry, handler ToolHandler) *RegistryExecutor {
	t.Helper()
	executor, err := NewRegistryExecutor(registry, []HandlerRegistration{{Name: "provider_lookup", Handler: handler}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func workerRegistry(t *testing.T, effectClass string) (toolregistry.Registry, toolregistry.Snapshot) {
	return workerRegistryWithTimeout(t, effectClass, "30s")
}

func workerRegistryWithTimeout(t *testing.T, effectClass, timeout string) (toolregistry.Registry, toolregistry.Snapshot) {
	return workerRegistryWithTrust(t, effectClass, timeout, "trusted")
}

func workerRegistryWithTrust(t *testing.T, effectClass, timeout, trustTier string) (toolregistry.Registry, toolregistry.Snapshot) {
	t.Helper()
	descriptor := toolregistry.Descriptor{
		SchemaVersion: 1, Name: "provider_lookup", Version: "1.0.0", DisplayName: "Provider lookup",
		Description: "Looks up provider state.", Category: "provider",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
		EffectClass:  effectClass, MaxAttempts: 3, ApprovalMode: toolregistry.ApprovalNone,
		ExecutionKind: toolregistry.ExecutionWorker, Handler: "provider_lookup", TrustTier: trustTier,
		RuntimeImage:        "registry.example/tools/provider@sha256:" + strings.Repeat("a", 64),
		Resources:           toolregistry.ResourceLimits{CPUMillis: 250, MemoryBytes: 128 << 20, DiskBytes: 64 << 20, Timeout: timeout, MaximumInput: 64 << 10, MaximumOutput: 1 << 20, MaximumLogBytes: 1 << 20},
		Scheduling:          toolregistry.Scheduling{QueueClass: "interactive", ResourceClass: "tool-network", Priority: 50, CostUnits: 2},
		NetworkEgressPolicy: "deny_all", Source: "platform", Changelog: "Initial production descriptor.",
	}
	if effectClass != "read_only" {
		descriptor.RequiresEffectKey = true
		descriptor.SupportsReconcile = true
		descriptor.ReconcileAfter = "17s"
	}
	registry, err := toolregistry.New([]toolregistry.Descriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := registry.Snapshots()[0]
	return registry, snapshot
}

func registryExecution(t *testing.T, registry toolregistry.Registry, snapshot toolregistry.Snapshot) Execution {
	t.Helper()
	input, inputHash, requestHash, err := registry.NormalizeInput(snapshot.SnapshotID, snapshot.Hash, json.RawMessage(`{"query":"status"}`))
	if err != nil {
		t.Fatal(err)
	}
	manifestHash := sha256.Sum256(input)
	manifest := payload.Manifest{Ref: "encrypted://tenant-1/tool-1/input", Hash: hex.EncodeToString(manifestHash[:])}
	command := CommandPayload{
		SchemaVersion: 1, ToolCallID: "tool-1", RunID: "run-1", CorrelationID: "correlation-1",
		ToolName: snapshot.Descriptor.Name, DescriptorSnapshotID: snapshot.SnapshotID, DescriptorHash: snapshot.Hash,
		Input: manifest, NormalizedInputHash: inputHash, RequestHash: requestHash, EffectClass: snapshot.Descriptor.EffectClass,
	}
	if snapshot.Descriptor.EffectClass != "read_only" {
		command.EffectKey, command.EffectScope, command.ProviderID = "effect-key-1", "tenant:provider", "provider"
	}
	binding := executionpostgres.ToolBinding{
		ToolName: command.ToolName, DescriptorSnapshotID: command.DescriptorSnapshotID,
		NormalizedInputRef: command.Input.Ref, RequestHash: command.RequestHash,
		EffectClass: command.EffectClass, EffectKey: command.EffectKey,
		EffectScope: command.EffectScope, ProviderID: command.ProviderID,
	}
	claim := executionpostgres.ToolClaim{
		ToolCallID: command.ToolCallID, RunID: command.RunID, TenantID: "tenant-1", UserID: "user-1",
		AttemptID: "attempt-1", EffectClass: command.EffectClass, ProviderRequestID: "effect-1", Binding: binding,
	}
	return Execution{ProposedExecution: ProposedExecution{Payload: command, Input: input}, Claim: claim, Policy: allowedPolicy()}
}
