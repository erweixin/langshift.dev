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
		writer.WriteHeader(http.StatusTooManyRequests)
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
	if metricsWriter.Code != http.StatusOK || !strings.Contains(text, "lites_http_server_requests_total") || !strings.Contains(text, `http_response_throttled="true"`) || !strings.Contains(text, `/v1/invitations/{invitation_id}/accept`) || strings.Contains(text, identifier) || strings.Contains(text, "secret-value") {
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

func TestAgentMetricContractIsExportedWithOnlyBoundedLabels(t *testing.T) {
	runtime, err := New(t.Context(), Config{ServiceName: "agent-worker", ServiceVersion: "test", Environment: "stage3-reference-production-v1", Region: "US", TraceSampleRatio: 0, AllowInsecureDevelopment: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Shutdown(context.Background()) }()
	metrics := runtime.AgentMetrics()
	metrics.ObserveEventAppend(t.Context(), 5*time.Millisecond, "private-outcome-value")
	metrics.ObserveQueueWait(t.Context(), 20*time.Millisecond, "interactive")
	metrics.AddExecutingRuns(t.Context(), 1, "interactive")
	metrics.AddActiveProviderRequests(t.Context(), 1, "custom")
	metrics.AddProviderTokens(t.Context(), 32, "custom", "custom")
	metrics.AddOwnedRuntimeSessions(t.Context(), 1, "untrusted")
	metrics.ObserveRuntimeColdStart(t.Context(), time.Second, "untrusted")
	metrics.AddArtifactBytes(t.Context(), 1024, "write")
	metrics.AddRetainedBytes(t.Context(), 2048)
	metrics.AddLeaseHeartbeats(t.Context(), 1, "run")
	metrics.AddRunReplays(t.Context(), 1, "success")
	metrics.AddRunDeadlineTransition(t.Context(), "interactive", true, "success")
	metrics.AddRunExecution(t.Context(), "interactive", "success")
	metrics.ObserveFirstSafeToken(t.Context(), 250*time.Millisecond, "openai")
	metrics.AddToolCall(t.Context(), "unknown", true)
	metrics.AddToolUnknown(t.Context(), true)
	metrics.AddRealtimeConnections(t.Context(), 1)
	metrics.ObserveRealtimeGapRecovery(t.Context(), 100*time.Millisecond)
	metrics.AddInvariantViolation(t.Context(), "lost_event_fact")

	writer := httptest.NewRecorder()
	runtime.MetricsHandler().ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, err := io.ReadAll(writer.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, required := range []string{
		"lites_event_appends_total", "lites_event_append_duration_seconds_bucket", "lites_queue_wait_seconds_bucket",
		"lites_runs_executing", "lites_provider_requests_active", "lites_provider_tokens_total",
		"lites_runtime_sessions_owned", "lites_runtime_cold_start_seconds_bucket", "lites_artifact_bytes_total",
		"lites_retained_bytes_total", "lites_lease_heartbeats_total", "lites_run_replays_total", "lites_run_deadline_transitions_total", "lites_run_executions_total", "lites_first_safe_token_seconds_bucket",
		"lites_tool_calls_total", "lites_tool_unknown_total", "lites_realtime_connections",
		"lites_realtime_gap_recovery_seconds_bucket", "lites_invariant_violations_total",
		`deployment_environment_name="stage3-reference-production-v1"`, `service_name="agent-worker"`, `cloud_region="US"`, `outcome="unknown"`,
		`invariant="duplicate_external_effect"`, `invariant="cross_tenant_access"`, `invariant="approval_bypass"`, `invariant="terminal_state_regression"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("metric contract missing %q:\n%s", required, text)
		}
	}
	if strings.Contains(text, "private-outcome-value") {
		t.Fatalf("unbounded metric label leaked: %s", text)
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
