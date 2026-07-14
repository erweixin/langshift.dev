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

	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

var repairAPINow = time.Date(2026, time.July, 15, 5, 0, 0, 0, time.UTC)

type repairServiceStub struct {
	proposeCommand ProposeRepairCommand
	decideCommand  DecideRepairCommand
	result         RepairResult
	err            error
}

func (stub *repairServiceStub) ProposeRepair(_ context.Context, command ProposeRepairCommand) (RepairResult, error) {
	stub.proposeCommand = command
	return stub.result, stub.err
}

func (stub *repairServiceStub) DecideRepair(_ context.Context, command DecideRepairCommand) (RepairResult, error) {
	stub.decideCommand = command
	return stub.result, stub.err
}

func TestRepairHandlerProposesStrictScopedCommand(t *testing.T) {
	stub := &repairServiceStub{result: RepairResult{ID: "repair-1", Version: 1, Status: "proposed", UpdatedAt: repairAPINow}}
	body := `{"request_id":"client-request-001","repair_kind":"accepted_unknown","target_id":"tool-1","target_version":7,"evidence_hash":"evidence-hash","reason":"automatic reconciliation exhausted"}`
	recorder := serveRepairRequest(t, RepairHandler{Service: stub}, http.MethodPost, "/v1/admin/repair-commands", body, []string{"admin"}, `"7"`)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"1"` || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	command := stub.proposeCommand
	if command.RequestID != "server-repair-request" || command.ClientRequestID != "client-request-001" || command.IdempotencyKey != "repair-idempotency-key-0001" || command.TenantID != "tenant-1" || command.UserID != "user-1" || command.SessionID != "session-1" || command.TargetID != "tool-1" || command.TargetVersion != 7 || command.Resolution != "accepted_unknown" || command.EvidenceHash != "evidence-hash" || command.Reason == "" {
		t.Fatalf("command=%#v", command)
	}
	var response RepairResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.ID != "repair-1" || response.Version != 1 || response.Status != "proposed" || !response.UpdatedAt.Equal(repairAPINow) {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestRepairHandlerDecidesWithRepairAndTargetVersions(t *testing.T) {
	stub := &repairServiceStub{result: RepairResult{ID: "repair-1", Version: 2, Status: "approved", UpdatedAt: repairAPINow}}
	body := `{"request_id":"client-decision-001","decision":"approve","proposal_hash":"proposal-hash","target_version":7,"permission_snapshot":"permission-v4"}`
	recorder := serveRepairRequest(t, RepairHandler{Service: stub}, http.MethodPost, "/v1/admin/repair-commands/repair-1/approval-decisions", body, []string{"owner"}, `"1"`)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"2"` {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	command := stub.decideCommand
	if command.RepairID != "repair-1" || command.Decision != "approve" || command.ProposalHash != "proposal-hash" || command.ExpectedRepairVersion != 1 || command.TargetVersion != 7 || command.PermissionSnapshot != "permission-v4" || command.TenantID != "tenant-1" || command.UserID != "user-1" || command.SessionID != "session-1" {
		t.Fatalf("command=%#v", command)
	}
}

func TestRepairHandlerFailsClosedAtHTTPBoundary(t *testing.T) {
	tests := []struct {
		name, method, target, body, ifMatch string
		roles                               []string
		service                             RepairService
		wantStatus                          int
		wantCode                            string
	}{
		{"unknown role", http.MethodPost, "/v1/admin/repair-commands", validProposalBody(), `"3"`, []string{"member"}, &repairServiceStub{}, http.StatusForbidden, "permission_denied"},
		{"missing precondition", http.MethodPost, "/v1/admin/repair-commands", validProposalBody(), "", []string{"admin"}, &repairServiceStub{}, http.StatusPreconditionRequired, "precondition_required"},
		{"target version mismatch", http.MethodPost, "/v1/admin/repair-commands", validProposalBody(), `"4"`, []string{"admin"}, &repairServiceStub{}, http.StatusConflict, "version_conflict"},
		{"unknown field", http.MethodPost, "/v1/admin/repair-commands", `{"request_id":"client-request-001","repair_kind":"confirmed_occurred","target_id":"tool","target_version":3,"evidence_hash":"hash","reason":"reason","extra":true}`, `"3"`, []string{"admin"}, &repairServiceStub{}, http.StatusBadRequest, "validation_failed"},
		{"unsupported media handled separately", http.MethodGet, "/v1/admin/repair-commands", "", "", []string{"admin"}, &repairServiceStub{}, http.StatusMethodNotAllowed, "method_not_allowed"},
		{"missing service", http.MethodPost, "/v1/admin/repair-commands", validProposalBody(), `"3"`, []string{"admin"}, nil, http.StatusServiceUnavailable, "dependency_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := serveRepairRequest(t, RepairHandler{Service: test.service}, test.method, test.target, test.body, test.roles, test.ifMatch)
			assertProblem(t, recorder, test.wantStatus, test.wantCode)
		})
	}
}

func TestRepairHandlerMapsServiceConflicts(t *testing.T) {
	tests := []struct {
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
		{ErrDependencyUnavailable, http.StatusServiceUnavailable, "dependency_unavailable"},
	}
	for _, test := range tests {
		stub := &repairServiceStub{err: test.err}
		recorder := serveRepairRequest(t, RepairHandler{Service: stub}, http.MethodPost, "/v1/admin/repair-commands", validProposalBody(), []string{"admin"}, `"3"`)
		assertProblem(t, recorder, test.status, test.code)
	}
}

func TestRepairServiceMuxRoutesClaimProposalsAndDecisions(t *testing.T) {
	claim := &repairServiceStub{result: RepairResult{ID: "claim-repair", Version: 1, Status: "proposed", UpdatedAt: repairAPINow}}
	tool := &repairServiceStub{result: RepairResult{ID: "tool-repair", Version: 1, Status: "proposed", UpdatedAt: repairAPINow}}
	mux := RepairServiceMux{ToolEffect: tool, AnonymousClaim: claim}
	result, err := mux.ProposeRepair(context.Background(), ProposeRepairCommand{Resolution: "reconcile_destination_committed"})
	if err != nil || result.ID != "claim-repair" || claim.proposeCommand.Resolution != "reconcile_destination_committed" || tool.proposeCommand.Resolution != "" {
		t.Fatalf("claim proposal result=%#v error=%v claim=%#v tool=%#v", result, err, claim.proposeCommand, tool.proposeCommand)
	}
	claim.err = ErrResourceNotFound
	result, err = mux.DecideRepair(context.Background(), DecideRepairCommand{RepairID: "tool-repair"})
	if err != nil || result.ID != "tool-repair" || tool.decideCommand.RepairID != "tool-repair" {
		t.Fatalf("tool decision result=%#v error=%v tool=%#v", result, err, tool.decideCommand)
	}
}

func serveRepairRequest(t *testing.T, handler http.Handler, method, target, body string, roles []string, ifMatch string) *httptest.ResponseRecorder {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x41}, ed25519.SeedSize))
	claims := trustedcontext.Claims{PrincipalKind: trustedcontext.AuthenticatedUser, Issuer: "gateway", Audience: "control-plane", SubjectID: "user-1", TenantID: "tenant-1", MembershipID: "membership-1", SessionID: "session-1", Roles: roles, RequestID: "server-repair-request", RequestMethod: method, RequestTarget: target, ClientIPHash: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x51}, 32)), UserAgentHash: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x52}, 32)), CSRFVerified: true, IssuedAt: repairAPINow.Unix(), ExpiresAt: repairAPINow.Add(time.Minute).Unix(), Nonce: "repair-nonce"}
	token, err := trustedcontext.Sign(claims, "key", privateKey, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set(transport.RequestIDHeader, claims.RequestID)
	request.Header.Set(transport.TrustedContextHeader, token)
	request.Header.Set(transport.IdempotencyHeader, "repair-idempotency-key-0001")
	request.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	recorder := httptest.NewRecorder()
	verified := serviceauth.Middleware{Verifier: trustedcontext.Verifier{Issuer: "gateway", Audience: "control-plane", Keys: map[string]ed25519.PublicKey{"key": privateKey.Public().(ed25519.PublicKey)}, MaximumTTL: 2 * time.Minute}, Now: func() time.Time { return repairAPINow }}.Wrap(handler)
	verified.ServeHTTP(recorder, request)
	return recorder
}

func validProposalBody() string {
	return `{"request_id":"client-request-001","repair_kind":"confirmed_occurred","target_id":"tool","target_version":3,"evidence_hash":"hash","reason":"reason"}`
}

func assertProblem(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status=%d content-type=%s body=%s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	var body struct {
		Code      string `json:"code"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.Code != code || body.RequestID != "server-repair-request" {
		t.Fatalf("problem=%#v err=%v body=%s", body, err, recorder.Body.String())
	}
}
