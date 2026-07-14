package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifestVerifiesSourcesAndStripsOuterTransaction(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	sql := []byte("BEGIN;\nSELECT 1;\nCOMMIT;\n")
	digest := sha256.Sum256(sql)
	if err := os.WriteFile(filepath.Join(root, "migrations", "one.sql"), sql, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"manifest_version":"1.0.0","migrations":[{"version":1,"name":"one","up":"migrations/one.sql","up_sha256":"%s","reversible":false}]}`, hex.EncodeToString(digest[:]))
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(root, "manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Migrations[0].upSQL != "SELECT 1;" {
		t.Fatalf("unexpected normalized SQL %q", loaded.Migrations[0].upSQL)
	}
}

func TestLoadManifestRejectsTraversalAndChecksumDrift(t *testing.T) {
	root := t.TempDir()
	manifest := `{"manifest_version":"1.0.0","migrations":[{"version":1,"name":"one","up":"../outside.sql","up_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","reversible":false}]}`
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(root, "manifest.json"); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestRollbackPlanRejectsIrreversibleRangeBeforeReturningWork(t *testing.T) {
	migrations := []Migration{{Version: 1, Reversible: false}, {Version: 2, Reversible: true}}
	if plan, err := rollbackPlan(migrations, 2, 2); err == nil || len(plan) != 0 {
		t.Fatalf("irreversible range returned partial plan: %#v err=%v", plan, err)
	}
	plan, err := rollbackPlan(migrations, 2, 1)
	if err != nil || len(plan) != 1 || plan[0].Version != 2 {
		t.Fatalf("reversible plan=%#v err=%v", plan, err)
	}
}
