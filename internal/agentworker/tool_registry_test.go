package agentworker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/langshift/lites/internal/behavior"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/toolregistry"
)

type promptAssetStub string

func (prompt promptAssetStub) LoadPrompt(context.Context, string, behavior.Binding) (string, error) {
	return string(prompt), nil
}

type admissionFunc func(context.Context, ToolAdmissionRequest) (ToolAdmissionDecision, error)

func (function admissionFunc) Evaluate(ctx context.Context, request ToolAdmissionRequest) (ToolAdmissionDecision, error) {
	return function(ctx, request)
}

func TestRegistryBehaviorAssetsUseFullDescriptorHash(t *testing.T) {
	registry, snapshot := agentToolRegistry(t, toolregistry.ApprovalNone, "read_only")
	assets := RegistryBehaviorAssets{Prompts: promptAssetStub("system prompt"), Registry: &registry}
	binding := behavior.Binding{ID: snapshot.Descriptor.Name, Version: snapshot.Descriptor.Version, Hash: snapshot.Hash}
	asset, err := assets.LoadTool(t.Context(), "tenant-1", binding)
	if err != nil || asset.DescriptorHash != snapshot.Hash || asset.Tool.Name != snapshot.Descriptor.Name || !asset.Tool.Strict {
		t.Fatalf("asset=%#v err=%v", asset, err)
	}
	binding.Hash = strings.Repeat("f", 64)
	if _, err = assets.LoadTool(t.Context(), "tenant-1", binding); !errors.Is(err, ErrToolRegistry) {
		t.Fatalf("mismatched descriptor hash loaded: %v", err)
	}
}

func TestRegistryToolResolverNormalizesAndAppliesDenyOnlyAdmission(t *testing.T) {
	registry, snapshot := agentToolRegistry(t, toolregistry.ApprovalNone, "read_only")
	resolver := RegistryToolResolver{Registry: &registry, Admission: admissionFunc(func(_ context.Context, request ToolAdmissionRequest) (ToolAdmissionDecision, error) {
		if request.Snapshot.Hash != snapshot.Hash || string(request.NormalizedInput) != `{"limit":2,"query":"status"}` || len(request.NormalizedInputHash) != 64 || len(request.RequestHash) != 64 {
			t.Fatalf("admission request=%#v", request)
		}
		return ToolAdmissionDecision{Decision: "require_approval", PolicySnapshotID: "policy-1", PolicyHash: strings.Repeat("b", 64), PolicyVersion: 7, PermissionSnapshot: "membership:m1:v3:role:member", MaxAttempts: 2}, nil
	})}
	advertised, err := registry.LLMTool(snapshot.SnapshotID, snapshot.Hash)
	if err != nil {
		t.Fatal(err)
	}
	runExecution := Execution{Claim: executionpostgres.RunClaim{TenantID: "tenant-1", UserID: "user-1", RunID: "run-1"}}
	resolved, normalized, requestHash, err := resolver.ResolveAndNormalize(t.Context(), runExecution, llmpostgres.SnapshotBinding{ID: snapshot.SnapshotID, Version: 1, Hash: snapshot.Hash}, advertised, provider.ToolCall{ID: "call-1", Name: advertised.Name, Input: json.RawMessage(`{"query":"status","limit":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(normalized) != `{"limit":2,"query":"status"}` || len(requestHash) != 64 || resolved.DescriptorSnapshotID != snapshot.SnapshotID || resolved.DescriptorHash != snapshot.Hash || resolved.EffectClass != "read_only" || !resolved.ManualApprovalRequired || resolved.RequiresPreview || resolved.MaxAttempts != 2 || resolved.PolicyVersion != 7 {
		t.Fatalf("resolved=%#v normalized=%s request=%s", resolved, normalized, requestHash)
	}
}

func TestRegistryToolResolverCannotDowngradePreviewOrWriteEffect(t *testing.T) {
	registry, snapshot := agentToolRegistry(t, toolregistry.ApprovalPreview, "reconcilable_write")
	advertised, err := registry.LLMTool(snapshot.SnapshotID, snapshot.Hash)
	if err != nil {
		t.Fatal(err)
	}
	decision := ToolAdmissionDecision{Decision: "allow", EffectKey: "effect-1", EffectScope: "tenant:provider", ProviderID: "provider", PolicySnapshotID: "policy-1", PolicyHash: strings.Repeat("b", 64), PolicyVersion: 8, PermissionSnapshot: "membership:m1:v3:role:member"}
	resolver := RegistryToolResolver{Registry: &registry, Admission: admissionFunc(func(context.Context, ToolAdmissionRequest) (ToolAdmissionDecision, error) { return decision, nil })}
	execution := Execution{Claim: executionpostgres.RunClaim{TenantID: "tenant-1", UserID: "user-1", RunID: "run-1"}}
	binding := llmpostgres.SnapshotBinding{ID: snapshot.SnapshotID, Version: 1, Hash: snapshot.Hash}
	resolved, _, _, err := resolver.ResolveAndNormalize(t.Context(), execution, binding, advertised, provider.ToolCall{ID: "call-1", Name: advertised.Name, Input: json.RawMessage(`{"query":"status","limit":2}`)})
	if err != nil || !resolved.ManualApprovalRequired || !resolved.RequiresPreview || resolved.EffectClass != "reconcilable_write" || resolved.EffectKey != "effect-1" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	decision.EffectKey = ""
	if _, _, _, err = resolver.ResolveAndNormalize(t.Context(), execution, binding, advertised, provider.ToolCall{ID: "call-2", Name: advertised.Name, Input: json.RawMessage(`{"query":"status","limit":2}`)}); !errors.Is(err, ErrToolAdmission) {
		t.Fatalf("write without effect key admitted: %v", err)
	}
}

func TestRegistryToolResolverRejectsAdvertisedSchemaSubstitution(t *testing.T) {
	registry, snapshot := agentToolRegistry(t, toolregistry.ApprovalNone, "read_only")
	advertised, err := registry.LLMTool(snapshot.SnapshotID, snapshot.Hash)
	if err != nil {
		t.Fatal(err)
	}
	advertised.InputSchema = json.RawMessage(`{"type":"object","additionalProperties":true}`)
	called := false
	resolver := RegistryToolResolver{Registry: &registry, Admission: admissionFunc(func(context.Context, ToolAdmissionRequest) (ToolAdmissionDecision, error) {
		called = true
		return ToolAdmissionDecision{}, nil
	})}
	execution := Execution{Claim: executionpostgres.RunClaim{TenantID: "tenant-1", UserID: "user-1", RunID: "run-1"}}
	_, _, _, err = resolver.ResolveAndNormalize(t.Context(), execution, llmpostgres.SnapshotBinding{ID: snapshot.SnapshotID, Version: 1, Hash: snapshot.Hash}, advertised, provider.ToolCall{ID: "call-1", Name: advertised.Name, Input: json.RawMessage(`{"query":"status","limit":2}`)})
	if !errors.Is(err, ErrToolRegistry) || called {
		t.Fatalf("error=%v admission_called=%v", err, called)
	}
}

func agentToolRegistry(t *testing.T, approvalMode, effectClass string) (toolregistry.Registry, toolregistry.Snapshot) {
	t.Helper()
	descriptor := toolregistry.Descriptor{
		SchemaVersion: 1, Name: "provider_lookup", Version: "1.2.3", DisplayName: "Provider lookup", Description: "Look up provider status.", Category: "external_api",
		InputSchema:  json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"query":{"type":"string","minLength":1},"limit":{"type":"integer","minimum":1,"maximum":10}},"required":["query","limit"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"items":{"type":"array","items":{"type":"string"}}},"required":["items"],"additionalProperties":false}`),
		EffectClass:  effectClass, MaxAttempts: 5, ApprovalMode: approvalMode,
		ExecutionKind: toolregistry.ExecutionWorker, Handler: "provider_lookup", TrustTier: "semi_trusted",
		RuntimeImage:        "registry.invalid/provider-lookup@sha256:" + strings.Repeat("a", 64),
		RuntimePolicy:       &toolregistry.RuntimePolicyBinding{SnapshotKey: "runtime-policy:provider:v1", IsolationKind: "firecracker", NetworkMode: "allowlist_proxy", NetworkPolicyHash: "sha256:" + strings.Repeat("b", 64), SecretMode: "none", SecretScopeHash: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", WorkspaceMode: "none", MinimumPids: 32},
		Resources:           toolregistry.ResourceLimits{CPUMillis: 250, MemoryBytes: 128 << 20, DiskBytes: 256 << 20, Timeout: "30s", MaximumInput: 64 << 10, MaximumOutput: 1 << 20, MaximumLogBytes: 1 << 20},
		Scheduling:          toolregistry.Scheduling{QueueClass: "interactive", ResourceClass: "tool-network", Priority: 50, CostUnits: 2},
		NetworkEgressPolicy: "allowlist", EgressAllowlist: []string{"api.provider.invalid"}, Source: "platform",
	}
	if approvalMode != toolregistry.ApprovalNone {
		descriptor.ApprovalHint = "Review the external target."
	}
	if effectClass != "read_only" {
		descriptor.RequiresEffectKey, descriptor.SupportsReconcile, descriptor.ReconcileAfter = true, true, "1m"
	}
	registry, err := toolregistry.New([]toolregistry.Descriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	snapshots := registry.Snapshots()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots=%d", len(snapshots))
	}
	return registry, snapshots[0]
}
