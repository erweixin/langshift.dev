package agentworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/behavior"
	"github.com/langshift/lites/internal/llmgateway/provider"
)

func TestPromptArtifactPinsReleaseAndBehaviorBinding(t *testing.T) {
	platform := "You are the production coach."
	override := "You are the enterprise production coach."
	tenantID := "018f5b18-9235-4740-b79c-29449ad7bc1a"
	definitions := []PromptDefinition{
		{TenantID: tenantID, ID: "coach", Version: "2026.07.1", Content: override, Hash: sha256Prompt([]byte(override))},
		{TenantID: "*", ID: "coach", Version: "2026.07.1", Content: platform, Hash: sha256Prompt([]byte(platform))},
	}
	encoded, fileHash, err := EncodePromptArtifact(definitions, strings.Repeat("a", 40), time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "prompts.json")
	if err = os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := LoadPromptArtifact(path, fileHash)
	if err != nil || artifact.FileHash != fileHash || artifact.ArtifactID != "prompt-artifact-"+artifact.ArtifactHash {
		t.Fatalf("artifact=%#v error=%v", artifact, err)
	}
	content, err := artifact.LoadPrompt(context.Background(), tenantID, behavior.Binding{ID: "coach", Version: "2026.07.1", Hash: sha256Prompt([]byte(override))})
	if err != nil || content != override {
		t.Fatalf("content=%q error=%v", content, err)
	}
	content, err = artifact.LoadPrompt(context.Background(), "018f5b18-9235-4740-b79c-29449ad7bc1b", behavior.Binding{ID: "coach", Version: "2026.07.1", Hash: sha256Prompt([]byte(platform))})
	if err != nil || content != platform {
		t.Fatalf("fallback=%q error=%v", content, err)
	}
}

func TestPromptArtifactRejectsSubstitutionUnknownFieldsAndBindingDrift(t *testing.T) {
	content := "Pinned prompt"
	encoded, fileHash, err := EncodePromptArtifact([]PromptDefinition{{TenantID: "*", ID: "coach", Version: "v1", Content: content, Hash: sha256Prompt([]byte(content))}}, strings.Repeat("b", 40), time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "prompts.json")
	if err = os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadPromptArtifact(path, strings.Repeat("f", 64)); !errors.Is(err, ErrPromptArtifact) {
		t.Fatalf("substitution error=%v", err)
	}
	var document map[string]any
	if json.Unmarshal(encoded, &document) != nil {
		t.Fatal("decode artifact")
	}
	document["mutable_alias"] = "latest"
	tampered, _ := json.Marshal(document)
	if err = os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadPromptArtifact(path, sha256Prompt(tampered)); !errors.Is(err, ErrPromptArtifact) {
		t.Fatalf("unknown field error=%v", err)
	}
	if err = os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := LoadPromptArtifact(path, fileHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = artifact.LoadPrompt(context.Background(), "tenant", behavior.Binding{ID: "coach", Version: "v1", Hash: strings.Repeat("c", 64)}); !errors.Is(err, ErrContextAsset) {
		t.Fatalf("binding drift error=%v", err)
	}
}

func TestPromptArtifactEncodingCanonicalizesOrder(t *testing.T) {
	at := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	first := PromptDefinition{TenantID: "*", ID: "coach", Version: "v1", Content: "one", Hash: sha256Prompt([]byte("one"))}
	second := PromptDefinition{TenantID: "*", ID: "planner", Version: "v2", Content: "two", Hash: sha256Prompt([]byte("two"))}
	left, leftHash, err := EncodePromptArtifact([]PromptDefinition{first, second}, strings.Repeat("d", 40), at)
	if err != nil {
		t.Fatal(err)
	}
	right, rightHash, err := EncodePromptArtifact([]PromptDefinition{second, first}, strings.Repeat("d", 40), at)
	if err != nil || string(left) != string(right) || leftHash != rightHash {
		t.Fatalf("canonical encoding drift: %v", err)
	}
}

func TestConservativeTokenEstimatorRejectsInvalidAndCapsOversize(t *testing.T) {
	request := provider.Request{System: "system", Messages: []provider.Message{{Role: "user", Content: []provider.ContentBlock{{Type: "text", Text: "hello"}}}}, MaxOutputTokens: 128}
	estimator := ConservativeTokenEstimator{FixedOverhead: 100, MaximumTokens: 100000}
	estimate, err := estimator.Estimate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(request)
	if estimate != uint64(len(encoded))*6+100 || estimate <= uint64(len(encoded)) {
		t.Fatalf("estimate=%d bytes=%d", estimate, len(encoded))
	}
	estimator.MaximumTokens = estimate - 1
	if _, err = estimator.Estimate(context.Background(), request); !errors.Is(err, ErrTokenEstimate) {
		t.Fatalf("cap error=%v", err)
	}
	if _, err = estimator.Estimate(context.Background(), provider.Request{}); !errors.Is(err, ErrTokenEstimate) {
		t.Fatalf("invalid error=%v", err)
	}
}
