package api

import (
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

const (
	shareGrantCreateMediaType = "application/vnd.lites.share-grant-create.v2+json"
	shareGrantRevokeMediaType = "application/vnd.lites.share-grant-revoke.v2+json"
	shareGrantMediaType       = "application/vnd.lites.share-grant.v2+json"
)

type ShareGrantHandler struct{ Service ShareGrantService }

func (handler ShareGrantHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	grantID, valid := shareGrantPath(request.URL.Path)
	if !valid {
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
		return
	}
	switch {
	case grantID == "" && request.Method == http.MethodPost:
		handler.create(writer, request)
	case grantID != "" && request.Method == http.MethodDelete:
		handler.revoke(writer, request, grantID)
	default:
		if grantID == "" {
			writer.Header().Set("Allow", http.MethodPost)
		} else {
			writer.Header().Set("Allow", http.MethodDelete)
		}
		handler.problem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", false)
	}
}

func (handler ShareGrantHandler) create(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID        string     `json:"request_id"`
		GranteeUserID    string     `json:"grantee_user_id"`
		ResourceKind     string     `json:"resource_kind"`
		ResourceID       string     `json:"resource_id"`
		ResourceRevision string     `json:"resource_revision"`
		Scope            []string   `json:"scope"`
		ExpiresAt        *time.Time `json:"expires_at"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if request.Header.Get("Content-Type") != shareGrantCreateMediaType || decoder.Decode(&body) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.GranteeUserID) || body.GranteeUserID == metadata.UserID || !validShareResource(body.ResourceKind, body.ResourceID, body.ResourceRevision) || !validShareScope(body.Scope) || body.ExpiresAt != nil && !body.ExpiresAt.After(time.Now().UTC()) {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.Create(request.Context(), CreateShareGrantCommand{CommandMetadata: metadata, GranteeUserID: body.GranteeUserID, ResourceKind: body.ResourceKind, ResourceID: body.ResourceID, ResourceRevision: body.ResourceRevision, Scope: body.Scope, ExpiresAt: body.ExpiresAt})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, result)
}

func (handler ShareGrantHandler) revoke(writer http.ResponseWriter, request *http.Request, grantID string) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID            string `json:"request_id"`
		Reason               string `json:"reason"`
		ExpectedGrantVersion uint64 `json:"expected_grant_version"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if request.Header.Get("Content-Type") != shareGrantRevokeMediaType || decoder.Decode(&body) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || !validClientRequestID(body.RequestID) || strings.TrimSpace(body.Reason) == "" || len(body.Reason) > 1000 || body.ExpectedGrantVersion < 1 {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	if expected, matched := ifMatch(request); !matched || expected != body.ExpectedGrantVersion {
		handler.problem(writer, request, http.StatusConflict, "version_conflict", true)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.Revoke(request.Context(), RevokeShareGrantCommand{CommandMetadata: metadata, GrantID: grantID, Reason: strings.TrimSpace(body.Reason), ExpectedGrantVersion: body.ExpectedGrantVersion})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, result)
}

func (handler ShareGrantHandler) metadata(writer http.ResponseWriter, request *http.Request) (CommandMetadata, bool) {
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if handler.Service == nil || !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || !claims.CSRFVerified {
		handler.problem(writer, request, http.StatusUnauthorized, "authentication_required", false)
		return CommandMetadata{}, false
	}
	keys := request.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return CommandMetadata{}, false
	}
	return CommandMetadata{RequestID: claims.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, true
}

func (handler ShareGrantHandler) write(writer http.ResponseWriter, result ShareGrantResult) {
	writer.Header().Set("Content-Type", shareGrantMediaType)
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler ShareGrantHandler) finish(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
	case errors.Is(err, ErrPermissionDenied):
		handler.problem(writer, request, http.StatusForbidden, "permission_denied", false)
	case errors.Is(err, ErrResourceNotFound):
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
	case errors.Is(err, ErrStateConflict), errors.Is(err, ErrIdempotencyConflict):
		handler.problem(writer, request, http.StatusConflict, "state_conflict", true)
	default:
		handler.problem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", true)
	}
}

func (handler ShareGrantHandler) problem(writer http.ResponseWriter, request *http.Request, status int, code string, retryable bool) {
	(RouteHandler{}).writeProblem(writer, request, status, code, strings.ReplaceAll(code, "_", " "), retryable)
}

func shareGrantPath(path string) (string, bool) {
	if path == "/v1/share-grants" {
		return "", true
	}
	value := strings.TrimPrefix(path, "/v1/share-grants/")
	if value == path || strings.Contains(value, "/") || !uuidPattern.MatchString(value) {
		return "", false
	}
	return value, true
}

func validShareResource(kind, id, revision string) bool {
	if kind != "evidence" && kind != "workspace" && kind != "artifact" && kind != "project" {
		return false
	}
	return uuidPattern.MatchString(id) && strings.TrimSpace(revision) == revision && len(revision) >= 1 && len(revision) <= 500
}

func validShareScope(scope []string) bool {
	if len(scope) < 1 || len(scope) > 2 {
		return false
	}
	seen := map[string]bool{}
	for _, value := range scope {
		if value != "read" && value != "review" || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
