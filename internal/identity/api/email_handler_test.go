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

type emailServiceStub struct {
	change  func(context.Context, EmailChangeCommand) (EmailChangeResult, error)
	confirm func(context.Context, EmailConfirmChangeCommand) (EmailConfirmChangeResult, error)
}

func (stub emailServiceStub) ChangeEmail(ctx context.Context, command EmailChangeCommand) (EmailChangeResult, error) {
	if stub.change == nil {
		return EmailChangeResult{}, errors.New("unexpected email change")
	}
	return stub.change(ctx, command)
}

func (stub emailServiceStub) ConfirmEmailChange(ctx context.Context, command EmailConfirmChangeCommand) (EmailConfirmChangeResult, error) {
	if stub.confirm == nil {
		return EmailConfirmChangeResult{}, errors.New("unexpected email confirmation")
	}
	return stub.confirm(ctx, command)
}

func TestEmailChangeRequiresMatchingIfMatchAndCurrentPassword(t *testing.T) {
	called := false
	service := emailServiceStub{change: func(_ context.Context, command EmailChangeCommand) (EmailChangeResult, error) {
		called = true
		if command.NewNormalizedEmail != "new-address@example.com" || command.CurrentPassword != "current password value" || command.ExpectedUserVersion != 4 || command.SessionID != "session-auth" {
			t.Fatalf("command=%#v", command)
		}
		return EmailChangeResult{ID: "verification-1", Version: 1, Status: "pending_confirmation", UpdatedAt: apiTestNow}, nil
	}}
	body := `{"request_id":"email-change-001","new_email":" NEW-ADDRESS@Example.com ","current_password":"current password value","expected_user_version":4}`
	recorder := serveAuthenticatedHandler(t, Handler{Emails: service, Now: func() time.Time { return apiTestNow }}, http.MethodPost, "/v1/auth/email/change", "email-change-key-01", `"4"`, body)
	if recorder.Code != http.StatusOK || !called || recorder.Header().Get("ETag") != `"1"` || !strings.Contains(recorder.Body.String(), `"status":"pending_confirmation"`) {
		t.Fatalf("status=%d called=%v headers=%v body=%s", recorder.Code, called, recorder.Header(), recorder.Body.String())
	}
	called = false
	recorder = serveAuthenticatedHandler(t, Handler{Emails: service, Now: func() time.Time { return apiTestNow }}, http.MethodPost, "/v1/auth/email/change", "email-change-key-01", `"3"`, body)
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("mismatch status=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
	}
}

func TestEmailConfirmationRotatesSessionWithoutReturningSecrets(t *testing.T) {
	sessionCredential, err := session.NewFrom(bytes.NewReader(bytes.Repeat([]byte{0x21}, 32)), bytes.Repeat([]byte{0x22}, 32))
	if err != nil {
		t.Fatal(err)
	}
	csrfCredential, err := session.NewFrom(bytes.NewReader(bytes.Repeat([]byte{0x23}, 32)), bytes.Repeat([]byte{0x24}, 32))
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("e", 48)
	service := emailServiceStub{confirm: func(_ context.Context, command EmailConfirmChangeCommand) (EmailConfirmChangeResult, error) {
		if command.Token != token || command.ClientRequestID != "email-confirm-001" {
			t.Fatalf("command=%#v", command)
		}
		return EmailConfirmChangeResult{EmailChangeResult: EmailChangeResult{ID: "user-auth", Version: 5, Status: "changed", UpdatedAt: apiTestNow}, SessionToken: sessionCredential.Raw, CSRFToken: csrfCredential.Raw, ExpiresAt: apiTestNow.Add(time.Hour)}, nil
	}}
	body := `{"request_id":"email-confirm-001","token":"` + token + `"}`
	recorder := serveAuthenticatedHandler(t, Handler{Emails: service, Now: func() time.Time { return apiTestNow }}, http.MethodPost, "/v1/auth/email/confirm-change", "email-confirm-key-1", "", body)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), token) || strings.Contains(recorder.Body.String(), sessionCredential.Raw) || recorder.Header().Get("ETag") != `"5"` {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 || cookies[0].Value != sessionCredential.Raw || cookies[1].Value != csrfCredential.Raw {
		t.Fatalf("cookies=%#v", cookies)
	}
}
