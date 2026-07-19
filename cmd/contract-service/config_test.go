package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContractConfigurationFailsClosedAndAllowsOnlyLoopbackPlaintextDevelopment(t *testing.T) {
	clearContractEnvironment(t)
	if _, err := loadConfig(); err == nil {
		t.Fatal("empty contract service configuration accepted")
	}
	values := map[string]string{
		"ALLOW_INSECURE_DEVELOPMENT":   "true",
		"DATABASE_URL":                 "postgres://lites_contract_service@127.0.0.1/lites?sslmode=disable",
		"LISTEN_ADDRESS":               "127.0.0.1:8443",
		"STORE_EPOCH_URL":              "http://127.0.0.1:8080/v1/store-epoch",
		"TRUSTED_CONTEXT_KEYRING_FILE": "/tmp/trusted.json",
		"CONTRACT_SECRET_BUNDLE_FILE":  "/tmp/contract-secrets.json",
		"S3_REGION":                    "us-east-1",
		"S3_PAYLOAD_BUCKET":            "payloads",
		"VAULT_ADDR":                   "http://127.0.0.1:8200",
		"LITES_ENVIRONMENT":            "test",
		"LITES_VERSION":                "test",
		"LITES_REGION":                 "US",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	configuration, err := loadConfig()
	if err != nil || configuration.trustedAudience != "contract-service" || configuration.proposalTTL != 5*time.Minute {
		t.Fatalf("loadConfig()=%#v err=%v", configuration, err)
	}
	t.Setenv("LISTEN_ADDRESS", ":8443")
	if _, err = loadConfig(); err == nil {
		t.Fatal("non-loopback plaintext listener accepted")
	}
	t.Setenv("LISTEN_ADDRESS", "127.0.0.1:8443")
	t.Setenv("STORE_EPOCH_URL", "http://epoch.internal/v1/store-epoch")
	if _, err = loadConfig(); err == nil {
		t.Fatal("remote plaintext epoch endpoint accepted")
	}
	t.Setenv("STORE_EPOCH_URL", "http://127.0.0.1:8080/v1/store-epoch")
	t.Setenv("CONTRACT_PROPOSAL_TTL", "6m")
	if _, err = loadConfig(); err == nil {
		t.Fatal("non-frozen proposal TTL accepted")
	}
}

func TestContractProductionConfigurationRequiresRoleIsolatedFileBackedTLSDependencies(t *testing.T) {
	clearContractEnvironment(t)
	databaseFile := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(databaseFile, []byte("postgres://lites_contract_service:secret@db.internal/lites\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"ALLOW_INSECURE_DEVELOPMENT": "false", "DATABASE_URL_FILE": databaseFile,
		"SERVER_TLS_CERT_FILE": "/run/tls/tls.crt", "SERVER_TLS_KEY_FILE": "/run/tls/tls.key", "SERVER_CLIENT_CA_FILE": "/run/tls/client-ca.crt",
		"TRUSTED_CONTEXT_KEYRING_FILE": "/run/config/trusted.json", "CONTRACT_SECRET_BUNDLE_FILE": "/run/secrets/contracts.json",
		"STORE_EPOCH_URL": "https://store-epoch.internal/v1/store-epoch", "STORE_EPOCH_ROOT_CA_FILE": "/run/tls/ca.crt", "STORE_EPOCH_TOKEN_FILE": "/run/secrets/epoch-token",
		"S3_REGION": "us-east-1", "S3_PAYLOAD_BUCKET": "payloads", "VAULT_ADDR": "https://vault.internal", "VAULT_CACERT": "/run/tls/ca.crt", "VAULT_TOKEN_FILE": "/run/secrets/vault-token",
		"LITES_ENVIRONMENT": "production", "LITES_VERSION": "test", "LITES_REGION": "US", "OTLP_GRPC_ENDPOINT": "otel.internal:4317", "OTLP_BEARER_TOKEN_FILE": "/run/secrets/otel-token",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	configuration, err := loadConfig()
	if err != nil || configuration.databaseURL == "" || configuration.trustedAudience != "contract-service" {
		t.Fatalf("loadConfig()=%#v err=%v", configuration, err)
	}
	t.Setenv("SERVER_CLIENT_CA_FILE", "")
	if _, err = loadConfig(); err == nil {
		t.Fatal("production configuration without inbound client CA accepted")
	}
}

func TestContractSecretBundleRequiresPurposeSeparatedMaterial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	material := func(value byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32)) }
	document := encodedSecretBundle{Version: "1.0.0", IDKey: material(1), IdempotencyPepper: material(2), RequestDigestPepper: material(3)}
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

func clearContractEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"ALLOW_INSECURE_DEVELOPMENT", "DATABASE_URL", "DATABASE_URL_FILE", "LISTEN_ADDRESS", "HEALTH_ADDRESS",
		"SERVER_TLS_CERT_FILE", "SERVER_TLS_KEY_FILE", "SERVER_CLIENT_CA_FILE", "TRUSTED_CONTEXT_KEYRING_FILE", "TRUSTED_CONTEXT_ISSUER", "TRUSTED_CONTEXT_AUDIENCE", "CONTRACT_SECRET_BUNDLE_FILE",
		"STORE_EPOCH_URL", "STORE_EPOCH_TOKEN_FILE", "STORE_EPOCH_ROOT_CA_FILE", "STORE_EPOCH_CLIENT_CERT_FILE", "STORE_EPOCH_CLIENT_KEY_FILE",
		"S3_REGION", "S3_ENDPOINT", "S3_PATH_STYLE", "S3_PAYLOAD_BUCKET", "S3_PAYLOAD_PREFIX", "S3_SERVER_SIDE_ENCRYPTION", "S3_KMS_KEY_ID",
		"VAULT_ADDR", "VAULT_NAMESPACE", "VAULT_PAYLOAD_KEY_MOUNT", "VAULT_TOKEN_FILE", "VAULT_CACERT", "VAULT_CLIENT_CERT_FILE", "VAULT_CLIENT_KEY_FILE", "VAULT_TLS_SERVER_NAME", "VAULT_PAYLOAD_KEY_PREFIX",
		"LITES_ENVIRONMENT", "LITES_VERSION", "LITES_REGION", "OTLP_GRPC_ENDPOINT", "OTLP_ROOT_CA_FILE", "OTLP_CLIENT_CERT_FILE", "OTLP_CLIENT_KEY_FILE", "OTLP_TLS_SERVER_NAME", "OTLP_BEARER_TOKEN_FILE",
		"DATABASE_MAX_CONNECTIONS", "IDEMPOTENCY_TTL", "CONTRACT_PROPOSAL_TTL", "TRACE_SAMPLE_RATIO",
		"INTERNAL_USAGE_WORKLOAD_IDENTITIES",
	} {
		t.Setenv(name, "")
	}
}
