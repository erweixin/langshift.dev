// Package postgres provides the tenant-user scoped EventStore read boundary
// used by realtime backfill. Live notifications never bypass this reader.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrConfiguration = errors.New("realtime event reader is not configured")
	ErrInvalidCursor = errors.New("realtime cursor is invalid")
)

type Event struct {
	ID               string    `json:"id"`
	Sequence         uint64    `json:"seq"`
	EventType        string    `json:"event_type"`
	SchemaVersion    int       `json:"event_schema_version"`
	AggregateKind    string    `json:"aggregate_kind"`
	AggregateID      string    `json:"aggregate_id"`
	AggregateVersion uint64    `json:"aggregate_version"`
	StoreEpoch       string    `json:"store_epoch"`
	OccurredAt       time.Time `json:"occurred_at"`
	CommittedAt      time.Time `json:"committed_at"`
	CausationID      *string   `json:"causation_id"`
	CorrelationID    string    `json:"correlation_id"`
	Actor            []byte    `json:"-"`
	PayloadRef       string    `json:"-"`
	PayloadHash      string    `json:"-"`
}

type Reader struct{ Pool *pgxpool.Pool }

func (reader Reader) HighWatermark(ctx context.Context, tenantID, userID string) (uint64, error) {
	if reader.Pool == nil || tenantID == "" || userID == "" {
		return 0, ErrConfiguration
	}
	tx, err := reader.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return 0, err
	}
	var high uint64
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT last_seq FROM agent.event_cursors WHERE tenant_id=$1 AND user_id=$2),0)`, tenantID, userID).Scan(&high)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return high, nil
}

func (reader Reader) List(ctx context.Context, tenantID, userID string, after, through uint64, limit int) ([]Event, error) {
	if reader.Pool == nil || tenantID == "" || userID == "" {
		return nil, ErrConfiguration
	}
	if through < after || limit < 1 || limit > 1000 {
		return nil, ErrInvalidCursor
	}
	if through == after {
		return []Event{}, nil
	}
	tx, err := reader.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,seq,event_type,event_schema_version,aggregate_kind,aggregate_id::text,aggregate_version,store_epoch::text,occurred_at,committed_at,actor,COALESCE(causation_id::text,''),correlation_id::text,payload_ref,payload_hash FROM agent.events WHERE tenant_id=$1 AND user_id=$2 AND seq>$3 AND seq<=$4 ORDER BY seq LIMIT $5`, tenantID, userID, after, through, limit)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, limit)
	for rows.Next() {
		var event Event
		var causation string
		if err = rows.Scan(&event.ID, &event.Sequence, &event.EventType, &event.SchemaVersion, &event.AggregateKind, &event.AggregateID, &event.AggregateVersion, &event.StoreEpoch, &event.OccurredAt, &event.CommittedAt, &event.Actor, &causation, &event.CorrelationID, &event.PayloadRef, &event.PayloadHash); err != nil {
			rows.Close()
			return nil, err
		}
		if causation != "" {
			event.CausationID = &causation
		}
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return events, nil
}
