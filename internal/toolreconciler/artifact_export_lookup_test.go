package toolreconciler

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/langshift/lites/internal/objectstore/s3store"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/toolregistry"
)

type artifactInspectorStub struct {
	object s3store.VersionedObject
	err    error
	key    string
}

func (stub *artifactInspectorStub) InspectScannedVersioned(_ context.Context, key, _, _ string, _ int64) (s3store.VersionedObject, error) {
	stub.key = key
	return stub.object, stub.err
}

func TestArtifactExportLookupConfirmsOnlyExactScannedVersion(t *testing.T) {
	registry, snapshot := artifactLookupRegistry(t)
	payloads := &memoryPayloads{}
	contents := []byte("portfolio-pdf")
	digest := sha256.Sum256(contents)
	contentHash := hex.EncodeToString(digest[:])
	input := json.RawMessage(`{"schema_version":1,"target_kind":"portfolio_export","target_id":"a7000000-0000-4000-8000-000000000001","media_type":"application/pdf","content_base64":"` + base64.StdEncoding.EncodeToString(contents) + `","content_sha256":"` + contentHash + `"}`)
	normalized, _, requestHash, err := registry.NormalizeInput(snapshot.SnapshotID, snapshot.Hash, input)
	if err != nil {
		t.Fatal(err)
	}
	command := artifactLookupCommand(snapshot, requestHash)
	command.Input, err = payloads.Put(t.Context(), payload.Descriptor{TenantID: command.TenantID, ObjectID: command.ToolCallID, Class: "tool-normalized-input", ContentType: "application/json"}, normalized)
	if err != nil {
		t.Fatal(err)
	}
	inspector := &artifactInspectorStub{object: s3store.VersionedObject{Reference: "s3://lites-artifacts/commercial/exact.pdf", VersionID: "v1", ContentHash: contentHash, MediaType: "application/pdf", ByteSize: int64(len(contents)), ScanResultHash: strings.Repeat("b", 64)}}
	result, err := (ArtifactExportLookup{Payloads: payloads, Objects: inspector, MaxBytes: 11 << 20}).Lookup(t.Context(), LookupRequest{Command: command, Snapshot: snapshot})
	if err != nil || result.Disposition != LookupConfirmed || result.ExternalResourceRef != inspector.object.Reference || inspector.key != command.TenantID+"/portfolio-exports/a7000000-0000-4000-8000-000000000001/"+command.ProviderRequestID+".pdf" {
		t.Fatalf("result=%#v key=%q err=%v", result, inspector.key, err)
	}
	var evidence artifactExportLookupEvidence
	if json.Unmarshal(result.Evidence, &evidence) != nil || evidence.ObjectVersion != "v1" || evidence.ScanResultHash != inspector.object.ScanResultHash {
		t.Fatalf("evidence=%s", result.Evidence)
	}

	inspector.err = s3store.ErrNotFound
	result, err = (ArtifactExportLookup{Payloads: payloads, Objects: inspector, MaxBytes: 11 << 20}).Lookup(t.Context(), LookupRequest{Command: command, Snapshot: snapshot})
	if err != nil || result.Disposition != LookupNotApplied || result.ExternalResourceRef != "" {
		t.Fatalf("not applied result=%#v err=%v", result, err)
	}

	command.RequestHash = strings.Repeat("f", 64)
	if _, err = (ArtifactExportLookup{Payloads: payloads, Objects: inspector, MaxBytes: 11 << 20}).Lookup(t.Context(), LookupRequest{Command: command, Snapshot: snapshot}); !errors.Is(err, ErrArtifactExportLookup) {
		t.Fatalf("unbound input err=%v", err)
	}
}

func artifactLookupRegistry(t *testing.T) (toolregistry.Registry, toolregistry.Snapshot) {
	t.Helper()
	descriptor := toolregistry.Descriptor{
		SchemaVersion: 1, Name: "artifact_export", Version: "1.0.0", DisplayName: "Artifact export", Description: "Export one portfolio Artifact.", Category: "artifact",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"schema_version":{"const":1},"target_kind":{"const":"portfolio_export"},"target_id":{"type":"string"},"media_type":{"enum":["application/pdf"]},"content_base64":{"type":"string"},"content_sha256":{"type":"string"}},"required":["schema_version","target_kind","target_id","media_type","content_base64","content_sha256"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`),
		EffectClass:  "reconcilable_write", RequiresEffectKey: true, SupportsReconcile: true, MaxAttempts: 5, ReconcileAfter: "1m", RequiredPermissions: []string{"private_work.update"}, ApprovalMode: toolregistry.ApprovalNone,
		ExecutionKind: toolregistry.ExecutionWorker, Handler: "artifact_export", TrustTier: "trusted", RuntimeImage: "registry.example/tools/tool-worker@sha256:" + strings.Repeat("a", 64), Resources: toolregistry.ResourceLimits{CPUMillis: 100, MemoryBytes: 64 << 20, Timeout: "5m", MaximumInput: 16 << 20, MaximumOutput: 16 << 10, MaximumLogBytes: 1 << 20}, Scheduling: toolregistry.Scheduling{QueueClass: "background", ResourceClass: "artifact-export", Priority: 60, CostUnits: 4}, NetworkEgressPolicy: "deny_all", Source: "platform",
	}
	registry, err := toolregistry.New([]toolregistry.Descriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	return registry, registry.Snapshots()[0]
}

func artifactLookupCommand(snapshot toolregistry.Snapshot, requestHash string) CommandPayload {
	return CommandPayload{SchemaVersion: 1, TenantID: "a7000000-0000-4000-8000-000000000010", RunID: "a7000000-0000-4000-8000-000000000011", ToolCallID: "a7000000-0000-4000-8000-000000000012", EffectID: "a7000000-0000-4000-8000-000000000013", ToolName: "artifact_export", DescriptorSnapshotID: snapshot.SnapshotID, DescriptorHash: snapshot.Hash, EffectClass: "reconcilable_write", EffectKey: requestHash, EffectScope: "tenant:a7", ProviderID: "artifact_export", ProviderRequestID: "a7000000-0000-4000-8000-000000000014", RequestHash: requestHash}
}
