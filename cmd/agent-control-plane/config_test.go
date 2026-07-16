package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentControlConfigurationFailsClosedAndAllowsExplicitLoopbackDevelopment(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "DATABASE_URL_FILE", "STORE_EPOCH_URL", "ALLOW_INSECURE_DEVELOPMENT", "TRUSTED_CONTEXT_KEYRING_FILE", "AGENT_CONTROL_SECRET_BUNDLE_FILE"} {
		t.Setenv(name, "")
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("empty agent control-plane configuration accepted")
	}
	values := map[string]string{
		"ALLOW_INSECURE_DEVELOPMENT": "true", "DATABASE_URL": "postgres://agent@127.0.0.1/lites?sslmode=disable",
		"STORE_EPOCH_URL": "http://127.0.0.1:8080/v1/store-epoch", "TRUSTED_CONTEXT_KEYRING_FILE": "/tmp/trusted.json",
		"AGENT_CONTROL_SECRET_BUNDLE_FILE": "/tmp/agent-control-secrets.json", "S3_REGION": "us-east-1", "S3_PAYLOAD_BUCKET": "payloads",
		"VAULT_ADDR": "http://127.0.0.1:8200", "LITES_ENVIRONMENT": "test", "LITES_VERSION": "test", "LITES_REGION": "US",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	configuration, err := loadConfig()
	if err != nil || !configuration.allowInsecureDevelopment || configuration.runMaxSteps != 64 || configuration.runMaxCostMicrounits != 500000 {
		t.Fatalf("configuration=%#v error=%v", configuration, err)
	}
	t.Setenv("DATABASE_MAX_CONNECTIONS", "7")
	if _, err = loadConfig(); err == nil {
		t.Fatal("undersized production connection pool accepted")
	}
}

func TestAgentControlSecretBundleRequiresPurposeSeparation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	material := func(value byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32)) }
	document := encodedSecretBundle{Version: "1.0.0", IDKey: material(1), IdempotencyPepper: material(2), RequestDigestPepper: material(3), LeaseTokenPepper: material(4)}
	write := func() {
		encoded, _ := json.Marshal(document)
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	loaded, err := loadSecretBundle(path)
	if err != nil || len(loaded.IDKey) != 32 || len(loaded.LeaseTokenPepper) != 32 {
		t.Fatalf("bundle=%#v error=%v", loaded, err)
	}
	document.LeaseTokenPepper = document.IDKey
	write()
	if _, err = loadSecretBundle(path); err == nil {
		t.Fatal("purpose-reused lease token material accepted")
	}
}
