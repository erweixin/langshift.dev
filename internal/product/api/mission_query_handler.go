package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

type ListMissionsQuery struct {
	TenantID, UserID, Cursor string
}

type GetMissionQuery struct {
	TenantID, UserID, MissionID string
}

type MissionListResult struct {
	Items      []MissionResource `json:"items"`
	Focus      MissionFocus      `json:"focus"`
	NextCursor *string           `json:"next_cursor"`
}

type MissionQueryService interface {
	List(context.Context, ListMissionsQuery) (MissionListResult, error)
	Get(context.Context, GetMissionQuery) (MissionResource, error)
}

type CreateMissionCommand struct {
	CommandMetadata
	SourceRoleProfileID string
	TargetRoleProfileID string
	Goal                string
}

type MissionCreateService interface {
	Create(context.Context, CreateMissionCommand) (MissionMutationResult, error)
}

type MissionHandler struct {
	Queries   MissionQueryService
	Creates   MissionCreateService
	Mutations MissionMutationService
}

func (handler MissionHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/v1/missions" {
		switch request.Method {
		case http.MethodGet:
			handler.list(writer, request)
		case http.MethodPost:
			handler.create(writer, request)
		default:
			writer.Header().Set("Allow", "GET, POST")
			MissionMutationHandler{}.writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
		}
		return
	}
	missionID, focus, ok := missionMutationPath(request.URL.Path)
	if !ok {
		MissionMutationHandler{}.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	if request.Method == http.MethodGet && !focus {
		handler.get(writer, request, missionID)
		return
	}
	MissionMutationHandler{Service: handler.Mutations}.ServeHTTP(writer, request)
}

func (handler MissionHandler) create(writer http.ResponseWriter, request *http.Request) {
	if handler.Creates == nil {
		MissionMutationHandler{}.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || !claims.CSRFVerified {
		MissionMutationHandler{}.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return
	}
	keys := request.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		MissionMutationHandler{}.validationFailed(writer, request)
		return
	}
	var body struct {
		RequestID           string          `json:"request_id"`
		SourceRoleProfileID json.RawMessage `json:"source_role_profile_id"`
		TargetRoleProfileID string          `json:"target_role_profile_id"`
		Goal                string          `json:"goal"`
	}
	if !(MissionMutationHandler{}).decode(writer, request, "application/vnd.lites.mission-create.v2+json", &body) {
		return
	}
	source, present, valid := nullableUUID(body.SourceRoleProfileID)
	if !present || !valid || !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.TargetRoleProfileID) || !validMissionGoal(body.Goal) {
		MissionMutationHandler{}.validationFailed(writer, request)
		return
	}
	metadata := CommandMetadata{RequestID: claims.RequestID, ClientRequestID: body.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}
	result, err := handler.Creates.Create(request.Context(), CreateMissionCommand{CommandMetadata: metadata, SourceRoleProfileID: source, TargetRoleProfileID: body.TargetRoleProfileID, Goal: body.Goal})
	if err != nil {
		MissionMutationHandler{}.finish(writer, request, result, err)
		return
	}
	if !validMutationResult(result) {
		MissionMutationHandler{}.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.lites.mission.v2+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.Mission.Version, 10)+`"`)
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler MissionHandler) list(writer http.ResponseWriter, request *http.Request) {
	claims, ok := handler.queryClaims(writer, request)
	if !ok {
		return
	}
	values := request.URL.Query()
	if len(values) > 1 || len(values) == 1 && (!values.Has("cursor") || len(values["cursor"]) != 1 || values.Get("cursor") == "") {
		MissionMutationHandler{}.validationFailed(writer, request)
		return
	}
	result, err := handler.Queries.List(request.Context(), ListMissionsQuery{TenantID: claims.TenantID, UserID: claims.SubjectID, Cursor: request.URL.Query().Get("cursor")})
	if err != nil {
		handler.queryError(writer, request, err)
		return
	}
	if result.Items == nil {
		result.Items = []MissionResource{}
	}
	writer.Header().Set("Content-Type", "application/vnd.lites.missions.v2+json")
	writer.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler MissionHandler) get(writer http.ResponseWriter, request *http.Request, missionID string) {
	claims, ok := handler.queryClaims(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		MissionMutationHandler{}.validationFailed(writer, request)
		return
	}
	result, err := handler.Queries.Get(request.Context(), GetMissionQuery{TenantID: claims.TenantID, UserID: claims.SubjectID, MissionID: missionID})
	if err != nil {
		handler.queryError(writer, request, err)
		return
	}
	if result.ID == "" || result.Version < 1 || result.TargetRoleProfileID == "" || !validMissionStatus(result.Status) {
		MissionMutationHandler{}.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.lites.mission.v2+json")
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler MissionHandler) queryClaims(writer http.ResponseWriter, request *http.Request) (trustedcontext.Claims, bool) {
	if handler.Queries == nil {
		MissionMutationHandler{}.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return trustedcontext.Claims{}, false
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" {
		MissionMutationHandler{}.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return trustedcontext.Claims{}, false
	}
	return claims, true
}

func (handler MissionHandler) queryError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		MissionMutationHandler{}.validationFailed(writer, request)
	case errors.Is(err, ErrResourceNotFound):
		MissionMutationHandler{}.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
	default:
		MissionMutationHandler{}.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
	}
}

func validMissionGoal(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) == value && value != "" && utf8.RuneCountInString(value) <= 4000
}
