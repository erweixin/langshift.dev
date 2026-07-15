package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

type StartLLMAttemptCommand struct {
	AttemptID, TenantID, UserID, RunID, RunAttemptID string
	RunVersion, RunFence, StreamGeneration           uint64
	AttemptKey, CorrelationID                        string
	Manifest                                         ContextManifest
	Actor                                            json.RawMessage
	StartedEvent                                     PayloadPointer
}

type LLMAttempt struct {
	AttemptID, Status, ContextManifestHash string
	Version, StreamGeneration              uint64
	StartedAt                              time.Time
	Replayed                               bool
}

func (store Store) StartLLMAttempt(ctx context.Context, command StartLLMAttemptCommand) (LLMAttempt, error) {
	if !store.valid() || !validStart(command) {
		return LLMAttempt{}, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return LLMAttempt{}, err
	}
	manifest, manifestHash, err := canonicalManifest(command.Manifest)
	if err != nil {
		return LLMAttempt{}, err
	}
	identifiers, err := store.eventIDs("llm-attempt-started", command.AttemptID, 1)
	if err != nil {
		return LLMAttempt{}, ErrConfiguration
	}
	now := store.now()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return LLMAttempt{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return LLMAttempt{}, err
	}
	var actual LLMAttempt
	var actualUser, actualRun, actualRunAttempt, actualKey, actualRouter, eventID string
	var actualRunFence uint64
	var actualManifest json.RawMessage
	err = tx.QueryRow(ctx, `SELECT id::text,user_id::text,run_id::text,run_attempt_id::text,run_fence,attempt_key,stream_generation,context_manifest,context_manifest_hash,router_snapshot_id,status,version,started_event_id::text,created_at FROM agent.llm_attempts WHERE tenant_id=$1 AND (id=$2 OR attempt_key=$3) FOR UPDATE`, command.TenantID, command.AttemptID, command.AttemptKey).Scan(&actual.AttemptID, &actualUser, &actualRun, &actualRunAttempt, &actualRunFence, &actualKey, &actual.StreamGeneration, &actualManifest, &actual.ContextManifestHash, &actualRouter, &actual.Status, &actual.Version, &eventID, &actual.StartedAt)
	if err == nil {
		var decoded ContextManifest
		if actual.AttemptID != command.AttemptID || actualUser != command.UserID || actualRun != command.RunID || actualRunAttempt != command.RunAttemptID || actualRunFence != command.RunFence || actualKey != command.AttemptKey || actual.StreamGeneration != command.StreamGeneration || actual.ContextManifestHash != manifestHash || actualRouter != command.Manifest.Router.ID || eventID != identifiers.Event || json.Unmarshal(actualManifest, &decoded) != nil || !reflect.DeepEqual(decoded, command.Manifest) {
			return LLMAttempt{}, ErrAttemptConflict
		}
		actual.Replayed = true
		if err = tx.Commit(ctx); err != nil {
			return LLMAttempt{}, err
		}
		return actual, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return LLMAttempt{}, err
	}
	var runUser, activeAttempt string
	var runVersion, runFence uint64
	var leaseExpires time.Time
	err = tx.QueryRow(ctx, `SELECT user_id::text,run_version,active_attempt_id::text,current_fence,lease_expires_at FROM agent.runs WHERE tenant_id=$1 AND id=$2 AND status='executing' FOR UPDATE`, command.TenantID, command.RunID).Scan(&runUser, &runVersion, &activeAttempt, &runFence, &leaseExpires)
	if err != nil || runUser != command.UserID || runVersion != command.RunVersion || activeAttempt != command.RunAttemptID || runFence != command.RunFence || !leaseExpires.After(now) {
		return LLMAttempt{}, ErrRunFence
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.llm_attempts(id,tenant_id,user_id,run_id,attempt_key,context_manifest,router_snapshot_id,status,run_attempt_id,run_fence,stream_generation,context_manifest_hash,started_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,'running',$8,$9,$10,$11,$12,$13,$13)`, command.AttemptID, command.TenantID, command.UserID, command.RunID, command.AttemptKey, manifest, command.Manifest.Router.ID, command.RunAttemptID, command.RunFence, command.StreamGeneration, manifestHash, identifiers.Event, now); err != nil {
		return LLMAttempt{}, ErrAttemptConflict
	}
	event := publishEvent(identifiers, eventpostgres.Event{TenantID: command.TenantID, UserID: command.UserID, EventType: "LLMAttemptStarted", SchemaVersion: 1, AggregateKind: "llm_attempt", AggregateID: command.AttemptID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID}, command.StartedEvent)
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return LLMAttempt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return LLMAttempt{}, err
	}
	return LLMAttempt{AttemptID: command.AttemptID, Status: "running", ContextManifestHash: manifestHash, Version: 1, StreamGeneration: command.StreamGeneration, StartedAt: now}, nil
}

func validStart(command StartLLMAttemptCommand) bool {
	return command.AttemptID != "" && command.TenantID != "" && command.UserID != "" && command.RunID != "" && command.RunAttemptID != "" && command.RunVersion > 0 && command.RunFence > 0 && command.StreamGeneration > 0 && command.AttemptKey != "" && command.CorrelationID != "" && validActor(command.Actor) && validPointer(command.StartedEvent)
}
