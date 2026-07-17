package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

var (
	ErrRouteResultStale = errors.New("route result is stale")
	digestPattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type RouteRevisionResource struct {
	ID                     string          `json:"id"`
	Version                uint64          `json:"version"`
	MissionID              string          `json:"mission_id"`
	RouteVersion           uint64          `json:"route_version"`
	BaseRouteVersion       uint64          `json:"base_route_version"`
	Status                 string          `json:"status"`
	ClaimSetHash           string          `json:"claim_set_hash"`
	InputManifest          json.RawMessage `json:"input_manifest"`
	Route                  json.RawMessage `json:"route"`
	AgentProfileSnapshotID string          `json:"agent_profile_snapshot_id"`
	OntologySnapshotID     string          `json:"ontology_snapshot_id"`
	ContentSnapshotID      string          `json:"content_snapshot_id"`
	PlannerCommandID       *string         `json:"planner_command_id"`
	PlannerRunID           *string         `json:"planner_run_id"`
	AcceptedAt             *time.Time      `json:"accepted_at"`
	StaleReason            *string         `json:"stale_reason"`
	FailureReason          *string         `json:"failure_reason"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
}

type RouteListQuery struct{ TenantID, UserID, MissionID, Cursor string }

type RouteListResult struct {
	Items      []RouteRevisionResource `json:"items"`
	NextCursor *string                 `json:"next_cursor"`
}

type GenerateRouteCommand struct {
	CommandMetadata
	MissionID            string
	ExpectedRouteVersion uint64
	ExpectedClaimSetHash string
}

type AcceptRouteCommand struct {
	CommandMetadata
	RouteRevisionID         string
	ExpectedRevisionVersion uint64
	ExpectedRouteVersion    uint64
	ExpectedClaimSetHash    string
}

type RouteGenerationResult struct {
	RouteRevisionID  string `json:"route_revision_id"`
	RevisionVersion  uint64 `json:"revision_version"`
	Status           string `json:"status"`
	PlannerCommandID string `json:"planner_command_id"`
	EventID          string `json:"event_id"`
	Replayed         bool   `json:"replayed"`
}

type RouteAcceptanceResult struct {
	RouteRevisionID        string `json:"route_revision_id"`
	RevisionVersion        uint64 `json:"revision_version"`
	MissionID              string `json:"mission_id"`
	MissionVersion         uint64 `json:"mission_version"`
	RouteVersion           uint64 `json:"route_version"`
	CurrentRouteRevisionID string `json:"current_route_revision_id"`
	Status                 string `json:"status"`
	EventID                string `json:"event_id"`
	Replayed               bool   `json:"replayed"`
}

type RouteService interface {
	List(context.Context, RouteListQuery) (RouteListResult, error)
	Generate(context.Context, GenerateRouteCommand) (RouteGenerationResult, error)
	Accept(context.Context, AcceptRouteCommand) (RouteAcceptanceResult, error)
}

type RouteHandler struct{ Service RouteService }

func (handler RouteHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/v1/route-revisions" {
		switch request.Method {
		case http.MethodGet:
			handler.list(writer, request)
		case http.MethodPost:
			handler.generate(writer, request)
		default:
			writer.Header().Set("Allow", "GET, POST")
			handler.writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
		}
		return
	}
	revisionID, ok := routeAcceptPath(request.URL.Path)
	if !ok {
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		handler.writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
		return
	}
	handler.accept(writer, request, revisionID)
}

func (handler RouteHandler) list(writer http.ResponseWriter, request *http.Request) {
	claims, ok := handler.claims(writer, request, false)
	if !ok {
		return
	}
	values := request.URL.Query()
	for name, entries := range values {
		if name != "mission_id" && name != "cursor" || len(entries) != 1 || entries[0] == "" {
			handler.validationFailed(writer, request)
			return
		}
	}
	missionID := values.Get("mission_id")
	if len(values) < 1 || !uuidPattern.MatchString(missionID) {
		handler.validationFailed(writer, request)
		return
	}
	result, err := handler.Service.List(request.Context(), RouteListQuery{TenantID: claims.TenantID, UserID: claims.SubjectID, MissionID: missionID, Cursor: values.Get("cursor")})
	if err != nil {
		handler.finishError(writer, request, err)
		return
	}
	if result.Items == nil {
		result.Items = []RouteRevisionResource{}
	}
	writer.Header().Set("Content-Type", "application/vnd.lites.route-revisions.v2+json")
	writer.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler RouteHandler) generate(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID            string `json:"request_id"`
		MissionID            string `json:"mission_id"`
		ExpectedRouteVersion uint64 `json:"expected_route_version"`
		ExpectedClaimSetHash string `json:"expected_claim_set_hash"`
	}
	if !(MissionMutationHandler{}).decode(writer, request, "application/vnd.lites.route-generate.v2+json", &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.MissionID) || !digestPattern.MatchString(body.ExpectedClaimSetHash) {
		handler.validationFailed(writer, request)
		return
	}
	if expected, valid := ifMatch(request); !valid || expected != body.ExpectedRouteVersion {
		handler.writeProblem(writer, request, http.StatusConflict, "version_conflict", "Version conflict", true)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.Generate(request.Context(), GenerateRouteCommand{CommandMetadata: metadata, MissionID: body.MissionID, ExpectedRouteVersion: body.ExpectedRouteVersion, ExpectedClaimSetHash: body.ExpectedClaimSetHash})
	if err != nil {
		handler.finishError(writer, request, err)
		return
	}
	if !validGenerationResult(result) {
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.lites.route-generation.v2+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.RevisionVersion, 10)+`"`)
	writer.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler RouteHandler) accept(writer http.ResponseWriter, request *http.Request, revisionID string) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID               string `json:"request_id"`
		ExpectedRevisionVersion uint64 `json:"expected_revision_version"`
		ExpectedRouteVersion    uint64 `json:"expected_route_version"`
		ExpectedClaimSetHash    string `json:"expected_claim_set_hash"`
	}
	if !(MissionMutationHandler{}).decode(writer, request, "application/vnd.lites.route-accept.v2+json", &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || body.ExpectedRevisionVersion < 1 || !digestPattern.MatchString(body.ExpectedClaimSetHash) {
		handler.validationFailed(writer, request)
		return
	}
	if expected, valid := ifMatch(request); !valid || expected != body.ExpectedRevisionVersion {
		handler.writeProblem(writer, request, http.StatusConflict, "version_conflict", "Version conflict", true)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.Accept(request.Context(), AcceptRouteCommand{CommandMetadata: metadata, RouteRevisionID: revisionID, ExpectedRevisionVersion: body.ExpectedRevisionVersion, ExpectedRouteVersion: body.ExpectedRouteVersion, ExpectedClaimSetHash: body.ExpectedClaimSetHash})
	if err != nil {
		handler.finishError(writer, request, err)
		return
	}
	if !validAcceptanceResult(result) {
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.lites.route-acceptance.v2+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.RevisionVersion, 10)+`"`)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler RouteHandler) metadata(writer http.ResponseWriter, request *http.Request) (CommandMetadata, bool) {
	claims, ok := handler.claims(writer, request, true)
	if !ok {
		return CommandMetadata{}, false
	}
	keys := request.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		handler.validationFailed(writer, request)
		return CommandMetadata{}, false
	}
	return CommandMetadata{RequestID: claims.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, true
}

func (handler RouteHandler) claims(writer http.ResponseWriter, request *http.Request, csrf bool) (trustedcontext.Claims, bool) {
	if handler.Service == nil {
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return trustedcontext.Claims{}, false
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || csrf && !claims.CSRFVerified {
		handler.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return trustedcontext.Claims{}, false
	}
	return claims, true
}

func (handler RouteHandler) finishError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		handler.validationFailed(writer, request)
	case errors.Is(err, ErrResourceNotFound):
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
	case errors.Is(err, ErrRouteResultStale):
		handler.writeProblem(writer, request, http.StatusConflict, "route_result_stale", "Route result stale", false)
	case errors.Is(err, ErrStateConflict), errors.Is(err, ErrIdempotencyConflict):
		handler.writeProblem(writer, request, http.StatusConflict, "state_conflict", "State conflict", true)
	default:
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
	}
}

func (handler RouteHandler) validationFailed(writer http.ResponseWriter, request *http.Request) {
	handler.writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Validation failed", false)
}

func (RouteHandler) writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, title string, retryable bool) {
	requestID := request.Header.Get(transport.RequestIDHeader)
	if claims, ok := serviceauth.ClaimsFromContext(request.Context()); ok && claims.RequestID != "" {
		requestID = claims.RequestID
	}
	problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/" + code, Title: title, Status: status, Code: code, RequestID: requestID, Retryable: retryable})
}

func routeAcceptPath(path string) (string, bool) {
	const prefix, suffix = "/v1/route-revisions/", "/accept"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return id, uuidPattern.MatchString(id)
}

func validGenerationResult(result RouteGenerationResult) bool {
	return uuidPattern.MatchString(result.RouteRevisionID) && result.RevisionVersion > 0 && result.Status == "generating" && uuidPattern.MatchString(result.PlannerCommandID) && uuidPattern.MatchString(result.EventID)
}

func validAcceptanceResult(result RouteAcceptanceResult) bool {
	return uuidPattern.MatchString(result.RouteRevisionID) && result.RevisionVersion > 0 && uuidPattern.MatchString(result.MissionID) && result.MissionVersion > 0 && result.RouteVersion > 0 && result.CurrentRouteRevisionID == result.RouteRevisionID && result.Status == "accepted" && uuidPattern.MatchString(result.EventID)
}
