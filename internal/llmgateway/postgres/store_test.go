package postgres

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestContextManifestIsCanonicalAndCandidateSetIsExact(t *testing.T) {
	manifest := ContextManifest{
		SchemaVersion:   1,
		Run:             SnapshotBinding{ID: "run", Version: 3, Hash: "run-hash"},
		Messages:        []SnapshotBinding{{ID: "message", Version: 1, Hash: "message-hash"}},
		Router:          SnapshotBinding{ID: "router", Version: 2, Hash: "router-hash"},
		Budget:          SnapshotBinding{ID: "budget", Version: 1, Hash: "budget-hash"},
		Policy:          SnapshotBinding{ID: "policy", Version: 4, Hash: "policy-hash"},
		CandidateModels: []ModelCandidate{{ProviderID: "openai", ModelID: "gpt", ModelVersion: "v1", BoundHost: "api.openai.com", PricingVersion: "price-v1"}},
	}
	first, firstHash, err := canonicalManifest(manifest)
	if err != nil || len(first) == 0 || len(firstHash) != 64 {
		t.Fatalf("canonical manifest hash=%q err=%v", firstHash, err)
	}
	second, secondHash, err := canonicalManifest(manifest)
	if err != nil || !bytes.Equal(first, second) || firstHash != secondHash {
		t.Fatalf("manifest encoding is not deterministic")
	}
	if !containsCandidate(manifest, manifest.CandidateModels[0]) || containsCandidate(manifest, ModelCandidate{ProviderID: "openai", ModelID: "gpt", ModelVersion: "v2", BoundHost: "api.openai.com", PricingVersion: "price-v1"}) {
		t.Fatal("candidate binding was not exact")
	}
	retrievalManifest := manifest
	retrievalManifest.SchemaVersion = 2
	retrievalManifest.Retrieval = &RetrievalBinding{ManifestID: "retrieval-manifest"}
	if _, _, err = canonicalManifest(retrievalManifest); err != nil {
		t.Fatalf("v2 retrieval-bound manifest rejected: %v", err)
	}
	missingRetrieval := manifest
	missingRetrieval.SchemaVersion = 2
	if _, _, err = canonicalManifest(missingRetrieval); err == nil {
		t.Fatal("v2 manifest without retrieval binding accepted")
	}
	invalid := manifest
	invalid.CandidateModels = append(invalid.CandidateModels, invalid.CandidateModels[0])
	if _, _, err = canonicalManifest(invalid); err == nil {
		t.Fatal("duplicate provider model candidate accepted")
	}
	invalid = manifest
	invalid.CandidateModels[0].BoundHost = "API.OPENAI.COM"
	if _, _, err = canonicalManifest(invalid); err == nil {
		t.Fatal("non-canonical provider host accepted")
	}
}

func TestProviderResultContractsSeparateKnownAndUnknownOutcomes(t *testing.T) {
	base := RecordProviderResultCommand{AttemptID: "attempt", TenantID: "tenant", CompletionToken: "token", CorrelationID: "correlation", Actor: json.RawMessage(`{"kind":"service"}`), RecordedEvent: PayloadPointer{Ref: "ref", Hash: "hash"}, SettledEvent: PayloadPointer{Ref: "settled-ref", Hash: "settled-hash"}}
	completed := base
	completed.Status, completed.ResponseHash, completed.UsageStatus = "completed", "response", "confirmed"
	if !validResult(completed) {
		t.Fatal("known completion rejected")
	}
	unknown := base
	unknown.SettledEvent = PayloadPointer{}
	due := time.Now().Add(time.Hour)
	unknown.Status, unknown.UsageStatus, unknown.ErrorClass, unknown.ReconciliationDueAt = "outcome_unknown", "unknown", "deadline_exceeded", &due
	if !validResult(unknown) {
		t.Fatal("reconcilable unknown result rejected")
	}
	unknown.ResponseHash = "invented-response"
	if validResult(unknown) {
		t.Fatal("unknown result accepted invented response evidence")
	}
}
