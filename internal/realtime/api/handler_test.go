package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/realtime"
	realtimepostgres "github.com/langshift/lites/internal/realtime/postgres"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

var handlerTestNow = time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)

type apiStore struct {
	events           []realtimepostgres.Event
	high             uint64
	tenantID, userID string
}

func (store *apiStore) HighWatermark(_ context.Context, tenantID, userID string) (uint64, error) {
	store.tenantID, store.userID = tenantID, userID
	return store.high, nil
}

func (store *apiStore) List(_ context.Context, tenantID, userID string, after, through uint64, limit int) ([]realtimepostgres.Event, error) {
	store.tenantID, store.userID = tenantID, userID
	result := make([]realtimepostgres.Event, 0, limit)
	for _, event := range store.events {
		if event.Sequence > after && event.Sequence <= through && len(result) < limit {
			result = append(result, event)
		}
	}
	return result, nil
}

type apiStream struct {
	session realtime.Session
	events  []realtimepostgres.Event
}

func (stream *apiStream) Run(ctx context.Context, session realtime.Session, sink realtime.Sink) error {
	stream.session = session
	for _, event := range stream.events {
		if err := sink.Event(ctx, event); err != nil {
			return err
		}
	}
	return sink.Control(ctx, realtime.Control{Kind: "ready", Cursor: session.AfterSequence + uint64(len(stream.events))})
}

func TestEventsListUsesVerifiedTenantUserScopeAndHidesPayloadMetadata(t *testing.T) {
	store := &apiStore{high: 3, events: []realtimepostgres.Event{
		{ID: "event-1", Sequence: 1, EventType: "RunQueued", SchemaVersion: 1, AggregateKind: "run", AggregateID: "run-1", AggregateVersion: 1, StoreEpoch: "epoch-1", OccurredAt: handlerTestNow, CommittedAt: handlerTestNow, CorrelationID: "correlation-1", PayloadRef: "s3://restricted/secret", PayloadHash: "secret-hash", Actor: []byte(`{"email":"secret@example.com"}`)},
		{ID: "event-2", Sequence: 2, EventType: "RunStarted", SchemaVersion: 1, AggregateKind: "run", AggregateID: "run-1", AggregateVersion: 2, StoreEpoch: "epoch-1", OccurredAt: handlerTestNow, CommittedAt: handlerTestNow, CorrelationID: "correlation-1"},
		{ID: "event-3", Sequence: 3},
	}}
	handler := authenticatedHandler(t, Handler{Store: store, PageSize: 2})
	request := httptest.NewRequest(http.MethodGet, "/v1/events?after_seq=0", nil)
	request.Header.Set(transport.RequestIDHeader, "request-events")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if store.tenantID != "tenant-1" || store.userID != "user-1" {
		t.Fatalf("store scope = %q/%q", store.tenantID, store.userID)
	}
	var response struct {
		Events        []map[string]any `json:"events"`
		HighWatermark uint64           `json:"high_watermark"`
		NextAfterSeq  *uint64          `json:"next_after_seq"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Events) != 2 || response.HighWatermark != 3 || response.NextAfterSeq == nil || *response.NextAfterSeq != 2 {
		t.Fatalf("response = %#v", response)
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"payload_ref", "payload_hash", "secret@example.com", "restricted/secret"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response exposed %q: %s", forbidden, body)
		}
	}
}

func TestRealtimeUsesLastEventIDAndProducesStrictSSE(t *testing.T) {
	stream := &apiStream{events: []realtimepostgres.Event{{ID: "event-8", Sequence: 8, EventType: "RunCompleted", SchemaVersion: 1, AggregateKind: "run", AggregateID: "run-1", AggregateVersion: 8, StoreEpoch: "epoch-1", OccurredAt: handlerTestNow, CommittedAt: handlerTestNow, CorrelationID: "correlation-1", PayloadRef: "secret"}}}
	handler := authenticatedHandler(t, Handler{Stream: stream})
	request := httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
	request.Header.Set("Last-Event-ID", "7")
	request.Header.Set(transport.RequestIDHeader, "request-realtime")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/event-stream" || recorder.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("status/headers = %d %#v", recorder.Code, recorder.Header())
	}
	if stream.session.TenantID != "tenant-1" || stream.session.UserID != "user-1" || stream.session.AfterSequence != 7 || !stream.session.ExpiresAt.Equal(handlerTestNow.Add(time.Minute)) {
		t.Fatalf("session = %#v", stream.session)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "id: 8\nevent: event\ndata:") || !strings.Contains(body, `"event_type":"RunCompleted"`) || !strings.Contains(body, "event: control") || strings.Contains(body, "secret") {
		t.Fatalf("SSE body = %q", body)
	}
}

func TestEventsListPreservesCallerSnapshotBoundaryAcrossPages(t *testing.T) {
	store := &apiStore{high: 5, events: []realtimepostgres.Event{{Sequence: 3}}}
	handler := authenticatedHandler(t, Handler{Store: store, PageSize: 2})
	request := httptest.NewRequest(http.MethodGet, "/v1/events?after_seq=2&through_seq=3", nil)
	request.Header.Set(transport.RequestIDHeader, "request-events-page")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		HighWatermark uint64  `json:"high_watermark"`
		NextAfterSeq  *uint64 `json:"next_after_seq"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.HighWatermark != 3 || response.NextAfterSeq != nil {
		t.Fatalf("response = %#v", response)
	}
}

func TestRealtimeRejectsAmbiguousCursorBeforeStartingSSE(t *testing.T) {
	handler := authenticatedHandler(t, Handler{Stream: &apiStream{}})
	request := httptest.NewRequest(http.MethodGet, "/v1/realtime?after_seq=6", nil)
	request.Header.Set("Last-Event-ID", "7")
	request.Header.Set(transport.RequestIDHeader, "request-ambiguous")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status/headers = %d %#v", recorder.Code, recorder.Header())
	}
}

func TestRealtimeRejectsUnverifiedRequest(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
	recorder := httptest.NewRecorder()
	Handler{Stream: &apiStream{}}.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func authenticatedHandler(t *testing.T, application http.Handler) http.Handler {
	t.Helper()
	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	verifier := trustedcontext.Verifier{Issuer: "gateway", Audience: "realtime", Keys: map[string]ed25519.PublicKey{"key-1": publicKey}, MaximumTTL: 5 * time.Minute}
	middleware := serviceauth.Middleware{Verifier: verifier, Now: func() time.Time { return handlerTestNow }}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fingerprint := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32))
		claims := trustedcontext.Claims{PrincipalKind: trustedcontext.AuthenticatedUser, Issuer: "gateway", Audience: "realtime", SubjectID: "user-1", TenantID: "tenant-1", MembershipID: "membership-1", SessionID: "session-1", Roles: []string{"member"}, RequestID: request.Header.Get(transport.RequestIDHeader), RequestMethod: request.Method, RequestTarget: request.URL.RequestURI(), ClientIPHash: fingerprint, UserAgentHash: fingerprint, IssuedAt: handlerTestNow.Unix(), ExpiresAt: handlerTestNow.Add(time.Minute).Unix(), Nonce: "nonce-1"}
		token, err := trustedcontext.Sign(claims, "key-1", privateKey, 5*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set(transport.TrustedContextHeader, token)
		middleware.Wrap(application).ServeHTTP(writer, request)
	})
}
