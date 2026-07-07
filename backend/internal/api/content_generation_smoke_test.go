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
	"lites/backend/internal/event"
	"lites/backend/internal/job"
	"lites/backend/internal/llm"
	"lites/backend/internal/run"
	"lites/backend/internal/testsupport"
)

func TestContentGenerationAPISmoke(t *testing.T) {
	ctx := context.Background()
	pool := testsupport.NewMigratedPool(t, ctx, "test_api")
	events := event.NewService(pool, event.Options{Dispatcher: run.NewReducer()})
	store := content.NewStore(pool)
	service := content.NewService(events, content.ServiceOptions{
		Artifacts:   store,
		IDGenerator: testsupport.NewSequenceIDs("run_smoke"),
	})
	workerLLM := &smokeLLM{
		response: llm.Response{
			LedgerID: "ledger_smoke",
			Content:  smokeArtifactJSON(),
			Usage:    llm.Usage{InputTokens: 42, OutputTokens: 84},
		},
	}
	worker := content.NewWorker(
		job.NewQueue(pool, job.Options{}),
		events,
		workerLLM,
		store,
		content.WorkerOptions{IDGenerator: testsupport.NewSequenceIDs("attempt_smoke")},
	)
	server := NewServer(ServerConfig{
		SingleUser:        true,
		ContentGeneration: service,
		ContentReader:     store,
	})

	create := httptest.NewRequest(http.MethodPost, "/api/content-generation-runs", strings.NewReader(`{
		"task_template_id": "fe2agent-d01",
		"target_stack": "frontend_to_agent",
		"level_band": "default",
		"title": "API boundaries",
		"judge": "Explain API acceptance versus worker execution.",
		"minutes": 30,
		"context": {"source": "api-smoke"}
	}`))
	create.Header.Set("Idempotency-Key", "api-smoke-key")
	createRecorder := httptest.NewRecorder()

	server.mux.ServeHTTP(createRecorder, create)

	if createRecorder.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d; body=%s", createRecorder.Code, http.StatusAccepted, createRecorder.Body.String())
	}
	var started content.StartResult
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if started.RunID != "run_smoke" || started.Status != "queued" {
		t.Fatalf("create response = %+v, want queued run_smoke", started)
	}

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("process content generation job: %v", err)
	}
	if !processed {
		t.Fatal("worker processed = false, want true")
	}
	if workerLLM.request.RunID != "run_smoke" || !workerLLM.request.JSONMode || workerLLM.request.SchemaName != "content_artifact" {
		t.Fatalf("llm request = %+v", workerLLM.request)
	}

	runRequest := httptest.NewRequest(http.MethodGet, "/api/content-generation-runs/run_smoke", nil)
	runRecorder := httptest.NewRecorder()
	server.mux.ServeHTTP(runRecorder, runRequest)

	if runRecorder.Code != http.StatusOK {
		t.Fatalf("run status = %d, want %d; body=%s", runRecorder.Code, http.StatusOK, runRecorder.Body.String())
	}
	var generated content.RunResult
	if err := json.Unmarshal(runRecorder.Body.Bytes(), &generated); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if generated.Status != run.StatusSucceeded || generated.ContentKey == "" {
		t.Fatalf("run response = %+v, want succeeded with content key", generated)
	}

	artifactRequest := httptest.NewRequest(http.MethodGet, "/api/content-artifacts/"+generated.ContentKey, nil)
	artifactRecorder := httptest.NewRecorder()
	server.mux.ServeHTTP(artifactRecorder, artifactRequest)

	if artifactRecorder.Code != http.StatusOK {
		t.Fatalf("artifact status = %d, want %d; body=%s", artifactRecorder.Code, http.StatusOK, artifactRecorder.Body.String())
	}
	if strings.Contains(artifactRecorder.Body.String(), "reference_solution") {
		t.Fatalf("public artifact leaked reference_solution: %s", artifactRecorder.Body.String())
	}
	var public contracts.PublicContentArtifact
	if err := json.Unmarshal(artifactRecorder.Body.Bytes(), &public); err != nil {
		t.Fatalf("decode public artifact: %v", err)
	}
	if public.ContentKey != generated.ContentKey || public.ReviewStatus != content.ReviewStatusAutoOK || public.ValidationAttempts != 1 {
		t.Fatalf("public artifact metadata = %s/%s/%d, want %s/auto_ok/1", public.ContentKey, public.ReviewStatus, public.ValidationAttempts, generated.ContentKey)
	}
	if public.Lesson.Title != "API boundaries" || public.Exercise.StarterCode == "" || public.Exercise.HarnessVersion != 1 {
		t.Fatalf("public artifact = %+v", public)
	}
}

type smokeLLM struct {
	request  llm.Request
	response llm.Response
	err      error
}

func (f *smokeLLM) Complete(_ context.Context, request llm.Request) (llm.Response, error) {
	f.request = request
	if f.err != nil {
		return llm.Response{}, f.err
	}
	return f.response, nil
}

func smokeArtifactJSON() string {
	return `{
		"lesson": {
			"title": "API boundaries",
			"minutes": 30,
			"judge": "Explain API acceptance versus worker execution.",
			"why": "Workers need a durable acceptance record before doing expensive work.",
			"sections": [
				{
					"id": "section_1",
					"title": "Acceptance",
					"body_md": "The API accepts a user request, records intent, and returns a run handle before any expensive worker execution happens."
				},
				{
					"id": "section_2",
					"title": "Execution",
					"body_md": "The worker later leases the queued job, calls the model, validates the generated content, and appends terminal run events."
				},
				{
					"id": "section_3",
					"title": "Reading output",
					"body_md": "The client watches the run projection and reads the content artifact only after the succeeded event exposes a content key."
				}
			]
		},
		"exercise": {
			"language": "javascript",
			"starter_code": "function explainBoundary() { return ''; }",
			"reference_solution": "function explainBoundary() { return 'API acceptance queues work; workers execute it.'; }",
			"harness_version": 1,
			"tests": [
				{
					"id": "case_1",
					"call": "explainBoundary()",
					"expect": "API acceptance queues work; workers execute it.",
					"judge": true,
					"label": "explains the boundary"
				},
				{
					"id": "case_2",
					"call": "typeof explainBoundary",
					"expect": "function",
					"judge": false,
					"label": "exports the function"
				},
				{
					"id": "case_3",
					"call": "explainBoundary().includes('workers')",
					"expect": true,
					"judge": false,
					"label": "mentions worker execution"
				}
			]
		}
	}`
}
