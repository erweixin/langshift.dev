package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecurityHeadersCoverLocallyGeneratedFailures(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://api.lites.dev/v1/account", nil))
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("Strict-Transport-Security") == "" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" || recorder.Header().Get("Referrer-Policy") != "no-referrer" || recorder.Header().Get("Permissions-Policy") == "" {
		t.Fatalf("status=%d headers=%v", recorder.Code, recorder.Header())
	}
}
