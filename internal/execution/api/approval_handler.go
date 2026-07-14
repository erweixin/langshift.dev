package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

type DecideApprovalCommand struct {
	RequestID, ClientRequestID, IdempotencyKey string
	TenantID, UserID, SessionID                string
	ApprovalID, Decision, ProposalHash, Mode   string
	ExpectedApprovalVersion, TargetVersion     uint64
	PermissionSnapshot                         string
}

type ApprovalResult = RepairResult

type ApprovalService interface {
	DecideApproval(context.Context, DecideApprovalCommand) (ApprovalResult, error)
}

type ApprovalHandler struct {
	Service ApprovalService
}

func (handler ApprovalHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	approvalID, mode, ok := approvalDecisionPath(request.URL.Path)
	if !ok {
		RepairHandler{}.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	if request.Method != http.MethodPost {
		RepairHandler{}.methodNotAllowed(writer, request)
		return
	}
	handler.decide(writer, request, approvalID, mode)
}

func (handler ApprovalHandler) decide(writer http.ResponseWriter, request *http.Request, approvalID, mode string) {
	metadata, ok := handler.metadata(writer, request, mode)
	if !ok {
		return
	}
	expectedApprovalVersion, ok := (RepairHandler{}).ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID             string `json:"request_id"`
		Decision              string `json:"decision"`
		ProposalHash          string `json:"proposal_hash"`
		ExpectedTargetVersion uint64 `json:"expected_target_version"`
		TargetVersion         uint64 `json:"target_version"`
		PermissionSnapshot    string `json:"permission_snapshot"`
	}
	if !(RepairHandler{}).decode(writer, request, &body) {
		return
	}
	targetVersion := body.ExpectedTargetVersion
	validDecision := body.Decision == "approve" || body.Decision == "reject" || body.Decision == "revise"
	if mode == "admin" {
		targetVersion = body.TargetVersion
		validDecision = body.Decision == "approve" || body.Decision == "reject"
	}
	if !validClientRequestID(body.RequestID) || !validDecision || body.ProposalHash == "" || len(body.ProposalHash) > 256 || targetVersion == 0 || (mode == "user" && (body.TargetVersion != 0 || body.PermissionSnapshot != "")) || (mode == "admin" && (body.ExpectedTargetVersion != 0 || body.PermissionSnapshot == "" || len(body.PermissionSnapshot) > 4096)) {
		RepairHandler{}.validationFailed(writer, request)
		return
	}
	result, err := handler.Service.DecideApproval(request.Context(), DecideApprovalCommand{
		RequestID: metadata.requestID, ClientRequestID: body.RequestID, IdempotencyKey: metadata.idempotencyKey,
		TenantID: metadata.tenantID, UserID: metadata.userID, SessionID: metadata.sessionID,
		ApprovalID: approvalID, Decision: body.Decision, ProposalHash: body.ProposalHash, Mode: mode,
		ExpectedApprovalVersion: expectedApprovalVersion, TargetVersion: targetVersion, PermissionSnapshot: body.PermissionSnapshot,
	})
	if err != nil {
		RepairHandler{}.serviceError(writer, request, err)
		return
	}
	RepairHandler{}.writeResult(writer, request, result)
}

type approvalMetadata struct {
	requestID, idempotencyKey, tenantID, userID, sessionID string
}

func (handler ApprovalHandler) metadata(writer http.ResponseWriter, request *http.Request, mode string) (approvalMetadata, bool) {
	if handler.Service == nil {
		RepairHandler{}.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return approvalMetadata{}, false
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || !claims.CSRFVerified || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" {
		RepairHandler{}.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return approvalMetadata{}, false
	}
	if mode == "admin" && !hasRepairRole(claims.Roles) {
		RepairHandler{}.writeProblem(writer, request, http.StatusForbidden, "permission_denied", "Permission denied", false)
		return approvalMetadata{}, false
	}
	values := request.Header.Values(transport.IdempotencyHeader)
	if len(values) != 1 || idempotency.ValidateRawKey(values[0]) != nil {
		RepairHandler{}.validationFailed(writer, request)
		return approvalMetadata{}, false
	}
	return approvalMetadata{requestID: claims.RequestID, idempotencyKey: values[0], tenantID: claims.TenantID, userID: claims.SubjectID, sessionID: claims.SessionID}, true
}

func approvalDecisionPath(path string) (approvalID, mode string, ok bool) {
	for _, candidate := range []struct{ prefix, suffix, mode string }{
		{"/v1/approvals/", "/decisions", "user"},
		{"/v1/admin/approval-requests/", "/decisions", "admin"},
	} {
		if strings.HasPrefix(path, candidate.prefix) && strings.HasSuffix(path, candidate.suffix) {
			id := strings.TrimSuffix(strings.TrimPrefix(path, candidate.prefix), candidate.suffix)
			if id != "" && !strings.Contains(id, "/") {
				return id, candidate.mode, true
			}
		}
	}
	return "", "", false
}
