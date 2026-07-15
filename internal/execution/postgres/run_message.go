package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

var ErrRunMessageConflict = errors.New("run message conflicts with durable state")

type CompleteRunMessageCommand struct {
	Completion     CompleteRunCommand
	MessageID      string
	Message        PayloadPointer
	ContentHash    string
	FinalizedEvent PayloadPointer
}

type RunMessageInput struct {
	MessageID      string
	Message        PayloadPointer
	ContentHash    string
	FinalizedEvent PayloadPointer
}

type CompletedRunMessage struct {
	CompletedRun
	MessageID      string
	MessageIndex   int
	FinalizedEvent string
}

// CompleteRunWithMessage is the commit point for a successful model answer.
// The immutable assistant message, its event, and the fenced Run/Attempt/Job
// terminal facts become visible together or not at all.
func (store RunStore) CompleteRunWithMessage(ctx context.Context, command CompleteRunMessageCommand) (CompletedRunMessage, error) {
	if !store.validClaim() || !validCompleteRunMessage(command) {
		return CompletedRunMessage{}, ErrConfiguration
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return CompletedRunMessage{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	completed, err := store.CompleteRunTerminalInTx(ctx, tx, command.Completion)
	if err != nil {
		return CompletedRunMessage{}, err
	}
	causationID := completed.RunEventID
	messageIndex, eventID, err := store.appendRunMessageInTx(ctx, tx, command.Completion.Claim, RunMessageInput{MessageID: command.MessageID, Message: command.Message, ContentHash: command.ContentHash, FinalizedEvent: command.FinalizedEvent}, completed.CompletedAt, causationID, command.Completion.Actor, command.Completion.CorrelationID)
	if err != nil {
		return CompletedRunMessage{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CompletedRunMessage{}, err
	}
	return CompletedRunMessage{CompletedRun: completed, MessageID: command.MessageID, MessageIndex: messageIndex, FinalizedEvent: eventID}, nil
}

func (store RunStore) appendRunMessageInTx(ctx context.Context, tx pgx.Tx, claim RunClaim, input RunMessageInput, finalizedAt time.Time, causationID string, actor json.RawMessage, correlationID string) (int, string, error) {
	if tx == nil || !validRunMessageInput(input) || !validRunClaim(claim) || causationID == "" || !validJSONObject(actor) || correlationID == "" {
		return 0, "", ErrConfiguration
	}
	messageIndex, err := nextRunMessageIndex(ctx, tx, claim.TenantID, claim.RunID)
	if err != nil {
		return 0, "", err
	}
	eventID, outboxID, publishID, err := store.runMessageIdentifiers(input.MessageID)
	if err != nil {
		return 0, "", err
	}
	event := eventpostgres.Input{Event: eventpostgres.Event{
		ID: eventID, TenantID: claim.TenantID, UserID: claim.UserID,
		EventType: "RunMessageFinalized", SchemaVersion: 1, AggregateKind: "run_message",
		AggregateID: input.MessageID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch,
		OccurredAt: finalizedAt, Actor: actor, CausationID: &causationID, CorrelationID: correlationID,
		PayloadRef: input.FinalizedEvent.Ref, PayloadHash: input.FinalizedEvent.Hash,
	}, Commands: []eventpostgres.OutboxCommand{{
		ID: outboxID, CommandID: publishID, CommandType: "events.publish",
		PayloadRef: input.FinalizedEvent.Ref, PayloadHash: input.FinalizedEvent.Hash,
	}}}
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return 0, "", err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.run_messages(id,tenant_id,user_id,run_id,role,message_index,payload_ref,payload_hash,content_hash,content_type,source_kind,trust_label,finalized_event_id,finalized_at,created_at,updated_at) VALUES($1,$2,$3,$4,'assistant',$5,$6,$7,$8,'application/json','agent_output','derived',$9,$10,$10,$10) ON CONFLICT DO NOTHING`, input.MessageID, claim.TenantID, claim.UserID, claim.RunID, messageIndex, input.Message.Ref, input.Message.Hash, input.ContentHash, eventID, finalizedAt)
	if err != nil {
		return 0, "", err
	}
	if tag.RowsAffected() != 1 {
		return 0, "", ErrRunMessageConflict
	}
	return messageIndex, eventID, nil
}

func nextRunMessageIndex(ctx context.Context, tx pgx.Tx, tenantID, runID string) (int, error) {
	var index int
	err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(message_index),-1)+1 FROM agent.run_messages WHERE tenant_id=$1 AND run_id=$2`, tenantID, runID).Scan(&index)
	return index, err
}

func (store RunStore) runMessageIdentifiers(messageID string) (string, string, string, error) {
	values := make([]string, 3)
	for index, domain := range []string{"run-message-finalized-event", "run-message-publish-outbox", "run-message-publish-command"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain, messageID)
		if err != nil {
			return "", "", "", err
		}
		values[index] = value
	}
	return values[0], values[1], values[2], nil
}

func validCompleteRunMessage(command CompleteRunMessageCommand) bool {
	if command.Completion.TargetState != "succeeded" || !validRunMessageInput(RunMessageInput{MessageID: command.MessageID, Message: command.Message, ContentHash: command.ContentHash, FinalizedEvent: command.FinalizedEvent}) {
		return false
	}
	return validCompleteRun(command.Completion)
}

func validRunMessageInput(input RunMessageInput) bool {
	return input.MessageID != "" && validPointer(input.Message) && validPointer(input.FinalizedEvent) && validSHA256(input.Message.Hash) && validSHA256(input.ContentHash)
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}
