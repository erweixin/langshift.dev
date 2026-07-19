package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/identity/password"
	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

type AuthenticatedRequestMetadata struct {
	RequestMetadata
	UserID       string
	TenantID     string
	MembershipID string
	SessionID    string
}

type LogoutCommand struct {
	AuthenticatedRequestMetadata
	AllDevices bool
}

type LogoutResult struct{ RevokedSessionCount int }

type SessionsQuery struct {
	AuthenticatedRequestMetadata
	Cursor string
	Limit  int
}

type SessionItem struct {
	ID             string
	ActiveTenantID string
	Version        uint64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastSeenAt     time.Time
	ExpiresAt      time.Time
	RevokedAt      *time.Time
	DeviceLabel    *string
	Current        bool
}

type SessionsPage struct {
	Items      []SessionItem
	NextCursor *string
}

type RevokeSessionCommand struct {
	AuthenticatedRequestMetadata
	TargetSessionID string
	ExpectedVersion uint64
	ReasonCode      string
}

type RevokeOtherSessionsCommand struct{ AuthenticatedRequestMetadata }

type SwitchTenantCommand struct {
	AuthenticatedRequestMetadata
	ActiveTenantID         string
	ExpectedSessionVersion uint64
}

type ReauthenticateCommand struct {
	AuthenticatedRequestMetadata
	Password string
}

type ReauthenticationResult struct {
	SessionID         string    `json:"session_id"`
	SessionVersion    uint64    `json:"session_version"`
	ReauthenticatedAt time.Time `json:"reauthenticated_at"`
	ValidUntil        time.Time `json:"valid_until"`
	Replayed          bool      `json:"replayed,omitempty"`
}

type SessionMutationResult struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SessionService interface {
	Reauthenticate(context.Context, ReauthenticateCommand) (ReauthenticationResult, error)
	Logout(context.Context, LogoutCommand) (LogoutResult, error)
	ListSessions(context.Context, SessionsQuery) (SessionsPage, error)
	SwitchTenant(context.Context, SwitchTenantCommand) (SessionMutationResult, error)
	RevokeSession(context.Context, RevokeSessionCommand) (SessionMutationResult, error)
	RevokeOtherSessions(context.Context, RevokeOtherSessionsCommand) (SessionMutationResult, error)
}

func (handler Handler) switchActiveTenant(writer http.ResponseWriter, request *http.Request) {
	if handler.Sessions == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	expected, ok := handler.ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID              string `json:"request_id"`
		ActiveTenantID         string `json:"active_tenant_id"`
		ExpectedSessionVersion uint64 `json:"expected_session_version"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || !validOpaqueField(body.ActiveTenantID, 1, 200) || body.ExpectedSessionVersion == 0 || body.ExpectedSessionVersion != expected {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Sessions.SwitchTenant(request.Context(), SwitchTenantCommand{AuthenticatedRequestMetadata: metadata, ActiveTenantID: body.ActiveTenantID, ExpectedSessionVersion: expected})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeSessionMutation(writer, request, result)
}

func (handler Handler) reauthenticate(writer http.ResponseWriter, request *http.Request) {
	if handler.Sessions == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		Password  string `json:"password"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	defer func() { body.Password = "" }()
	if !validClientRequestID(body.RequestID) || password.ValidateForAuthentication(body.Password) != nil {
		handler.validationFailed(writer, request)
		return
	}
	if !handler.allowRequest(writer, request, "identity-reauthentication-session", reauthenticationSessionLimit, metadata.ClientIPHash, []byte(metadata.SessionID)) {
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Sessions.Reauthenticate(request.Context(), ReauthenticateCommand{AuthenticatedRequestMetadata: metadata, Password: body.Password})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.SessionID != metadata.SessionID || result.SessionVersion == 0 || result.ReauthenticatedAt.IsZero() || !result.ValidUntil.After(result.ReauthenticatedAt) {
		handler.internalError(writer, request)
		return
	}
	writer.Header().Set("Cache-Control", "private, no-store")
	handler.writeJSON(writer, http.StatusOK, result)
}

func (handler Handler) logout(writer http.ResponseWriter, request *http.Request) {
	if handler.Sessions == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	var body struct {
		RequestID  string  `json:"request_id"`
		SessionID  *string `json:"session_id"`
		AllDevices bool    `json:"all_devices"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || (!body.AllDevices && (body.SessionID == nil || *body.SessionID != metadata.SessionID)) || (body.AllDevices && body.SessionID != nil && *body.SessionID != metadata.SessionID) {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Sessions.Logout(request.Context(), LogoutCommand{AuthenticatedRequestMetadata: metadata, AllDevices: body.AllDevices})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.RevokedSessionCount < 1 {
		handler.internalError(writer, request)
		return
	}
	handler.clearSessionCookies(writer)
	handler.writeJSON(writer, http.StatusOK, struct {
		Status              string `json:"status"`
		RevokedSessionCount int    `json:"revoked_session_count"`
	}{Status: "revoked", RevokedSessionCount: result.RevokedSessionCount})
}

func (handler Handler) listSessions(writer http.ResponseWriter, request *http.Request) {
	if handler.Sessions == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, false)
	if !ok {
		return
	}
	values, exists := request.URL.Query()["cursor"]
	if len(request.URL.Query()) > 1 || (!exists && len(request.URL.Query()) != 0) || (exists && len(values) != 1) || (len(values) == 1 && (values[0] == "" || len(values[0]) > 4096)) {
		handler.validationFailed(writer, request)
		return
	}
	var cursor string
	if len(values) == 1 {
		cursor = values[0]
	}
	page, err := handler.Sessions.ListSessions(request.Context(), SessionsQuery{AuthenticatedRequestMetadata: metadata, Cursor: cursor, Limit: 50})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	items := make([]sessionResource, len(page.Items))
	for index, item := range page.Items {
		items[index] = sessionResource{ID: item.ID, ActiveTenantID: item.ActiveTenantID, Version: item.Version, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, LastSeenAt: item.LastSeenAt, ExpiresAt: item.ExpiresAt, RevokedAt: item.RevokedAt, DeviceLabel: item.DeviceLabel, Current: item.Current}
	}
	handler.writeJSON(writer, http.StatusOK, struct {
		Items      []sessionResource `json:"items"`
		NextCursor *string           `json:"next_cursor"`
	}{Items: items, NextCursor: page.NextCursor})
}

type sessionResource struct {
	ID             string     `json:"id"`
	ActiveTenantID string     `json:"active_tenant_id"`
	Version        uint64     `json:"version"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastSeenAt     time.Time  `json:"last_seen_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	RevokedAt      *time.Time `json:"revoked_at"`
	DeviceLabel    *string    `json:"device_label"`
	Current        bool       `json:"current"`
}

func (handler Handler) revokeSession(writer http.ResponseWriter, request *http.Request) {
	if handler.Sessions == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	targetID := strings.TrimPrefix(request.URL.Path, "/v1/auth/sessions/")
	if targetID == "" || strings.Contains(targetID, "/") || len(targetID) > 200 {
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	expected, ok := handler.ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID              string `json:"request_id"`
		ExpectedSessionVersion uint64 `json:"expected_session_version"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || body.ExpectedSessionVersion == 0 || body.ExpectedSessionVersion != expected {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Sessions.RevokeSession(request.Context(), RevokeSessionCommand{AuthenticatedRequestMetadata: metadata, TargetSessionID: targetID, ExpectedVersion: expected, ReasonCode: "user_revoked"})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if targetID == metadata.SessionID {
		handler.clearSessionCookies(writer)
	}
	handler.writeSessionMutation(writer, request, result)
}

func (handler Handler) revokeOtherSessions(writer http.ResponseWriter, request *http.Request) {
	if handler.Sessions == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	scopeValues := request.URL.Query()["scope"]
	if len(request.URL.Query()) != 1 || len(scopeValues) != 1 || scopeValues[0] != "others" {
		handler.validationFailed(writer, request)
		return
	}
	var body struct {
		RequestID        string `json:"request_id"`
		CurrentSessionID string `json:"current_session_id"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || body.CurrentSessionID != metadata.SessionID {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Sessions.RevokeOtherSessions(request.Context(), RevokeOtherSessionsCommand{AuthenticatedRequestMetadata: metadata})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeSessionMutation(writer, request, result)
}

func (handler Handler) authenticatedMetadata(writer http.ResponseWriter, request *http.Request, requireIdempotency bool) (AuthenticatedRequestMetadata, bool) {
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.SessionID == "" || (requireIdempotency && !claims.CSRFVerified) {
		handler.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return AuthenticatedRequestMetadata{}, false
	}
	var idempotencyKey string
	if requireIdempotency {
		values := request.Header.Values(transport.IdempotencyHeader)
		if len(values) != 1 || idempotency.ValidateRawKey(values[0]) != nil {
			handler.validationFailed(writer, request)
			return AuthenticatedRequestMetadata{}, false
		}
		idempotencyKey = values[0]
	}
	ipHash, ipErr := decodeHash(claims.ClientIPHash)
	userAgentHash, userAgentErr := decodeHash(claims.UserAgentHash)
	if ipErr != nil || userAgentErr != nil {
		handler.internalError(writer, request)
		return AuthenticatedRequestMetadata{}, false
	}
	return AuthenticatedRequestMetadata{RequestMetadata: RequestMetadata{RequestID: claims.RequestID, IdempotencyKey: idempotencyKey, ClientIPHash: ipHash, UserAgentHash: userAgentHash}, UserID: claims.SubjectID, TenantID: claims.TenantID, MembershipID: claims.MembershipID, SessionID: claims.SessionID}, true
}

func (handler Handler) ifMatch(writer http.ResponseWriter, request *http.Request) (uint64, bool) {
	values := request.Header.Values("If-Match")
	if len(values) == 0 {
		handler.writeProblem(writer, request, http.StatusPreconditionRequired, "precondition_required", "Precondition required", false)
		return 0, false
	}
	if len(values) != 1 {
		handler.validationFailed(writer, request)
		return 0, false
	}
	value := values[0]
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		handler.validationFailed(writer, request)
		return 0, false
	}
	version, err := strconv.ParseUint(value[1:len(value)-1], 10, 64)
	if err != nil || version == 0 {
		handler.validationFailed(writer, request)
		return 0, false
	}
	return version, true
}

func (handler Handler) clearSessionCookies(writer http.ResponseWriter) {
	http.SetCookie(writer, session.ClearCookie())
	http.SetCookie(writer, session.ClearCSRFCookie())
}

func (handler Handler) writeSessionMutation(writer http.ResponseWriter, request *http.Request, result SessionMutationResult) {
	if result.ID == "" || result.Version == 0 || result.Status == "" || result.UpdatedAt.IsZero() {
		handler.internalError(writer, request)
		return
	}
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatUint(result.Version, 10)))
	handler.writeJSON(writer, http.StatusOK, struct {
		ID        string    `json:"id"`
		Version   uint64    `json:"version"`
		Status    string    `json:"status"`
		UpdatedAt time.Time `json:"updated_at"`
	}{ID: result.ID, Version: result.Version, Status: result.Status, UpdatedAt: result.UpdatedAt.UTC()})
}
