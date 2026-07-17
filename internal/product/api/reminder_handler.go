package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
	"io"
	"net/http"
	"strconv"
	"time"
)

type ReminderResource struct {
	ID               string     `json:"id"`
	Version          uint64     `json:"version"`
	Status           string     `json:"status"`
	Timezone         string     `json:"timezone"`
	LocalTime        string     `json:"local_time"`
	Weekdays         []int      `json:"weekdays"`
	Channel          string     `json:"channel"`
	NextOccurrenceAt *time.Time `json:"next_occurrence_at"`
	CancelledAt      *time.Time `json:"cancelled_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	Replayed         bool       `json:"replayed,omitempty"`
}
type ReminderListQuery struct{ TenantID, UserID string }
type ReminderListResult struct {
	Items []ReminderResource `json:"items"`
}
type CreateReminderCommand struct {
	CommandMetadata
	Timezone, LocalTime, Channel string
	Weekdays                     []int
}
type UpdateReminderCommand struct {
	CommandMetadata
	ReminderID, Action, Timezone, LocalTime, Channel string
	Weekdays                                         []int
	ExpectedVersion                                  uint64
}
type ReminderService interface {
	List(context.Context, ReminderListQuery) (ReminderListResult, error)
	Create(context.Context, CreateReminderCommand) (ReminderResource, error)
	Update(context.Context, UpdateReminderCommand) (ReminderResource, error)
}
type ReminderHandler struct{ Service ReminderService }

func (h ReminderHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, item := reminderPath(r.URL.Path)
	if !item && r.URL.Path != "/v1/reminder-schedules" {
		(RouteHandler{}).writeProblem(w, r, 404, "resource_not_found", "Resource not found", false)
		return
	}
	claims, ok := serviceauth.ClaimsFromContext(r.Context())
	if h.Service == nil || !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.TenantID == "" || claims.SubjectID == "" {
		(RouteHandler{}).writeProblem(w, r, 401, "authentication_required", "Authentication required", false)
		return
	}
	if !item && r.Method == http.MethodGet {
		result, e := h.Service.List(r.Context(), ReminderListQuery{TenantID: claims.TenantID, UserID: claims.SubjectID})
		if e != nil {
			h.finish(w, r, e)
			return
		}
		if result.Items == nil {
			result.Items = []ReminderResource{}
		}
		w.Header().Set("Content-Type", "application/vnd.lites.reminder-list.v2+json")
		w.Header().Set("Cache-Control", "private, no-store")
		_ = json.NewEncoder(w).Encode(result)
		return
	}
	if claims.SessionID == "" || !claims.CSRFVerified {
		(RouteHandler{}).writeProblem(w, r, 401, "authentication_required", "Authentication required", false)
		return
	}
	keys := r.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		(RouteHandler{}).writeProblem(w, r, 400, "validation_failed", "Validation failed", false)
		return
	}
	if !item && r.Method == http.MethodPost {
		var body struct {
			RequestID string `json:"request_id"`
			Timezone  string `json:"timezone"`
			LocalTime string `json:"local_time"`
			Weekdays  []int  `json:"weekdays"`
			Channel   string `json:"channel"`
		}
		if !decodeReminder(w, r, "application/vnd.lites.reminder-create.v2+json", &body) || !validClientRequestID(body.RequestID) {
			(RouteHandler{}).writeProblem(w, r, 400, "validation_failed", "Validation failed", false)
			return
		}
		result, e := h.Service.Create(r.Context(), CreateReminderCommand{CommandMetadata: CommandMetadata{RequestID: claims.RequestID, ClientRequestID: body.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, Timezone: body.Timezone, LocalTime: body.LocalTime, Weekdays: body.Weekdays, Channel: body.Channel})
		if e != nil {
			h.finish(w, r, e)
			return
		}
		h.write(w, http.StatusCreated, result)
		return
	}
	if item && r.Method == http.MethodPatch {
		var body struct {
			RequestID       string `json:"request_id"`
			Action          string `json:"action"`
			Timezone        string `json:"timezone"`
			LocalTime       string `json:"local_time"`
			Weekdays        []int  `json:"weekdays"`
			Channel         string `json:"channel"`
			ExpectedVersion uint64 `json:"expected_schedule_version"`
		}
		if !decodeReminder(w, r, "application/vnd.lites.reminder-update.v2+json", &body) || !validClientRequestID(body.RequestID) || body.ExpectedVersion < 1 {
			(RouteHandler{}).writeProblem(w, r, 400, "validation_failed", "Validation failed", false)
			return
		}
		expected, valid := ifMatch(r)
		if !valid || expected != body.ExpectedVersion {
			(RouteHandler{}).writeProblem(w, r, 409, "version_conflict", "Version conflict", true)
			return
		}
		result, e := h.Service.Update(r.Context(), UpdateReminderCommand{CommandMetadata: CommandMetadata{RequestID: claims.RequestID, ClientRequestID: body.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, ReminderID: id, Action: body.Action, Timezone: body.Timezone, LocalTime: body.LocalTime, Weekdays: body.Weekdays, Channel: body.Channel, ExpectedVersion: body.ExpectedVersion})
		if e != nil {
			h.finish(w, r, e)
			return
		}
		h.write(w, 200, result)
		return
	}
	w.Header().Set("Allow", map[bool]string{true: "PATCH", false: "GET, POST"}[item])
	(RouteHandler{}).writeProblem(w, r, 405, "method_not_allowed", "Method not allowed", false)
}
func decodeReminder(w http.ResponseWriter, r *http.Request, contentType string, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	return r.Header.Get("Content-Type") == contentType && decoder.Decode(target) == nil && errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}
func (ReminderHandler) write(w http.ResponseWriter, status int, result ReminderResource) {
	w.Header().Set("Content-Type", "application/vnd.lites.reminder.v2+json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}
func (ReminderHandler) finish(w http.ResponseWriter, r *http.Request, e error) {
	status, code, retry := 503, "dependency_unavailable", true
	switch {
	case errors.Is(e, ErrValidation):
		status, code, retry = 400, "validation_failed", false
	case errors.Is(e, ErrResourceNotFound):
		status, code, retry = 404, "resource_not_found", false
	case errors.Is(e, ErrStateConflict), errors.Is(e, ErrIdempotencyConflict):
		status, code = 409, "state_conflict"
	}
	(RouteHandler{}).writeProblem(w, r, status, code, code, retry)
}
func reminderPath(path string) (string, bool) {
	const prefix = "/v1/reminder-schedules/"
	if len(path) <= len(prefix) || path[:len(prefix)] != prefix {
		return "", false
	}
	id := path[len(prefix):]
	return id, uuidPattern.MatchString(id)
}
