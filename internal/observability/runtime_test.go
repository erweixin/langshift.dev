package observability

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPMetricsUseBoundedRouteAndExcludeRequestData(t *testing.T) {
	runtime, err := New(t.Context(), Config{ServiceName: "identity-service", ServiceVersion: "test", Environment: "test", Region: "US", TraceSampleRatio: 1, AllowInsecureDevelopment: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Shutdown(context.Background()) }()
	identifier := "invitation-private-identifier"
	handler := runtime.WrapHTTP(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusConflict)
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/invitations/"+identifier+"/accept?token=secret-value", nil)
	writer := httptest.NewRecorder()
	handler.ServeHTTP(writer, request)
	metricsWriter := httptest.NewRecorder()
	runtime.MetricsHandler().ServeHTTP(metricsWriter, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, err := io.ReadAll(metricsWriter.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if metricsWriter.Code != http.StatusOK || !strings.Contains(text, `/v1/invitations/{invitation_id}/accept`) || strings.Contains(text, identifier) || strings.Contains(text, "secret-value") {
		t.Fatalf("unsafe metrics output status=%d body=%s", metricsWriter.Code, text)
	}
}

func TestProductionTelemetryRequiresAuthenticatedCollector(t *testing.T) {
	_, err := New(t.Context(), Config{ServiceName: "identity-service", ServiceVersion: "test", Environment: "production", Region: "US", OTLPEndpoint: "collector:4317", TraceSampleRatio: 0.1})
	if err == nil {
		t.Fatal("unauthenticated production collector accepted")
	}
	_, err = New(t.Context(), Config{ServiceName: "identity-service", ServiceVersion: "test", Environment: "production", Region: "US", TraceSampleRatio: 0.1})
	if err == nil {
		t.Fatal("missing production collector accepted")
	}
}

func TestRouteTemplateNeverEchoesUnknownPath(t *testing.T) {
	if route := routeTemplate("/v1/private/person@example.com"); route != "/unmatched" {
		t.Fatalf("unsafe route template %q", route)
	}
	if route := routeTemplate("/v1/sessions/session-secret"); route != "/v1/sessions/{session_id}" {
		t.Fatalf("dynamic session route %q", route)
	}
}

func TestShutdownIsBounded(t *testing.T) {
	runtime, err := New(t.Context(), Config{ServiceName: "worker", ServiceVersion: "test", Environment: "test", Region: "US", TraceSampleRatio: 0, AllowInsecureDevelopment: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
