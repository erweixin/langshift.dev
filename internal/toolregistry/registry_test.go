package toolregistry

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRegistryHashesFullAuthorityAndValidatesSchemas(t *testing.T) {
	descriptor := validDescriptor()
	registry, err := New([]Descriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := onlySnapshot(t, registry, descriptor)
	if snapshot.SnapshotID != "provider_lookup@1.2.3" || len(snapshot.Hash) != 64 || len(registry.Hash()) != 64 || snapshot.InputHash == snapshot.OutputHash {
		t.Fatalf("unexpected snapshot: %#v registry=%s", snapshot, registry.Hash())
	}
	tool, err := registry.LLMTool(snapshot.SnapshotID, snapshot.Hash)
	if err != nil || tool.Name != descriptor.Name || tool.Description != descriptor.Description || !tool.Strict {
		t.Fatalf("tool=%#v err=%v", tool, err)
	}
	normalized, inputHash, requestHash, err := registry.NormalizeInput(snapshot.SnapshotID, snapshot.Hash, json.RawMessage(`{"limit":2,"query":"status"}`))
	if err != nil || string(normalized) != `{"limit":2,"query":"status"}` || len(inputHash) != 64 || len(requestHash) != 64 || requestHash == inputHash {
		t.Fatalf("normalized=%s input=%s request=%s err=%v", normalized, inputHash, requestHash, err)
	}
	second, secondInputHash, secondRequestHash, err := registry.NormalizeInput(snapshot.SnapshotID, snapshot.Hash, json.RawMessage("{\n  \"query\": \"status\", \"limit\": 2\n}"))
	if err != nil || string(second) != string(normalized) || secondInputHash != inputHash || secondRequestHash != requestHash {
		t.Fatalf("canonicalization drifted: %s/%s/%s err=%v", second, secondInputHash, secondRequestHash, err)
	}
	if _, _, _, err = registry.NormalizeInput(snapshot.SnapshotID, snapshot.Hash, json.RawMessage(`{"query":"status","extra":true}`)); !errors.Is(err, ErrInputInvalid) {
		t.Fatalf("unknown property accepted: %v", err)
	}
	output, outputHash, err := registry.ValidateOutput(snapshot.SnapshotID, snapshot.Hash, json.RawMessage(`{"items":["up"]}`))
	if err != nil || string(output) != `{"items":["up"]}` || len(outputHash) != 64 {
		t.Fatalf("output=%s hash=%s err=%v", output, outputHash, err)
	}
	if _, _, err = registry.ValidateOutput(snapshot.SnapshotID, snapshot.Hash, json.RawMessage(`{"items":[1]}`)); !errors.Is(err, ErrOutputInvalid) {
		t.Fatalf("invalid output accepted: %v", err)
	}
}

func TestRegistryDescriptorHashCoversNonLLMAuthority(t *testing.T) {
	first := validDescriptor()
	firstRegistry, err := New([]Descriptor{first})
	if err != nil {
		t.Fatal(err)
	}
	firstSnapshot := onlySnapshot(t, firstRegistry, first)
	second := validDescriptor()
	second.ApprovalMode, second.ApprovalHint = ApprovalDirect, "Review the external target."
	secondRegistry, err := New([]Descriptor{second})
	if err != nil {
		t.Fatal(err)
	}
	secondSnapshot := onlySnapshot(t, secondRegistry, second)
	if firstSnapshot.Hash == secondSnapshot.Hash {
		t.Fatal("approval authority did not affect descriptor hash")
	}
	if _, err = secondRegistry.Resolve(secondSnapshot.SnapshotID, firstSnapshot.Hash); !errors.Is(err, ErrToolNotFound) {
		t.Fatalf("mismatched immutable hash resolved: %v", err)
	}
}

func TestRegistryRejectsRemoteRefsOpenSchemasAndInvalidRuntime(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Descriptor)
	}{
		{"remote ref", func(value *Descriptor) {
			value.InputSchema = json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"value":{"$ref":"https://attacker.invalid/schema"}},"additionalProperties":false}`)
		}},
		{"open input", func(value *Descriptor) {
			value.InputSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)
		}},
		{"tag only image", func(value *Descriptor) { value.RuntimeImage = "registry.invalid/tools/provider:latest" }},
		{"missing runtime policy binding", func(value *Descriptor) { value.RuntimePolicy = nil }},
		{"mutable workspace policy", func(value *Descriptor) { value.RuntimePolicy.WorkspaceMode = "read_write" }},
		{"egress authority mismatch", func(value *Descriptor) {
			value.RuntimePolicy.NetworkMode = "none"
			value.RuntimePolicy.NetworkPolicyHash = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
		}},
		{"ip egress", func(value *Descriptor) { value.EgressAllowlist = []string{"127.0.0.1"} }},
		{"effect mismatch", func(value *Descriptor) { value.EffectClass, value.RequiresEffectKey = "irreversible_write", false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validDescriptor()
			test.mutate(&value)
			if _, err := New([]Descriptor{value}); err == nil {
				t.Fatal("invalid descriptor accepted")
			}
		})
	}
}

func TestRegistryRejectsDuplicateSnapshotsAndReturnsDefensiveCopies(t *testing.T) {
	descriptor := validDescriptor()
	if _, err := New([]Descriptor{descriptor, descriptor}); !errors.Is(err, ErrRegistryInvalid) {
		t.Fatalf("duplicate error=%v", err)
	}
	registry, err := New([]Descriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := onlySnapshot(t, registry, descriptor)
	snapshot.Descriptor.RequiredPermissions[0] = "mutated.permission"
	snapshot.Descriptor.InputSchema[0] = '['
	snapshot.Descriptor.RuntimePolicy.SnapshotKey = "runtime-policy:mutated:v1"
	again, err := registry.Resolve(snapshot.SnapshotID, snapshot.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if again.Descriptor.RequiredPermissions[0] != "tools.read" || !strings.HasPrefix(string(again.Descriptor.InputSchema), "{") || again.Descriptor.RuntimePolicy.SnapshotKey != "runtime-policy:provider:v1" {
		t.Fatal("caller mutated immutable registry state")
	}
}

func onlySnapshot(t *testing.T, registry Registry, descriptor Descriptor) Snapshot {
	t.Helper()
	snapshots := registry.byName[descriptor.Name]
	if len(snapshots) != 1 {
		t.Fatalf("snapshots=%d", len(snapshots))
	}
	resolved, err := registry.Resolve(snapshots[0].SnapshotID, snapshots[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func validDescriptor() Descriptor {
	return Descriptor{
		SchemaVersion: 1, Name: "provider_lookup", Version: "1.2.3", DisplayName: "Provider lookup",
		Description: "Look up provider status without changing external state.", Category: "external_api",
		InputSchema:  json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":128},"limit":{"type":"integer","minimum":1,"maximum":10}},"required":["query","limit"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"items":{"type":"array","items":{"type":"string"},"maxItems":10}},"required":["items"],"additionalProperties":false}`),
		EffectClass:  "read_only", MaxAttempts: 5,
		RequiredPermissions: []string{"tools.read"}, ApprovalMode: ApprovalNone,
		ExecutionKind: ExecutionWorker, Handler: "provider_lookup", TrustTier: "semi_trusted",
		RuntimeImage:        "registry.invalid/langshift/provider-lookup@sha256:" + strings.Repeat("a", 64),
		RuntimePolicy:       testRuntimePolicy("allowlist_proxy", "sha256:"+strings.Repeat("b", 64)),
		Resources:           ResourceLimits{CPUMillis: 250, MemoryBytes: 128 << 20, DiskBytes: 256 << 20, Timeout: "30s", MaximumInput: 64 << 10, MaximumOutput: 1 << 20, MaximumLogBytes: 1 << 20},
		Scheduling:          Scheduling{QueueClass: "interactive", ResourceClass: "tool-network", Priority: 50, CostUnits: 2},
		NetworkEgressPolicy: "allowlist", EgressAllowlist: []string{"api.provider.invalid"},
		Source: "platform", Changelog: "Initial production descriptor.",
	}
}

func testRuntimePolicy(networkMode, networkHash string) *RuntimePolicyBinding {
	return &RuntimePolicyBinding{SnapshotKey: "runtime-policy:provider:v1", IsolationKind: "firecracker", NetworkMode: networkMode, NetworkPolicyHash: networkHash, SecretMode: "none", SecretScopeHash: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", WorkspaceMode: "none", MinimumPids: 32}
}
