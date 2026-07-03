package job

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lites/backend/internal/event"
)

type Queue struct {
	pool          *pgxpool.Pool
	ids           IDGenerator
	leaseDuration time.Duration
	maxAttempts   int
}

func NewQueue(pool *pgxpool.Pool, options Options) *Queue {
	ids := options.IDGenerator
	if ids == nil {
		ids = event.NewULIDGenerator(nil)
	}

	leaseDuration := options.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = defaultLeaseDuration
	}

	maxAttempts := options.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}

	return &Queue{
		pool:          pool,
		ids:           ids,
		leaseDuration: leaseDuration,
		maxAttempts:   maxAttempts,
	}
}

func (q *Queue) Claim(ctx context.Context, kinds []string, workerID string) (Job, event.JobFence, error) {
	if err := q.validate(); err != nil {
		return Job{}, event.JobFence{}, err
	}
	if len(kinds) == 0 {
		return Job{}, event.JobFence{}, fmt.Errorf("%w: at least one kind is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(workerID) == "" {
		return Job{}, event.JobFence{}, fmt.Errorf("%w: worker_id is required", ErrInvalidRequest)
	}

	leaseToken, err := q.ids.NewID()
	if err != nil {
		return Job{}, event.JobFence{}, fmt.Errorf("generate lease token: %w", err)
	}

	var claimed Job
	if err := q.pool.QueryRow(ctx, `
		UPDATE jobs
		SET status = 'leased',
			attempts = attempts + 1,
			lease_until = now() + ($4::bigint * interval '1 millisecond'),
			lease_token = $1,
			leased_by = $2,
			heartbeat_at = now(),
			last_error = NULL,
			updated_at = now()
		WHERE job_id = (
			SELECT job_id
			FROM jobs
			WHERE kind = ANY($3::text[])
				AND due_at <= now()
				AND attempts < $5
				AND (
					status = 'queued'
					OR status = 'failed'
					OR (status = 'leased' AND lease_until <= now())
				)
			ORDER BY due_at, created_at, job_id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING
			job_id, command_id, kind, COALESCE(subject_user_id, ''),
			payload, status, attempts, lease_until, COALESCE(lease_token, ''),
			COALESCE(leased_by, ''), heartbeat_at, due_at,
			COALESCE(last_error, ''), created_at, updated_at
	`, leaseToken, workerID, kinds, q.leaseDuration.Milliseconds(), q.maxAttempts).Scan(
		&claimed.JobID,
		&claimed.CommandID,
		&claimed.Kind,
		&claimed.SubjectUserID,
		&claimed.Payload,
		&claimed.Status,
		&claimed.Attempts,
		&claimed.LeaseUntil,
		&claimed.LeaseToken,
		&claimed.LeasedBy,
		&claimed.HeartbeatAt,
		&claimed.DueAt,
		&claimed.LastError,
		&claimed.CreatedAt,
		&claimed.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, event.JobFence{}, ErrNoJobAvailable
		}
		return Job{}, event.JobFence{}, fmt.Errorf("claim job: %w", err)
	}

	return claimed, event.JobFence{
		JobID:      claimed.JobID,
		LeaseToken: claimed.LeaseToken,
	}, nil
}

func (q *Queue) Heartbeat(ctx context.Context, fence event.JobFence) error {
	if err := q.validate(); err != nil {
		return err
	}
	if err := validateFence(fence); err != nil {
		return err
	}

	tag, err := q.pool.Exec(ctx, `
		UPDATE jobs
		SET lease_until = now() + ($3::bigint * interval '1 millisecond'),
			heartbeat_at = now(),
			updated_at = now()
		WHERE job_id = $1
			AND status = 'leased'
			AND lease_token = $2
			AND lease_until > now()
	`, fence.JobID, fence.LeaseToken, q.leaseDuration.Milliseconds())
	if err != nil {
		return fmt.Errorf("heartbeat job: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrJobFenceInvalid
	}
	return nil
}

func (q *Queue) Fail(ctx context.Context, fence event.JobFence, cause error) error {
	if err := q.validate(); err != nil {
		return err
	}
	if err := validateFence(fence); err != nil {
		return err
	}

	tag, err := q.pool.Exec(ctx, `
		UPDATE jobs
		SET status = CASE WHEN attempts >= $3 THEN 'dead' ELSE 'failed' END,
			lease_until = NULL,
			lease_token = NULL,
			leased_by = NULL,
			heartbeat_at = NULL,
			due_at = now(),
			last_error = $4,
			updated_at = now()
		WHERE job_id = $1
			AND status = 'leased'
			AND lease_token = $2
			AND lease_until > now()
	`, fence.JobID, fence.LeaseToken, q.maxAttempts, truncateError(cause))
	if err != nil {
		return fmt.Errorf("fail job: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrJobFenceInvalid
	}
	return nil
}

func (q *Queue) Reschedule(ctx context.Context, fence event.JobFence, dueAt time.Time) error {
	if err := q.validate(); err != nil {
		return err
	}
	if err := validateFence(fence); err != nil {
		return err
	}
	if dueAt.IsZero() {
		return fmt.Errorf("%w: due_at is required", ErrInvalidRequest)
	}

	tag, err := q.pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'queued',
			lease_until = NULL,
			lease_token = NULL,
			leased_by = NULL,
			heartbeat_at = NULL,
			due_at = $3,
			last_error = NULL,
			updated_at = now()
		WHERE job_id = $1
			AND status = 'leased'
			AND lease_token = $2
			AND lease_until > now()
	`, fence.JobID, fence.LeaseToken, dueAt)
	if err != nil {
		return fmt.Errorf("reschedule job: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrJobFenceInvalid
	}
	return nil
}

func (q *Queue) validate() error {
	if q == nil || q.pool == nil {
		return errMissingPool
	}
	if q.ids == nil {
		return errMissingIDs
	}
	if q.leaseDuration <= 0 {
		return fmt.Errorf("%w: lease duration must be positive", ErrInvalidRequest)
	}
	if q.maxAttempts <= 0 {
		return fmt.Errorf("%w: max attempts must be positive", ErrInvalidRequest)
	}
	return nil
}

func validateFence(fence event.JobFence) error {
	if strings.TrimSpace(fence.JobID) == "" || strings.TrimSpace(fence.LeaseToken) == "" {
		return fmt.Errorf("%w: job_id and lease_token are required", ErrJobFenceInvalid)
	}
	return nil
}

func truncateError(cause error) string {
	if cause == nil {
		return ""
	}
	message := cause.Error()
	if len(message) <= maxLastErrorBytes {
		return message
	}
	return message[:maxLastErrorBytes]
}
