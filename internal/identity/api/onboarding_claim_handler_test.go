package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/identity/anonymoussession"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

type onboardingClaimStub struct {
	claim func(context.Context, OnboardingClaimCommand) (OnboardingClaimResult, error)
}

func (stub onboardingClaimStub) ClaimOnboarding(ctx context.Context, command OnboardingClaimCommand) (OnboardingClaimResult, error) {
	return stub.claim(ctx, command)
}

func TestOnboardingClaimRequiresBoundAnonymousSubjectAndClearsIt(t *testing.T) {
	service := onboardingClaimStub{claim: func(_ context.Context, command OnboardingClaimCommand) (OnboardingClaimResult, error) {
		if command.OnboardingSessionID != "onboarding-1" || command.AnonymousSubjectID != "anonymous-subject" || command.UserID != "user-auth" || command.TenantID != "tenant-auth" || command.ExpectedClaimVersion != 4 || command.ClientRequestID != "claim-request-001" {
			t.Fatalf("command=%#v", command)
		}
		return OnboardingClaimResult{ID: "claim-1", Version: 5, Status: "reserved", UpdatedAt: apiTestNow}, nil
	}}
	recorder := serveAuthenticatedClaim(t, Handler{Claims: service}, `{"request_id":"claim-request-001","target_tenant_id":"tenant-auth","expected_claim_version":4}`)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"5"` {
		t.Fatalf("status=%d etag=%q body=%s", recorder.Code, recorder.Header().Get("ETag"), recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 || cookies[0].Name != anonymoussession.CookieName || cookies[0].MaxAge != -1 || cookies[1].Name != anonymoussession.CSRFCookieName || cookies[1].MaxAge != -1 {
		t.Fatalf("cookies=%#v", cookies)
	}
}

func serveAuthenticatedClaim(t *testing.T, handler Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	target := "/v1/onboarding-sessions/onboarding-1/claim"
	claims := trustedcontext.Claims{PrincipalKind: trustedcontext.AuthenticatedUser, Issuer: "gateway", Audience: "identity", SubjectID: "user-auth", TenantID: "tenant-auth", MembershipID: "membership-auth", SessionID: "session-auth", AnonymousSubjectID: "anonymous-subject", Roles: []string{"owner"}, RequestID: "server-claim-request", RequestMethod: http.MethodPost, RequestTarget: target, ClientIPHash: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x71}, 32)), UserAgentHash: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x72}, 32)), CSRFVerified: true, IssuedAt: apiTestNow.Unix(), ExpiresAt: apiTestNow.Add(time.Minute).Unix(), Nonce: "claim-nonce"}
	token, err := trustedcontext.Sign(claims, "key", privateKey, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://identity.internal"+target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(transport.RequestIDHeader, claims.RequestID)
	request.Header.Set(transport.TrustedContextHeader, token)
	request.Header.Set(transport.IdempotencyHeader, "claim-idempotency-0001")
	request.Header.Set("If-Match", `"4"`)
	request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}
	recorder := httptest.NewRecorder()
	middleware := serviceauth.Middleware{Verifier: trustedcontext.Verifier{Issuer: "gateway", Audience: "identity", Keys: map[string]ed25519.PublicKey{"key": publicKey}, MaximumTTL: 5 * time.Minute}, Now: func() time.Time { return apiTestNow }, RequireVerifiedClientCertificate: true}
	middleware.Wrap(handler).ServeHTTP(recorder, request)
	return recorder
}
