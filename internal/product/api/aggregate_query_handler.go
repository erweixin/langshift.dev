package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const (
	aggregateQueryRequestMediaType = "application/vnd.lites.aggregate-query-request.v2+json"
	aggregateQueryMediaType        = "application/vnd.lites.aggregate-query.v2+json"
)

type AggregateQueryHandler struct{ Service AggregateQueryService }

func (handler AggregateQueryHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v1/admin/aggregate-queries" {
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		handler.problem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", false)
		return
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if handler.Service == nil || !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || !claims.CSRFVerified {
		handler.problem(writer, request, http.StatusUnauthorized, "authentication_required", false)
		return
	}
	if !aggregateRoleAllowed(claims.Roles) {
		handler.problem(writer, request, http.StatusForbidden, "permission_denied", false)
		return
	}
	keys := request.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	var body struct {
		RequestID  string            `json:"request_id"`
		SnapshotID string            `json:"snapshot_id"`
		MetricKey  string            `json:"metric_key"`
		Dimensions map[string]string `json:"dimensions"`
		TimeBucket string            `json:"time_bucket"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if request.Header.Get("Content-Type") != aggregateQueryRequestMediaType || decoder.Decode(&body) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.SnapshotID) || !validAggregateMetric(body.MetricKey) || !validAggregateBucket(body.TimeBucket) || !validAggregateDimensions(body.Dimensions) {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	result, err := handler.Service.Query(request.Context(), AggregateQueryCommand{CommandMetadata: CommandMetadata{RequestID: claims.RequestID, ClientRequestID: body.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, SnapshotID: body.SnapshotID, MetricKey: body.MetricKey, TimeBucket: body.TimeBucket, Dimensions: body.Dimensions})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	if result.Status == "query_budget_exhausted" {
		handler.problem(writer, request, http.StatusTooManyRequests, "rate_limited", false)
		return
	}
	writer.Header().Set("Content-Type", aggregateQueryMediaType)
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler AggregateQueryHandler) finish(writer http.ResponseWriter, request *http.Request, err error) {
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

func (handler AggregateQueryHandler) problem(writer http.ResponseWriter, request *http.Request, status int, code string, retryable bool) {
	(RouteHandler{}).writeProblem(writer, request, status, code, strings.ReplaceAll(code, "_", " "), retryable)
}

func aggregateRoleAllowed(roles []string) bool {
	for _, role := range roles {
		if role == "owner" || role == "admin" || role == "program_manager" {
			return true
		}
	}
	return false
}

func validAggregateMetric(value string) bool {
	switch value {
	case "active_members", "task_completion_rate", "weekly_loop_completion_rate", "project_completion_rate", "aggregate_credit_usage":
		return true
	default:
		return false
	}
}

func validAggregateBucket(value string) bool {
	return value == "week" || value == "month" || value == "quarter"
}

func validAggregateDimensions(values map[string]string) bool {
	if len(values) != 1 {
		return false
	}
	for name, value := range values {
		if name != "program" && name != "cohort" && name != "role_pack" && name != "locale" && name != "coarse_week" {
			return false
		}
		if strings.TrimSpace(value) == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	return true
}
