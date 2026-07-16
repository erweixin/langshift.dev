package agentworker

import (
	"bytes"
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

func TestNewProductionRuntimeSharesPinnedArtifactsAcrossAgentPath(t *testing.T) {
	const sourceCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	generatedAt := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	promptContent := "You are the pinned production coach."
	promptBytes, promptHash, err := EncodePromptArtifact([]PromptDefinition{{TenantID: "*", ID: "coach", Version: "2026.07.1", Content: promptContent, Hash: sha256Prompt([]byte(promptContent))}}, sourceCommit, generatedAt)
	if err != nil {
		t.Fatal(err)
	}
	promptPath := writeProductionArtifact(t, directory, "prompts.json", promptBytes)
	routeBytes, routeHash, err := EncodeRouteArtifact([]RouteDefinition{testRouteDefinition([]RouteCandidateDefinition{{ProviderID: "openai", ModelID: "reasoning", ModelVersion: "2026-07-01", BoundHost: "api.openai.com", PricingVersion: "price-v1", CredentialMode: "managed"}})}, sourceCommit, generatedAt)
	if err != nil {
		t.Fatal(err)
	}
	routePath := writeProductionArtifact(t, directory, "routes.json", routeBytes)
	_, snapshot := agentToolRegistry(t, toolregistry.ApprovalNone, "read_only")
	descriptor := snapshot.Descriptor
	descriptor.RequiredPermissions = []string{"private_work.read"}
	toolBytes, toolHash, err := toolregistry.EncodeArtifact([]toolregistry.Descriptor{descriptor}, sourceCommit, generatedAt)
	if err != nil {
		t.Fatal(err)
	}
	toolPath := writeProductionArtifact(t, directory, "tools.json", toolBytes)
	configuration := ProductionConfig{
		Pool: &pgxpool.Pool{}, Payloads: &memoryPayloads{}, Runs: &runStoreStub{}, Attempts: &attemptStoreStub{}, Billing: &billingStub{}, Gateway: &gatewayStub{}, Providers: testProviderRegistry(t, true),
		PromptArtifactPath: promptPath, PromptArtifactHash: promptHash, RouteArtifactPath: routePath, RouteArtifactHash: routeHash, ToolArtifactPath: toolPath, ToolArtifactHash: toolHash,
		ConsumerName: "agent-worker-v1", WorkerID: "agent-worker-az1-1", Actor: json.RawMessage(`{"kind":"service","name":"agent-worker"}`), IDKey: bytes.Repeat([]byte{0x91}, 32),
		MaximumOutputTokens: 4096, MaximumCommand: 1 << 20, MaximumMessageBytes: 4 << 20, MaximumMessages: 4096, MaximumCalls: 16,
		HeartbeatInterval: 10 * time.Second, PrepareTTL: time.Minute, CompletionTTL: 5 * time.Minute, ReconciliationDelay: time.Minute, ApprovalTTL: 30 * time.Minute, BucketTTL: 10 * time.Minute,
		Metrics:      &agentMetricsStub{},
		Notification: PlanSchedule{QueueClass: "interactive", ResourceClass: "notification", Priority: 50, CostUnits: 1, MaxAttempts: 10},
	}
	runtime, err := NewProductionRuntime(configuration)
	if err != nil || runtime.PromptArtifact.FileHash != promptHash || runtime.RouteArtifact.FileHash != routeHash || runtime.ToolArtifact.FileHash != toolHash || runtime.Handler.Runner == nil {
		t.Fatalf("runtime=%#v error=%v", runtime, err)
	}
	configuration.RouteArtifactHash = strings.Repeat("f", 64)
	if _, err = NewProductionRuntime(configuration); !errors.Is(err, ErrProductionConfiguration) {
		t.Fatalf("substituted artifact error=%v", err)
	}
}

func writeProductionArtifact(t *testing.T, directory, name string, value []byte) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, value, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
