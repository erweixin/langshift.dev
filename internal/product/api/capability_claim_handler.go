package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maximumCapabilityClaimBodyBytes int64 = 32 * 1024

type CapabilityClaimResource struct {
	ID                string          `json:"id"`
	RevisionID        string          `json:"revision_id"`
	Version           uint64          `json:"version"`
	MissionID         string          `json:"mission_id"`
	CapabilityID      string          `json:"capability_id"`
	Status            string          `json:"status"`
	Origin            string          `json:"origin"`
	VerificationLevel string          `json:"verification_level"`
	Statement         string          `json:"statement"`
	Reason            *string         `json:"reason"`
	EvidenceIDs       []string        `json:"evidence_ids"`
	RecordedAt        time.Time       `json:"recorded_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

type CapabilityClaimListQuery struct {
	TenantID string
	UserID   string
	Cursor   string
}

type CapabilityClaimListResult struct {
	Items      []CapabilityClaimResource `json:"items"`
	NextCursor *string                   `json:"next_cursor"`
}

type CreateCapabilityClaimCommand struct {
	CommandMetadata
	MissionID, CapabilityID, Origin, Statement string
	EvidenceIDs                                []string
}

type ReviseCapabilityClaimCommand struct {
	CommandMetadata
	ClaimIdentityID, Action, Reason, CapabilityID, Statement string
	EvidenceIDs                                              []string
	ExpectedVersion                                          uint64
}

type CapabilityClaimMutationResult struct {
	ID           string    `json:"id"`
	RevisionID   string    `json:"revision_id"`
	Version      uint64    `json:"version"`
	Status       string    `json:"status"`
	ClaimSetHash string    `json:"claim_set_hash"`
	UpdatedAt    time.Time `json:"updated_at"`
	EventID      string    `json:"event_id"`
	Replayed     bool      `json:"replayed"`
}

type CapabilityClaimService interface {
	List(context.Context, CapabilityClaimListQuery) (CapabilityClaimListResult, error)
	Create(context.Context, CreateCapabilityClaimCommand) (CapabilityClaimMutationResult, error)
	Revise(context.Context, ReviseCapabilityClaimCommand) (CapabilityClaimMutationResult, error)
}

type CapabilityClaimHandler struct{ Service CapabilityClaimService }

func (handler CapabilityClaimHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/v1/capability-claims" {
		switch request.Method {
		case http.MethodGet:
			handler.list(writer, request)
		case http.MethodPost:
			handler.create(writer, request)
		default:
			writer.Header().Set("Allow", "GET, POST")
			(RouteHandler{}).writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
		}
		return
	}
	identityID, ok := capabilityClaimRevisionPath(request.URL.Path)
	if !ok {
		(RouteHandler{}).writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		(RouteHandler{}).writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
		return
	}
	handler.revise(writer, request, identityID)
}

func (handler CapabilityClaimHandler) list(writer http.ResponseWriter, request *http.Request) {
	claims, ok := (RouteHandler{Service: claimRouteServiceAdapter{handler.Service}}).claims(writer, request, false)
	if !ok {
		return
	}
	for name, values := range request.URL.Query() {
		if name != "cursor" || len(values) != 1 || values[0] == "" {
			(RouteHandler{}).validationFailed(writer, request)
			return
		}
	}
	result, err := handler.Service.List(request.Context(), CapabilityClaimListQuery{TenantID: claims.TenantID, UserID: claims.SubjectID, Cursor: request.URL.Query().Get("cursor")})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	if result.Items == nil {
		result.Items = []CapabilityClaimResource{}
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler CapabilityClaimHandler) create(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := (RouteHandler{Service: claimRouteServiceAdapter{handler.Service}}).metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID    string   `json:"request_id"`
		MissionID    string   `json:"mission_id"`
		CapabilityID string   `json:"capability_id"`
		Origin       string   `json:"origin"`
		Statement    string   `json:"statement"`
		EvidenceIDs  []string `json:"evidence_ids"`
	}
	if !decodeCapabilityClaimBody(writer, request, &body) || !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.MissionID) || !uuidPattern.MatchString(body.CapabilityID) || !validClaimOrigin(body.Origin) || !validClaimText(body.Statement, 4000) || !validUUIDSet(body.EvidenceIDs, 0, 100) {
		(RouteHandler{}).validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.Create(request.Context(), CreateCapabilityClaimCommand{CommandMetadata: metadata, MissionID: body.MissionID, CapabilityID: body.CapabilityID, Origin: body.Origin, Statement: body.Statement, EvidenceIDs: append([]string(nil), body.EvidenceIDs...)})
	handler.writeMutation(writer, request, result, err)
}

func (handler CapabilityClaimHandler) revise(writer http.ResponseWriter, request *http.Request, identityID string) {
	metadata, ok := (RouteHandler{Service: claimRouteServiceAdapter{handler.Service}}).metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID            string   `json:"request_id"`
		Action               string   `json:"action"`
		Reason               string   `json:"reason"`
		EvidenceIDs          []string `json:"evidence_ids"`
		ExpectedClaimVersion uint64   `json:"expected_claim_version"`
		CapabilityID         string   `json:"capability_id"`
		Statement            string   `json:"statement"`
	}
	if !decodeCapabilityClaimBody(writer, request, &body) || !validClientRequestID(body.RequestID) || !validClaimAction(body.Action) || !validClaimText(body.Reason, 2000) || !validUUIDSet(body.EvidenceIDs, 0, 100) || body.ExpectedClaimVersion < 1 {
		(RouteHandler{}).validationFailed(writer, request)
		return
	}
	if body.Action == "correct" {
		if !uuidPattern.MatchString(body.CapabilityID) || !validClaimText(body.Statement, 4000) {
			(RouteHandler{}).validationFailed(writer, request)
			return
		}
	} else if body.CapabilityID != "" || body.Statement != "" {
		(RouteHandler{}).validationFailed(writer, request)
		return
	}
	if expected, valid := ifMatch(request); !valid || expected != body.ExpectedClaimVersion {
		(RouteHandler{}).writeProblem(writer, request, http.StatusConflict, "version_conflict", "Version conflict", true)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.Revise(request.Context(), ReviseCapabilityClaimCommand{CommandMetadata: metadata, ClaimIdentityID: identityID, Action: body.Action, Reason: body.Reason, EvidenceIDs: append([]string(nil), body.EvidenceIDs...), ExpectedVersion: body.ExpectedClaimVersion, CapabilityID: body.CapabilityID, Statement: body.Statement})
	handler.writeMutation(writer, request, result, err)
}

func (handler CapabilityClaimHandler) writeMutation(writer http.ResponseWriter, request *http.Request, result CapabilityClaimMutationResult, err error) {
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	if result.ID == "" || result.RevisionID == "" || result.Version < 1 || result.Status == "" || result.ClaimSetHash == "" || result.EventID == "" || result.UpdatedAt.IsZero() {
		(RouteHandler{}).writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	_ = json.NewEncoder(writer).Encode(result)
}

func (CapabilityClaimHandler) finish(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		(RouteHandler{}).validationFailed(writer, request)
	case errors.Is(err, ErrPermissionDenied):
		(RouteHandler{}).writeProblem(writer, request, http.StatusForbidden, "permission_denied", "Permission denied", false)
	case errors.Is(err, ErrResourceNotFound):
		(RouteHandler{}).writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
	case errors.Is(err, ErrStateConflict), errors.Is(err, ErrIdempotencyConflict):
		(RouteHandler{}).writeProblem(writer, request, http.StatusConflict, "state_conflict", "State conflict", true)
	default:
		(RouteHandler{}).writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
	}
}

// RouteHandler centralizes trusted-context, CSRF and idempotency parsing. This
// adapter lets claim handlers reuse that boundary without weakening it.
type claimRouteServiceAdapter struct{ CapabilityClaimService }

func (claimRouteServiceAdapter) List(context.Context, RouteListQuery) (RouteListResult, error) {
	return RouteListResult{}, ErrDependencyUnavailable
}
func (claimRouteServiceAdapter) Generate(context.Context, GenerateRouteCommand) (RouteGenerationResult, error) {
	return RouteGenerationResult{}, ErrDependencyUnavailable
}
func (claimRouteServiceAdapter) Accept(context.Context, AcceptRouteCommand) (RouteAcceptanceResult, error) {
	return RouteAcceptanceResult{}, ErrDependencyUnavailable
}

func decodeCapabilityClaimBody(writer http.ResponseWriter, request *http.Request, target any) bool {
	if request.Header.Get("Content-Type") != "application/json" {
		(RouteHandler{}).writeProblem(writer, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Unsupported media type", false)
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maximumCapabilityClaimBodyBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		(RouteHandler{}).validationFailed(writer, request)
		return false
	}
	return true
}

func validClaimOrigin(value string) bool {
	return value == "inferred" || value == "user_asserted" || value == "system_derived" || value == "reviewer_asserted"
}

func validClaimAction(value string) bool {
	return value == "confirm" || value == "correct" || value == "dispute" || value == "supersede" || value == "withdraw" || value == "reject"
}

func validClaimText(value string, maximum int) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) == value && value != "" && utf8.RuneCountInString(value) <= maximum
}

func capabilityClaimRevisionPath(path string) (string, bool) {
	const prefix, suffix = "/v1/capability-claims/", "/revisions"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return id, uuidPattern.MatchString(id)
}
