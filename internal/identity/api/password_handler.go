package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	identityemail "github.com/langshift/lites/internal/identity/email"
	"github.com/langshift/lites/internal/identity/password"
	"github.com/langshift/lites/internal/identity/session"
)

const PasswordResetAcceptedMessage = "If the account exists, password reset instructions will be sent."

type PasswordForgotCommand struct {
	RequestMetadata
	NormalizedEmail string
}

type PasswordForgotResult struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type PasswordResetCommand struct {
	RequestMetadata
	Token       string
	NewPassword string
}

type PasswordResetResult struct{ RevokedSessionCount int }

type PasswordChangeCommand struct {
	AuthenticatedRequestMetadata
	CurrentPassword string
	NewPassword     string
}

type PasswordChangeResult struct {
	ID           string
	Version      uint64
	Status       string
	UpdatedAt    time.Time
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
}

type PasswordService interface {
	ForgotPassword(context.Context, PasswordForgotCommand) (PasswordForgotResult, error)
	ResetPassword(context.Context, PasswordResetCommand) (PasswordResetResult, error)
	ChangePassword(context.Context, PasswordChangeCommand) (PasswordChangeResult, error)
}

func (handler Handler) passwordForgot(writer http.ResponseWriter, request *http.Request) {
	if handler.Passwords == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.publicMetadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		Email     string `json:"email"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	normalizedEmail, err := identityemail.Normalize(body.Email)
	if err != nil || !validClientRequestID(body.RequestID) {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Passwords.ForgotPassword(request.Context(), PasswordForgotCommand{RequestMetadata: metadata, NormalizedEmail: normalizedEmail})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.Status != "accepted" || result.Message != PasswordResetAcceptedMessage {
		handler.internalError(writer, request)
		return
	}
	handler.writeJSON(writer, http.StatusOK, struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}{Status: result.Status, Message: result.Message})
}

func (handler Handler) passwordReset(writer http.ResponseWriter, request *http.Request) {
	if handler.Passwords == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.publicMetadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID   string `json:"request_id"`
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	defer func() { body.Token, body.NewPassword = "", "" }()
	if !validClientRequestID(body.RequestID) || len(body.Token) < 32 || len(body.Token) > 512 || strings.ContainsFunc(body.Token, unicode.IsSpace) || password.ValidateForRegistration(body.NewPassword) != nil {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Passwords.ResetPassword(request.Context(), PasswordResetCommand{RequestMetadata: metadata, Token: body.Token, NewPassword: body.NewPassword})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.RevokedSessionCount < 0 {
		handler.internalError(writer, request)
		return
	}
	handler.clearSessionCookies(writer)
	handler.writeJSON(writer, http.StatusOK, struct {
		Status              string `json:"status"`
		RevokedSessionCount int    `json:"revoked_session_count"`
	}{Status: "changed", RevokedSessionCount: result.RevokedSessionCount})
}

func (handler Handler) passwordChange(writer http.ResponseWriter, request *http.Request) {
	if handler.Passwords == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	var body struct {
		RequestID       string `json:"request_id"`
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	defer func() { body.CurrentPassword, body.NewPassword = "", "" }()
	if !validClientRequestID(body.RequestID) || password.ValidateForAuthentication(body.CurrentPassword) != nil || password.ValidateForRegistration(body.NewPassword) != nil || body.CurrentPassword == body.NewPassword {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Passwords.ChangePassword(request.Context(), PasswordChangeCommand{AuthenticatedRequestMetadata: metadata, CurrentPassword: body.CurrentPassword, NewPassword: body.NewPassword})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.ID == "" || result.Version == 0 || result.Status != "changed" || result.UpdatedAt.IsZero() || !result.ExpiresAt.After(handler.now()) {
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
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatUint(result.Version, 10)))
	handler.writeJSON(writer, http.StatusOK, struct {
		ID        string    `json:"id"`
		Version   uint64    `json:"version"`
		Status    string    `json:"status"`
		UpdatedAt time.Time `json:"updated_at"`
	}{ID: result.ID, Version: result.Version, Status: result.Status, UpdatedAt: result.UpdatedAt.UTC()})
}
