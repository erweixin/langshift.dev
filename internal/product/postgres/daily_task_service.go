package postgres

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
	producttask "github.com/langshift/lites/internal/product/task"
)

const (
	dailyTaskUpdateOperation = "tasks.update.v2"
	dailyTaskResponseClass   = "product-task-idempotency"
	dailyTaskPageSize        = 50
)

type DailyTaskService struct {
	Pool                 *pgxpool.Pool
	Appender             eventpostgres.Appender
	Payloads             payload.Store
	IDKey                []byte
	CursorKey            []byte
	IdempotencyKeyPepper []byte
	RequestDigestPepper  []byte
	StoreEpoch           string
	IdempotencyTTL       time.Duration
	Now                  func() time.Time
}

type dailyTaskCursor struct {
	TenantID, UserID string
	ScheduledFor     string
	CreatedAt        time.Time
	ID               string
}

type preparedTaskReschedule struct {
	CommandID string
	Document  dailyTaskCommandDocument
	Payload   PayloadPointer
}

func (service DailyTaskService) List(ctx context.Context, query productapi.DailyTaskListQuery) (productapi.DailyTaskListResult, error) {
	if !service.valid() || query.TenantID == "" || query.UserID == "" {
		return productapi.DailyTaskListResult{}, productapi.ErrValidation
	}
	var cursor *dailyTaskCursor
	if query.Cursor != "" {
		decoded, err := service.decodeCursor(query.Cursor)
		if err != nil || decoded.TenantID != query.TenantID || decoded.UserID != query.UserID {
			return productapi.DailyTaskListResult{}, productapi.ErrValidation
		}
		cursor = &decoded
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return productapi.DailyTaskListResult{}, service.mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, query.TenantID); err != nil {
		return productapi.DailyTaskListResult{}, service.mapError(err)
	}
	args := []any{query.TenantID, query.UserID, dailyTaskPageSize + 1}
	statement := `SELECT id::text,version,mission_id::text,route_revision_id::text,status,practice_kind,task_payload_ref,task_payload_hash,estimated_minutes,difficulty,focus_version,scheduled_for,rescheduled_to,current_submission_id::text,current_review_id::text,completed_at,created_at,updated_at FROM product.daily_tasks WHERE tenant_id=$1 AND user_id=$2`
	if cursor != nil {
		statement += ` AND (scheduled_for < $4::date OR (scheduled_for=$4::date AND (created_at < $5 OR (created_at=$5 AND id < $6::uuid))))`
		args = append(args, cursor.ScheduledFor, cursor.CreatedAt, cursor.ID)
	}
	statement += ` ORDER BY scheduled_for DESC,created_at DESC,id DESC LIMIT $3`
	rows, err := tx.Query(ctx, statement, args...)
	if err != nil {
		return productapi.DailyTaskListResult{}, service.mapError(err)
	}
	defer rows.Close()
	items := make([]productapi.DailyTaskResource, 0, dailyTaskPageSize+1)
	for rows.Next() {
		var item productapi.DailyTaskResource
		var ref, hash string
		var scheduled time.Time
		var rescheduled *time.Time
		if err = rows.Scan(&item.ID, &item.Version, &item.MissionID, &item.RouteRevisionID, &item.Status, &item.PracticeKind, &ref, &hash, &item.EstimatedMinutes, &item.Difficulty, &item.FocusVersion, &scheduled, &rescheduled, &item.CurrentSubmissionID, &item.CurrentReviewID, &item.CompletedAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return productapi.DailyTaskListResult{}, service.mapError(err)
		}
		item.ScheduledFor = scheduled.Format("2006-01-02")
		if rescheduled != nil {
			value := rescheduled.Format("2006-01-02")
			item.RescheduledTo = &value
		}
		encoded, getErr := service.Payloads.Get(ctx, payload.Descriptor{TenantID: query.TenantID, ObjectID: item.ID, Class: dailyTaskPayloadClass, ContentType: "application/json"}, payload.Manifest{Ref: ref, Hash: hash})
		if getErr != nil || !validJSONObject(encoded) {
			return productapi.DailyTaskListResult{}, service.mapError(errors.Join(getErr, payload.ErrIntegrity))
		}
		item.Task = encoded
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return productapi.DailyTaskListResult{}, service.mapError(err)
	}
	var next *string
	if len(items) > dailyTaskPageSize {
		last := items[dailyTaskPageSize-1]
		cursorValue, cursorErr := service.encodeCursor(dailyTaskCursor{TenantID: query.TenantID, UserID: query.UserID, ScheduledFor: last.ScheduledFor, CreatedAt: last.CreatedAt, ID: last.ID})
		if cursorErr != nil {
			return productapi.DailyTaskListResult{}, service.mapError(cursorErr)
		}
		next = &cursorValue
		items = items[:dailyTaskPageSize]
	}
	if err = tx.Commit(ctx); err != nil {
		return productapi.DailyTaskListResult{}, service.mapError(err)
	}
	return productapi.DailyTaskListResult{Items: items, NextCursor: next}, nil
}

func (service DailyTaskService) Update(ctx context.Context, command productapi.UpdateDailyTaskCommand) (productapi.DailyTaskMutationResult, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || command.TaskID == "" || command.ExpectedTaskVersion == 0 || command.Action != "start" && command.Action != "skip" && command.Action != "reschedule" && command.Action != "lower_difficulty" {
		return productapi.DailyTaskMutationResult{}, productapi.ErrValidation
	}
	canonical, err := json.Marshal(struct {
		RequestID, TaskID, Action, RescheduleFor string
		ExpectedVersion                          uint64
	}{command.ClientRequestID, command.TaskID, command.Action, command.RescheduleFor, command.ExpectedTaskVersion})
	if err != nil {
		return productapi.DailyTaskMutationResult{}, productapi.ErrDependencyUnavailable
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.DailyTaskMutationResult{}, productapi.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+dailyTaskUpdateOperation, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return productapi.DailyTaskMutationResult{}, productapi.ErrDependencyUnavailable
	}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: dailyTaskUpdateOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: dailyTaskResponseClass, ContentType: "application/json"}
	prepared := preparedTaskReschedule{}
	if command.Action == "reschedule" {
		prepared, err = service.prepareReschedule(ctx, command, recordID)
		if err != nil {
			return productapi.DailyTaskMutationResult{}, service.mapError(err)
		}
	}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.DailyTaskMutationResult{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.readResult(ctx, descriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	eventID, err := ids.DeterministicUUID(service.IDKey, "daily-task-updated:event", recordID)
	if err != nil {
		return productapi.DailyTaskMutationResult{}, productapi.ErrDependencyUnavailable
	}
	eventPayload, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventID, Class: routeEventClass, ContentType: "application/json"}, map[string]any{"subject_id": command.TaskID, "subject_version": command.ExpectedTaskVersion + 1, "action": command.Action, "reschedule_for": nullableString(command.RescheduleFor)})
	if err != nil {
		return productapi.DailyTaskMutationResult{}, productapi.ErrDependencyUnavailable
	}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, execErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); execErr != nil {
			return idempotency.Response{}, execErr
		}
		var current producttask.Task
		var scheduled time.Time
		var status, difficulty string
		var estimatedMinutes int
		var focusVersion uint64
		var submissionID, reviewID *string
		err := tx.QueryRow(ctx, `SELECT id::text,version,mission_id::text,route_revision_id::text,status,scheduled_for,current_submission_id::text,current_review_id::text,completed_at,difficulty,estimated_minutes,focus_version FROM product.daily_tasks WHERE tenant_id=$1 AND user_id=$2 AND id=$3 FOR UPDATE`, command.TenantID, command.UserID, command.TaskID).Scan(&current.ID, &current.Version, &current.MissionID, &current.RouteRevisionID, &status, &scheduled, &submissionID, &reviewID, &current.CompletedAt, &difficulty, &estimatedMinutes, &focusVersion)
		if err != nil {
			return idempotency.Response{}, err
		}
		if submissionID != nil {
			current.SubmissionID = *submissionID
		}
		if reviewID != nil {
			current.ReviewID = *reviewID
		}
		current.Status, current.ScheduledFor = producttask.Status(status), scheduled
		next := current
		if command.Action == "lower_difficulty" {
			if current.Version != command.ExpectedTaskVersion || current.Status != producttask.Scheduled && current.Status != producttask.InProgress || difficulty == "easier" {
				return idempotency.Response{}, ErrRouteConflict
			}
			next.Version++
		} else {
			nextStatus := map[string]producttask.Status{"start": producttask.InProgress, "skip": producttask.Skipped, "reschedule": producttask.Rescheduled}[command.Action]
			var transitionErr error
			next, transitionErr = current.Transition(producttask.TransitionCommand{ExpectedVersion: command.ExpectedTaskVersion, Next: nextStatus, Now: service.now()})
			if transitionErr != nil {
				return idempotency.Response{}, ErrRouteConflict
			}
		}
		var rescheduled any
		if command.Action == "reschedule" {
			date, dateErr := time.Parse("2006-01-02", command.RescheduleFor)
			if dateErr != nil || !date.After(time.Date(scheduled.Year(), scheduled.Month(), scheduled.Day(), 0, 0, 0, 0, time.UTC)) {
				return idempotency.Response{}, ErrInvalidCommand
			}
			if prepared.Document.MissionID != current.MissionID || prepared.Document.RouteRevisionID != current.RouteRevisionID || prepared.Document.ExpectedFocusVersion != focusVersion || prepared.Document.Difficulty != difficulty || prepared.Document.AvailableMinutes != estimatedMinutes || prepared.Document.ScheduledFor != command.RescheduleFor {
				return idempotency.Response{}, ErrRouteConflict
			}
			var admissible bool
			if lockErr := tx.QueryRow(ctx, `SELECT agent.lock_owned_daily_planning_mission($1,$2,$3,$4,$5)`, command.TenantID, command.UserID, current.MissionID, current.RouteRevisionID, focusVersion).Scan(&admissible); lockErr != nil || !admissible {
				return idempotency.Response{}, ErrRouteConflict
			}
			rescheduled = command.RescheduleFor
		} else if command.RescheduleFor != "" {
			return idempotency.Response{}, ErrInvalidCommand
		}
		now := service.now()
		nextDifficulty, nextMinutes := difficulty, estimatedMinutes
		if command.Action == "lower_difficulty" {
			nextDifficulty = "easier"
			nextMinutes = max(5, estimatedMinutes*2/3)
		}
		tag, execErr := tx.Exec(ctx, `UPDATE product.daily_tasks SET version=$1,status=$2,rescheduled_to=$3,difficulty=$4,estimated_minutes=$5,updated_at=$6 WHERE tenant_id=$7 AND user_id=$8 AND id=$9 AND version=$10 AND status=$11`, next.Version, next.Status, rescheduled, nextDifficulty, nextMinutes, now, command.TenantID, command.UserID, command.TaskID, current.Version, current.Status)
		if execErr != nil || tag.RowsAffected() != 1 {
			return idempotency.Response{}, ErrRouteConflict
		}
		outboxID, _ := ids.DeterministicUUID(service.IDKey, "daily-task-updated:outbox", recordID)
		publishID, _ := ids.DeterministicUUID(service.IDKey, "daily-task-updated:publish", recordID)
		commands := []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}}
		if prepared.CommandID != "" {
			dailyOutboxID, idErr := ids.DeterministicUUID(service.IDKey, "daily-task-rescheduled:outbox", recordID)
			if idErr != nil {
				return idempotency.Response{}, idErr
			}
			commands = append(commands, eventpostgres.OutboxCommand{ID: dailyOutboxID, CommandID: prepared.CommandID, CommandType: "GenerateDailyTask", TargetAggregateKind: "mission", TargetAggregateID: current.MissionID, PayloadRef: prepared.Payload.Ref, PayloadHash: prepared.Payload.Hash})
		}
		_, appendErr := service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "DailyTaskUpdated", SchemaVersion: 1, AggregateKind: "daily_task", AggregateID: command.TaskID, AggregateVersion: next.Version, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: missionActor(command.UserID, command.SessionID), CorrelationID: command.RequestID, PayloadRef: eventPayload.Ref, PayloadHash: eventPayload.Hash}, Commands: commands})
		if appendErr != nil {
			return idempotency.Response{}, appendErr
		}
		result := productapi.DailyTaskMutationResult{ID: command.TaskID, Version: next.Version, Status: string(next.Status), UpdatedAt: now, EventID: eventID}
		stored, putErr := service.putJSON(ctx, descriptor, result)
		if putErr != nil {
			return idempotency.Response{}, putErr
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: "application/vnd.lites.daily-task-mutation.v2+json", PayloadRef: stored.Ref, Hash: stored.Hash, ResourceVersion: next.Version}, nil
	})
	if err != nil {
		return productapi.DailyTaskMutationResult{}, service.mapError(err)
	}
	result, err := service.readResult(ctx, descriptor, response)
	if err != nil {
		return productapi.DailyTaskMutationResult{}, service.mapError(err)
	}
	result.Replayed = replayed
	return result, nil
}

func (service DailyTaskService) prepareReschedule(ctx context.Context, command productapi.UpdateDailyTaskCommand, recordID string) (preparedTaskReschedule, error) {
	date, err := time.Parse("2006-01-02", command.RescheduleFor)
	if err != nil {
		return preparedTaskReschedule{}, ErrInvalidCommand
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return preparedTaskReschedule{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return preparedTaskReschedule{}, err
	}
	var document dailyTaskCommandDocument
	var scheduled time.Time
	var status, missionStatus, currentRouteID, focusedMissionID, routeStatus string
	var observedFocusVersion uint64
	err = tx.QueryRow(ctx, `SELECT t.mission_id::text,t.route_revision_id::text,t.focus_version,t.difficulty,t.estimated_minutes,t.scheduled_for,t.status,m.status,m.route_version,COALESCE(m.current_route_revision_id::text,''),COALESCE(f.mission_id::text,''),COALESCE(f.focus_version,0),r.status FROM product.daily_tasks t JOIN product.missions m ON m.tenant_id=t.tenant_id AND m.id=t.mission_id JOIN product.route_revisions r ON r.tenant_id=t.tenant_id AND r.id=t.route_revision_id LEFT JOIN product.mission_focuses f ON f.tenant_id=t.tenant_id AND f.user_id=t.user_id WHERE t.tenant_id=$1 AND t.user_id=$2 AND t.id=$3`, command.TenantID, command.UserID, command.TaskID).Scan(&document.MissionID, &document.RouteRevisionID, &document.ExpectedFocusVersion, &document.Difficulty, &document.AvailableMinutes, &scheduled, &status, &missionStatus, &document.ExpectedRouteVersion, &currentRouteID, &focusedMissionID, &observedFocusVersion, &routeStatus)
	if err != nil {
		return preparedTaskReschedule{}, err
	}
	if status != "scheduled" && status != "in_progress" || missionStatus != "active" || currentRouteID != document.RouteRevisionID || focusedMissionID != document.MissionID || observedFocusVersion != document.ExpectedFocusVersion || routeStatus != "accepted" || !date.After(time.Date(scheduled.Year(), scheduled.Month(), scheduled.Day(), 0, 0, 0, 0, time.UTC)) {
		return preparedTaskReschedule{}, ErrRouteConflict
	}
	document.SchemaVersion = 1
	document.UserID = command.UserID
	document.ScheduledFor = command.RescheduleFor
	document.CorrelationID = command.RequestID
	if err = tx.Commit(ctx); err != nil {
		return preparedTaskReschedule{}, err
	}
	commandID, err := ids.DeterministicUUID(service.IDKey, "daily-task-rescheduled:command", recordID)
	if err != nil {
		return preparedTaskReschedule{}, err
	}
	manifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: commandID, Class: missionCommandClass, ContentType: "application/json"}, document)
	if err != nil {
		return preparedTaskReschedule{}, err
	}
	return preparedTaskReschedule{CommandID: commandID, Document: document, Payload: PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}}, nil
}

func (service DailyTaskService) readResult(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (productapi.DailyTaskMutationResult, error) {
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return productapi.DailyTaskMutationResult{}, err
	}
	var result productapi.DailyTaskMutationResult
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return productapi.DailyTaskMutationResult{}, payload.ErrIntegrity
	}
	return result, nil
}

func (service DailyTaskService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func (service DailyTaskService) encodeCursor(cursor dailyTaskCursor) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, service.CursorKey)
	_, _ = mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(append(body, mac.Sum(nil)...)), nil
}
func (service DailyTaskService) decodeCursor(value string) (dailyTaskCursor, error) {
	if len(value) > 2048 {
		return dailyTaskCursor{}, productapi.ErrValidation
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) <= sha256.Size {
		return dailyTaskCursor{}, productapi.ErrValidation
	}
	body, sig := decoded[:len(decoded)-sha256.Size], decoded[len(decoded)-sha256.Size:]
	mac := hmac.New(sha256.New, service.CursorKey)
	_, _ = mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return dailyTaskCursor{}, productapi.ErrValidation
	}
	var cursor dailyTaskCursor
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || cursor.TenantID == "" || cursor.UserID == "" || !validDateValue(cursor.ScheduledFor) || cursor.CreatedAt.IsZero() || cursor.ID == "" {
		return dailyTaskCursor{}, productapi.ErrValidation
	}
	return cursor, nil
}
func validDateValue(value string) bool { _, err := time.Parse("2006-01-02", value); return err == nil }
func (service DailyTaskService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func (service DailyTaskService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.CursorKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.StoreEpoch != "" && service.IdempotencyTTL > 0
}
func (service DailyTaskService) mapError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return productapi.ErrResourceNotFound
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(err, ErrRouteConflict):
		return productapi.ErrStateConflict
	case errors.Is(err, ErrInvalidCommand):
		return productapi.ErrValidation
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, err)
	}
}

var _ productapi.DailyTaskService = DailyTaskService{}
