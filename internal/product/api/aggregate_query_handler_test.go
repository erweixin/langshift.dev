package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
)

type aggregateQueryStub struct {
	query func(AggregateQueryCommand) (AggregateQueryResult, error)
}

func (stub aggregateQueryStub) Query(_ context.Context, command AggregateQueryCommand) (AggregateQueryResult, error) {
	return stub.query(command)
}

func TestAggregateQueryReturnsOnlyAllowlistedPrecomputedResult(t *testing.T) {
	snapshotID := "c4000000-0000-4000-8000-000000000030"
	service := aggregateQueryStub{query: func(command AggregateQueryCommand) (AggregateQueryResult, error) {
		if command.SnapshotID != snapshotID || command.MetricKey != "task_completion_rate" || command.TimeBucket != "week" || command.Dimensions["cohort"] != "cohort-a" || command.IdempotencyKey != "aggregate-query-key-0001" {
			t.Fatalf("command=%#v", command)
		}
		value := .75
		return AggregateQueryResult{ID: "c4000000-0000-4000-8000-000000000031", Version: 2, Status: "returned", UpdatedAt: missionAPINow, SnapshotID: snapshotID, MetricKey: command.MetricKey, Value: &value, BudgetRemaining: 99}, nil
	}}
	request := authenticatedAggregateRequest(t, "program_manager", `{"request_id":"aggregate-client-0001","snapshot_id":"`+snapshotID+`","metric_key":"task_completion_rate","dimensions":{"cohort":"cohort-a"},"time_bucket":"week"}`, "aggregate-query-key-0001")
	recorder := httptest.NewRecorder()
	request.handler(AggregateQueryHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != aggregateQueryMediaType || recorder.Header().Get("ETag") != `"2"` {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestAggregateQueryRejectsMemberForbiddenDimensionAndCrossFilter(t *testing.T) {
	service := aggregateQueryStub{query: func(AggregateQueryCommand) (AggregateQueryResult, error) {
		t.Fatal("service called")
		return AggregateQueryResult{}, nil
	}}
	snapshotID := "c4000000-0000-4000-8000-000000000030"
	valid := `{"request_id":"aggregate-client-0002","snapshot_id":"` + snapshotID + `","metric_key":"active_members","dimensions":{"cohort":"cohort-a"},"time_bucket":"week"}`
	request := authenticatedAggregateRequest(t, "member", valid, "aggregate-query-key-0002")
	recorder := httptest.NewRecorder()
	request.handler(AggregateQueryHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("member status=%d", recorder.Code)
	}
	for _, body := range []string{
		`{"request_id":"aggregate-client-0003","snapshot_id":"` + snapshotID + `","metric_key":"active_members","dimensions":{"member_id":"person"},"time_bucket":"week"}`,
		`{"request_id":"aggregate-client-0004","snapshot_id":"` + snapshotID + `","metric_key":"active_members","dimensions":{"cohort":"a","locale":"en"},"time_bucket":"week"}`,
	} {
		request = authenticatedAggregateRequest(t, "program_manager", body, "aggregate-query-key-0003")
		recorder = httptest.NewRecorder()
		request.handler(AggregateQueryHandler{Service: service}).ServeHTTP(recorder, request.request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d", body, recorder.Code)
		}
	}
}

func TestAggregateQueryBudgetExhaustionIsAuditedThenRateLimited(t *testing.T) {
	service := aggregateQueryStub{query: func(command AggregateQueryCommand) (AggregateQueryResult, error) {
		return AggregateQueryResult{ID: "c4000000-0000-4000-8000-000000000031", Version: 101, Status: "query_budget_exhausted", UpdatedAt: missionAPINow, SnapshotID: command.SnapshotID, MetricKey: command.MetricKey}, nil
	}}
	request := authenticatedAggregateRequest(t, "owner", `{"request_id":"aggregate-client-0005","snapshot_id":"c4000000-0000-4000-8000-000000000030","metric_key":"active_members","dimensions":{"cohort":"cohort-a"},"time_bucket":"week"}`, "aggregate-query-key-0005")
	recorder := httptest.NewRecorder()
	request.handler(AggregateQueryHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func authenticatedAggregateRequest(t *testing.T, role, body, key string) signedMissionRequest {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	target := "/v1/admin/aggregate-queries"
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", aggregateQueryRequestMediaType)
	request.Header.Set(transport.IdempotencyHeader, key)
	request.Header.Set(transport.RequestIDHeader, "c4000000-0000-4000-8000-000000000014")
	fingerprint := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	claims := trustedcontext.Claims{PrincipalKind: trustedcontext.AuthenticatedUser, Issuer: "gateway", Audience: "product-service", SubjectID: "c4000000-0000-4000-8000-000000000010", TenantID: "c4000000-0000-4000-8000-000000000011", MembershipID: "c4000000-0000-4000-8000-000000000012", SessionID: "c4000000-0000-4000-8000-000000000013", Roles: []string{role}, RequestID: "c4000000-0000-4000-8000-000000000014", RequestMethod: http.MethodPost, RequestTarget: target, ClientIPHash: fingerprint, UserAgentHash: fingerprint, CSRFVerified: true, IssuedAt: missionAPINow.Unix(), ExpiresAt: missionAPINow.Add(time.Minute).Unix(), Nonce: "aggregate-nonce"}
	token, err := trustedcontext.Sign(claims, "key-1", privateKey, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(transport.TrustedContextHeader, token)
	return signedMissionRequest{request: request, keys: map[string]ed25519.PublicKey{"key-1": publicKey}}
}
