package event_test

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
)

func TestAppendAllocatesUserScopedSeq(t *testing.T) {
	ctx := context.Background()
	service, pool := newTestService(t, ctx)

	first, err := service.Append(ctx, event.AppendRequest{
		Actor:     event.Actor{Kind: event.ActorSystem},
		UserID:    "user_a",
		CommandID: "system-a-1",
		Events: []event.EventDraft{{
			Type:          "TestStarted",
			SchemaVersion: 1,
			Payload:       json.RawMessage(`{"step":1}`),
		}},
	})
	if err != nil {
		t.Fatalf("append first event: %v", err)
	}
	if got := first.Events[0].Seq; got != 1 {
		t.Fatalf("first seq = %d, want 1", got)
	}

	second, err := service.Append(ctx, event.AppendRequest{
		Actor:     event.Actor{Kind: event.ActorSystem},
		UserID:    "user_a",
		CommandID: "system-a-2",
		Events: []event.EventDraft{{
			Type:          "TestContinued",
			SchemaVersion: 1,
			Payload:       json.RawMessage(`{"step":2}`),
		}},
	})
	if err != nil {
		t.Fatalf("append second event: %v", err)
	}
	if got := second.Events[0].Seq; got != 2 {
		t.Fatalf("second seq = %d, want 2", got)
	}

	otherUser, err := service.Append(ctx, event.AppendRequest{
		Actor:     event.Actor{Kind: event.ActorSystem},
		UserID:    "user_b",
		CommandID: "system-b-1",
		Events: []event.EventDraft{{
			Type:          "TestStarted",
			SchemaVersion: 1,
			Payload:       json.RawMessage(`{"step":1}`),
		}},
	})
	if err != nil {
		t.Fatalf("append other user event: %v", err)
	}
	if got := otherUser.Events[0].Seq; got != 1 {
		t.Fatalf("other user seq = %d, want 1", got)
	}

	assertEventCount(t, ctx, pool, "user_a", 2)
	assertEventCount(t, ctx, pool, "user_b", 1)
}

func TestAppendIdempotencyReplayAndHashConflict(t *testing.T) {
	ctx := context.Background()
	service, pool := newTestService(t, ctx)

	request := event.AppendRequest{
		Actor:  event.Actor{Kind: event.ActorUser},
		UserID: "user_a",
		Idempotency: &event.Idempotency{
			Scope:       "POST /api/test",
			Key:         "same-key",
			RequestHash: "hash-a",
		},
		IdempotencyResponse: &event.IdempotencyResponse{
			Status: 202,
			Body:   json.RawMessage(`{"accepted":true}`),
		},
		Events: []event.EventDraft{{
			Type:          "EvidenceSubmitted",
			SchemaVersion: 1,
			Payload:       json.RawMessage(`{"evidence_id":"ev_1"}`),
		}},
	}

	first, err := service.Append(ctx, request)
	if err != nil {
		t.Fatalf("append idempotent request: %v", err)
	}
	if first.Replayed {
		t.Fatal("first request was marked as replay")
	}

	replay, err := service.Append(ctx, request)
	if err != nil {
		t.Fatalf("replay idempotent request: %v", err)
	}
	if !replay.Replayed {
		t.Fatal("second request was not marked as replay")
	}
	if replay.CommandID != first.CommandID {
		t.Fatalf("replay command id = %q, want %q", replay.CommandID, first.CommandID)
	}
	if replay.IdempotencyResponse.Status != 202 || string(replay.IdempotencyResponse.Body) != `{"accepted": true}` {
		t.Fatalf("unexpected replay response: status=%d body=%s", replay.IdempotencyResponse.Status, replay.IdempotencyResponse.Body)
	}
	assertEventCount(t, ctx, pool, "user_a", 1)

	conflicting := request
	conflicting.Idempotency = &event.Idempotency{
		Scope:       request.Idempotency.Scope,
		Key:         request.Idempotency.Key,
		RequestHash: "hash-b",
	}
	if _, err := service.Append(ctx, conflicting); !errors.Is(err, event.ErrIdempotencyConflict) {
		t.Fatalf("conflicting idempotency error = %v, want %v", err, event.ErrIdempotencyConflict)
	}
	assertEventCount(t, ctx, pool, "user_a", 1)
}

func TestAppendEnqueuesCommandsAndWorkerFenceMarksJobDone(t *testing.T) {
	ctx := context.Background()
	service, pool := newTestService(t, ctx)

	created, err := service.Append(ctx, event.AppendRequest{
		Actor:     event.Actor{Kind: event.ActorSystem},
		UserID:    "user_a",
		CommandID: "system-command",
		Commands: []event.CommandDraft{{
			Kind:    "review",
			Payload: json.RawMessage(`{"evidence_id":"ev_1"}`),
		}},
	})
	if err != nil {
		t.Fatalf("append command: %v", err)
	}
	if len(created.Commands) != 1 {
		t.Fatalf("created commands = %d, want 1", len(created.Commands))
	}
	job := created.Commands[0]

	leaseJob(t, ctx, pool, job.JobID, "lease-1", time.Now().Add(5*time.Minute))

	workerResult, err := service.Append(ctx, event.AppendRequest{
		Actor:  event.Actor{Kind: event.ActorWorker},
		UserID: "user_a",
		JobFence: &event.JobFence{
			JobID:      job.JobID,
			LeaseToken: "lease-1",
		},
		Events: []event.EventDraft{{
			Type:          "ReviewCompleted",
			SchemaVersion: 1,
			Payload:       json.RawMessage(`{"evidence_id":"ev_1","summary":"ok"}`),
		}},
	})
	if err != nil {
		t.Fatalf("worker append: %v", err)
	}
	if workerResult.CommandID != job.CommandID {
		t.Fatalf("worker command id = %q, want %q", workerResult.CommandID, job.CommandID)
	}

	var status string
	var leaseToken *string
	if err := pool.QueryRow(ctx, `
		SELECT status, lease_token
		FROM jobs
		WHERE job_id = $1
	`, job.JobID).Scan(&status, &leaseToken); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if status != "done" || leaseToken != nil {
		t.Fatalf("job status=%q lease_token=%v, want done with no lease", status, leaseToken)
	}

	var eventCommandID string
	if err := pool.QueryRow(ctx, `
		SELECT command_id
		FROM events
		WHERE type = 'ReviewCompleted'
	`).Scan(&eventCommandID); err != nil {
		t.Fatalf("read review event command id: %v", err)
	}
	if eventCommandID != job.CommandID {
		t.Fatalf("event command id = %q, want %q", eventCommandID, job.CommandID)
	}
}

func TestAppendRejectsWrongOrExpiredJobFence(t *testing.T) {
	ctx := context.Background()
	service, pool := newTestService(t, ctx)

	created, err := service.Append(ctx, event.AppendRequest{
		Actor:     event.Actor{Kind: event.ActorSystem},
		UserID:    "user_a",
		CommandID: "system-command",
		Commands: []event.CommandDraft{{
			Kind:    "review",
			Payload: json.RawMessage(`{"evidence_id":"ev_1"}`),
		}},
	})
	if err != nil {
		t.Fatalf("append command: %v", err)
	}
	job := created.Commands[0]
	leaseJob(t, ctx, pool, job.JobID, "lease-1", time.Now().Add(5*time.Minute))

	_, err = service.Append(ctx, event.AppendRequest{
		Actor:  event.Actor{Kind: event.ActorWorker},
		UserID: "user_a",
		JobFence: &event.JobFence{
			JobID:      job.JobID,
			LeaseToken: "wrong-token",
		},
		Events: []event.EventDraft{{
			Type:          "ReviewCompleted",
			SchemaVersion: 1,
			Payload:       json.RawMessage(`{"evidence_id":"ev_1"}`),
		}},
	})
	if !errors.Is(err, event.ErrJobFenceInvalid) {
		t.Fatalf("wrong token error = %v, want %v", err, event.ErrJobFenceInvalid)
	}
	assertEventCount(t, ctx, pool, "user_a", 0)

	leaseJob(t, ctx, pool, job.JobID, "lease-2", time.Now().Add(-time.Minute))
	_, err = service.Append(ctx, event.AppendRequest{
		Actor:  event.Actor{Kind: event.ActorWorker},
		UserID: "user_a",
		JobFence: &event.JobFence{
			JobID:      job.JobID,
			LeaseToken: "lease-2",
		},
		Events: []event.EventDraft{{
			Type:          "ReviewCompleted",
			SchemaVersion: 1,
			Payload:       json.RawMessage(`{"evidence_id":"ev_1"}`),
		}},
	})
	if !errors.Is(err, event.ErrJobFenceInvalid) {
		t.Fatalf("expired lease error = %v, want %v", err, event.ErrJobFenceInvalid)
	}
	assertEventCount(t, ctx, pool, "user_a", 0)
}

func TestAppendRunCAS(t *testing.T) {
	ctx := context.Background()
	service, pool := newTestService(t, ctx)

	if _, err := pool.Exec(ctx, `
		INSERT INTO runs (run_id, user_id, run_type, status, run_version)
		VALUES ('run_1', 'user_a', 'review', 'accepted', 0)
	`); err != nil {
		t.Fatalf("insert run: %v", err)
	}

	result, err := service.Append(ctx, event.AppendRequest{
		Actor:     event.Actor{Kind: event.ActorSystem},
		UserID:    "user_a",
		CommandID: "system-command",
		Aggregate: &event.RunAggregate{
			RunID:           "run_1",
			ExpectedVersion: 0,
		},
		Events: []event.EventDraft{{
			Type:          "RunQueued",
			SchemaVersion: 1,
			RunID:         "run_1",
			Payload:       json.RawMessage(`{"run_id":"run_1"}`),
		}},
	})
	if err != nil {
		t.Fatalf("append with run CAS: %v", err)
	}
	if result.RunVersion == nil || *result.RunVersion != 1 {
		t.Fatalf("run version = %v, want 1", result.RunVersion)
	}

	_, err = service.Append(ctx, event.AppendRequest{
		Actor:     event.Actor{Kind: event.ActorSystem},
		UserID:    "user_a",
		CommandID: "system-command-2",
		Aggregate: &event.RunAggregate{
			RunID:           "run_1",
			ExpectedVersion: 0,
		},
		Events: []event.EventDraft{{
			Type:          "RunStarted",
			SchemaVersion: 1,
			RunID:         "run_1",
			Payload:       json.RawMessage(`{"run_id":"run_1"}`),
		}},
	})
	if !errors.Is(err, event.ErrRunVersionConflict) {
		t.Fatalf("stale run CAS error = %v, want %v", err, event.ErrRunVersionConflict)
	}
	assertEventCount(t, ctx, pool, "user_a", 1)
}

func newTestService(t *testing.T, ctx context.Context) (*event.Service, *pgxpool.Pool) {
	t.Helper()

	rawURL := os.Getenv("LITES_TEST_DATABASE_URL")
	if rawURL == "" {
		t.Skip("set LITES_TEST_DATABASE_URL to run event integration tests")
	}

	admin, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}

	schema := fmt.Sprintf("test_event_%d", time.Now().UnixNano())
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

	return event.NewService(pool, event.Options{}), pool
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

func assertEventCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID string, want int) {
	t.Helper()

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM events
		WHERE user_id = $1
	`, userID).Scan(&count); err != nil {
		t.Fatalf("count events for %s: %v", userID, err)
	}
	if count != want {
		t.Fatalf("event count for %s = %d, want %d", userID, count, want)
	}
}

func leaseJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jobID string, leaseToken string, leaseUntil time.Time) {
	t.Helper()

	tag, err := pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'leased',
			lease_token = $2,
			lease_until = $3,
			leased_by = 'test-worker',
			heartbeat_at = now(),
			attempts = attempts + 1,
			updated_at = now()
		WHERE job_id = $1
	`, jobID, leaseToken, leaseUntil)
	if err != nil {
		t.Fatalf("lease job: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("lease job rows = %d, want 1", tag.RowsAffected())
	}
}

func TestWithSearchPathKeepsExistingQueryParams(t *testing.T) {
	got := withSearchPath(t, "postgres://user:pass@example.test/db?sslmode=disable", "schema_a")
	if !strings.Contains(got, "sslmode=disable") || !strings.Contains(got, "search_path=schema_a") {
		t.Fatalf("search_path url = %s", got)
	}
}
