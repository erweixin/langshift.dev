package reference

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/security/transport"
)

type loadMetricsStub struct {
	mu      sync.Mutex
	percent float64
	appends int64
}

func (metrics *loadMetricsStub) SetHotTenantPercent(value float64) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.percent = value
}
func (metrics *loadMetricsStub) AddHotUserEventAppends(_ context.Context, count int64) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.appends += count
}

func TestMessageDriverUsesPublicConcurrencyAndAuthenticationContract(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		cookie, err := request.Cookie(session.CookieName)
		if err != nil || cookie.Value != "session-secret" || request.Header.Get(transport.CSRFHeader) != "csrf-secret" || request.Header.Get("Origin") != "https://capacity.example" || request.Header.Get("If-Match") != `"1"` || request.Header.Get(transport.IdempotencyHeader) != "capacity-run-test-000000000001" {
			t.Errorf("request headers=%v cookie=%#v err=%v", request.Header, cookie, err)
		}
		var body map[string]any
		if err = json.NewDecoder(request.Body).Decode(&body); err != nil || body["conversation_id"] != "conversation-1" || body["expected_conversation_version"] != float64(1) || body["mode"] != "enqueue" {
			t.Errorf("body=%#v err=%v", body, err)
		}
		if requests == 1 {
			http.Error(writer, "retry", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("ETag", `"2"`)
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"run_id":"run-1","status":"queued"}`))
	}))
	defer server.Close()
	metrics := &loadMetricsStub{}
	workload := &Workload{configuration: WorkloadConfig{GatewayURL: server.URL, PublicOrigin: "https://capacity.example", Client: server.Client(), RunID: "run-test", RequestTimeout: time.Second, Metrics: metrics}, acceptedByTen: map[string]int64{}}
	principal := &loadPrincipal{Principal: Principal{HotUser: true}, sessionToken: "session-secret", csrfToken: "csrf-secret"}
	slot := &messageSlot{principal: principal, tenant: "tenant-a", id: "conversation-1", scenario: "provider_runtime_artifact", version: 1}
	workload.sendMessage(t.Context(), slot, 1)
	if requests != 2 || workload.attempts.Load() != 1 || workload.accepted.Load() != 1 || workload.apiFailures.Load() != 0 || slot.version != 2 || metrics.appends != 3 {
		t.Fatalf("requests=%d attempts=%d accepted=%d failures=%d version=%d metrics=%#v", requests, workload.attempts.Load(), workload.accepted.Load(), workload.apiFailures.Load(), slot.version, metrics)
	}
}

func TestRealtimeDriverCountsOnlyProtocolReadyConnections(t *testing.T) {
	requestSeen := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie(session.CookieName)
		if err != nil || cookie.Value != "session-secret" || request.URL.Path != "/v1/realtime" || request.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("request=%s headers=%v cookie=%#v err=%v", request.URL.Path, request.Header, cookie, err)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("event: control\ndata: {\"kind\":\"ready\",\"cursor\":0}\n\n"))
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		close(requestSeen)
		<-request.Context().Done()
	}))
	defer server.Close()
	workload := &Workload{configuration: WorkloadConfig{GatewayURL: server.URL, Client: server.Client()}}
	principal := &loadPrincipal{sessionToken: "session-secret"}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan bool, 1)
	go func() {
		ready, _ := workload.realtimeOnce(ctx, principal)
		done <- ready
	}()
	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("realtime request was not established")
	}
	deadline := time.Now().Add(time.Second)
	for workload.connections.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if workload.connections.Load() != 1 {
		t.Fatal("connection was counted before or without ready control")
	}
	cancel()
	select {
	case ready := <-done:
		if !ready {
			t.Fatal("ready connection lost its protocol-ready state")
		}
	case <-time.After(time.Second):
		t.Fatal("realtime request did not stop")
	}
}
