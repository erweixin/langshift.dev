package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

type GenerateReviewCommand struct {
	CommandMetadata
	SubmissionID, RubricVersionID string
	ExpectedSubmissionRevision    int
	ExpectedTaskVersion           uint64
}

type ReviewGenerationResult struct {
	GenerationID string    `json:"generation_id"`
	RunID        string    `json:"run_id"`
	Status       string    `json:"status"`
	AcceptedAt   time.Time `json:"accepted_at"`
	Replayed     bool      `json:"replayed"`
}

type ReviewResource struct {
	ID                 string          `json:"id"`
	SubmissionID       string          `json:"submission_id"`
	RubricVersionID    string          `json:"rubric_version_id"`
	DailyTaskID        string          `json:"daily_task_id"`
	Status             string          `json:"status"`
	EvidenceID         string          `json:"evidence_id"`
	Version            uint64          `json:"version"`
	SubmissionRevision int             `json:"submission_revision"`
	Review             json.RawMessage `json:"review"`
	ReviewedAt         time.Time       `json:"reviewed_at"`
}
type ReviewGetQuery struct{ TenantID, UserID, ReviewID string }

type ReviewService interface {
	Generate(context.Context, GenerateReviewCommand) (ReviewGenerationResult, error)
	Get(context.Context, ReviewGetQuery) (ReviewResource, error)
}
type ReviewHandler struct{ Service ReviewService }

func (handler ReviewHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v1/reviews" {
		id, ok := reviewPath(request.URL.Path)
		if !ok {
			(RouteHandler{}).writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
			return
		}
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			(RouteHandler{}).writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
			return
		}
		handler.get(writer, request, id)
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
	keys := request.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		(RouteHandler{}).writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Validation failed", false)
		return
	}
	var body struct {
		RequestID                  string `json:"request_id"`
		SubmissionID               string `json:"submission_id"`
		RubricVersionID            string `json:"rubric_version_id"`
		ExpectedSubmissionRevision int    `json:"expected_submission_revision"`
		ExpectedTaskVersion        uint64 `json:"expected_task_version"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if request.Header.Get("Content-Type") != "application/vnd.lites.review-generate.v2+json" || decoder.Decode(&body) != nil || decoder.Decode(&struct{}{}) == nil || !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.SubmissionID) || !uuidPattern.MatchString(body.RubricVersionID) || body.ExpectedSubmissionRevision < 1 || body.ExpectedTaskVersion < 1 {
		(RouteHandler{}).writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Validation failed", false)
		return
	}
	expected, valid := ifMatch(request)
	if !valid || expected != body.ExpectedTaskVersion {
		(RouteHandler{}).writeProblem(writer, request, http.StatusConflict, "version_conflict", "Version conflict", true)
		return
	}
	result, err := handler.Service.Generate(request.Context(), GenerateReviewCommand{CommandMetadata: CommandMetadata{RequestID: claims.RequestID, ClientRequestID: body.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, SubmissionID: body.SubmissionID, RubricVersionID: body.RubricVersionID, ExpectedSubmissionRevision: body.ExpectedSubmissionRevision, ExpectedTaskVersion: body.ExpectedTaskVersion})
	if err != nil {
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
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.lites.review-generation.v2+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(body.ExpectedTaskVersion, 10)+`"`)
	writer.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler ReviewHandler) get(writer http.ResponseWriter, request *http.Request, id string) {
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if handler.Service == nil || !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" {
		(RouteHandler{}).writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return
	}
	result, err := handler.Service.Get(request.Context(), ReviewGetQuery{TenantID: claims.TenantID, UserID: claims.SubjectID, ReviewID: id})
	if err != nil {
		status, code := http.StatusServiceUnavailable, "dependency_unavailable"
		if errors.Is(err, ErrResourceNotFound) {
			status, code = http.StatusNotFound, "resource_not_found"
		}
		(RouteHandler{}).writeProblem(writer, request, status, code, code, status >= 500)
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.lites.review.v2+json")
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	_ = json.NewEncoder(writer).Encode(result)
}
func reviewPath(path string) (string, bool) {
	const prefix = "/v1/reviews/"
	if len(path) <= len(prefix) || path[:len(prefix)] != prefix {
		return "", false
	}
	id := path[len(prefix):]
	return id, uuidPattern.MatchString(id)
}
