package gateway

import (
	"net/http"
	"sort"
	"strings"
)

var allowedCORSHeaders = map[string]struct{}{
	"accept": {}, "content-type": {}, "idempotency-key": {}, "if-match": {}, "last-event-id": {}, "x-csrf-token": {},
}

var allowedCORSMethods = map[string]struct{}{
	http.MethodGet: {}, http.MethodPost: {}, http.MethodPatch: {}, http.MethodDelete: {},
}

type CORS struct{ Origins []string }

func (cors CORS) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		origin := request.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(writer, request)
			return
		}
		if !cors.allowedOrigin(origin) {
			if request.Method == http.MethodOptions {
				writer.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(writer, request)
			return
		}
		writer.Header().Add("Vary", "Origin")
		writer.Header().Set("Access-Control-Allow-Origin", origin)
		writer.Header().Set("Access-Control-Allow-Credentials", "true")
		writer.Header().Set("Access-Control-Expose-Headers", "ETag, X-Request-ID")
		if request.Method != http.MethodOptions {
			next.ServeHTTP(writer, request)
			return
		}
		writer.Header().Add("Vary", "Access-Control-Request-Method")
		writer.Header().Add("Vary", "Access-Control-Request-Headers")
		method := request.Header.Get("Access-Control-Request-Method")
		if _, ok := allowedCORSMethods[method]; !ok || !validRequestedHeaders(request.Header.Values("Access-Control-Request-Headers")) {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		methods := make([]string, 0, len(allowedCORSMethods))
		for value := range allowedCORSMethods {
			methods = append(methods, value)
		}
		sort.Strings(methods)
		writer.Header().Set("Access-Control-Allow-Methods", strings.Join(methods, ", "))
		writer.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Idempotency-Key, If-Match, Last-Event-ID, X-CSRF-Token")
		writer.Header().Set("Access-Control-Max-Age", "600")
		writer.WriteHeader(http.StatusNoContent)
	})
}

func (cors CORS) allowedOrigin(candidate string) bool {
	for _, origin := range cors.Origins {
		if candidate == origin {
			return true
		}
	}
	return false
}

func validRequestedHeaders(values []string) bool {
	for _, value := range values {
		for _, header := range strings.Split(value, ",") {
			header = strings.ToLower(strings.TrimSpace(header))
			if header == "" {
				continue
			}
			if _, ok := allowedCORSHeaders[header]; !ok {
				return false
			}
		}
	}
	return true
}
