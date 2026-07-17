package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
	"net/http"
	"time"
)

type EvidenceResource struct {
	ID            string          `json:"id"`
	Version       uint64          `json:"version"`
	MissionID     string          `json:"mission_id"`
	EvidenceType  string          `json:"evidence_type"`
	Status        string          `json:"status"`
	SourceKind    string          `json:"source_kind"`
	SourceID      *string         `json:"source_id"`
	Content       json.RawMessage `json:"content"`
	PlaintextHash string          `json:"plaintext_hash"`
	RecordedAt    time.Time       `json:"recorded_at"`
}
type EvidenceListQuery struct{ TenantID, UserID, Cursor string }
type EvidenceListResult struct {
	Items      []EvidenceResource `json:"items"`
	NextCursor *string            `json:"next_cursor"`
}
type RecordEvidenceCommand struct {
	CommandMetadata
	MissionID, EvidenceType, SourceKind, SourceID, Content, ContentHash string
}
type EvidenceMutationResult struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
	EventID   string    `json:"event_id"`
	Replayed  bool      `json:"replayed"`
}
type EvidenceService interface {
	List(context.Context, EvidenceListQuery) (EvidenceListResult, error)
	Record(context.Context, RecordEvidenceCommand) (EvidenceMutationResult, error)
}
type EvidenceHandler struct{ Service EvidenceService }

func (h EvidenceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/capability-evidence" {
		(RouteHandler{}).writeProblem(w, r, 404, "resource_not_found", "Resource not found", false)
		return
	}
	claims, ok := serviceauth.ClaimsFromContext(r.Context())
	if h.Service == nil || !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" {
		(RouteHandler{}).writeProblem(w, r, 401, "authentication_required", "Authentication required", false)
		return
	}
	if r.Method == http.MethodGet {
		values := r.URL.Query()
		for k, v := range values {
			if k != "cursor" || len(v) != 1 || v[0] == "" {
				(RouteHandler{}).writeProblem(w, r, 400, "validation_failed", "Validation failed", false)
				return
			}
		}
		result, e := h.Service.List(r.Context(), EvidenceListQuery{TenantID: claims.TenantID, UserID: claims.SubjectID, Cursor: values.Get("cursor")})
		if e != nil {
			h.finish(w, r, e)
			return
		}
		if result.Items == nil {
			result.Items = []EvidenceResource{}
		}
		w.Header().Set("Content-Type", "application/vnd.lites.evidence-list.v2+json")
		w.Header().Set("Cache-Control", "private, no-store")
		_ = json.NewEncoder(w).Encode(result)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
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
		RequestID    string  `json:"request_id"`
		MissionID    string  `json:"mission_id"`
		EvidenceType string  `json:"evidence_type"`
		SourceKind   string  `json:"source_kind"`
		SourceID     *string `json:"source_id"`
		Content      string  `json:"content"`
		ContentHash  string  `json:"content_hash"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	dec.DisallowUnknownFields()
	if r.Header.Get("Content-Type") != "application/vnd.lites.evidence-record.v2+json" || dec.Decode(&body) != nil || dec.Decode(&struct{}{}) == nil || !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.MissionID) || len(body.EvidenceType) < 1 || len(body.EvidenceType) > 128 || body.SourceKind != "user_note" && body.SourceKind != "artifact" && body.SourceKind != "test_result" || len(body.Content) < 1 || len(body.Content) > 100000 || len(body.ContentHash) != 64 {
		(RouteHandler{}).writeProblem(w, r, 400, "validation_failed", "Validation failed", false)
		return
	}
	sourceID := ""
	if body.SourceID != nil {
		sourceID = *body.SourceID
		if !uuidPattern.MatchString(sourceID) {
			(RouteHandler{}).writeProblem(w, r, 400, "validation_failed", "Validation failed", false)
			return
		}
	}
	result, e := h.Service.Record(r.Context(), RecordEvidenceCommand{CommandMetadata: CommandMetadata{RequestID: claims.RequestID, ClientRequestID: body.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, MissionID: body.MissionID, EvidenceType: body.EvidenceType, SourceKind: body.SourceKind, SourceID: sourceID, Content: body.Content, ContentHash: body.ContentHash})
	if e != nil {
		h.finish(w, r, e)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.lites.evidence.v2+json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(result)
}
func (EvidenceHandler) finish(w http.ResponseWriter, r *http.Request, e error) {
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
