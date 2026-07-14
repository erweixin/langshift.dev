package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func setProductionEnvironment(t *testing.T) {
	t.Helper()
	databaseURLFile := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(databaseURLFile, []byte("postgres://publisher:secret@db.internal/lites\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DATABASE_URL_FILE", databaseURLFile)
	t.Setenv("NATS_URLS", "tls://nats-1.internal:4222,tls://nats-2.internal:4222")
	t.Setenv("NATS_CREDENTIALS_FILE", "/run/secrets/nats.creds")
	t.Setenv("STORE_EPOCH_URL", "https://epoch.internal/v1/store-epoch")
	t.Setenv("STORE_EPOCH_TOKEN_FILE", "/run/secrets/epoch-token")
	t.Setenv("OUTBOX_LEASE_PEPPER_FILE", "/run/secrets/outbox-pepper")
	t.Setenv("ALLOW_INSECURE_DEVELOPMENT", "false")
	t.Setenv("LITES_ENVIRONMENT", "production")
	t.Setenv("LITES_VERSION", "test")
	t.Setenv("LITES_REGION", "US")
	t.Setenv("OTLP_GRPC_ENDPOINT", "otel.internal:4317")
	t.Setenv("OTLP_BEARER_TOKEN_FILE", "/run/secrets/otel-token")
}

func TestLoadConfigEnforcesProductionReplicationAndStrictTypes(t *testing.T) {
	setProductionEnvironment(t)
	configuration, err := loadConfig()
	if err != nil || configuration.streamReplicas != 3 || len(configuration.natsURLs) != 2 {
		t.Fatalf("config=%#v err=%v", configuration, err)
	}
	t.Setenv("NATS_STREAM_REPLICAS", "2")
	if _, err = loadConfig(); err == nil {
		t.Fatal("expected production replication rejection")
	}
	t.Setenv("NATS_STREAM_REPLICAS", "three")
	if _, err = loadConfig(); err == nil {
		t.Fatal("expected malformed integer rejection")
	}
}

func TestLoadConfigAllowsExplicitInsecureDevelopmentOnly(t *testing.T) {
	setProductionEnvironment(t)
	t.Setenv("ALLOW_INSECURE_DEVELOPMENT", "true")
	t.Setenv("NATS_URLS", "nats://127.0.0.1:4222")
	t.Setenv("NATS_CREDENTIALS_FILE", "")
	t.Setenv("STORE_EPOCH_URL", "http://127.0.0.1:8090/v1/store-epoch")
	t.Setenv("STORE_EPOCH_TOKEN_FILE", "")
	t.Setenv("NATS_STREAM_REPLICAS", "1")
	t.Setenv("OTLP_GRPC_ENDPOINT", "")
	t.Setenv("OTLP_BEARER_TOKEN_FILE", "")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
}

func TestReadBase64SecretRequiresDecodedEntropy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pepper")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readBase64Secret(path, 32)
	if err != nil || len(value) != 32 {
		t.Fatalf("length=%d err=%v", len(value), err)
	}
	if err = os.WriteFile(path, []byte("plaintext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = readBase64Secret(path, 32); err == nil {
		t.Fatal("expected malformed secret rejection")
	}
}
