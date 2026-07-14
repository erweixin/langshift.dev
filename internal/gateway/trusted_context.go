// Package gateway owns the browser-to-service trust boundary.
package gateway

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"time"

	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
)

const TrustedContextHeader = transport.TrustedContextHeader
const CSRFHeader = transport.CSRFHeader
const RequestIDHeader = transport.RequestIDHeader

var untrustedIdentityHeaders = []string{
	TrustedContextHeader, "X-Lites-User-ID", "X-Lites-Tenant-ID", "X-Lites-Membership-ID", "X-Lites-Roles", "X-Lites-Session-ID",
}

type TrustBoundary struct {
	Resolver     session.Resolver
	SigningKey   ed25519.PrivateKey
	SigningKeyID string
	Issuer       string
	Audience     string
	TTL          time.Duration
	CSRFPepper   []byte
	Random       io.Reader
	Now          func() time.Time
}

func (boundary TrustBoundary) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		randomSource := boundary.Random
		if randomSource == nil {
			randomSource = rand.Reader
		}
		requestIDBytes := make([]byte, 16)
		if _, err := io.ReadFull(randomSource, requestIDBytes); err != nil {
			boundary.internalError(writer, request)
			return
		}
		requestID := base64.RawURLEncoding.EncodeToString(requestIDBytes)
		request.Header.Set(RequestIDHeader, requestID)
		writer.Header().Set(RequestIDHeader, requestID)
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
		csrfVerified := false
		if transport.RequiresCSRF(request.Method) {
			rawCSRF := request.Header.Get(CSRFHeader)
			digest, digestErr := session.Digest(rawCSRF, boundary.CSRFPepper)
			if digestErr != nil || len(principal.CSRFSecretHash) != len(digest) || !hmac.Equal(principal.CSRFSecretHash, digest[:]) {
				request.Header.Del(CSRFHeader)
				problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/permission_denied", Title: "Permission denied", Status: http.StatusForbidden, Code: "permission_denied", RequestID: requestID, Retryable: false})
				return
			}
			csrfVerified = true
		}
		request.Header.Del(CSRFHeader)
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
		if _, err = io.ReadFull(randomSource, nonceBytes); err != nil {
			boundary.internalError(writer, request)
			return
		}
		claims := trustedcontext.Claims{Issuer: boundary.Issuer, Audience: boundary.Audience, SubjectID: principal.UserID, TenantID: principal.TenantID, MembershipID: principal.MembershipID, SessionID: principal.SessionID, Roles: principal.Roles, RequestID: requestID, RequestMethod: request.Method, RequestTarget: request.URL.RequestURI(), CSRFVerified: csrfVerified, IssuedAt: now.Unix(), ExpiresAt: expiresAt.Unix(), Nonce: base64.RawURLEncoding.EncodeToString(nonceBytes)}
		token, err := trustedcontext.Sign(claims, boundary.SigningKeyID, boundary.SigningKey, 5*time.Minute)
		if err != nil {
			boundary.internalError(writer, request)
			return
		}
		request.Header.Set(TrustedContextHeader, token)
		next.ServeHTTP(writer, request)
	})
}

func (boundary TrustBoundary) unauthorized(writer http.ResponseWriter, request *http.Request) {
	problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/authentication_required", Title: "Authentication required", Status: http.StatusUnauthorized, Code: "authentication_required", RequestID: request.Header.Get(RequestIDHeader), Retryable: false})
}

func (boundary TrustBoundary) internalError(writer http.ResponseWriter, request *http.Request) {
	problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/internal_error", Title: "Internal error", Status: http.StatusInternalServerError, Code: "internal_error", RequestID: request.Header.Get(RequestIDHeader), Retryable: true})
}
