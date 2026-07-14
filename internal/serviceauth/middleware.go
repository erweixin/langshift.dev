// Package serviceauth verifies the gateway-issued context at every downstream
// HTTP service. Verified claims are process-local and the bearer header is
// removed before application handlers execute.
package serviceauth

import (
	"context"
	"net/http"
	"time"

	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
)

type contextKey struct{}

type Middleware struct {
	Verifier                         trustedcontext.Verifier
	Now                              func() time.Time
	RequireVerifiedClientCertificate bool
}

func (middleware Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID := request.Header.Get(transport.RequestIDHeader)
		if middleware.RequireVerifiedClientCertificate && (request.TLS == nil || len(request.TLS.VerifiedChains) == 0) {
			request.Header.Del(transport.TrustedContextHeader)
			problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/authentication_required", Title: "Authentication required", Status: http.StatusUnauthorized, Code: "authentication_required", RequestID: requestID, Retryable: false})
			return
		}
		now := time.Now().UTC()
		if middleware.Now != nil {
			now = middleware.Now().UTC()
		}
		claims, err := middleware.Verifier.VerifyRequest(request.Header.Get(transport.TrustedContextHeader), now, requestID, request.Method, request.URL.RequestURI(), transport.RequiresCSRF(request.Method))
		request.Header.Del(transport.TrustedContextHeader)
		if err != nil {
			problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/authentication_required", Title: "Authentication required", Status: http.StatusUnauthorized, Code: "authentication_required", RequestID: requestID, Retryable: false})
			return
		}
		ctx := context.WithValue(request.Context(), contextKey{}, claims)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func ClaimsFromContext(ctx context.Context) (trustedcontext.Claims, bool) {
	claims, ok := ctx.Value(contextKey{}).(trustedcontext.Claims)
	return claims, ok
}
