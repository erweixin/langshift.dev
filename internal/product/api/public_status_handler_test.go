package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
	"github.com/langshift/lites/internal/statuspage"
)

type publicStatusReaderStub struct {
	document statuspage.Document
	etag     string
	err      error
}

func (stub publicStatusReaderStub) Read(context.Context) (statuspage.Document, string, error) {
	return stub.document, stub.etag, stub.err
}

func TestPublicStatusReturnsVerifiedSnapshotWithBoundedCaching(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	document := statuspage.Document{SchemaVersion: 1, Overall: statuspage.Operational, GeneratedAt: now, ValidUntil: now.Add(time.Minute), Components: []statuspage.Component{{ID: "api", Name: "API", State: statuspage.Operational}}, Incidents: []statuspage.Incident{}}
	handler, privateKey := publicStatusAuthenticatedHandler(t, now, PublicStatusHandler{Reader: publicStatusReaderStub{document: document, etag: `"digest"`}})
	request := publicStatusRequest(t, now, privateKey, http.MethodGet, "/v1/public/status")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != publicStatusMediaType || recorder.Header().Get("Cache-Control") != "public, max-age=15, must-revalidate" || recorder.Header().Get("ETag") != `"digest"` {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestPublicStatusFailsClosedWhenSnapshotUnavailable(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	handler, privateKey := publicStatusAuthenticatedHandler(t, now, PublicStatusHandler{Reader: publicStatusReaderStub{err: errors.New("unavailable")}})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, publicStatusRequest(t, now, privateKey, http.MethodGet, "/v1/public/status"))
	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func publicStatusAuthenticatedHandler(t *testing.T, now time.Time, handler http.Handler) (http.Handler, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return serviceauth.Middleware{Verifier: trustedcontext.Verifier{Issuer: "gateway", Audience: "product", Keys: map[string]ed25519.PublicKey{"gateway-1": publicKey}, MaximumTTL: 2 * time.Minute}, Now: func() time.Time { return now }}.Wrap(handler), privateKey
}

func publicStatusRequest(t *testing.T, now time.Time, privateKey ed25519.PrivateKey, method, target string) *http.Request {
	t.Helper()
	claims := trustedcontext.Claims{PrincipalKind: trustedcontext.PublicRequest, Issuer: "gateway", Audience: "product", RequestID: "status-request", RequestMethod: method, RequestTarget: target, ClientIPHash: "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE", UserAgentHash: "YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmI", CSRFVerified: true, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Nonce: "status-nonce"}
	token, err := trustedcontext.Sign(claims, "gateway-1", privateKey, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, "https://product.internal"+target, nil)
	request.Header.Set(transport.RequestIDHeader, claims.RequestID)
	request.Header.Set(transport.TrustedContextHeader, token)
	return request
}
