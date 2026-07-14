package migrations

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestProductionManifestIsSequentialAndContentAddressed(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	manifest, err := LoadManifest(root, "deploy/migrations/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Migrations) != 22 {
		t.Fatalf("production migration count=%d, want 22", len(manifest.Migrations))
	}
	latest := manifest.Migrations[len(manifest.Migrations)-1]
	if latest.Version != 22 || latest.Name != "workspace_publish_heartbeat" || !latest.Reversible {
		t.Fatalf("unexpected latest production migration: %#v", latest)
	}
}
