package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/langshift/lites/internal/identity/anonymoussession"
	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/security/trustedcontext"
)

type resolverStub struct {
	principal session.Principal
	err       error
}

type anonymousResolverStub struct {
	principal anonymoussession.Principal
	err       error
}

func (resolver anonymousResolverStub) Resolve(context.Context, string) (anonymoussession.Principal, error) {
	return resolver.principal, resolver.err
}

func (resolver resolverStub) Resolve(context.Context, string) (session.Principal, error) {
	return resolver.principal, resolver.err
}

func TestGatewayStripsForgedIdentityAndIssuesServerContext(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	principal := session.Principal{UserID: "user-real", TenantID: "tenant-real", MembershipID: "membership-real", SessionID: "session-real", Roles: []string{"member"}, ExpiresAt: now.Add(time.Hour)}
	boundary := TrustBoundary{Resolver: resolverStub{principal: principal}, SigningKey: privateKey, SigningKeyID: "gateway-key", Issuer: "lites-gateway", Audience: "identity-service", TTL: 2 * time.Minute, FingerprintPepper: bytes.Repeat([]byte{0x51}, 32), Now: func() time.Time { return now }}
	verifier := trustedcontext.Verifier{Issuer: "lites-gateway", Audience: "identity-service", Keys: map[string]ed25519.PublicKey{"gateway-key": publicKey}, MaximumTTL: 5 * time.Minute, ClockSkew: time.Second}
	upstream := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		for _, name := range []string{"Authorization", "X-Lites-User-ID", "X-Lites-Tenant-ID", "X-Lites-Roles"} {
			if request.Header.Get(name) != "" {
				t.Errorf("forged header survived: %s", name)
			}
		}
		claims, err := verifier.Verify(request.Header.Get(TrustedContextHeader), now)
		if err != nil {
			t.Errorf("verify context: %v", err)
			writer.WriteHeader(500)
			return
		}
		if claims.SubjectID != "user-real" || claims.TenantID != "tenant-real" {
			t.Errorf("unexpected claims: %#v", claims)
		}
		if _, err = verifier.VerifyRequest(request.Header.Get(TrustedContextHeader), now, request.Header.Get(RequestIDHeader), request.Method, request.URL.RequestURI(), false); err != nil {
			t.Errorf("request binding: %v", err)
		}
		if request.Header.Get(RequestIDHeader) == "attacker-request" {
			t.Error("client request id survived trust boundary")
		}
		if matched := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(request.Header.Get(RequestIDHeader)); !matched {
			t.Errorf("server request id is not a UUIDv4: %q", request.Header.Get(RequestIDHeader))
		}
		if _, err := request.Cookie(session.CookieName); err == nil {
			t.Error("session bearer cookie survived trust boundary")
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "https://api.lites.dev/v1/account", nil)
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: "opaque-session"})
	request.Header.Set(TrustedContextHeader, "attacker-token")
	request.Header.Set("X-Lites-User-ID", "attacker")
	request.Header.Set("X-Lites-Tenant-ID", "victim")
	request.Header.Set("X-Lites-Roles", "owner")
	request.Header.Set("Authorization", "Bearer attacker-token")
	request.Header.Set("X-Forwarded-For", "203.0.113.99")
	request.Header.Set("User-Agent", "sensitive-device-label")
	request.Header.Set(RequestIDHeader, "attacker-request")
	recorder := httptest.NewRecorder()
	boundary.Wrap(upstream).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestGatewayBindsTrustedContextAudienceToSelectedUpstream(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	principal := session.Principal{UserID: "user", TenantID: "tenant", MembershipID: "membership", SessionID: "session", Roles: []string{"member"}, ExpiresAt: now.Add(time.Hour)}
	boundary := TrustBoundary{Resolver: resolverStub{principal: principal}, SigningKey: privateKey, SigningKeyID: "key", Issuer: "gateway", Audience: "identity-service", AudienceForRequest: func(request *http.Request) string {
		if request.URL.Path == "/v1/realtime" || request.URL.Path == "/v1/events" {
			return "realtime-gateway"
		}
		return "identity-service"
	}, TTL: time.Minute, FingerprintPepper: bytes.Repeat([]byte{0x51}, 32), Now: func() time.Time { return now }}
	for _, test := range []struct{ path, audience string }{{"/v1/account", "identity-service"}, {"/v1/realtime", "realtime-gateway"}, {"/v1/events", "realtime-gateway"}} {
		request := httptest.NewRequest(http.MethodGet, "https://api.lites.dev"+test.path, nil)
		request.AddCookie(&http.Cookie{Name: session.CookieName, Value: "opaque"})
		recorder := httptest.NewRecorder()
		boundary.Wrap(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			verifier := trustedcontext.Verifier{Issuer: "gateway", Audience: test.audience, Keys: map[string]ed25519.PublicKey{"key": publicKey}, MaximumTTL: 5 * time.Minute}
			if _, verifyErr := verifier.Verify(request.Header.Get(TrustedContextHeader), now); verifyErr != nil {
				t.Errorf("path %s audience verification: %v", test.path, verifyErr)
			}
			writer.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("path %s status = %d", test.path, recorder.Code)
		}
	}
}

func TestGatewayRejectsMissingRevokedAndExpiredSessions(t *testing.T) {
	_, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Unix(1_800_000_000, 0)
	tests := []struct {
		name     string
		cookie   bool
		resolver resolverStub
	}{
		{name: "missing cookie", resolver: resolverStub{}},
		{name: "revoked", cookie: true, resolver: resolverStub{err: session.ErrUnauthenticated}},
		{name: "expired", cookie: true, resolver: resolverStub{principal: session.Principal{UserID: "u", TenantID: "t", MembershipID: "m", SessionID: "s", Roles: []string{"member"}, ExpiresAt: now.Add(-time.Second)}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			boundary := TrustBoundary{Resolver: test.resolver, SigningKey: privateKey, SigningKeyID: "key", Issuer: "gateway", Audience: "identity", TTL: time.Minute, FingerprintPepper: bytes.Repeat([]byte{0x51}, 32), Now: func() time.Time { return now }}
			request := httptest.NewRequest(http.MethodGet, "https://api.lites.dev/v1/account", nil)
			if test.cookie {
				request.AddCookie(&http.Cookie{Name: session.CookieName, Value: "opaque"})
			}
			recorder := httptest.NewRecorder()
			boundary.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unauthenticated request reached upstream") })).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d", recorder.Code)
			}
			if recorder.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("content-type=%s", recorder.Header().Get("Content-Type"))
			}
			var value problem.Value
			if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
				t.Fatal(err)
			}
			if value.Code != "authentication_required" {
				t.Fatalf("problem=%#v", value)
			}
		})
	}
}

func TestGatewayRequiresCSRFAndBindsUnsafeRequest(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pepper := bytes.Repeat([]byte{0x71}, 32)
	csrf, err := session.NewFrom(bytes.NewReader(bytes.Repeat([]byte{0x17}, 32)), pepper)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	principal := session.Principal{UserID: "u", TenantID: "t", MembershipID: "m", SessionID: "s", Roles: []string{"member"}, CSRFSecretHash: csrf.Digest[:], ExpiresAt: now.Add(time.Hour)}
	newBoundary := func() TrustBoundary {
		return TrustBoundary{Resolver: resolverStub{principal: principal}, SigningKey: privateKey, SigningKeyID: "key", Issuer: "gateway", Audience: "identity", TTL: time.Minute, CSRFPepper: pepper, FingerprintPepper: bytes.Repeat([]byte{0x51}, 32), Random: bytes.NewReader(bytes.Repeat([]byte{0x31}, 64)), Now: func() time.Time { return now }}
	}
	verifier := trustedcontext.Verifier{Issuer: "gateway", Audience: "identity", Keys: map[string]ed25519.PublicKey{"key": publicKey}, MaximumTTL: 5 * time.Minute, ClockSkew: time.Second}
	request := httptest.NewRequest(http.MethodPatch, "https://api.lites.dev/v1/account?view=full", nil)
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: "opaque"})
	request.Header.Set(CSRFHeader, csrf.Raw)
	recorder := httptest.NewRecorder()
	newBoundary().Wrap(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get(CSRFHeader) != "" {
			t.Fatal("CSRF secret forwarded downstream")
		}
		claims, err := verifier.VerifyRequest(request.Header.Get(TrustedContextHeader), now, request.Header.Get(RequestIDHeader), http.MethodPatch, "/v1/account?view=full", true)
		if err != nil {
			t.Fatal(err)
		}
		if !claims.CSRFVerified {
			t.Fatal("CSRF verification not bound")
		}
		writer.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, raw := range []string{"", "forged"} {
		request = httptest.NewRequest(http.MethodPost, "https://api.lites.dev/v1/account", nil)
		request.AddCookie(&http.Cookie{Name: session.CookieName, Value: "opaque"})
		if raw != "" {
			request.Header.Set(CSRFHeader, raw)
		}
		recorder = httptest.NewRecorder()
		newBoundary().Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid CSRF reached upstream") })).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("raw=%q status=%d", raw, recorder.Code)
		}
		var value problem.Value
		if err = json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if value.Code != "permission_denied" {
			t.Fatalf("problem=%#v", value)
		}
	}
}

func TestResolverErrorDoesNotLeak(t *testing.T) {
	_, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	boundary := TrustBoundary{Resolver: resolverStub{err: errors.New("database details")}, SigningKey: privateKey, SigningKeyID: "key", Issuer: "gateway", Audience: "identity", TTL: time.Minute, FingerprintPepper: bytes.Repeat([]byte{0x51}, 32)}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: "opaque"})
	recorder := httptest.NewRecorder()
	boundary.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || recorder.Body.String() == "database details" {
		t.Fatalf("unsafe response: %s", recorder.Body.String())
	}
}

func TestPublicIdentityRouteRequiresAllowedBrowserOriginAndCarriesNoIdentity(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	newBoundary := func() TrustBoundary {
		return TrustBoundary{SigningKey: privateKey, SigningKeyID: "key", Issuer: "gateway", Audience: "identity", TTL: time.Minute, FingerprintPepper: bytes.Repeat([]byte{0x51}, 32), PublicOrigins: []string{"https://app.lites.dev"}, RoutePolicy: IdentityRoutePolicy, Random: bytes.NewReader(bytes.Repeat([]byte{0x41}, 64)), Now: func() time.Time { return now }}
	}
	verifier := trustedcontext.Verifier{Issuer: "gateway", Audience: "identity", Keys: map[string]ed25519.PublicKey{"key": publicKey}, MaximumTTL: 5 * time.Minute}
	request := httptest.NewRequest(http.MethodPost, "https://api.lites.dev/v1/auth/login", nil)
	request.Header.Set("Origin", "https://app.lites.dev")
	request.Header.Set("Sec-Fetch-Site", "same-site")
	request.Header.Set("X-Lites-User-ID", "attacker")
	request.Header.Set(CSRFHeader, "attacker-csrf")
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: "attacker-session"})
	recorder := httptest.NewRecorder()
	newBoundary().Wrap(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, err := verifier.VerifyRequest(request.Header.Get(TrustedContextHeader), now, request.Header.Get(RequestIDHeader), http.MethodPost, "/v1/auth/login", true)
		if err != nil {
			t.Fatal(err)
		}
		if claims.PrincipalKind != trustedcontext.PublicRequest || claims.SubjectID != "" || claims.TenantID != "" || len(claims.Roles) != 0 {
			t.Fatalf("public claims contain identity: %#v", claims)
		}
		if request.Header.Get(CSRFHeader) != "" || request.Header.Get("X-Lites-User-ID") != "" {
			t.Fatal("untrusted public headers reached service")
		}
		if request.Header.Get("Cookie") != "" || request.Header.Get("User-Agent") != "" || request.Header.Get("Authorization") != "" {
			t.Fatal("raw browser credentials or fingerprint reached service")
		}
		writer.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	for _, candidate := range []struct{ origin, fetchSite string }{
		{},
		{origin: "https://evil.example"},
		{origin: "https://app.lites.dev", fetchSite: "cross-site"},
		{origin: "http://app.lites.dev", fetchSite: "same-site"},
	} {
		request = httptest.NewRequest(http.MethodPost, "https://api.lites.dev/v1/auth/register", nil)
		request.Header.Set("Origin", candidate.origin)
		request.Header.Set("Sec-Fetch-Site", candidate.fetchSite)
		recorder = httptest.NewRecorder()
		newBoundary().Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid origin reached service") })).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("origin=%q fetch-site=%q status=%d", candidate.origin, candidate.fetchSite, recorder.Code)
		}
	}
}

func TestEngineeringBoundaryAllowsOnlyExplicitHTTPLoopbackOrigin(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	newBoundary := func(allow bool) TrustBoundary {
		return TrustBoundary{SigningKey: privateKey, SigningKeyID: "key", Issuer: "gateway", Audience: "identity", TTL: time.Minute, FingerprintPepper: bytes.Repeat([]byte{0x51}, 32), PublicOrigins: []string{"http://127.0.0.1:3118", "http://app.lites.test"}, AllowInsecureLoopbackOrigins: allow, RoutePolicy: IdentityRoutePolicy}
	}
	assertStatus := func(boundary TrustBoundary, origin string, want int) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "https://api.lites.dev/v1/onboarding-sessions", nil)
		request.Header.Set("Origin", origin)
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		recorder := httptest.NewRecorder()
		boundary.Wrap(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })).ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("allow=%t origin=%q status=%d want=%d body=%s", boundary.AllowInsecureLoopbackOrigins, origin, recorder.Code, want, recorder.Body.String())
		}
	}
	assertStatus(newBoundary(true), "http://127.0.0.1:3118", http.StatusNoContent)
	assertStatus(newBoundary(false), "http://127.0.0.1:3118", http.StatusForbidden)
	assertStatus(newBoundary(true), "http://app.lites.test", http.StatusForbidden)
	assertStatus(newBoundary(true), "http://localhost:3118", http.StatusForbidden)
}

func TestIdentityRoutePolicyFailsClosedForUnknownRoute(t *testing.T) {
	publicStatus := httptest.NewRequest(http.MethodGet, "https://api.lites.dev/v1/public/status", nil)
	if got := IdentityRoutePolicy(publicStatus); got != PublicAuthentication {
		t.Fatalf("public status policy=%d want=%d", got, PublicAuthentication)
	}
	publicStatus.Method = http.MethodPost
	if got := IdentityRoutePolicy(publicStatus); got != AuthenticationRequired {
		t.Fatalf("public status mutation policy=%d want=%d", got, AuthenticationRequired)
	}
	for _, test := range []struct {
		path string
		want AuthenticationPolicy
	}{
		{path: "/v1/auth/register", want: PublicAuthentication},
		{path: "/v1/catalog/roles", want: PublicOrAnonymousOrSession},
		{path: "/v1/onboarding-sessions", want: PublicOrAnonymousOrSession},
		{path: "/v1/onboarding-sessions/session-1", want: AnonymousOrSession},
		{path: "/v1/onboarding-sessions/session-1/route-preview", want: AnonymousOrSession},
		{path: "/v1/onboarding-sessions/session-1/claim", want: AuthenticatedWithAnonymous},
		{path: "/v1/onboarding-sessions/session-1/claim/extra", want: AuthenticationRequired},
		{path: "/v1/onboarding-sessions/session-1/unknown", want: AuthenticationRequired},
		{path: "/v1/auth/login/extra", want: AuthenticationRequired},
		{path: "/v1/account", want: AuthenticationRequired},
	} {
		method := http.MethodPost
		if test.path == "/v1/catalog/roles" {
			method = http.MethodGet
		}
		request := httptest.NewRequest(method, "https://api.lites.dev"+test.path, nil)
		if got := IdentityRoutePolicy(request); got != test.want {
			t.Fatalf("path=%s policy=%d want=%d", test.path, got, test.want)
		}
	}
}

func TestAnonymousRouteIssuesIsolatedTrustedContextAndRequiresBoundCSRF(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	handle := "LITES-A1.key.opaque.exp.signature"
	csrfKey := bytes.Repeat([]byte{0x62}, 32)
	csrf, err := anonymoussession.CSRF(handle, csrfKey)
	if err != nil {
		t.Fatal(err)
	}
	principal := anonymoussession.Principal{AnonymousSubjectID: "anonymous-subject", UserID: "ephemeral-user", TenantID: "anonymous-system", ExpiresAt: now.Add(time.Hour)}
	boundary := TrustBoundary{AnonymousResolver: anonymousResolverStub{principal: principal}, AnonymousCSRFKey: csrfKey, SigningKey: privateKey, SigningKeyID: "key", Issuer: "gateway", Audience: "identity", TTL: time.Minute, FingerprintPepper: bytes.Repeat([]byte{0x51}, 32), RoutePolicy: IdentityRoutePolicy, Random: bytes.NewReader(bytes.Repeat([]byte{0x41}, 64)), Now: func() time.Time { return now }}
	verifier := trustedcontext.Verifier{Issuer: "gateway", Audience: "identity", Keys: map[string]ed25519.PublicKey{"key": publicKey}, MaximumTTL: 5 * time.Minute}
	request := httptest.NewRequest(http.MethodPatch, "https://api.lites.dev/v1/onboarding-sessions/session-1", nil)
	request.AddCookie(&http.Cookie{Name: anonymoussession.CookieName, Value: handle})
	request.AddCookie(&http.Cookie{Name: "preference", Value: "allowed"})
	request.Header.Set(CSRFHeader, csrf)
	recorder := httptest.NewRecorder()
	boundary.Wrap(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, verifyErr := verifier.VerifyRequest(request.Header.Get(TrustedContextHeader), now, request.Header.Get(RequestIDHeader), http.MethodPatch, "/v1/onboarding-sessions/session-1", true)
		if verifyErr != nil {
			t.Fatal(verifyErr)
		}
		if claims.PrincipalKind != trustedcontext.AnonymousUser || claims.SubjectID != "ephemeral-user" || claims.TenantID != "anonymous-system" || claims.AnonymousSubjectID != "anonymous-subject" || len(claims.Roles) != 1 || claims.Roles[0] != "anonymous_preview" || claims.MembershipID != "" || claims.SessionID != "" {
			t.Fatalf("claims=%#v", claims)
		}
		if _, cookieErr := request.Cookie(anonymoussession.CookieName); cookieErr == nil || request.Header.Get(CSRFHeader) != "" {
			t.Fatal("anonymous browser credential reached upstream")
		}
		if preference, cookieErr := request.Cookie("preference"); cookieErr != nil || preference.Value != "allowed" {
			t.Fatal("non-sensitive cookie was removed")
		}
		writer.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodPatch, "https://api.lites.dev/v1/onboarding-sessions/session-1", nil)
	request.AddCookie(&http.Cookie{Name: anonymoussession.CookieName, Value: handle})
	request.Header.Set(CSRFHeader, "attacker")
	recorder = httptest.NewRecorder()
	boundary.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid csrf reached upstream") })).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("invalid csrf status=%d", recorder.Code)
	}
}

func TestClaimRouteBindsVerifiedSessionToResolvedAnonymousSubject(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	csrfPepper := bytes.Repeat([]byte{0x65}, 32)
	csrf, err := session.NewFrom(bytes.NewReader(bytes.Repeat([]byte{0x66}, 32)), csrfPepper)
	if err != nil {
		t.Fatal(err)
	}
	formal := session.Principal{UserID: "formal-user", TenantID: "personal-tenant", MembershipID: "membership", SessionID: "session", Roles: []string{"owner"}, CSRFSecretHash: csrf.Digest[:], ExpiresAt: now.Add(time.Hour)}
	anonymous := anonymoussession.Principal{AnonymousSubjectID: "anonymous-subject", UserID: "ephemeral-user", TenantID: "anonymous-system", ExpiresAt: now.Add(90 * time.Second)}
	boundary := TrustBoundary{Resolver: resolverStub{principal: formal}, AnonymousResolver: anonymousResolverStub{principal: anonymous}, SigningKey: privateKey, SigningKeyID: "key", Issuer: "gateway", Audience: "identity", TTL: 2 * time.Minute, CSRFPepper: csrfPepper, FingerprintPepper: bytes.Repeat([]byte{0x67}, 32), RoutePolicy: IdentityRoutePolicy, Random: bytes.NewReader(bytes.Repeat([]byte{0x68}, 64)), Now: func() time.Time { return now }}
	verifier := trustedcontext.Verifier{Issuer: "gateway", Audience: "identity", Keys: map[string]ed25519.PublicKey{"key": publicKey}, MaximumTTL: time.Hour}
	request := httptest.NewRequest(http.MethodPost, "https://api.lites.dev/v1/onboarding-sessions/onboarding-1/claim", nil)
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: "formal-handle"})
	request.AddCookie(&http.Cookie{Name: anonymoussession.CookieName, Value: "anonymous-handle"})
	request.AddCookie(&http.Cookie{Name: anonymoussession.CSRFCookieName, Value: "anonymous-csrf"})
	request.Header.Set(CSRFHeader, csrf.Raw)
	recorder := httptest.NewRecorder()
	boundary.Wrap(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, verifyErr := verifier.VerifyRequest(request.Header.Get(TrustedContextHeader), now, request.Header.Get(RequestIDHeader), http.MethodPost, "/v1/onboarding-sessions/onboarding-1/claim", true)
		if verifyErr != nil {
			t.Fatal(verifyErr)
		}
		if claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID != formal.UserID || claims.TenantID != formal.TenantID || claims.AnonymousSubjectID != anonymous.AnonymousSubjectID || claims.ExpiresAt != anonymous.ExpiresAt.Unix() {
			t.Fatalf("claims=%#v", claims)
		}
		if request.Header.Get("Cookie") != "" || request.Header.Get(CSRFHeader) != "" {
			t.Fatal("claim credentials reached upstream")
		}
		writer.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "https://api.lites.dev/v1/onboarding-sessions/onboarding-1/claim", nil)
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: "formal-handle"})
	request.Header.Set(CSRFHeader, csrf.Raw)
	recorder = httptest.NewRecorder()
	boundary.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("claim without anonymous handle reached upstream") })).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing anonymous handle status=%d", recorder.Code)
	}
}
