package releaseassets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/agentworker"
	"github.com/langshift/lites/internal/behavior"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/toolregistry"
)

func TestBuildIsDeterministicAndEveryArtifactLoads(t *testing.T) {
	definitions := testDefinitions()
	commit := strings.Repeat("a", 40)
	generatedAt := time.Date(2026, time.July, 16, 9, 10, 11, 123456789, time.UTC)
	first, err := Build(definitions, commit, generatedAt)
	if err != nil {
		t.Fatal(err)
	}
	reversed := testDefinitions()
	capabilities := reversed.Providers.Providers[0].Models[0].Capabilities
	capabilities[0], capabilities[1] = capabilities[1], capabilities[0]
	second, err := Build(reversed, commit, generatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.BundleSHA256 != second.Manifest.BundleSHA256 || first.Manifest.DefinitionSHA256 != second.Manifest.DefinitionSHA256 {
		t.Fatalf("non-deterministic bundle: %#v %#v", first.Manifest, second.Manifest)
	}
	otherCommit, err := Build(reversed, strings.Repeat("d", 40), generatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.DefinitionSHA256 != otherCommit.Manifest.DefinitionSHA256 || first.Manifest.BundleSHA256 == otherCommit.Manifest.BundleSHA256 {
		t.Fatalf("definition digest or release identity has the wrong scope")
	}
	for name, encoded := range first.Files {
		if string(encoded) != string(second.Files[name]) {
			t.Fatalf("file %s is not deterministic", name)
		}
	}

	output := filepath.Join(t.TempDir(), "release-assets")
	if err = Write(output, first); err != nil {
		t.Fatal(err)
	}
	if info, statErr := os.Stat(output); statErr != nil || info.Mode().Perm()&0o027 != 0 {
		t.Fatalf("directory mode=%v error=%v", info, statErr)
	}
	for _, artifact := range first.Manifest.Artifacts {
		info, statErr := os.Stat(filepath.Join(output, artifact.File))
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o137 != 0 {
			t.Fatalf("artifact %s mode=%v error=%v", artifact.File, info, statErr)
		}
	}
	if _, err = agentworker.LoadPromptArtifact(filepath.Join(output, "prompts.json"), first.Manifest.Artifacts["prompts"].SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err = agentworker.LoadRouteArtifact(filepath.Join(output, "routes.json"), first.Manifest.Artifacts["routes"].SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err = toolregistry.LoadArtifact(filepath.Join(output, "tools.json"), first.Manifest.Artifacts["tools"].SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.LoadRegistryFile(filepath.Join(output, "providers.json"), first.Manifest.Artifacts["providers"].SHA256); err != nil {
		t.Fatal(err)
	}
	if err = Write(output, first); !errors.Is(err, ErrInvalid) {
		t.Fatalf("existing output replaced: %v", err)
	}
}

func TestWriteRejectsTamperingAndLoadDefinitionsIsStrict(t *testing.T) {
	bundle, err := Build(testDefinitions(), strings.Repeat("b", 40), time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	tampered := cloneBundle(bundle)
	tampered.Files["prompts.json"][0] ^= 1
	if err = Write(filepath.Join(t.TempDir(), "tampered-payload"), tampered); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered payload accepted: %v", err)
	}
	tampered = cloneBundle(bundle)
	tampered.Manifest.Artifacts["prompts"] = Artifact{File: "routes.json", SHA256: tampered.Manifest.Artifacts["routes"].SHA256}
	tampered.Manifest.BundleSHA256, _ = manifestHash(tampered.Manifest)
	manifest, _ := json.Marshal(tampered.Manifest)
	tampered.Files["manifest.json"] = manifest
	if err = Write(filepath.Join(t.TempDir(), "tampered-map"), tampered); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered artifact map accepted: %v", err)
	}
	tampered = cloneBundle(bundle)
	tampered.Files["manifest.json"] = append(tampered.Files["manifest.json"], '\n')
	if err = Write(filepath.Join(t.TempDir(), "tampered-manifest"), tampered); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-canonical manifest accepted: %v", err)
	}

	configuration := filepath.Join(t.TempDir(), "definitions.json")
	encoded, _ := json.Marshal(testDefinitions())
	if err = os.WriteFile(configuration, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadDefinitions(configuration); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadDefinitions(filepath.Base(configuration)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative path accepted: %v", err)
	}
	encoded = append(encoded[:len(encoded)-1], []byte(`,"unknown":true}`)...)
	if err = os.WriteFile(configuration, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadDefinitions(configuration); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown field accepted: %v", err)
	}
	if _, err = Build(testDefinitions(), strings.Repeat("c", 40), time.Date(2026, time.July, 16, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-UTC timestamp accepted: %v", err)
	}
}

func cloneBundle(source Bundle) Bundle {
	encoded, _ := json.Marshal(source.Manifest)
	var manifest Manifest
	_ = json.Unmarshal(encoded, &manifest)
	files := make(map[string][]byte, len(source.Files))
	for name, content := range source.Files {
		files[name] = append([]byte(nil), content...)
	}
	return Bundle{Manifest: manifest, Files: files}
}

func testDefinitions() Definitions {
	prompt := "You are the production coaching agent."
	temperature, topP := 0.2, 0.9
	return Definitions{
		DefinitionVersion: DefinitionVersion,
		Prompts:           []agentworker.PromptDefinition{{TenantID: "*", ID: "coach", Version: "2026.07.1", Content: prompt, Hash: digest(prompt)}},
		Routes: []agentworker.RouteDefinition{{
			Profile: behavior.Coach, ModelID: "coach-models", ModelVersion: "2026.07.1", RouterID: "coach-router", RouterVersion: "2026.07.1",
			Candidates:  []agentworker.RouteCandidateDefinition{{ProviderID: "openai", ModelID: "reasoning", ModelVersion: "2026-07-01", BoundHost: "api.openai.com", PricingVersion: "price-v1", CredentialMode: "managed"}},
			Temperature: &temperature, TopP: &topP, ToolChoice: provider.ToolChoice{Mode: "auto"}, Stream: true,
		}},
		Tools: []toolregistry.Descriptor{testTool()},
		Providers: provider.RegistryDocument{SchemaVersion: 1, SnapshotID: "provider-registry:production", Version: 1, Providers: []provider.ProviderDefinition{{
			ID: "openai", Type: "openai", Status: "active", Endpoint: "https://api.openai.com/v1/", BoundHost: "api.openai.com", Region: "global",
			Credential: provider.ManagedCredential{Mode: "bearer", SecretRef: "lites/providers/openai", SecretVersion: "1"}, RequestTimeoutText: "1m", MaximumRequestBytes: 1 << 20, MaximumResponseBytes: 1 << 20,
			Models: []provider.ModelDefinition{{ID: "reasoning", Version: "2026-07-01", WireModel: "gpt-pinned", PricingVersion: "price-v1", Capabilities: []string{"text", "tool_use"}, MaximumInputTokens: 100000, MaximumOutputTokens: 10000, InputMicrounitsPerMillion: 1000000, OutputMicrounitsPerMillion: 1000000, InputCreditUnitsPerMillion: 2000000, OutputCreditUnitsPerMillion: 4000000}},
		}}},
	}
}

func testTool() toolregistry.Descriptor {
	return toolregistry.Descriptor{
		SchemaVersion: 1, Name: "provider_lookup", Version: "1.2.3", DisplayName: "Provider lookup", Description: "Look up provider status.", Category: "external_api",
		InputSchema:  json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"query":{"type":"string","minLength":1},"limit":{"type":"integer","minimum":1,"maximum":10}},"required":["query","limit"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"items":{"type":"array","items":{"type":"string"}}},"required":["items"],"additionalProperties":false}`),
		EffectClass:  "read_only", MaxAttempts: 5, ApprovalMode: toolregistry.ApprovalNone, ExecutionKind: toolregistry.ExecutionWorker, Handler: "provider_lookup", TrustTier: "semi_trusted",
		RuntimeImage: "registry.invalid/provider-lookup@sha256:" + strings.Repeat("a", 64), RuntimePolicy: &toolregistry.RuntimePolicyBinding{SnapshotKey: "runtime-policy:provider:v1", IsolationKind: "firecracker", NetworkMode: "allowlist_proxy", NetworkPolicyHash: "sha256:" + strings.Repeat("b", 64), SecretMode: "none", SecretScopeHash: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", WorkspaceMode: "none", MinimumPids: 32},
		Resources: toolregistry.ResourceLimits{CPUMillis: 250, MemoryBytes: 128 << 20, DiskBytes: 256 << 20, Timeout: "30s", MaximumInput: 64 << 10, MaximumOutput: 1 << 20, MaximumLogBytes: 1 << 20}, Scheduling: toolregistry.Scheduling{QueueClass: "interactive", ResourceClass: "tool-network", Priority: 50, CostUnits: 2}, NetworkEgressPolicy: "allowlist", EgressAllowlist: []string{"api.provider.invalid"}, Source: "platform",
	}
}

func digest(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}
