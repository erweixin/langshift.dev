// Package postgres implements the transactional PostgreSQL EventStore append
// boundary. Callers lock and CAS the business aggregate before invoking Append;
// this appender then allocates the tenant-user sequence and writes event plus
// outbox command in the same transaction.
package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidAppend   = errors.New("event append input is invalid")
	ErrVersionConflict = errors.New("event aggregate version conflict")
	ErrCommandConflict = errors.New("event outbox command conflict")
)

type Event struct {
	ID               string
	TenantID         string
	UserID           string
	EventType        string
	SchemaVersion    int
	AggregateKind    string
	AggregateID      string
	AggregateVersion uint64
	StoreEpoch       string
	OccurredAt       time.Time
	Actor            json.RawMessage
	CausationID      *string
	CorrelationID    string
	PayloadRef       string
	PayloadHash      string
}

type OutboxCommand struct {
	ID          string
	CommandID   string
	CommandType string
	PayloadRef  string
	PayloadHash string
}

type Input struct {
	Event   Event
	Command OutboxCommand
}

type Result struct {
	Sequence uint64
	Replayed bool
}

type Appender struct{ Now func() time.Time }

func (appender Appender) Append(ctx context.Context, tx pgx.Tx, input Input) (Result, error) {
	if tx == nil || !validInput(input) {
		return Result{}, ErrInvalidAppend
	}
	now := time.Now().UTC()
	if appender.Now != nil {
		now = appender.Now().UTC()
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, input.Event.TenantID); err != nil {
		return Result{}, err
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT lites_event_append`); err != nil {
		return Result{}, err
	}
	var sequence uint64
	err := tx.QueryRow(ctx, `INSERT INTO agent.event_cursors (tenant_id,user_id,last_seq) VALUES ($1,$2,1) ON CONFLICT (tenant_id,user_id) DO UPDATE SET last_seq=agent.event_cursors.last_seq+1 RETURNING last_seq`, input.Event.TenantID, input.Event.UserID).Scan(&sequence)
	if err != nil {
		return Result{}, appender.rollback(ctx, tx, err)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.events (id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,causation_id,correlation_id,payload_ref,payload_hash) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) ON CONFLICT DO NOTHING`, input.Event.ID, input.Event.TenantID, input.Event.UserID, sequence, input.Event.EventType, input.Event.SchemaVersion, input.Event.AggregateKind, input.Event.AggregateID, input.Event.AggregateVersion, input.Event.StoreEpoch, input.Event.OccurredAt.UTC(), now, input.Event.Actor, input.Event.CausationID, input.Event.CorrelationID, input.Event.PayloadRef, input.Event.PayloadHash)
	if err != nil {
		return Result{}, appender.rollback(ctx, tx, err)
	}
	if tag.RowsAffected() == 0 {
		if err = appender.rollbackToSavepoint(ctx, tx); err != nil {
			return Result{}, err
		}
		result, replayed, replayErr := loadReplay(ctx, tx, input.Event)
		if replayErr != nil {
			return Result{}, replayErr
		}
		if !replayed {
			return Result{}, ErrVersionConflict
		}
		commandReplayed, commandErr := loadCommandReplay(ctx, tx, input.Event, input.Command)
		if commandErr != nil {
			return Result{}, commandErr
		}
		if !commandReplayed {
			return Result{}, ErrCommandConflict
		}
		return result, nil
	}
	tag, err = tx.Exec(ctx, `INSERT INTO agent.outbox (id,tenant_id,command_id,command_type,aggregate_kind,aggregate_id,store_epoch,payload_ref,payload_hash,status,available_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10) ON CONFLICT DO NOTHING`, input.Command.ID, input.Event.TenantID, input.Command.CommandID, input.Command.CommandType, input.Event.AggregateKind, input.Event.AggregateID, input.Event.StoreEpoch, input.Command.PayloadRef, input.Command.PayloadHash, now)
	if err != nil {
		return Result{}, appender.rollback(ctx, tx, err)
	}
	if tag.RowsAffected() != 1 {
		if err = appender.rollbackToSavepoint(ctx, tx); err != nil {
			return Result{}, err
		}
		return Result{}, ErrCommandConflict
	}
	if _, err = tx.Exec(ctx, `RELEASE SAVEPOINT lites_event_append`); err != nil {
		return Result{}, err
	}
	return Result{Sequence: sequence}, nil
}

func loadCommandReplay(ctx context.Context, tx pgx.Tx, event Event, expected OutboxCommand) (bool, error) {
	var actual OutboxCommand
	var tenantID, aggregateKind, aggregateID, storeEpoch string
	err := tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,command_type,aggregate_kind,aggregate_id::text,store_epoch::text,payload_ref,payload_hash FROM agent.outbox WHERE command_id=$1`, expected.CommandID).Scan(&actual.ID, &tenantID, &actual.CommandType, &aggregateKind, &aggregateID, &storeEpoch, &actual.PayloadRef, &actual.PayloadHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return actual.ID == expected.ID && tenantID == event.TenantID && actual.CommandType == expected.CommandType && aggregateKind == event.AggregateKind && aggregateID == event.AggregateID && storeEpoch == event.StoreEpoch && actual.PayloadRef == expected.PayloadRef && actual.PayloadHash == expected.PayloadHash, nil
}

func (appender Appender) rollback(ctx context.Context, tx pgx.Tx, cause error) error {
	if err := appender.rollbackToSavepoint(ctx, tx); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (Appender) rollbackToSavepoint(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT lites_event_append`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `RELEASE SAVEPOINT lites_event_append`)
	return err
}

func loadReplay(ctx context.Context, tx pgx.Tx, expected Event) (Result, bool, error) {
	var actual Event
	var sequence uint64
	err := tx.QueryRow(ctx, `SELECT tenant_id::text,user_id::text,seq,event_type,event_schema_version,aggregate_kind,aggregate_id::text,aggregate_version,store_epoch::text,correlation_id::text,payload_ref,payload_hash FROM agent.events WHERE id=$1`, expected.ID).Scan(&actual.TenantID, &actual.UserID, &sequence, &actual.EventType, &actual.SchemaVersion, &actual.AggregateKind, &actual.AggregateID, &actual.AggregateVersion, &actual.StoreEpoch, &actual.CorrelationID, &actual.PayloadRef, &actual.PayloadHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	replayed := actual.TenantID == expected.TenantID && actual.UserID == expected.UserID && actual.EventType == expected.EventType && actual.SchemaVersion == expected.SchemaVersion && actual.AggregateKind == expected.AggregateKind && actual.AggregateID == expected.AggregateID && actual.AggregateVersion == expected.AggregateVersion && actual.StoreEpoch == expected.StoreEpoch && actual.CorrelationID == expected.CorrelationID && actual.PayloadRef == expected.PayloadRef && actual.PayloadHash == expected.PayloadHash
	return Result{Sequence: sequence, Replayed: replayed}, replayed, nil
}

func validInput(input Input) bool {
	event := input.Event
	command := input.Command
	if event.ID == "" || event.TenantID == "" || event.UserID == "" || event.EventType == "" || event.SchemaVersion < 1 || event.AggregateKind == "" || event.AggregateID == "" || event.AggregateVersion < 1 || event.StoreEpoch == "" || event.OccurredAt.IsZero() || event.CorrelationID == "" || event.PayloadRef == "" || event.PayloadHash == "" || command.ID == "" || command.CommandID == "" || command.CommandType == "" || command.PayloadRef == "" || command.PayloadHash == "" {
		return false
	}
	if !json.Valid(event.Actor) || bytes.Equal(bytes.TrimSpace(event.Actor), []byte("null")) {
		return false
	}
	var actor map[string]any
	return json.Unmarshal(event.Actor, &actor) == nil && actor != nil
}
