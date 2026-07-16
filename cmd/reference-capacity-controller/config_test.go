package main

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langshift/lites/internal/capacity/reference"
	"github.com/langshift/lites/internal/observability"
)

func TestConfigRequiresProductionTLSAndFixedEnvironment(t *testing.T) {
	values := map[string]string{
		"SERVER_TLS_CERT_FILE": "/run/secrets/server-cert", "SERVER_TLS_KEY_FILE": "/run/secrets/server-key", "SERVER_CLIENT_CA_FILE": "/run/secrets/client-ca", "CAPACITY_CONTROLLER_BEARER_TOKEN_FILE": "/run/secrets/controller-token",
		"CAPACITY_PROFILE_FILE": "/run/config/profile.json", "CAPACITY_DATASET_FILE": "/run/config/dataset.json", "LITES_SOURCE_COMMIT": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"GATEWAY_URL": "https://gateway.reference.internal", "GATEWAY_ROOT_CA_FILE": "/run/secrets/gateway-ca", "GATEWAY_CLIENT_CERT_FILE": "/run/secrets/gateway-cert", "GATEWAY_CLIENT_KEY_FILE": "/run/secrets/gateway-key", "GATEWAY_TLS_SERVER_NAME": "gateway.reference.internal", "PUBLIC_ORIGIN": "https://capacity.reference.internal",
		"LITES_ENVIRONMENT": reference.ProfileID, "LITES_VERSION": "test-version", "LITES_REGION": "us-east-2", "OTLP_GRPC_ENDPOINT": "otel.reference.internal:4317", "OTLP_ROOT_CA_FILE": "/run/secrets/otel-ca", "OTLP_BEARER_TOKEN_FILE": "/run/secrets/otel-token",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	configuration, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	configuration.environment = "production"
	if err = configuration.validate(); err == nil {
		t.Fatal("non-reference environment was accepted")
	}
	configuration.environment = reference.ProfileID
	configuration.gatewayCAFile = "relative-ca"
	if err = configuration.validate(); err == nil {
		t.Fatal("relative credential path was accepted")
	}
}

func TestLoadMetricsMatchCanonicalPrometheusQueries(t *testing.T) {
	runtime, err := observability.New(t.Context(), observability.Config{ServiceName: "reference-capacity-controller", ServiceVersion: "test", Environment: reference.ProfileID, Region: "test", AllowInsecureDevelopment: true})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Shutdown(context.Background())
	metrics, registration, err := newLoadMetrics(runtime.Meter("capacity-test"))
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Unregister()
	metrics.SetHotTenantPercent(20)
	metrics.AddHotUserEventAppends(t.Context(), 3)
	recorder := httptest.NewRecorder()
	runtime.MetricsHandler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, name := range []string{"lites_capacity_hot_tenant_percent", "lites_capacity_hot_tenant_user_event_appends_total"} {
		if !strings.Contains(text, name) {
			t.Fatalf("missing %s in metrics:\n%s", name, text)
		}
	}
}
