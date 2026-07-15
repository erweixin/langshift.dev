package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/behavior"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

var behaviorAPINow = time.Date(2026, time.July, 15, 10, 0, 0, 0, time.UTC)

type controlServiceStub struct {
	createCommand     CreateSnapshotCommand
	evaluationCommand RecordEvaluationCommand
	promotionCommand  PromoteCommand
	currentTenant     string
	currentProfile    behavior.Profile
	currentEnv        string
	mutation          MutationResult
	binding           behavior.ChannelBinding
	err               error
}

func (stub *controlServiceStub) CreateSnapshot(_ context.Context, command CreateSnapshotCommand) (MutationResult, error) {
	stub.createCommand = command
	return stub.mutation, stub.err
}

func (stub *controlServiceStub) RecordEvaluation(_ context.Context, command RecordEvaluationCommand) (MutationResult, error) {
	stub.evaluationCommand = command
	return stub.mutation, stub.err
}

func (stub *controlServiceStub) Promote(_ context.Context, command PromoteCommand) (MutationResult, error) {
	stub.promotionCommand = command
	return stub.mutation, stub.err
}

func (stub *controlServiceStub) Current(_ context.Context, tenantID string, profile behavior.Profile, environment string) (behavior.ChannelBinding, error) {
	stub.currentTenant, stub.currentProfile, stub.currentEnv = tenantID, profile, environment
	return stub.binding, stub.err
}

type rollbackServiceStub struct {
	command AutomaticRollbackCommand
	result  MutationResult
	err     error
}

func (stub *rollbackServiceStub) AutomaticRollback(_ context.Context, command AutomaticRollbackCommand) (MutationResult, error) {
	stub.command = command
	return stub.result, stub.err
}

func TestHandlerCreatesSnapshotFromTrustedAdminScope(t *testing.T) {
	stub := &controlServiceStub{mutation: validMutation()}
	body := `{"request_id":"client-behavior-001","manifest":{"schema_version":1,"profile":"route_planner","model":{"id":"m","version":"v1","hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"prompt":{"id":"p","version":"v1","hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"tools":[],"profile_definition":{"id":"profile","version":"v1","hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"guardrail_policy":{"id":"guard","version":"v1","hash":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},"router_policy":{"id":"router","version":"v1","hash":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},"source_commit":"ffffffffffffffffffffffffffffffffffffffff","created_at":"2026-07-15T09:00:00Z"}}`
	recorder := serveAdmin(t, Handler{Service: stub}, http.MethodPost, "/v1/admin/behavior/snapshots", body, []string{"admin"}, true, true)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	command := stub.createCommand
	if command.RequestID != "server-behavior-request" || command.ClientRequestID != "client-behavior-001" || command.IdempotencyKey != "behavior-idempotency-key-0001" || command.TenantID != "tenant-1" || command.UserID != "user-1" || command.SessionID != "session-1" || command.Manifest.Profile != behavior.RoutePlanner {
		t.Fatalf("command=%#v", command)
	}
}

func TestHandlerReadsCurrentChannelWithoutMutationHeaders(t *testing.T) {
	stub := &controlServiceStub{binding: behavior.ChannelBinding{ChannelID: "channel-1", Sequence: 8, SnapshotID: "behavior-" + repeat("a", 64), Profile: behavior.Coach, Environment: "production", ActivatedAt: behaviorAPINow}}
	recorder := serveAdmin(t, Handler{Service: stub}, http.MethodGet, "/v1/admin/behavior/channels/coach/production", "", []string{"owner"}, false, false)
	if recorder.Code != http.StatusOK || stub.currentTenant != "tenant-1" || stub.currentProfile != behavior.Coach || stub.currentEnv != "production" {
		t.Fatalf("status=%d scope=%q/%q/%q body=%s", recorder.Code, stub.currentTenant, stub.currentProfile, stub.currentEnv, recorder.Body.String())
	}
}

func TestHandlerFailsClosedAtAdminBoundary(t *testing.T) {
	tests := []struct {
		name, body string
		roles      []string
		csrf, key  bool
		status     int
		code       string
	}{
		{"member role", `{"request_id":"client-behavior-001","manifest":{}}`, []string{"member"}, true, true, http.StatusForbidden, "permission_denied"},
		{"missing csrf", `{"request_id":"client-behavior-001","manifest":{}}`, []string{"admin"}, false, true, http.StatusUnauthorized, "authentication_required"},
		{"missing key", `{"request_id":"client-behavior-001","manifest":{}}`, []string{"admin"}, true, false, http.StatusBadRequest, "validation_failed"},
		{"unknown field", `{"request_id":"client-behavior-001","manifest":{},"authority":"browser"}`, []string{"admin"}, true, true, http.StatusBadRequest, "validation_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := serveAdmin(t, Handler{Service: &controlServiceStub{}}, http.MethodPost, "/v1/admin/behavior/snapshots", test.body, test.roles, test.csrf, test.key)
			assertBehaviorProblem(t, recorder, test.status, test.code)
		})
	}
}

func TestRollbackHandlerRequiresExactVerifiedSPIFFEIdentity(t *testing.T) {
	stub := &rollbackServiceStub{result: validMutation()}
	handler := RollbackHandler{Service: stub, RequireVerifiedClientCertificate: true, AllowedClientSPIFFEID: "spiffe://lites.internal/behavior-rollback-controller"}
	body := `{"request_id":"rollback-request-001","rollback":{"schema_version":1,"tenant_id":"tenant-1","environment":"production","profile":"coach","sequence":2,"from_snapshot_id":"behavior-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","to_snapshot_id":"behavior-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","trigger":"error_rate","observed_value":0.02,"threshold":0.01,"incident_evidence_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","automation_key_id":"rollback-key","occurred_at":"2026-07-15T09:59:59Z","signature":"signature"}}`
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/behavior/rollbacks", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(transport.RequestIDHeader, "server-rollback-request")
	request.Header.Set(transport.IdempotencyHeader, "rollback-idempotency-key-001")
	identity, _ := url.Parse("spiffe://lites.internal/behavior-rollback-controller")
	request.TLS = verifiedClient(identity)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || stub.command.Request.TenantID != "tenant-1" || stub.command.RequestID != "server-rollback-request" || stub.command.ClientRequestID != "rollback-request-001" {
		t.Fatalf("status=%d command=%#v body=%s", recorder.Code, stub.command, recorder.Body.String())
	}

	wrongRequest := httptest.NewRequest(http.MethodPost, "/internal/v1/behavior/rollbacks", bytes.NewBufferString(body))
	wrongRequest.Header = request.Header.Clone()
	wrongIdentity, _ := url.Parse("spiffe://lites.internal/another-service")
	wrongRequest.TLS = verifiedClient(wrongIdentity)
	wrongRecorder := httptest.NewRecorder()
	handler.ServeHTTP(wrongRecorder, wrongRequest)
	if wrongRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("wrong identity status=%d body=%s", wrongRecorder.Code, wrongRecorder.Body.String())
	}
}

func serveAdmin(t *testing.T, handler http.Handler, method, target, body string, roles []string, csrf, key bool) *httptest.ResponseRecorder {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x61}, ed25519.SeedSize))
	claims := trustedcontext.Claims{
		PrincipalKind: trustedcontext.AuthenticatedUser, Issuer: "gateway", Audience: "behavior-control-plane",
		SubjectID: "user-1", TenantID: "tenant-1", MembershipID: "membership-1", SessionID: "session-1", Roles: roles,
		RequestID: "server-behavior-request", RequestMethod: method, RequestTarget: target,
		ClientIPHash: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x71}, 32)), UserAgentHash: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x72}, 32)),
		CSRFVerified: csrf, IssuedAt: behaviorAPINow.Unix(), ExpiresAt: behaviorAPINow.Add(time.Minute).Unix(), Nonce: "behavior-nonce",
	}
	token, err := trustedcontext.Sign(claims, "key", privateKey, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set(transport.RequestIDHeader, claims.RequestID)
	request.Header.Set(transport.TrustedContextHeader, token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key {
		request.Header.Set(transport.IdempotencyHeader, "behavior-idempotency-key-0001")
	}
	recorder := httptest.NewRecorder()
	verified := serviceauth.Middleware{Verifier: trustedcontext.Verifier{Issuer: "gateway", Audience: "behavior-control-plane", Keys: map[string]ed25519.PublicKey{"key": privateKey.Public().(ed25519.PublicKey)}, MaximumTTL: 2 * time.Minute}, Now: func() time.Time { return behaviorAPINow }}.Wrap(handler)
	verified.ServeHTTP(recorder, request)
	return recorder
}

func assertBehaviorProblem(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); recorder.Code != status || err != nil || body.Code != code {
		t.Fatalf("status=%d problem=%#v err=%v body=%s", recorder.Code, body, err, recorder.Body.String())
	}
}

func validMutation() MutationResult {
	return MutationResult{ID: "30000000-0000-4000-8000-000000000001", ResourceID: "behavior-" + repeat("a", 64), EventID: "40000000-0000-4000-8000-000000000001", Hash: repeat("b", 64), Sequence: 1}
}

func verifiedClient(identity *url.URL) *tls.ConnectionState {
	return &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{URIs: []*url.URL{identity}}}}}
}

func repeat(value string, count int) string {
	return strings.Repeat(value, count)
}
