package api

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const (
	contractProposalMedia    = "application/vnd.lites.contract-proposal.v2+json"
	contractDecisionMedia    = "application/vnd.lites.contract-decision.v2+json"
	adjustmentProposalMedia  = "application/vnd.lites.accounting-adjustment-proposal.v2+json"
	adjustmentDecisionMedia  = "application/vnd.lites.accounting-adjustment-decision.v2+json"
	entitlementProposalMedia = "application/vnd.lites.entitlement-proposal.v2+json"
	entitlementDecisionMedia = "application/vnd.lites.entitlement-decision.v2+json"
	resourceMedia            = "application/vnd.lites.admin-control-resource.v2+json"
	usageMedia               = "application/vnd.lites.usage-snapshot.v2+json"
	auditPageMedia           = "application/vnd.lites.admin-audit-page.v2+json"
	auditExportRequestMedia  = "application/vnd.lites.admin-audit-export-request.v2+json"
	auditExportResourceMedia = "application/vnd.lites.admin-audit-export.v2+json"
	auditReasonHeader        = "X-Audit-Reason"
)

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Handler struct{ Service Service }

func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	claims, ok := handler.authorize(writer, request)
	if !ok {
		return
	}
	path := strings.TrimPrefix(request.URL.Path, "/v1/admin/")
	switch {
	case path == "contracts" && request.Method == http.MethodPost:
		handler.proposeContract(writer, request, claims)
	case strings.HasPrefix(path, "contracts/") && strings.HasSuffix(path, "/approval-decisions") && request.Method == http.MethodPost:
		id := strings.TrimSuffix(strings.TrimPrefix(path, "contracts/"), "/approval-decisions")
		if uuid.Validate(id) != nil {
			handler.writeProblem(writer, request, 404, "resource_not_found", false)
			return
		}
		handler.decideContract(writer, request, claims, id)
	case path == "usage" && request.Method == http.MethodGet:
		handler.readUsage(writer, request, claims)
	case path == "audit" && request.Method == http.MethodGet:
		handler.readAudit(writer, request, claims)
	case path == "audit-exports" && request.Method == http.MethodPost:
		handler.requestAuditExport(writer, request, claims)
	case strings.HasPrefix(path, "audit-exports/") && request.Method == http.MethodGet:
		id := strings.TrimPrefix(path, "audit-exports/")
		if strings.Contains(id, "/") || uuid.Validate(id) != nil {
			handler.writeProblem(writer, request, 404, "resource_not_found", false)
			return
		}
		handler.readAuditExport(writer, request, claims, id)
	case path == "usage/adjustments" && request.Method == http.MethodPost:
		handler.proposeAdjustment(writer, request, claims)
	case strings.HasPrefix(path, "usage/adjustments/") && strings.HasSuffix(path, "/approval-decisions") && request.Method == http.MethodPost:
		id := strings.TrimSuffix(strings.TrimPrefix(path, "usage/adjustments/"), "/approval-decisions")
		if uuid.Validate(id) != nil {
			handler.writeProblem(writer, request, 404, "resource_not_found", false)
			return
		}
		handler.decideAdjustment(writer, request, claims, id)
	case path == "entitlements" && request.Method == http.MethodPost:
		handler.proposeEntitlement(writer, request, claims)
	case strings.HasPrefix(path, "entitlements/") && strings.HasSuffix(path, "/approval-decisions") && request.Method == http.MethodPost:
		id := strings.TrimSuffix(strings.TrimPrefix(path, "entitlements/"), "/approval-decisions")
		if uuid.Validate(id) != nil {
			handler.writeProblem(writer, request, 404, "resource_not_found", false)
			return
		}
		handler.decideEntitlement(writer, request, claims, id)
	default:
		handler.writeProblem(writer, request, 404, "resource_not_found", false)
	}
}

func (handler Handler) proposeContract(w http.ResponseWriter, r *http.Request, claims trustedcontext.Claims) {
	var body struct {
		RequestID        string     `json:"request_id"`
		Action           string     `json:"action"`
		TargetContractID string     `json:"target_contract_id"`
		TargetVersion    uint64     `json:"target_version"`
		ContractNumber   string     `json:"contract_number"`
		StartsAt         *time.Time `json:"starts_at"`
		EndsAt           *time.Time `json:"ends_at"`
		SeatLimit        int        `json:"seat_limit"`
		Region           string     `json:"region"`
		LicenseKind      string     `json:"license_kind"`
		Reason           string     `json:"reason"`
	}
	if !decode(w, r, contractProposalMedia, &body) || !validRequestID(body.RequestID) || !validReason(body.Reason) || !validContractTerms(body.Action, body.TargetContractID, body.TargetVersion, body.ContractNumber, body.StartsAt, body.EndsAt, body.SeatLimit, body.Region, body.LicenseKind) {
		handler.writeProblem(w, r, 400, "validation_failed", false)
		return
	}
	metadata, ok := handler.metadata(w, r, claims, body.RequestID)
	if !ok {
		return
	}
	result, err := handler.Service.ProposeContract(r.Context(), ContractProposalCommand{CommandMetadata: metadata, Action: body.Action, TargetContractID: body.TargetContractID, TargetVersion: body.TargetVersion, ContractNumber: strings.TrimSpace(body.ContractNumber), StartsAt: body.StartsAt, EndsAt: body.EndsAt, SeatLimit: body.SeatLimit, Region: strings.TrimSpace(body.Region), LicenseKind: body.LicenseKind, Reason: strings.TrimSpace(body.Reason)})
	handler.finish(w, r, result, err)
}

func (handler Handler) decideContract(w http.ResponseWriter, r *http.Request, claims trustedcontext.Claims, proposalID string) {
	var body struct {
		RequestID               string `json:"request_id"`
		Decision                string `json:"decision"`
		ProposalHash            string `json:"proposal_hash"`
		TargetVersion           uint64 `json:"target_version"`
		ExpectedProposalVersion uint64 `json:"expected_proposal_version"`
	}
	if !decode(w, r, contractDecisionMedia, &body) || !validRequestID(body.RequestID) || !validDecision(body.Decision) || !hashPattern.MatchString(body.ProposalHash) || body.ExpectedProposalVersion != 1 {
		handler.writeProblem(w, r, 400, "validation_failed", false)
		return
	}
	if !handler.requireIfMatch(w, r, 1) {
		return
	}
	metadata, ok := handler.metadata(w, r, claims, body.RequestID)
	if !ok {
		return
	}
	result, err := handler.Service.DecideContract(r.Context(), ContractDecisionCommand{CommandMetadata: metadata, ProposalID: proposalID, Decision: body.Decision, ProposalHash: body.ProposalHash, TargetVersion: body.TargetVersion, ExpectedProposalVersion: body.ExpectedProposalVersion})
	handler.finish(w, r, result, err)
}

func (handler Handler) proposeAdjustment(w http.ResponseWriter, r *http.Request, claims trustedcontext.Claims) {
	var body struct {
		RequestID     string `json:"request_id"`
		BucketID      string `json:"bucket_id"`
		Reason        string `json:"reason"`
		TargetVersion uint64 `json:"target_version"`
		Units         int64  `json:"units"`
	}
	if !decode(w, r, adjustmentProposalMedia, &body) || !validRequestID(body.RequestID) || uuid.Validate(body.BucketID) != nil || body.TargetVersion < 1 || body.Units == 0 || !validReason(body.Reason) {
		handler.writeProblem(w, r, 400, "validation_failed", false)
		return
	}
	metadata, ok := handler.metadata(w, r, claims, body.RequestID)
	if !ok {
		return
	}
	result, err := handler.Service.ProposeAdjustment(r.Context(), AdjustmentProposalCommand{CommandMetadata: metadata, BucketID: body.BucketID, TargetVersion: body.TargetVersion, Units: body.Units, Reason: strings.TrimSpace(body.Reason)})
	handler.finish(w, r, result, err)
}

func (handler Handler) decideAdjustment(w http.ResponseWriter, r *http.Request, claims trustedcontext.Claims, proposalID string) {
	var body struct {
		RequestID               string `json:"request_id"`
		Decision                string `json:"decision"`
		ProposalHash            string `json:"proposal_hash"`
		TargetVersion           uint64 `json:"target_version"`
		ExpectedProposalVersion uint64 `json:"expected_proposal_version"`
	}
	if !decode(w, r, adjustmentDecisionMedia, &body) || !validRequestID(body.RequestID) || !validDecision(body.Decision) || !hashPattern.MatchString(body.ProposalHash) || body.TargetVersion < 1 || body.ExpectedProposalVersion != 1 {
		handler.writeProblem(w, r, 400, "validation_failed", false)
		return
	}
	if !handler.requireIfMatch(w, r, 1) {
		return
	}
	metadata, ok := handler.metadata(w, r, claims, body.RequestID)
	if !ok {
		return
	}
	result, err := handler.Service.DecideAdjustment(r.Context(), AdjustmentDecisionCommand{CommandMetadata: metadata, ProposalID: proposalID, Decision: body.Decision, ProposalHash: body.ProposalHash, TargetVersion: body.TargetVersion, ExpectedProposalVersion: body.ExpectedProposalVersion})
	handler.finish(w, r, result, err)
}

func (handler Handler) proposeEntitlement(w http.ResponseWriter, r *http.Request, claims trustedcontext.Claims) {
	var body struct {
		RequestID                  string          `json:"request_id"`
		ContractID                 string          `json:"contract_id"`
		ExpectedContractVersion    uint64          `json:"expected_contract_version"`
		EntitlementKey             string          `json:"entitlement_key"`
		ExpectedEntitlementVersion uint64          `json:"expected_entitlement_version"`
		LimitValue                 *int64          `json:"limit_value"`
		Config                     json.RawMessage `json:"config"`
		Reason                     string          `json:"reason"`
	}
	if !decode(w, r, entitlementProposalMedia, &body) || !validRequestID(body.RequestID) || uuid.Validate(body.ContractID) != nil || body.ExpectedContractVersion < 1 || !validEntitlementKey(body.EntitlementKey) || body.LimitValue != nil && *body.LimitValue < 0 || !validConfigObject(body.Config) || !validReason(body.Reason) {
		handler.writeProblem(w, r, 400, "validation_failed", false)
		return
	}
	if !handler.requireIfMatch(w, r, body.ExpectedContractVersion) {
		return
	}
	metadata, ok := handler.metadata(w, r, claims, body.RequestID)
	if !ok {
		return
	}
	result, err := handler.Service.ProposeEntitlement(r.Context(), EntitlementProposalCommand{CommandMetadata: metadata, ContractID: body.ContractID, TargetContractVersion: body.ExpectedContractVersion, EntitlementKey: body.EntitlementKey, TargetEntitlementVersion: body.ExpectedEntitlementVersion, LimitValue: body.LimitValue, Config: append(json.RawMessage(nil), body.Config...), Reason: strings.TrimSpace(body.Reason)})
	handler.finish(w, r, result, err)
}

func (handler Handler) decideEntitlement(w http.ResponseWriter, r *http.Request, claims trustedcontext.Claims, proposalID string) {
	var body struct {
		RequestID                string `json:"request_id"`
		Decision                 string `json:"decision"`
		ProposalHash             string `json:"proposal_hash"`
		TargetContractVersion    uint64 `json:"target_contract_version"`
		TargetEntitlementVersion uint64 `json:"target_entitlement_version"`
		ExpectedProposalVersion  uint64 `json:"expected_proposal_version"`
	}
	if !decode(w, r, entitlementDecisionMedia, &body) || !validRequestID(body.RequestID) || !validDecision(body.Decision) || !hashPattern.MatchString(body.ProposalHash) || body.TargetContractVersion < 1 || body.ExpectedProposalVersion != 1 {
		handler.writeProblem(w, r, 400, "validation_failed", false)
		return
	}
	if !handler.requireIfMatch(w, r, 1) {
		return
	}
	metadata, ok := handler.metadata(w, r, claims, body.RequestID)
	if !ok {
		return
	}
	result, err := handler.Service.DecideEntitlement(r.Context(), EntitlementDecisionCommand{CommandMetadata: metadata, ProposalID: proposalID, Decision: body.Decision, ProposalHash: body.ProposalHash, TargetContractVersion: body.TargetContractVersion, TargetEntitlementVersion: body.TargetEntitlementVersion, ExpectedProposalVersion: body.ExpectedProposalVersion})
	handler.finish(w, r, result, err)
}

func (handler Handler) readUsage(w http.ResponseWriter, r *http.Request, claims trustedcontext.Claims) {
	reason := strings.TrimSpace(r.Header.Get(auditReasonHeader))
	if reason == "" || len(reason) > 500 {
		handler.writeProblem(w, r, 400, "audit_reason_required", false)
		return
	}
	result, err := handler.Service.ReadUsage(r.Context(), UsageQuery{RequestID: claims.RequestID, TenantID: claims.TenantID, UserID: claims.SubjectID, MembershipID: claims.MembershipID, SessionID: claims.SessionID, Reason: reason})
	if err != nil {
		handler.finishError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", usageMedia)
	w.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(w).Encode(result)
}

func (handler Handler) readAudit(w http.ResponseWriter, r *http.Request, claims trustedcontext.Claims) {
	reason := strings.TrimSpace(r.Header.Get(auditReasonHeader))
	if reason == "" || len(reason) > 500 {
		handler.writeProblem(w, r, 400, "audit_reason_required", false)
		return
	}
	query := r.URL.Query()
	for key, values := range query {
		if key != "limit" && key != "before" || len(values) != 1 {
			handler.writeProblem(w, r, 400, "validation_failed", false)
			return
		}
	}
	limit := 100
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			handler.writeProblem(w, r, 400, "validation_failed", false)
			return
		}
		limit = parsed
	}
	result, err := handler.Service.ReadAudit(r.Context(), AuditQuery{RequestID: claims.RequestID, TenantID: claims.TenantID, UserID: claims.SubjectID, MembershipID: claims.MembershipID, SessionID: claims.SessionID, Reason: reason, Before: query.Get("before"), Limit: limit})
	if err != nil {
		handler.finishError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", auditPageMedia)
	w.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(w).Encode(result)
}

func (handler Handler) requestAuditExport(w http.ResponseWriter, r *http.Request, claims trustedcontext.Claims) {
	var body struct {
		RequestID   string    `json:"request_id"`
		PeriodStart time.Time `json:"period_start"`
		PeriodEnd   time.Time `json:"period_end"`
		Kinds       []string  `json:"kinds"`
		Format      string    `json:"format"`
		Reason      string    `json:"reason"`
	}
	if !decode(w, r, auditExportRequestMedia, &body) || !validRequestID(body.RequestID) || body.PeriodStart.IsZero() || body.PeriodEnd.IsZero() || !body.PeriodEnd.After(body.PeriodStart) || body.PeriodEnd.Sub(body.PeriodStart) > 366*24*time.Hour || !validAuditKinds(body.Kinds) || body.Format != "jsonl" && body.Format != "csv" || strings.TrimSpace(body.Reason) == "" || len(strings.TrimSpace(body.Reason)) > 500 {
		handler.writeProblem(w, r, 400, "validation_failed", false)
		return
	}
	metadata, ok := handler.metadata(w, r, claims, body.RequestID)
	if !ok {
		return
	}
	result, err := handler.Service.RequestAuditExport(r.Context(), AuditExportCommand{CommandMetadata: metadata, PeriodStart: body.PeriodStart.UTC(), PeriodEnd: body.PeriodEnd.UTC(), Kinds: body.Kinds, Format: body.Format, Reason: strings.TrimSpace(body.Reason)})
	if err != nil {
		handler.finishError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", auditExportResourceMedia)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	_ = json.NewEncoder(w).Encode(result)
}

func (handler Handler) readAuditExport(w http.ResponseWriter, r *http.Request, claims trustedcontext.Claims, id string) {
	reason := strings.TrimSpace(r.Header.Get(auditReasonHeader))
	if reason == "" || len(reason) > 500 || len(r.URL.Query()) != 0 {
		handler.writeProblem(w, r, 400, "audit_reason_required", false)
		return
	}
	result, err := handler.Service.ReadAuditExport(r.Context(), AuditExportQuery{RequestID: claims.RequestID, TenantID: claims.TenantID, UserID: claims.SubjectID, MembershipID: claims.MembershipID, SessionID: claims.SessionID, ExportID: id, Reason: reason})
	if err != nil {
		handler.finishError(w, r, err)
		return
	}
	contentType := "application/x-ndjson"
	extension := "jsonl"
	if result.Format == "csv" {
		contentType = "text/csv; charset=utf-8"
		extension = "csv"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="lites-audit-`+result.ID+`.`+extension+`"`)
	w.Header().Set("Cache-Control", "private, no-store")
	if digest, decodeErr := hex.DecodeString(result.ContentHash); decodeErr == nil {
		w.Header().Set("Digest", "SHA-256="+base64.StdEncoding.EncodeToString(digest))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Content)
}

func validAuditKinds(values []string) bool {
	if len(values) < 1 || len(values) > 3 {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if value != "contract_change" && value != "accounting_adjustment" && value != "admin_read" || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func (handler Handler) authorize(w http.ResponseWriter, r *http.Request) (trustedcontext.Claims, bool) {
	claims, ok := serviceauth.ClaimsFromContext(r.Context())
	if handler.Service == nil || !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || (r.Method != http.MethodGet && !claims.CSRFVerified) {
		handler.writeProblem(w, r, 401, "authentication_required", false)
		return trustedcontext.Claims{}, false
	}
	for _, role := range claims.Roles {
		if role == "owner" || role == "contract_admin" {
			return claims, true
		}
	}
	handler.writeProblem(w, r, 403, "permission_denied", false)
	return trustedcontext.Claims{}, false
}

func (handler Handler) metadata(w http.ResponseWriter, r *http.Request, claims trustedcontext.Claims, clientRequestID string) (CommandMetadata, bool) {
	keys := r.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		handler.writeProblem(w, r, 400, "validation_failed", false)
		return CommandMetadata{}, false
	}
	return CommandMetadata{RequestID: claims.RequestID, ClientRequestID: clientRequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, MembershipID: claims.MembershipID, SessionID: claims.SessionID}, true
}

func (handler Handler) finish(w http.ResponseWriter, r *http.Request, result Resource, err error) {
	if err != nil {
		handler.finishError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", resourceMedia)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	_ = json.NewEncoder(w).Encode(result)
}

func (handler Handler) finishError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		handler.writeProblem(w, r, 400, "validation_failed", false)
	case errors.Is(err, ErrReauthentication):
		handler.writeProblem(w, r, 401, "reauthentication_required", false)
	case errors.Is(err, ErrPermissionDenied):
		handler.writeProblem(w, r, 403, "permission_denied", false)
	case errors.Is(err, ErrResourceNotFound):
		handler.writeProblem(w, r, 404, "resource_not_found", false)
	case errors.Is(err, ErrApprovalScopeChanged):
		handler.writeProblem(w, r, 409, "approval_scope_changed", false)
	case errors.Is(err, ErrStateConflict), errors.Is(err, ErrIdempotencyConflict):
		handler.writeProblem(w, r, 409, "state_conflict", true)
	default:
		handler.writeProblem(w, r, 503, "dependency_unavailable", true)
	}
}

func (handler Handler) writeProblem(w http.ResponseWriter, r *http.Request, status int, code string, retry bool) {
	requestID := r.Header.Get(transport.RequestIDHeader)
	problem.Write(w, problem.Value{Type: "https://errors.lites.dev/" + code, Title: strings.ReplaceAll(code, "_", " "), Status: status, Code: code, RequestID: requestID, Retryable: retry})
}

func (handler Handler) requireIfMatch(w http.ResponseWriter, r *http.Request, expected uint64) bool {
	value := r.Header.Get("If-Match")
	if value == "" {
		handler.writeProblem(w, r, http.StatusPreconditionRequired, "precondition_required", false)
		return false
	}
	if value != `"`+strconv.FormatUint(expected, 10)+`"` {
		handler.writeProblem(w, r, http.StatusPreconditionFailed, "precondition_failed", false)
		return false
	}
	return true
}

func decode(w http.ResponseWriter, r *http.Request, media string, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	decoder.DisallowUnknownFields()
	return r.Header.Get("Content-Type") == media && decoder.Decode(target) == nil && errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}
func validRequestID(value string) bool { return len(value) >= 8 && len(value) <= 200 }
func validReason(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 1000
}
func validDecision(value string) bool { return value == "approve" || value == "reject" }
func validEntitlementKey(value string) bool {
	switch value {
	case "programs", "cohorts", "role_packs", "aggregate_analytics", "audit_export", "private_delivery", "commercial_license", "support_tier":
		return true
	default:
		return false
	}
}
func validConfigObject(value json.RawMessage) bool {
	if len(value) < 2 || len(value) > 16<<10 {
		return false
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(value))
	return decoder.Decode(&object) == nil && object != nil && errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}
func validContractTerms(action, target string, version uint64, number string, starts, ends *time.Time, seats int, region, license string) bool {
	if action != "create" && action != "renew" && action != "suspend" && action != "terminate" {
		return false
	}
	if action == "create" && (target != "" || version != 0) {
		return false
	}
	if action != "create" && (uuid.Validate(target) != nil || version < 1) {
		return false
	}
	if action == "suspend" || action == "terminate" {
		return number == "" && starts == nil && ends == nil && seats == 0 && region == "" && license == ""
	}
	return len(strings.TrimSpace(number)) >= 1 && len(strings.TrimSpace(number)) <= 200 && starts != nil && ends != nil && ends.After(*starts) && seats > 0 && len(strings.TrimSpace(region)) >= 1 && len(strings.TrimSpace(region)) <= 100 && (license == "enterprise_cloud" || license == "private_cloud" || license == "commercial_self_hosted")
}
