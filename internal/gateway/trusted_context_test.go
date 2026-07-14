package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/security/trustedcontext"
)

type resolverStub struct {
	principal Principal
	err       error
}

func (resolver resolverStub) Resolve(context.Context, string) (Principal, error) {
	return resolver.principal, resolver.err
}

func TestGatewayStripsForgedIdentityAndIssuesServerContext(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	principal := Principal{UserID: "user-real", TenantID: "tenant-real", MembershipID: "membership-real", SessionID: "session-real", Roles: []string{"member"}, ExpiresAt: now.Add(time.Hour)}
	boundary := TrustBoundary{Resolver: resolverStub{principal: principal}, SigningKey: privateKey, SigningKeyID: "gateway-key", Issuer: "lites-gateway", Audience: "identity-service", TTL: 2 * time.Minute, Now: func() time.Time { return now }}
	verifier := trustedcontext.Verifier{Issuer: "lites-gateway", Audience: "identity-service", Keys: map[string]ed25519.PublicKey{"gateway-key": publicKey}, MaximumTTL: 5 * time.Minute, ClockSkew: time.Second}
	upstream := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		for _, name := range []string{"X-Lites-User-ID", "X-Lites-Tenant-ID", "X-Lites-Roles"} {
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
		writer.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "https://api.lites.dev/v1/account", nil)
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: "opaque-session"})
	request.Header.Set(TrustedContextHeader, "attacker-token")
	request.Header.Set("X-Lites-User-ID", "attacker")
	request.Header.Set("X-Lites-Tenant-ID", "victim")
	request.Header.Set("X-Lites-Roles", "owner")
	recorder := httptest.NewRecorder()
	boundary.Wrap(upstream).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
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
		{name: "revoked", cookie: true, resolver: resolverStub{err: ErrUnauthenticated}},
		{name: "expired", cookie: true, resolver: resolverStub{principal: Principal{UserID: "u", TenantID: "t", MembershipID: "m", SessionID: "s", Roles: []string{"member"}, ExpiresAt: now.Add(-time.Second)}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			boundary := TrustBoundary{Resolver: test.resolver, SigningKey: privateKey, SigningKeyID: "key", Issuer: "gateway", Audience: "identity", TTL: time.Minute, Now: func() time.Time { return now }}
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
			if value.Code != "unauthenticated" {
				t.Fatalf("problem=%#v", value)
			}
		})
	}
}

func TestResolverErrorDoesNotLeak(t *testing.T) {
	_, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	boundary := TrustBoundary{Resolver: resolverStub{err: errors.New("database details")}, SigningKey: privateKey, SigningKeyID: "key", Issuer: "gateway", Audience: "identity", TTL: time.Minute}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: "opaque"})
	recorder := httptest.NewRecorder()
	boundary.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || recorder.Body.String() == "database details" {
		t.Fatalf("unsafe response: %s", recorder.Body.String())
	}
}
