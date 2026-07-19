package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/langshift/lites/internal/identity/anonymoussession"
	"github.com/langshift/lites/internal/security/trustedcontext"
)

type onboardingStub struct {
	create func(context.Context, OnboardingCreateCommand) (OnboardingCreateResult, error)
	update func(context.Context, OnboardingUpdateCommand) (OnboardingUpdateResult, error)
}

func (stub onboardingStub) CreateOnboarding(ctx context.Context, command OnboardingCreateCommand) (OnboardingCreateResult, error) {
	return stub.create(ctx, command)
}

func (stub onboardingStub) UpdateOnboarding(ctx context.Context, command OnboardingUpdateCommand) (OnboardingUpdateResult, error) {
	return stub.update(ctx, command)
}

func TestOnboardingCreateBootstrapsSecureAnonymousCookies(t *testing.T) {
	key := bytes.Repeat([]byte{0x81}, 32)
	pepper := bytes.Repeat([]byte{0x82}, 32)
	credential, err := (anonymoussession.Signer{KeyID: "anonymous-test", Key: key, DigestPepper: pepper, Random: bytes.NewReader(bytes.Repeat([]byte{0x83}, 32))}).New(apiTestNow, anonymoussession.MaximumTTL)
	if err != nil {
		t.Fatal(err)
	}
	service := onboardingStub{create: func(_ context.Context, command OnboardingCreateCommand) (OnboardingCreateResult, error) {
		if command.PrincipalKind != trustedcontext.PublicRequest || command.UserID != "" || command.TenantID != "" || command.CurrentRole != "Product manager" || command.TargetRole != "AI product lead" || command.WeeklyMinutes != 300 || command.ClientRequestID != "onboarding-request-001" {
			t.Fatalf("command=%#v", command)
		}
		return OnboardingCreateResult{ID: "onboarding-1", Version: 1, Status: "collecting", UpdatedAt: apiTestNow, AnonymousHandle: credential.Raw, HandleExpiresAt: credential.ExpiresAt}, nil
	}}
	recorder := servePublicHandler(t, Handler{Onboarding: service, AnonymousCSRFKey: key, Now: func() time.Time { return apiTestNow }}, http.MethodPost, "/v1/onboarding-sessions", "application/json", "onboarding-key-00000001", `{"request_id":"onboarding-request-001","current_role":" Product manager ","target_role":"AI product lead","experience_summary":"Built B2B workflows","weekly_minutes":300}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		ID      string `json:"id"`
		Version uint64 `json:"version"`
		Status  string `json:"status"`
	}
	if err = json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.ID != "onboarding-1" || response.Version != 1 || response.Status != "collecting" {
		t.Fatalf("response=%#v error=%v", response, err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 || cookies[0].Name != anonymoussession.CookieName || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[1].Name != anonymoussession.CSRFCookieName || cookies[1].HttpOnly || !anonymoussession.VerifyCSRF(credential.Raw, cookies[1].Value, key) {
		t.Fatalf("cookies=%#v", cookies)
	}
}

func TestOnboardingUpdateRequiresCASAndReturnsDurableVersion(t *testing.T) {
	const sessionID = "6b000000-0000-4000-8000-000000000010"
	service := onboardingStub{update: func(_ context.Context, command OnboardingUpdateCommand) (OnboardingUpdateResult, error) {
		if command.PrincipalKind != trustedcontext.AuthenticatedUser || command.OnboardingSessionID != sessionID || command.UserID != "user-auth" || command.TenantID != "tenant-auth" || command.ExpectedOnboardingVersion != 1 || command.ClientRequestID != "onboarding-update-request-001" {
			t.Fatalf("command=%#v", command)
		}
		if command.CurrentRole == nil || *command.CurrentRole != "Senior frontend engineer" || command.ExperienceSummary == nil || *command.ExperienceSummary != "Led a migration across three teams" || command.WeeklyMinutes == nil || *command.WeeklyMinutes != 240 {
			t.Fatalf("patch=%#v", command)
		}
		return OnboardingUpdateResult{ID: sessionID, Version: 2, Status: "collecting", UpdatedAt: apiTestNow}, nil
	}}
	target := "/v1/onboarding-sessions/" + sessionID
	recorder := serveAuthenticatedHandler(t, Handler{Onboarding: service, Now: func() time.Time { return apiTestNow }}, http.MethodPatch, target, "onboarding-update-key-0001", `"1"`, `{"request_id":"onboarding-update-request-001","current_role":" Senior frontend engineer ","experience_summary":"Led a migration across three teams","weekly_minutes":240,"expected_onboarding_version":1}`)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"2"` || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d etag=%q cache=%q body=%s", recorder.Code, recorder.Header().Get("ETag"), recorder.Header().Get("Cache-Control"), recorder.Body.String())
	}
}
