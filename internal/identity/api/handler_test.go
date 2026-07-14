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
	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

var apiTestNow = time.Unix(1_800_000_000, 0).UTC()

type serviceStub struct {
	register    func(context.Context, RegisterCommand) (RegisterResult, error)
	verifyEmail func(context.Context, VerifyEmailCommand) (VerifyEmailResult, error)
	login       func(context.Context, LoginCommand) (LoginResult, error)
}

func (stub serviceStub) Register(ctx context.Context, command RegisterCommand) (RegisterResult, error) {
	if stub.register == nil {
		return RegisterResult{}, errors.New("unexpected register")
	}
	return stub.register(ctx, command)
}

func (stub serviceStub) VerifyEmail(ctx context.Context, command VerifyEmailCommand) (VerifyEmailResult, error) {
	if stub.verifyEmail == nil {
		return VerifyEmailResult{}, errors.New("unexpected verify email")
	}
	return stub.verifyEmail(ctx, command)
}

func (stub serviceStub) Login(ctx context.Context, command LoginCommand) (LoginResult, error) {
	if stub.login == nil {
		return LoginResult{}, errors.New("unexpected login")
	}
	return stub.login(ctx, command)
}

func TestRegisterStrictlyValidatesAndNormalizesOpenAPIRequest(t *testing.T) {
	called := false
	service := serviceStub{register: func(_ context.Context, command RegisterCommand) (RegisterResult, error) {
		called = true
		if command.NormalizedEmail != "user@example.com" || command.Password != "correct horse battery staple" || command.Locale != "zh-CN" {
			t.Fatalf("command=%#v", command)
		}
		if command.RequestID != "server-request-id" || command.ClientRequestID != "client-request-001" || command.IdempotencyKey != "register-key-0000001" || len(command.ClientIPHash) != 32 || len(command.UserAgentHash) != 32 {
			t.Fatalf("metadata=%#v", command.RequestMetadata)
		}
		return RegisterResult{UserID: "user-1", EmailVerificationExpiresAt: apiTestNow.Add(24 * time.Hour)}, nil
	}}
	body := `{"request_id":"client-request-001","email":" User@Example.COM ","password":"correct horse battery staple","locale":"zh-CN"}`
	recorder := servePublic(t, service, http.MethodPost, "/v1/auth/register", "application/json; charset=utf-8", "register-key-0000001", body)
	if recorder.Code != http.StatusOK || !called {
		t.Fatalf("status=%d body=%s called=%v", recorder.Code, recorder.Body.String(), called)
	}
	var response struct {
		UserID string `json:"user_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.UserID != "user-1" || response.Status != "verification_required" || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response=%#v headers=%v", response, recorder.Header())
	}
}

func TestVerifyEmailUsesOpaqueTokenWithoutReturningIt(t *testing.T) {
	token := strings.Repeat("v", 48)
	service := serviceStub{verifyEmail: func(_ context.Context, command VerifyEmailCommand) (VerifyEmailResult, error) {
		if command.Token != token || command.ClientRequestID != "verify-request-01" {
			t.Fatalf("command=%#v", command)
		}
		return VerifyEmailResult{UserID: "user-2", VerifiedAt: apiTestNow}, nil
	}}
	body := `{"request_id":"verify-request-01","token":"` + token + `"}`
	recorder := servePublic(t, service, http.MethodPost, "/v1/auth/verify-email", "application/json", "verify-key-000000001", body)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), token) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestLoginReturnsSecureSessionAndSeparateCSRFCookies(t *testing.T) {
	sessionCredential, err := session.NewFrom(bytes.NewReader(bytes.Repeat([]byte{0x31}, 32)), bytes.Repeat([]byte{0x41}, 32))
	if err != nil {
		t.Fatal(err)
	}
	csrfCredential, err := session.NewFrom(bytes.NewReader(bytes.Repeat([]byte{0x32}, 32)), bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service := serviceStub{login: func(_ context.Context, command LoginCommand) (LoginResult, error) {
		if command.NormalizedEmail != "member@example.com" || command.Password != "complete-password-value" {
			t.Fatalf("command=%#v", command)
		}
		return LoginResult{UserID: "user-3", SessionID: "session-3", SessionToken: sessionCredential.Raw, CSRFToken: csrfCredential.Raw, ExpiresAt: apiTestNow.Add(time.Hour)}, nil
	}}
	body := `{"request_id":"login-request-001","email":"MEMBER@example.com","password":"complete-password-value"}`
	recorder := servePublic(t, service, http.MethodPost, "/v1/auth/login", "application/json", "login-key-0000000001", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), sessionCredential.Raw) || strings.Contains(recorder.Body.String(), csrfCredential.Raw) {
		t.Fatal("credential leaked into JSON response")
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookies=%#v", cookies)
	}
	byName := map[string]*http.Cookie{}
	for _, cookie := range cookies {
		byName[cookie.Name] = cookie
	}
	if byName[session.CookieName] == nil || !byName[session.CookieName].HttpOnly || byName[session.CSRFCookieName] == nil || byName[session.CSRFCookieName].HttpOnly {
		t.Fatalf("cookies=%#v", cookies)
	}
}

func TestLoginCredentialFailuresAreIndistinguishable(t *testing.T) {
	responses := make([]string, 0, 2)
	for _, internal := range []error{ErrInvalidCredentials, errors.Join(ErrInvalidCredentials, errors.New("user missing"))} {
		service := serviceStub{login: func(context.Context, LoginCommand) (LoginResult, error) { return LoginResult{}, internal }}
		recorder := servePublic(t, service, http.MethodPost, "/v1/auth/login", "application/json", "login-key-0000000001", `{"request_id":"login-request-001","email":"member@example.com","password":"wrong-value"}`)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		responses = append(responses, recorder.Body.String())
	}
	if responses[0] != responses[1] || strings.Contains(responses[0], "missing") {
		t.Fatalf("enumerating responses: %#v", responses)
	}
}

func TestPublicIdentityBoundaryRejectsMalformedRequestsBeforeService(t *testing.T) {
	service := serviceStub{register: func(context.Context, RegisterCommand) (RegisterResult, error) {
		t.Fatal("malformed request reached service")
		return RegisterResult{}, nil
	}}
	tests := []struct {
		name, contentType, key, body string
		status                       int
	}{
		{name: "media type", contentType: "text/plain", key: "register-key-0000001", body: `{}`, status: http.StatusUnsupportedMediaType},
		{name: "unknown field", contentType: "application/json", key: "register-key-0000001", body: `{"request_id":"request-001","email":"u@example.com","password":"correct horse battery staple","locale":"en","admin":true}`, status: http.StatusBadRequest},
		{name: "trailing JSON", contentType: "application/json", key: "register-key-0000001", body: `{"request_id":"request-001","email":"u@example.com","password":"correct horse battery staple","locale":"en"}{}`, status: http.StatusBadRequest},
		{name: "missing idempotency", contentType: "application/json", body: `{}`, status: http.StatusBadRequest},
		{name: "oversized", contentType: "application/json", key: "register-key-0000001", body: `{"padding":"` + strings.Repeat("x", int(maximumBodyBytes)) + `"}`, status: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := servePublic(t, service, http.MethodPost, "/v1/auth/register", test.contentType, test.key, test.body)
			if recorder.Code != test.status || recorder.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("status=%d content-type=%s body=%s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
			}
		})
	}
}

func TestHandlerFailsClosedWithoutTrustedPublicContextAndDoesNotLeakServiceErrors(t *testing.T) {
	handler := Handler{Service: serviceStub{}, Now: func() time.Time { return apiTestNow }}
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	service := serviceStub{login: func(context.Context, LoginCommand) (LoginResult, error) {
		return LoginResult{}, errors.New("postgres password=secret")
	}}
	recorder = servePublic(t, service, http.MethodPost, "/v1/auth/login", "application/json", "login-key-0000000001", `{"request_id":"login-request-001","email":"member@example.com","password":"wrong-value"}`)
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "postgres") || strings.Contains(recorder.Body.String(), "secret") {
		t.Fatalf("unsafe response: %s", recorder.Body.String())
	}
	var value problem.Value
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil || value.Code != "internal_error" {
		t.Fatalf("problem=%#v err=%v", value, err)
	}
}

func servePublic(t *testing.T, service Service, method, path, contentType, idempotencyKey, body string) *httptest.ResponseRecorder {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ipHash := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32))
	userAgentHash := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x62}, 32))
	claims := trustedcontext.Claims{PrincipalKind: trustedcontext.PublicRequest, Issuer: "gateway", Audience: "identity", RequestID: "server-request-id", RequestMethod: method, RequestTarget: path, ClientIPHash: ipHash, UserAgentHash: userAgentHash, CSRFVerified: true, IssuedAt: apiTestNow.Unix(), ExpiresAt: apiTestNow.Add(time.Minute).Unix(), Nonce: "nonce-public"}
	token, err := trustedcontext.Sign(claims, "key", privateKey, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, "https://identity.internal"+path, strings.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	request.Header.Set(transport.RequestIDHeader, claims.RequestID)
	request.Header.Set(transport.TrustedContextHeader, token)
	if idempotencyKey != "" {
		request.Header.Set(transport.IdempotencyHeader, idempotencyKey)
	}
	request.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}
	recorder := httptest.NewRecorder()
	middleware := serviceauth.Middleware{Verifier: trustedcontext.Verifier{Issuer: "gateway", Audience: "identity", Keys: map[string]ed25519.PublicKey{"key": publicKey}, MaximumTTL: 5 * time.Minute}, Now: func() time.Time { return apiTestNow }, RequireVerifiedClientCertificate: true}
	middleware.Wrap(Handler{Service: service, Now: func() time.Time { return apiTestNow }}).ServeHTTP(recorder, request)
	return recorder
}
