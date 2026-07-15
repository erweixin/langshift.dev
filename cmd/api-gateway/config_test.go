package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func gatewaySecretFile(t *testing.T, now time.Time) string {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x71}, ed25519.SeedSize))
	values := make([]string, 6)
	for index := range values {
		values[index] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{byte(0x72 + index)}, 32))
	}
	encoded := fmt.Sprintf(`{"version":"1.0.0","trusted_context_key_id":"gateway-key-v1","trusted_context_private_key":%q,"trusted_context_not_before":%q,"trusted_context_not_after":%q,"session_pepper":%q,"csrf_pepper":%q,"fingerprint_pepper":%q,"anonymous_handle_key_id":"anonymous-key-v1","anonymous_handle_signing_key":%q,"anonymous_handle_digest_pepper":%q,"anonymous_csrf_key":%q}`,
		base64.StdEncoding.EncodeToString(privateKey), now.Add(-time.Hour).Format(time.RFC3339), now.Add(30*24*time.Hour).Format(time.RFC3339), values[0], values[1], values[2], values[3], values[4], values[5])
	path := filepath.Join(t.TempDir(), "gateway-secrets.json")
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func gatewayKeyringFile(t *testing.T, now time.Time) string {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x71}, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	encoded := fmt.Sprintf(`{"version":"1.0.0","keys":[{"id":"gateway-key-v1","public_key":%q,"not_before":%q,"not_after":%q}]}`,
		base64.RawURLEncoding.EncodeToString(publicKey), now.Add(-time.Hour).Format(time.RFC3339), now.Add(30*24*time.Hour).Format(time.RFC3339))
	path := filepath.Join(t.TempDir(), "gateway-keyring.json")
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func setGatewayProductionEnvironment(t *testing.T) {
	t.Helper()
	databaseFile := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(databaseFile, []byte("postgres://gateway:secret@db.internal/lites\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"DATABASE_URL": "", "DATABASE_URL_FILE": databaseFile,
		"SERVER_TLS_CERT_FILE": "/run/tls/tls.crt", "SERVER_TLS_KEY_FILE": "/run/tls/tls.key",
		"IDENTITY_UPSTREAM_URL": "https://identity.internal:8443", "IDENTITY_UPSTREAM_ROOT_CA_FILE": "/run/tls/ca.crt", "IDENTITY_UPSTREAM_CLIENT_CERT_FILE": "/run/tls/client.crt", "IDENTITY_UPSTREAM_CLIENT_KEY_FILE": "/run/tls/client.key", "IDENTITY_UPSTREAM_TLS_SERVER_NAME": "identity.internal",
		"REALTIME_UPSTREAM_URL": "https://realtime.internal:8443", "REALTIME_UPSTREAM_ROOT_CA_FILE": "/run/tls/ca.crt", "REALTIME_UPSTREAM_CLIENT_CERT_FILE": "/run/tls/client.crt", "REALTIME_UPSTREAM_CLIENT_KEY_FILE": "/run/tls/client.key", "REALTIME_UPSTREAM_TLS_SERVER_NAME": "realtime.internal", "REALTIME_TRUSTED_CONTEXT_AUDIENCE": "realtime-gateway",
		"GATEWAY_SECRET_BUNDLE_FILE": "/run/secrets/gateway.json", "TRUSTED_CONTEXT_KEYRING_FILE": "/run/config/gateway-keyring.json", "TRUSTED_PROXY_CIDRS": "10.0.0.0/8,192.168.0.0/16", "PUBLIC_ORIGINS": "https://app.lites.dev,https://admin.lites.dev",
		"ALLOW_INSECURE_DEVELOPMENT": "false", "LITES_ENVIRONMENT": "production", "LITES_VERSION": "test", "LITES_REGION": "US",
		"OTLP_GRPC_ENDPOINT": "otel.internal:4317", "OTLP_BEARER_TOKEN_FILE": "/run/secrets/otel-token",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}

func TestGatewayConfigEnforcesProductionTLSAndOrigins(t *testing.T) {
	setGatewayProductionEnvironment(t)
	configuration, err := loadConfig()
	if err != nil || configuration.databaseMaxConnections != 64 || configuration.trustedContextTTL != 2*time.Minute || len(configuration.publicOrigins) != 2 {
		t.Fatalf("config=%#v error=%v", configuration, err)
	}
	t.Setenv("PUBLIC_ORIGINS", "https://app.lites.dev,https://app.lites.dev")
	if _, err = loadConfig(); err == nil {
		t.Fatal("expected duplicate origin rejection")
	}
	t.Setenv("PUBLIC_ORIGINS", "https://app.lites.dev/path")
	if _, err = loadConfig(); err == nil {
		t.Fatal("expected origin path rejection")
	}
}

func TestGatewayConfigAllowsExplicitLoopbackDevelopment(t *testing.T) {
	setGatewayProductionEnvironment(t)
	t.Setenv("ALLOW_INSECURE_DEVELOPMENT", "true")
	t.Setenv("DATABASE_URL", "postgres://gateway:secret@127.0.0.1/lites")
	t.Setenv("DATABASE_URL_FILE", "")
	t.Setenv("SERVER_TLS_CERT_FILE", "")
	t.Setenv("SERVER_TLS_KEY_FILE", "")
	t.Setenv("IDENTITY_UPSTREAM_URL", "http://127.0.0.1:8444")
	t.Setenv("IDENTITY_UPSTREAM_ROOT_CA_FILE", "")
	t.Setenv("IDENTITY_UPSTREAM_CLIENT_CERT_FILE", "")
	t.Setenv("IDENTITY_UPSTREAM_CLIENT_KEY_FILE", "")
	t.Setenv("IDENTITY_UPSTREAM_TLS_SERVER_NAME", "")
	t.Setenv("REALTIME_UPSTREAM_URL", "http://127.0.0.1:8445")
	t.Setenv("REALTIME_UPSTREAM_ROOT_CA_FILE", "")
	t.Setenv("REALTIME_UPSTREAM_CLIENT_CERT_FILE", "")
	t.Setenv("REALTIME_UPSTREAM_CLIENT_KEY_FILE", "")
	t.Setenv("REALTIME_UPSTREAM_TLS_SERVER_NAME", "")
	t.Setenv("OTLP_GRPC_ENDPOINT", "")
	t.Setenv("OTLP_BEARER_TOKEN_FILE", "")
	configuration, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	client, endpoint, err := configuration.realtimeUpstreamClient()
	if err != nil || client.Timeout != 0 || endpoint.String() != "http://127.0.0.1:8445" {
		t.Fatalf("realtime upstream client = %#v endpoint = %v error = %v", client, endpoint, err)
	}
}

func TestGatewaySecretBundleRequiresPurposeSeparationAndActiveWindow(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	secrets, err := loadGatewaySecrets(gatewaySecretFile(t, now), now, 2*time.Minute)
	if err != nil || len(secrets.SigningKey) != ed25519.PrivateKeySize || secrets.SigningKeyID != "gateway-key-v1" || secrets.AnonymousHandleKeyID != "anonymous-key-v1" {
		t.Fatalf("secrets=%#v error=%v", secrets, err)
	}
	if err = verifySigningKeyring(gatewayKeyringFile(t, now), secrets, now, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	secrets.SigningKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	if err = verifySigningKeyring(gatewayKeyringFile(t, now), secrets, now, 2*time.Minute); err == nil {
		t.Fatal("expected mismatched public key rejection")
	}
	if _, err = loadGatewaySecrets(gatewaySecretFile(t, now), now.Add(31*24*time.Hour), 2*time.Minute); err == nil {
		t.Fatal("expected expired signing window rejection")
	}
}
