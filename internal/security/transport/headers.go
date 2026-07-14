// Package transport defines the internal HTTP trust-boundary headers.
package transport

import "net/http"

const (
	TrustedContextHeader = "X-Lites-Trusted-Context"
	CSRFHeader           = "X-CSRF-Token"
	RequestIDHeader      = "X-Request-ID"
)

func RequiresCSRF(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}
