package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigurationFailsClosedAndAcceptsExplicitLoopbackDevelopment(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "DATABASE_URL_FILE", "STORE_EPOCH_URL", "ALLOW_INSECURE_DEVELOPMENT", "ROLLBACK_ALLOWED_CLIENT_SPIFFE_ID"} {
		t.Setenv(name, "")
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("empty behavior control-plane configuration accepted")
	}
	values := map[string]string{
		"ALLOW_INSECURE_DEVELOPMENT": "true", "DATABASE_URL": "postgres://behavior@127.0.0.1/lites?sslmode=disable", "STORE_EPOCH_URL": "http://127.0.0.1:8080/v1/store-epoch",
		"TRUSTED_CONTEXT_KEYRING_FILE": "/tmp/trusted.json", "BEHAVIOR_SIGNING_KEYRING_FILE": "/tmp/behavior-keys.json", "BEHAVIOR_SECRET_BUNDLE_FILE": "/tmp/behavior-secrets.json",
		"BEHAVIOR_AUTOMATION_USER_ID": "10000000-0000-4000-8000-000000000001", "ROLLBACK_ALLOWED_CLIENT_SPIFFE_ID": "spiffe://lites.internal/behavior-rollback-controller",
		"S3_REGION": "us-east-1", "S3_PAYLOAD_BUCKET": "payloads", "VAULT_ADDR": "http://127.0.0.1:8200", "LITES_ENVIRONMENT": "test", "LITES_VERSION": "test", "LITES_REGION": "US",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	configuration, err := loadConfig()
	if err != nil || !configuration.allowInsecureDevelopment || configuration.adminAddress == configuration.rollbackAddress {
		t.Fatalf("loadConfig()=%#v err=%v", configuration, err)
	}
	t.Setenv("STORE_EPOCH_URL", "http://epoch.internal/v1/store-epoch")
	if _, err = loadConfig(); err == nil {
		t.Fatal("remote plaintext epoch endpoint accepted")
	}
	t.Setenv("STORE_EPOCH_URL", "http://127.0.0.1:8080/v1/store-epoch")
	t.Setenv("ROLLBACK_ALLOWED_CLIENT_SPIFFE_ID", "https://lites.internal/rollback")
	if _, err = loadConfig(); err == nil {
		t.Fatal("non-SPIFFE rollback identity accepted")
	}
}

func TestSecretBundleRequiresPurposeSeparatedMaterial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	material := func(value byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32)) }
	document := encodedSecretBundle{Version: "1.0.0", IDKey: material(1), IdempotencyPepper: material(2), RequestDigestPepper: material(3)}
	encoded, _ := json.Marshal(document)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSecretBundle(path)
	if err != nil || len(loaded.IDKey) != 32 || bytes.Equal(loaded.IDKey, loaded.IdempotencyPepper) {
		t.Fatalf("bundle=%#v err=%v", loaded, err)
	}
	document.RequestDigestPepper = document.IdempotencyPepper
	encoded, _ = json.Marshal(document)
	if err = os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadSecretBundle(path); err == nil {
		t.Fatal("purpose-reused secret material accepted")
	}
}

func TestBehaviorKeyringRequiresThreeActivePurposeSeparatedKeys(t *testing.T) {
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	document := behaviorKeyringDocument{Version: "1.0.0"}
	for index, purpose := range []string{"risk_owner", "release_owner", "rollback_automation"} {
		privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(index + 1)}, ed25519.SeedSize))
		document.Keys = append(document.Keys, behaviorKeyRecord{ID: purpose + "-2026", Purpose: purpose, PublicKey: base64.RawURLEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)})
	}
	path := filepath.Join(t.TempDir(), "keyring.json")
	writeKeyring := func() {
		encoded, _ := json.Marshal(document)
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeKeyring()
	keys, windows, purposes, err := loadBehaviorKeyring(path, now)
	if err != nil || len(keys) != 3 || len(windows) != 3 || purposes["rollback_automation-2026"] != "rollback_automation" {
		t.Fatalf("keys=%d windows=%d purposes=%v err=%v", len(keys), len(windows), purposes, err)
	}
	document.Keys[2].NotAfter = now
	writeKeyring()
	if _, _, _, err = loadBehaviorKeyring(path, now); err == nil {
		t.Fatal("keyring without active rollback automation key accepted")
	}
}
