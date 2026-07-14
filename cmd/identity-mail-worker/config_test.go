package main

import (
	"os"
	"path/filepath"
	"testing"
)

func setMailWorkerProductionEnvironment(t *testing.T) {
	t.Helper()
	databaseFile := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(databaseFile, []byte("postgres://worker:secret@db.internal/lites\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"DATABASE_URL": "", "DATABASE_URL_FILE": databaseFile,
		"STORE_EPOCH_URL": "https://epoch.internal/v1/store-epoch", "STORE_EPOCH_TOKEN_FILE": "/run/secrets/epoch-token",
		"NATS_URLS": "tls://nats.internal:4222", "NATS_CREDENTIALS_FILE": "/run/secrets/nats.creds",
		"VAULT_ADDR": "https://vault.internal:8200", "VAULT_TOKEN_FILE": "/run/secrets/vault-token",
		"S3_REGION": "us-east-1", "S3_PAYLOAD_BUCKET": "lites-payloads",
		"IDENTITY_INBOX_LEASE_PEPPER_FILE": "/run/secrets/inbox-pepper",
		"PUBLIC_APP_URL":                   "https://app.lites.dev", "SMTP_ADDRESS": "smtp.internal:587", "SMTP_SERVER_NAME": "smtp.internal", "SMTP_FROM_ADDRESS": "security@lites.dev", "SMTP_USERNAME": "lites", "SMTP_PASSWORD_FILE": "/run/secrets/smtp-password",
		"ALLOW_INSECURE_DEVELOPMENT": "false", "LITES_ENVIRONMENT": "production", "LITES_VERSION": "test", "LITES_REGION": "US",
		"OTLP_GRPC_ENDPOINT": "otel.internal:4317", "OTLP_BEARER_TOKEN_FILE": "/run/secrets/otel-token",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}

func TestLoadConfigEnforcesIsolatedProductionMailDependencies(t *testing.T) {
	setMailWorkerProductionEnvironment(t)
	configuration, err := loadConfig()
	if err != nil || configuration.streamReplicas != 3 || configuration.concurrency != 8 || configuration.consumerName != "IDENTITY_MAIL_WORKER" {
		t.Fatalf("config=%#v err=%v", configuration, err)
	}
	t.Setenv("NATS_STREAM_REPLICAS", "2")
	if _, err = loadConfig(); err == nil {
		t.Fatal("expected replication rejection")
	}
	t.Setenv("NATS_STREAM_REPLICAS", "3")
	t.Setenv("SMTP_PASSWORD_FILE", "")
	if _, err = loadConfig(); err == nil {
		t.Fatal("expected unauthenticated SMTP rejection")
	}
}

func TestLoadConfigAllowsExplicitLoopbackDevelopment(t *testing.T) {
	setMailWorkerProductionEnvironment(t)
	t.Setenv("ALLOW_INSECURE_DEVELOPMENT", "true")
	t.Setenv("DATABASE_URL", "postgres://worker:secret@127.0.0.1/lites")
	t.Setenv("DATABASE_URL_FILE", "")
	t.Setenv("STORE_EPOCH_URL", "http://127.0.0.1:8090/v1/store-epoch")
	t.Setenv("STORE_EPOCH_TOKEN_FILE", "")
	t.Setenv("NATS_URLS", "nats://127.0.0.1:4222")
	t.Setenv("NATS_CREDENTIALS_FILE", "")
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:8200")
	t.Setenv("VAULT_TOKEN_FILE", "")
	t.Setenv("S3_ENDPOINT", "http://127.0.0.1:9000")
	t.Setenv("NATS_STREAM_REPLICAS", "1")
	t.Setenv("OTLP_GRPC_ENDPOINT", "")
	t.Setenv("OTLP_BEARER_TOKEN_FILE", "")
	t.Setenv("PUBLIC_APP_URL", "http://127.0.0.1:3000")
	t.Setenv("SMTP_USERNAME", "")
	t.Setenv("SMTP_PASSWORD_FILE", "")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
}
