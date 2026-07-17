package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/langshift/lites/internal/product/mission"
)

type missionQueryStub struct {
	list func(ListMissionsQuery) (MissionListResult, error)
	get  func(GetMissionQuery) (MissionResource, error)
}

type missionCreateStub struct {
	create func(CreateMissionCommand) (MissionMutationResult, error)
}

func (stub missionCreateStub) Create(_ context.Context, command CreateMissionCommand) (MissionMutationResult, error) {
	return stub.create(command)
}

func (stub missionQueryStub) List(_ context.Context, query ListMissionsQuery) (MissionListResult, error) {
	return stub.list(query)
}

func (stub missionQueryStub) Get(_ context.Context, query GetMissionQuery) (MissionResource, error) {
	return stub.get(query)
}

func TestMissionListReturnsFocusFromSameServiceSnapshot(t *testing.T) {
	missionID := "c5000000-0000-4000-8000-000000000001"
	cursor := "signed-next-cursor"
	queries := missionQueryStub{list: func(query ListMissionsQuery) (MissionListResult, error) {
		if query.TenantID != "c4000000-0000-4000-8000-000000000011" || query.UserID != "c4000000-0000-4000-8000-000000000010" || query.Cursor != "signed-cursor" {
			t.Fatalf("query=%#v", query)
		}
		return MissionListResult{Items: []MissionResource{{ID: missionID, Version: 3, Status: mission.Active, TargetRoleProfileID: "c5000000-0000-4000-8000-000000000002", Focused: true, CreatedAt: missionAPINow, UpdatedAt: missionAPINow}}, Focus: MissionFocus{MissionID: &missionID, Version: 7}, NextCursor: &cursor}, nil
	}}
	request := authenticatedMissionRequest(t, http.MethodGet, "/v1/missions?cursor=signed-cursor", "", "", "", "", false)
	recorder := httptest.NewRecorder()
	request.handler(MissionHandler{Queries: queries}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/vnd.lites.missions.v2+json" || recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	var result MissionListResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil || result.Focus.Version != 7 || result.NextCursor == nil || *result.NextCursor != cursor || len(result.Items) != 1 || !result.Items[0].Focused {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestMissionGetIsOwnerScopedAndReturnsResourceETag(t *testing.T) {
	missionID := "c5000000-0000-4000-8000-000000000003"
	queries := missionQueryStub{get: func(query GetMissionQuery) (MissionResource, error) {
		if query.MissionID != missionID || query.UserID == "" || query.TenantID == "" {
			t.Fatalf("query=%#v", query)
		}
		return MissionResource{ID: missionID, Version: 4, Status: mission.Paused, TargetRoleProfileID: "c5000000-0000-4000-8000-000000000004", CreatedAt: missionAPINow, UpdatedAt: missionAPINow}, nil
	}}
	request := authenticatedMissionRequest(t, http.MethodGet, "/v1/missions/"+missionID, "", "", "", "", false)
	recorder := httptest.NewRecorder()
	request.handler(MissionHandler{Queries: queries}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"4"` || recorder.Header().Get("Content-Type") != "application/vnd.lites.mission.v2+json" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestMissionCreateRequiresCSRFAndReturnsIdempotentResource(t *testing.T) {
	missionID := "c5000000-0000-4000-8000-000000000006"
	targetID := "c5000000-0000-4000-8000-000000000007"
	creates := missionCreateStub{create: func(command CreateMissionCommand) (MissionMutationResult, error) {
		if command.SourceRoleProfileID != "" || command.TargetRoleProfileID != targetID || command.Goal != "Build reliable cloud agents" || command.IdempotencyKey != "mission-create-key-0001" || command.ClientRequestID != "client-request-create-0001" {
			t.Fatalf("command=%#v", command)
		}
		return mutationFixture(missionID, mission.Draft, 1, "", 0), nil
	}}
	request := authenticatedMissionRequest(t, http.MethodPost, "/v1/missions", "application/vnd.lites.mission-create.v2+json", `{"request_id":"client-request-create-0001","source_role_profile_id":null,"target_role_profile_id":"`+targetID+`","goal":"Build reliable cloud agents"}`, "mission-create-key-0001", "", true)
	recorder := httptest.NewRecorder()
	request.handler(MissionHandler{Creates: creates}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"1"` || recorder.Header().Get("Content-Type") != "application/vnd.lites.mission.v2+json" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	request = authenticatedMissionRequest(t, http.MethodPost, "/v1/missions", "application/vnd.lites.mission-create.v2+json", `{"request_id":"client-request-create-0001","source_role_profile_id":null,"target_role_profile_id":"`+targetID+`","goal":"Build reliable cloud agents"}`, "mission-create-key-0001", "", false)
	recorder = httptest.NewRecorder()
	request.handler(MissionHandler{Creates: creates}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestMissionQueryRejectsUnknownCursorAndHidesCrossScopeMiss(t *testing.T) {
	queries := missionQueryStub{
		list: func(ListMissionsQuery) (MissionListResult, error) {
			t.Fatal("list called")
			return MissionListResult{}, nil
		},
		get: func(GetMissionQuery) (MissionResource, error) { return MissionResource{}, ErrResourceNotFound },
	}
	request := authenticatedMissionRequest(t, http.MethodGet, "/v1/missions?offset=1", "", "", "", "", false)
	recorder := httptest.NewRecorder()
	request.handler(MissionHandler{Queries: queries}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request = authenticatedMissionRequest(t, http.MethodGet, "/v1/missions/c5000000-0000-4000-8000-000000000005", "", "", "", "", false)
	recorder = httptest.NewRecorder()
	request.handler(MissionHandler{Queries: queries}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestMissionQueryMapsDependencyFailureSafely(t *testing.T) {
	queries := missionQueryStub{list: func(ListMissionsQuery) (MissionListResult, error) {
		return MissionListResult{}, errors.New("database details")
	}}
	request := authenticatedMissionRequest(t, http.MethodGet, "/v1/missions", "", "", "", "", false)
	recorder := httptest.NewRecorder()
	request.handler(MissionHandler{Queries: queries}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusServiceUnavailable || recorder.Body.String() == "database details" {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
