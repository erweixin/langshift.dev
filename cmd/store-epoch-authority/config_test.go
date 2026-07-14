package main

import "testing"

func setProductionEnvironment(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"LISTEN_ADDRESS": ":8443", "HEALTH_ADDRESS": "127.0.0.1:8084", "STORE_EPOCH_FILE": "/run/epoch/current", "STORE_EPOCH_BEARER_TOKEN_FILE": "/run/secrets/epoch-token",
		"SERVER_TLS_CERT_FILE": "/run/tls/tls.crt", "SERVER_TLS_KEY_FILE": "/run/tls/tls.key", "SERVER_CLIENT_CA_FILE": "/run/tls/client-ca.crt",
		"LITES_ENVIRONMENT": "production", "LITES_VERSION": "test", "LITES_REGION": "US", "OTLP_GRPC_ENDPOINT": "otel.internal:4317", "OTLP_BEARER_TOKEN_FILE": "/run/secrets/otel-token", "ALLOW_INSECURE_DEVELOPMENT": "false",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}

func TestProductionConfigRequiresMTLSAndAuthenticatedTelemetry(t *testing.T) {
	setProductionEnvironment(t)
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SERVER_CLIENT_CA_FILE", "OTLP_BEARER_TOKEN_FILE"} {
		t.Run(name, func(t *testing.T) {
			setProductionEnvironment(t)
			t.Setenv(name, "")
			if _, err := loadConfig(); err == nil {
				t.Fatalf("production config without %s accepted", name)
			}
		})
	}
}

func TestDevelopmentConfigRequiresLoopbackListener(t *testing.T) {
	setProductionEnvironment(t)
	t.Setenv("ALLOW_INSECURE_DEVELOPMENT", "true")
	t.Setenv("LISTEN_ADDRESS", "127.0.0.1:8443")
	t.Setenv("SERVER_TLS_CERT_FILE", "")
	t.Setenv("SERVER_TLS_KEY_FILE", "")
	t.Setenv("SERVER_CLIENT_CA_FILE", "")
	t.Setenv("OTLP_GRPC_ENDPOINT", "")
	t.Setenv("OTLP_BEARER_TOKEN_FILE", "")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LISTEN_ADDRESS", "0.0.0.0:8443")
	if _, err := loadConfig(); err == nil {
		t.Fatal("insecure non-loopback listener accepted")
	}
}

func TestHealthListenerCannotExposeUnauthenticatedMetrics(t *testing.T) {
	setProductionEnvironment(t)
	t.Setenv("HEALTH_ADDRESS", "0.0.0.0:8084")
	if _, err := loadConfig(); err == nil {
		t.Fatal("non-loopback health listener accepted")
	}
}
