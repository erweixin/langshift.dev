// Package api exposes the authenticated behavior release control plane and
// the separate mTLS-only automatic rollback boundary.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/langshift/lites/internal/behavior"
	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const maximumBehaviorBodyBytes int64 = 2 * 1024 * 1024

var (
	ErrValidation            = errors.New("behavior request validation failed")
	ErrPermissionDenied      = errors.New("behavior permission denied")
	ErrReauthentication      = errors.New("behavior reauthentication required")
	ErrResourceNotFound      = errors.New("behavior resource not found")
	ErrStateConflict         = errors.New("behavior state conflict")
	ErrIdempotencyConflict   = errors.New("behavior idempotency conflict")
	ErrDependencyUnavailable = errors.New("behavior dependency unavailable")
	errUnsupportedMediaType  = errors.New("unsupported media type")
	errPayloadTooLarge       = errors.New("payload too large")
	errMalformedPayload      = errors.New("malformed payload")
)

type CommandMetadata struct {
	RequestID, ClientRequestID, IdempotencyKey string
	TenantID, UserID, SessionID                string
}

type CreateSnapshotCommand struct {
	CommandMetadata
	Manifest behavior.Manifest
}

type RecordEvaluationCommand struct {
	CommandMetadata
	Report behavior.EvaluationReport
}

type PromoteCommand struct {
	CommandMetadata
	Request behavior.PromotionRequest
}

type AutomaticRollbackCommand struct {
	RequestID, ClientRequestID, IdempotencyKey string
	Request                                    behavior.RollbackRequest
}

type MutationResult struct {
	ID         string `json:"id"`
	ResourceID string `json:"resource_id"`
	EventID    string `json:"event_id"`
	Hash       string `json:"hash"`
	Sequence   uint64 `json:"sequence,omitempty"`
	Replayed   bool   `json:"replayed"`
}

type ControlService interface {
	CreateSnapshot(context.Context, CreateSnapshotCommand) (MutationResult, error)
	RecordEvaluation(context.Context, RecordEvaluationCommand) (MutationResult, error)
	Promote(context.Context, PromoteCommand) (MutationResult, error)
	Current(context.Context, string, behavior.Profile, string) (behavior.ChannelBinding, error)
}

type RollbackService interface {
	AutomaticRollback(context.Context, AutomaticRollbackCommand) (MutationResult, error)
}

type Handler struct{ Service ControlService }

func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch {
	case request.URL.Path == "/v1/admin/behavior/snapshots":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.createSnapshot(writer, request)
	case request.URL.Path == "/v1/admin/behavior/evaluations":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.recordEvaluation(writer, request)
	case request.URL.Path == "/v1/admin/behavior/promotions":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.promote(writer, request)
	default:
		profile, environment, ok := channelPath(request.URL.Path)
		if !ok {
			handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
			return
		}
		if request.Method != http.MethodGet {
			handler.methodNotAllowed(writer, request, http.MethodGet)
			return
		}
		handler.current(writer, request, profile, environment)
	}
}

func (handler Handler) createSnapshot(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.metadata(writer, request, true)
	if !ok {
		return
	}
	var body struct {
		RequestID string            `json:"request_id"`
		Manifest  behavior.Manifest `json:"manifest"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) {
		handler.validationFailed(writer, request)
		return
	}
	result, err := handler.Service.CreateSnapshot(request.Context(), CreateSnapshotCommand{CommandMetadata: metadata.command(body.RequestID), Manifest: body.Manifest})
	handler.finishMutation(writer, request, result, err)
}

func (handler Handler) recordEvaluation(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.metadata(writer, request, true)
	if !ok {
		return
	}
	var body struct {
		RequestID string                    `json:"request_id"`
		Report    behavior.EvaluationReport `json:"report"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) {
		handler.validationFailed(writer, request)
		return
	}
	result, err := handler.Service.RecordEvaluation(request.Context(), RecordEvaluationCommand{CommandMetadata: metadata.command(body.RequestID), Report: body.Report})
	handler.finishMutation(writer, request, result, err)
}

func (handler Handler) promote(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.metadata(writer, request, true)
	if !ok {
		return
	}
	var body struct {
		RequestID string                    `json:"request_id"`
		Promotion behavior.PromotionRequest `json:"promotion"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) {
		handler.validationFailed(writer, request)
		return
	}
	result, err := handler.Service.Promote(request.Context(), PromoteCommand{CommandMetadata: metadata.command(body.RequestID), Request: body.Promotion})
	handler.finishMutation(writer, request, result, err)
}

func (handler Handler) current(writer http.ResponseWriter, request *http.Request, profile behavior.Profile, environment string) {
	metadata, ok := handler.metadata(writer, request, false)
	if !ok {
		return
	}
	result, err := handler.Service.Current(request.Context(), metadata.tenantID, profile, environment)
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(writer).Encode(result)
}

type requestMetadata struct{ requestID, idempotencyKey, tenantID, userID, sessionID string }

func (metadata requestMetadata) command(clientRequestID string) CommandMetadata {
	return CommandMetadata{RequestID: metadata.requestID, ClientRequestID: clientRequestID, IdempotencyKey: metadata.idempotencyKey, TenantID: metadata.tenantID, UserID: metadata.userID, SessionID: metadata.sessionID}
}

func (handler Handler) metadata(writer http.ResponseWriter, request *http.Request, mutation bool) (requestMetadata, bool) {
	if handler.Service == nil {
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return requestMetadata{}, false
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || mutation && !claims.CSRFVerified {
		handler.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return requestMetadata{}, false
	}
	if !hasAdminRole(claims.Roles) {
		handler.writeProblem(writer, request, http.StatusForbidden, "permission_denied", "Permission denied", false)
		return requestMetadata{}, false
	}
	metadata := requestMetadata{requestID: claims.RequestID, tenantID: claims.TenantID, userID: claims.SubjectID, sessionID: claims.SessionID}
	if mutation {
		values := request.Header.Values(transport.IdempotencyHeader)
		if len(values) != 1 || idempotency.ValidateRawKey(values[0]) != nil {
			handler.validationFailed(writer, request)
			return requestMetadata{}, false
		}
		metadata.idempotencyKey = values[0]
	}
	return metadata, true
}

type RollbackHandler struct {
	Service                          RollbackService
	RequireVerifiedClientCertificate bool
	AllowedClientSPIFFEID            string
}

func (handler RollbackHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/internal/v1/behavior/rollbacks" {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if handler.Service == nil || handler.RequireVerifiedClientCertificate && handler.AllowedClientSPIFFEID == "" {
		http.Error(writer, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	if handler.RequireVerifiedClientCertificate && !verifiedSPIFFEClient(request, handler.AllowedClientSPIFFEID) {
		http.Error(writer, "client certificate required", http.StatusUnauthorized)
		return
	}
	values := request.Header.Values(transport.IdempotencyHeader)
	if len(values) != 1 || idempotency.ValidateRawKey(values[0]) != nil {
		http.Error(writer, "invalid idempotency key", http.StatusBadRequest)
		return
	}
	var body struct {
		RequestID string                   `json:"request_id"`
		Rollback  behavior.RollbackRequest `json:"rollback"`
	}
	if err := decodeStrict(writer, request, &body); err != nil || !validClientRequestID(body.RequestID) {
		status := http.StatusBadRequest
		if errors.Is(err, errUnsupportedMediaType) {
			status = http.StatusUnsupportedMediaType
		} else if errors.Is(err, errPayloadTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(writer, http.StatusText(status), status)
		return
	}
	result, err := handler.Service.AutomaticRollback(request.Context(), AutomaticRollbackCommand{RequestID: request.Header.Get(transport.RequestIDHeader), ClientRequestID: body.RequestID, IdempotencyKey: values[0], Request: body.Rollback})
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, ErrValidation) {
			status = http.StatusBadRequest
		} else if errors.Is(err, ErrIdempotencyConflict) || errors.Is(err, ErrStateConflict) {
			status = http.StatusConflict
		} else if errors.Is(err, ErrResourceNotFound) {
			status = http.StatusNotFound
		}
		http.Error(writer, http.StatusText(status), status)
		return
	}
	writeMutation(writer, result)
}

func (handler Handler) decode(writer http.ResponseWriter, request *http.Request, target any) bool {
	err := decodeStrict(writer, request, target)
	if errors.Is(err, errUnsupportedMediaType) {
		handler.writeProblem(writer, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Unsupported media type", false)
		return false
	}
	if errors.Is(err, errPayloadTooLarge) {
		handler.writeProblem(writer, request, http.StatusRequestEntityTooLarge, "payload_too_large", "Payload too large", false)
		return false
	}
	if err != nil {
		handler.validationFailed(writer, request)
		return false
	}
	return true
}

func decodeStrict(writer http.ResponseWriter, request *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errUnsupportedMediaType
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maximumBehaviorBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			return errPayloadTooLarge
		}
		return errMalformedPayload
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errMalformedPayload
	}
	return nil
}

func (handler Handler) finishMutation(writer http.ResponseWriter, request *http.Request, result MutationResult, err error) {
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.ID == "" || result.ResourceID == "" || result.EventID == "" || result.Hash == "" {
		handler.serviceError(writer, request, ErrDependencyUnavailable)
		return
	}
	writeMutation(writer, result)
}

func writeMutation(writer http.ResponseWriter, result MutationResult) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler Handler) serviceError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		handler.validationFailed(writer, request)
	case errors.Is(err, ErrPermissionDenied):
		handler.writeProblem(writer, request, http.StatusForbidden, "permission_denied", "Permission denied", false)
	case errors.Is(err, ErrReauthentication):
		handler.writeProblem(writer, request, http.StatusUnauthorized, "reauthentication_required", "Reauthentication required", false)
	case errors.Is(err, ErrResourceNotFound):
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
	case errors.Is(err, ErrIdempotencyConflict):
		handler.writeProblem(writer, request, http.StatusConflict, "idempotency_conflict", "Idempotency conflict", false)
	case errors.Is(err, ErrStateConflict):
		handler.writeProblem(writer, request, http.StatusConflict, "state_conflict", "State conflict", true)
	default:
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
	}
}

func (handler Handler) validationFailed(writer http.ResponseWriter, request *http.Request) {
	handler.writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Validation failed", false)
}

func (handler Handler) methodNotAllowed(writer http.ResponseWriter, request *http.Request, method string) {
	writer.Header().Set("Allow", method)
	handler.writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
}

func (handler Handler) writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, title string, retryable bool) {
	requestID := request.Header.Get(transport.RequestIDHeader)
	if claims, ok := serviceauth.ClaimsFromContext(request.Context()); ok && claims.RequestID != "" {
		requestID = claims.RequestID
	}
	problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/" + code, Title: title, Status: status, Code: code, RequestID: requestID, Retryable: retryable})
}

func channelPath(path string) (behavior.Profile, string, bool) {
	const prefix = "/v1/admin/behavior/channels/"
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if !strings.HasPrefix(path, prefix) || len(parts) != 2 {
		return "", "", false
	}
	profile, environment := behavior.Profile(parts[0]), parts[1]
	return profile, environment, profile.Valid() && (environment == "staging" || environment == "production")
}

func validClientRequestID(value string) bool {
	return len(value) >= 8 && len(value) <= 256 && !strings.ContainsAny(value, "\r\n\x00")
}

func hasAdminRole(roles []string) bool {
	for _, role := range roles {
		if role == "owner" || role == "admin" {
			return true
		}
	}
	return false
}

func verifiedSPIFFEClient(request *http.Request, expected string) bool {
	if request.TLS == nil || len(request.TLS.VerifiedChains) == 0 || len(request.TLS.VerifiedChains[0]) == 0 {
		return false
	}
	for _, identity := range request.TLS.VerifiedChains[0][0].URIs {
		if identity != nil && identity.String() == expected {
			return true
		}
	}
	return false
}
