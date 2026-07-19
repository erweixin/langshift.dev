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

type aggregateSnapshotStub struct {
	create func(AggregateSnapshotCommand) (AggregateSnapshotResult, error)
}

func (stub aggregateSnapshotStub) Create(_ context.Context, command AggregateSnapshotCommand) (AggregateSnapshotResult, error) {
	return stub.create(command)
}

func TestAggregateSnapshotHandlerCreatesFixedAllowlistedSnapshot(t *testing.T) {
	service := aggregateSnapshotStub{create: func(command AggregateSnapshotCommand) (AggregateSnapshotResult, error) {
		if command.MetricKey != "active_members" || command.Dimensions["cohort"] != "c4000000-0000-4000-8000-000000000020" || command.TimeBucket != "week" || command.PeriodStart.Format(time.DateOnly) != "2026-07-13" || command.PeriodEnd.Format(time.DateOnly) != "2026-07-19" || command.IdempotencyKey != "aggregate-snapshot-key-0001" {
			t.Fatalf("command=%#v", command)
		}
		return AggregateSnapshotResult{ID: "c4000000-0000-4000-8000-000000000030", Version: 1, Status: "frozen", MetricKey: command.MetricKey, Dimensions: command.Dimensions, TimeBucket: command.TimeBucket, PeriodStart: "2026-07-13", PeriodEnd: "2026-07-19", PopulationCount: 10, CellCount: 5, SourceHighWatermark: "sha256:" + string(bytes.Repeat([]byte{'a'}, 64)), FrozenAt: missionAPINow}, nil
	}}
	request := authenticatedAggregateSnapshotRequest(t, "program_manager", `{"request_id":"aggregate-snapshot-client-0001","metric_key":"active_members","dimensions":{"cohort":"c4000000-0000-4000-8000-000000000020"},"time_bucket":"week","period_start":"2026-07-13","period_end":"2026-07-19"}`, "aggregate-snapshot-key-0001")
	recorder := httptest.NewRecorder()
	request.handler(AggregateSnapshotHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/json" || recorder.Header().Get("ETag") != `"1"` {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestAggregateSnapshotHandlerRejectsMemberFreeFormAndOversizedPeriod(t *testing.T) {
	service := aggregateSnapshotStub{create: func(AggregateSnapshotCommand) (AggregateSnapshotResult, error) {
		t.Fatal("service called")
		return AggregateSnapshotResult{}, nil
	}}
	valid := `{"request_id":"aggregate-snapshot-client-0002","metric_key":"active_members","dimensions":{"locale":"en"},"time_bucket":"week","period_start":"2026-07-13","period_end":"2026-07-19"}`
	request := authenticatedAggregateSnapshotRequest(t, "member", valid, "aggregate-snapshot-key-0002")
	recorder := httptest.NewRecorder()
	request.handler(AggregateSnapshotHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("member status=%d", recorder.Code)
	}
	for _, body := range []string{
		`{"request_id":"aggregate-snapshot-client-0003","metric_key":"active_members","dimensions":{"member_id":"person"},"time_bucket":"week","period_start":"2026-07-13","period_end":"2026-07-19"}`,
		`{"request_id":"aggregate-snapshot-client-0004","metric_key":"active_members","dimensions":{"locale":"en"},"time_bucket":"week","period_start":"2026-07-01","period_end":"2026-07-19"}`,
	} {
		request = authenticatedAggregateSnapshotRequest(t, "owner", body, "aggregate-snapshot-key-0003")
		recorder = httptest.NewRecorder()
		request.handler(AggregateSnapshotHandler{Service: service}).ServeHTTP(recorder, request.request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d", body, recorder.Code)
		}
	}
}

func authenticatedAggregateSnapshotRequest(t *testing.T, role, body, key string) signedMissionRequest {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	target := "/v1/admin/aggregate-snapshots"
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(transport.IdempotencyHeader, key)
	request.Header.Set(transport.RequestIDHeader, "c4000000-0000-4000-8000-000000000014")
	fingerprint := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x43}, 32))
	claims := trustedcontext.Claims{PrincipalKind: trustedcontext.AuthenticatedUser, Issuer: "gateway", Audience: "product-service", SubjectID: "c4000000-0000-4000-8000-000000000010", TenantID: "c4000000-0000-4000-8000-000000000011", MembershipID: "c4000000-0000-4000-8000-000000000012", SessionID: "c4000000-0000-4000-8000-000000000013", Roles: []string{role}, RequestID: "c4000000-0000-4000-8000-000000000014", RequestMethod: http.MethodPost, RequestTarget: target, ClientIPHash: fingerprint, UserAgentHash: fingerprint, CSRFVerified: true, IssuedAt: missionAPINow.Unix(), ExpiresAt: missionAPINow.Add(time.Minute).Unix(), Nonce: "aggregate-snapshot-nonce"}
	token, err := trustedcontext.Sign(claims, "key-1", privateKey, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(transport.TrustedContextHeader, token)
	return signedMissionRequest{request: request, keys: map[string]ed25519.PublicKey{"key-1": publicKey}}
}
