package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productreminder "github.com/langshift/lites/internal/product/reminder"
)

const defaultReminderDeliveryConsumer = "product-reminder-delivery-worker"

type ReminderDeliveryDispatcher struct {
	Pool interface {
		BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	}
	Appender     eventpostgres.Appender
	Payloads     payload.Store
	Inbox        eventpostgres.InboxStore
	Sender       productreminder.Sender
	IDKey        []byte
	StoreEpoch   string
	ConsumerName string
	Now          func() time.Time
}

type ReminderDeliveryResult struct {
	Claimed, Completed, Replayed, TerminalFailure bool
	DeliveryID                                    string
	Attempt                                       int
}

type reminderDeliveryState struct {
	Version      uint64
	AttemptCount int
	Status       string
	UserID       string
	ScheduleID   string
	DeliveryKey  string
	OccurrenceAt time.Time
}

func (dispatcher ReminderDeliveryDispatcher) Dispatch(ctx context.Context, command eventpostgres.DeliveredCommand) (ReminderDeliveryResult, error) {
	if !dispatcher.valid() || command.CommandType != "DeliverReminder" || command.AggregateKind != "reminder_delivery" || command.StoreEpoch != dispatcher.StoreEpoch {
		return ReminderDeliveryResult{}, eventpostgres.ErrDeliveryConfiguration
	}
	consumer := dispatcher.ConsumerName
	if consumer == "" {
		consumer = defaultReminderDeliveryConsumer
	}
	claim, err := dispatcher.Inbox.Claim(ctx, consumer, command)
	if err != nil {
		return ReminderDeliveryResult{}, err
	}
	if claim.Completed {
		return ReminderDeliveryResult{Completed: true, Replayed: true, DeliveryID: command.AggregateID}, nil
	}
	result := ReminderDeliveryResult{Claimed: true, DeliveryID: command.AggregateID}
	document, err := dispatcher.loadCommand(ctx, command)
	if err != nil {
		if completeErr := dispatcher.Inbox.Complete(ctx, claim); completeErr != nil {
			return result, completeErr
		}
		result.Completed, result.TerminalFailure = true, true
		return result, nil
	}
	state, terminal, err := dispatcher.beginAttempt(ctx, claim, document)
	result.Attempt = state.AttemptCount
	if err != nil {
		_ = dispatcher.Inbox.Abandon(ctx, claim)
		return result, err
	}
	if terminal {
		result.Completed, result.TerminalFailure = true, true
		return result, nil
	}
	delivery := productreminder.Delivery{TenantID: command.TenantID, UserID: document.UserID, DeliveryID: document.DeliveryID, ScheduleID: document.ScheduleID, DeliveryKey: document.DeliveryKey, Channel: document.Channel, Locale: document.Locale, Title: document.Title, Body: document.Body, OccurrenceAt: document.OccurrenceAt}
	sendErr := dispatcher.Sender.Send(ctx, delivery)
	permanent := false
	if sendErr != nil {
		_, permanent = productreminder.ClassifySendError(sendErr)
	}
	completed, finalizeErr := dispatcher.finalize(ctx, claim, document, state, sendErr, permanent)
	if finalizeErr != nil {
		_ = dispatcher.Inbox.Abandon(ctx, claim)
		return result, finalizeErr
	}
	result.Completed = completed
	result.TerminalFailure = sendErr != nil && completed
	if sendErr != nil && !completed {
		if abandonErr := dispatcher.Inbox.Abandon(ctx, claim); abandonErr != nil && !errors.Is(abandonErr, eventpostgres.ErrDeliveryConflict) {
			return result, abandonErr
		}
		return result, sendErr
	}
	return result, nil
}

func (dispatcher ReminderDeliveryDispatcher) beginAttempt(ctx context.Context, claim eventpostgres.InboxClaim, document ReminderDeliveryCommand) (reminderDeliveryState, bool, error) {
	now := dispatcher.now()
	tx, err := dispatcher.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return reminderDeliveryState{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.Command.TenantID); err != nil {
		return reminderDeliveryState{}, false, err
	}
	if err = dispatcher.Inbox.LockClaimTx(ctx, tx, claim, now); err != nil {
		return reminderDeliveryState{}, false, err
	}
	state, err := loadReminderDeliveryState(ctx, tx, claim.Command.TenantID, document.DeliveryID)
	if err != nil {
		return state, false, err
	}
	if !deliveryMatchesCommand(state, document) {
		return state, false, payload.ErrIntegrity
	}
	if state.Status == "delivered" || state.Status == "cancelled" {
		if err = dispatcher.Inbox.CompleteTx(ctx, tx, claim, now); err != nil {
			return state, false, err
		}
		return state, true, tx.Commit(ctx)
	}
	if state.Status != "pending" && state.Status != "failed" && state.Status != "delivering" {
		return state, false, ErrRouteConflict
	}
	if state.AttemptCount >= 20 {
		nextVersion := state.Version + 1
		if err = dispatcher.updateFailure(ctx, tx, claim, document, state, nextVersion, "delivery_attempts_exhausted", now); err != nil {
			return state, false, err
		}
		if err = dispatcher.Inbox.CompleteTx(ctx, tx, claim, now); err != nil {
			return state, false, err
		}
		state.Version, state.Status = nextVersion, "failed"
		return state, true, tx.Commit(ctx)
	}
	nextVersion, nextAttempt := state.Version+1, state.AttemptCount+1
	tag, err := tx.Exec(ctx, `UPDATE product.reminder_deliveries SET version=$1,status='delivering',attempt_count=$2,last_error_code=NULL,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND version=$6 AND status=$7`, nextVersion, nextAttempt, now, claim.Command.TenantID, document.DeliveryID, state.Version, state.Status)
	if err != nil || tag.RowsAffected() != 1 {
		return state, false, errors.Join(ErrRouteConflict, err)
	}
	if err = dispatcher.appendDeliveryEvent(ctx, tx, claim, document, nextVersion, "ReminderDeliveryClaimed", map[string]any{"request_id": claim.Command.CommandID, "claim_set_hash": document.DeliveryKey, "schedule_id": document.ScheduleID, "delivery_id": document.DeliveryID, "delivery_key": document.DeliveryKey, "occurrence_at": document.OccurrenceAt, "timezone": "UTC", "attempt": nextAttempt}, now); err != nil {
		return state, false, err
	}
	state.Version, state.AttemptCount, state.Status = nextVersion, nextAttempt, "delivering"
	return state, false, tx.Commit(ctx)
}

func (dispatcher ReminderDeliveryDispatcher) finalize(ctx context.Context, claim eventpostgres.InboxClaim, document ReminderDeliveryCommand, expected reminderDeliveryState, sendErr error, permanent bool) (bool, error) {
	now := dispatcher.now()
	tx, err := dispatcher.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.Command.TenantID); err != nil {
		return false, err
	}
	if err = dispatcher.Inbox.LockClaimTx(ctx, tx, claim, now); err != nil {
		return false, err
	}
	state, err := loadReminderDeliveryState(ctx, tx, claim.Command.TenantID, document.DeliveryID)
	if err != nil {
		return false, err
	}
	if state.Version != expected.Version || state.AttemptCount != expected.AttemptCount || state.Status != "delivering" || !deliveryMatchesCommand(state, document) {
		return false, ErrRouteConflict
	}
	nextVersion := state.Version + 1
	if sendErr == nil {
		tag, updateErr := tx.Exec(ctx, `UPDATE product.reminder_deliveries SET version=$1,status='delivered',delivered_at=$2,last_error_code=NULL,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND version=$5 AND status='delivering'`, nextVersion, now, claim.Command.TenantID, document.DeliveryID, state.Version)
		if updateErr != nil || tag.RowsAffected() != 1 {
			return false, errors.Join(ErrRouteConflict, updateErr)
		}
		if err = dispatcher.appendDeliveryEvent(ctx, tx, claim, document, nextVersion, "ReminderDelivered", map[string]any{"schedule_id": document.ScheduleID, "delivery_id": document.DeliveryID, "delivery_key": document.DeliveryKey, "occurrence_at": document.OccurrenceAt, "delivered_at": now}, now); err != nil {
			return false, err
		}
		if err = dispatcher.Inbox.CompleteTx(ctx, tx, claim, now); err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	code, classifiedPermanent := productreminder.ClassifySendError(sendErr)
	permanent = permanent || classifiedPermanent || state.AttemptCount >= 20
	if err = dispatcher.updateFailure(ctx, tx, claim, document, state, nextVersion, code, now); err != nil {
		return false, err
	}
	if permanent {
		if err = dispatcher.Inbox.CompleteTx(ctx, tx, claim, now); err != nil {
			return false, err
		}
	}
	return permanent, tx.Commit(ctx)
}

func (dispatcher ReminderDeliveryDispatcher) updateFailure(ctx context.Context, tx pgx.Tx, claim eventpostgres.InboxClaim, document ReminderDeliveryCommand, state reminderDeliveryState, nextVersion uint64, code string, now time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE product.reminder_deliveries SET version=$1,status='failed',last_error_code=$2,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND version=$6 AND status IN ('pending','failed','delivering')`, nextVersion, code, now, claim.Command.TenantID, document.DeliveryID, state.Version)
	if err != nil || tag.RowsAffected() != 1 {
		return errors.Join(ErrRouteConflict, err)
	}
	return dispatcher.appendDeliveryEvent(ctx, tx, claim, document, nextVersion, "ReminderDeliveryFailed", map[string]any{"schedule_id": document.ScheduleID, "delivery_id": document.DeliveryID, "delivery_key": document.DeliveryKey, "occurrence_at": document.OccurrenceAt, "attempt": state.AttemptCount, "error_code": code}, now)
}

func (dispatcher ReminderDeliveryDispatcher) appendDeliveryEvent(ctx context.Context, tx pgx.Tx, claim eventpostgres.InboxClaim, document ReminderDeliveryCommand, version uint64, eventType string, body map[string]any, now time.Time) error {
	body["subject_id"], body["subject_version"] = document.DeliveryID, version
	eventID, _ := ids.DeterministicUUID(dispatcher.IDKey, "reminder-delivery-event:"+eventType, document.DeliveryID+"\x00"+jsonNumber(version))
	encoded, _ := json.Marshal(body)
	manifest, err := dispatcher.Payloads.Put(ctx, payload.Descriptor{TenantID: claim.Command.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, encoded)
	if err != nil {
		return err
	}
	outboxID, _ := ids.DeterministicUUID(dispatcher.IDKey, "reminder-delivery-event-outbox:"+eventType, eventID)
	publishID, _ := ids.DeterministicUUID(dispatcher.IDKey, "reminder-delivery-event-publish:"+eventType, eventID)
	actor, _ := json.Marshal(map[string]any{"kind": "system", "service": "product-worker"})
	causation := claim.Command.CommandID
	_, err = dispatcher.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: claim.Command.TenantID, UserID: document.UserID, EventType: eventType, SchemaVersion: 1, AggregateKind: "reminder_delivery", AggregateID: document.DeliveryID, AggregateVersion: version, StoreEpoch: dispatcher.StoreEpoch, OccurredAt: now, Actor: actor, CausationID: &causation, CorrelationID: claim.Command.CommandID, PayloadRef: manifest.Ref, PayloadHash: manifest.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: manifest.Ref, PayloadHash: manifest.Hash}}})
	return err
}

func (dispatcher ReminderDeliveryDispatcher) loadCommand(ctx context.Context, command eventpostgres.DeliveredCommand) (ReminderDeliveryCommand, error) {
	encoded, err := dispatcher.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.CommandID, Class: reminderDeliveryCommandClass, ContentType: "application/json"}, payload.Manifest{Ref: command.PayloadRef, Hash: command.PayloadHash})
	if err != nil {
		return ReminderDeliveryCommand{}, err
	}
	var document ReminderDeliveryCommand
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || document.SchemaVersion != 1 || document.DeliveryID != command.AggregateID || document.ScheduleID == "" || document.UserID == "" || len(document.DeliveryKey) != 64 || document.Channel != "email" && document.Channel != "push" && document.Channel != "in_app" || document.Locale != "en" && document.Locale != "zh-CN" || document.OccurrenceAt.IsZero() || document.Title == "" || document.Body == "" {
		return ReminderDeliveryCommand{}, payload.ErrIntegrity
	}
	return document, nil
}

func loadReminderDeliveryState(ctx context.Context, tx pgx.Tx, tenantID, deliveryID string) (reminderDeliveryState, error) {
	var state reminderDeliveryState
	err := tx.QueryRow(ctx, `SELECT version,attempt_count,status,user_id::text,schedule_id::text,delivery_key,occurrence_at FROM product.reminder_deliveries WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, deliveryID).Scan(&state.Version, &state.AttemptCount, &state.Status, &state.UserID, &state.ScheduleID, &state.DeliveryKey, &state.OccurrenceAt)
	return state, err
}

func deliveryMatchesCommand(state reminderDeliveryState, document ReminderDeliveryCommand) bool {
	return state.UserID == document.UserID && state.ScheduleID == document.ScheduleID && state.DeliveryKey == document.DeliveryKey && state.OccurrenceAt.Equal(document.OccurrenceAt)
}

func (dispatcher ReminderDeliveryDispatcher) valid() bool {
	return dispatcher.Pool != nil && dispatcher.Payloads != nil && dispatcher.Sender != nil && len(dispatcher.IDKey) >= 32 && dispatcher.StoreEpoch != ""
}

func (dispatcher ReminderDeliveryDispatcher) now() time.Time {
	if dispatcher.Now != nil {
		return dispatcher.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func jsonNumber(value uint64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
