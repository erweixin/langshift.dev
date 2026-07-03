package content_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lites/backend/internal/content"
	"lites/backend/internal/db"
	"lites/backend/internal/event"
	"lites/backend/internal/job"
	"lites/backend/internal/llm"
	"lites/backend/internal/run"
)

func TestServiceStartCreatesRunAndContentGenerationJob(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	events := event.NewService(pool, event.Options{Dispatcher: run.NewReducer()})
	service := content.NewService(events, content.ServiceOptions{
		IDGenerator: &sequenceIDs{values: []string{"run_1"}},
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
	pool := newTestPool(t, ctx)
	events := event.NewService(pool, event.Options{Dispatcher: run.NewReducer()})
	service := content.NewService(events, content.ServiceOptions{
		IDGenerator: &sequenceIDs{values: []string{"run_1"}},
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
			Content:  `{"schema_version":1}`,
			Usage:    llm.Usage{InputTokens: 10, OutputTokens: 5},
		},
	}
	worker := content.NewWorker(
		job.NewQueue(pool, job.Options{}),
		events,
		fake,
		content.WorkerOptions{IDGenerator: &sequenceIDs{values: []string{"attempt_1"}}},
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
	assertRunEvents(t, ctx, pool, "run_1", []string{
		run.EventRunAccepted,
		run.EventRunQueued,
		run.EventRunStarted,
		run.EventRunSucceeded,
	})
}

func TestWorkerProcessOneFailsRunWhenLLMReturnsError(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	events := event.NewService(pool, event.Options{Dispatcher: run.NewReducer()})
	service := content.NewService(events, content.ServiceOptions{
		IDGenerator: &sequenceIDs{values: []string{"run_1"}},
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
		content.WorkerOptions{IDGenerator: &sequenceIDs{values: []string{"attempt_1"}}},
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

type sequenceIDs struct {
	values []string
	next   int
}

func (g *sequenceIDs) NewID() (string, error) {
	if g.next >= len(g.values) {
		id := fmt.Sprintf("generated_%d", g.next)
		g.next++
		return id, nil
	}
	id := g.values[g.next]
	g.next++
	return id, nil
}

func assertRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID string, runType string, status string, version int) {
	t.Helper()

	var gotType string
	var gotStatus string
	var gotVersion int
	if err := pool.QueryRow(ctx, `
		SELECT run_type, status, run_version
		FROM runs
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
		FROM jobs
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
		FROM jobs
		WHERE payload->>'run_id' = $1
	`, runID).Scan(&count); err != nil {
		t.Fatalf("count jobs for run %s: %v", runID, err)
	}
	if count != want {
		t.Fatalf("job count = %d, want %d", count, want)
	}
}

func assertRunEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID string, want []string) {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT type
		FROM events
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

func newTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	rawURL := os.Getenv("LITES_TEST_DATABASE_URL")
	if rawURL == "" {
		t.Skip("set LITES_TEST_DATABASE_URL to run content integration tests")
	}

	admin, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}

	schema := fmt.Sprintf("test_content_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatalf("create test schema: %v", err)
	}

	testURL := withSearchPath(t, rawURL, schema)
	if err := db.RunMigrations(testURL, migrationsDir(t)); err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
		t.Fatalf("run migrations: %v", err)
	}

	pool, err := db.NewPool(ctx, testURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
		t.Fatalf("connect test database: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
	})

	return pool
}

func withSearchPath(t *testing.T, rawURL string, schema string) string {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse database url: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func migrationsDir(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve caller path")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..", "migrations")
}
