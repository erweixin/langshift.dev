// Package referenceassets produces the reviewed Stage 3 capacity behavior
// definition from deployment-specific immutable coordinates.
package referenceassets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/langshift/lites/internal/agentworker"
	"github.com/langshift/lites/internal/behavior"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/releaseassets"
	"github.com/langshift/lites/internal/toolregistry"
)

var ErrInvalid = errors.New("reference release definition configuration is invalid")

var (
	hostPattern  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
	imagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*@sha256:[0-9a-f]{64}$`)
)

type Config struct {
	ProviderHost, CredentialSecretRef, CredentialSecretVersion string
	RuntimeImage                                               string
}

func Build(config Config) (releaseassets.Definitions, error) {
	host := strings.ToLower(config.ProviderHost)
	if !hostPattern.MatchString(host) || net.ParseIP(host) != nil || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".localhost") || config.CredentialSecretRef == "" || config.CredentialSecretVersion == "" || strings.ContainsAny(config.CredentialSecretRef+config.CredentialSecretVersion, "\x00\r\n") || !imagePattern.MatchString(config.RuntimeImage) {
		return releaseassets.Definitions{}, ErrInvalid
	}
	profiles := []behavior.Profile{behavior.RoutePlanner, behavior.DailyPlanner, behavior.Coach, behavior.Evaluator, behavior.ArtifactBuilder}
	prompts := make([]agentworker.PromptDefinition, 0, len(profiles))
	routes := make([]agentworker.RouteDefinition, 0, len(profiles))
	for _, profile := range profiles {
		id := strings.ReplaceAll(string(profile), "_", "-")
		content := "You are the immutable " + string(profile) + " for the Stage 3 reference-production capacity gate. Execute only the controlled reference scenario and never infer customer facts."
		prompts = append(prompts, agentworker.PromptDefinition{TenantID: "*", ID: id + "-reference", Version: "2026.07.1", Content: content, Hash: digest(content)})
		temperature, topP := 0.0, 1.0
		routes = append(routes, agentworker.RouteDefinition{
			Profile: profile, ModelID: id + "-reference-model", ModelVersion: "2026.07.1", RouterID: id + "-reference-router", RouterVersion: "2026.07.1",
			Candidates:  []agentworker.RouteCandidateDefinition{{ProviderID: "reference-provider", ModelID: "reference-capacity", ModelVersion: "2026-07-16", BoundHost: host, PricingVersion: "reference-2026-07", CredentialMode: "managed"}},
			Temperature: &temperature, TopP: &topP, ToolChoice: provider.ToolChoice{Mode: "auto"}, Stream: true,
		})
	}
	definitions := releaseassets.Definitions{
		DefinitionVersion: releaseassets.DefinitionVersion, Prompts: prompts, Routes: routes,
		Tools: []toolregistry.Descriptor{referenceTool(config.RuntimeImage)},
		Providers: provider.RegistryDocument{SchemaVersion: 1, SnapshotID: "provider-registry:stage3-reference-production", Version: 1, Providers: []provider.ProviderDefinition{{
			ID: "reference-provider", Type: "openai_compatible", Status: "active", Endpoint: "https://" + host + "/v1/", BoundHost: host, Region: "global",
			Credential: provider.ManagedCredential{Mode: "bearer", SecretRef: config.CredentialSecretRef, SecretVersion: config.CredentialSecretVersion}, RequestTimeoutText: "2m", MaximumRequestBytes: 8 << 20, MaximumResponseBytes: 16 << 20,
			Models: []provider.ModelDefinition{{ID: "reference-capacity", Version: "2026-07-16", WireModel: "reference-model-2026-07-16", PricingVersion: "reference-2026-07", Capabilities: []string{"streaming", "text", "tool_use"}, MaximumInputTokens: 100000, MaximumOutputTokens: 10000, InputMicrounitsPerMillion: 0, OutputMicrounitsPerMillion: 0, InputCreditUnitsPerMillion: 1, OutputCreditUnitsPerMillion: 1}},
		}}},
	}
	if _, err := releaseassets.Build(definitions, strings.Repeat("0", 40), time.Unix(1, 0).UTC()); err != nil {
		return releaseassets.Definitions{}, errors.Join(ErrInvalid, err)
	}
	return definitions, nil
}

func Write(path string, definitions releaseassets.Definitions) error {
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return ErrInvalid
	}
	encoded, err := json.MarshalIndent(definitions, "", "  ")
	if err != nil {
		return ErrInvalid
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err = file.Write(encoded); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func referenceTool(image string) toolregistry.Descriptor {
	empty := "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	return toolregistry.Descriptor{
		SchemaVersion: 1, Name: "reference_artifact", Version: "1.0.0", DisplayName: "Reference artifact", Description: "Generate the fixed Stage 3 Runtime and encrypted Artifact load payload.", Category: "capacity",
		InputSchema:  json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"bytes":{"type":"integer","minimum":1048576,"maximum":8388608},"hold_milliseconds":{"type":"integer","minimum":100,"maximum":10000}},"required":["bytes","hold_milliseconds"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"artifact":{"type":"string","minLength":1048576,"maxLength":8388608}},"required":["artifact"],"additionalProperties":false}`),
		EffectClass:  "read_only", MaxAttempts: 5, ApprovalMode: toolregistry.ApprovalNone,
		ExecutionKind: toolregistry.ExecutionWorker, Handler: "reference_artifact", TrustTier: "untrusted", RuntimeImage: image,
		RuntimePolicy:       &toolregistry.RuntimePolicyBinding{SnapshotKey: "runtime-policy:reference-artifact:v1", IsolationKind: "firecracker", NetworkMode: "none", NetworkPolicyHash: empty, SecretMode: "none", SecretScopeHash: empty, WorkspaceMode: "none", MinimumPids: 32},
		Resources:           toolregistry.ResourceLimits{CPUMillis: 250, MemoryBytes: 128 << 20, DiskBytes: 256 << 20, Timeout: "30s", MaximumInput: 64 << 10, MaximumOutput: 9 << 20, MaximumLogBytes: 1 << 20},
		Scheduling:          toolregistry.Scheduling{QueueClass: "interactive", ResourceClass: "tool-reference-runtime", Priority: 50, CostUnits: 2},
		NetworkEgressPolicy: "deny_all", Source: "platform", Changelog: "Initial immutable Stage 3 reference artifact tool.",
	}
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
