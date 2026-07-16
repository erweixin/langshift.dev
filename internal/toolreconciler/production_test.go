package toolreconciler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/toolregistry"
)

func TestProductionRuntimePinsRegistryAndRequiresLookupCoverage(t *testing.T) {
	registry, snapshot := reconciliationRegistry(t)
	descriptor := registry.Snapshots()[0].Descriptor
	encoded, fileHash, err := toolregistry.EncodeArtifact([]toolregistry.Descriptor{descriptor}, strings.Repeat("a", 40), time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "registry.json")
	if err = os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := ProductionConfig{ArtifactPath: path, ArtifactFileHash: fileHash, Payloads: &memoryPayloads{}, Store: &reconciliationStoreStub{}, ConsumerName: "tool-reconciliation-worker", WorkerID: "worker-1", Actor: json.RawMessage(`{"kind":"service"}`), IDKey: testIDKey(), HeartbeatInterval: time.Second, MaximumCommand: 1 << 20, MaximumEvidence: 1 << 20, MaximumRounds: 5, RetryDelay: time.Minute, MaximumRetryDelay: time.Hour, Resume: Schedule{QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 4, MaxAttempts: 5}, Retry: Schedule{QueueClass: "background", ResourceClass: "tool-reconciliation", Priority: 40, CostUnits: 1, MaxAttempts: 20}, Metrics: &reconciliationMetricsStub{}}
	if _, err = NewProductionRuntime(configuration); !errors.Is(err, ErrProductionConfiguration) || !errors.Is(err, ErrLookupConfiguration) {
		t.Fatalf("missing lookup coverage error=%v", err)
	}
	configuration.LookupRegistrations = []LookupRegistration{{Name: descriptor.Handler, Lookup: effectLookupFunc(func(context.Context, LookupRequest) (LookupResult, error) { return LookupResult{}, nil })}}
	runtime, err := NewProductionRuntime(configuration)
	if err != nil || runtime.Artifact.RegistryHash != registry.Hash() || runtime.Handler.Lookup == nil {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
	resolved, err := runtime.Artifact.Registry.Resolve(snapshot.SnapshotID, snapshot.Hash)
	if err != nil || resolved.Hash != snapshot.Hash {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
}
