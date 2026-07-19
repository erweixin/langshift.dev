package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type capabilityClaimServiceStub struct {
	list   func(CapabilityClaimListQuery) (CapabilityClaimListResult, error)
	create func(CreateCapabilityClaimCommand) (CapabilityClaimMutationResult, error)
	revise func(ReviseCapabilityClaimCommand) (CapabilityClaimMutationResult, error)
}

func (stub capabilityClaimServiceStub) List(_ context.Context, query CapabilityClaimListQuery) (CapabilityClaimListResult, error) {
	return stub.list(query)
}
func (stub capabilityClaimServiceStub) Create(_ context.Context, command CreateCapabilityClaimCommand) (CapabilityClaimMutationResult, error) {
	return stub.create(command)
}
func (stub capabilityClaimServiceStub) Revise(_ context.Context, command ReviseCapabilityClaimCommand) (CapabilityClaimMutationResult, error) {
	return stub.revise(command)
}

func TestCapabilityClaimCreateAndCorrectBoundaries(t *testing.T) {
	missionID := "ca000000-0000-4000-8000-000000000001"
	capabilityID := "ca000000-0000-4000-8000-000000000002"
	correctedCapabilityID := "ca000000-0000-4000-8000-000000000003"
	identityID := "ca000000-0000-4000-8000-000000000004"
	revisionID := "ca000000-0000-4000-8000-000000000005"
	eventID := "ca000000-0000-4000-8000-000000000006"
	now := time.Now().UTC()
	service := capabilityClaimServiceStub{
		create: func(command CreateCapabilityClaimCommand) (CapabilityClaimMutationResult, error) {
			if command.MissionID != missionID || command.CapabilityID != capabilityID || command.Origin != "user_asserted" || command.Statement != "I led a production migration." || command.IdempotencyKey != "claim-create-key-0001" {
				t.Fatalf("create command=%#v", command)
			}
			return CapabilityClaimMutationResult{ID: identityID, RevisionID: revisionID, Version: 1, Status: "active", ClaimSetHash: strings.Repeat("a", 64), UpdatedAt: now, EventID: eventID}, nil
		},
		revise: func(command ReviseCapabilityClaimCommand) (CapabilityClaimMutationResult, error) {
			if command.ClaimIdentityID != identityID || command.Action != "correct" || command.ExpectedVersion != 1 || command.CapabilityID != correctedCapabilityID || command.Statement != "I supported, but did not lead, the migration." {
				t.Fatalf("revise command=%#v", command)
			}
			return CapabilityClaimMutationResult{ID: identityID, RevisionID: revisionID, Version: 2, Status: "active", ClaimSetHash: strings.Repeat("b", 64), UpdatedAt: now, EventID: eventID}, nil
		},
	}
	createBody := `{"request_id":"claim-create-request-01","mission_id":"` + missionID + `","capability_id":"` + capabilityID + `","origin":"user_asserted","statement":"I led a production migration.","evidence_ids":[]}`
	request := authenticatedMissionRequest(t, http.MethodPost, "/v1/capability-claims", "application/json", createBody, "claim-create-key-0001", "", true)
	recorder := httptest.NewRecorder()
	request.handler(CapabilityClaimHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"1"` || recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("create status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}

	reviseBody := `{"request_id":"claim-revise-request-01","action":"correct","reason":"The scope was overstated.","evidence_ids":[],"expected_claim_version":1,"capability_id":"` + correctedCapabilityID + `","statement":"I supported, but did not lead, the migration."}`
	request = authenticatedMissionRequest(t, http.MethodPost, "/v1/capability-claims/"+identityID+"/revisions", "application/json", reviseBody, "claim-revise-key-0001", `"1"`, true)
	recorder = httptest.NewRecorder()
	request.handler(CapabilityClaimHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"2"` {
		t.Fatalf("revise status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestCapabilityClaimRejectsCorrectionWithoutReplacementAndListsOwnerScope(t *testing.T) {
	identityID := "cb000000-0000-4000-8000-000000000001"
	service := capabilityClaimServiceStub{
		list: func(query CapabilityClaimListQuery) (CapabilityClaimListResult, error) {
			if query.TenantID == "" || query.UserID == "" || query.Cursor != "signed-cursor" {
				t.Fatalf("query=%#v", query)
			}
			return CapabilityClaimListResult{}, nil
		},
		revise: func(ReviseCapabilityClaimCommand) (CapabilityClaimMutationResult, error) {
			t.Fatal("invalid correction reached service")
			return CapabilityClaimMutationResult{}, nil
		},
	}
	request := authenticatedMissionRequest(t, http.MethodGet, "/v1/capability-claims?cursor=signed-cursor", "", "", "", "", false)
	recorder := httptest.NewRecorder()
	request.handler(CapabilityClaimHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	body := `{"request_id":"claim-revise-request-02","action":"correct","reason":"Incorrect.","evidence_ids":[],"expected_claim_version":1}`
	request = authenticatedMissionRequest(t, http.MethodPost, "/v1/capability-claims/"+identityID+"/revisions", "application/json", body, "claim-revise-key-0002", `"1"`, true)
	recorder = httptest.NewRecorder()
	request.handler(CapabilityClaimHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
