package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigIsProductionStrict(t *testing.T) {
	values := map[string]string{
		"SERVER_ADDRESS": ":8443", "HEALTH_ADDRESS": ":8081",
		"SERVER_TLS_CERT_FILE": "/run/secrets/tls.crt", "SERVER_TLS_KEY_FILE": "/run/secrets/tls.key",
		"REFERENCE_PROVIDER_MODEL": "reference-model-2026-07-16", "REFERENCE_PROVIDER_BEARER_TOKEN_FILE": "/run/secrets/provider-token",
		"LITES_VERSION": "sha-test", "LITES_ENVIRONMENT": "stage3-reference-production-v1", "LITES_REGION": "us-east-2",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	configuration, err := loadConfig()
	if err != nil || configuration.replayRateNumerator != 153 || configuration.runtimeArtifactBytes != 1<<20 {
		t.Fatalf("configuration=%#v error=%v", configuration, err)
	}
	t.Setenv("REFERENCE_PROVIDER_BEARER_TOKEN_FILE", "relative")
	if _, err = loadConfig(); err == nil {
		t.Fatal("relative secret path accepted")
	}
}

func TestReadSecretRejectsMultilineAndShortValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	for _, value := range []string{"short\n", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nextra\n"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSecret(path, 1024); err == nil {
			t.Fatalf("secret %q accepted", value)
		}
	}
	if err := os.WriteFile(path, []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, err := readSecret(path, 1024); err != nil || value != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("value=%q error=%v", value, err)
	}
}
