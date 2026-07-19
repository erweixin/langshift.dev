package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestResendVerificationNormalizesEmailAndReturnsEnumerationSafeResponse(t *testing.T) {
	next := apiTestNow.Add(15 * time.Minute)
	service := serviceStub{resendVerification: func(_ context.Context, command ResendVerificationCommand) (ResendVerificationResult, error) {
		if command.NormalizedEmail != "person@example.com" || command.ClientRequestID != "resend-request-001" || command.IdempotencyKey != "resend-key-00000001" || len(command.ClientIPHash) != 32 {
			t.Fatalf("command=%#v", command)
		}
		return ResendVerificationResult{Status: "accepted", NextAllowedAt: next}, nil
	}}
	recorder := servePublicHandler(t, Handler{Service: service}, http.MethodPost, "/v1/auth/resend-verification", "application/json", "resend-key-00000001", `{"request_id":"resend-request-001","email":" Person@Example.com "}`)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" || !strings.Contains(recorder.Body.String(), `"status":"accepted"`) || !strings.Contains(recorder.Body.String(), next.Format(time.RFC3339)) {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestResendVerificationRejectsInvalidInputBeforeService(t *testing.T) {
	called := false
	service := serviceStub{resendVerification: func(context.Context, ResendVerificationCommand) (ResendVerificationResult, error) {
		called = true
		return ResendVerificationResult{}, nil
	}}
	recorder := servePublicHandler(t, Handler{Service: service}, http.MethodPost, "/v1/auth/resend-verification", "application/json", "resend-key-00000002", `{"request_id":"short","email":"not-an-email"}`)
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("status=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
	}
}
