package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToolReconciliationWorkerConfigurationFailsClosedAndAcceptsDevelopmentFixture(t *testing.T) {
	for _, name := range []string{
		"ALLOW_INSECURE_DEVELOPMENT", "DATABASE_URL", "DATABASE_URL_FILE", "STORE_EPOCH_URL",
		"NATS_URLS", "TOOL_REGISTRY_ARTIFACT_FILE", "TOOL_REGISTRY_ARTIFACT_HASH",
		"TOOL_RECONCILIATION_ADAPTERS_FILE", "TOOL_RECONCILIATION_WORKER_ID", "EXECUTION_ID_KEY_FILE",
		"EXECUTION_LEASE_PEPPER_FILE", "S3_REGION", "S3_PAYLOAD_BUCKET", "VAULT_ADDR",
		"HEALTH_ADDRESS", "LITES_ENVIRONMENT", "LITES_VERSION", "LITES_REGION", "OTLP_GRPC_ENDPOINT",
	} {
		t.Setenv(name, "")
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("empty configuration accepted")
	}
	values := map[string]string{
		"ALLOW_INSECURE_DEVELOPMENT":        "true",
		"DATABASE_URL":                      "postgres://reconciler@127.0.0.1/lites?sslmode=disable",
		"STORE_EPOCH_URL":                   "http://127.0.0.1:8080/v1/store-epoch",
		"NATS_URLS":                         "nats://127.0.0.1:4222",
		"TOOL_REGISTRY_ARTIFACT_FILE":       "/tmp/tool-registry.json",
		"TOOL_REGISTRY_ARTIFACT_HASH":       "sha256:release",
		"TOOL_RECONCILIATION_ADAPTERS_FILE": "/tmp/reconciliation-adapters.json",
		"TOOL_RECONCILIATION_WORKER_ID":     "reconciler-test",
		"EXECUTION_ID_KEY_FILE":             "/tmp/id-key",
		"EXECUTION_LEASE_PEPPER_FILE":       "/tmp/lease-pepper",
		"S3_REGION":                         "us-east-1",
		"S3_PAYLOAD_BUCKET":                 "payloads",
		"VAULT_ADDR":                        "http://127.0.0.1:8200",
		"HEALTH_ADDRESS":                    "127.0.0.1:8091",
		"LITES_ENVIRONMENT":                 "test",
		"LITES_VERSION":                     "test",
		"LITES_REGION":                      "US",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	configuration, err := loadConfig()
	if err != nil || configuration.maximumRounds != 8 || configuration.shardCount != 16 || !configuration.allowInsecure {
		t.Fatalf("loadConfig()=%#v err=%v", configuration, err)
	}
	t.Setenv("TOOL_RECONCILIATION_MAX_ROUNDS", "0")
	if _, err = loadConfig(); err == nil {
		t.Fatal("zero reconciliation rounds accepted")
	}
}

func TestDatabaseURLSecretSourceIsExclusiveAndBounded(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "database-url")
	if err := os.WriteFile(path, []byte("postgres://service@database/lites?sslmode=verify-full\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := loadDatabaseURL("", path)
	if err != nil || value != "postgres://service@database/lites?sslmode=verify-full" {
		t.Fatalf("loadDatabaseURL()=%q err=%v", value, err)
	}
	if _, err = loadDatabaseURL("postgres://direct", path); err == nil {
		t.Fatal("multiple database URL sources accepted")
	}
	if err = os.WriteFile(path, []byte("postgres://first\npostgres://second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadDatabaseURL("", path); err == nil {
		t.Fatal("multi-line database secret accepted")
	}
}
