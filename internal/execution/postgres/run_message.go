package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

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
	messageIndex, err := nextRunMessageIndex(ctx, tx, command.Completion.Claim.TenantID, command.Completion.Claim.RunID)
	if err != nil {
		return CompletedRunMessage{}, err
	}
	eventID, outboxID, publishID, err := store.runMessageIdentifiers(command.MessageID)
	if err != nil {
		return CompletedRunMessage{}, err
	}
	causationID := completed.RunEventID
	event := eventpostgres.Input{Event: eventpostgres.Event{
		ID: eventID, TenantID: command.Completion.Claim.TenantID, UserID: command.Completion.Claim.UserID,
		EventType: "RunMessageFinalized", SchemaVersion: 1, AggregateKind: "run_message",
		AggregateID: command.MessageID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch,
		OccurredAt: completed.CompletedAt, Actor: command.Completion.Actor, CausationID: &causationID,
		CorrelationID: command.Completion.CorrelationID, PayloadRef: command.FinalizedEvent.Ref,
		PayloadHash: command.FinalizedEvent.Hash,
	}, Commands: []eventpostgres.OutboxCommand{{
		ID: outboxID, CommandID: publishID, CommandType: "events.publish",
		PayloadRef: command.FinalizedEvent.Ref, PayloadHash: command.FinalizedEvent.Hash,
	}}}
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return CompletedRunMessage{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO agent.run_messages(id,tenant_id,user_id,run_id,role,message_index,payload_ref,payload_hash,content_hash,content_type,source_kind,trust_label,finalized_event_id,finalized_at,created_at,updated_at) VALUES($1,$2,$3,$4,'assistant',$5,$6,$7,$8,'application/json','agent_output','derived',$9,$10,$10,$10) ON CONFLICT DO NOTHING`, command.MessageID, command.Completion.Claim.TenantID, command.Completion.Claim.UserID, command.Completion.Claim.RunID, messageIndex, command.Message.Ref, command.Message.Hash, command.ContentHash, eventID, completed.CompletedAt)
	if err != nil {
		return CompletedRunMessage{}, err
	}
	if tag.RowsAffected() != 1 {
		return CompletedRunMessage{}, ErrRunMessageConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return CompletedRunMessage{}, err
	}
	return CompletedRunMessage{CompletedRun: completed, MessageID: command.MessageID, MessageIndex: messageIndex, FinalizedEvent: eventID}, nil
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
	if command.Completion.TargetState != "succeeded" || command.MessageID == "" || !validPointer(command.Message) || !validPointer(command.FinalizedEvent) || !validSHA256(command.Message.Hash) || !validSHA256(command.ContentHash) {
		return false
	}
	return validCompleteRun(command.Completion)
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}
