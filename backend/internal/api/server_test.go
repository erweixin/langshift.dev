package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lites/backend/internal/content"
	"lites/backend/internal/contracts"
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
	request.Header.Set("X-User-ID", "attacker")
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

func TestGetContentGenerationRun(t *testing.T) {
	reader := &fakeContentReader{
		run: content.RunResult{
			RunID:      "run_1",
			Status:     "succeeded",
			ContentKey: "content_abc",
		},
	}
	server := NewServer(ServerConfig{
		SingleUser:    true,
		ContentReader: reader,
	})

	request := httptest.NewRequest(http.MethodGet, "/api/content-generation-runs/run_1", nil)
	recorder := httptest.NewRecorder()

	server.mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if reader.runUserID != "local-user" || reader.runID != "run_1" {
		t.Fatalf("reader run args = %q/%q", reader.runUserID, reader.runID)
	}
	var response content.RunResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.ContentKey != "content_abc" || response.Status != "succeeded" {
		t.Fatalf("response = %+v", response)
	}
}

func TestGetContentArtifactRedactsReferenceSolution(t *testing.T) {
	reader := &fakeContentReader{
		artifact: content.ArtifactRecord{
			ReviewStatus:       content.ReviewStatusAutoOK,
			ValidationAttempts: 1,
			Artifact: contracts.ContentArtifact{
				SchemaVersion:  1,
				ContentKey:     "content_abc",
				TaskTemplateID: "fe2agent-d01",
				TargetStack:    "frontend_to_agent",
				LevelBand:      "default",
				ContentVersion: 1,
				PromptVersion:  1,
				Lesson: contracts.Lesson{
					Title:   "API boundaries",
					Minutes: 30,
					Judge:   "Explain API acceptance versus worker execution.",
					Sections: []contracts.LessonSection{{
						ID:     "section_1",
						Title:  "The boundary",
						BodyMD: "API acceptance queues work.",
					}},
				},
				Exercise: contracts.Exercise{
					Language:          "javascript",
					StarterCode:       "function f() {}",
					ReferenceSolution: "secret solution",
					HarnessVersion:    1,
					Tests: []contracts.ExerciseTest{{
						ID:     "case_1",
						Call:   "f()",
						Expect: "ok",
						Judge:  true,
						Label:  "case",
					}},
				},
			},
		},
	}
	server := NewServer(ServerConfig{
		SingleUser:    true,
		ContentReader: reader,
	})

	request := httptest.NewRequest(http.MethodGet, "/api/content-artifacts/content_abc", nil)
	recorder := httptest.NewRecorder()

	server.mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if reader.contentKey != "content_abc" {
		t.Fatalf("content key = %q", reader.contentKey)
	}
	var response contracts.PublicContentArtifact
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.Contains(recorder.Body.String(), "reference_solution") {
		t.Fatalf("reference_solution leaked: %s", recorder.Body.String())
	}
	if response.ReviewStatus != content.ReviewStatusAutoOK || response.ValidationAttempts != 1 {
		t.Fatalf("validation metadata = %s/%d", response.ReviewStatus, response.ValidationAttempts)
	}
	if response.Exercise.StarterCode != "function f() {}" || response.Exercise.HarnessVersion != 1 {
		t.Fatalf("public exercise = %+v", response.Exercise)
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

type fakeContentReader struct {
	run        content.RunResult
	artifact   content.ArtifactRecord
	err        error
	runUserID  string
	runID      string
	contentKey string
}

func (f *fakeContentReader) GetRun(_ context.Context, userID string, runID string) (content.RunResult, error) {
	f.runUserID = userID
	f.runID = runID
	if f.err != nil {
		return content.RunResult{}, f.err
	}
	return f.run, nil
}

func (f *fakeContentReader) GetArtifact(_ context.Context, contentKey string) (content.ArtifactRecord, error) {
	f.contentKey = contentKey
	if f.err != nil {
		return content.ArtifactRecord{}, f.err
	}
	return f.artifact, nil
}
