package job_test

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
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lites/backend/internal/db"
	"lites/backend/internal/event"
	"lites/backend/internal/job"
)

func TestClaimLeasesOneJobOnce(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	queue := job.NewQueue(pool, job.Options{
		LeaseDuration: time.Minute,
		MaxAttempts:   3,
	})
	insertJob(t, ctx, pool, "job_1", "command_1", "review", "user_a", json.RawMessage(`{"n":1}`))

	type claimResult struct {
		job job.Job
		err error
	}
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for _, workerID := range []string{"worker_a", "worker_b"} {
		wg.Add(1)
		go func(workerID string) {
			defer wg.Done()
			claimed, _, err := queue.Claim(ctx, []string{"review"}, workerID)
			results <- claimResult{job: claimed, err: err}
		}(workerID)
	}
	wg.Wait()
	close(results)

	var claimedCount int
	var noJobCount int
	for result := range results {
		switch {
		case result.err == nil:
			claimedCount++
			if result.job.JobID != "job_1" {
				t.Fatalf("claimed job id = %q, want job_1", result.job.JobID)
			}
		case errors.Is(result.err, job.ErrNoJobAvailable):
			noJobCount++
		default:
			t.Fatalf("claim error = %v", result.err)
		}
	}
	if claimedCount != 1 || noJobCount != 1 {
		t.Fatalf("claimed=%d no_job=%d, want 1 and 1", claimedCount, noJobCount)
	}

	stored := readJob(t, ctx, pool, "job_1")
	if stored.Status != "leased" || stored.Attempts != 1 || stored.LeaseToken == "" {
		t.Fatalf("stored job after claim = %+v, want leased attempt 1 with token", stored)
	}
}

func TestFenceOperationsRejectWrongTokenAndDeadLetter(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	queue := job.NewQueue(pool, job.Options{
		LeaseDuration: time.Minute,
		MaxAttempts:   2,
	})
	insertJob(t, ctx, pool, "job_1", "command_1", "review", "user_a", json.RawMessage(`{"n":1}`))

	_, fence, err := queue.Claim(ctx, []string{"review"}, "worker_a")
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}

	wrongFence := event.JobFence{JobID: fence.JobID, LeaseToken: "wrong-token"}
	if err := queue.Heartbeat(ctx, wrongFence); !errors.Is(err, job.ErrJobFenceInvalid) {
		t.Fatalf("wrong heartbeat error = %v, want %v", err, job.ErrJobFenceInvalid)
	}
	if err := queue.Fail(ctx, wrongFence, errors.New("bad token")); !errors.Is(err, job.ErrJobFenceInvalid) {
		t.Fatalf("wrong fail error = %v, want %v", err, job.ErrJobFenceInvalid)
	}
	if err := queue.Heartbeat(ctx, fence); err != nil {
		t.Fatalf("heartbeat with correct fence: %v", err)
	}
	if err := queue.Fail(ctx, fence, errors.New("first failure")); err != nil {
		t.Fatalf("fail with correct fence: %v", err)
	}

	stored := readJob(t, ctx, pool, "job_1")
	if stored.Status != "failed" || stored.LastError != "first failure" || stored.LeaseToken != "" {
		t.Fatalf("stored job after first fail = %+v, want failed without lease", stored)
	}

	_, secondFence, err := queue.Claim(ctx, []string{"review"}, "worker_b")
	if err != nil {
		t.Fatalf("claim failed job: %v", err)
	}
	if err := queue.Fail(ctx, secondFence, errors.New("second failure")); err != nil {
		t.Fatalf("fail second attempt: %v", err)
	}

	stored = readJob(t, ctx, pool, "job_1")
	if stored.Status != "dead" || stored.LastError != "second failure" || stored.LeaseToken != "" {
		t.Fatalf("stored job after second fail = %+v, want dead without lease", stored)
	}
}

func TestExpiredLeaseCanBeReclaimed(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	queue := job.NewQueue(pool, job.Options{
		LeaseDuration: time.Minute,
		MaxAttempts:   3,
	})
	insertLeasedJob(t, ctx, pool, "job_1", "command_1", "review", "old-token", time.Now().Add(-time.Minute), 1)

	claimed, fence, err := queue.Claim(ctx, []string{"review"}, "worker_b")
	if err != nil {
		t.Fatalf("claim expired leased job: %v", err)
	}
	if claimed.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", claimed.Attempts)
	}
	if fence.LeaseToken == "old-token" || fence.LeaseToken == "" {
		t.Fatalf("new fence token = %q, want fresh token", fence.LeaseToken)
	}
	if claimed.LeasedBy != "worker_b" {
		t.Fatalf("leased_by = %q, want worker_b", claimed.LeasedBy)
	}
}

func TestRescheduleDelaysJob(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	queue := job.NewQueue(pool, job.Options{
		LeaseDuration: time.Minute,
		MaxAttempts:   3,
	})
	insertJob(t, ctx, pool, "job_1", "command_1", "review", "user_a", json.RawMessage(`{"n":1}`))

	_, fence, err := queue.Claim(ctx, []string{"review"}, "worker_a")
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	dueAt := time.Now().Add(time.Hour).UTC()
	if err := queue.Reschedule(ctx, fence, dueAt); err != nil {
		t.Fatalf("reschedule job: %v", err)
	}

	if _, _, err := queue.Claim(ctx, []string{"review"}, "worker_b"); !errors.Is(err, job.ErrNoJobAvailable) {
		t.Fatalf("claim rescheduled future job error = %v, want %v", err, job.ErrNoJobAvailable)
	}

	stored := readJob(t, ctx, pool, "job_1")
	if stored.Status != "queued" || stored.LeaseToken != "" {
		t.Fatalf("stored job after reschedule = %+v, want queued without lease", stored)
	}
	if stored.DueAt.Before(dueAt.Add(-time.Second)) {
		t.Fatalf("due_at = %s, want near or after %s", stored.DueAt, dueAt)
	}
}

func TestClaimedFenceWorksWithEventAppend(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	queue := job.NewQueue(pool, job.Options{
		LeaseDuration: time.Minute,
		MaxAttempts:   3,
	})
	eventService := event.NewService(pool, event.Options{})

	created, err := eventService.Append(ctx, event.AppendRequest{
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

	claimed, fence, err := queue.Claim(ctx, []string{"review"}, "worker_a")
	if err != nil {
		t.Fatalf("claim command job: %v", err)
	}
	if claimed.CommandID != created.Commands[0].CommandID {
		t.Fatalf("claimed command id = %q, want %q", claimed.CommandID, created.Commands[0].CommandID)
	}

	result, err := eventService.Append(ctx, event.AppendRequest{
		Actor:  event.Actor{Kind: event.ActorWorker},
		UserID: "user_a",
		JobFence: &event.JobFence{
			JobID:      fence.JobID,
			LeaseToken: fence.LeaseToken,
		},
		Events: []event.EventDraft{{
			Type:          "ReviewCompleted",
			SchemaVersion: 1,
			Payload:       json.RawMessage(`{"evidence_id":"ev_1","summary":"ok"}`),
		}},
	})
	if err != nil {
		t.Fatalf("worker append with claimed fence: %v", err)
	}
	if result.CommandID != claimed.CommandID {
		t.Fatalf("append command id = %q, want claimed command id %q", result.CommandID, claimed.CommandID)
	}

	stored := readJob(t, ctx, pool, claimed.JobID)
	if stored.Status != "done" || stored.LeaseToken != "" {
		t.Fatalf("stored job after EventService append = %+v, want done without lease", stored)
	}
}

func newTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	rawURL := os.Getenv("LITES_TEST_DATABASE_URL")
	if rawURL == "" {
		t.Skip("set LITES_TEST_DATABASE_URL to run job integration tests")
	}

	admin, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}

	schema := fmt.Sprintf("test_job_%d", time.Now().UnixNano())
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

func insertJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jobID string, commandID string, kind string, subjectUserID string, payload json.RawMessage) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO agent_jobs (job_id, command_id, kind, subject_user_id, payload)
		VALUES ($1, $2, $3, nullif($4, ''), $5::jsonb)
	`, jobID, commandID, kind, subjectUserID, string(payload))
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}
}

func insertLeasedJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jobID string, commandID string, kind string, leaseToken string, leaseUntil time.Time, attempts int) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO agent_jobs (
			job_id, command_id, kind, status, attempts, lease_until,
			lease_token, leased_by, heartbeat_at
		)
		VALUES ($1, $2, $3, 'leased', $4, $5, $6, 'old-worker', now())
	`, jobID, commandID, kind, attempts, leaseUntil, leaseToken)
	if err != nil {
		t.Fatalf("insert leased job: %v", err)
	}
}

func readJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jobID string) job.Job {
	t.Helper()

	var stored job.Job
	if err := pool.QueryRow(ctx, `
		SELECT
			job_id, command_id, kind, COALESCE(subject_user_id, ''),
			payload, status, attempts, lease_until, COALESCE(lease_token, ''),
			COALESCE(leased_by, ''), heartbeat_at, due_at,
			COALESCE(last_error, ''), created_at, updated_at
		FROM agent_jobs
		WHERE job_id = $1
	`, jobID).Scan(
		&stored.JobID,
		&stored.CommandID,
		&stored.Kind,
		&stored.SubjectUserID,
		&stored.Payload,
		&stored.Status,
		&stored.Attempts,
		&stored.LeaseUntil,
		&stored.LeaseToken,
		&stored.LeasedBy,
		&stored.HeartbeatAt,
		&stored.DueAt,
		&stored.LastError,
		&stored.CreatedAt,
		&stored.UpdatedAt,
	); err != nil {
		t.Fatalf("read job %s: %v", jobID, err)
	}
	return stored
}

func TestWithSearchPathKeepsExistingQueryParams(t *testing.T) {
	got := withSearchPath(t, "postgres://user:pass@example.test/db?sslmode=disable", "schema_a")
	if !strings.Contains(got, "sslmode=disable") || !strings.Contains(got, "search_path=schema_a") {
		t.Fatalf("search_path url = %s", got)
	}
}
