package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigurationFailsClosedAndAllowsExplicitLoopbackDevelopment(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "DATABASE_URL_FILE", "STORE_EPOCH_URL", "ALLOW_INSECURE_DEVELOPMENT"} {
		t.Setenv(name, "")
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("empty product service configuration accepted")
	}
	values := map[string]string{
		"ALLOW_INSECURE_DEVELOPMENT": "true", "DATABASE_URL": "postgres://product@127.0.0.1/lites?sslmode=disable", "STORE_EPOCH_URL": "http://127.0.0.1:8080/v1/store-epoch",
		"TRUSTED_CONTEXT_KEYRING_FILE": "/tmp/trusted.json", "PRODUCT_SECRET_BUNDLE_FILE": "/tmp/product-secrets.json",
		"IDENTITY_PUBLIC_TENANT_ID": "00000000-0000-4000-8000-000000000099",
		"S3_REGION":                 "us-east-1", "S3_PAYLOAD_BUCKET": "payloads", "S3_ARTIFACT_BUCKET": "artifacts", "VAULT_ADDR": "http://127.0.0.1:8200", "LITES_ENVIRONMENT": "test", "LITES_VERSION": "test", "LITES_REGION": "US",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	configuration, err := loadConfig()
	if err != nil || !configuration.allowInsecureDevelopment || configuration.listenAddress == configuration.healthAddress {
		t.Fatalf("loadConfig()=%#v err=%v", configuration, err)
	}
	t.Setenv("STORE_EPOCH_URL", "http://epoch.internal/v1/store-epoch")
	if _, err = loadConfig(); err == nil {
		t.Fatal("remote plaintext epoch endpoint accepted")
	}
}

func TestProductionConfigurationRequiresAuthenticatedFileBackedDependencies(t *testing.T) {
	databaseFile := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(databaseFile, []byte("postgres://product:secret@db.internal/lites\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"ALLOW_INSECURE_DEVELOPMENT": "false", "DATABASE_URL": "", "DATABASE_URL_FILE": databaseFile,
		"SERVER_TLS_CERT_FILE": "/run/tls/tls.crt", "SERVER_TLS_KEY_FILE": "/run/tls/tls.key", "SERVER_CLIENT_CA_FILE": "/run/tls/client-ca.crt",
		"TRUSTED_CONTEXT_KEYRING_FILE": "/run/config/trusted.json", "PRODUCT_SECRET_BUNDLE_FILE": "/run/secrets/product.json",
		"IDENTITY_PUBLIC_TENANT_ID": "00000000-0000-4000-8000-000000000099",
		"STORE_EPOCH_URL":           "https://store-epoch.internal/v1/store-epoch", "STORE_EPOCH_ROOT_CA_FILE": "/run/tls/ca.crt", "STORE_EPOCH_TOKEN_FILE": "/run/secrets/epoch-token",
		"S3_REGION": "us-east-1", "S3_PAYLOAD_BUCKET": "payloads", "S3_ARTIFACT_BUCKET": "artifacts", "VAULT_ADDR": "https://vault.internal", "VAULT_CACERT": "/run/tls/ca.crt", "VAULT_TOKEN_FILE": "/run/secrets/vault-token",
		"LITES_ENVIRONMENT": "production", "LITES_VERSION": "test", "LITES_REGION": "US", "OTLP_GRPC_ENDPOINT": "otel.internal:4317", "OTLP_BEARER_TOKEN_FILE": "/run/secrets/otel-token",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	configuration, err := loadConfig()
	if err != nil || configuration.databaseURL == "" || configuration.trustedAudience != "product-service" {
		t.Fatalf("loadConfig()=%#v err=%v", configuration, err)
	}
	t.Setenv("SERVER_CLIENT_CA_FILE", "")
	if _, err = loadConfig(); err == nil {
		t.Fatal("production configuration without inbound client CA accepted")
	}
}

func TestSecretBundleRequiresPurposeSeparatedMaterial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	material := func(value byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32)) }
	document := encodedSecretBundle{Version: "1.0.0", IDKey: material(1), IdempotencyPepper: material(2), RequestDigestPepper: material(3), CursorKey: material(4)}
	write := func() {
		encoded, _ := json.Marshal(document)
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	loaded, err := loadSecretBundle(path)
	if err != nil || len(loaded.IDKey) != 32 || bytes.Equal(loaded.IDKey, loaded.IdempotencyPepper) {
		t.Fatalf("bundle=%#v err=%v", loaded, err)
	}
	document.RequestDigestPepper = document.IdempotencyPepper
	write()
	if _, err = loadSecretBundle(path); err == nil {
		t.Fatal("purpose-reused secret material accepted")
	}
}
