// Package api implements the authenticated Control Plane HTTP boundary for
// execution repair commands. It only accepts gateway-verified principals.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const maximumRepairBodyBytes int64 = 64 * 1024

var (
	ErrValidation               = errors.New("repair request validation failed")
	ErrPermissionDenied         = errors.New("repair permission denied")
	ErrReauthenticationRequired = errors.New("repair reauthentication required")
	ErrResourceNotFound         = errors.New("repair resource not found")
	ErrStateConflict            = errors.New("repair state conflict")
	ErrVersionConflict          = errors.New("repair version conflict")
	ErrIdempotencyConflict      = errors.New("repair idempotency conflict")
	ErrDependencyUnavailable    = errors.New("repair dependency unavailable")
)

type ProposeRepairCommand struct {
	RequestID, ClientRequestID, IdempotencyKey string
	TenantID, UserID, SessionID                string
	TargetID                                   string
	TargetVersion                              uint64
	Resolution, EvidenceHash, Reason           string
}

type DecideRepairCommand struct {
	RequestID, ClientRequestID, IdempotencyKey string
	TenantID, UserID, SessionID                string
	RepairID, Decision, ProposalHash           string
	ExpectedRepairVersion, TargetVersion       uint64
	PermissionSnapshot                         string
}

type RepairResult struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RepairService interface {
	ProposeRepair(context.Context, ProposeRepairCommand) (RepairResult, error)
	DecideRepair(context.Context, DecideRepairCommand) (RepairResult, error)
}

// RepairServiceMux keeps the public Repair API stable while domain owners
// retain their own evidence inspection and atomic execution boundaries.
type RepairServiceMux struct {
	ToolEffect     RepairService
	AnonymousClaim RepairService
}

func (mux RepairServiceMux) ProposeRepair(ctx context.Context, command ProposeRepairCommand) (RepairResult, error) {
	if isAnonymousClaimResolution(command.Resolution) {
		if mux.AnonymousClaim == nil {
			return RepairResult{}, ErrDependencyUnavailable
		}
		return mux.AnonymousClaim.ProposeRepair(ctx, command)
	}
	if mux.ToolEffect == nil {
		return RepairResult{}, ErrDependencyUnavailable
	}
	return mux.ToolEffect.ProposeRepair(ctx, command)
}

func (mux RepairServiceMux) DecideRepair(ctx context.Context, command DecideRepairCommand) (RepairResult, error) {
	if mux.AnonymousClaim != nil {
		result, err := mux.AnonymousClaim.DecideRepair(ctx, command)
		if !errors.Is(err, ErrResourceNotFound) {
			return result, err
		}
	}
	if mux.ToolEffect == nil {
		return RepairResult{}, ErrDependencyUnavailable
	}
	return mux.ToolEffect.DecideRepair(ctx, command)
}

type RepairHandler struct {
	Service RepairService
}

func (handler RepairHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/v1/admin/repair-commands" {
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request)
			return
		}
		handler.propose(writer, request)
		return
	}
	if repairID, ok := repairDecisionPath(request.URL.Path); ok {
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request)
			return
		}
		handler.decide(writer, request, repairID)
		return
	}
	handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
}

func (handler RepairHandler) propose(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	expectedTargetVersion, ok := handler.ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID     string `json:"request_id"`
		RepairKind    string `json:"repair_kind"`
		TargetID      string `json:"target_id"`
		TargetVersion uint64 `json:"target_version"`
		EvidenceHash  string `json:"evidence_hash"`
		Reason        string `json:"reason"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || !validResolution(body.RepairKind) || body.TargetID == "" || body.TargetVersion == 0 || body.EvidenceHash == "" || len(body.EvidenceHash) > 256 || strings.TrimSpace(body.Reason) == "" || len(body.Reason) > 4096 {
		handler.validationFailed(writer, request)
		return
	}
	if body.TargetVersion != expectedTargetVersion {
		handler.writeProblem(writer, request, http.StatusConflict, "version_conflict", "Version conflict", true)
		return
	}
	result, err := handler.Service.ProposeRepair(request.Context(), ProposeRepairCommand{RequestID: metadata.RequestID, ClientRequestID: body.RequestID, IdempotencyKey: metadata.IdempotencyKey, TenantID: metadata.TenantID, UserID: metadata.UserID, SessionID: metadata.SessionID, TargetID: body.TargetID, TargetVersion: body.TargetVersion, Resolution: body.RepairKind, EvidenceHash: body.EvidenceHash, Reason: body.Reason})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeResult(writer, request, result)
}

func (handler RepairHandler) decide(writer http.ResponseWriter, request *http.Request, repairID string) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	expectedRepairVersion, ok := handler.ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID          string `json:"request_id"`
		Decision           string `json:"decision"`
		ProposalHash       string `json:"proposal_hash"`
		TargetVersion      uint64 `json:"target_version"`
		PermissionSnapshot string `json:"permission_snapshot"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || (body.Decision != "approve" && body.Decision != "reject") || body.ProposalHash == "" || len(body.ProposalHash) > 256 || body.TargetVersion == 0 || body.PermissionSnapshot == "" || len(body.PermissionSnapshot) > 4096 {
		handler.validationFailed(writer, request)
		return
	}
	result, err := handler.Service.DecideRepair(request.Context(), DecideRepairCommand{RequestID: metadata.RequestID, ClientRequestID: body.RequestID, IdempotencyKey: metadata.IdempotencyKey, TenantID: metadata.TenantID, UserID: metadata.UserID, SessionID: metadata.SessionID, RepairID: repairID, Decision: body.Decision, ProposalHash: body.ProposalHash, ExpectedRepairVersion: expectedRepairVersion, TargetVersion: body.TargetVersion, PermissionSnapshot: body.PermissionSnapshot})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeResult(writer, request, result)
}

type repairMetadata struct {
	RequestID, IdempotencyKey, TenantID, UserID, SessionID string
}

func (handler RepairHandler) metadata(writer http.ResponseWriter, request *http.Request) (repairMetadata, bool) {
	if handler.Service == nil {
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return repairMetadata{}, false
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || !claims.CSRFVerified || claims.SubjectID == "" || claims.TenantID == "" || claims.SessionID == "" {
		handler.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return repairMetadata{}, false
	}
	if !hasRepairRole(claims.Roles) {
		handler.writeProblem(writer, request, http.StatusForbidden, "permission_denied", "Permission denied", false)
		return repairMetadata{}, false
	}
	values := request.Header.Values(transport.IdempotencyHeader)
	if len(values) != 1 || idempotency.ValidateRawKey(values[0]) != nil {
		handler.validationFailed(writer, request)
		return repairMetadata{}, false
	}
	return repairMetadata{RequestID: claims.RequestID, IdempotencyKey: values[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, true
}

func (handler RepairHandler) ifMatch(writer http.ResponseWriter, request *http.Request) (uint64, bool) {
	values := request.Header.Values("If-Match")
	if len(values) == 0 {
		handler.writeProblem(writer, request, http.StatusPreconditionRequired, "precondition_required", "Precondition required", false)
		return 0, false
	}
	if len(values) != 1 || len(values[0]) < 3 || values[0][0] != '"' || values[0][len(values[0])-1] != '"' {
		handler.validationFailed(writer, request)
		return 0, false
	}
	version, err := strconv.ParseUint(values[0][1:len(values[0])-1], 10, 64)
	if err != nil || version == 0 {
		handler.validationFailed(writer, request)
		return 0, false
	}
	return version, true
}

func (handler RepairHandler) decode(writer http.ResponseWriter, request *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		handler.writeProblem(writer, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Unsupported media type", false)
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maximumRepairBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			handler.writeProblem(writer, request, http.StatusRequestEntityTooLarge, "payload_too_large", "Payload too large", false)
		} else {
			handler.validationFailed(writer, request)
		}
		return false
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		handler.validationFailed(writer, request)
		return false
	}
	return true
}

func (handler RepairHandler) writeResult(writer http.ResponseWriter, request *http.Request, result RepairResult) {
	if result.ID == "" || result.Version == 0 || result.Status == "" || result.UpdatedAt.IsZero() {
		handler.writeProblem(writer, request, http.StatusInternalServerError, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(RepairResult{ID: result.ID, Version: result.Version, Status: result.Status, UpdatedAt: result.UpdatedAt.UTC()})
}

func (handler RepairHandler) serviceError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		handler.validationFailed(writer, request)
	case errors.Is(err, ErrPermissionDenied):
		handler.writeProblem(writer, request, http.StatusForbidden, "permission_denied", "Permission denied", false)
	case errors.Is(err, ErrReauthenticationRequired):
		handler.writeProblem(writer, request, http.StatusUnauthorized, "reauthentication_required", "Reauthentication required", false)
	case errors.Is(err, ErrResourceNotFound):
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
	case errors.Is(err, ErrVersionConflict):
		handler.writeProblem(writer, request, http.StatusConflict, "version_conflict", "Version conflict", true)
	case errors.Is(err, ErrIdempotencyConflict):
		handler.writeProblem(writer, request, http.StatusConflict, "idempotency_conflict", "Idempotency conflict", false)
	case errors.Is(err, ErrStateConflict):
		handler.writeProblem(writer, request, http.StatusConflict, "state_conflict", "State conflict", true)
	default:
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
	}
}

func (handler RepairHandler) validationFailed(writer http.ResponseWriter, request *http.Request) {
	handler.writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Validation failed", false)
}

func (handler RepairHandler) methodNotAllowed(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Allow", http.MethodPost)
	handler.writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
}

func (handler RepairHandler) writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, title string, retryable bool) {
	requestID := request.Header.Get(transport.RequestIDHeader)
	if claims, ok := serviceauth.ClaimsFromContext(request.Context()); ok && claims.RequestID != "" {
		requestID = claims.RequestID
	}
	problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/" + code, Title: title, Status: status, Code: code, RequestID: requestID, Retryable: retryable})
}

func repairDecisionPath(path string) (string, bool) {
	const prefix, suffix = "/v1/admin/repair-commands/", "/approval-decisions"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return id, id != "" && !strings.Contains(id, "/")
}

func validResolution(value string) bool {
	return value == "confirmed_occurred" || value == "confirmed_not_occurred" || value == "accepted_unknown" || isAnonymousClaimResolution(value)
}

func isAnonymousClaimResolution(value string) bool {
	return value == "reconcile_reserved" || value == "reconcile_destination_committed" || value == "reconcile_erasing"
}

var _ RepairService = RepairServiceMux{}

func validClientRequestID(value string) bool {
	return len(value) >= 8 && len(value) <= 256 && !strings.ContainsAny(value, "\r\n\x00")
}

func hasRepairRole(roles []string) bool {
	for _, role := range roles {
		if role == "owner" || role == "admin" {
			return true
		}
	}
	return false
}
