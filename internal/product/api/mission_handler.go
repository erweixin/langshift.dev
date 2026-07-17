// Package api exposes the authenticated Product Service HTTP boundary.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/product/mission"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const maximumMissionBodyBytes int64 = 64 * 1024

var (
	ErrValidation            = errors.New("product request validation failed")
	ErrPermissionDenied      = errors.New("product permission denied")
	ErrResourceNotFound      = errors.New("product resource not found")
	ErrStateConflict         = errors.New("product state conflict")
	ErrIdempotencyConflict   = errors.New("product idempotency conflict")
	ErrDependencyUnavailable = errors.New("product dependency unavailable")
	uuidPattern              = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

type CommandMetadata struct {
	RequestID, ClientRequestID, IdempotencyKey string
	TenantID, UserID, SessionID                string
}

type ChangeMissionStatusCommand struct {
	CommandMetadata
	MissionID              string
	Status                 mission.Status
	ReplacementMissionID   string
	ExpectedMissionVersion uint64
	ExpectedFocusVersion   uint64
}

type SetMissionFocusCommand struct {
	CommandMetadata
	MissionID            string
	ExpectedFocusVersion uint64
}

type MissionResource struct {
	ID                     string         `json:"id"`
	Version                uint64         `json:"version"`
	Status                 mission.Status `json:"status"`
	SourceRoleProfileID    *string        `json:"source_role_profile_id"`
	TargetRoleProfileID    string         `json:"target_role_profile_id"`
	CurrentRouteRevisionID *string        `json:"current_route_revision_id"`
	Focused                bool           `json:"focused"`
	CreatedAt              time.Time      `json:"created_at"`
	UpdatedAt              time.Time      `json:"updated_at"`
}

type MissionFocus struct {
	MissionID *string `json:"mission_id"`
	Version   uint64  `json:"version"`
}

type MissionMutationResult struct {
	Mission  MissionResource `json:"mission"`
	Focus    MissionFocus    `json:"focus"`
	EventIDs []string        `json:"event_ids"`
	Replayed bool            `json:"replayed"`
}

type MissionMutationService interface {
	ChangeStatus(context.Context, ChangeMissionStatusCommand) (MissionMutationResult, error)
	SetFocus(context.Context, SetMissionFocusCommand) (MissionMutationResult, error)
}

type MissionMutationHandler struct{ Service MissionMutationService }

func (handler MissionMutationHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	missionID, focus, ok := missionMutationPath(request.URL.Path)
	if !ok {
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	if focus {
		if request.Method != http.MethodPut {
			handler.methodNotAllowed(writer, request, http.MethodPut)
			return
		}
		handler.setFocus(writer, request, missionID)
		return
	}
	if request.Method != http.MethodPatch {
		handler.methodNotAllowed(writer, request, http.MethodPatch)
		return
	}
	handler.changeStatus(writer, request, missionID)
}

func (handler MissionMutationHandler) changeStatus(writer http.ResponseWriter, request *http.Request, missionID string) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID              string          `json:"request_id"`
		Status                 mission.Status  `json:"status"`
		ReplacementMissionID   json.RawMessage `json:"replacement_mission_id"`
		ExpectedMissionVersion uint64          `json:"expected_mission_version"`
		ExpectedFocusVersion   uint64          `json:"expected_focus_version"`
	}
	if !handler.decode(writer, request, "application/vnd.lites.mission-status.v2+json", &body) {
		return
	}
	replacement, present, valid := nullableUUID(body.ReplacementMissionID)
	if !present || !valid || !validClientRequestID(body.RequestID) || !validMissionStatus(body.Status) || body.ExpectedMissionVersion < 1 {
		handler.validationFailed(writer, request)
		return
	}
	if expected, valid := ifMatch(request); !valid || expected != body.ExpectedMissionVersion {
		handler.writeProblem(writer, request, http.StatusConflict, "version_conflict", "Version conflict", true)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.ChangeStatus(request.Context(), ChangeMissionStatusCommand{CommandMetadata: metadata, MissionID: missionID, Status: body.Status, ReplacementMissionID: replacement, ExpectedMissionVersion: body.ExpectedMissionVersion, ExpectedFocusVersion: body.ExpectedFocusVersion})
	handler.finish(writer, request, result, err)
}

func (handler MissionMutationHandler) setFocus(writer http.ResponseWriter, request *http.Request, missionID string) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID            string          `json:"request_id"`
		ExpectedFocusVersion uint64          `json:"expected_focus_version"`
		ReplacementMissionID json.RawMessage `json:"replacement_mission_id"`
	}
	if !handler.decode(writer, request, "application/vnd.lites.mission-focus.v2+json", &body) {
		return
	}
	_, present, valid := nullableUUID(body.ReplacementMissionID)
	if !present || !valid || !bytes.Equal(bytes.TrimSpace(body.ReplacementMissionID), []byte("null")) || !validClientRequestID(body.RequestID) {
		handler.validationFailed(writer, request)
		return
	}
	if expected, valid := ifMatch(request); !valid || expected != body.ExpectedFocusVersion {
		handler.writeProblem(writer, request, http.StatusConflict, "version_conflict", "Version conflict", true)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.SetFocus(request.Context(), SetMissionFocusCommand{CommandMetadata: metadata, MissionID: missionID, ExpectedFocusVersion: body.ExpectedFocusVersion})
	handler.finish(writer, request, result, err)
}

func (handler MissionMutationHandler) metadata(writer http.ResponseWriter, request *http.Request) (CommandMetadata, bool) {
	if handler.Service == nil {
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return CommandMetadata{}, false
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || !claims.CSRFVerified {
		handler.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return CommandMetadata{}, false
	}
	keys := request.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		handler.validationFailed(writer, request)
		return CommandMetadata{}, false
	}
	return CommandMetadata{RequestID: claims.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, true
}

func (handler MissionMutationHandler) decode(writer http.ResponseWriter, request *http.Request, expectedType string, target any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != expectedType {
		handler.writeProblem(writer, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Unsupported media type", false)
		return false
	}
	if request.ContentLength > maximumMissionBodyBytes {
		handler.writeProblem(writer, request, http.StatusRequestEntityTooLarge, "payload_too_large", "Payload too large", false)
		return false
	}
	reader := http.MaxBytesReader(writer, request.Body, maximumMissionBodyBytes)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		handler.validationFailed(writer, request)
		return false
	}
	return true
}

func (handler MissionMutationHandler) finish(writer http.ResponseWriter, request *http.Request, result MissionMutationResult, err error) {
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation):
			handler.validationFailed(writer, request)
		case errors.Is(err, ErrPermissionDenied):
			handler.writeProblem(writer, request, http.StatusForbidden, "permission_denied", "Permission denied", false)
		case errors.Is(err, ErrResourceNotFound):
			handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		case errors.Is(err, ErrStateConflict), errors.Is(err, ErrIdempotencyConflict):
			handler.writeProblem(writer, request, http.StatusConflict, "state_conflict", "State conflict", true)
		default:
			handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		}
		return
	}
	if !validMutationResult(result) {
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.lites.mission.v2+json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.Focus.Version, 10)+`"`)
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler MissionMutationHandler) validationFailed(writer http.ResponseWriter, request *http.Request) {
	handler.writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Validation failed", false)
}

func (handler MissionMutationHandler) methodNotAllowed(writer http.ResponseWriter, request *http.Request, method string) {
	writer.Header().Set("Allow", method)
	handler.writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
}

func (MissionMutationHandler) writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, title string, retryable bool) {
	requestID := request.Header.Get(transport.RequestIDHeader)
	if claims, ok := serviceauth.ClaimsFromContext(request.Context()); ok && claims.RequestID != "" {
		requestID = claims.RequestID
	}
	problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/" + code, Title: title, Status: status, Code: code, RequestID: requestID, Retryable: retryable})
}

func missionMutationPath(path string) (string, bool, bool) {
	const prefix = "/v1/missions/"
	if !strings.HasPrefix(path, prefix) {
		return "", false, false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) == 1 && uuidPattern.MatchString(parts[0]) {
		return parts[0], false, true
	}
	if len(parts) == 2 && uuidPattern.MatchString(parts[0]) && parts[1] == "focus" {
		return parts[0], true, true
	}
	return "", false, false
}

func nullableUUID(raw json.RawMessage) (string, bool, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", false, false
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return "", true, true
	}
	var value string
	if json.Unmarshal(trimmed, &value) != nil || !uuidPattern.MatchString(value) {
		return "", true, false
	}
	return value, true, true
}

func ifMatch(request *http.Request) (uint64, bool) {
	values := request.Header.Values("If-Match")
	if len(values) != 1 || len(values[0]) < 3 || values[0][0] != '"' || values[0][len(values[0])-1] != '"' {
		return 0, false
	}
	value, err := strconv.ParseUint(values[0][1:len(values[0])-1], 10, 64)
	return value, err == nil
}

func validClientRequestID(value string) bool {
	return utf8.ValidString(value) && len(value) >= 8 && len(value) <= 256 && strings.TrimSpace(value) == value
}

func validMissionStatus(value mission.Status) bool {
	return value == mission.Draft || value == mission.Active || value == mission.Paused || value == mission.Completed || value == mission.Archived
}

func validMutationResult(result MissionMutationResult) bool {
	validFocus := result.Focus.MissionID == nil || uuidPattern.MatchString(*result.Focus.MissionID)
	return uuidPattern.MatchString(result.Mission.ID) && result.Mission.Version > 0 && validMissionStatus(result.Mission.Status) && uuidPattern.MatchString(result.Mission.TargetRoleProfileID) && !result.Mission.CreatedAt.IsZero() && !result.Mission.UpdatedAt.IsZero() && validFocus && len(result.EventIDs) <= 2
}
