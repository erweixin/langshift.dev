package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	onboardingRouteSessionID = "6b000000-0000-4000-8000-000000000010"
	onboardingSourceRoleID   = "6b000000-0000-4000-8000-000000000011"
	onboardingTargetRoleID   = "6b000000-0000-4000-8000-000000000012"
	onboardingClaimID        = "6b000000-0000-4000-8000-000000000013"
	onboardingMissionID      = "6b000000-0000-4000-8000-000000000014"
	onboardingRevisionID     = "6b000000-0000-4000-8000-000000000015"
)

type onboardingRouteStub struct {
	request func(context.Context, OnboardingRouteCommand) (OnboardingRouteRequestResult, error)
	get     func(context.Context, string, string, string, string) (OnboardingRouteResource, error)
}

func (stub onboardingRouteStub) RequestRoute(ctx context.Context, command OnboardingRouteCommand) (OnboardingRouteRequestResult, error) {
	if stub.request == nil {
		return OnboardingRouteRequestResult{}, errors.New("unexpected route request")
	}
	return stub.request(ctx, command)
}

func (stub onboardingRouteStub) GetRoute(ctx context.Context, sessionID, tenantID, userID, anonymousSubjectID string) (OnboardingRouteResource, error) {
	if stub.get == nil {
		return OnboardingRouteResource{}, errors.New("unexpected route read")
	}
	return stub.get(ctx, sessionID, tenantID, userID, anonymousSubjectID)
}

func TestOnboardingRoutePreviewBindsTrustedOwnerCASAndIdempotency(t *testing.T) {
	service := onboardingRouteStub{request: func(_ context.Context, command OnboardingRouteCommand) (OnboardingRouteRequestResult, error) {
		if command.OnboardingSessionID != onboardingRouteSessionID || command.TenantID != "tenant-auth" || command.UserID != "user-auth" || command.AnonymousSubjectID != "" {
			t.Fatalf("owner command=%#v", command)
		}
		if command.RequestID != "server-request-id" || command.ClientRequestID != "route-preview-request-001" || command.IdempotencyKey != "route-preview-key-000001" || command.ExpectedOnboardingVersion != 3 {
			t.Fatalf("request metadata=%#v", command)
		}
		if command.SourceRoleProfileID != onboardingSourceRoleID || command.TargetRoleProfileID != onboardingTargetRoleID || len(command.ConfirmedClaimIDs) != 2 || command.ConfirmedClaimIDs[0] != onboardingClaimID || command.ConfirmedClaimIDs[1] != "stakeholder_alignment" {
			t.Fatalf("route inputs=%#v", command)
		}
		return OnboardingRouteRequestResult{RunID: onboardingMissionID, Status: "accepted", AcceptedAt: apiTestNow, Version: 4}, nil
	}}
	target := "/v1/onboarding-sessions/" + onboardingRouteSessionID + "/route-preview"
	body := `{"request_id":"route-preview-request-001","source_role_profile_id":"` + onboardingSourceRoleID + `","target_role_profile_id":"` + onboardingTargetRoleID + `","confirmed_claim_ids":["` + onboardingClaimID + `","stakeholder_alignment"],"expected_onboarding_version":3}`
	recorder := serveAuthenticatedHandler(t, Handler{Routes: service, Now: func() time.Time { return apiTestNow }}, http.MethodPost, target, "route-preview-key-000001", `"3"`, body)
	if recorder.Code != http.StatusAccepted || recorder.Header().Get("ETag") != `"4"` || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	var response struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.RunID != onboardingMissionID || response.Status != "accepted" {
		t.Fatalf("response=%#v error=%v", response, err)
	}
}

func TestOnboardingRoutePreviewRejectsDuplicateClaimsBeforeService(t *testing.T) {
	service := onboardingRouteStub{request: func(context.Context, OnboardingRouteCommand) (OnboardingRouteRequestResult, error) {
		t.Fatal("invalid route request reached service")
		return OnboardingRouteRequestResult{}, nil
	}}
	target := "/v1/onboarding-sessions/" + onboardingRouteSessionID + "/route-preview"
	body := `{"request_id":"route-preview-request-002","source_role_profile_id":"","target_role_profile_id":"` + onboardingTargetRoleID + `","confirmed_claim_ids":["stakeholder_alignment","stakeholder_alignment"],"expected_onboarding_version":3}`
	recorder := serveAuthenticatedHandler(t, Handler{Routes: service}, http.MethodPost, target, "route-preview-key-000002", `"3"`, body)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"validation_failed"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestOnboardingRoutePreviewRejectsInvalidServiceSuccess(t *testing.T) {
	service := onboardingRouteStub{request: func(context.Context, OnboardingRouteCommand) (OnboardingRouteRequestResult, error) {
		return OnboardingRouteRequestResult{Status: "accepted", AcceptedAt: apiTestNow, Version: 4}, nil
	}}
	target := "/v1/onboarding-sessions/" + onboardingRouteSessionID + "/route-preview"
	body := `{"request_id":"route-preview-request-003","source_role_profile_id":"","target_role_profile_id":"` + onboardingTargetRoleID + `","confirmed_claim_ids":[],"expected_onboarding_version":3}`
	recorder := serveAuthenticatedHandler(t, Handler{Routes: service}, http.MethodPost, target, "route-preview-key-000003", `"3"`, body)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestOnboardingRouteGetUsesTrustedOwnerAndReturnsDurableResource(t *testing.T) {
	missionID := onboardingMissionID
	revisionID := onboardingRevisionID
	claimVersion := uint64(7)
	service := onboardingRouteStub{get: func(_ context.Context, sessionID, tenantID, userID, anonymousSubjectID string) (OnboardingRouteResource, error) {
		if sessionID != onboardingRouteSessionID || tenantID != "tenant-auth" || userID != "user-auth" || anonymousSubjectID != "" {
			t.Fatalf("owner scope session=%q tenant=%q user=%q anonymous=%q", sessionID, tenantID, userID, anonymousSubjectID)
		}
		return OnboardingRouteResource{ID: onboardingRouteSessionID, Version: 5, Status: "route_ready", MissionID: &missionID, RouteRevisionID: &revisionID, Route: json.RawMessage(`{"steps":[{"title":"First evidence"}]}`), ClaimVersion: &claimVersion, UpdatedAt: apiTestNow}, nil
	}}
	target := "/v1/onboarding-sessions/" + onboardingRouteSessionID
	recorder := serveAuthenticatedHandler(t, Handler{Routes: service}, http.MethodGet, target, "", "", "")
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"5"` || recorder.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(recorder.Body.String(), `"claim_version":7`) {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestOnboardingRouteGetRejectsMalformedServiceResource(t *testing.T) {
	service := onboardingRouteStub{get: func(context.Context, string, string, string, string) (OnboardingRouteResource, error) {
		return OnboardingRouteResource{ID: onboardingRouteSessionID, Version: 1, Status: "route_ready", Route: json.RawMessage(`{"steps":`), UpdatedAt: apiTestNow}, nil
	}}
	target := "/v1/onboarding-sessions/" + onboardingRouteSessionID
	recorder := serveAuthenticatedHandler(t, Handler{Routes: service}, http.MethodGet, target, "", "", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
