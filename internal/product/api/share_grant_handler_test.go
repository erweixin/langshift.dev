package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type shareGrantServiceStub struct {
	create func(CreateShareGrantCommand) (ShareGrantResult, error)
	revoke func(RevokeShareGrantCommand) (ShareGrantResult, error)
}

func (stub shareGrantServiceStub) Create(_ context.Context, command CreateShareGrantCommand) (ShareGrantResult, error) {
	return stub.create(command)
}
func (stub shareGrantServiceStub) Revoke(_ context.Context, command RevokeShareGrantCommand) (ShareGrantResult, error) {
	return stub.revoke(command)
}
func (shareGrantServiceStub) Authorize(context.Context, ShareGrantAccessQuery) (bool, error) {
	return false, nil
}

func TestShareGrantCreateUsesExactRevisionAndScope(t *testing.T) {
	grantee := "c4000000-0000-4000-8000-000000000020"
	resource := "c4000000-0000-4000-8000-000000000021"
	grant := "c4000000-0000-4000-8000-000000000022"
	service := shareGrantServiceStub{create: func(command CreateShareGrantCommand) (ShareGrantResult, error) {
		if command.GranteeUserID != grantee || command.ResourceKind != "workspace" || command.ResourceID != resource || command.ResourceRevision != "sha256:exact-revision" || len(command.Scope) != 2 || command.Scope[0] != "read" || command.Scope[1] != "review" || command.ExpiresAt != nil || command.IdempotencyKey != "share-grant-create-key-0001" {
			t.Fatalf("command=%#v", command)
		}
		return ShareGrantResult{ID: grant, Version: 1, Status: "active", UpdatedAt: missionAPINow}, nil
	}}
	request := authenticatedMissionRequest(t, http.MethodPost, "/v1/share-grants", shareGrantCreateMediaType, `{"request_id":"share-client-request-0001","grantee_user_id":"`+grantee+`","resource_kind":"workspace","resource_id":"`+resource+`","resource_revision":"sha256:exact-revision","scope":["read","review"],"expires_at":null}`, "share-grant-create-key-0001", "", true)
	recorder := httptest.NewRecorder()
	request.handler(ShareGrantHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"1"` || recorder.Header().Get("Content-Type") != shareGrantMediaType {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestShareGrantRevokeRequiresExactCASAndReason(t *testing.T) {
	grant := "c4000000-0000-4000-8000-000000000030"
	service := shareGrantServiceStub{revoke: func(command RevokeShareGrantCommand) (ShareGrantResult, error) {
		if command.GrantID != grant || command.Reason != "Review engagement ended" || command.ExpectedGrantVersion != 3 || command.IdempotencyKey != "share-grant-revoke-key-0001" {
			t.Fatalf("command=%#v", command)
		}
		return ShareGrantResult{ID: grant, Version: 4, Status: "revoked", UpdatedAt: missionAPINow}, nil
	}}
	request := authenticatedMissionRequest(t, http.MethodDelete, "/v1/share-grants/"+grant, shareGrantRevokeMediaType, `{"request_id":"share-client-request-0002","reason":" Review engagement ended ","expected_grant_version":3}`, "share-grant-revoke-key-0001", `"3"`, true)
	recorder := httptest.NewRecorder()
	request.handler(ShareGrantHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"4"` {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestShareGrantRejectsMissingCSRFWrongMediaAndStaleCAS(t *testing.T) {
	service := shareGrantServiceStub{
		create: func(CreateShareGrantCommand) (ShareGrantResult, error) {
			t.Fatal("create called")
			return ShareGrantResult{}, nil
		},
		revoke: func(RevokeShareGrantCommand) (ShareGrantResult, error) {
			t.Fatal("revoke called")
			return ShareGrantResult{}, nil
		},
	}
	body := `{"request_id":"share-client-request-0003","grantee_user_id":"c4000000-0000-4000-8000-000000000020","resource_kind":"project","resource_id":"c4000000-0000-4000-8000-000000000021","resource_revision":"1","scope":["read"],"expires_at":null}`
	request := authenticatedMissionRequest(t, http.MethodPost, "/v1/share-grants", shareGrantCreateMediaType, body, "share-grant-create-key-0002", "", false)
	recorder := httptest.NewRecorder()
	request.handler(ShareGrantHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("csrf status=%d", recorder.Code)
	}
	request = authenticatedMissionRequest(t, http.MethodPost, "/v1/share-grants", "application/json", body, "share-grant-create-key-0003", "", true)
	recorder = httptest.NewRecorder()
	request.handler(ShareGrantHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("media status=%d", recorder.Code)
	}
	grant := "c4000000-0000-4000-8000-000000000030"
	request = authenticatedMissionRequest(t, http.MethodDelete, "/v1/share-grants/"+grant, shareGrantRevokeMediaType, `{"request_id":"share-client-request-0004","reason":"ended","expected_grant_version":3}`, "share-grant-revoke-key-0002", `"2"`, true)
	recorder = httptest.NewRecorder()
	request.handler(ShareGrantHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("cas status=%d", recorder.Code)
	}
}
