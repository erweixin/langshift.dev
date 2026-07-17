package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const enterpriseResourceMediaType = "application/vnd.lites.enterprise-resource.v2+json"

type EnterpriseAdminHandler struct{ Service EnterpriseAdminService }

func (handler EnterpriseAdminHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	path := strings.TrimPrefix(request.URL.Path, "/v1/admin/")
	switch {
	case path == "programs" && request.Method == http.MethodPost:
		handler.createProgram(writer, request, metadata)
	case strings.HasPrefix(path, "programs/") && request.Method == http.MethodPatch && validSingleUUID(strings.TrimPrefix(path, "programs/")):
		handler.updateProgram(writer, request, metadata, strings.TrimPrefix(path, "programs/"))
	case path == "cohorts" && request.Method == http.MethodPost:
		handler.createCohort(writer, request, metadata)
	case strings.HasSuffix(path, "/enrollments") && request.Method == http.MethodPost:
		cohortID := strings.TrimSuffix(strings.TrimPrefix(path, "cohorts/"), "/enrollments")
		if !validSingleUUID(cohortID) {
			handler.problem(writer, request, 404, "resource_not_found", false)
			return
		}
		handler.enroll(writer, request, metadata, cohortID)
	case strings.Contains(path, "/enrollments/") && request.Method == http.MethodDelete:
		parts := strings.Split(strings.TrimPrefix(path, "cohorts/"), "/enrollments/")
		if len(parts) != 2 || !validSingleUUID(parts[0]) || !validSingleUUID(parts[1]) {
			handler.problem(writer, request, 404, "resource_not_found", false)
			return
		}
		handler.unenroll(writer, request, metadata, parts[0], parts[1])
	case path == "role-packs" && request.Method == http.MethodPost:
		handler.publishRolePack(writer, request, metadata)
	case path == "task-packs" && request.Method == http.MethodPost:
		handler.publishTaskPack(writer, request, metadata)
	case path == "programs":
		handler.methodNotAllowed(writer, request, http.MethodPost)
	case strings.HasPrefix(path, "programs/") && validSingleUUID(strings.TrimPrefix(path, "programs/")):
		handler.methodNotAllowed(writer, request, http.MethodPatch)
	case path == "cohorts":
		handler.methodNotAllowed(writer, request, http.MethodPost)
	case strings.HasPrefix(path, "cohorts/") && strings.HasSuffix(path, "/enrollments"):
		handler.methodNotAllowed(writer, request, http.MethodPost)
	case strings.HasPrefix(path, "cohorts/") && strings.Contains(path, "/enrollments/"):
		handler.methodNotAllowed(writer, request, http.MethodDelete)
	case path == "role-packs":
		handler.methodNotAllowed(writer, request, http.MethodPost)
	case path == "task-packs":
		handler.methodNotAllowed(writer, request, http.MethodPost)
	default:
		handler.problem(writer, request, 404, "resource_not_found", false)
	}
}

func (handler EnterpriseAdminHandler) createProgram(w http.ResponseWriter, r *http.Request, m CommandMetadata) {
	var body struct {
		RequestID string          `json:"request_id"`
		Name      string          `json:"name"`
		Settings  json.RawMessage `json:"settings"`
	}
	if !decodeEnterprise(w, r, "application/vnd.lites.program-create.v2+json", &body) || !validClientRequestID(body.RequestID) || !validEnterpriseName(body.Name) || !validJSONObject(body.Settings) {
		handler.problem(w, r, 400, "validation_failed", false)
		return
	}
	m.ClientRequestID = body.RequestID
	result, err := handler.Service.CreateProgram(r.Context(), CreateProgramCommand{CommandMetadata: m, Name: strings.TrimSpace(body.Name), Settings: body.Settings})
	handler.finishResult(w, r, result, err)
}

func (handler EnterpriseAdminHandler) updateProgram(w http.ResponseWriter, r *http.Request, m CommandMetadata, id string) {
	var body struct {
		RequestID string          `json:"request_id"`
		Name      string          `json:"name"`
		Status    string          `json:"status"`
		Settings  json.RawMessage `json:"settings"`
		Expected  uint64          `json:"expected_program_version"`
	}
	if !decodeEnterprise(w, r, "application/vnd.lites.program-update.v2+json", &body) || !validClientRequestID(body.RequestID) || !validEnterpriseName(body.Name) || (body.Status != "active" && body.Status != "paused" && body.Status != "archived") || !validJSONObject(body.Settings) || body.Expected < 1 {
		handler.problem(w, r, 400, "validation_failed", false)
		return
	}
	if expected, ok := ifMatch(r); !ok || expected != body.Expected {
		handler.problem(w, r, 409, "version_conflict", true)
		return
	}
	m.ClientRequestID = body.RequestID
	result, err := handler.Service.UpdateProgram(r.Context(), UpdateProgramCommand{CommandMetadata: m, ProgramID: id, Name: strings.TrimSpace(body.Name), Status: body.Status, Settings: body.Settings, ExpectedVersion: body.Expected})
	handler.finishResult(w, r, result, err)
}

func (handler EnterpriseAdminHandler) createCohort(w http.ResponseWriter, r *http.Request, m CommandMetadata) {
	var body struct {
		RequestID string     `json:"request_id"`
		ProgramID string     `json:"program_id"`
		Name      string     `json:"name"`
		StartsAt  *time.Time `json:"starts_at"`
		EndsAt    *time.Time `json:"ends_at"`
	}
	if !decodeEnterprise(w, r, "application/vnd.lites.cohort-create.v2+json", &body) || !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.ProgramID) || !validEnterpriseName(body.Name) || (body.StartsAt != nil && body.EndsAt != nil && !body.EndsAt.After(*body.StartsAt)) {
		handler.problem(w, r, 400, "validation_failed", false)
		return
	}
	m.ClientRequestID = body.RequestID
	result, err := handler.Service.CreateCohort(r.Context(), CreateCohortCommand{CommandMetadata: m, ProgramID: body.ProgramID, Name: strings.TrimSpace(body.Name), StartsAt: body.StartsAt, EndsAt: body.EndsAt})
	handler.finishResult(w, r, result, err)
}

func (handler EnterpriseAdminHandler) enroll(w http.ResponseWriter, r *http.Request, m CommandMetadata, cohortID string) {
	var body struct {
		RequestID string   `json:"request_id"`
		UserIDs   []string `json:"user_ids"`
		Expected  uint64   `json:"expected_cohort_version"`
	}
	if !decodeEnterprise(w, r, "application/vnd.lites.cohort-enroll.v2+json", &body) || !validClientRequestID(body.RequestID) || !validUUIDSet(body.UserIDs, 1, 1000) || body.Expected < 1 {
		handler.problem(w, r, 400, "validation_failed", false)
		return
	}
	if expected, ok := ifMatch(r); !ok || expected != body.Expected {
		handler.problem(w, r, 409, "version_conflict", true)
		return
	}
	m.ClientRequestID = body.RequestID
	result, err := handler.Service.EnrollCohort(r.Context(), EnrollCohortCommand{CommandMetadata: m, CohortID: cohortID, UserIDs: body.UserIDs, ExpectedVersion: body.Expected})
	handler.finishResult(w, r, result, err)
}

func (handler EnterpriseAdminHandler) unenroll(w http.ResponseWriter, r *http.Request, m CommandMetadata, cohortID, userID string) {
	var body struct {
		RequestID string `json:"request_id"`
		Reason    string `json:"reason"`
		Expected  uint64 `json:"expected_cohort_version"`
	}
	if !decodeEnterprise(w, r, "application/vnd.lites.cohort-unenroll.v2+json", &body) || !validClientRequestID(body.RequestID) || strings.TrimSpace(body.Reason) == "" || len(body.Reason) > 1000 || body.Expected < 1 {
		handler.problem(w, r, 400, "validation_failed", false)
		return
	}
	if expected, ok := ifMatch(r); !ok || expected != body.Expected {
		handler.problem(w, r, 409, "version_conflict", true)
		return
	}
	m.ClientRequestID = body.RequestID
	result, err := handler.Service.UnenrollCohort(r.Context(), UnenrollCohortCommand{CommandMetadata: m, CohortID: cohortID, TargetUserID: userID, Reason: strings.TrimSpace(body.Reason), ExpectedVersion: body.Expected})
	handler.finishResult(w, r, result, err)
}

func (handler EnterpriseAdminHandler) publishRolePack(w http.ResponseWriter, r *http.Request, m CommandMetadata) {
	var body struct {
		RequestID       string   `json:"request_id"`
		ProgramID       string   `json:"program_id"`
		Revision        int      `json:"revision"`
		RoleProfileIDs  []string `json:"role_profile_ids"`
		TaskTemplateIDs []string `json:"task_template_ids"`
	}
	if !decodeEnterprise(w, r, "application/vnd.lites.role-pack-publish.v2+json", &body) || !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.ProgramID) || body.Revision < 1 || !validUUIDSet(body.RoleProfileIDs, 1, 100) || !validUUIDSet(body.TaskTemplateIDs, 1, 500) {
		handler.problem(w, r, 400, "validation_failed", false)
		return
	}
	m.ClientRequestID = body.RequestID
	result, err := handler.Service.PublishRolePack(r.Context(), PublishRolePackCommand{CommandMetadata: m, ProgramID: body.ProgramID, Revision: body.Revision, RoleProfileIDs: body.RoleProfileIDs, TaskTemplateIDs: body.TaskTemplateIDs})
	handler.finishResult(w, r, result, err)
}

func (handler EnterpriseAdminHandler) publishTaskPack(w http.ResponseWriter, r *http.Request, m CommandMetadata) {
	var body struct {
		RequestID       string          `json:"request_id"`
		ProgramID       string          `json:"program_id"`
		Revision        int             `json:"revision"`
		Name            string          `json:"name"`
		TaskTemplateIDs []string        `json:"task_template_ids"`
		Assignment      json.RawMessage `json:"assignment"`
	}
	if !decodeEnterprise(w, r, "application/vnd.lites.task-pack-publish.v2+json", &body) || !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.ProgramID) || body.Revision < 1 || !validEnterpriseName(body.Name) || !validUUIDSet(body.TaskTemplateIDs, 1, 500) || !validJSONObject(body.Assignment) {
		handler.problem(w, r, 400, "validation_failed", false)
		return
	}
	m.ClientRequestID = body.RequestID
	result, err := handler.Service.PublishTaskPack(r.Context(), PublishTaskPackCommand{CommandMetadata: m, ProgramID: body.ProgramID, Revision: body.Revision, Name: strings.TrimSpace(body.Name), TaskTemplateIDs: body.TaskTemplateIDs, Assignment: body.Assignment})
	handler.finishResult(w, r, result, err)
}

func (handler EnterpriseAdminHandler) metadata(w http.ResponseWriter, r *http.Request) (CommandMetadata, bool) {
	claims, ok := serviceauth.ClaimsFromContext(r.Context())
	if handler.Service == nil || !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || !claims.CSRFVerified {
		handler.problem(w, r, 401, "authentication_required", false)
		return CommandMetadata{}, false
	}
	if !aggregateRoleAllowed(claims.Roles) {
		handler.problem(w, r, 403, "permission_denied", false)
		return CommandMetadata{}, false
	}
	keys := r.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		handler.problem(w, r, 400, "validation_failed", false)
		return CommandMetadata{}, false
	}
	return CommandMetadata{RequestID: claims.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, true
}

func (handler EnterpriseAdminHandler) finishResult(w http.ResponseWriter, r *http.Request, result EnterpriseResource, err error) {
	if err != nil {
		handler.finish(w, r, err)
		return
	}
	w.Header().Set("Content-Type", enterpriseResourceMediaType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	_ = json.NewEncoder(w).Encode(result)
}
func (handler EnterpriseAdminHandler) finish(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		handler.problem(w, r, 400, "validation_failed", false)
	case errors.Is(err, ErrPermissionDenied):
		handler.problem(w, r, 403, "permission_denied", false)
	case errors.Is(err, ErrResourceNotFound):
		handler.problem(w, r, 404, "resource_not_found", false)
	case errors.Is(err, ErrStateConflict), errors.Is(err, ErrIdempotencyConflict):
		handler.problem(w, r, 409, "state_conflict", true)
	default:
		handler.problem(w, r, 503, "dependency_unavailable", true)
	}
}
func (handler EnterpriseAdminHandler) problem(w http.ResponseWriter, r *http.Request, status int, code string, retry bool) {
	(RouteHandler{}).writeProblem(w, r, status, code, strings.ReplaceAll(code, "_", " "), retry)
}

func (handler EnterpriseAdminHandler) methodNotAllowed(w http.ResponseWriter, r *http.Request, allow string) {
	w.Header().Set("Allow", allow)
	handler.problem(w, r, http.StatusMethodNotAllowed, "method_not_allowed", false)
}

func decodeEnterprise(w http.ResponseWriter, r *http.Request, media string, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	decoder.DisallowUnknownFields()
	return r.Header.Get("Content-Type") == media && decoder.Decode(target) == nil && errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}
func validEnterpriseName(value string) bool {
	trimmed := strings.TrimSpace(value)
	return len(trimmed) >= 1 && len(trimmed) <= 200
}
func validJSONObject(value json.RawMessage) bool {
	if len(value) == 0 || len(value) > 64<<10 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	var object map[string]any
	return decoder.Decode(&object) == nil && object != nil && len(object) <= 100 && errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}
func validSingleUUID(value string) bool {
	return !strings.Contains(value, "/") && uuidPattern.MatchString(value)
}
func validUUIDSet(values []string, min, max int) bool {
	if len(values) < min || len(values) > max {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if !uuidPattern.MatchString(value) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
