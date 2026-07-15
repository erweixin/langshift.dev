package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setRealtimeProductionEnvironment(t *testing.T) {
	t.Helper()
	databaseFile := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(databaseFile, []byte("postgres://realtime:secret@db.internal/lites\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"DATABASE_URL": "", "DATABASE_URL_FILE": databaseFile,
		"SERVER_TLS_CERT_FILE": "/run/tls/server.crt", "SERVER_TLS_KEY_FILE": "/run/tls/server.key", "SERVER_CLIENT_CA_FILE": "/run/tls/client-ca.crt",
		"TRUSTED_CONTEXT_KEYRING_FILE": "/run/config/keyring.json", "TRUSTED_CONTEXT_ISSUER": "lites-gateway", "TRUSTED_CONTEXT_AUDIENCE": "realtime-gateway",
		"NATS_URLS": "tls://nats-a.internal:4222,tls://nats-b.internal:4222", "NATS_CLIENT_CERT_FILE": "/run/tls/nats.crt", "NATS_CLIENT_KEY_FILE": "/run/tls/nats.key", "NATS_ROOT_CA_FILE": "/run/tls/nats-ca.crt",
		"ALLOW_INSECURE_DEVELOPMENT": "false", "LITES_ENVIRONMENT": "production", "LITES_VERSION": "test", "LITES_REGION": "US",
		"OTLP_GRPC_ENDPOINT": "otel.internal:4317", "OTLP_BEARER_TOKEN_FILE": "/run/secrets/otel-token",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}

func TestRealtimeConfigRequiresProductionCredentialsAndSafeBounds(t *testing.T) {
	setRealtimeProductionEnvironment(t)
	configuration, err := loadConfig()
	if err != nil || configuration.pageSize != 200 || configuration.heartbeat != 15*time.Second || configuration.catchUpInterval != 2*time.Second || len(configuration.natsURLs) != 2 {
		t.Fatalf("config = %#v, error = %v", configuration, err)
	}
	t.Setenv("REALTIME_PAGE_SIZE", "1001")
	if _, err = loadConfig(); err == nil {
		t.Fatal("page size above the bounded backfill limit was accepted")
	}
	t.Setenv("REALTIME_PAGE_SIZE", "200")
	t.Setenv("NATS_URLS", "nats://nats.internal:4222")
	if _, err = loadConfig(); err == nil {
		t.Fatal("plaintext production NATS endpoint was accepted")
	}
}

func TestRealtimeConfigAllowsExplicitLoopbackDevelopment(t *testing.T) {
	setRealtimeProductionEnvironment(t)
	t.Setenv("ALLOW_INSECURE_DEVELOPMENT", "true")
	t.Setenv("DATABASE_URL", "postgres://realtime:secret@127.0.0.1/lites")
	t.Setenv("DATABASE_URL_FILE", "")
	t.Setenv("SERVER_TLS_CERT_FILE", "")
	t.Setenv("SERVER_TLS_KEY_FILE", "")
	t.Setenv("SERVER_CLIENT_CA_FILE", "")
	t.Setenv("NATS_URLS", "nats://127.0.0.1:4222")
	t.Setenv("NATS_CLIENT_CERT_FILE", "")
	t.Setenv("NATS_CLIENT_KEY_FILE", "")
	t.Setenv("NATS_ROOT_CA_FILE", "")
	t.Setenv("OTLP_GRPC_ENDPOINT", "")
	t.Setenv("OTLP_BEARER_TOKEN_FILE", "")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
}
