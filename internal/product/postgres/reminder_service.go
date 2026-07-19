package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
	productreminder "github.com/langshift/lites/internal/product/reminder"
)

const (
	reminderCreateOperation = "reminders.create.v2"
	reminderUpdateOperation = "reminders.update.v2"
	reminderResponseClass   = "product-reminder-idempotency"
	reminderScheduleKind    = "daily_practice"
	reminderDSTPolicy       = "next_valid_wall_time_earliest_duplicate"
)

type ReminderService struct {
	Pool                                             *pgxpool.Pool
	Appender                                         eventpostgres.Appender
	Payloads                                         payload.Store
	IDKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	StoreEpoch                                       string
	IdempotencyTTL                                   time.Duration
	Now                                              func() time.Time
}

type reminderScheduleDocument struct {
	SchemaVersion int    `json:"schema_version"`
	DSTPolicy     string `json:"dst_policy"`
	Timezone      string `json:"timezone"`
	LocalTime     string `json:"local_time"`
	Weekdays      []int  `json:"weekdays"`
	Channel       string `json:"channel"`
}

type reminderRowScanner interface {
	Scan(...any) error
}

func (service ReminderService) List(ctx context.Context, query productapi.ReminderListQuery) (productapi.ReminderListResult, error) {
	if !service.valid() || query.TenantID == "" || query.UserID == "" {
		return productapi.ReminderListResult{}, productapi.ErrValidation
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return productapi.ReminderListResult{}, service.mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, query.TenantID); err != nil {
		return productapi.ReminderListResult{}, service.mapError(err)
	}
	rows, err := tx.Query(ctx, reminderSelect+` WHERE tenant_id=$1 AND user_id=$2 ORDER BY created_at DESC,id DESC LIMIT 50`, query.TenantID, query.UserID)
	if err != nil {
		return productapi.ReminderListResult{}, service.mapError(err)
	}
	defer rows.Close()
	items := make([]productapi.ReminderResource, 0)
	for rows.Next() {
		item, scanErr := scanReminder(rows)
		if scanErr != nil {
			return productapi.ReminderListResult{}, service.mapError(scanErr)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return productapi.ReminderListResult{}, service.mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return productapi.ReminderListResult{}, service.mapError(err)
	}
	return productapi.ReminderListResult{Items: items}, nil
}

func (service ReminderService) Create(ctx context.Context, command productapi.CreateReminderCommand) (productapi.ReminderResource, error) {
	schedule := productreminder.Schedule{Timezone: command.Timezone, LocalTime: command.LocalTime, Weekdays: command.Weekdays, Channel: command.Channel}.Normalized()
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || schedule.Validate() != nil {
		return productapi.ReminderResource{}, productapi.ErrValidation
	}
	canonical, _ := json.Marshal(struct {
		RequestID string                   `json:"request_id"`
		Schedule  productreminder.Schedule `json:"schedule"`
	}{RequestID: command.ClientRequestID, Schedule: schedule})
	return service.executeCreate(ctx, command, schedule, canonical)
}

func (service ReminderService) executeCreate(ctx context.Context, command productapi.CreateReminderCommand, schedule productreminder.Schedule, canonical []byte) (productapi.ReminderResource, error) {
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.ReminderResource{}, productapi.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+reminderCreateOperation, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return productapi.ReminderResource{}, productapi.ErrDependencyUnavailable
	}
	scheduleID, _ := ids.DeterministicUUID(service.IDKey, "reminder-schedule", recordID)
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: reminderResponseClass, ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: reminderCreateOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.ReminderResource{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.readResponse(ctx, descriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	now := service.now()
	next, err := schedule.Next(now)
	if err != nil {
		return productapi.ReminderResource{}, productapi.ErrValidation
	}
	document := newReminderDocument(schedule)
	documentJSON, _ := json.Marshal(document)
	weekdays := smallWeekdays(schedule.Weekdays)
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, execErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); execErr != nil {
			return idempotency.Response{}, execErr
		}
		var live bool
		if scanErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.reminder_schedules WHERE tenant_id=$1 AND user_id=$2 AND schedule_kind=$3 AND status IN ('active','paused'))`, command.TenantID, command.UserID, reminderScheduleKind).Scan(&live); scanErr != nil {
			return idempotency.Response{}, scanErr
		}
		if live {
			return idempotency.Response{}, ErrRouteConflict
		}
		_, execErr := tx.Exec(ctx, `INSERT INTO product.reminder_schedules(id,tenant_id,user_id,version,status,timezone,local_schedule,next_occurrence_at,cancelled_at,schedule_kind,local_time,weekdays,channel,created_at,updated_at) VALUES($1,$2,$3,1,'active',$4,$5,$6,NULL,$7,$8::time,$9,$10,$11,$11)`, scheduleID, command.TenantID, command.UserID, schedule.Timezone, documentJSON, next, reminderScheduleKind, schedule.LocalTime, weekdays, schedule.Channel, now)
		if execErr != nil {
			return idempotency.Response{}, execErr
		}
		eventID, _ := ids.DeterministicUUID(service.IDKey, "reminder-created-event", recordID)
		eventPayload, putErr := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": scheduleID, "subject_version": 1, "schedule_kind": reminderScheduleKind, "status": "active", "schedule": document, "next_occurrence_at": next})
		if putErr != nil {
			return idempotency.Response{}, putErr
		}
		outboxID, _ := ids.DeterministicUUID(service.IDKey, "reminder-created-outbox", recordID)
		publishID, _ := ids.DeterministicUUID(service.IDKey, "reminder-created-publish", recordID)
		_, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "ReminderScheduleCreated", SchemaVersion: 1, AggregateKind: "reminder_schedule", AggregateID: scheduleID, AggregateVersion: 1, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: missionActor(command.UserID, command.SessionID), CorrelationID: command.RequestID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}})
		if appendErr != nil {
			return idempotency.Response{}, appendErr
		}
		result := productapi.ReminderResource{ID: scheduleID, Version: 1, Status: "active", Timezone: schedule.Timezone, LocalTime: schedule.LocalTime, Weekdays: schedule.Weekdays, Channel: schedule.Channel, NextOccurrenceAt: &next, CreatedAt: now, UpdatedAt: now}
		stored, putErr := service.putJSON(ctx, descriptor, result)
		if putErr != nil {
			return idempotency.Response{}, putErr
		}
		return idempotency.Response{Status: http.StatusCreated, ContentType: "application/vnd.lites.reminder.v2+json", PayloadRef: stored.Ref, Hash: stored.Hash, ResourceVersion: 1}, nil
	})
	if err != nil {
		return productapi.ReminderResource{}, service.mapError(err)
	}
	result, err := service.readResponse(ctx, descriptor, response)
	result.Replayed = replayed
	return result, service.mapError(err)
}

func (service ReminderService) Update(ctx context.Context, command productapi.UpdateReminderCommand) (productapi.ReminderResource, error) {
	operationID := command.OperationID
	if operationID == "" {
		operationID = reminderUpdateOperation
	}
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || command.ReminderID == "" || command.ExpectedVersion < 1 || !slices.Contains([]string{"replace", "pause", "resume", "cancel"}, command.Action) || operationID != reminderUpdateOperation && operationID != "reminders.cancel" || operationID == "reminders.cancel" && (command.Action != "cancel" || command.Reason == "") || operationID == reminderUpdateOperation && command.Reason != "" {
		return productapi.ReminderResource{}, productapi.ErrValidation
	}
	var replacement productreminder.Schedule
	if command.Action == "replace" {
		replacement = productreminder.Schedule{Timezone: command.Timezone, LocalTime: command.LocalTime, Weekdays: command.Weekdays, Channel: command.Channel}.Normalized()
		if replacement.Validate() != nil {
			return productapi.ReminderResource{}, productapi.ErrValidation
		}
	} else if command.Timezone != "" || command.LocalTime != "" || len(command.Weekdays) != 0 || command.Channel != "" {
		return productapi.ReminderResource{}, productapi.ErrValidation
	}
	canonical, _ := json.Marshal(struct {
		RequestID  string                   `json:"request_id"`
		ReminderID string                   `json:"reminder_id"`
		Action     string                   `json:"action"`
		Reason     string                   `json:"reason,omitempty"`
		Schedule   productreminder.Schedule `json:"schedule,omitempty"`
		Expected   uint64                   `json:"expected_version"`
	}{command.ClientRequestID, command.ReminderID, command.Action, command.Reason, replacement, command.ExpectedVersion})
	return service.executeUpdate(ctx, command, operationID, replacement, canonical)
}

func (service ReminderService) executeUpdate(ctx context.Context, command productapi.UpdateReminderCommand, operationID string, replacement productreminder.Schedule, canonical []byte) (productapi.ReminderResource, error) {
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.ReminderResource{}, productapi.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+operationID, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return productapi.ReminderResource{}, productapi.ErrDependencyUnavailable
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: reminderResponseClass, ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: operationID}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.ReminderResource{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.readResponse(ctx, descriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	now := service.now()
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, execErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); execErr != nil {
			return idempotency.Response{}, execErr
		}
		current, scanErr := scanReminder(tx.QueryRow(ctx, reminderSelect+` WHERE tenant_id=$1 AND user_id=$2 AND id=$3 FOR UPDATE`, command.TenantID, command.UserID, command.ReminderID))
		if scanErr != nil {
			return idempotency.Response{}, scanErr
		}
		if current.Version != command.ExpectedVersion {
			return idempotency.Response{}, ErrRouteConflict
		}
		nextState, transitionErr := transitionReminder(current, command.Action, replacement, now)
		if transitionErr != nil {
			return idempotency.Response{}, transitionErr
		}
		document := newReminderDocument(productreminder.Schedule{Timezone: nextState.Timezone, LocalTime: nextState.LocalTime, Weekdays: nextState.Weekdays, Channel: nextState.Channel})
		documentJSON, _ := json.Marshal(document)
		tag, execErr := tx.Exec(ctx, `UPDATE product.reminder_schedules SET version=$1,status=$2,timezone=$3,local_schedule=$4,next_occurrence_at=$5,cancelled_at=$6,local_time=$7::time,weekdays=$8,channel=$9,updated_at=$10 WHERE tenant_id=$11 AND user_id=$12 AND id=$13 AND version=$14`, nextState.Version, nextState.Status, nextState.Timezone, documentJSON, nextState.NextOccurrenceAt, nextState.CancelledAt, nextState.LocalTime, smallWeekdays(nextState.Weekdays), nextState.Channel, now, command.TenantID, command.UserID, command.ReminderID, current.Version)
		if execErr != nil {
			return idempotency.Response{}, execErr
		}
		if tag.RowsAffected() != 1 {
			return idempotency.Response{}, ErrRouteConflict
		}
		eventID, _ := ids.DeterministicUUID(service.IDKey, "reminder-updated-event", recordID)
		eventPayload, putErr := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, map[string]any{"subject_id": current.ID, "subject_version": nextState.Version, "action": command.Action, "reason": command.Reason, "previous_status": current.Status, "status": nextState.Status, "schedule": document, "next_occurrence_at": nextState.NextOccurrenceAt})
		if putErr != nil {
			return idempotency.Response{}, putErr
		}
		outboxID, _ := ids.DeterministicUUID(service.IDKey, "reminder-updated-outbox", recordID)
		publishID, _ := ids.DeterministicUUID(service.IDKey, "reminder-updated-publish", recordID)
		eventTypes := map[string]string{"replace": "ReminderScheduleReplaced", "pause": "ReminderSchedulePaused", "resume": "ReminderScheduleResumed", "cancel": "ReminderScheduleCancelled"}
		_, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: command.TenantID, UserID: command.UserID, EventType: eventTypes[command.Action], SchemaVersion: 1, AggregateKind: "reminder_schedule", AggregateID: current.ID, AggregateVersion: nextState.Version, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: missionActor(command.UserID, command.SessionID), CorrelationID: command.RequestID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}})
		if appendErr != nil {
			return idempotency.Response{}, appendErr
		}
		stored, putErr := service.putJSON(ctx, descriptor, nextState)
		if putErr != nil {
			return idempotency.Response{}, putErr
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: "application/vnd.lites.reminder.v2+json", PayloadRef: stored.Ref, Hash: stored.Hash, ResourceVersion: nextState.Version}, nil
	})
	if err != nil {
		return productapi.ReminderResource{}, service.mapError(err)
	}
	result, err := service.readResponse(ctx, descriptor, response)
	result.Replayed = replayed
	return result, service.mapError(err)
}

func transitionReminder(current productapi.ReminderResource, action string, replacement productreminder.Schedule, now time.Time) (productapi.ReminderResource, error) {
	if current.Status == "cancelled" || current.Status == "completed" {
		return productapi.ReminderResource{}, ErrRouteConflict
	}
	next := current
	next.Version++
	next.UpdatedAt = now
	next.Replayed = false
	switch action {
	case "pause":
		if current.Status != "active" {
			return productapi.ReminderResource{}, ErrRouteConflict
		}
		next.Status, next.NextOccurrenceAt = "paused", nil
	case "resume":
		if current.Status != "paused" {
			return productapi.ReminderResource{}, ErrRouteConflict
		}
		schedule := productreminder.Schedule{Timezone: current.Timezone, LocalTime: current.LocalTime, Weekdays: current.Weekdays, Channel: current.Channel}
		occurrence, err := schedule.Next(now)
		if err != nil {
			return productapi.ReminderResource{}, err
		}
		next.Status, next.NextOccurrenceAt = "active", &occurrence
	case "replace":
		next.Timezone, next.LocalTime, next.Weekdays, next.Channel = replacement.Timezone, replacement.LocalTime, replacement.Weekdays, replacement.Channel
		if current.Status == "active" {
			occurrence, err := replacement.Next(now)
			if err != nil {
				return productapi.ReminderResource{}, err
			}
			next.NextOccurrenceAt = &occurrence
		} else {
			next.NextOccurrenceAt = nil
		}
	case "cancel":
		next.Status, next.NextOccurrenceAt, next.CancelledAt = "cancelled", nil, &now
	default:
		return productapi.ReminderResource{}, ErrRouteConflict
	}
	return next, nil
}

const reminderSelect = `SELECT id::text,version,status,timezone,to_char(local_time,'HH24:MI'),weekdays,channel,next_occurrence_at,cancelled_at,created_at,updated_at,local_schedule FROM product.reminder_schedules`

func scanReminder(row reminderRowScanner) (productapi.ReminderResource, error) {
	var result productapi.ReminderResource
	var weekdays []int16
	var documentJSON json.RawMessage
	if err := row.Scan(&result.ID, &result.Version, &result.Status, &result.Timezone, &result.LocalTime, &weekdays, &result.Channel, &result.NextOccurrenceAt, &result.CancelledAt, &result.CreatedAt, &result.UpdatedAt, &documentJSON); err != nil {
		return result, err
	}
	result.Weekdays = make([]int, len(weekdays))
	for index, day := range weekdays {
		result.Weekdays[index] = int(day)
	}
	var document reminderScheduleDocument
	decoder := json.NewDecoder(bytes.NewReader(documentJSON))
	decoder.DisallowUnknownFields()
	expected := newReminderDocument(productreminder.Schedule{Timezone: result.Timezone, LocalTime: result.LocalTime, Weekdays: result.Weekdays, Channel: result.Channel})
	if decoder.Decode(&document) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || document.SchemaVersion != expected.SchemaVersion || document.DSTPolicy != expected.DSTPolicy || document.Timezone != expected.Timezone || document.LocalTime != expected.LocalTime || !slices.Equal(document.Weekdays, expected.Weekdays) || document.Channel != expected.Channel {
		return result, payload.ErrIntegrity
	}
	return result, nil
}

func newReminderDocument(schedule productreminder.Schedule) reminderScheduleDocument {
	return reminderScheduleDocument{SchemaVersion: 1, DSTPolicy: reminderDSTPolicy, Timezone: schedule.Timezone, LocalTime: schedule.LocalTime, Weekdays: schedule.Weekdays, Channel: schedule.Channel}
}

func smallWeekdays(days []int) []int16 {
	result := make([]int16, len(days))
	for index, day := range days {
		result[index] = int16(day)
	}
	return result
}

func (service ReminderService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func (service ReminderService) readResponse(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (productapi.ReminderResource, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return productapi.ReminderResource{}, err
	}
	var result productapi.ReminderResource
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return result, payload.ErrIntegrity
	}
	return result, nil
}

func (service ReminderService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.StoreEpoch != "" && service.IdempotencyTTL > 0
}

func (service ReminderService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (service ReminderService) mapError(err error) error {
	var postgresError *pgconn.PgError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return productapi.ErrResourceNotFound
	case errors.Is(err, productreminder.ErrInvalid):
		return productapi.ErrValidation
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(err, ErrRouteConflict):
		return productapi.ErrStateConflict
	case errors.As(err, &postgresError) && postgresError.Code == "23505":
		return productapi.ErrStateConflict
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, err)
	}
}

var _ productapi.ReminderService = ReminderService{}
