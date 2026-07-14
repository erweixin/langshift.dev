// Package postgres implements the transactional execution-kernel projections
// on top of the shared PostgreSQL EventStore.
package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
	"github.com/langshift/lites/internal/security/opaque"
)

var (
	ErrConfiguration  = errors.New("execution store configuration is invalid")
	ErrInvalidCommand = errors.New("execution command is invalid")
	ErrRunConflict    = errors.New("run replay conflicts with durable state")
)

type PayloadPointer struct {
	Ref  string
	Hash string
}

type AcceptRunCommand struct {
	RunID             string
	TenantID          string
	UserID            string
	ConversationID    string
	CorrelationID     string
	DueAt             time.Time
	ProfileSnapshotID string
	BudgetSnapshot    json.RawMessage
	Actor             json.RawMessage
	AcceptedEvent     PayloadPointer
	QueuedEvent       PayloadPointer
	StartCommand      PayloadPointer
	QueueClass        string
	ResourceClass     string
	Priority          int
	CostUnits         int64
	MaxAttempts       int
}

type AcceptedRun struct {
	RunID           string
	RunVersion      uint64
	Status          string
	StartCommandID  string
	StartJobID      string
	AcceptedEventID string
	QueuedEventID   string
	Replayed        bool
}

type RunStore struct {
	Pool       *pgxpool.Pool
	Appender   eventpostgres.Appender
	IDKey      []byte
	StoreEpoch string
	Now        func() time.Time
	Epochs     eventpostgres.EpochAuthority
	Tokens     opaque.Manager
	LeaseTTL   time.Duration
	Random     io.Reader
}

// Accept queues a Run in one transaction. The accepted state is an immutable
// event, but it is never externally observable without the matching queued
// projection and StartAgentRun command.
func (store RunStore) Accept(ctx context.Context, command AcceptRunCommand) (AcceptedRun, error) {
	if !store.valid() {
		return AcceptedRun{}, ErrConfiguration
	}
	if !validAcceptRun(command) {
		return AcceptedRun{}, ErrInvalidCommand
	}
	identifiers, err := store.identifiers(command.RunID)
	if err != nil {
		return AcceptedRun{}, ErrConfiguration
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if store.Now != nil {
		now = store.Now().UTC().Truncate(time.Microsecond)
	}
	command.DueAt = command.DueAt.UTC().Truncate(time.Microsecond)
	if !command.DueAt.After(now) {
		return AcceptedRun{}, ErrInvalidCommand
	}

	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return AcceptedRun{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return AcceptedRun{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.runs (id,tenant_id,user_id,conversation_id,status,run_version,pending_command_id,due_at,profile_snapshot_id,budget_snapshot,created_at,updated_at) VALUES ($1,$2,$3,$4,'queued',2,$5,$6,$7,$8,$9,$9) ON CONFLICT DO NOTHING`, command.RunID, command.TenantID, command.UserID, command.ConversationID, identifiers.startCommand, command.DueAt, command.ProfileSnapshotID, command.BudgetSnapshot, now)
	if err != nil {
		return AcceptedRun{}, err
	}
	replayed := tag.RowsAffected() == 0
	durableTime := now
	if replayed {
		err = tx.QueryRow(ctx, `SELECT created_at FROM agent.runs WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND conversation_id=$4 AND status='queued' AND run_version=2 AND pending_command_id=$5 AND active_command_id IS NULL AND active_attempt_id IS NULL AND current_fence=0 AND lease_token_hash IS NULL AND lease_expires_at IS NULL AND cancel_requested_at IS NULL AND due_at=$6 AND profile_snapshot_id=$7 AND budget_snapshot=$8::jsonb`, command.RunID, command.TenantID, command.UserID, command.ConversationID, identifiers.startCommand, command.DueAt, command.ProfileSnapshotID, command.BudgetSnapshot).Scan(&durableTime)
		if errors.Is(err, pgx.ErrNoRows) {
			return AcceptedRun{}, ErrRunConflict
		}
		if err != nil {
			return AcceptedRun{}, err
		}
	}

	tag, err = tx.Exec(ctx, `INSERT INTO agent.jobs (id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$9,$9,$9) ON CONFLICT DO NOTHING`, identifiers.startJob, command.TenantID, identifiers.startCommand, command.QueueClass, command.ResourceClass, command.Priority, command.CostUnits, command.MaxAttempts, durableTime, command.DueAt)
	if err != nil {
		return AcceptedRun{}, err
	}
	if tag.RowsAffected() == 0 {
		var exact bool
		err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agent.jobs WHERE id=$1 AND tenant_id=$2 AND command_id=$3 AND queue_class=$4 AND resource_class=$5 AND priority=$6 AND cost_units=$7 AND max_attempts=$8 AND retry_count=0 AND status='pending' AND available_at=$9 AND due_at=$10 AND enqueued_at=$9)`, identifiers.startJob, command.TenantID, identifiers.startCommand, command.QueueClass, command.ResourceClass, command.Priority, command.CostUnits, command.MaxAttempts, durableTime, command.DueAt).Scan(&exact)
		if err != nil {
			return AcceptedRun{}, err
		}
		if !exact {
			return AcceptedRun{}, ErrRunConflict
		}
	}

	accepted := eventpostgres.Input{
		Event:    eventpostgres.Event{ID: identifiers.acceptedEvent, TenantID: command.TenantID, UserID: command.UserID, EventType: "RunAccepted", SchemaVersion: 1, AggregateKind: "run", AggregateID: command.RunID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: durableTime, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.AcceptedEvent.Ref, PayloadHash: command.AcceptedEvent.Hash},
		Commands: []eventpostgres.OutboxCommand{{ID: identifiers.acceptedPublishOutbox, CommandID: identifiers.acceptedPublishCommand, CommandType: "events.publish", PayloadRef: command.AcceptedEvent.Ref, PayloadHash: command.AcceptedEvent.Hash}},
	}
	if _, err = store.Appender.Append(ctx, tx, accepted); err != nil {
		return AcceptedRun{}, err
	}
	causationID := identifiers.acceptedEvent
	queued := eventpostgres.Input{
		Event: eventpostgres.Event{ID: identifiers.queuedEvent, TenantID: command.TenantID, UserID: command.UserID, EventType: "RunQueued", SchemaVersion: 1, AggregateKind: "run", AggregateID: command.RunID, AggregateVersion: 2, StoreEpoch: store.StoreEpoch, OccurredAt: durableTime, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.QueuedEvent.Ref, PayloadHash: command.QueuedEvent.Hash},
		Commands: []eventpostgres.OutboxCommand{
			{ID: identifiers.queuedPublishOutbox, CommandID: identifiers.queuedPublishCommand, CommandType: "events.publish", PayloadRef: command.QueuedEvent.Ref, PayloadHash: command.QueuedEvent.Hash},
			{ID: identifiers.startOutbox, CommandID: identifiers.startCommand, CommandType: "StartAgentRun", PayloadRef: command.StartCommand.Ref, PayloadHash: command.StartCommand.Hash},
		},
	}
	if _, err = store.Appender.Append(ctx, tx, queued); err != nil {
		return AcceptedRun{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AcceptedRun{}, err
	}
	return AcceptedRun{RunID: command.RunID, RunVersion: 2, Status: "queued", StartCommandID: identifiers.startCommand, StartJobID: identifiers.startJob, AcceptedEventID: identifiers.acceptedEvent, QueuedEventID: identifiers.queuedEvent, Replayed: replayed}, nil
}

type runIdentifiers struct {
	acceptedEvent, acceptedPublishOutbox, acceptedPublishCommand string
	queuedEvent, queuedPublishOutbox, queuedPublishCommand       string
	startOutbox, startCommand, startJob                          string
}

func (store RunStore) identifiers(runID string) (runIdentifiers, error) {
	derive := func(domain string) (string, error) { return ids.DeterministicUUID(store.IDKey, domain, runID) }
	values := make([]string, 9)
	domains := []string{"run-accepted-event", "run-accepted-publish-outbox", "run-accepted-publish-command", "run-queued-event", "run-queued-publish-outbox", "run-queued-publish-command", "run-start-outbox", "run-start-command", "run-start-job"}
	for index, domain := range domains {
		value, err := derive(domain)
		if err != nil {
			return runIdentifiers{}, err
		}
		values[index] = value
	}
	return runIdentifiers{acceptedEvent: values[0], acceptedPublishOutbox: values[1], acceptedPublishCommand: values[2], queuedEvent: values[3], queuedPublishOutbox: values[4], queuedPublishCommand: values[5], startOutbox: values[6], startCommand: values[7], startJob: values[8]}, nil
}

func (store RunStore) valid() bool {
	return store.Pool != nil && len(store.IDKey) >= 32 && store.StoreEpoch != ""
}

func validAcceptRun(command AcceptRunCommand) bool {
	return command.RunID != "" && command.TenantID != "" && command.UserID != "" && command.ConversationID != "" && command.CorrelationID != "" && !command.DueAt.IsZero() && command.ProfileSnapshotID != "" && validJSONObject(command.BudgetSnapshot) && validJSONObject(command.Actor) && validPointer(command.AcceptedEvent) && validPointer(command.QueuedEvent) && validPointer(command.StartCommand) && (command.QueueClass == "interactive" || command.QueueClass == "background") && command.ResourceClass != "" && command.Priority >= 0 && command.Priority <= 1000 && command.CostUnits > 0 && command.CostUnits <= 1_000_000_000_000 && command.MaxAttempts > 0 && command.MaxAttempts <= 100
}

func validPointer(pointer PayloadPointer) bool { return pointer.Ref != "" && pointer.Hash != "" }

func validJSONObject(value json.RawMessage) bool {
	if !json.Valid(value) || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return false
	}
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}
