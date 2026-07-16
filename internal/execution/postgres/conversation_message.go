package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

type AcceptMessageRunCommand struct {
	Run                         AcceptRunCommand
	MessageID                   string
	ExpectedConversationVersion uint64
	Message                     PayloadPointer
	ContentHash                 string
	AppendedEvent               PayloadPointer
	Actor                       json.RawMessage
}

type AcceptedMessageRun struct {
	AcceptedRun
	MessageID           string
	MessageEventID      string
	ConversationVersion uint64
	AcceptedAt          time.Time
	Replayed            bool
}

type conversationMessageIdentifiers struct {
	event, publishOutbox, publishCommand string
}

// AcceptMessageRun is the public Message admission commit point. The
// Conversation CAS, immutable user context source, MessageAppended fact,
// RunAccepted/RunQueued facts, Job, and StartAgentRun outbox command become
// visible together. Concurrent requests against one expected Conversation
// version have exactly one winner.
func (store RunStore) AcceptMessageRun(ctx context.Context, command AcceptMessageRunCommand) (AcceptedMessageRun, error) {
	if store.Pool == nil || store.Behavior == nil || !store.validCore() || !validAcceptMessageRun(command) {
		return AcceptedMessageRun{}, ErrInvalidCommand
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return AcceptedMessageRun{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	accepted, err := store.AcceptMessageRunInTx(ctx, tx, command)
	if err != nil {
		return AcceptedMessageRun{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AcceptedMessageRun{}, err
	}
	return accepted, nil
}

// AcceptMessageRunInTx lets the public idempotency record, Conversation CAS,
// immutable context source, Run, Job, events, and commands share one commit.
// The caller owns commit or rollback.
func (store RunStore) AcceptMessageRunInTx(ctx context.Context, tx pgx.Tx, command AcceptMessageRunCommand) (AcceptedMessageRun, error) {
	if tx == nil || store.Behavior == nil || !store.validCore() || !validAcceptMessageRun(command) {
		return AcceptedMessageRun{}, ErrInvalidCommand
	}
	identifiers, err := store.conversationMessageIdentifiers(command.MessageID)
	if err != nil {
		return AcceptedMessageRun{}, ErrConfiguration
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if store.Now != nil {
		now = store.Now().UTC().Truncate(time.Microsecond)
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.Run.TenantID); err != nil {
		return AcceptedMessageRun{}, err
	}
	var currentVersion uint64
	var status string
	var lastRunID *string
	var durableTime time.Time
	err = tx.QueryRow(ctx, `SELECT version,status,last_run_id::text,updated_at FROM agent.conversations WHERE tenant_id=$1 AND id=$2 AND user_id=$3 FOR UPDATE`, command.Run.TenantID, command.Run.ConversationID, command.Run.UserID).Scan(&currentVersion, &status, &lastRunID, &durableTime)
	if errors.Is(err, pgx.ErrNoRows) {
		return AcceptedMessageRun{}, ErrRunConflict
	}
	if err != nil {
		return AcceptedMessageRun{}, err
	}
	if status != "active" {
		return AcceptedMessageRun{}, ErrRunConflict
	}
	nextVersion := command.ExpectedConversationVersion + 1
	replayed := currentVersion == nextVersion && lastRunID != nil && *lastRunID == command.Run.RunID
	if currentVersion != command.ExpectedConversationVersion && !replayed {
		return AcceptedMessageRun{}, ErrRunConflict
	}
	if !replayed {
		durableTime = now
	}
	messageEvent := eventpostgres.Input{
		Event: eventpostgres.Event{
			ID: identifiers.event, TenantID: command.Run.TenantID, UserID: command.Run.UserID,
			EventType: "MessageAppended", SchemaVersion: 1, AggregateKind: "conversation",
			AggregateID: command.Run.ConversationID, AggregateVersion: nextVersion, StoreEpoch: store.StoreEpoch,
			OccurredAt: durableTime, Actor: command.Actor, CorrelationID: command.Run.CorrelationID,
			PayloadRef: command.AppendedEvent.Ref, PayloadHash: command.AppendedEvent.Hash,
		},
		Commands: []eventpostgres.OutboxCommand{{ID: identifiers.publishOutbox, CommandID: identifiers.publishCommand, CommandType: "events.publish", PayloadRef: command.AppendedEvent.Ref, PayloadHash: command.AppendedEvent.Hash}},
	}
	if _, err = store.Appender.Append(ctx, tx, messageEvent); err != nil {
		return AcceptedMessageRun{}, err
	}
	accepted, err := store.AcceptInTx(ctx, tx, command.Run)
	if err != nil {
		return AcceptedMessageRun{}, err
	}
	if replayed != accepted.Replayed {
		return AcceptedMessageRun{}, ErrRunConflict
	}
	if replayed {
		var exact bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.run_messages WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND run_id=$4 AND role='user' AND message_index=0 AND payload_ref=$5 AND payload_hash=$6 AND content_hash=$7 AND content_type='application/json' AND source_kind='conversation_user' AND trust_label='user_asserted' AND finalized_event_id=$8 AND finalized_at=$9)`, command.MessageID, command.Run.TenantID, command.Run.UserID, command.Run.RunID, command.Message.Ref, command.Message.Hash, command.ContentHash, identifiers.event, durableTime).Scan(&exact)
		if err != nil {
			return AcceptedMessageRun{}, err
		}
		if !exact {
			return AcceptedMessageRun{}, ErrRunMessageConflict
		}
	} else {
		tag, updateErr := tx.Exec(ctx, `UPDATE agent.conversations SET version=$1,last_run_id=$2,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND user_id=$6 AND version=$7 AND status='active'`, nextVersion, command.Run.RunID, durableTime, command.Run.TenantID, command.Run.ConversationID, command.Run.UserID, command.ExpectedConversationVersion)
		if updateErr != nil || tag.RowsAffected() != 1 {
			return AcceptedMessageRun{}, ErrRunConflict
		}
		tag, err = tx.Exec(ctx, `INSERT INTO agent.run_messages(id,tenant_id,user_id,run_id,role,message_index,payload_ref,payload_hash,content_hash,content_type,source_kind,trust_label,finalized_event_id,finalized_at,created_at,updated_at) VALUES($1,$2,$3,$4,'user',0,$5,$6,$7,'application/json','conversation_user','user_asserted',$8,$9,$9,$9)`, command.MessageID, command.Run.TenantID, command.Run.UserID, command.Run.RunID, command.Message.Ref, command.Message.Hash, command.ContentHash, identifiers.event, durableTime)
		if err != nil || tag.RowsAffected() != 1 {
			return AcceptedMessageRun{}, ErrRunMessageConflict
		}
	}
	return AcceptedMessageRun{AcceptedRun: accepted, MessageID: command.MessageID, MessageEventID: identifiers.event, ConversationVersion: nextVersion, AcceptedAt: durableTime, Replayed: replayed}, nil
}

func (store RunStore) conversationMessageIdentifiers(messageID string) (conversationMessageIdentifiers, error) {
	values := make([]string, 3)
	for index, domain := range []string{"conversation-message-appended-event", "conversation-message-publish-outbox", "conversation-message-publish-command"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain, messageID)
		if err != nil {
			return conversationMessageIdentifiers{}, err
		}
		values[index] = value
	}
	return conversationMessageIdentifiers{event: values[0], publishOutbox: values[1], publishCommand: values[2]}, nil
}

func validAcceptMessageRun(command AcceptMessageRunCommand) bool {
	return validAcceptRun(command.Run) && command.ExpectedConversationVersion > 0 && command.ExpectedConversationVersion < math.MaxUint64 && command.MessageID != "" &&
		validPointer(command.Message) && validSHA256(command.Message.Hash) && validSHA256(command.ContentHash) &&
		validPointer(command.AppendedEvent) && validJSONObject(command.Actor) && equivalentJSONObject(command.Actor, command.Run.Actor)
}

func equivalentJSONObject(left, right json.RawMessage) bool {
	var leftValue, rightValue map[string]any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}
