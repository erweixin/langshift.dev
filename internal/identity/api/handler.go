// Package api implements the strict public HTTP boundary for Identity Service.
// It is mounted behind serviceauth middleware; browser-controlled identity
// headers never reach these handlers.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/langshift/lites/internal/idempotency"
	identityemail "github.com/langshift/lites/internal/identity/email"
	"github.com/langshift/lites/internal/identity/password"
	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const maximumBodyBytes int64 = 64 * 1024

var (
	ErrInvalidCredentials    = errors.New("identity credential is invalid")
	ErrStateConflict         = errors.New("identity state conflict")
	ErrIdempotencyConflict   = errors.New("identity idempotency conflict")
	ErrRateLimited           = errors.New("identity request rate limited")
	ErrDependencyUnavailable = errors.New("identity dependency unavailable")
)

type RequestMetadata struct {
	RequestID       string
	ClientRequestID string
	IdempotencyKey  string
	ClientIPHash    []byte
	UserAgentHash   []byte
}

type RegisterCommand struct {
	RequestMetadata
	NormalizedEmail string
	Password        string
	Locale          string
}

type RegisterResult struct {
	UserID                     string
	EmailVerificationExpiresAt time.Time
}

type VerifyEmailCommand struct {
	RequestMetadata
	Token string
}

type VerifyEmailResult struct {
	UserID     string
	VerifiedAt time.Time
}

type LoginCommand struct {
	RequestMetadata
	NormalizedEmail string
	Password        string
}

type LoginResult struct {
	UserID       string
	SessionID    string
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
}

type Service interface {
	Register(context.Context, RegisterCommand) (RegisterResult, error)
	VerifyEmail(context.Context, VerifyEmailCommand) (VerifyEmailResult, error)
	Login(context.Context, LoginCommand) (LoginResult, error)
}

type Handler struct {
	Service Service
	Now     func() time.Time
}

func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/v1/auth/register":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request)
			return
		}
		handler.register(writer, request)
	case "/v1/auth/verify-email":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request)
			return
		}
		handler.verifyEmail(writer, request)
	case "/v1/auth/login":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request)
			return
		}
		handler.login(writer, request)
	default:
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
	}
}

func (handler Handler) register(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.publicMetadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		Email     string `json:"email"`
		Password  string `json:"password"`
		Locale    string `json:"locale"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	defer func() { body.Password = "" }()
	normalizedEmail, err := identityemail.Normalize(body.Email)
	if err != nil || password.ValidateForRegistration(body.Password) != nil || !validClientRequestID(body.RequestID) || (body.Locale != "en" && body.Locale != "zh-CN") {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.Register(request.Context(), RegisterCommand{RequestMetadata: metadata, NormalizedEmail: normalizedEmail, Password: body.Password, Locale: body.Locale})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.UserID == "" || result.EmailVerificationExpiresAt.IsZero() {
		handler.internalError(writer, request)
		return
	}
	handler.writeJSON(writer, http.StatusOK, struct {
		UserID                     string    `json:"user_id"`
		Status                     string    `json:"status"`
		EmailVerificationExpiresAt time.Time `json:"email_verification_expires_at"`
	}{UserID: result.UserID, Status: "verification_required", EmailVerificationExpiresAt: result.EmailVerificationExpiresAt.UTC()})
}

func (handler Handler) verifyEmail(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.publicMetadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		Token     string `json:"token"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	defer func() { body.Token = "" }()
	if !validClientRequestID(body.RequestID) || len(body.Token) < 32 || len(body.Token) > 512 || strings.ContainsFunc(body.Token, unicode.IsSpace) {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.VerifyEmail(request.Context(), VerifyEmailCommand{RequestMetadata: metadata, Token: body.Token})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.UserID == "" || result.VerifiedAt.IsZero() {
		handler.internalError(writer, request)
		return
	}
	handler.writeJSON(writer, http.StatusOK, struct {
		UserID     string    `json:"user_id"`
		Status     string    `json:"status"`
		VerifiedAt time.Time `json:"verified_at"`
	}{UserID: result.UserID, Status: "verified", VerifiedAt: result.VerifiedAt.UTC()})
}

func (handler Handler) login(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.publicMetadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		Email     string `json:"email"`
		Password  string `json:"password"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	defer func() { body.Password = "" }()
	normalizedEmail, err := identityemail.Normalize(body.Email)
	if err != nil || password.ValidateForAuthentication(body.Password) != nil || !validClientRequestID(body.RequestID) {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.Login(request.Context(), LoginCommand{RequestMetadata: metadata, NormalizedEmail: normalizedEmail, Password: body.Password})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.UserID == "" || result.SessionID == "" || result.ExpiresAt.IsZero() || !result.ExpiresAt.After(handler.now()) {
		handler.internalError(writer, request)
		return
	}
	sessionCookie, err := session.Cookie(result.SessionToken, result.ExpiresAt)
	if err != nil {
		handler.internalError(writer, request)
		return
	}
	csrfCookie, err := session.CSRFCookie(result.CSRFToken, result.ExpiresAt)
	if err != nil {
		handler.internalError(writer, request)
		return
	}
	http.SetCookie(writer, sessionCookie)
	http.SetCookie(writer, csrfCookie)
	handler.writeJSON(writer, http.StatusOK, struct {
		UserID    string    `json:"user_id"`
		SessionID string    `json:"session_id"`
		ExpiresAt time.Time `json:"expires_at"`
	}{UserID: result.UserID, SessionID: result.SessionID, ExpiresAt: result.ExpiresAt.UTC()})
}

func (handler Handler) publicMetadata(writer http.ResponseWriter, request *http.Request) (RequestMetadata, bool) {
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.PublicRequest || !claims.CSRFVerified {
		handler.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return RequestMetadata{}, false
	}
	if handler.Service == nil {
		handler.internalError(writer, request)
		return RequestMetadata{}, false
	}
	if idempotency.ValidateRawKey(request.Header.Get(transport.IdempotencyHeader)) != nil {
		handler.validationFailed(writer, request)
		return RequestMetadata{}, false
	}
	ipHash, ipErr := decodeHash(claims.ClientIPHash)
	userAgentHash, userAgentErr := decodeHash(claims.UserAgentHash)
	if ipErr != nil || userAgentErr != nil {
		handler.internalError(writer, request)
		return RequestMetadata{}, false
	}
	return RequestMetadata{RequestID: claims.RequestID, IdempotencyKey: request.Header.Get(transport.IdempotencyHeader), ClientIPHash: ipHash, UserAgentHash: userAgentHash}, true
}

func (handler Handler) decode(writer http.ResponseWriter, request *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		handler.writeProblem(writer, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Unsupported media type", false)
		return false
	}
	if request.ContentLength > maximumBodyBytes {
		handler.writeProblem(writer, request, http.StatusRequestEntityTooLarge, "payload_too_large", "Payload too large", false)
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maximumBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			handler.writeProblem(writer, request, http.StatusRequestEntityTooLarge, "payload_too_large", "Payload too large", false)
		} else {
			handler.validationFailed(writer, request)
		}
		return false
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		handler.validationFailed(writer, request)
		return false
	}
	return true
}

func (handler Handler) serviceError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		handler.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
	case errors.Is(err, ErrStateConflict):
		handler.writeProblem(writer, request, http.StatusConflict, "state_conflict", "State conflict", true)
	case errors.Is(err, ErrIdempotencyConflict):
		handler.writeProblem(writer, request, http.StatusConflict, "idempotency_conflict", "Idempotency conflict", false)
	case errors.Is(err, ErrRateLimited):
		handler.writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Rate limited", true)
	case errors.Is(err, ErrDependencyUnavailable):
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
	default:
		handler.internalError(writer, request)
	}
}

func (handler Handler) methodNotAllowed(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Allow", http.MethodPost)
	handler.writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
}

func (handler Handler) validationFailed(writer http.ResponseWriter, request *http.Request) {
	handler.writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Validation failed", false)
}

func (handler Handler) internalError(writer http.ResponseWriter, request *http.Request) {
	handler.writeProblem(writer, request, http.StatusInternalServerError, "internal_error", "Internal error", true)
}

func (handler Handler) writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, title string, retryable bool) {
	problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/" + code, Title: title, Status: status, Code: code, RequestID: request.Header.Get(transport.RequestIDHeader), Retryable: retryable})
}

func (handler Handler) writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func validClientRequestID(value string) bool {
	return len(value) >= 8 && len(value) <= 200 && !strings.ContainsFunc(value, unicode.IsControl)
}

func decodeHash(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("trusted fingerprint is invalid")
	}
	return decoded, nil
}

func (handler Handler) now() time.Time {
	if handler.Now != nil {
		return handler.Now().UTC()
	}
	return time.Now().UTC()
}
