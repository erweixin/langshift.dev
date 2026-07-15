package main

import "testing"

func TestRuntimeSweeperConfigurationFailsClosedAndAcceptsDevelopmentFixture(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "DATABASE_URL_FILE", "STORE_EPOCH_URL", "ALLOW_INSECURE_DEVELOPMENT"} {
		t.Setenv(name, "")
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("empty configuration accepted")
	}
	values := map[string]string{
		"ALLOW_INSECURE_DEVELOPMENT": "true", "DATABASE_URL": "postgres://runtime@127.0.0.1/lites?sslmode=disable", "STORE_EPOCH_URL": "http://127.0.0.1:8080/v1/store-epoch",
		"RUNTIME_ID_KEY_FILE": "/tmp/id", "RUNTIME_TOKEN_PEPPER_FILE": "/tmp/pepper", "S3_REGION": "us-east-1", "S3_PAYLOAD_BUCKET": "payloads", "VAULT_ADDR": "http://127.0.0.1:8200",
		"HEALTH_ADDRESS": "127.0.0.1:8086", "LITES_ENVIRONMENT": "test", "LITES_VERSION": "test", "LITES_REGION": "US", "RUNTIME_SWEEPER_SHARD_COUNT": "16",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	configuration, err := loadConfig()
	if err != nil || configuration.shardCount != 16 || !configuration.allowInsecureDevelopment {
		t.Fatalf("loadConfig()=%#v err=%v", configuration, err)
	}
	t.Setenv("RUNTIME_SWEEPER_SHARD_COUNT", "0")
	if _, err = loadConfig(); err == nil {
		t.Fatal("zero shard count accepted")
	}
}
