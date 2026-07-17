package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type routeServiceStub struct {
	list     func(RouteListQuery) (RouteListResult, error)
	generate func(GenerateRouteCommand) (RouteGenerationResult, error)
	accept   func(AcceptRouteCommand) (RouteAcceptanceResult, error)
}

func (stub routeServiceStub) List(_ context.Context, query RouteListQuery) (RouteListResult, error) {
	return stub.list(query)
}

func (stub routeServiceStub) Generate(_ context.Context, command GenerateRouteCommand) (RouteGenerationResult, error) {
	return stub.generate(command)
}

func (stub routeServiceStub) Accept(_ context.Context, command AcceptRouteCommand) (RouteAcceptanceResult, error) {
	return stub.accept(command)
}

func TestRouteGenerateRequiresMissionAndClaimCAS(t *testing.T) {
	missionID := "b6000000-0000-4000-8000-000000000001"
	revisionID := "b6000000-0000-4000-8000-000000000002"
	commandID := "b6000000-0000-4000-8000-000000000003"
	eventID := "b6000000-0000-4000-8000-000000000004"
	claimHash := strings.Repeat("a", 64)
	service := routeServiceStub{generate: func(command GenerateRouteCommand) (RouteGenerationResult, error) {
		if command.MissionID != missionID || command.ExpectedRouteVersion != 7 || command.ExpectedClaimSetHash != claimHash || command.IdempotencyKey != "route-generate-key-0001" {
			t.Fatalf("command=%#v", command)
		}
		return RouteGenerationResult{RouteRevisionID: revisionID, RevisionVersion: 1, Status: "generating", PlannerCommandID: commandID, EventID: eventID}, nil
	}}
	request := authenticatedMissionRequest(t, http.MethodPost, "/v1/route-revisions", "application/vnd.lites.route-generate.v2+json", `{"request_id":"client-route-generate-0001","mission_id":"`+missionID+`","expected_route_version":7,"expected_claim_set_hash":"`+claimHash+`"}`, "route-generate-key-0001", `"7"`, true)
	recorder := httptest.NewRecorder()
	request.handler(RouteHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusAccepted || recorder.Header().Get("ETag") != `"1"` || recorder.Header().Get("Content-Type") != "application/vnd.lites.route-generation.v2+json" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestRouteAcceptRequiresRevisionMissionAndClaimCAS(t *testing.T) {
	revisionID := "b6000000-0000-4000-8000-000000000010"
	missionID := "b6000000-0000-4000-8000-000000000011"
	eventID := "b6000000-0000-4000-8000-000000000012"
	claimHash := strings.Repeat("b", 64)
	service := routeServiceStub{accept: func(command AcceptRouteCommand) (RouteAcceptanceResult, error) {
		if command.RouteRevisionID != revisionID || command.ExpectedRevisionVersion != 2 || command.ExpectedRouteVersion != 4 || command.ExpectedClaimSetHash != claimHash {
			t.Fatalf("command=%#v", command)
		}
		return RouteAcceptanceResult{RouteRevisionID: revisionID, RevisionVersion: 3, MissionID: missionID, MissionVersion: 6, RouteVersion: 5, CurrentRouteRevisionID: revisionID, Status: "accepted", EventID: eventID}, nil
	}}
	target := "/v1/route-revisions/" + revisionID + "/accept"
	request := authenticatedMissionRequest(t, http.MethodPost, target, "application/vnd.lites.route-accept.v2+json", `{"request_id":"client-route-accept-0001","expected_revision_version":2,"expected_route_version":4,"expected_claim_set_hash":"`+claimHash+`"}`, "route-accept-key-0001", `"2"`, true)
	recorder := httptest.NewRecorder()
	request.handler(RouteHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"3"` || recorder.Header().Get("Content-Type") != "application/vnd.lites.route-acceptance.v2+json" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestRouteListRequiresOwnerMissionScopeAndRejectsUnknownQuery(t *testing.T) {
	missionID := "b6000000-0000-4000-8000-000000000020"
	service := routeServiceStub{list: func(query RouteListQuery) (RouteListResult, error) {
		if query.MissionID != missionID || query.Cursor != "signed-cursor" || query.TenantID == "" || query.UserID == "" {
			t.Fatalf("query=%#v", query)
		}
		return RouteListResult{}, nil
	}}
	request := authenticatedMissionRequest(t, http.MethodGet, "/v1/route-revisions?mission_id="+missionID+"&cursor=signed-cursor", "", "", "", "", false)
	recorder := httptest.NewRecorder()
	request.handler(RouteHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/vnd.lites.route-revisions.v2+json" {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request = authenticatedMissionRequest(t, http.MethodGet, "/v1/route-revisions?mission_id="+missionID+"&offset=1", "", "", "", "", false)
	recorder = httptest.NewRecorder()
	request.handler(RouteHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRouteMutationRequiresCSRF(t *testing.T) {
	service := routeServiceStub{generate: func(GenerateRouteCommand) (RouteGenerationResult, error) {
		t.Fatal("service called")
		return RouteGenerationResult{}, nil
	}}
	missionID := "b6000000-0000-4000-8000-000000000030"
	claimHash := strings.Repeat("c", 64)
	request := authenticatedMissionRequest(t, http.MethodPost, "/v1/route-revisions", "application/vnd.lites.route-generate.v2+json", `{"request_id":"client-route-generate-0002","mission_id":"`+missionID+`","expected_route_version":0,"expected_claim_set_hash":"`+claimHash+`"}`, "route-generate-key-0002", `"0"`, false)
	recorder := httptest.NewRecorder()
	request.handler(RouteHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
