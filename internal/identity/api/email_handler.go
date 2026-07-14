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

type EmailChangeCommand struct {
	AuthenticatedRequestMetadata
	NewNormalizedEmail  string
	CurrentPassword     string
	ExpectedUserVersion uint64
}

type EmailChangeResult struct {
	ID        string
	Version   uint64
	Status    string
	UpdatedAt time.Time
}

type EmailConfirmChangeCommand struct {
	AuthenticatedRequestMetadata
	Token string
}

type EmailConfirmChangeResult struct {
	EmailChangeResult
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
}

type EmailService interface {
	ChangeEmail(context.Context, EmailChangeCommand) (EmailChangeResult, error)
	ConfirmEmailChange(context.Context, EmailConfirmChangeCommand) (EmailConfirmChangeResult, error)
}

func (handler Handler) emailChange(writer http.ResponseWriter, request *http.Request) {
	if handler.Emails == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	expectedVersion, ok := handler.ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID           string `json:"request_id"`
		NewEmail            string `json:"new_email"`
		CurrentPassword     string `json:"current_password"`
		ExpectedUserVersion uint64 `json:"expected_user_version"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	defer func() { body.CurrentPassword = "" }()
	normalizedEmail, err := identityemail.Normalize(body.NewEmail)
	if err != nil || !validClientRequestID(body.RequestID) || password.ValidateForAuthentication(body.CurrentPassword) != nil || body.ExpectedUserVersion == 0 || body.ExpectedUserVersion != expectedVersion {
		handler.validationFailed(writer, request)
		return
	}
	if !handler.allowRequest(writer, request, "identity-email-change-actor", mailActorLimit, []byte(metadata.UserID)) || !handler.allowRequest(writer, request, "identity-email-change-target", mailTargetLimit, metadata.ClientIPHash, []byte(normalizedEmail)) {
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Emails.ChangeEmail(request.Context(), EmailChangeCommand{AuthenticatedRequestMetadata: metadata, NewNormalizedEmail: normalizedEmail, CurrentPassword: body.CurrentPassword, ExpectedUserVersion: expectedVersion})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeEmailMutation(writer, request, result)
}

func (handler Handler) emailConfirmChange(writer http.ResponseWriter, request *http.Request) {
	if handler.Emails == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
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
	result, err := handler.Emails.ConfirmEmailChange(request.Context(), EmailConfirmChangeCommand{AuthenticatedRequestMetadata: metadata, Token: body.Token})
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
	handler.writeEmailMutation(writer, request, result.EmailChangeResult)
}

func (handler Handler) writeEmailMutation(writer http.ResponseWriter, request *http.Request, result EmailChangeResult) {
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
