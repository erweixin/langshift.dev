package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

type AggregateSnapshotHandler struct{ Service AggregateSnapshotService }

func (handler AggregateSnapshotHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v1/admin/aggregate-snapshots" {
		(RouteHandler{}).writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		(RouteHandler{}).writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
		return
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if handler.Service == nil || !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || !claims.CSRFVerified {
		(RouteHandler{}).writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return
	}
	if !aggregateRoleAllowed(claims.Roles) {
		(RouteHandler{}).writeProblem(writer, request, http.StatusForbidden, "permission_denied", "Permission denied", false)
		return
	}
	keys := request.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		(RouteHandler{}).validationFailed(writer, request)
		return
	}
	var body struct {
		RequestID   string            `json:"request_id"`
		MetricKey   string            `json:"metric_key"`
		Dimensions  map[string]string `json:"dimensions"`
		TimeBucket  string            `json:"time_bucket"`
		PeriodStart string            `json:"period_start"`
		PeriodEnd   string            `json:"period_end"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if request.Header.Get("Content-Type") != "application/json" || decoder.Decode(&body) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		(RouteHandler{}).validationFailed(writer, request)
		return
	}
	start, startErr := time.Parse(time.DateOnly, body.PeriodStart)
	end, endErr := time.Parse(time.DateOnly, body.PeriodEnd)
	if !validClientRequestID(body.RequestID) || !validAggregateMetric(body.MetricKey) || !validAggregateBucket(body.TimeBucket) || !validAggregateDimensions(body.Dimensions) || startErr != nil || endErr != nil || !validAggregatePeriod(body.TimeBucket, start, end) {
		(RouteHandler{}).validationFailed(writer, request)
		return
	}
	result, err := handler.Service.Create(request.Context(), AggregateSnapshotCommand{CommandMetadata: CommandMetadata{RequestID: claims.RequestID, ClientRequestID: body.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, MetricKey: body.MetricKey, Dimensions: body.Dimensions, TimeBucket: body.TimeBucket, PeriodStart: start, PeriodEnd: end})
	if err != nil {
		(AggregateQueryHandler{}).finish(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("ETag", `"1"`)
	_ = json.NewEncoder(writer).Encode(result)
}

func validAggregatePeriod(bucket string, start, end time.Time) bool {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return false
	}
	days := int(end.Sub(start).Hours()/24) + 1
	switch bucket {
	case "week":
		return days <= 7
	case "month":
		return days <= 31
	case "quarter":
		return days <= 92
	default:
		return false
	}
}
