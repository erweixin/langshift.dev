package gateway

import (
	"net/http"
	"strings"
)

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
	if request.URL.Path == "/v1/onboarding-sessions" && request.Method == http.MethodPost {
		return PublicOrAnonymousOrSession
	}
	if remainder, found := strings.CutPrefix(request.URL.Path, "/v1/onboarding-sessions/"); found {
		segments := strings.Split(remainder, "/")
		if len(segments) == 1 && segments[0] != "" {
			return AnonymousOrSession
		}
		if len(segments) == 2 && segments[0] != "" {
			switch segments[1] {
			case "route-preview":
				return AnonymousOrSession
			case "claim":
				return AuthenticationRequired
			}
		}
	}
	return AuthenticationRequired
}
