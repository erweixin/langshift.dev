package run_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lites/backend/internal/db"
	"lites/backend/internal/event"
	"lites/backend/internal/run"
)

func TestReducerAppliesRunLifecycleWithCAS(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	service := event.NewService(pool, event.Options{
		Dispatcher: run.NewReducer(),
	})

	accepted := appendRunAccepted(t, ctx, service, "run_1", "user_a", time.Now().Add(time.Hour))
	if accepted.RunVersion != nil {
		t.Fatalf("RunAccepted run version = %v, want nil", accepted.RunVersion)
	}
	assertRunState(t, ctx, pool, "run_1", run.StatusAccepted, 0)

	queued := appendRunEvent(t, ctx, service, "run_1", "user_a", run.EventRunQueued, 0, nil)
	if queued.RunVersion == nil || *queued.RunVersion != 1 {
		t.Fatalf("RunQueued version = %v, want 1", queued.RunVersion)
	}
	assertRunState(t, ctx, pool, "run_1", run.StatusQueued, 1)

	started := appendRunEvent(t, ctx, service, "run_1", "user_a", run.EventRunStarted, 1, nil)
	if started.RunVersion == nil || *started.RunVersion != 2 {
		t.Fatalf("RunStarted version = %v, want 2", started.RunVersion)
	}
	assertRunState(t, ctx, pool, "run_1", run.StatusExecuting, 2)

	succeeded := appendRunEvent(t, ctx, service, "run_1", "user_a", run.EventRunSucceeded, 2, nil)
	if succeeded.RunVersion == nil || *succeeded.RunVersion != 3 {
		t.Fatalf("RunSucceeded version = %v, want 3", succeeded.RunVersion)
	}
	assertRunState(t, ctx, pool, "run_1", run.StatusSucceeded, 3)
	assertEventCount(t, ctx, pool, "user_a", 4)
}

func TestReducerRejectsTerminalRegression(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	service := event.NewService(pool, event.Options{
		Dispatcher: run.NewReducer(),
	})

	appendRunAccepted(t, ctx, service, "run_1", "user_a", time.Now().Add(time.Hour))
	appendRunEvent(t, ctx, service, "run_1", "user_a", run.EventRunQueued, 0, nil)
	appendRunEvent(t, ctx, service, "run_1", "user_a", run.EventRunStarted, 1, nil)
	appendRunEvent(t, ctx, service, "run_1", "user_a", run.EventRunSucceeded, 2, nil)

	_, err := service.Append(ctx, event.AppendRequest{
		Actor:     event.Actor{Kind: event.ActorSystem},
		UserID:    "user_a",
		CommandID: "cmd-regress",
		Aggregate: &event.RunAggregate{
			RunID:           "run_1",
			ExpectedVersion: 3,
		},
		Events: []event.EventDraft{{
			Type:          run.EventRunStarted,
			SchemaVersion: 1,
			RunID:         "run_1",
			Payload:       json.RawMessage(`{"run_id":"run_1"}`),
		}},
	})
	if !errors.Is(err, run.ErrInvalidTransition) {
		t.Fatalf("terminal regression error = %v, want %v", err, run.ErrInvalidTransition)
	}
	assertRunState(t, ctx, pool, "run_1", run.StatusSucceeded, 3)
	assertEventCount(t, ctx, pool, "user_a", 4)
}

func TestReducerRequiresRunCASForTransitions(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	service := event.NewService(pool, event.Options{
		Dispatcher: run.NewReducer(),
	})

	appendRunAccepted(t, ctx, service, "run_1", "user_a", time.Now().Add(time.Hour))

	_, err := service.Append(ctx, event.AppendRequest{
		Actor:     event.Actor{Kind: event.ActorSystem},
		UserID:    "user_a",
		CommandID: "cmd-no-cas",
		Events: []event.EventDraft{{
			Type:          run.EventRunQueued,
			SchemaVersion: 1,
			RunID:         "run_1",
			Payload:       json.RawMessage(`{"run_id":"run_1"}`),
		}},
	})
	if !errors.Is(err, run.ErrMissingRunCAS) {
		t.Fatalf("missing CAS error = %v, want %v", err, run.ErrMissingRunCAS)
	}
	assertRunState(t, ctx, pool, "run_1", run.StatusAccepted, 0)
	assertEventCount(t, ctx, pool, "user_a", 1)
}

func TestReducerRejectsStaleRunVersion(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	service := event.NewService(pool, event.Options{
		Dispatcher: run.NewReducer(),
	})

	appendRunAccepted(t, ctx, service, "run_1", "user_a", time.Now().Add(time.Hour))
	appendRunEvent(t, ctx, service, "run_1", "user_a", run.EventRunQueued, 0, nil)

	_, err := service.Append(ctx, event.AppendRequest{
		Actor:     event.Actor{Kind: event.ActorSystem},
		UserID:    "user_a",
		CommandID: "cmd-stale",
		Aggregate: &event.RunAggregate{
			RunID:           "run_1",
			ExpectedVersion: 0,
		},
		Events: []event.EventDraft{{
			Type:          run.EventRunStarted,
			SchemaVersion: 1,
			RunID:         "run_1",
			Payload:       json.RawMessage(`{"run_id":"run_1"}`),
		}},
	})
	if !errors.Is(err, event.ErrRunVersionConflict) {
		t.Fatalf("stale run version error = %v, want %v", err, event.ErrRunVersionConflict)
	}
	assertRunState(t, ctx, pool, "run_1", run.StatusQueued, 1)
	assertEventCount(t, ctx, pool, "user_a", 2)
}

func TestSweeperExpiresDueRuns(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	service := event.NewService(pool, event.Options{
		Dispatcher: run.NewReducer(),
	})
	sweeper := run.NewSweeper(pool, service, run.SweeperOptions{Limit: 10})

	appendRunAccepted(t, ctx, service, "due_run", "user_a", time.Now().Add(-time.Minute))
	appendRunAccepted(t, ctx, service, "future_run", "user_a", time.Now().Add(time.Hour))

	expired, err := sweeper.ExpireDue(ctx)
	if err != nil {
		t.Fatalf("expire due runs: %v", err)
	}
	if expired != 1 {
		t.Fatalf("expired = %d, want 1", expired)
	}
	assertRunState(t, ctx, pool, "due_run", run.StatusExpired, 1)
	assertRunState(t, ctx, pool, "future_run", run.StatusAccepted, 0)
}

func newTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	rawURL := os.Getenv("LITES_TEST_DATABASE_URL")
	if rawURL == "" {
		t.Skip("set LITES_TEST_DATABASE_URL to run run integration tests")
	}

	admin, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}

	schema := fmt.Sprintf("test_run_%d", time.Now().UnixNano())
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

func appendRunAccepted(t *testing.T, ctx context.Context, service *event.Service, runID string, userID string, dueAt time.Time) event.AppendResult {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"run_id":    runID,
		"run_type":  "review",
		"input_ref": map[string]string{"source": "test"},
		"due_at":    dueAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("marshal RunAccepted payload: %v", err)
	}

	result, err := service.Append(ctx, event.AppendRequest{
		Actor:     event.Actor{Kind: event.ActorSystem},
		UserID:    userID,
		CommandID: "cmd-accept-" + runID,
		Events: []event.EventDraft{{
			Type:          run.EventRunAccepted,
			SchemaVersion: 1,
			RunID:         runID,
			Payload:       payload,
		}},
	})
	if err != nil {
		t.Fatalf("append RunAccepted: %v", err)
	}
	return result
}

func appendRunEvent(t *testing.T, ctx context.Context, service *event.Service, runID string, userID string, eventType string, expectedVersion int, payload json.RawMessage) event.AppendResult {
	t.Helper()

	if len(payload) == 0 {
		payload = json.RawMessage(fmt.Sprintf(`{"run_id":%q}`, runID))
	}
	result, err := service.Append(ctx, event.AppendRequest{
		Actor:     event.Actor{Kind: event.ActorSystem},
		UserID:    userID,
		CommandID: fmt.Sprintf("cmd-%s-%d", eventType, expectedVersion),
		Aggregate: &event.RunAggregate{
			RunID:           runID,
			ExpectedVersion: expectedVersion,
		},
		Events: []event.EventDraft{{
			Type:          eventType,
			SchemaVersion: 1,
			RunID:         runID,
			Payload:       payload,
		}},
	})
	if err != nil {
		t.Fatalf("append %s: %v", eventType, err)
	}
	return result
}

func assertRunState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID string, wantStatus string, wantVersion int) {
	t.Helper()

	var status string
	var version int
	if err := pool.QueryRow(ctx, `
		SELECT status, run_version
		FROM agent_runs
		WHERE run_id = $1
	`, runID).Scan(&status, &version); err != nil {
		t.Fatalf("read run state: %v", err)
	}
	if status != wantStatus || version != wantVersion {
		t.Fatalf("run %s status/version = %s/%d, want %s/%d", runID, status, version, wantStatus, wantVersion)
	}
}

func assertEventCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID string, want int) {
	t.Helper()

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM agent_events
		WHERE user_id = $1
	`, userID).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != want {
		t.Fatalf("event count = %d, want %d", count, want)
	}
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

func TestWithSearchPathKeepsExistingQueryParams(t *testing.T) {
	got := withSearchPath(t, "postgres://user:pass@example.test/db?sslmode=disable", "schema_a")
	if !strings.Contains(got, "sslmode=disable") || !strings.Contains(got, "search_path=schema_a") {
		t.Fatalf("search_path url = %s", got)
	}
}
