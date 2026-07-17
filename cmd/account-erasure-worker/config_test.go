package main

import "testing"

func TestErasureWorkerConfigurationRequiresDedicatedDependencies(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_DEVELOPMENT", "true")
	t.Setenv("DATABASE_URL", "postgres://worker@127.0.0.1/lites")
	t.Setenv("STORE_EPOCH_URL", "http://127.0.0.1:8080/v1/store-epoch")
	t.Setenv("NATS_URLS", "nats://127.0.0.1:4222")
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:8200")
	t.Setenv("S3_REGION", "us-test-1")
	t.Setenv("S3_PAYLOAD_BUCKET", "lites-payloads")
	t.Setenv("S3_ARTIFACT_BUCKET", "lites-artifacts")
	t.Setenv("VALKEY_ADDRESSES", "127.0.0.1:6379")
	t.Setenv("ERASURE_INBOX_LEASE_PEPPER_FILE", "/run/secrets/inbox")
	t.Setenv("ERASURE_IDENTITY_KEY_FILE", "/run/secrets/identity")
	t.Setenv("ERASURE_RECEIPT_KEY_FILE", "/run/secrets/receipt")
	t.Setenv("LITES_ENVIRONMENT", "development")
	t.Setenv("LITES_VERSION", "test")
	t.Setenv("LITES_REGION", "local")
	configuration, err := loadConfig()
	if err != nil || configuration.consumerName != "ACCOUNT_ERASURE_WORKER" || configuration.restoreInterval.String() != "1m0s" || configuration.vaultPayloadMount == configuration.vaultTransitMount {
		t.Fatalf("configuration=%#v err=%v", configuration, err)
	}
	t.Setenv("S3_ARTIFACT_BUCKET", "")
	if _, err = loadConfig(); err == nil {
		t.Fatal("missing artifact bucket was accepted")
	}
}
