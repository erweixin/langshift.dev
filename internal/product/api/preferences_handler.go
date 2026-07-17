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

type CoachPreferences struct {
	SchemaVersion    int    `json:"schema_version"`
	Difficulty       string `json:"difficulty"`
	AvailableMinutes int    `json:"available_minutes"`
	Tone             string `json:"tone"`
	ExplanationDepth string `json:"explanation_depth"`
}

func (p CoachPreferences) Valid() bool {
	return p.SchemaVersion == 1 && (p.Difficulty == "easier" || p.Difficulty == "standard" || p.Difficulty == "harder") && p.AvailableMinutes >= 5 && p.AvailableMinutes <= 480 && (p.Tone == "encouraging" || p.Tone == "direct" || p.Tone == "socratic") && (p.ExplanationDepth == "concise" || p.ExplanationDepth == "balanced" || p.ExplanationDepth == "deep")
}

type PreferencesResource struct {
	ID               string           `json:"id"`
	Version          uint64           `json:"version"`
	Status           string           `json:"status"`
	Locale           string           `json:"locale"`
	Timezone         string           `json:"timezone"`
	CoachPreferences CoachPreferences `json:"coach_preferences"`
	UpdatedAt        time.Time        `json:"updated_at"`
	Replayed         bool             `json:"replayed,omitempty"`
}
type PreferencesQuery struct{ TenantID, UserID string }
type UpdatePreferencesCommand struct {
	CommandMetadata
	Locale, Timezone string
	CoachPreferences CoachPreferences
	ExpectedVersion  uint64
}
type PreferencesService interface {
	Get(context.Context, PreferencesQuery) (PreferencesResource, error)
	Update(context.Context, UpdatePreferencesCommand) (PreferencesResource, error)
}
type PreferencesHandler struct{ Service PreferencesService }

func (h PreferencesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/preferences" {
		(RouteHandler{}).writeProblem(w, r, 404, "resource_not_found", "Resource not found", false)
		return
	}
	claims, ok := serviceauth.ClaimsFromContext(r.Context())
	if h.Service == nil || !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.TenantID == "" || claims.SubjectID == "" {
		(RouteHandler{}).writeProblem(w, r, 401, "authentication_required", "Authentication required", false)
		return
	}
	if r.Method == http.MethodGet {
		result, e := h.Service.Get(r.Context(), PreferencesQuery{TenantID: claims.TenantID, UserID: claims.SubjectID})
		if e != nil {
			h.finish(w, r, e)
			return
		}
		h.write(w, result)
		return
	}
	if r.Method != http.MethodPatch {
		w.Header().Set("Allow", "GET, PATCH")
		(RouteHandler{}).writeProblem(w, r, 405, "method_not_allowed", "Method not allowed", false)
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
	var body struct {
		RequestID        string           `json:"request_id"`
		Locale           string           `json:"locale"`
		Timezone         string           `json:"timezone"`
		CoachPreferences CoachPreferences `json:"coach_preferences"`
		ExpectedVersion  uint64           `json:"expected_preferences_version"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if r.Header.Get("Content-Type") != "application/vnd.lites.preferences-update.v2+json" || decoder.Decode(&body) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || !validClientRequestID(body.RequestID) || body.Locale != "en" && body.Locale != "zh-CN" || len(body.Timezone) < 1 || len(body.Timezone) > 128 || !body.CoachPreferences.Valid() || body.ExpectedVersion < 1 {
		(RouteHandler{}).writeProblem(w, r, 400, "validation_failed", "Validation failed", false)
		return
	}
	expected, valid := ifMatch(r)
	if !valid || expected != body.ExpectedVersion {
		(RouteHandler{}).writeProblem(w, r, 409, "version_conflict", "Version conflict", true)
		return
	}
	result, e := h.Service.Update(r.Context(), UpdatePreferencesCommand{CommandMetadata: CommandMetadata{RequestID: claims.RequestID, ClientRequestID: body.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, Locale: body.Locale, Timezone: body.Timezone, CoachPreferences: body.CoachPreferences, ExpectedVersion: body.ExpectedVersion})
	if e != nil {
		h.finish(w, r, e)
		return
	}
	h.write(w, result)
}
func (PreferencesHandler) write(w http.ResponseWriter, result PreferencesResource) {
	w.Header().Set("Content-Type", "application/vnd.lites.preferences.v2+json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	_ = json.NewEncoder(w).Encode(result)
}
func (PreferencesHandler) finish(w http.ResponseWriter, r *http.Request, e error) {
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
