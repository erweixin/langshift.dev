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
}

func (stub onboardingStub) CreateOnboarding(ctx context.Context, command OnboardingCreateCommand) (OnboardingCreateResult, error) {
	return stub.create(ctx, command)
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
