package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/langshift/lites/internal/product/mission"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

var missionAPINow = time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)

type missionMutationStub struct {
	status func(ChangeMissionStatusCommand) (MissionMutationResult, error)
	focus  func(SetMissionFocusCommand) (MissionMutationResult, error)
}

func (stub missionMutationStub) ChangeStatus(_ context.Context, command ChangeMissionStatusCommand) (MissionMutationResult, error) {
	return stub.status(command)
}
func (stub missionMutationStub) SetFocus(_ context.Context, command SetMissionFocusCommand) (MissionMutationResult, error) {
	return stub.focus(command)
}

func TestMissionStatusRequiresBothCASTokens(t *testing.T) {
	missionID := "c4000000-0000-4000-8000-000000000001"
	service := missionMutationStub{status: func(command ChangeMissionStatusCommand) (MissionMutationResult, error) {
		if command.MissionID != missionID || command.Status != mission.Paused || command.ExpectedMissionVersion != 4 || command.ExpectedFocusVersion != 7 || command.ReplacementMissionID != "c4000000-0000-4000-8000-000000000002" || command.IdempotencyKey != "mission-status-key-0001" {
			t.Fatalf("command=%#v", command)
		}
		return mutationFixture(missionID, mission.Paused, 5, "c4000000-0000-4000-8000-000000000002", 8), nil
	}}
	request := authenticatedMissionRequest(t, http.MethodPatch, "/v1/missions/"+missionID, "application/vnd.lites.mission-status.v2+json", `{"request_id":"client-request-0001","status":"paused","replacement_mission_id":"c4000000-0000-4000-8000-000000000002","expected_mission_version":4,"expected_focus_version":7}`, "mission-status-key-0001", `"4"`, true)
	recorder := httptest.NewRecorder()
	request.handler(MissionMutationHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"8"` || recorder.Header().Get("Content-Type") != "application/vnd.lites.mission.v2+json" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestMissionFocusUsesPathTargetAndNullLegacyField(t *testing.T) {
	missionID := "c4000000-0000-4000-8000-000000000003"
	service := missionMutationStub{focus: func(command SetMissionFocusCommand) (MissionMutationResult, error) {
		if command.MissionID != missionID || command.ExpectedFocusVersion != 2 {
			t.Fatalf("command=%#v", command)
		}
		return mutationFixture(missionID, mission.Active, 3, missionID, 3), nil
	}}
	request := authenticatedMissionRequest(t, http.MethodPut, "/v1/missions/"+missionID+"/focus", "application/vnd.lites.mission-focus.v2+json", `{"request_id":"client-request-0002","expected_focus_version":2,"replacement_mission_id":null}`, "mission-focus-key-0001", `"2"`, true)
	recorder := httptest.NewRecorder()
	request.handler(MissionMutationHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"3"` {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestMissionMutationRejectsMissingCSRFAndUnknownFields(t *testing.T) {
	missionID := "c4000000-0000-4000-8000-000000000004"
	service := missionMutationStub{focus: func(SetMissionFocusCommand) (MissionMutationResult, error) {
		t.Fatal("service called")
		return MissionMutationResult{}, nil
	}}
	request := authenticatedMissionRequest(t, http.MethodPut, "/v1/missions/"+missionID+"/focus", "application/vnd.lites.mission-focus.v2+json", `{"request_id":"client-request-0003","expected_focus_version":0,"replacement_mission_id":null}`, "mission-focus-key-0002", `"0"`, false)
	recorder := httptest.NewRecorder()
	request.handler(MissionMutationHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request = authenticatedMissionRequest(t, http.MethodPut, "/v1/missions/"+missionID+"/focus", "application/vnd.lites.mission-focus.v2+json", `{"request_id":"client-request-0003","expected_focus_version":0,"replacement_mission_id":null,"extra":true}`, "mission-focus-key-0002", `"0"`, true)
	recorder = httptest.NewRecorder()
	request.handler(MissionMutationHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

type signedMissionRequest struct {
	request *http.Request
	keys    map[string]ed25519.PublicKey
}

func (value signedMissionRequest) handler(handler http.Handler) http.Handler {
	return serviceauth.Middleware{Verifier: trustedcontext.Verifier{Issuer: "gateway", Audience: "product-service", Keys: value.keys, MaximumTTL: 2 * time.Minute}, Now: func() time.Time { return missionAPINow }}.Wrap(handler)
}

func authenticatedMissionRequest(t *testing.T, method, target, contentType, body, key, etag string, csrf bool) signedMissionRequest {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set(transport.IdempotencyHeader, key)
	request.Header.Set("If-Match", etag)
	request.Header.Set(transport.RequestIDHeader, "c4000000-0000-4000-8000-000000000014")
	fingerprint := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 32))
	claims := trustedcontext.Claims{PrincipalKind: trustedcontext.AuthenticatedUser, Issuer: "gateway", Audience: "product-service", SubjectID: "c4000000-0000-4000-8000-000000000010", TenantID: "c4000000-0000-4000-8000-000000000011", MembershipID: "c4000000-0000-4000-8000-000000000012", SessionID: "c4000000-0000-4000-8000-000000000013", Roles: []string{"member"}, RequestID: "c4000000-0000-4000-8000-000000000014", RequestMethod: method, RequestTarget: target, ClientIPHash: fingerprint, UserAgentHash: fingerprint, CSRFVerified: csrf, IssuedAt: missionAPINow.Unix(), ExpiresAt: missionAPINow.Add(time.Minute).Unix(), Nonce: "nonce-1"}
	token, err := trustedcontext.Sign(claims, "key-1", privateKey, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(transport.TrustedContextHeader, token)
	return signedMissionRequest{request: request, keys: map[string]ed25519.PublicKey{"key-1": publicKey}}
}

func mutationFixture(id string, status mission.Status, version uint64, focusID string, focusVersion uint64) MissionMutationResult {
	var focused *string
	if focusID != "" {
		focused = &focusID
	}
	return MissionMutationResult{Mission: MissionResource{ID: id, Version: version, Status: status, TargetRoleProfileID: "c4000000-0000-4000-8000-000000000020", Focused: id == focusID && focusID != "", CreatedAt: missionAPINow.Add(-time.Hour), UpdatedAt: missionAPINow}, Focus: MissionFocus{MissionID: focused, Version: focusVersion}, EventIDs: []string{"c4000000-0000-4000-8000-000000000021"}}
}

func TestMutationResponseIsClosedJSON(t *testing.T) {
	encoded, err := json.Marshal(mutationFixture("c4000000-0000-4000-8000-000000000030", mission.Active, 1, "c4000000-0000-4000-8000-000000000030", 1))
	if err != nil || !json.Valid(encoded) {
		t.Fatal(err)
	}
}
