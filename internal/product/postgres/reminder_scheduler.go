package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
	productreminder "github.com/langshift/lites/internal/product/reminder"
)

const reminderDeliveryCommandClass = "reminder-delivery-command"

type ReminderScheduler struct {
	Pool       *pgxpool.Pool
	Appender   eventpostgres.Appender
	Payloads   payload.Store
	IDKey      []byte
	StoreEpoch string
	Now        func() time.Time
}

type ReminderScheduleResult struct {
	Scanned, Scheduled int
}

type ReminderDeliveryCommand struct {
	SchemaVersion int       `json:"schema_version"`
	DeliveryID    string    `json:"delivery_id"`
	ScheduleID    string    `json:"schedule_id"`
	UserID        string    `json:"user_id"`
	DeliveryKey   string    `json:"delivery_key"`
	Channel       string    `json:"channel"`
	OccurrenceAt  time.Time `json:"occurrence_at"`
	Locale        string    `json:"locale"`
	Title         string    `json:"title"`
	Body          string    `json:"body"`
}

func (scheduler ReminderScheduler) ListTenantIDs(ctx context.Context, after string, limit int) ([]string, error) {
	if !scheduler.valid() || limit < 1 || limit > 5000 {
		return nil, ErrInvalidCommand
	}
	rows, err := scheduler.Pool.Query(ctx, `SELECT tenant_id::text FROM agent.list_due_reminder_schedule_tenants(NULLIF($1,'')::uuid,$2)`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var tenantID string
		if err = rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		result = append(result, tenantID)
	}
	return result, rows.Err()
}

func (scheduler ReminderScheduler) ScheduleTenant(ctx context.Context, tenantID string, limit int) (ReminderScheduleResult, error) {
	if !scheduler.valid() || tenantID == "" || limit < 1 || limit > 500 {
		return ReminderScheduleResult{}, ErrInvalidCommand
	}
	now := scheduler.now()
	tx, err := scheduler.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ReminderScheduleResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return ReminderScheduleResult{}, err
	}
	rows, err := tx.Query(ctx, reminderSelect+` WHERE tenant_id=$1 AND status='active' AND next_occurrence_at<=$2 ORDER BY next_occurrence_at,id LIMIT $3 FOR UPDATE SKIP LOCKED`, tenantID, now, limit)
	if err != nil {
		return ReminderScheduleResult{}, err
	}
	due := make([]reminderDueItem, 0, limit)
	for rows.Next() {
		resource, scanErr := scanReminder(rows)
		if scanErr != nil {
			rows.Close()
			return ReminderScheduleResult{}, scanErr
		}
		due = append(due, reminderDueItem{Resource: resource})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return ReminderScheduleResult{}, err
	}
	rows.Close()
	result := ReminderScheduleResult{Scanned: len(due)}
	for index := range due {
		if err = scheduler.scheduleOne(ctx, tx, tenantID, due[index], now); err != nil {
			return result, err
		}
		result.Scheduled++
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}

type reminderDueItem struct {
	Resource productapi.ReminderResource
}

func (scheduler ReminderScheduler) scheduleOne(ctx context.Context, tx pgx.Tx, tenantID string, due reminderDueItem, now time.Time) error {
	current := due.Resource
	if current.Status != "active" || current.NextOccurrenceAt == nil || current.NextOccurrenceAt.After(now) {
		return ErrRouteConflict
	}
	var userID, locale string
	if err := tx.QueryRow(ctx, `SELECT s.user_id::text,COALESCE(p.locale,'en') FROM product.reminder_schedules s LEFT JOIN product.user_preferences p ON p.tenant_id=s.tenant_id AND p.user_id=s.user_id WHERE s.tenant_id=$1 AND s.id=$2`, tenantID, current.ID).Scan(&userID, &locale); err != nil {
		return err
	}
	if locale != "en" && locale != "zh-CN" {
		return payload.ErrIntegrity
	}
	occurrence := current.NextOccurrenceAt.UTC().Truncate(time.Microsecond)
	schedule := productreminder.Schedule{Timezone: current.Timezone, LocalTime: current.LocalTime, Weekdays: current.Weekdays, Channel: current.Channel}
	next, err := schedule.Next(occurrence)
	if err != nil {
		return err
	}
	deliveryID, err := ids.DeterministicUUID(scheduler.IDKey, "reminder-delivery", tenantID+"\x00"+current.ID+"\x00"+occurrence.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(tenantID + "\x00" + current.ID + "\x00" + occurrence.Format(time.RFC3339Nano)))
	deliveryKey := hex.EncodeToString(digest[:])
	commandID, _ := ids.DeterministicUUID(scheduler.IDKey, "reminder-delivery-command", deliveryID)
	title, body := reminderCopy(locale)
	command := ReminderDeliveryCommand{SchemaVersion: 1, DeliveryID: deliveryID, ScheduleID: current.ID, UserID: userID, DeliveryKey: deliveryKey, Channel: current.Channel, OccurrenceAt: occurrence, Locale: locale, Title: title, Body: body}
	commandPayload, err := scheduler.putJSON(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: commandID, Class: reminderDeliveryCommandClass, ContentType: "application/json"}, command)
	if err != nil {
		return err
	}
	insert, err := tx.Exec(ctx, `INSERT INTO product.reminder_deliveries(id,tenant_id,user_id,version,schedule_id,occurrence_at,delivery_key,status,attempt_count,delivered_at,last_error_code,created_at,updated_at) VALUES($1,$2,$3,1,$4,$5,$6,'pending',0,NULL,NULL,$7,$7) ON CONFLICT DO NOTHING`, deliveryID, tenantID, userID, current.ID, occurrence, deliveryKey, now)
	if err != nil {
		return err
	}
	if insert.RowsAffected() != 1 {
		return ErrRouteConflict
	}
	nextScheduleVersion := current.Version + 1
	updated, err := tx.Exec(ctx, `UPDATE product.reminder_schedules SET version=$1,next_occurrence_at=$2,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND version=$6 AND status='active' AND next_occurrence_at=$7`, nextScheduleVersion, next, now, tenantID, current.ID, current.Version, occurrence)
	if err != nil || updated.RowsAffected() != 1 {
		return errors.Join(ErrRouteConflict, err)
	}
	correlationID, _ := ids.DeterministicUUID(scheduler.IDKey, "reminder-delivery-correlation", deliveryID)
	scheduleEventID, _ := ids.DeterministicUUID(scheduler.IDKey, "reminder-occurrence-scheduled-event", deliveryID)
	schedulePayload, err := scheduler.putJSON(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: scheduleEventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": current.ID, "subject_version": nextScheduleVersion, "delivery_id": deliveryID, "delivery_key": deliveryKey, "occurrence_at": occurrence, "next_occurrence_at": next})
	if err != nil {
		return err
	}
	scheduleOutbox, _ := ids.DeterministicUUID(scheduler.IDKey, "reminder-occurrence-scheduled-outbox", deliveryID)
	schedulePublish, _ := ids.DeterministicUUID(scheduler.IDKey, "reminder-occurrence-scheduled-publish", deliveryID)
	actor, _ := json.Marshal(map[string]any{"kind": "system", "service": "product-worker"})
	if _, err = scheduler.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: scheduleEventID, TenantID: tenantID, UserID: userID, EventType: "ReminderOccurrenceScheduled", SchemaVersion: 1, AggregateKind: "reminder_schedule", AggregateID: current.ID, AggregateVersion: nextScheduleVersion, StoreEpoch: scheduler.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: correlationID, PayloadRef: schedulePayload.Ref, PayloadHash: schedulePayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: scheduleOutbox, CommandID: schedulePublish, CommandType: "events.publish", PayloadRef: schedulePayload.Ref, PayloadHash: schedulePayload.Hash}}}); err != nil {
		return err
	}
	deliveryEventID, _ := ids.DeterministicUUID(scheduler.IDKey, "reminder-delivery-scheduled-event", deliveryID)
	deliveryPayload, err := scheduler.putJSON(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: deliveryEventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": deliveryID, "subject_version": 1, "schedule_id": current.ID, "delivery_id": deliveryID, "delivery_key": deliveryKey, "occurrence_at": occurrence, "channel": current.Channel})
	if err != nil {
		return err
	}
	deliveryOutbox, _ := ids.DeterministicUUID(scheduler.IDKey, "reminder-delivery-scheduled-outbox", deliveryID)
	deliveryPublish, _ := ids.DeterministicUUID(scheduler.IDKey, "reminder-delivery-scheduled-publish", deliveryID)
	deliverOutbox, _ := ids.DeterministicUUID(scheduler.IDKey, "reminder-delivery-command-outbox", deliveryID)
	if _, err = scheduler.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: deliveryEventID, TenantID: tenantID, UserID: userID, EventType: "ReminderDeliveryScheduled", SchemaVersion: 1, AggregateKind: "reminder_delivery", AggregateID: deliveryID, AggregateVersion: 1, StoreEpoch: scheduler.StoreEpoch, OccurredAt: now, Actor: actor, CorrelationID: correlationID, PayloadRef: deliveryPayload.Ref, PayloadHash: deliveryPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: deliveryOutbox, CommandID: deliveryPublish, CommandType: "events.publish", PayloadRef: deliveryPayload.Ref, PayloadHash: deliveryPayload.Hash}, {ID: deliverOutbox, CommandID: commandID, CommandType: "DeliverReminder", TargetAggregateKind: "reminder_delivery", TargetAggregateID: deliveryID, PayloadRef: commandPayload.Ref, PayloadHash: commandPayload.Hash}}}); err != nil {
		return err
	}
	return nil
}

func reminderCopy(locale string) (string, string) {
	if locale == "zh-CN" {
		return "今天的成长练习已准备好", "打开 Lites，完成今天最重要的一项练习。"
	}
	return "Today's growth practice is ready", "Open Lites and complete the one practice that matters most today."
}

func (scheduler ReminderScheduler) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return scheduler.Payloads.Put(ctx, descriptor, encoded)
}

func (scheduler ReminderScheduler) now() time.Time {
	if scheduler.Now != nil {
		return scheduler.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (scheduler ReminderScheduler) valid() bool {
	return scheduler.Pool != nil && scheduler.Payloads != nil && len(scheduler.IDKey) >= 32 && scheduler.StoreEpoch != ""
}
