package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

type roleCatalogStub struct {
	query  RoleCatalogQuery
	result RoleCatalogResult
	err    error
}

func (stub *roleCatalogStub) ListRoles(_ context.Context, query RoleCatalogQuery) (RoleCatalogResult, error) {
	stub.query = query
	return stub.result, stub.err
}

func TestRoleCatalogUsesPublicTenantAndLocalizedRelease(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := &roleCatalogStub{result: RoleCatalogResult{ReleaseVersion: "1.0.0", ContentRootSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Locale: "zh-CN", Items: []RoleCatalogItem{{ID: "00000000-0000-4000-8000-000000000001", Slug: "role_b", Revision: 1, Status: "active", Name: "乙"}, {ID: "00000000-0000-4000-8000-000000000002", Slug: "role_a", Revision: 1, Status: "active", Name: "甲"}}, Rubrics: []RubricCatalogItem{{ID: "00000000-0000-4000-8000-000000000003", Slug: "rubric_writing", Revision: 1, Status: "active", PracticeKind: "writing"}}}}
	handler, privateKey := roleCatalogAuthenticatedHandler(t, now, RoleCatalogHandler{Service: store, PublicTenantID: "00000000-0000-4000-8000-000000000099"})
	request := roleCatalogRequest(t, now, privateKey, trustedcontext.Claims{PrincipalKind: trustedcontext.PublicRequest}, "/v1/catalog/roles?locale=zh-CN")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != roleCatalogMediaType || recorder.Header().Get("Cache-Control") != "public, max-age=300, must-revalidate" || recorder.Header().Get("ETag") == "" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if store.query.TenantID != "00000000-0000-4000-8000-000000000099" || store.query.Locale != "zh-CN" {
		t.Fatalf("query=%#v", store.query)
	}
	if body := recorder.Body.String(); len(body) == 0 || body[0] != '{' {
		t.Fatalf("body=%q", body)
	}
}

func TestRoleCatalogUsesAuthenticatedTenantWithoutPublicCaching(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := &roleCatalogStub{result: RoleCatalogResult{ReleaseVersion: "1", ContentRootSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Locale: "en", Items: []RoleCatalogItem{}, Rubrics: []RubricCatalogItem{}}}
	handler, privateKey := roleCatalogAuthenticatedHandler(t, now, RoleCatalogHandler{Service: store, PublicTenantID: "public"})
	claims := trustedcontext.Claims{PrincipalKind: trustedcontext.AuthenticatedUser, SubjectID: "user", TenantID: "tenant", MembershipID: "membership", SessionID: "session", Roles: []string{"member"}}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, roleCatalogRequest(t, now, privateKey, claims, "/v1/catalog/roles"))
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "private, no-store" || store.query.TenantID != "tenant" {
		t.Fatalf("status=%d headers=%v query=%#v body=%s", recorder.Code, recorder.Header(), store.query, recorder.Body.String())
	}
}

func TestRoleCatalogRejectsUnknownQuery(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := &roleCatalogStub{}
	handler, privateKey := roleCatalogAuthenticatedHandler(t, now, RoleCatalogHandler{Service: store, PublicTenantID: "public"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, roleCatalogRequest(t, now, privateKey, trustedcontext.Claims{PrincipalKind: trustedcontext.PublicRequest}, "/v1/catalog/roles?cursor=x"))
	if recorder.Code != http.StatusBadRequest || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func roleCatalogAuthenticatedHandler(t *testing.T, now time.Time, handler http.Handler) (http.Handler, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return serviceauth.Middleware{Verifier: trustedcontext.Verifier{Issuer: "gateway", Audience: "product", Keys: map[string]ed25519.PublicKey{"key": publicKey}, MaximumTTL: 2 * time.Minute}, Now: func() time.Time { return now }}.Wrap(handler), privateKey
}

func roleCatalogRequest(t *testing.T, now time.Time, key ed25519.PrivateKey, claims trustedcontext.Claims, target string) *http.Request {
	t.Helper()
	claims.Issuer, claims.Audience, claims.RequestID = "gateway", "product", "catalog-request"
	claims.RequestMethod, claims.RequestTarget = http.MethodGet, target
	claims.ClientIPHash = "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	claims.UserAgentHash = "YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmI"
	claims.IssuedAt, claims.ExpiresAt, claims.Nonce = now.Unix(), now.Add(time.Minute).Unix(), "nonce"
	token, err := trustedcontext.Sign(claims, "key", key, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://product.internal"+target, nil)
	request.Header.Set(transport.RequestIDHeader, claims.RequestID)
	request.Header.Set(transport.TrustedContextHeader, token)
	return request
}
