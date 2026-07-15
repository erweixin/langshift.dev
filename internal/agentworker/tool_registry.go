package agentworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/langshift/lites/internal/behavior"
	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/toolregistry"
)

var (
	ErrToolRegistry  = errors.New("agent tool registry binding is invalid")
	ErrToolAdmission = errors.New("agent tool call was not admitted")
)

var policyDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type PromptAssetLoader interface {
	LoadPrompt(context.Context, string, behavior.Binding) (string, error)
}

// RegistryBehaviorAssets makes behavior tool bindings resolve through the
// complete immutable descriptor while prompt storage remains independently
// replaceable.
type RegistryBehaviorAssets struct {
	Prompts  PromptAssetLoader
	Registry *toolregistry.Registry
}

func (loader RegistryBehaviorAssets) LoadPrompt(ctx context.Context, tenantID string, binding behavior.Binding) (string, error) {
	if loader.Prompts == nil {
		return "", ErrToolRegistry
	}
	return loader.Prompts.LoadPrompt(ctx, tenantID, binding)
}

func (loader RegistryBehaviorAssets) LoadTool(_ context.Context, _ string, binding behavior.Binding) (ToolAsset, error) {
	if loader.Registry == nil || binding.ID == "" || binding.Version == "" || binding.Hash == "" {
		return ToolAsset{}, ErrToolRegistry
	}
	tool, err := loader.Registry.LLMTool(binding.ID+"@"+binding.Version, binding.Hash)
	if err != nil || tool.Name != binding.ID {
		return ToolAsset{}, errors.Join(ErrToolRegistry, err)
	}
	return ToolAsset{Tool: tool, DescriptorHash: binding.Hash}, nil
}

type ToolAdmissionRequest struct {
	TenantID, UserID, RunID string
	Snapshot                toolregistry.Snapshot
	NormalizedInput         json.RawMessage
	NormalizedInputHash     string
	RequestHash             string
}

type ToolAdmissionDecision struct {
	Decision                           string
	RequiresPreview                    bool
	EffectKey, EffectScope, ProviderID string
	PolicySnapshotID, PolicyHash       string
	PermissionSnapshot                 string
	PolicyVersion                      uint64
	MaxAttempts                        int
}

type ToolAdmission interface {
	Evaluate(context.Context, ToolAdmissionRequest) (ToolAdmissionDecision, error)
}

// RegistryToolResolver replays the exact descriptor in the context manifest,
// validates and normalizes model input, then applies current permissions and
// deny-only policy. Admission may only preserve or reduce descriptor powers.
type RegistryToolResolver struct {
	Registry  *toolregistry.Registry
	Admission ToolAdmission
}

func (resolver RegistryToolResolver) ResolveAndNormalize(ctx context.Context, execution Execution, binding llmpostgres.SnapshotBinding, advertised provider.Tool, call provider.ToolCall) (ResolvedTool, json.RawMessage, string, error) {
	if resolver.Registry == nil || resolver.Admission == nil || binding.ID == "" || binding.Hash == "" || binding.Version != 1 || call.Name == "" || call.Name != advertised.Name {
		return ResolvedTool{}, nil, "", ErrToolRegistry
	}
	snapshot, err := resolver.Registry.Resolve(binding.ID, binding.Hash)
	if err != nil || snapshot.Descriptor.Name != call.Name || !matchesAdvertisedTool(advertised, snapshot) {
		return ResolvedTool{}, nil, "", errors.Join(ErrToolRegistry, err)
	}
	normalized, inputHash, requestHash, err := resolver.Registry.NormalizeInput(binding.ID, binding.Hash, call.Input)
	if err != nil {
		return ResolvedTool{}, nil, "", err
	}
	decision, err := resolver.Admission.Evaluate(ctx, ToolAdmissionRequest{
		TenantID: execution.Claim.TenantID, UserID: execution.Claim.UserID, RunID: execution.Claim.RunID,
		Snapshot: snapshot, NormalizedInput: normalized, NormalizedInputHash: inputHash, RequestHash: requestHash,
	})
	if err != nil || !validAdmission(snapshot, decision) {
		return ResolvedTool{}, nil, "", errors.Join(ErrToolAdmission, err)
	}
	manual := snapshot.Descriptor.ApprovalMode != toolregistry.ApprovalNone || decision.Decision == "require_approval"
	preview := snapshot.Descriptor.ApprovalMode == toolregistry.ApprovalPreview || decision.RequiresPreview
	maxAttempts := snapshot.Descriptor.MaxAttempts
	if decision.MaxAttempts > 0 {
		maxAttempts = decision.MaxAttempts
	}
	executionKind := ToolExecutionWorker
	if snapshot.Descriptor.ExecutionKind == toolregistry.ExecutionChild {
		executionKind = ToolExecutionChild
	}
	return ResolvedTool{
		Name: snapshot.Descriptor.Name, DescriptorSnapshotID: snapshot.SnapshotID, DescriptorHash: snapshot.Hash,
		ExecutionKind: executionKind, ManualApprovalRequired: manual, RequiresPreview: preview,
		EffectClass: snapshot.Descriptor.EffectClass, EffectKey: decision.EffectKey,
		EffectScope: decision.EffectScope, ProviderID: decision.ProviderID,
		QueueClass: snapshot.Descriptor.Scheduling.QueueClass, ResourceClass: snapshot.Descriptor.Scheduling.ResourceClass,
		Priority: snapshot.Descriptor.Scheduling.Priority, CostUnits: snapshot.Descriptor.Scheduling.CostUnits,
		MaxAttempts: maxAttempts, PolicySnapshotID: decision.PolicySnapshotID,
		PolicyHash: decision.PolicyHash, PolicyVersion: decision.PolicyVersion,
		PermissionSnapshot: decision.PermissionSnapshot,
	}, normalized, requestHash, nil
}

func matchesAdvertisedTool(advertised provider.Tool, snapshot toolregistry.Snapshot) bool {
	return advertised.Name == snapshot.Descriptor.Name && advertised.Description == snapshot.Descriptor.Description && advertised.Strict && bytes.Equal(advertised.InputSchema, snapshot.Descriptor.InputSchema)
}

func validAdmission(snapshot toolregistry.Snapshot, decision ToolAdmissionDecision) bool {
	if decision.PolicySnapshotID == "" || !policyDigestPattern.MatchString(decision.PolicyHash) || decision.PolicyVersion == 0 || decision.PermissionSnapshot == "" || decision.MaxAttempts < 0 || decision.MaxAttempts > snapshot.Descriptor.MaxAttempts || decision.Decision != "allow" && decision.Decision != "require_approval" || decision.Decision == "allow" && decision.RequiresPreview {
		return false
	}
	if snapshot.Descriptor.ExecutionKind == toolregistry.ExecutionChild {
		return decision.Decision == "allow" && !decision.RequiresPreview && decision.EffectKey == "" && decision.EffectScope == "" && decision.ProviderID == ""
	}
	if snapshot.Descriptor.EffectClass == "read_only" {
		return decision.EffectKey == "" && decision.EffectScope == "" && decision.ProviderID == ""
	}
	return decision.EffectKey != "" && decision.EffectScope != "" && decision.ProviderID != ""
}
