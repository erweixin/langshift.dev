package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

type sessionServiceStub struct {
	reauthenticate func(context.Context, ReauthenticateCommand) (ReauthenticationResult, error)
	logout         func(context.Context, LogoutCommand) (LogoutResult, error)
	list           func(context.Context, SessionsQuery) (SessionsPage, error)
	revoke         func(context.Context, RevokeSessionCommand) (SessionMutationResult, error)
	revokeOthers   func(context.Context, RevokeOtherSessionsCommand) (SessionMutationResult, error)
}

func (stub sessionServiceStub) Reauthenticate(ctx context.Context, command ReauthenticateCommand) (ReauthenticationResult, error) {
	if stub.reauthenticate == nil {
		return ReauthenticationResult{}, errors.New("unexpected reauthentication")
	}
	return stub.reauthenticate(ctx, command)
}

func (stub sessionServiceStub) Logout(ctx context.Context, command LogoutCommand) (LogoutResult, error) {
	if stub.logout == nil {
		return LogoutResult{}, errors.New("unexpected logout")
	}
	return stub.logout(ctx, command)
}
func (stub sessionServiceStub) ListSessions(ctx context.Context, query SessionsQuery) (SessionsPage, error) {
	if stub.list == nil {
		return SessionsPage{}, errors.New("unexpected list sessions")
	}
	return stub.list(ctx, query)
}
func (stub sessionServiceStub) RevokeSession(ctx context.Context, command RevokeSessionCommand) (SessionMutationResult, error) {
	if stub.revoke == nil {
		return SessionMutationResult{}, errors.New("unexpected revoke session")
	}
	return stub.revoke(ctx, command)
}
func (stub sessionServiceStub) RevokeOtherSessions(ctx context.Context, command RevokeOtherSessionsCommand) (SessionMutationResult, error) {
	if stub.revokeOthers == nil {
		return SessionMutationResult{}, errors.New("unexpected revoke other sessions")
	}
	return stub.revokeOthers(ctx, command)
}

func TestReauthenticationUsesTrustedSessionAndNeverReturnsPassword(t *testing.T) {
	service := sessionServiceStub{reauthenticate: func(_ context.Context, command ReauthenticateCommand) (ReauthenticationResult, error) {
		if command.UserID != "user-auth" || command.TenantID != "tenant-auth" || command.MembershipID != "membership-auth" || command.SessionID != "session-auth" || command.ClientRequestID != "client-reauth-001" || command.IdempotencyKey != "reauth-key-00000001" || command.Password != "current password value" {
			t.Fatalf("command=%#v", command)
		}
		return ReauthenticationResult{SessionID: command.SessionID, SessionVersion: 4, ReauthenticatedAt: apiTestNow, ValidUntil: apiTestNow.Add(5 * time.Minute)}, nil
	}}
	recorder := serveAuthenticated(t, service, http.MethodPost, "/v1/auth/reauthentication", "reauth-key-00000001", "", `{"request_id":"client-reauth-001","password":"current password value"}`)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"session_version":4`) || strings.Contains(recorder.Body.String(), "current password value") || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestAuthenticatedSessionListUsesTrustedPrincipalAndOpaqueCursor(t *testing.T) {
	next := "signed.next.cursor"
	service := sessionServiceStub{list: func(_ context.Context, query SessionsQuery) (SessionsPage, error) {
		if query.UserID != "user-auth" || query.TenantID != "tenant-auth" || query.SessionID != "session-auth" || query.Cursor != "signed.cursor" || query.Limit != 50 || len(query.ClientIPHash) != 32 {
			t.Fatalf("query=%#v", query)
		}
		return SessionsPage{Items: []SessionItem{{ID: "session-auth", ActiveTenantID: "tenant-auth", Version: 2, CreatedAt: apiTestNow.Add(-time.Hour), UpdatedAt: apiTestNow, LastSeenAt: apiTestNow, ExpiresAt: apiTestNow.Add(time.Hour), Current: true}}, NextCursor: &next}, nil
	}}
	recorder := serveAuthenticated(t, service, http.MethodGet, "/v1/auth/sessions?cursor=signed.cursor", "", "", "")
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "ip_hash") || strings.Contains(recorder.Body.String(), "user_agent_hash") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Items []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || len(response.Items) != 1 || !response.Items[0].Current || response.NextCursor == nil || *response.NextCursor != next {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestLogoutRevokesTrustedCurrentSessionAndClearsBothCookies(t *testing.T) {
	service := sessionServiceStub{logout: func(_ context.Context, command LogoutCommand) (LogoutResult, error) {
		if command.SessionID != "session-auth" || command.ClientRequestID != "client-logout-001" || command.IdempotencyKey != "logout-key-00000001" || command.AllDevices {
			t.Fatalf("command=%#v", command)
		}
		return LogoutResult{RevokedSessionCount: 1}, nil
	}}
	recorder := serveAuthenticated(t, service, http.MethodPost, "/v1/auth/logout", "logout-key-00000001", "", `{"request_id":"client-logout-001","session_id":"session-auth","all_devices":false}`)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"revoked_session_count":1`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 || cookies[0].Name != session.CookieName || cookies[0].MaxAge != -1 || cookies[1].Name != session.CSRFCookieName || cookies[1].MaxAge != -1 {
		t.Fatalf("cookies=%#v", cookies)
	}
}

func TestRevokeSessionRequiresMatchingBodyAndIfMatch(t *testing.T) {
	called := false
	service := sessionServiceStub{revoke: func(_ context.Context, command RevokeSessionCommand) (SessionMutationResult, error) {
		called = true
		if command.TargetSessionID != "other-session" || command.ExpectedVersion != 7 || command.ReasonCode != "user_revoked" {
			t.Fatalf("command=%#v", command)
		}
		return SessionMutationResult{ID: "other-session", Version: 8, Status: "revoked", UpdatedAt: apiTestNow}, nil
	}}
	body := `{"request_id":"client-revoke-001","expected_session_version":7}`
	recorder := serveAuthenticated(t, service, http.MethodDelete, "/v1/auth/sessions/other-session", "revoke-key-00000001", `"7"`, body)
	if recorder.Code != http.StatusOK || !called || strings.Contains(strings.Join(recorder.Header().Values("Set-Cookie"), ""), session.CookieName) {
		t.Fatalf("status=%d called=%v headers=%v body=%s", recorder.Code, called, recorder.Header(), recorder.Body.String())
	}
	called = false
	recorder = serveAuthenticated(t, service, http.MethodDelete, "/v1/auth/sessions/other-session", "revoke-key-00000001", `"6"`, body)
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("mismatch status=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
	}
	recorder = serveAuthenticated(t, service, http.MethodDelete, "/v1/auth/sessions/other-session", "revoke-key-00000001", "", body)
	if recorder.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing precondition status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRevokeOthersRejectsClientSessionSubstitution(t *testing.T) {
	service := sessionServiceStub{revokeOthers: func(context.Context, RevokeOtherSessionsCommand) (SessionMutationResult, error) {
		t.Fatal("substituted current session reached service")
		return SessionMutationResult{}, nil
	}}
	recorder := serveAuthenticated(t, service, http.MethodDelete, "/v1/auth/sessions?scope=others", "others-key-00000001", "", `{"request_id":"client-others-001","current_session_id":"attacker-session"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func serveAuthenticated(t *testing.T, sessions SessionService, method, target, idempotencyKey, ifMatch, body string) *httptest.ResponseRecorder {
	return serveAuthenticatedHandler(t, Handler{Sessions: sessions, Now: func() time.Time { return apiTestNow }}, method, target, idempotencyKey, ifMatch, body)
}

func serveAuthenticatedHandler(t *testing.T, handler Handler, method, target, idempotencyKey, ifMatch, body string) *httptest.ResponseRecorder {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	claims := trustedcontext.Claims{PrincipalKind: trustedcontext.AuthenticatedUser, Issuer: "gateway", Audience: "identity", SubjectID: "user-auth", TenantID: "tenant-auth", MembershipID: "membership-auth", SessionID: "session-auth", Roles: []string{"member"}, RequestID: "server-request-id", RequestMethod: method, RequestTarget: target, ClientIPHash: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32)), UserAgentHash: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x62}, 32)), CSRFVerified: true, IssuedAt: apiTestNow.Unix(), ExpiresAt: apiTestNow.Add(time.Minute).Unix(), Nonce: "nonce-authenticated"}
	token, err := trustedcontext.Sign(claims, "key", privateKey, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, "https://identity.internal"+target, strings.NewReader(body))
	request.Header.Set(transport.RequestIDHeader, claims.RequestID)
	request.Header.Set(transport.TrustedContextHeader, token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set(transport.IdempotencyHeader, idempotencyKey)
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}
	recorder := httptest.NewRecorder()
	middleware := serviceauth.Middleware{Verifier: trustedcontext.Verifier{Issuer: "gateway", Audience: "identity", Keys: map[string]ed25519.PublicKey{"key": publicKey}, MaximumTTL: 5 * time.Minute}, Now: func() time.Time { return apiTestNow }, RequireVerifiedClientCertificate: true}
	middleware.Wrap(handler).ServeHTTP(recorder, request)
	return recorder
}
