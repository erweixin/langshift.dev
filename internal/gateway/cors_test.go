package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSAllowsExactCredentialedOriginAndStrictPreflight(t *testing.T) {
	cors := CORS{Origins: []string{"https://app.lites.dev"}}
	reached := false
	handler := cors.Wrap(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		reached = true
		writer.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodOptions, "https://api.lites.dev/v1/auth/login", nil)
	request.Header.Set("Origin", "https://app.lites.dev")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "Content-Type, Idempotency-Key")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || reached || recorder.Header().Get("Access-Control-Allow-Origin") != "https://app.lites.dev" || recorder.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("status=%d reached=%v headers=%v", recorder.Code, reached, recorder.Header())
	}
	for _, test := range []struct{ origin, method, headers string }{
		{"https://evil.example", http.MethodPost, "Content-Type"},
		{"https://app.lites.dev.evil.example", http.MethodPost, "Content-Type"},
		{"https://app.lites.dev", http.MethodPut, "Content-Type"},
		{"https://app.lites.dev", http.MethodPost, "Authorization"},
	} {
		request = httptest.NewRequest(http.MethodOptions, "https://api.lites.dev/v1/auth/login", nil)
		request.Header.Set("Origin", test.origin)
		request.Header.Set("Access-Control-Request-Method", test.method)
		request.Header.Set("Access-Control-Request-Headers", test.headers)
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("test=%#v status=%d", test, recorder.Code)
		}
	}
}
