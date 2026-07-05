package outline_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lites/backend/internal/db"
	"lites/backend/internal/event"
	"lites/backend/internal/job"
	"lites/backend/internal/llm"
	"lites/backend/internal/outline"
	"lites/backend/internal/run"
)

func TestServiceStartCreatesRunAndOutlineGenerationJob(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	events := event.NewService(pool, event.Options{Dispatcher: run.NewReducer()})
	service := outline.NewService(events, outline.ServiceOptions{
		IDGenerator: &sequenceIDs{values: []string{"run_1"}},
		Now:         func() time.Time { return time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC) },
	})

	result, err := service.Start(ctx, outline.StartRequest{
		UserID:         "user_1",
		IdempotencyKey: "same-key",
		Input:          testInput(),
	})
	if err != nil {
		t.Fatalf("start outline generation: %v", err)
	}
	if result.RunID != "run_1" || result.Status != "queued" || result.Replayed {
		t.Fatalf("result = %+v, want first queued run_1", result)
	}

	assertRun(t, ctx, pool, "run_1", "outline_gen", "accepted", 0)
	assertJob(t, ctx, pool, "run_1", "queued")

	replay, err := service.Start(ctx, outline.StartRequest{
		UserID:         "user_1",
		IdempotencyKey: "same-key",
		Input:          testInput(),
	})
	if err != nil {
		t.Fatalf("replay outline generation: %v", err)
	}
	if replay.RunID != "run_1" || !replay.Replayed {
		t.Fatalf("replay = %+v, want replayed run_1", replay)
	}
	assertJobCount(t, ctx, pool, "run_1", 1)
}

func TestWorkerProcessOneCompletesRunJobOutlineAndTasks(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	events := event.NewService(pool, event.Options{Dispatcher: run.NewReducer()})
	service := outline.NewService(events, outline.ServiceOptions{
		IDGenerator: &sequenceIDs{values: []string{"run_1"}},
	})
	if _, err := service.Start(ctx, outline.StartRequest{
		UserID:         "user_1",
		IdempotencyKey: "key-1",
		Input:          testInput(),
	}); err != nil {
		t.Fatalf("start outline generation: %v", err)
	}

	fake := &fakeLLM{
		response: llm.Response{
			LedgerID: "ledger_1",
			Content:  validOutlineJSON(),
			Usage:    llm.Usage{InputTokens: 12, OutputTokens: 34},
		},
	}
	store := outline.NewStore(pool)
	worker := outline.NewWorker(
		job.NewQueue(pool, job.Options{}),
		events,
		fake,
		store,
		outline.WorkerOptions{IDGenerator: &sequenceIDs{values: []string{"attempt_1"}}},
	)

	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("process one: %v", err)
	}
	if !processed {
		t.Fatal("processed = false, want true")
	}
	if fake.request.Surface != outline.LLMSurfaceOutline {
		t.Fatalf("llm surface = %q, want %q", fake.request.Surface, outline.LLMSurfaceOutline)
	}
	if fake.request.RunID != "run_1" || fake.request.UserID != "user_1" || fake.request.AttemptKey != "attempt_1" {
		t.Fatalf("llm request = %+v", fake.request)
	}
	if !fake.request.JSONMode || fake.request.SchemaName != "learning_outline" {
		t.Fatalf("llm output mode = json:%t schema:%q", fake.request.JSONMode, fake.request.SchemaName)
	}

	assertRun(t, ctx, pool, "run_1", "outline_gen", "succeeded", 1)
	assertJob(t, ctx, pool, "run_1", "done")
	outlineID := assertRunSucceededOutlineID(t, ctx, pool, "run_1")
	assertOutline(t, ctx, pool, outlineID)
	assertLearningTasks(t, ctx, pool, outlineID, 2)
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

func testInput() outline.GenerationInput {
	return outline.GenerationInput{
		Goal:         "Become productive with cloud agent execution.",
		CurrentLevel: "frontend engineer new to durable workers",
		TargetRole:   "AI product engineer",
		TargetStack:  "frontend_to_agent",
		DailyMinutes: 30,
		DurationDays: 2,
		Preferences:  []string{"hands-on tasks"},
		Constraints:  []string{"avoid long theory"},
		Background:   "React and TypeScript experience.",
	}
}

func validOutlineJSON() string {
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

func assertRunSucceededOutlineID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID string) string {
	t.Helper()

	var outlineID string
	if err := pool.QueryRow(ctx, `
		SELECT payload->>'outline_id'
		FROM agent_events
		WHERE run_id = $1 AND type = $2
		ORDER BY seq DESC
		LIMIT 1
	`, runID, run.EventRunSucceeded).Scan(&outlineID); err != nil {
		t.Fatalf("read RunSucceeded outline_id: %v", err)
	}
	if outlineID == "" {
		t.Fatal("RunSucceeded outline_id is empty")
	}
	return outlineID
}

func assertOutline(t *testing.T, ctx context.Context, pool *pgxpool.Pool, outlineID string) {
	t.Helper()

	var userID string
	var title string
	if err := pool.QueryRow(ctx, `
		SELECT user_id, outline->>'summary'
		FROM learning_outlines
		WHERE outline_id = $1
	`, outlineID).Scan(&userID, &title); err != nil {
		t.Fatalf("read outline %s: %v", outlineID, err)
	}
	if userID != "user_1" || title == "" {
		t.Fatalf("outline = %s/%q, want user_1 with summary", userID, title)
	}
}

func assertLearningTasks(t *testing.T, ctx context.Context, pool *pgxpool.Pool, outlineID string, want int) {
	t.Helper()

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM learning_tasks
		WHERE outline_id = $1
	`, outlineID).Scan(&count); err != nil {
		t.Fatalf("count learning tasks: %v", err)
	}
	if count != want {
		t.Fatalf("learning task count = %d, want %d", count, want)
	}
}

func newTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	rawURL := os.Getenv("LITES_TEST_DATABASE_URL")
	if rawURL == "" {
		t.Skip("set LITES_TEST_DATABASE_URL to run outline integration tests")
	}

	admin, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}

	schema := fmt.Sprintf("test_outline_%d", time.Now().UnixNano())
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
