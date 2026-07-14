package main

import (
	"os"
	"path/filepath"
	"testing"
)

func setWorkerProductionEnvironment(t *testing.T) {
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
		"S3_REGION": "us-east-1", "S3_PAYLOAD_BUCKET": "lites-payloads", "S3_IMPORT_BUCKET": "lites-imports",
		"IDENTITY_INBOX_LEASE_PEPPER_FILE": "/run/secrets/inbox-pepper", "IDENTITY_INVITATION_TOKEN_PEPPER_FILE": "/run/secrets/invitation-pepper",
		"ALLOW_INSECURE_DEVELOPMENT": "false",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}

func TestLoadConfigEnforcesProductionAuthReplicationAndStrictTypes(t *testing.T) {
	setWorkerProductionEnvironment(t)
	configuration, err := loadConfig()
	if err != nil || configuration.streamReplicas != 3 || configuration.concurrency != 8 {
		t.Fatalf("config=%#v err=%v", configuration, err)
	}
	t.Setenv("NATS_STREAM_REPLICAS", "2")
	if _, err = loadConfig(); err == nil {
		t.Fatal("expected replication rejection")
	}
	t.Setenv("NATS_STREAM_REPLICAS", "3")
	t.Setenv("S3_PATH_STYLE", "sometimes")
	if _, err = loadConfig(); err == nil {
		t.Fatal("expected malformed boolean rejection")
	}
}

func TestLoadConfigAllowsExplicitLoopbackDevelopment(t *testing.T) {
	setWorkerProductionEnvironment(t)
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
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
}
