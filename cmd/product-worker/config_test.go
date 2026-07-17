package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProductWorkerProductionConfiguration(t *testing.T) {
	databaseFile := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(databaseFile, []byte("postgres://product@postgres/lites\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"DATABASE_URL_FILE": databaseFile, "PRODUCT_SECRET_BUNDLE_FILE": "/run/secrets/product", "PRODUCT_INBOX_LEASE_PEPPER_FILE": "/run/secrets/inbox",
		"STORE_EPOCH_URL": "https://store-epoch:8443/v1/epoch", "STORE_EPOCH_TOKEN_FILE": "/run/secrets/epoch-token", "STORE_EPOCH_ROOT_CA_FILE": "/run/secrets/epoch-ca",
		"NATS_URLS": "tls://nats-0:4222,tls://nats-1:4222", "NATS_CLIENT_CERT_FILE": "/run/secrets/nats-cert", "NATS_CLIENT_KEY_FILE": "/run/secrets/nats-key", "NATS_ROOT_CA_FILE": "/run/secrets/nats-ca",
		"S3_REGION": "us-east-1", "S3_PAYLOAD_BUCKET": "payloads", "S3_SERVER_SIDE_ENCRYPTION": "aws:kms", "S3_KMS_KEY_ID": "alias/lites-payloads",
		"VAULT_ADDR": "https://vault:8200", "VAULT_TOKEN_FILE": "/run/secrets/vault-token", "VAULT_CACERT": "/run/secrets/vault-ca",
		"REMINDER_DELIVERY_URL": "https://notification-gateway:8443/v1/internal/reminder-deliveries", "REMINDER_DELIVERY_READINESS_URL": "https://notification-gateway:8443/ready", "REMINDER_DELIVERY_TOKEN_FILE": "/run/secrets/reminder-token", "REMINDER_DELIVERY_ROOT_CA_FILE": "/run/secrets/reminder-ca",
		"LITES_ENVIRONMENT": "production", "LITES_VERSION": "1.0.0", "LITES_REGION": "us-east-1", "OTLP_GRPC_ENDPOINT": "otel:4317", "OTLP_ROOT_CA_FILE": "/run/secrets/otel-ca", "OTLP_BEARER_TOKEN_FILE": "/run/secrets/otel-token",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	configuration, err := loadConfig()
	if err != nil || configuration.streamReplicas != 3 || configuration.consumerName == "" || configuration.runMaxSteps != 24 || configuration.portfolioRetention.String() != "720h0m0s" || configuration.portfolioExportBatch != 100 {
		t.Fatalf("configuration=%#v err=%v", configuration, err)
	}
	t.Setenv("NATS_STREAM_REPLICAS", "2")
	if _, err = loadConfig(); err == nil {
		t.Fatal("production accepted a non-replicated command stream")
	}
	t.Setenv("NATS_STREAM_REPLICAS", "invalid")
	if _, err = loadConfig(); err == nil {
		t.Fatal("invalid typed configuration was silently defaulted")
	}
	t.Setenv("NATS_STREAM_REPLICAS", "3")
	t.Setenv("PORTFOLIO_EXPORT_RETENTION", "23h")
	if _, err = loadConfig(); err == nil {
		t.Fatal("worker accepted a portfolio retention below the durable minimum")
	}
}

func TestLoadWorkerSecretsRequiresFourPurposeSeparatedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product-bundle.json")
	key := func(value byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32)) }
	write := func(values map[string]string) {
		t.Helper()
		encoded, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]string{"version": "1.0.0", "id_key": key(1), "idempotency_pepper": key(2), "request_digest_pepper": key(3), "cursor_key": key(4)})
	secrets, err := loadWorkerSecrets(path)
	if err != nil || len(secrets.IDKey) != 32 || len(secrets.IdempotencyPepper) != 32 || len(secrets.RequestDigestPepper) != 32 || len(secrets.CursorKey) != 32 {
		t.Fatalf("secrets=%#v err=%v", secrets, err)
	}
	write(map[string]string{"version": "1.0.0", "id_key": key(1), "idempotency_pepper": key(1), "request_digest_pepper": key(3), "cursor_key": key(4)})
	if _, err = loadWorkerSecrets(path); err == nil {
		t.Fatal("worker accepted reused purpose-separated key material")
	}
}
