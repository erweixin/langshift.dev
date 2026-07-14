package api

import (
	"context"
	"net/http"
	"testing"
)

type approvalServiceStub struct {
	command DecideApprovalCommand
	result  ApprovalResult
	err     error
}

func (stub *approvalServiceStub) DecideApproval(_ context.Context, command DecideApprovalCommand) (ApprovalResult, error) {
	stub.command = command
	return stub.result, stub.err
}

func TestApprovalHandlerSeparatesUserAndAdminAuthorizationModes(t *testing.T) {
	tests := []struct {
		name, path, body, mode, decision, permission string
		roles                                        []string
		targetVersion                                uint64
	}{
		{"user revise", "/v1/approvals/approval-1/decisions", `{"request_id":"client-approval-001","decision":"revise","proposal_hash":"proposal-hash","expected_target_version":7}`, "user", "revise", "", []string{"member"}, 7},
		{"admin approve", "/v1/admin/approval-requests/approval-2/decisions", `{"request_id":"client-approval-002","decision":"approve","proposal_hash":"proposal-hash","target_version":8,"permission_snapshot":"membership:m1:v4:role:member"}`, "admin", "approve", "membership:m1:v4:role:member", []string{"admin"}, 8},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &approvalServiceStub{result: ApprovalResult{ID: "approval-result", Version: 2, Status: "granted", UpdatedAt: repairAPINow}}
			recorder := serveRepairRequest(t, ApprovalHandler{Service: stub}, http.MethodPost, test.path, test.body, test.roles, `"1"`)
			if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"2"` {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			command := stub.command
			if command.ApprovalID == "" || command.Mode != test.mode || command.Decision != test.decision || command.TargetVersion != test.targetVersion || command.PermissionSnapshot != test.permission || command.ExpectedApprovalVersion != 1 || command.TenantID != "tenant-1" || command.UserID != "user-1" || command.SessionID != "session-1" || command.RequestID != "server-repair-request" || command.ClientRequestID == "" || command.IdempotencyKey == "" {
				t.Fatalf("command=%#v", command)
			}
		})
	}
}

func TestApprovalHandlerFailsClosed(t *testing.T) {
	tests := []struct {
		name, method, path, body, ifMatch string
		roles                             []string
		service                           ApprovalService
		status                            int
		code                              string
	}{
		{"admin role required", http.MethodPost, "/v1/admin/approval-requests/a/decisions", `{"request_id":"client-approval-001","decision":"approve","proposal_hash":"hash","target_version":2,"permission_snapshot":"p"}`, `"1"`, []string{"member"}, &approvalServiceStub{}, http.StatusForbidden, "permission_denied"},
		{"admin revise forbidden", http.MethodPost, "/v1/admin/approval-requests/a/decisions", `{"request_id":"client-approval-001","decision":"revise","proposal_hash":"hash","target_version":2,"permission_snapshot":"p"}`, `"1"`, []string{"owner"}, &approvalServiceStub{}, http.StatusBadRequest, "validation_failed"},
		{"user cannot submit permission snapshot", http.MethodPost, "/v1/approvals/a/decisions", `{"request_id":"client-approval-001","decision":"approve","proposal_hash":"hash","expected_target_version":2,"permission_snapshot":"p"}`, `"1"`, []string{"member"}, &approvalServiceStub{}, http.StatusBadRequest, "validation_failed"},
		{"precondition required", http.MethodPost, "/v1/approvals/a/decisions", `{"request_id":"client-approval-001","decision":"approve","proposal_hash":"hash","expected_target_version":2}`, "", []string{"member"}, &approvalServiceStub{}, http.StatusPreconditionRequired, "precondition_required"},
		{"missing service", http.MethodPost, "/v1/approvals/a/decisions", `{"request_id":"client-approval-001","decision":"approve","proposal_hash":"hash","expected_target_version":2}`, `"1"`, []string{"member"}, nil, http.StatusServiceUnavailable, "dependency_unavailable"},
		{"unknown path", http.MethodPost, "/v1/approvals/a", `{}`, `"1"`, []string{"member"}, &approvalServiceStub{}, http.StatusNotFound, "resource_not_found"},
		{"method", http.MethodGet, "/v1/approvals/a/decisions", ``, `"1"`, []string{"member"}, &approvalServiceStub{}, http.StatusMethodNotAllowed, "method_not_allowed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := serveRepairRequest(t, ApprovalHandler{Service: test.service}, test.method, test.path, test.body, test.roles, test.ifMatch)
			assertProblem(t, recorder, test.status, test.code)
		})
	}
}

func TestApprovalHandlerMapsDurableServiceConflicts(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrReauthenticationRequired, http.StatusUnauthorized, "reauthentication_required"},
		{ErrPermissionDenied, http.StatusForbidden, "permission_denied"},
		{ErrResourceNotFound, http.StatusNotFound, "resource_not_found"},
		{ErrVersionConflict, http.StatusConflict, "version_conflict"},
		{ErrIdempotencyConflict, http.StatusConflict, "idempotency_conflict"},
		{ErrStateConflict, http.StatusConflict, "state_conflict"},
	} {
		stub := &approvalServiceStub{err: test.err}
		recorder := serveRepairRequest(t, ApprovalHandler{Service: stub}, http.MethodPost, "/v1/approvals/a/decisions", `{"request_id":"client-approval-001","decision":"approve","proposal_hash":"hash","expected_target_version":2}`, []string{"member"}, `"1"`)
		assertProblem(t, recorder, test.status, test.code)
	}
}
