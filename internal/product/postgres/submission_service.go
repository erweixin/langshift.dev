package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

const submissionCreateOperation = "submissions.create.v2"

type SubmissionService struct {
	Pool                                             *pgxpool.Pool
	Appender                                         eventpostgres.Appender
	Payloads                                         payload.Store
	IDKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	StoreEpoch                                       string
	IdempotencyTTL                                   time.Duration
	Now                                              func() time.Time
}

func (service SubmissionService) Create(ctx context.Context, command productapi.CreateSubmissionCommand) (productapi.SubmissionMutationResult, error) {
	if !service.valid() || !validMissionMetadata(command.CommandMetadata) || command.DailyTaskID == "" || command.ExpectedTaskVersion == 0 || command.SubmissionKind != "code" && command.SubmissionKind != "writing" && command.SubmissionKind != "design" || command.Content == "" || command.Understanding == "" {
		return productapi.SubmissionMutationResult{}, productapi.ErrValidation
	}
	canonical, _ := json.Marshal(command)
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.SubmissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+submissionCreateOperation, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return productapi.SubmissionMutationResult{}, productapi.ErrDependencyUnavailable
	}
	submissionID, _ := ids.DeterministicUUID(service.IDKey, "submission", recordID)
	eventID, _ := ids.DeterministicUUID(service.IDKey, "submission-created:event", recordID)
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: submissionCreateOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	responseDescriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: "product-submission-idempotency", ContentType: "application/json"}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.SubmissionMutationResult{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.read(ctx, responseDescriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	contentHash := sha256.Sum256([]byte(command.Content))
	understandingHash := sha256.Sum256([]byte(command.Understanding))
	contentManifest, err := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: submissionID, Class: "product-submission-content", ContentType: "text/plain; charset=utf-8"}, []byte(command.Content))
	if err != nil {
		return productapi.SubmissionMutationResult{}, service.mapError(err)
	}
	understandingManifest, err := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: submissionID, Class: "product-submission-understanding", ContentType: "text/plain; charset=utf-8"}, []byte(command.Understanding))
	if err != nil {
		return productapi.SubmissionMutationResult{}, service.mapError(err)
	}
	eventManifest, err := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventID, Class: routeEventClass, ContentType: "application/json"}, map[string]any{"submission_id": submissionID, "daily_task_id": command.DailyTaskID, "task_version": command.ExpectedTaskVersion + 1, "submission_revision": 1, "submission_kind": command.SubmissionKind, "content_hash": hex.EncodeToString(contentHash[:]), "understanding_hash": hex.EncodeToString(understandingHash[:])})
	if err != nil {
		return productapi.SubmissionMutationResult{}, service.mapError(err)
	}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, e := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); e != nil {
			return idempotency.Response{}, e
		}
		var scopedMissionID, scopedRouteRevisionID string
		var scopedFocusVersion uint64
		if e := tx.QueryRow(ctx, `SELECT mission_id::text,route_revision_id::text,focus_version FROM product.daily_tasks WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, command.TenantID, command.UserID, command.DailyTaskID).Scan(&scopedMissionID, &scopedRouteRevisionID, &scopedFocusVersion); e != nil {
			return idempotency.Response{}, e
		}
		var admissible bool
		if e := tx.QueryRow(ctx, `SELECT agent.lock_owned_daily_planning_mission($1,$2,$3,$4,$5)`, command.TenantID, command.UserID, scopedMissionID, scopedRouteRevisionID, scopedFocusVersion).Scan(&admissible); e != nil || !admissible {
			return idempotency.Response{}, ErrRouteConflict
		}
		var current producttask.Task
		var scheduled time.Time
		var status string
		var focusVersion uint64
		e := tx.QueryRow(ctx, `SELECT id::text,version,mission_id::text,route_revision_id::text,status,scheduled_for,focus_version FROM product.daily_tasks WHERE tenant_id=$1 AND user_id=$2 AND id=$3 FOR UPDATE`, command.TenantID, command.UserID, command.DailyTaskID).Scan(&current.ID, &current.Version, &current.MissionID, &current.RouteRevisionID, &status, &scheduled, &focusVersion)
		if e != nil {
			return idempotency.Response{}, e
		}
		if current.MissionID != scopedMissionID || current.RouteRevisionID != scopedRouteRevisionID || focusVersion != scopedFocusVersion {
			return idempotency.Response{}, ErrRouteConflict
		}
		current.Status, current.ScheduledFor = producttask.Status(status), scheduled
		next, e := current.Transition(producttask.TransitionCommand{ExpectedVersion: command.ExpectedTaskVersion, Next: producttask.Submitted, SubmissionID: submissionID, Now: service.now()})
		if e != nil {
			return idempotency.Response{}, ErrRouteConflict
		}
		now := service.now()
		_, e = tx.Exec(ctx, `INSERT INTO product.submissions(id,tenant_id,user_id,version,daily_task_id,submission_revision,payload_ref,content_hash,understanding_ref,submitted_at,created_at,updated_at,task_version,submission_kind,understanding_hash,payload_manifest_hash,understanding_manifest_hash) VALUES($1,$2,$3,1,$4,1,$5,$6,$7,$8,$8,$8,$9,$10,$11,$12,$13)`, submissionID, command.TenantID, command.UserID, command.DailyTaskID, contentManifest.Ref, hex.EncodeToString(contentHash[:]), understandingManifest.Ref, now, command.ExpectedTaskVersion, command.SubmissionKind, hex.EncodeToString(understandingHash[:]), contentManifest.Hash, understandingManifest.Hash)
		if e != nil {
			return idempotency.Response{}, e
		}
		tag, e := tx.Exec(ctx, `UPDATE product.daily_tasks SET version=$1,status='submitted',current_submission_id=$2,updated_at=$3 WHERE tenant_id=$4 AND user_id=$5 AND id=$6 AND version=$7 AND status='in_progress'`, next.Version, submissionID, now, command.TenantID, command.UserID, command.DailyTaskID, current.Version)
		if e != nil || tag.RowsAffected() != 1 {
			return idempotency.Response{}, ErrRouteConflict
		}
		outboxID, _ := ids.DeterministicUUID(service.IDKey, "submission-created:outbox", recordID)
		publishID, _ := ids.DeterministicUUID(service.IDKey, "submission-created:publish", recordID)
		_, e = service.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: eventID, TenantID: command.TenantID, UserID: command.UserID, EventType: "SubmissionCreated", SchemaVersion: 1, AggregateKind: "daily_task", AggregateID: command.DailyTaskID, AggregateVersion: next.Version, StoreEpoch: service.StoreEpoch, OccurredAt: now, Actor: missionActor(command.UserID, command.SessionID), CorrelationID: command.RequestID, PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishID, CommandType: "events.publish", PayloadRef: eventManifest.Ref, PayloadHash: eventManifest.Hash}}})
		if e != nil {
			return idempotency.Response{}, e
		}
		result := productapi.SubmissionMutationResult{ID: submissionID, Version: 1, SubmissionRevision: 1, DailyTaskID: command.DailyTaskID, DailyTaskVersion: next.Version, Status: "submitted", UpdatedAt: now.Format(time.RFC3339Nano), EventID: eventID}
		stored, e := service.putJSON(ctx, responseDescriptor, result)
		if e != nil {
			return idempotency.Response{}, e
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: "application/vnd.lites.submission.v2+json", PayloadRef: stored.Ref, Hash: stored.Hash, ResourceVersion: next.Version}, nil
	})
	if err != nil {
		return productapi.SubmissionMutationResult{}, service.mapError(err)
	}
	result, err := service.read(ctx, responseDescriptor, response)
	if err != nil {
		return result, service.mapError(err)
	}
	result.Replayed = replayed
	return result, nil
}

func (service SubmissionService) putJSON(ctx context.Context, d payload.Descriptor, v any) (payload.Manifest, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return payload.Manifest{}, e
	}
	return service.Payloads.Put(ctx, d, b)
}
func (service SubmissionService) read(ctx context.Context, d payload.Descriptor, r idempotency.Response) (productapi.SubmissionMutationResult, error) {
	b, e := service.Payloads.Get(ctx, d, payload.Manifest{Ref: r.PayloadRef, Hash: r.Hash})
	if e != nil {
		return productapi.SubmissionMutationResult{}, e
	}
	var out productapi.SubmissionMutationResult
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&out) != nil || !errors.Is(dec.Decode(&struct{}{}), io.EOF) {
		return out, payload.ErrIntegrity
	}
	return out, nil
}
func (service SubmissionService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
func (service SubmissionService) valid() bool {
	return service.Pool != nil && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.StoreEpoch != "" && service.IdempotencyTTL > 0
}
func (service SubmissionService) mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return productapi.ErrResourceNotFound
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(err, ErrRouteConflict):
		return productapi.ErrStateConflict
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, err)
	}
}

var _ productapi.SubmissionService = SubmissionService{}
