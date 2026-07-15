// Package gateway owns the browser-to-service trust boundary.
package gateway

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/langshift/lites/internal/identity/anonymoussession"
	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
)

const TrustedContextHeader = transport.TrustedContextHeader
const CSRFHeader = transport.CSRFHeader
const RequestIDHeader = transport.RequestIDHeader

var untrustedIdentityHeaders = []string{
	TrustedContextHeader, "Authorization", "Proxy-Authorization", "X-Lites-User-ID", "X-Lites-Tenant-ID", "X-Lites-Membership-ID", "X-Lites-Roles", "X-Lites-Session-ID",
}

var untrustedNetworkHeaders = []string{
	"Forwarded", "X-Forwarded-For", "X-Real-IP", "True-Client-IP", "CF-Connecting-IP",
}

type TrustBoundary struct {
	Resolver           session.Resolver
	AnonymousResolver  anonymoussession.Resolver
	SigningKey         ed25519.PrivateKey
	SigningKeyID       string
	Issuer             string
	Audience           string
	AudienceForRequest func(*http.Request) string
	TTL                time.Duration
	CSRFPepper         []byte
	AnonymousCSRFKey   []byte
	FingerprintPepper  []byte
	PublicOrigins      []string
	RoutePolicy        func(*http.Request) AuthenticationPolicy
	Random             io.Reader
	Now                func() time.Time
}

type AuthenticationPolicy uint8

const (
	AuthenticationRequired AuthenticationPolicy = iota
	PublicAuthentication
	AnonymousOrSession
	PublicOrAnonymousOrSession
	AuthenticatedWithAnonymous
)

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
		clientIPHash, userAgentHash, err := boundary.requestFingerprints(request)
		if err != nil {
			boundary.internalError(writer, request)
			return
		}
		policy := AuthenticationRequired
		if boundary.RoutePolicy != nil {
			policy = boundary.RoutePolicy(request)
		}
		principal := session.Principal{}
		anonymousPrincipal := anonymoussession.Principal{}
		principalKind := trustedcontext.PublicRequest
		csrfVerified := !transport.RequiresCSRF(request.Method)
		if policy == PublicAuthentication {
			if transport.RequiresCSRF(request.Method) && !boundary.validPublicOrigin(request) {
				request.Header.Del(CSRFHeader)
				boundary.permissionDenied(writer, request)
				return
			}
			csrfVerified = true
		} else if sessionCookie, sessionCookieErr := request.Cookie(session.CookieName); sessionCookieErr == nil {
			principalKind = trustedcontext.AuthenticatedUser
			if boundary.Resolver == nil {
				boundary.unauthorized(writer, request)
				return
			}
			principal, err = boundary.Resolver.Resolve(request.Context(), sessionCookie.Value)
			if err != nil || principal.UserID == "" || principal.TenantID == "" || principal.MembershipID == "" || principal.SessionID == "" || len(principal.Roles) == 0 {
				boundary.unauthorized(writer, request)
				return
			}
			if transport.RequiresCSRF(request.Method) {
				rawCSRF := request.Header.Get(CSRFHeader)
				digest, digestErr := session.Digest(rawCSRF, boundary.CSRFPepper)
				if digestErr != nil || len(principal.CSRFSecretHash) != len(digest) || !hmac.Equal(principal.CSRFSecretHash, digest[:]) {
					request.Header.Del(CSRFHeader)
					boundary.permissionDenied(writer, request)
					return
				}
				csrfVerified = true
			}
			if policy == AuthenticatedWithAnonymous {
				anonymousCookie, anonymousCookieErr := request.Cookie(anonymoussession.CookieName)
				if anonymousCookieErr != nil || boundary.AnonymousResolver == nil {
					boundary.unauthorized(writer, request)
					return
				}
				anonymousPrincipal, err = boundary.AnonymousResolver.Resolve(request.Context(), anonymousCookie.Value)
				if err != nil || anonymousPrincipal.AnonymousSubjectID == "" || anonymousPrincipal.UserID == "" || anonymousPrincipal.TenantID == "" || anonymousPrincipal.ExpiresAt.IsZero() {
					boundary.unauthorized(writer, request)
					return
				}
			}
		} else if policy == AnonymousOrSession || policy == PublicOrAnonymousOrSession {
			anonymousCookie, anonymousCookieErr := request.Cookie(anonymoussession.CookieName)
			if anonymousCookieErr == nil {
				principalKind = trustedcontext.AnonymousUser
				if boundary.AnonymousResolver == nil {
					boundary.unauthorized(writer, request)
					return
				}
				anonymousPrincipal, err = boundary.AnonymousResolver.Resolve(request.Context(), anonymousCookie.Value)
				if err != nil || anonymousPrincipal.AnonymousSubjectID == "" || anonymousPrincipal.UserID == "" || anonymousPrincipal.TenantID == "" || anonymousPrincipal.ExpiresAt.IsZero() {
					boundary.unauthorized(writer, request)
					return
				}
				if transport.RequiresCSRF(request.Method) {
					if !anonymoussession.VerifyCSRF(anonymousCookie.Value, request.Header.Get(CSRFHeader), boundary.AnonymousCSRFKey) {
						request.Header.Del(CSRFHeader)
						boundary.permissionDenied(writer, request)
						return
					}
					csrfVerified = true
				}
			} else if policy == PublicOrAnonymousOrSession {
				if transport.RequiresCSRF(request.Method) && !boundary.validPublicOrigin(request) {
					request.Header.Del(CSRFHeader)
					boundary.permissionDenied(writer, request)
					return
				}
				csrfVerified = true
			} else {
				boundary.unauthorized(writer, request)
				return
			}
		} else {
			boundary.unauthorized(writer, request)
			return
		}
		stripSensitiveCookies(request)
		request.Header.Del("User-Agent")
		for _, name := range untrustedNetworkHeaders {
			request.Header.Del(name)
		}
		request.Header.Del(CSRFHeader)
		now := time.Now().UTC()
		if boundary.Now != nil {
			now = boundary.Now().UTC()
		}
		expiresAt := now.Add(boundary.TTL)
		if principalKind == trustedcontext.AuthenticatedUser && principal.ExpiresAt.Before(expiresAt) {
			expiresAt = principal.ExpiresAt
		}
		if policy == AuthenticatedWithAnonymous && anonymousPrincipal.ExpiresAt.Before(expiresAt) {
			expiresAt = anonymousPrincipal.ExpiresAt
		}
		if principalKind == trustedcontext.AnonymousUser && anonymousPrincipal.ExpiresAt.Before(expiresAt) {
			expiresAt = anonymousPrincipal.ExpiresAt
		}
		if !expiresAt.After(now) {
			boundary.unauthorized(writer, request)
			return
		}
		nonceBytes := make([]byte, 16)
		if _, err := io.ReadFull(randomSource, nonceBytes); err != nil {
			boundary.internalError(writer, request)
			return
		}
		audience := boundary.Audience
		if boundary.AudienceForRequest != nil {
			audience = boundary.AudienceForRequest(request)
		}
		claims := trustedcontext.Claims{PrincipalKind: principalKind, Issuer: boundary.Issuer, Audience: audience, SubjectID: principal.UserID, TenantID: principal.TenantID, MembershipID: principal.MembershipID, SessionID: principal.SessionID, Roles: principal.Roles, RequestID: requestID, RequestMethod: request.Method, RequestTarget: request.URL.RequestURI(), ClientIPHash: clientIPHash, UserAgentHash: userAgentHash, CSRFVerified: csrfVerified, IssuedAt: now.Unix(), ExpiresAt: expiresAt.Unix(), Nonce: base64.RawURLEncoding.EncodeToString(nonceBytes)}
		if policy == AuthenticatedWithAnonymous {
			claims.AnonymousSubjectID = anonymousPrincipal.AnonymousSubjectID
		}
		if principalKind == trustedcontext.AnonymousUser {
			claims.SubjectID = anonymousPrincipal.UserID
			claims.TenantID = anonymousPrincipal.TenantID
			claims.AnonymousSubjectID = anonymousPrincipal.AnonymousSubjectID
			claims.Roles = []string{"anonymous_preview"}
		}
		token, err := trustedcontext.Sign(claims, boundary.SigningKeyID, boundary.SigningKey, 5*time.Minute)
		if err != nil {
			boundary.internalError(writer, request)
			return
		}
		request.Header.Set(TrustedContextHeader, token)
		next.ServeHTTP(writer, request)
	})
}

func stripSensitiveCookies(request *http.Request) {
	cookies := request.Cookies()
	request.Header.Del("Cookie")
	for _, cookie := range cookies {
		if cookie.Name == session.CookieName || cookie.Name == session.CSRFCookieName || cookie.Name == anonymoussession.CookieName || cookie.Name == anonymoussession.CSRFCookieName {
			continue
		}
		request.AddCookie(cookie)
	}
}

func (boundary TrustBoundary) requestFingerprints(request *http.Request) (string, string, error) {
	if len(boundary.FingerprintPepper) < 32 {
		return "", "", errors.New("fingerprint pepper must contain at least 32 bytes")
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", "", errors.New("client address is invalid")
	}
	return keyedFingerprint(ip.String(), boundary.FingerprintPepper), keyedFingerprint(request.UserAgent(), boundary.FingerprintPepper), nil
}

func keyedFingerprint(value string, pepper []byte) string {
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (boundary TrustBoundary) validPublicOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if fetchSite := request.Header.Get("Sec-Fetch-Site"); fetchSite != "" && fetchSite != "same-origin" && fetchSite != "same-site" {
		return false
	}
	canonical := parsed.Scheme + "://" + parsed.Host
	for _, allowed := range boundary.PublicOrigins {
		if canonical == allowed {
			return true
		}
	}
	return false
}

func (boundary TrustBoundary) unauthorized(writer http.ResponseWriter, request *http.Request) {
	problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/authentication_required", Title: "Authentication required", Status: http.StatusUnauthorized, Code: "authentication_required", RequestID: request.Header.Get(RequestIDHeader), Retryable: false})
}

func (boundary TrustBoundary) permissionDenied(writer http.ResponseWriter, request *http.Request) {
	problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/permission_denied", Title: "Permission denied", Status: http.StatusForbidden, Code: "permission_denied", RequestID: request.Header.Get(RequestIDHeader), Retryable: false})
}

func (boundary TrustBoundary) internalError(writer http.ResponseWriter, request *http.Request) {
	problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/internal_error", Title: "Internal error", Status: http.StatusInternalServerError, Code: "internal_error", RequestID: request.Header.Get(RequestIDHeader), Retryable: true})
}
