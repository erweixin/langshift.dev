// Package gateway owns the browser-to-service trust boundary.
package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/security/trustedcontext"
)

const TrustedContextHeader = "X-Lites-Trusted-Context"

var ErrUnauthenticated = errors.New("session is not authenticated")

var untrustedIdentityHeaders = []string{
	TrustedContextHeader, "X-Lites-User-ID", "X-Lites-Tenant-ID", "X-Lites-Membership-ID", "X-Lites-Roles", "X-Lites-Session-ID",
}

type Principal struct {
	UserID       string
	TenantID     string
	MembershipID string
	SessionID    string
	Roles        []string
	ExpiresAt    time.Time
}

type SessionResolver interface {
	Resolve(context.Context, string) (Principal, error)
}

type TrustBoundary struct {
	Resolver     SessionResolver
	SigningKey   ed25519.PrivateKey
	SigningKeyID string
	Issuer       string
	Audience     string
	TTL          time.Duration
	Now          func() time.Time
}

func (boundary TrustBoundary) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		for _, name := range untrustedIdentityHeaders {
			request.Header.Del(name)
		}
		cookie, err := request.Cookie(session.CookieName)
		if err != nil || boundary.Resolver == nil {
			boundary.unauthorized(writer, request)
			return
		}
		principal, err := boundary.Resolver.Resolve(request.Context(), cookie.Value)
		if err != nil || principal.UserID == "" || principal.TenantID == "" || principal.MembershipID == "" || principal.SessionID == "" || len(principal.Roles) == 0 {
			boundary.unauthorized(writer, request)
			return
		}
		now := time.Now().UTC()
		if boundary.Now != nil {
			now = boundary.Now().UTC()
		}
		expiresAt := now.Add(boundary.TTL)
		if principal.ExpiresAt.Before(expiresAt) {
			expiresAt = principal.ExpiresAt
		}
		if !expiresAt.After(now) {
			boundary.unauthorized(writer, request)
			return
		}
		nonceBytes := make([]byte, 16)
		if _, err = rand.Read(nonceBytes); err != nil {
			problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/internal_error", Title: "Internal error", Status: http.StatusInternalServerError, Code: "internal_error", RequestID: request.Header.Get("X-Request-ID"), Retryable: true})
			return
		}
		claims := trustedcontext.Claims{Issuer: boundary.Issuer, Audience: boundary.Audience, SubjectID: principal.UserID, TenantID: principal.TenantID, MembershipID: principal.MembershipID, SessionID: principal.SessionID, Roles: principal.Roles, IssuedAt: now.Unix(), ExpiresAt: expiresAt.Unix(), Nonce: base64.RawURLEncoding.EncodeToString(nonceBytes)}
		token, err := trustedcontext.Sign(claims, boundary.SigningKeyID, boundary.SigningKey, 5*time.Minute)
		if err != nil {
			problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/internal_error", Title: "Internal error", Status: http.StatusInternalServerError, Code: "internal_error", RequestID: request.Header.Get("X-Request-ID"), Retryable: true})
			return
		}
		request.Header.Set(TrustedContextHeader, token)
		next.ServeHTTP(writer, request)
	})
}

func (boundary TrustBoundary) unauthorized(writer http.ResponseWriter, request *http.Request) {
	problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/unauthenticated", Title: "Authentication required", Status: http.StatusUnauthorized, Code: "unauthenticated", RequestID: request.Header.Get("X-Request-ID"), Retryable: false})
}
