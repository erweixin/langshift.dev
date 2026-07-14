package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/identity/session"
)

type passwordServiceStub struct {
	forgot func(context.Context, PasswordForgotCommand) (PasswordForgotResult, error)
	reset  func(context.Context, PasswordResetCommand) (PasswordResetResult, error)
	change func(context.Context, PasswordChangeCommand) (PasswordChangeResult, error)
}

func (stub passwordServiceStub) ForgotPassword(ctx context.Context, command PasswordForgotCommand) (PasswordForgotResult, error) {
	if stub.forgot == nil {
		return PasswordForgotResult{}, errors.New("unexpected forgot password")
	}
	return stub.forgot(ctx, command)
}
func (stub passwordServiceStub) ResetPassword(ctx context.Context, command PasswordResetCommand) (PasswordResetResult, error) {
	if stub.reset == nil {
		return PasswordResetResult{}, errors.New("unexpected reset password")
	}
	return stub.reset(ctx, command)
}
func (stub passwordServiceStub) ChangePassword(ctx context.Context, command PasswordChangeCommand) (PasswordChangeResult, error) {
	if stub.change == nil {
		return PasswordChangeResult{}, errors.New("unexpected change password")
	}
	return stub.change(ctx, command)
}

func TestPasswordForgotNormalizesEmailAndEnforcesUniformResponse(t *testing.T) {
	service := passwordServiceStub{forgot: func(_ context.Context, command PasswordForgotCommand) (PasswordForgotResult, error) {
		if command.NormalizedEmail != "member@example.com" || command.ClientRequestID != "forgot-request-001" {
			t.Fatalf("command=%#v", command)
		}
		return PasswordForgotResult{Status: "accepted", Message: PasswordResetAcceptedMessage}, nil
	}}
	recorder := servePublicHandler(t, Handler{Passwords: service, Now: func() time.Time { return apiTestNow }}, http.MethodPost, "/v1/auth/password/forgot", "application/json", "forgot-key-00000001", `{"request_id":"forgot-request-001","email":" MEMBER@Example.com "}`)
	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"status":"accepted","message":"`+PasswordResetAcceptedMessage+`"}`+"\n" {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPasswordResetNeverReturnsSecretsAndClearsLocalSession(t *testing.T) {
	token := strings.Repeat("r", 48)
	newPassword := "new secure password value"
	service := passwordServiceStub{reset: func(_ context.Context, command PasswordResetCommand) (PasswordResetResult, error) {
		if command.Token != token || command.NewPassword != newPassword {
			t.Fatalf("command=%#v", command)
		}
		return PasswordResetResult{RevokedSessionCount: 3}, nil
	}}
	body := `{"request_id":"reset-request-0001","token":"` + token + `","new_password":"` + newPassword + `"}`
	recorder := servePublicHandler(t, Handler{Passwords: service, Now: func() time.Time { return apiTestNow }}, http.MethodPost, "/v1/auth/password/reset", "application/json", "reset-key-000000001", body)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), token) || strings.Contains(recorder.Body.String(), newPassword) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 || cookies[0].MaxAge != -1 || cookies[1].MaxAge != -1 {
		t.Fatalf("cookies=%#v", cookies)
	}
}

func TestPasswordChangeReauthenticatesAndRotatesSessionCookies(t *testing.T) {
	sessionCredential, err := session.NewFrom(bytes.NewReader(bytes.Repeat([]byte{0x71}, 32)), bytes.Repeat([]byte{0x72}, 32))
	if err != nil {
		t.Fatal(err)
	}
	csrfCredential, err := session.NewFrom(bytes.NewReader(bytes.Repeat([]byte{0x73}, 32)), bytes.Repeat([]byte{0x74}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := passwordServiceStub{change: func(_ context.Context, command PasswordChangeCommand) (PasswordChangeResult, error) {
		if command.CurrentPassword != "current password value" || command.NewPassword != "replacement password value" || command.SessionID != "session-auth" || command.MembershipID != "membership-auth" {
			t.Fatalf("command=%#v", command)
		}
		return PasswordChangeResult{ID: "credential-1", Version: 2, Status: "changed", UpdatedAt: apiTestNow, SessionToken: sessionCredential.Raw, CSRFToken: csrfCredential.Raw, ExpiresAt: apiTestNow.Add(time.Hour)}, nil
	}}
	body := `{"request_id":"change-request-001","current_password":"current password value","new_password":"replacement password value"}`
	recorder := serveAuthenticatedHandler(t, Handler{Passwords: service, Now: func() time.Time { return apiTestNow }}, http.MethodPost, "/v1/auth/password/change", "change-key-0000001", "", body)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"2"` || strings.Contains(recorder.Body.String(), sessionCredential.Raw) {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 || cookies[0].Name != session.CookieName || cookies[0].Value != sessionCredential.Raw || cookies[1].Name != session.CSRFCookieName || cookies[1].Value != csrfCredential.Raw {
		t.Fatalf("cookies=%#v", cookies)
	}
}

func TestPasswordChangeMapsCurrentPasswordFailureToReauthentication(t *testing.T) {
	service := passwordServiceStub{change: func(context.Context, PasswordChangeCommand) (PasswordChangeResult, error) {
		return PasswordChangeResult{}, ErrReauthenticationRequired
	}}
	body := `{"request_id":"change-request-001","current_password":"wrong password","new_password":"replacement password value"}`
	recorder := serveAuthenticatedHandler(t, Handler{Passwords: service, Now: func() time.Time { return apiTestNow }}, http.MethodPost, "/v1/auth/password/change", "change-key-0000001", "", body)
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), `"code":"reauthentication_required"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
