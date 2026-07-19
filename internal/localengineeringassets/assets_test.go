package localengineeringassets

import (
	"bytes"
	"testing"
	"time"

	"github.com/langshift/lites/internal/agentworker"
	"github.com/langshift/lites/internal/behavior"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/toolregistry"
)

func TestBundleAndBehaviorManifestShareEveryRuntimeBinding(t *testing.T) {
	at := time.Date(2026, time.July, 18, 10, 0, 0, 0, time.UTC)
	bundle, err := Build(at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = agentworker.DecodePromptArtifact(bundle.Files["prompts.json"], bundle.Manifest.Artifacts["prompts"].SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err = agentworker.DecodeRouteArtifact(bundle.Files["routes.json"], bundle.Manifest.Artifacts["routes"].SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err = toolregistry.DecodeArtifact(bundle.Files["tools.json"], bundle.Manifest.Artifacts["tools"].SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.LoadRegistry(bytes.NewReader(bundle.Files["providers.json"])); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []behavior.Profile{behavior.RoutePlanner, behavior.DailyPlanner, behavior.Coach, behavior.Evaluator, behavior.ArtifactBuilder} {
		manifest, manifestErr := BehaviorManifest(profile, at, "candidate")
		if manifestErr != nil {
			t.Fatal(manifestErr)
		}
		if _, manifestErr = manifest.SnapshotID(); manifestErr != nil || manifest.Model.Hash == "" || manifest.Prompt.Hash == "" || manifest.RouterPolicy.Hash == "" {
			t.Fatalf("profile=%s manifest=%#v error=%v", profile, manifest, manifestErr)
		}
	}
}
