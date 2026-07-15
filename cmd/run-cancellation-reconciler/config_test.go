package main

import "testing"

func TestRunCancellationConfigurationFailsClosedAndAcceptsDevelopmentFixture(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "DATABASE_URL_FILE", "RUNTIME_DATABASE_URL", "RUNTIME_DATABASE_URL_FILE", "STORE_EPOCH_URL", "ALLOW_INSECURE_DEVELOPMENT"} {
		t.Setenv(name, "")
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("empty configuration accepted")
	}
	values := map[string]string{
		"ALLOW_INSECURE_DEVELOPMENT": "true", "DATABASE_URL": "postgres://agent@127.0.0.1/lites?sslmode=disable", "RUNTIME_DATABASE_URL": "postgres://runtime@127.0.0.1/lites?sslmode=disable", "STORE_EPOCH_URL": "http://127.0.0.1:8080/v1/store-epoch",
		"EXECUTION_ID_KEY_FILE": "/tmp/execution-id", "EXECUTION_LEASE_PEPPER_FILE": "/tmp/execution-pepper", "LLM_ID_KEY_FILE": "/tmp/llm-id", "LLM_TOKEN_PEPPER_FILE": "/tmp/llm-pepper", "BILLING_ID_KEY_FILE": "/tmp/billing-id", "RUNTIME_ID_KEY_FILE": "/tmp/runtime-id", "RUNTIME_TOKEN_PEPPER_FILE": "/tmp/runtime-pepper",
		"S3_REGION": "us-east-1", "S3_PAYLOAD_BUCKET": "payloads", "VAULT_ADDR": "http://127.0.0.1:8200", "HEALTH_ADDRESS": "127.0.0.1:8088", "LITES_ENVIRONMENT": "test", "LITES_VERSION": "test", "LITES_REGION": "US", "RUN_CANCELLATION_SHARD_COUNT": "16",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	configuration, err := loadConfig()
	if err != nil || configuration.shardCount != 16 || !configuration.allowInsecureDevelopment {
		t.Fatalf("loadConfig()=%#v err=%v", configuration, err)
	}
	t.Setenv("RUN_CANCELLATION_PAGE", "0")
	if _, err = loadConfig(); err == nil {
		t.Fatal("zero cancellation page accepted")
	}
}
