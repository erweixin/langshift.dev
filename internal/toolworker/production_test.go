package toolworker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/toolregistry"
)

func TestProductionRuntimeSharesContentPinnedRegistry(t *testing.T) {
	descriptor := productionDescriptor()
	encoded, fileHash, err := toolregistry.EncodeArtifact([]toolregistry.Descriptor{descriptor}, strings.Repeat("a", 40), time.Date(2026, time.July, 16, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "registry.json")
	if err = os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	payloads := &memoryPayloads{}
	tools := &toolStoreStub{}
	runtime, err := NewProductionRuntime(ProductionConfig{
		ArtifactPath: path, ArtifactFileHash: fileHash, Pool: &pgxpool.Pool{}, Payloads: payloads, Tools: tools,
		SandboxBroker: &sandboxBrokerStub{},
		ConsumerName:  "tool-worker", WorkerID: "tool-worker-1", Actor: json.RawMessage(`{"kind":"service","name":"tool-worker"}`),
		IDKey: []byte("0123456789abcdef0123456789abcdef"), HeartbeatInterval: time.Second,
		Resume:                Schedule{QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 4, MaxAttempts: 5},
		Reconcile:             Schedule{QueueClass: "background", ResourceClass: "tool-reconciliation", Priority: 40, CostUnits: 1, MaxAttempts: 8},
		DefaultReconcileAfter: time.Minute,
	})
	if err != nil || runtime.Artifact.RegistryHash == "" || runtime.Handler.Executor == nil || runtime.Handler.Policy == nil {
		t.Fatalf("runtime=%#v error=%v", runtime, err)
	}
	executor := runtime.Handler.Executor.(DispatchExecutor)
	sandbox := executor.Sandbox.(*SandboxExecutor)
	policy := runtime.Handler.Policy.(PostgresPolicyEvaluator)
	if sandbox.Registry.Hash() != runtime.Artifact.RegistryHash || policy.Registry.Hash() != runtime.Artifact.RegistryHash || sandbox.Registry != policy.Registry || executor.Registry != policy.Registry {
		t.Fatal("executor and policy evaluator did not share one registry instance")
	}
}

func TestProductionRuntimeFailsClosedOnArtifactOrHandlerDrift(t *testing.T) {
	configuration := ProductionConfig{Pool: &pgxpool.Pool{}, Payloads: &memoryPayloads{}, Tools: &toolStoreStub{}}
	if _, err := NewProductionRuntime(configuration); !errors.Is(err, ErrProductionConfiguration) {
		t.Fatalf("missing artifact error=%v", err)
	}
	descriptor := productionDescriptor()
	encoded, fileHash, err := toolregistry.EncodeArtifact([]toolregistry.Descriptor{descriptor}, strings.Repeat("b", 40), time.Date(2026, time.July, 16, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "registry.json")
	if os.WriteFile(path, encoded, 0o600) != nil {
		t.Fatal("write artifact")
	}
	configuration.ArtifactPath, configuration.ArtifactFileHash = path, fileHash
	configuration.DefaultReconcileAfter = time.Minute
	if _, err = NewProductionRuntime(configuration); !errors.Is(err, ErrProductionConfiguration) || !errors.Is(err, ErrSandboxConfiguration) {
		t.Fatalf("missing sandbox coverage error=%v", err)
	}
}

func productionDescriptor() toolregistry.Descriptor {
	return toolregistry.Descriptor{
		SchemaVersion: 1, Name: "provider_lookup", Version: "1.0.0", DisplayName: "Provider lookup",
		Description: "Looks up provider state.", Category: "provider",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
		EffectClass:  "read_only", MaxAttempts: 3, ApprovalMode: toolregistry.ApprovalNone,
		ExecutionKind: toolregistry.ExecutionWorker, Handler: "provider_lookup", TrustTier: "semi_trusted",
		RuntimeImage:        "registry.example/tools/provider@sha256:" + strings.Repeat("a", 64),
		RuntimePolicy:       &toolregistry.RuntimePolicyBinding{SnapshotKey: "runtime-policy:provider:v1", IsolationKind: "firecracker", NetworkMode: "none", NetworkPolicyHash: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", SecretMode: "none", SecretScopeHash: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", WorkspaceMode: "none", MinimumPids: 32},
		Resources:           toolregistry.ResourceLimits{CPUMillis: 250, MemoryBytes: 128 << 20, DiskBytes: 64 << 20, Timeout: "30s", MaximumInput: 64 << 10, MaximumOutput: 1 << 20, MaximumLogBytes: 1 << 20},
		Scheduling:          toolregistry.Scheduling{QueueClass: "interactive", ResourceClass: "tool-network", Priority: 50, CostUnits: 2},
		NetworkEgressPolicy: "deny_all", Source: "platform", Changelog: "Initial production descriptor.",
	}
}
