package serviceauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
)

func TestMiddlewareRequiresMTLSAndRequestBoundContext(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	verifier := trustedcontext.Verifier{Issuer: "gateway", Audience: "identity", Keys: map[string]ed25519.PublicKey{"key": publicKey}, MaximumTTL: 5 * time.Minute, ClockSkew: time.Second}
	middleware := Middleware{Verifier: verifier, Now: func() time.Time { return now }, RequireVerifiedClientCertificate: true}
	newToken := func(method, target string, csrf bool) string {
		token, err := trustedcontext.Sign(trustedcontext.Claims{Issuer: "gateway", Audience: "identity", SubjectID: "u", TenantID: "t", MembershipID: "m", SessionID: "s", Roles: []string{"member"}, RequestID: "request-1", RequestMethod: method, RequestTarget: target, CSRFVerified: csrf, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Nonce: "nonce"}, "key", privateKey, 5*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	application := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get(transport.TrustedContextHeader) != "" {
			t.Fatal("bearer context reached application")
		}
		claims, ok := ClaimsFromContext(request.Context())
		if !ok || claims.SubjectID != "u" || claims.TenantID != "t" {
			t.Fatalf("claims=%#v ok=%v", claims, ok)
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPatch, "https://identity.internal/v1/account", nil)
	request.Header.Set(transport.RequestIDHeader, "request-1")
	request.Header.Set(transport.TrustedContextHeader, newToken(http.MethodPatch, "/v1/account", true))
	request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}
	recorder := httptest.NewRecorder()
	middleware.Wrap(application).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	tests := []struct {
		name           string
		tls            bool
		method, target string
		csrf           bool
	}{
		{name: "missing mTLS", method: http.MethodGet, target: "/v1/account"},
		{name: "method replay", tls: true, method: http.MethodGet, target: "/v1/account"},
		{name: "target replay", tls: true, method: http.MethodPatch, target: "/v1/other", csrf: true},
		{name: "missing CSRF proof", tls: true, method: http.MethodPatch, target: "/v1/account"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPatch, "https://identity.internal/v1/account", nil)
			request.Header.Set(transport.RequestIDHeader, "request-1")
			request.Header.Set(transport.TrustedContextHeader, newToken(test.method, test.target, test.csrf))
			if test.tls {
				request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}
			}
			recorder := httptest.NewRecorder()
			middleware.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid context reached application") })).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d", recorder.Code)
			}
			if request.Header.Get(transport.TrustedContextHeader) != "" {
				t.Fatal("rejected bearer context was not removed")
			}
		})
	}
}
