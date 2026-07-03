package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lites/backend/internal/content"
)

func TestCreateContentGenerationRun(t *testing.T) {
	starter := &fakeContentGeneration{
		result: content.StartResult{RunID: "run_1", Status: "queued"},
	}
	server := NewServer(ServerConfig{
		SingleUser:        true,
		ContentGeneration: starter,
	})

	request := httptest.NewRequest(http.MethodPost, "/api/content-generation-runs", strings.NewReader(`{
		"task_template_id": "fe2agent-d01",
		"target_stack": "frontend_to_agent",
		"level_band": "default",
		"title": "API boundaries",
		"judge": "Explain API acceptance versus worker execution."
	}`))
	request.Header.Set("Idempotency-Key", "key-1")
	recorder := httptest.NewRecorder()

	server.mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	if starter.request.UserID != "local-user" {
		t.Fatalf("user id = %q, want local-user", starter.request.UserID)
	}
	if starter.request.IdempotencyKey != "key-1" {
		t.Fatalf("idempotency key = %q, want key-1", starter.request.IdempotencyKey)
	}
	if starter.request.Input.TaskTemplateID != "fe2agent-d01" {
		t.Fatalf("task template = %q", starter.request.Input.TaskTemplateID)
	}

	var response content.StartResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.RunID != "run_1" || response.Status != "queued" {
		t.Fatalf("response = %+v, want queued run_1", response)
	}
}

func TestCreateContentGenerationRunRequiresIdempotencyKey(t *testing.T) {
	server := NewServer(ServerConfig{
		SingleUser:        true,
		ContentGeneration: &fakeContentGeneration{},
	})

	request := httptest.NewRequest(http.MethodPost, "/api/content-generation-runs", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()

	server.mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

type fakeContentGeneration struct {
	request content.StartRequest
	result  content.StartResult
	err     error
}

func (f *fakeContentGeneration) Start(_ context.Context, request content.StartRequest) (content.StartResult, error) {
	f.request = request
	if f.err != nil {
		return content.StartResult{}, f.err
	}
	return f.result, nil
}
