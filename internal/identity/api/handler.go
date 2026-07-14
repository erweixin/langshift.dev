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
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/langshift/lites/internal/idempotency"
	identityemail "github.com/langshift/lites/internal/identity/email"
	"github.com/langshift/lites/internal/identity/password"
	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/platform/problem"
	platformratelimit "github.com/langshift/lites/internal/platform/ratelimit"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const maximumBodyBytes int64 = 64 * 1024

var (
	ErrInvalidCredentials       = errors.New("identity credential is invalid")
	ErrPermissionDenied         = errors.New("identity permission denied")
	ErrStateConflict            = errors.New("identity state conflict")
	ErrIdempotencyConflict      = errors.New("identity idempotency conflict")
	ErrRateLimited              = errors.New("identity request rate limited")
	ErrDependencyUnavailable    = errors.New("identity dependency unavailable")
	ErrResourceNotFound         = errors.New("identity resource not found")
	ErrVersionConflict          = errors.New("identity version conflict")
	ErrValidation               = errors.New("identity request validation failed")
	ErrReauthenticationRequired = errors.New("identity reauthentication required")
	ErrClaimManualReview        = errors.New("anonymous claim requires manual review")
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

type RequestLimiter interface {
	Allow(context.Context, platformratelimit.Request) (platformratelimit.Decision, error)
}

type Handler struct {
	Service     Service
	Sessions    SessionService
	Passwords   PasswordService
	Emails      EmailService
	Accounts    AccountService
	Invitations InvitationService
	Memberships MembershipService
	Onboarding  OnboardingService
	Claims      OnboardingClaimService
	RateLimiter RequestLimiter
	// RateLimitPepper is purpose-separated from database, token, and password
	// peppers. It ensures Valkey keys never contain public or authenticated PII.
	RateLimitPepper  []byte
	AnonymousCSRFKey []byte
	Now              func() time.Time
}

var (
	registerIPLimit       = platformratelimit.Limit{Capacity: 20, Window: time.Hour}
	registerSubjectLimit  = platformratelimit.Limit{Capacity: 5, Window: time.Hour}
	loginIPLimit          = platformratelimit.Limit{Capacity: 60, Window: 15 * time.Minute}
	loginSubjectLimit     = platformratelimit.Limit{Capacity: 10, Window: 15 * time.Minute}
	passwordMailIPLimit   = platformratelimit.Limit{Capacity: 10, Window: time.Hour}
	passwordMailUserLimit = platformratelimit.Limit{Capacity: 3, Window: time.Hour}
	tokenAttemptLimit     = platformratelimit.Limit{Capacity: 5, Window: time.Hour}
	mailActorLimit        = platformratelimit.Limit{Capacity: 100, Window: time.Hour}
	mailTargetLimit       = platformratelimit.Limit{Capacity: 5, Window: 24 * time.Hour}
	onboardingIPLimit     = platformratelimit.Limit{Capacity: 60, Window: time.Hour}
	onboardingActorLimit  = platformratelimit.Limit{Capacity: 120, Window: time.Hour}
)

func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/v1/auth/register":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.register(writer, request)
	case "/v1/auth/verify-email":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.verifyEmail(writer, request)
	case "/v1/auth/login":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.login(writer, request)
	case "/v1/auth/password/forgot":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.passwordForgot(writer, request)
	case "/v1/auth/password/reset":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.passwordReset(writer, request)
	case "/v1/auth/password/change":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.passwordChange(writer, request)
	case "/v1/auth/email/change":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.emailChange(writer, request)
	case "/v1/auth/email/confirm-change":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.emailConfirmChange(writer, request)
	case "/v1/auth/logout":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.logout(writer, request)
	case "/v1/account/export-requests":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.accountExportCreate(writer, request)
	case "/v1/account/erasure-requests":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.accountErasureCreate(writer, request)
	case "/v1/invitations":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.invitationCreate(writer, request)
	case "/v1/invitation-imports":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.invitationImport(writer, request)
	case "/v1/memberships":
		if request.Method != http.MethodGet {
			handler.methodNotAllowed(writer, request, http.MethodGet)
			return
		}
		handler.membershipList(writer, request)
	case "/v1/membership-imports":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.membershipImport(writer, request)
	case "/v1/onboarding-sessions":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.onboardingCreate(writer, request)
	case "/v1/auth/sessions":
		switch request.Method {
		case http.MethodGet:
			handler.listSessions(writer, request)
		case http.MethodDelete:
			handler.revokeOtherSessions(writer, request)
		default:
			handler.methodNotAllowed(writer, request, http.MethodGet, http.MethodDelete)
		}
	default:
		if strings.HasPrefix(request.URL.Path, "/v1/onboarding-sessions/") && strings.HasSuffix(request.URL.Path, "/claim") {
			if request.Method != http.MethodPost {
				handler.methodNotAllowed(writer, request, http.MethodPost)
				return
			}
			handler.onboardingClaim(writer, request)
			return
		}
		if strings.HasPrefix(request.URL.Path, "/v1/invitations/") && strings.HasSuffix(request.URL.Path, "/reject") {
			if request.Method != http.MethodPost {
				handler.methodNotAllowed(writer, request, http.MethodPost)
				return
			}
			handler.invitationReject(writer, request)
			return
		}
		if strings.HasPrefix(request.URL.Path, "/v1/memberships/") {
			if request.Method != http.MethodDelete {
				handler.methodNotAllowed(writer, request, http.MethodDelete)
				return
			}
			handler.membershipDeactivate(writer, request)
			return
		}
		if strings.HasPrefix(request.URL.Path, "/v1/invitations/") && strings.HasSuffix(request.URL.Path, "/accept") {
			if request.Method != http.MethodPost {
				handler.methodNotAllowed(writer, request, http.MethodPost)
				return
			}
			handler.invitationAccept(writer, request)
			return
		}
		if strings.HasPrefix(request.URL.Path, "/v1/invitations/") {
			if request.Method != http.MethodDelete {
				handler.methodNotAllowed(writer, request, http.MethodDelete)
				return
			}
			handler.invitationRevoke(writer, request)
			return
		}
		if strings.HasPrefix(request.URL.Path, "/v1/account/erasure-requests/") {
			if request.Method != http.MethodDelete {
				handler.methodNotAllowed(writer, request, http.MethodDelete)
				return
			}
			handler.accountErasureCancel(writer, request)
			return
		}
		if strings.HasPrefix(request.URL.Path, "/v1/auth/sessions/") {
			if request.Method != http.MethodDelete {
				handler.methodNotAllowed(writer, request, http.MethodDelete)
				return
			}
			handler.revokeSession(writer, request)
			return
		}
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
	}
}

func (handler Handler) register(writer http.ResponseWriter, request *http.Request) {
	if handler.Service == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.publicMetadata(writer, request)
	if !ok {
		return
	}
	if !handler.allowRequest(writer, request, "identity-register-ip", registerIPLimit, metadata.ClientIPHash) {
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
	if !handler.allowRequest(writer, request, "identity-register-subject", registerSubjectLimit, metadata.ClientIPHash, []byte(normalizedEmail)) {
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
	if handler.Service == nil {
		handler.internalError(writer, request)
		return
	}
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
	if !handler.allowRequest(writer, request, "identity-verify-email-token", tokenAttemptLimit, metadata.ClientIPHash, []byte(body.Token)) {
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
	if handler.Service == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.publicMetadata(writer, request)
	if !ok {
		return
	}
	if !handler.allowRequest(writer, request, "identity-login-ip", loginIPLimit, metadata.ClientIPHash) {
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
	if !handler.allowRequest(writer, request, "identity-login-subject", loginSubjectLimit, metadata.ClientIPHash, []byte(normalizedEmail)) {
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

func (handler Handler) allowRequest(writer http.ResponseWriter, request *http.Request, action string, limit platformratelimit.Limit, subjectParts ...[]byte) bool {
	if handler.RateLimiter == nil {
		return true
	}
	digest, err := platformratelimit.SubjectDigest(handler.RateLimitPepper, action, subjectParts...)
	if err != nil {
		handler.internalError(writer, request)
		return false
	}
	decision, err := handler.RateLimiter.Allow(request.Context(), platformratelimit.Request{Action: action, SubjectDigest: digest, Cost: 1, Limit: limit})
	if err != nil {
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return false
	}
	if !decision.Allowed {
		retrySeconds := int64((decision.RetryAfter + time.Second - 1) / time.Second)
		if retrySeconds < 1 {
			retrySeconds = 1
		}
		writer.Header().Set("Retry-After", strconv.FormatInt(retrySeconds, 10))
		handler.writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Rate limited", true)
		return false
	}
	return true
}

func (handler Handler) publicMetadata(writer http.ResponseWriter, request *http.Request) (RequestMetadata, bool) {
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.PublicRequest || !claims.CSRFVerified {
		handler.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return RequestMetadata{}, false
	}
	idempotencyValues := request.Header.Values(transport.IdempotencyHeader)
	if len(idempotencyValues) != 1 || idempotency.ValidateRawKey(idempotencyValues[0]) != nil {
		handler.validationFailed(writer, request)
		return RequestMetadata{}, false
	}
	ipHash, ipErr := decodeHash(claims.ClientIPHash)
	userAgentHash, userAgentErr := decodeHash(claims.UserAgentHash)
	if ipErr != nil || userAgentErr != nil {
		handler.internalError(writer, request)
		return RequestMetadata{}, false
	}
	return RequestMetadata{RequestID: claims.RequestID, IdempotencyKey: idempotencyValues[0], ClientIPHash: ipHash, UserAgentHash: userAgentHash}, true
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
	case errors.Is(err, ErrPermissionDenied):
		handler.writeProblem(writer, request, http.StatusForbidden, "permission_denied", "Permission denied", false)
	case errors.Is(err, ErrStateConflict):
		handler.writeProblem(writer, request, http.StatusConflict, "state_conflict", "State conflict", true)
	case errors.Is(err, ErrIdempotencyConflict):
		handler.writeProblem(writer, request, http.StatusConflict, "idempotency_conflict", "Idempotency conflict", false)
	case errors.Is(err, ErrVersionConflict):
		handler.writeProblem(writer, request, http.StatusConflict, "version_conflict", "Version conflict", false)
	case errors.Is(err, ErrResourceNotFound):
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
	case errors.Is(err, ErrValidation):
		handler.validationFailed(writer, request)
	case errors.Is(err, ErrReauthenticationRequired):
		handler.writeProblem(writer, request, http.StatusUnauthorized, "reauthentication_required", "Reauthentication required", false)
	case errors.Is(err, ErrRateLimited):
		handler.writeProblem(writer, request, http.StatusTooManyRequests, "rate_limited", "Rate limited", true)
	case errors.Is(err, ErrDependencyUnavailable):
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
	case errors.Is(err, ErrClaimManualReview):
		handler.writeProblem(writer, request, http.StatusConflict, "claim_manual_review", "Claim requires manual review", false)
	default:
		handler.internalError(writer, request)
	}
}

func (handler Handler) methodNotAllowed(writer http.ResponseWriter, request *http.Request, allowed ...string) {
	writer.Header().Set("Allow", strings.Join(allowed, ", "))
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
