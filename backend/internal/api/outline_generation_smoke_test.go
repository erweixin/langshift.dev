package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lites/backend/internal/contracts"
	"lites/backend/internal/event"
	"lites/backend/internal/job"
	"lites/backend/internal/llm"
	"lites/backend/internal/outline"
	"lites/backend/internal/run"
	"lites/backend/internal/testsupport"
)

func TestOutlineGenerationAPISmoke(t *testing.T) {
	ctx := context.Background()
	pool := testsupport.NewMigratedPool(t, ctx, "test_api")
	events := event.NewService(pool, event.Options{Dispatcher: run.NewReducer()})
	store := outline.NewStore(pool)
	service := outline.NewService(events, outline.ServiceOptions{
		IDGenerator: testsupport.NewSequenceIDs("run_outline_smoke"),
	})
	workerLLM := &smokeLLM{
		response: llm.Response{
			LedgerID: "ledger_outline_smoke",
			Content:  smokeOutlineJSON(),
			Usage:    llm.Usage{InputTokens: 50, OutputTokens: 100},
		},
	}
	worker := outline.NewWorker(
		job.NewQueue(pool, job.Options{}),
		events,
		workerLLM,
		store,
		outline.WorkerOptions{IDGenerator: testsupport.NewSequenceIDs("attempt_outline_smoke")},
	)
	server := NewServer(ServerConfig{
		SingleUser:        true,
		OutlineGeneration: service,
		OutlineReader:     store,
	})

	create := httptest.NewRequest(http.MethodPost, "/api/outline-generation-runs", strings.NewReader(`{
		"goal": "Become productive with cloud agent execution.",
		"current_level": "frontend engineer new to durable workers",
		"target_role": "AI product engineer",
		"target_stack": "frontend_to_agent",
		"daily_minutes": 30,
		"duration_days": 2,
		"preferences": ["hands-on tasks"],
		"constraints": ["avoid long theory"],
		"background": "React and TypeScript experience."
	}`))
	create.Header.Set("Idempotency-Key", "outline-smoke-key")
	createRecorder := httptest.NewRecorder()

	server.mux.ServeHTTP(createRecorder, create)

	if createRecorder.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d; body=%s", createRecorder.Code, http.StatusAccepted, createRecorder.Body.String())
	}
	var started outline.StartResult
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if started.RunID != "run_outline_smoke" || started.Status != "queued" {
		t.Fatalf("create response = %+v, want queued run_outline_smoke", started)
	}

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("process outline generation job: %v", err)
	}
	if !processed {
		t.Fatal("worker processed = false, want true")
	}
	if workerLLM.request.RunID != "run_outline_smoke" || !workerLLM.request.JSONMode || workerLLM.request.SchemaName != "learning_outline" {
		t.Fatalf("llm request = %+v", workerLLM.request)
	}

	runRequest := httptest.NewRequest(http.MethodGet, "/api/outline-generation-runs/run_outline_smoke", nil)
	runRecorder := httptest.NewRecorder()
	server.mux.ServeHTTP(runRecorder, runRequest)

	if runRecorder.Code != http.StatusOK {
		t.Fatalf("run status = %d, want %d; body=%s", runRecorder.Code, http.StatusOK, runRecorder.Body.String())
	}
	var generated outline.RunResult
	if err := json.Unmarshal(runRecorder.Body.Bytes(), &generated); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if generated.Status != run.StatusSucceeded || generated.OutlineID == "" {
		t.Fatalf("run response = %+v, want succeeded with outline id", generated)
	}

	outlineRequest := httptest.NewRequest(http.MethodGet, "/api/learning-outlines/"+generated.OutlineID, nil)
	outlineRecorder := httptest.NewRecorder()
	server.mux.ServeHTTP(outlineRecorder, outlineRequest)

	if outlineRecorder.Code != http.StatusOK {
		t.Fatalf("outline status = %d, want %d; body=%s", outlineRecorder.Code, http.StatusOK, outlineRecorder.Body.String())
	}
	var public contracts.LearningOutline
	if err := json.Unmarshal(outlineRecorder.Body.Bytes(), &public); err != nil {
		t.Fatalf("decode public outline: %v", err)
	}
	if public.OutlineID != generated.OutlineID || len(public.Tasks) != 2 || public.Tasks[0].TaskTemplateID != "agentcore-d01" {
		t.Fatalf("public outline = %+v", public)
	}
}

func smokeOutlineJSON() string {
	return `{
		"goal": "Become productive with cloud agent execution.",
		"target_role": "AI product engineer",
		"target_stack": "frontend_to_agent",
		"level_band": "default",
		"duration_days": 2,
		"daily_minutes": 30,
		"summary": "Two focused days to connect frontend product instincts to reliable agent execution.",
		"stages": [
			{"id": "stage_1", "title": "Execution basics", "goal": "Understand run and job boundaries.", "days": [1]},
			{"id": "stage_2", "title": "Durability", "goal": "Use events and fences to avoid duplicate work.", "days": [2]}
		],
		"tasks": [
			{
				"day_index": 1,
				"task_template_id": "agentcore-d01",
				"target_stack": "frontend_to_agent",
				"level_band": "default",
				"title": "Trace an accepted run",
				"judge": "Can explain why API acceptance and worker execution are separate.",
				"minutes": 30
			},
			{
				"day_index": 2,
				"task_template_id": "agentcore-d02",
				"target_stack": "frontend_to_agent",
				"level_band": "default",
				"title": "Defend against stale workers",
				"judge": "Can explain how fence and CAS prevent stale worker writes.",
				"minutes": 30
			}
		]
	}`
}
