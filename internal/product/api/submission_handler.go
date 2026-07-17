package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

type CreateSubmissionCommand struct {
	CommandMetadata
	DailyTaskID, SubmissionKind, Content, Understanding string
	ExpectedTaskVersion                                 uint64
}

type SubmissionMutationResult struct {
	ID                 string `json:"id"`
	Version            uint64 `json:"version"`
	SubmissionRevision int    `json:"submission_revision"`
	DailyTaskID        string `json:"daily_task_id"`
	DailyTaskVersion   uint64 `json:"daily_task_version"`
	Status             string `json:"status"`
	UpdatedAt          string `json:"updated_at"`
	EventID            string `json:"event_id"`
	Replayed           bool   `json:"replayed"`
}

type SubmissionService interface {
	Create(context.Context, CreateSubmissionCommand) (SubmissionMutationResult, error)
}

type SubmissionHandler struct{ Service SubmissionService }

func (handler SubmissionHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v1/submissions" {
		(RouteHandler{}).writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		(RouteHandler{}).writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
		return
	}
	claims, ok := handler.claims(writer, request)
	if !ok {
		return
	}
	keys := request.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		(RouteHandler{}).writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Validation failed", false)
		return
	}
	var body struct {
		RequestID           string `json:"request_id"`
		DailyTaskID         string `json:"daily_task_id"`
		SubmissionKind      string `json:"submission_kind"`
		Content             string `json:"content"`
		Understanding       string `json:"understanding"`
		ExpectedTaskVersion uint64 `json:"expected_task_version"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 512<<10))
	decoder.DisallowUnknownFields()
	if request.Header.Get("Content-Type") != "application/vnd.lites.submission-create.v2+json" || decoder.Decode(&body) != nil || decoder.Decode(&struct{}{}) == nil || !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.DailyTaskID) || body.ExpectedTaskVersion == 0 || body.SubmissionKind != "code" && body.SubmissionKind != "writing" && body.SubmissionKind != "design" || len(body.Content) < 1 || len(body.Content) > 500000 || len(body.Understanding) < 1 || len(body.Understanding) > 4000 {
		(RouteHandler{}).writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Validation failed", false)
		return
	}
	expected, valid := ifMatch(request)
	if !valid || expected != body.ExpectedTaskVersion {
		(RouteHandler{}).writeProblem(writer, request, http.StatusConflict, "version_conflict", "Version conflict", true)
		return
	}
	result, err := handler.Service.Create(request.Context(), CreateSubmissionCommand{CommandMetadata: CommandMetadata{RequestID: claims.RequestID, ClientRequestID: body.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, DailyTaskID: body.DailyTaskID, SubmissionKind: body.SubmissionKind, Content: body.Content, Understanding: body.Understanding, ExpectedTaskVersion: body.ExpectedTaskVersion})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.lites.submission.v2+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.DailyTaskVersion, 10)+`"`)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler SubmissionHandler) claims(writer http.ResponseWriter, request *http.Request) (trustedcontext.Claims, bool) {
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if handler.Service == nil || !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || !claims.CSRFVerified {
		(RouteHandler{}).writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return trustedcontext.Claims{}, false
	}
	return claims, true
}

func (SubmissionHandler) finish(writer http.ResponseWriter, request *http.Request, err error) {
	status, code, retry := http.StatusServiceUnavailable, "dependency_unavailable", true
	switch {
	case errors.Is(err, ErrValidation):
		status, code, retry = http.StatusBadRequest, "validation_failed", false
	case errors.Is(err, ErrResourceNotFound):
		status, code, retry = http.StatusNotFound, "resource_not_found", false
	case errors.Is(err, ErrStateConflict), errors.Is(err, ErrIdempotencyConflict):
		status, code = http.StatusConflict, "state_conflict"
	}
	(RouteHandler{}).writeProblem(writer, request, status, code, code, retry)
}
