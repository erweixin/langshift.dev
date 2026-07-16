package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

type CreateConversationCommand struct {
	ConversationID string
	TenantID       string
	UserID         string
	MissionID      string
	Title          *string
	Mode           string
	CorrelationID  string
	Actor          json.RawMessage
	CreatedEvent   PayloadPointer
}

type ConversationResult struct {
	ID        string
	Version   uint64
	Status    string
	UpdatedAt time.Time
	EventID   string
	Replayed  bool
}

type conversationIdentifiers struct {
	event, publishOutbox, publishCommand string
}

// CreateConversation commits the mission-bound conversation projection and
// its immutable creation fact in one tenant-scoped transaction. A caller may
// safely retry the same derived ConversationID, but conflicting material is
// rejected instead of being treated as a replay.
func (store RunStore) CreateConversation(ctx context.Context, command CreateConversationCommand) (ConversationResult, error) {
	if !store.validCore() || store.Pool == nil || !validCreateConversation(command) {
		return ConversationResult{}, ErrInvalidCommand
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ConversationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := store.CreateConversationInTx(ctx, tx, command)
	if err != nil {
		return ConversationResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ConversationResult{}, err
	}
	return result, nil
}

// CreateConversationInTx lets a public control-plane operation commit its
// idempotency response and the Conversation aggregate atomically. The caller
// owns commit or rollback.
func (store RunStore) CreateConversationInTx(ctx context.Context, tx pgx.Tx, command CreateConversationCommand) (ConversationResult, error) {
	if tx == nil || !store.validCore() || !validCreateConversation(command) {
		return ConversationResult{}, ErrInvalidCommand
	}
	identifiers, err := store.conversationIdentifiers(command.ConversationID)
	if err != nil {
		return ConversationResult{}, ErrConfiguration
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if store.Now != nil {
		now = store.Now().UTC().Truncate(time.Microsecond)
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return ConversationResult{}, err
	}
	var missionActive bool
	if err = tx.QueryRow(ctx, `SELECT agent.lock_active_owned_mission($1,$2,$3)`, command.TenantID, command.UserID, command.MissionID).Scan(&missionActive); err != nil {
		return ConversationResult{}, err
	}
	if !missionActive {
		return ConversationResult{}, ErrRunConflict
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.conversations(id,tenant_id,user_id,mission_id,version,title,mode,status,created_at,updated_at) VALUES($1,$2,$3,$4,1,$5,$6,'active',$7,$7) ON CONFLICT DO NOTHING`, command.ConversationID, command.TenantID, command.UserID, command.MissionID, command.Title, command.Mode, now)
	if err != nil {
		return ConversationResult{}, err
	}
	replayed := tag.RowsAffected() == 0
	durableTime := now
	if replayed {
		if err = tx.QueryRow(ctx, `SELECT updated_at FROM agent.conversations WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND mission_id=$4 AND version=1 AND title IS NOT DISTINCT FROM $5 AND mode=$6 AND status='active' AND last_run_id IS NULL`, command.ConversationID, command.TenantID, command.UserID, command.MissionID, command.Title, command.Mode).Scan(&durableTime); errors.Is(err, pgx.ErrNoRows) {
			return ConversationResult{}, ErrRunConflict
		} else if err != nil {
			return ConversationResult{}, err
		}
	}
	created := eventpostgres.Input{
		Event: eventpostgres.Event{
			ID: identifiers.event, TenantID: command.TenantID, UserID: command.UserID,
			EventType: "ConversationCreated", SchemaVersion: 1, AggregateKind: "conversation",
			AggregateID: command.ConversationID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch,
			OccurredAt: durableTime, Actor: command.Actor, CorrelationID: command.CorrelationID,
			PayloadRef: command.CreatedEvent.Ref, PayloadHash: command.CreatedEvent.Hash,
		},
		Commands: []eventpostgres.OutboxCommand{{ID: identifiers.publishOutbox, CommandID: identifiers.publishCommand, CommandType: "events.publish", PayloadRef: command.CreatedEvent.Ref, PayloadHash: command.CreatedEvent.Hash}},
	}
	if _, err = store.Appender.Append(ctx, tx, created); err != nil {
		return ConversationResult{}, err
	}
	return ConversationResult{ID: command.ConversationID, Version: 1, Status: "active", UpdatedAt: durableTime, EventID: identifiers.event, Replayed: replayed}, nil
}

func (store RunStore) conversationIdentifiers(conversationID string) (conversationIdentifiers, error) {
	values := make([]string, 3)
	for index, domain := range []string{"conversation-created-event", "conversation-created-publish-outbox", "conversation-created-publish-command"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain, conversationID)
		if err != nil {
			return conversationIdentifiers{}, err
		}
		values[index] = value
	}
	return conversationIdentifiers{event: values[0], publishOutbox: values[1], publishCommand: values[2]}, nil
}

func validCreateConversation(command CreateConversationCommand) bool {
	validTitle := command.Title == nil || utf8.ValidString(*command.Title) && utf8.RuneCountInString(*command.Title) >= 1 && utf8.RuneCountInString(*command.Title) <= 200
	return command.ConversationID != "" && command.TenantID != "" && command.UserID != "" && command.MissionID != "" && validTitle &&
		(command.Mode == "coach" || command.Mode == "task" || command.Mode == "project") && command.CorrelationID != "" &&
		validJSONObject(command.Actor) && validPointer(command.CreatedEvent)
}
