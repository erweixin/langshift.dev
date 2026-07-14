package problem

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteUsesProblemJSONAndNoStore(t *testing.T) {
	recorder := httptest.NewRecorder()
	Write(recorder, Value{Type: "https://errors.lites.dev/version_conflict", Title: "Version conflict", Status: http.StatusConflict, Code: "version_conflict", RequestID: "req-1"})
	if recorder.Code != http.StatusConflict || recorder.Header().Get("Content-Type") != "application/problem+json" || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response=%#v", recorder.Result())
	}
	if !strings.Contains(recorder.Body.String(), `"request_id":"req-1"`) {
		t.Fatalf("body=%s", recorder.Body.String())
	}
}
