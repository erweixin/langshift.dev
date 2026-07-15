package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProductionPolicyCoversExecutionResourceClasses(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "..", "deploy", "scheduler", "production-policy.json")
	policy, resources, err := loadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"llm", "tool-network", "tool-reconciliation", "runtime-untrusted", "workspace-publisher", "notification", "run-cancellation", "run-cancellation-propagation", "child-group-cancellation"} {
		if _, exists := policy.Resources[required]; !exists {
			t.Fatalf("production policy missing %s", required)
		}
	}
	if len(resources) != len(policy.Resources) || policy.BatchLimit != 1000 {
		t.Fatalf("resources=%d policy=%d batch=%d", len(resources), len(policy.Resources), policy.BatchLimit)
	}
}

func TestLoadPolicyRejectsUnknownAndTrailingFields(t *testing.T) {
	for _, body := range []string{
		`{"resources":{},"default_tenant":{},"tenants":{},"batch_limit":1,"unknown":true}`,
		`{"resources":{},"default_tenant":{},"tenants":{},"batch_limit":1}{}`,
	} {
		path := filepath.Join(t.TempDir(), "policy.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadPolicy(path); err == nil {
			t.Fatal("invalid policy was accepted")
		}
	}
}
