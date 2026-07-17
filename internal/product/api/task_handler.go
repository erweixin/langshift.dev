package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

type DailyTaskResource struct {
	ID, MissionID, RouteRevisionID string          `json:"id"`
	Version                        uint64          `json:"version"`
	Status                         string          `json:"status"`
	PracticeKind                   string          `json:"practice_kind"`
	Task                           json.RawMessage `json:"task"`
	EstimatedMinutes               int             `json:"estimated_minutes"`
	Difficulty                     string          `json:"difficulty"`
	FocusVersion                   uint64          `json:"focus_version"`
	ScheduledFor                   string          `json:"scheduled_for"`
	RescheduledTo                  *string         `json:"rescheduled_to"`
	CurrentSubmissionID            *string         `json:"current_submission_id"`
	CurrentReviewID                *string         `json:"current_review_id"`
	CompletedAt                    *time.Time      `json:"completed_at"`
	CreatedAt                      time.Time       `json:"created_at"`
	UpdatedAt                      time.Time       `json:"updated_at"`
}

type DailyTaskListQuery struct{ TenantID, UserID, Cursor string }
type DailyTaskListResult struct {
	Items      []DailyTaskResource `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

type UpdateDailyTaskCommand struct {
	CommandMetadata
	TaskID              string
	Action              string
	ExpectedTaskVersion uint64
	RescheduleFor       string
}

type DailyTaskMutationResult struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
	EventID   string    `json:"event_id"`
	Replayed  bool      `json:"replayed"`
}

type DailyTaskService interface {
	List(context.Context, DailyTaskListQuery) (DailyTaskListResult, error)
	Update(context.Context, UpdateDailyTaskCommand) (DailyTaskMutationResult, error)
}

type DailyTaskHandler struct{ Service DailyTaskService }

func (handler DailyTaskHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/v1/daily-tasks" {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			handler.problem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", false)
			return
		}
		handler.list(writer, request)
		return
	}
	id, ok := dailyTaskPath(request.URL.Path)
	if !ok {
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
		return
	}
	if request.Method != http.MethodPatch {
		writer.Header().Set("Allow", http.MethodPatch)
		handler.problem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", false)
		return
	}
	handler.update(writer, request, id)
}

func (handler DailyTaskHandler) list(writer http.ResponseWriter, request *http.Request) {
	claims, ok := handler.claims(writer, request, false)
	if !ok {
		return
	}
	values := request.URL.Query()
	for name, entries := range values {
		if name != "cursor" || len(entries) != 1 || entries[0] == "" {
			handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
			return
		}
	}
	result, err := handler.Service.List(request.Context(), DailyTaskListQuery{TenantID: claims.TenantID, UserID: claims.SubjectID, Cursor: values.Get("cursor")})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	if result.Items == nil {
		result.Items = []DailyTaskResource{}
	}
	writer.Header().Set("Content-Type", "application/vnd.lites.daily-tasks.v2+json")
	writer.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler DailyTaskHandler) update(writer http.ResponseWriter, request *http.Request, taskID string) {
	claims, ok := handler.claims(writer, request, true)
	if !ok {
		return
	}
	keys := request.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	var body struct {
		RequestID           string `json:"request_id"`
		Action              string `json:"action"`
		RescheduleFor       string `json:"reschedule_for"`
		ExpectedTaskVersion uint64 `json:"expected_task_version"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if request.Header.Get("Content-Type") != "application/vnd.lites.daily-task-update.v2+json" || decoder.Decode(&body) != nil || decoder.Decode(&struct{}{}) == nil || !validClientRequestID(body.RequestID) || body.ExpectedTaskVersion == 0 {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	expected, valid := ifMatch(request)
	if !valid || expected != body.ExpectedTaskVersion {
		handler.problem(writer, request, http.StatusConflict, "version_conflict", true)
		return
	}
	if body.Action != "start" && body.Action != "skip" && body.Action != "reschedule" || body.Action == "reschedule" && !validDate(body.RescheduleFor) || body.Action != "reschedule" && body.RescheduleFor != "" {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	metadata := CommandMetadata{RequestID: claims.RequestID, ClientRequestID: body.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}
	result, err := handler.Service.Update(request.Context(), UpdateDailyTaskCommand{CommandMetadata: metadata, TaskID: taskID, Action: body.Action, ExpectedTaskVersion: body.ExpectedTaskVersion, RescheduleFor: body.RescheduleFor})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.lites.daily-task-mutation.v2+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler DailyTaskHandler) claims(writer http.ResponseWriter, request *http.Request, csrf bool) (trustedcontext.Claims, bool) {
	if handler.Service == nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", true)
		return trustedcontext.Claims{}, false
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || csrf && !claims.CSRFVerified {
		handler.problem(writer, request, http.StatusUnauthorized, "authentication_required", false)
		return trustedcontext.Claims{}, false
	}
	return claims, true
}

func (handler DailyTaskHandler) finish(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
	case errors.Is(err, ErrResourceNotFound):
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
	case errors.Is(err, ErrStateConflict), errors.Is(err, ErrIdempotencyConflict):
		handler.problem(writer, request, http.StatusConflict, "state_conflict", true)
	default:
		handler.problem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", true)
	}
}

func (DailyTaskHandler) problem(writer http.ResponseWriter, request *http.Request, status int, code string, retryable bool) {
	(RouteHandler{}).writeProblem(writer, request, status, code, strings.ReplaceAll(strings.Title(strings.ReplaceAll(code, "_", " ")), " ", " "), retryable)
}

func dailyTaskPath(path string) (string, bool) {
	const prefix = "/v1/daily-tasks/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(path, prefix)
	return id, uuidPattern.MatchString(id)
}

func validDate(value string) bool { _, err := time.Parse("2006-01-02", value); return err == nil }
