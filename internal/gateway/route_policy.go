package gateway

import "net/http"

var publicIdentityPaths = map[string]struct{}{
	"/v1/auth/register":            {},
	"/v1/auth/verify-email":        {},
	"/v1/auth/resend-verification": {},
	"/v1/auth/login":               {},
	"/v1/auth/password/forgot":     {},
	"/v1/auth/password/reset":      {},
}

// IdentityRoutePolicy is intentionally an exact allowlist. Any new route is
// authenticated by default until its OpenAPI security review adds it here.
func IdentityRoutePolicy(request *http.Request) AuthenticationPolicy {
	if _, ok := publicIdentityPaths[request.URL.Path]; ok {
		return PublicAuthentication
	}
	return AuthenticationRequired
}
