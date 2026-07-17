package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type supportStub struct {
	create func(CreateSupportCaseCommand) (SupportCase, error)
	list   func(SupportQuery) (SupportPage, error)
	get    func(SupportQuery, string) (SupportCase, error)
	reply  func(ReplySupportCaseCommand) (SupportCase, error)
}

func (stub supportStub) Create(_ context.Context, command CreateSupportCaseCommand) (SupportCase, error) {
	return stub.create(command)
}
func (stub supportStub) List(_ context.Context, query SupportQuery) (SupportPage, error) {
	return stub.list(query)
}
func (stub supportStub) Get(_ context.Context, query SupportQuery, id string) (SupportCase, error) {
	return stub.get(query, id)
}
func (stub supportStub) Reply(_ context.Context, command ReplySupportCaseCommand) (SupportCase, error) {
	return stub.reply(command)
}

func TestSupportCaseCreateBindsAuthenticatedTenantAndStrictPayload(t *testing.T) {
	caseID := "c5000000-0000-4000-8000-000000000001"
	service := supportStub{create: func(command CreateSupportCaseCommand) (SupportCase, error) {
		if command.TenantID != "c4000000-0000-4000-8000-000000000011" || command.UserID != "c4000000-0000-4000-8000-000000000010" || command.MembershipID != "c4000000-0000-4000-8000-000000000012" || command.ClientRequestID != "support-client-0001" || command.IdempotencyKey != "support-create-key-0001" || command.Category != "availability" || command.Priority != "urgent" || command.Subject != "Agent runs unavailable" || command.Body != "Production runs fail before scheduling." {
			t.Fatalf("command=%#v", command)
		}
		return SupportCase{ID: caseID, Reference: "LTS-20260717-AB12CD34", RequesterUserID: command.UserID, Category: command.Category, Priority: command.Priority, Status: "open", Subject: command.Subject, SupportTier: "enterprise", Version: 1, CreatedAt: missionAPINow, UpdatedAt: missionAPINow}, nil
	}}
	request := authenticatedEnterpriseRequest(t, "member", http.MethodPost, "/v1/support/cases", supportCaseCreateMediaType, `{"request_id":"support-client-0001","category":"availability","priority":"urgent","subject":" Agent runs unavailable ","body":" Production runs fail before scheduling. "}`, "support-create-key-0001", "", true)
	recorder := httptest.NewRecorder()
	request.handler(SupportHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusCreated || recorder.Header().Get("Content-Type") != supportCaseMediaType || recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("ETag") != `"1"` {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body["reference"] != "LTS-20260717-AB12CD34" || body["requester_user_id"] == nil {
		t.Fatalf("response=%s error=%v", recorder.Body.String(), err)
	}
}

func TestSupportCaseVisibilityAndReplyCarryServerEnforcedScopeAndCAS(t *testing.T) {
	caseID := "c5000000-0000-4000-8000-000000000002"
	service := supportStub{
		list: func(query SupportQuery) (SupportPage, error) {
			if query.TenantWide || query.UserID == "" || query.Cursor != "opaque.cursor" || query.Limit != 50 {
				t.Fatalf("member query=%#v", query)
			}
			return SupportPage{Items: []SupportCase{{ID: caseID, Reference: "LTS-1", RequesterUserID: query.UserID, Status: "open", Version: 1, CreatedAt: missionAPINow, UpdatedAt: missionAPINow}}}, nil
		},
		reply: func(command ReplySupportCaseCommand) (SupportCase, error) {
			if command.CaseID != caseID || command.Body != "Additional diagnostic context" || command.ExpectedCaseVersion != 3 || command.TenantWide || command.MembershipID == "" {
				t.Fatalf("reply=%#v", command)
			}
			return SupportCase{ID: caseID, Reference: "LTS-1", RequesterUserID: command.UserID, Status: "waiting_on_support", Version: 4, CreatedAt: missionAPINow, UpdatedAt: missionAPINow}, nil
		},
	}
	request := authenticatedEnterpriseRequest(t, "member", http.MethodGet, "/v1/support/cases?cursor=opaque.cursor", "", "", "", "", false)
	recorder := httptest.NewRecorder()
	request.handler(SupportHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != supportCasePageMediaType {
		t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = authenticatedEnterpriseRequest(t, "member", http.MethodPost, "/v1/support/cases/"+caseID+"/messages", supportReplyMediaType, `{"request_id":"support-client-0002","body":" Additional diagnostic context ","expected_case_version":3}`, "support-reply-key-0001", `"3"`, true)
	recorder = httptest.NewRecorder()
	request.handler(SupportHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"4"` {
		t.Fatalf("reply status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSupportCaseBoundaryRejectsMissingCSRFWrongMediaAndStalePrecondition(t *testing.T) {
	service := supportStub{create: func(CreateSupportCaseCommand) (SupportCase, error) {
		t.Fatal("create called")
		return SupportCase{}, nil
	}, reply: func(ReplySupportCaseCommand) (SupportCase, error) {
		t.Fatal("reply called")
		return SupportCase{}, nil
	}}
	body := `{"request_id":"support-client-0003","category":"product","priority":"normal","subject":"Question","body":"Detailed product question"}`
	tests := []struct {
		name, media string
		csrf        bool
		want        int
	}{
		{"missing csrf", supportCaseCreateMediaType, false, http.StatusUnauthorized},
		{"wrong media", "application/json", true, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := authenticatedEnterpriseRequest(t, "member", http.MethodPost, "/v1/support/cases", test.media, body, "support-reject-key-0001", "", test.csrf)
			recorder := httptest.NewRecorder()
			request.handler(SupportHandler{Service: service}).ServeHTTP(recorder, request.request)
			if recorder.Code != test.want {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	caseID := "c5000000-0000-4000-8000-000000000003"
	request := authenticatedEnterpriseRequest(t, "member", http.MethodPost, "/v1/support/cases/"+caseID+"/messages", supportReplyMediaType, `{"request_id":"support-client-0004","body":"Context","expected_case_version":2}`, "support-reject-key-0002", `"1"`, true)
	recorder := httptest.NewRecorder()
	request.handler(SupportHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
