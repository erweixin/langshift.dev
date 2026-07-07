package content_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"lites/backend/internal/content"
	"lites/backend/internal/event"
	"lites/backend/internal/job"
	"lites/backend/internal/llm"
	"lites/backend/internal/run"
	"lites/backend/internal/testsupport"
)

func TestServiceStartCreatesRunAndContentGenerationJob(t *testing.T) {
	ctx := context.Background()
	pool := testsupport.NewMigratedPool(t, ctx, "test_content")
	events := event.NewService(pool, event.Options{Dispatcher: run.NewReducer()})
	service := content.NewService(events, content.ServiceOptions{
		IDGenerator: testsupport.NewSequenceIDs("run_1"),
		Now:         func() time.Time { return time.Date(2026, 7, 4, 1, 2, 3, 0, time.UTC) },
	})

	result, err := service.Start(ctx, content.StartRequest{
		UserID:         "user_1",
		IdempotencyKey: "same-key",
		Input:          testInput(),
	})
	if err != nil {
		t.Fatalf("start content generation: %v", err)
	}
	if result.RunID != "run_1" || result.Status != "queued" || result.Replayed {
		t.Fatalf("result = %+v, want first queued run_1", result)
	}

	assertRun(t, ctx, pool, "run_1", "lesson_gen", "accepted", 0)
	assertJob(t, ctx, pool, "run_1", "queued")

	replay, err := service.Start(ctx, content.StartRequest{
		UserID:         "user_1",
		IdempotencyKey: "same-key",
		Input:          testInput(),
	})
	if err != nil {
		t.Fatalf("replay content generation: %v", err)
	}
	if replay.RunID != "run_1" || !replay.Replayed {
		t.Fatalf("replay = %+v, want replayed run_1", replay)
	}
	assertJobCount(t, ctx, pool, "run_1", 1)
}

func TestWorkerProcessOneCompletesRunAndJob(t *testing.T) {
	ctx := context.Background()
	pool := testsupport.NewMigratedPool(t, ctx, "test_content")
	events := event.NewService(pool, event.Options{Dispatcher: run.NewReducer()})
	service := content.NewService(events, content.ServiceOptions{
		IDGenerator: testsupport.NewSequenceIDs("run_1"),
	})
	if _, err := service.Start(ctx, content.StartRequest{
		UserID:         "user_1",
		IdempotencyKey: "key-1",
		Input:          testInput(),
	}); err != nil {
		t.Fatalf("start content generation: %v", err)
	}

	fake := &fakeLLM{
		response: llm.Response{
			LedgerID: "ledger_1",
			Content:  validArtifactJSON(),
			Usage:    llm.Usage{InputTokens: 10, OutputTokens: 5},
		},
	}
	worker := content.NewWorker(
		job.NewQueue(pool, job.Options{}),
		events,
		fake,
		content.NewStore(pool),
		content.WorkerOptions{IDGenerator: testsupport.NewSequenceIDs("attempt_1")},
	)

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("process one: %v", err)
	}
	if !processed {
		t.Fatal("processed = false, want true")
	}
	if fake.request.Surface != content.LLMSurfaceGeneration {
		t.Fatalf("llm surface = %q, want %q", fake.request.Surface, content.LLMSurfaceGeneration)
	}
	if fake.request.RunID != "run_1" || fake.request.UserID != "user_1" || fake.request.AttemptKey != "attempt_1" {
		t.Fatalf("llm request = %+v", fake.request)
	}
	if !fake.request.JSONMode || fake.request.SchemaName != "content_artifact" {
		t.Fatalf("llm output mode = json:%t schema:%q", fake.request.JSONMode, fake.request.SchemaName)
	}

	assertRun(t, ctx, pool, "run_1", "lesson_gen", "succeeded", 1)
	assertJob(t, ctx, pool, "run_1", "done")
	contentKey := assertRunSucceededContentKey(t, ctx, pool, "run_1")
	assertContentArtifact(t, ctx, pool, contentKey)
	assertRunEvents(t, ctx, pool, "run_1", []string{
		run.EventRunAccepted,
		run.EventRunQueued,
		run.EventRunStarted,
		run.EventRunSucceeded,
	})
}

func TestServiceStartCacheHitCompletesRunWithoutJob(t *testing.T) {
	ctx := context.Background()
	pool := testsupport.NewMigratedPool(t, ctx, "test_content")
	events := event.NewService(pool, event.Options{Dispatcher: run.NewReducer()})
	store := content.NewStore(pool)
	service := content.NewService(events, content.ServiceOptions{
		IDGenerator: testsupport.NewSequenceIDs("run_1", "run_2"),
		Artifacts:   store,
	})
	if _, err := service.Start(ctx, content.StartRequest{
		UserID:         "user_1",
		IdempotencyKey: "key-1",
		Input:          testInput(),
	}); err != nil {
		t.Fatalf("start first content generation: %v", err)
	}

	worker := content.NewWorker(
		job.NewQueue(pool, job.Options{}),
		events,
		&fakeLLM{response: llm.Response{
			LedgerID: "ledger_1",
			Content:  validArtifactJSON(),
		}},
		store,
		content.WorkerOptions{IDGenerator: testsupport.NewSequenceIDs("attempt_1")},
	)
	if processed, err := worker.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("process first generation = %t/%v, want processed", processed, err)
	}
	contentKey := assertRunSucceededContentKey(t, ctx, pool, "run_1")

	result, err := service.Start(ctx, content.StartRequest{
		UserID:         "user_1",
		IdempotencyKey: "key-2",
		Input:          testInput(),
	})
	if err != nil {
		t.Fatalf("start cached content generation: %v", err)
	}
	if result.RunID != "run_2" || result.Status != "succeeded" || result.ContentKey != contentKey || !result.CacheHit {
		t.Fatalf("cache hit result = %+v, want succeeded run_2 %s", result, contentKey)
	}

	assertRun(t, ctx, pool, "run_2", "lesson_gen", "succeeded", 1)
	assertJobCount(t, ctx, pool, "run_2", 0)
	assertRunSucceededCacheHit(t, ctx, pool, "run_2", contentKey)
	assertRunEvents(t, ctx, pool, "run_2", []string{
		run.EventRunAccepted,
		run.EventRunQueued,
		run.EventRunStarted,
		run.EventRunSucceeded,
	})
}

func TestWorkerProcessOneFailsRunWhenLLMReturnsError(t *testing.T) {
	ctx := context.Background()
	pool := testsupport.NewMigratedPool(t, ctx, "test_content")
	events := event.NewService(pool, event.Options{Dispatcher: run.NewReducer()})
	service := content.NewService(events, content.ServiceOptions{
		IDGenerator: testsupport.NewSequenceIDs("run_1"),
	})
	if _, err := service.Start(ctx, content.StartRequest{
		UserID:         "user_1",
		IdempotencyKey: "key-1",
		Input:          testInput(),
	}); err != nil {
		t.Fatalf("start content generation: %v", err)
	}

	worker := content.NewWorker(
		job.NewQueue(pool, job.Options{}),
		events,
		&fakeLLM{err: fmt.Errorf("provider failed")},
		content.NewStore(pool),
		content.WorkerOptions{IDGenerator: testsupport.NewSequenceIDs("attempt_1")},
	)

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("process one: %v", err)
	}
	if !processed {
		t.Fatal("processed = false, want true")
	}
	assertRun(t, ctx, pool, "run_1", "lesson_gen", "failed", 1)
	assertJob(t, ctx, pool, "run_1", "done")
}

func TestWorkerProcessOneFailsRunWhenValidationFails(t *testing.T) {
	ctx := context.Background()
	pool := testsupport.NewMigratedPool(t, ctx, "test_content")
	events := event.NewService(pool, event.Options{Dispatcher: run.NewReducer()})
	service := content.NewService(events, content.ServiceOptions{
		IDGenerator: testsupport.NewSequenceIDs("run_1"),
	})
	if _, err := service.Start(ctx, content.StartRequest{
		UserID:         "user_1",
		IdempotencyKey: "key-1",
		Input:          testInput(),
	}); err != nil {
		t.Fatalf("start content generation: %v", err)
	}

	worker := content.NewWorker(
		job.NewQueue(pool, job.Options{}),
		events,
		&fakeLLM{response: llm.Response{
			LedgerID: "ledger_1",
			Content:  artifactWithoutJudgeJSON(),
		}},
		content.NewStore(pool),
		content.WorkerOptions{IDGenerator: testsupport.NewSequenceIDs("attempt_1")},
	)

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("process one: %v", err)
	}
	if !processed {
		t.Fatal("processed = false, want true")
	}
	assertRun(t, ctx, pool, "run_1", "lesson_gen", "failed", 1)
	assertJob(t, ctx, pool, "run_1", "done")
	assertArtifactCount(t, ctx, pool, 0)
	assertRunFailedValidation(t, ctx, pool, "run_1")
}

type fakeLLM struct {
	request  llm.Request
	response llm.Response
	err      error
}

func (f *fakeLLM) Complete(_ context.Context, request llm.Request) (llm.Response, error) {
	f.request = request
	if f.err != nil {
		return llm.Response{}, f.err
	}
	return f.response, nil
}

func testInput() content.GenerationInput {
	return content.GenerationInput{
		TaskTemplateID: "fe2agent-d01",
		TargetStack:    "frontend_to_agent",
		LevelBand:      "default",
		Title:          "API boundaries",
		Judge:          "Explain API acceptance versus worker execution.",
		Minutes:        30,
		Context:        json.RawMessage(`{"source":"test"}`),
	}
}

func validArtifactJSON() string {
	return `{
		"lesson": {
			"title": "API boundaries",
			"minutes": 30,
			"judge": "Explain API acceptance versus worker execution.",
			"sections": [
				{
					"id": "section_1",
					"title": "The boundary",
					"body_md": "API acceptance records intent and queues work. Workers execute that work later."
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
				}
			]
		}
	}`
}

func artifactWithoutJudgeJSON() string {
	return `{
		"lesson": {
			"title": "API boundaries",
			"minutes": 30,
			"judge": "Explain API acceptance versus worker execution.",
			"sections": [
				{
					"id": "section_1",
					"title": "The boundary",
					"body_md": "API acceptance records intent and queues work. Workers execute that work later."
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
					"judge": false,
					"label": "explains the boundary"
				}
			]
		}
	}`
}

func assertRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID string, runType string, status string, version int) {
	t.Helper()

	var gotType string
	var gotStatus string
	var gotVersion int
	if err := pool.QueryRow(ctx, `
		SELECT run_type, status, run_version
		FROM agent_runs
		WHERE run_id = $1
	`, runID).Scan(&gotType, &gotStatus, &gotVersion); err != nil {
		t.Fatalf("read run %s: %v", runID, err)
	}
	if gotType != runType || gotStatus != status || gotVersion != version {
		t.Fatalf("run = %s/%s/%d, want %s/%s/%d", gotType, gotStatus, gotVersion, runType, status, version)
	}
}

func assertJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID string, status string) {
	t.Helper()

	var gotStatus string
	if err := pool.QueryRow(ctx, `
		SELECT status
		FROM agent_jobs
		WHERE payload->>'run_id' = $1
	`, runID).Scan(&gotStatus); err != nil {
		t.Fatalf("read job for run %s: %v", runID, err)
	}
	if gotStatus != status {
		t.Fatalf("job status = %q, want %q", gotStatus, status)
	}
}

func assertJobCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID string, want int) {
	t.Helper()

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM agent_jobs
		WHERE payload->>'run_id' = $1
	`, runID).Scan(&count); err != nil {
		t.Fatalf("count jobs for run %s: %v", runID, err)
	}
	if count != want {
		t.Fatalf("job count = %d, want %d", count, want)
	}
}

func assertRunSucceededContentKey(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID string) string {
	t.Helper()

	var contentKey string
	if err := pool.QueryRow(ctx, `
		SELECT payload->>'content_key'
		FROM agent_events
		WHERE run_id = $1 AND type = $2
		ORDER BY seq DESC
		LIMIT 1
	`, runID, run.EventRunSucceeded).Scan(&contentKey); err != nil {
		t.Fatalf("read RunSucceeded content_key: %v", err)
	}
	if contentKey == "" {
		t.Fatal("RunSucceeded content_key is empty")
	}
	return contentKey
}

func assertContentArtifact(t *testing.T, ctx context.Context, pool *pgxpool.Pool, contentKey string) {
	t.Helper()

	var artifactHash string
	var taskTemplateID string
	var title string
	var reviewStatus string
	var validationAttempts int
	if err := pool.QueryRow(ctx, `
		SELECT
			artifact_hash,
			task_template_id,
			artifact->'lesson'->>'title',
			review_status,
			validation_attempts
		FROM content_artifacts
		WHERE content_key = $1
	`, contentKey).Scan(&artifactHash, &taskTemplateID, &title, &reviewStatus, &validationAttempts); err != nil {
		t.Fatalf("read content artifact %s: %v", contentKey, err)
	}
	if artifactHash == "" {
		t.Fatal("artifact_hash is empty")
	}
	if taskTemplateID != "fe2agent-d01" || title != "API boundaries" {
		t.Fatalf("artifact = %s/%s, want fe2agent-d01/API boundaries", taskTemplateID, title)
	}
	if reviewStatus != content.ReviewStatusAutoOK || validationAttempts != 1 {
		t.Fatalf("artifact review = %s/%d, want auto_ok/1", reviewStatus, validationAttempts)
	}
}

func assertArtifactCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int) {
	t.Helper()

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM content_artifacts`).Scan(&count); err != nil {
		t.Fatalf("count content artifacts: %v", err)
	}
	if count != want {
		t.Fatalf("artifact count = %d, want %d", count, want)
	}
}

func assertRunFailedValidation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID string) {
	t.Helper()

	var errorCode string
	var issueCode string
	if err := pool.QueryRow(ctx, `
		SELECT payload->'error'->>'code', payload->'validation'->'issues'->0->>'code'
		FROM agent_events
		WHERE run_id = $1 AND type = $2
		ORDER BY seq DESC
		LIMIT 1
	`, runID, run.EventRunFailed).Scan(&errorCode, &issueCode); err != nil {
		t.Fatalf("read RunFailed validation payload: %v", err)
	}
	if errorCode != "content_validation_failed" || issueCode != "missing_judge_test" {
		t.Fatalf("RunFailed validation = %s/%s, want content_validation_failed/missing_judge_test", errorCode, issueCode)
	}
}

func assertRunSucceededCacheHit(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID string, wantContentKey string) {
	t.Helper()

	var contentKey string
	var cacheHit bool
	if err := pool.QueryRow(ctx, `
		SELECT payload->>'content_key', COALESCE((payload->>'cache_hit')::boolean, false)
		FROM agent_events
		WHERE run_id = $1 AND type = $2
		ORDER BY seq DESC
		LIMIT 1
	`, runID, run.EventRunSucceeded).Scan(&contentKey, &cacheHit); err != nil {
		t.Fatalf("read RunSucceeded cache hit payload: %v", err)
	}
	if contentKey != wantContentKey || !cacheHit {
		t.Fatalf("RunSucceeded cache payload = %s/%t, want %s/true", contentKey, cacheHit, wantContentKey)
	}
}

func assertRunEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID string, want []string) {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT type
		FROM agent_events
		WHERE run_id = $1
		ORDER BY seq
	`, runID)
	if err != nil {
		t.Fatalf("query run events: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatalf("scan event type: %v", err)
		}
		got = append(got, eventType)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate event types: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %#v, want %#v", got, want)
		}
	}
}
