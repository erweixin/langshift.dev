package run

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"lites/backend/internal/event"
)

const defaultSweepLimit = 100

type Sweeper struct {
	pool   *pgxpool.Pool
	events *event.Service
	ids    event.IDGenerator
	limit  int
}

type SweeperOptions struct {
	IDGenerator event.IDGenerator
	Limit       int
}

type dueRun struct {
	RunID      string
	UserID     string
	RunVersion int
}

func NewSweeper(pool *pgxpool.Pool, events *event.Service, options SweeperOptions) *Sweeper {
	ids := options.IDGenerator
	if ids == nil {
		ids = event.NewULIDGenerator(nil)
	}
	limit := options.Limit
	if limit <= 0 {
		limit = defaultSweepLimit
	}
	return &Sweeper{
		pool:   pool,
		events: events,
		ids:    ids,
		limit:  limit,
	}
}

func (s *Sweeper) ExpireDue(ctx context.Context) (int, error) {
	if s == nil || s.pool == nil {
		return 0, errMissingPool
	}
	if s.events == nil {
		return 0, errMissingEvents
	}
	if s.ids == nil {
		return 0, errMissingIDs
	}

	rows, err := s.pool.Query(ctx, `
		SELECT run_id, user_id, run_version
		FROM agent_runs
		WHERE due_at <= now()
			AND status IN (
				'accepted',
				'queued',
				'executing',
				'waiting_tool',
				'waiting_approval'
			)
		ORDER BY due_at, created_at, run_id
		LIMIT $1
	`, s.limit)
	if err != nil {
		return 0, fmt.Errorf("query due runs: %w", err)
	}
	defer rows.Close()

	var due []dueRun
	for rows.Next() {
		var item dueRun
		if err := rows.Scan(&item.RunID, &item.UserID, &item.RunVersion); err != nil {
			return 0, fmt.Errorf("scan due run: %w", err)
		}
		due = append(due, item)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate due runs: %w", err)
	}

	var expired int
	for _, item := range due {
		commandID, err := s.ids.NewID()
		if err != nil {
			return expired, fmt.Errorf("generate sweeper command id: %w", err)
		}
		runExpired, err := ExpiredEvent(item.RunID, "deadline_exceeded", "run exceeded due_at", nil)
		if err != nil {
			return expired, err
		}

		_, err = s.events.Append(ctx, event.NewSystemRunAppend(event.SystemRunAppendRequest{
			UserID:    item.UserID,
			CommandID: commandID,
			Aggregate: event.RunAggregate{
				RunID:           item.RunID,
				ExpectedVersion: item.RunVersion,
			},
			Events: []event.EventDraft{runExpired},
		}))
		if err != nil {
			if errors.Is(err, event.ErrRunVersionConflict) || errors.Is(err, ErrInvalidTransition) {
				continue
			}
			return expired, err
		}
		if err := cancelRunJobs(ctx, s.pool, item.UserID, item.RunID); err != nil {
			return expired, err
		}
		expired++
	}
	return expired, nil
}

func cancelRunJobs(ctx context.Context, pool *pgxpool.Pool, userID string, runID string) error {
	_, err := pool.Exec(ctx, `
		UPDATE agent_jobs
		SET status = 'cancelled',
			lease_until = NULL,
			lease_token = NULL,
			leased_by = NULL,
			heartbeat_at = NULL,
			last_error = 'run expired',
			updated_at = now()
		WHERE subject_user_id = $1
			AND payload->>'run_id' = $2
			AND status IN ('queued', 'leased', 'failed')
	`, userID, runID)
	if err != nil {
		return fmt.Errorf("cancel expired run jobs: %w", err)
	}
	return nil
}
