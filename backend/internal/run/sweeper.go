package run

import (
	"context"
	"encoding/json"
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
		payload, err := json.Marshal(map[string]any{
			"run_id": item.RunID,
			"error": map[string]string{
				"code":    "deadline_exceeded",
				"message": "run exceeded due_at",
			},
		})
		if err != nil {
			return expired, fmt.Errorf("marshal RunExpired payload: %w", err)
		}

		_, err = s.events.Append(ctx, event.AppendRequest{
			Actor:     event.Actor{Kind: event.ActorSystem},
			UserID:    item.UserID,
			CommandID: commandID,
			Aggregate: &event.RunAggregate{
				RunID:           item.RunID,
				ExpectedVersion: item.RunVersion,
			},
			Events: []event.EventDraft{{
				Type:          EventRunExpired,
				SchemaVersion: 1,
				RunID:         item.RunID,
				Payload:       payload,
			}},
		})
		if err != nil {
			if errors.Is(err, event.ErrRunVersionConflict) || errors.Is(err, ErrInvalidTransition) {
				continue
			}
			return expired, err
		}
		expired++
	}
	return expired, nil
}
