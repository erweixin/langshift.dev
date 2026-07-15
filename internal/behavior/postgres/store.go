// Package postgres persists immutable AI behavior snapshots, evaluation
// evidence and append-only channel promotions in the shared EventStore cell.
package postgres

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/behavior"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

var (
	ErrConfiguration   = errors.New("behavior store configuration is invalid")
	ErrCommand         = errors.New("behavior store command is invalid")
	ErrConflict        = errors.New("behavior store state conflicts with command")
	ErrStaleEpoch      = errors.New("behavior store epoch is stale")
	ErrNoActiveChannel = errors.New("behavior channel has no active deployment")
)

type EpochAuthority interface {
	CurrentStoreEpoch(context.Context) (string, error)
}

type Store struct {
	Pool       *pgxpool.Pool
	Appender   eventpostgres.Appender
	Epochs     EpochAuthority
	StoreEpoch string
	PublicKeys map[string]ed25519.PublicKey
	// PublicKeyWindows is required by production control planes and checked on
	// every signed mutation. Nil is retained for deterministic domain tests.
	PublicKeyWindows map[string]KeyWindow
	// PublicKeyPurposes prevents a valid release-owner key from being reused as
	// a risk-owner or rollback-automation credential.
	PublicKeyPurposes map[string]string
	Now               func() time.Time
}

type KeyWindow struct{ NotBefore, NotAfter time.Time }

type PayloadPointer struct{ Ref, Hash string }

type CreateSnapshotCommand struct {
	ID, EventID, OutboxID, PublishCommandID, TenantID, UserID, CorrelationID string
	Manifest                                                                 behavior.Manifest
	Actor                                                                    json.RawMessage
	EventPayload                                                             PayloadPointer
}

type RecordEvaluationCommand struct {
	ID, EventID, OutboxID, PublishCommandID, TenantID, UserID, CorrelationID string
	Report                                                                   behavior.EvaluationReport
	Actor                                                                    json.RawMessage
	EventPayload                                                             PayloadPointer
}

type PromoteCommand struct {
	ID, ChannelID, EventID, OutboxID, PublishCommandID, EvaluationID, TenantID, UserID, CorrelationID string
	Manifest                                                                                          behavior.Manifest
	Report                                                                                            behavior.EvaluationReport
	Request                                                                                           behavior.PromotionRequest
	Actor                                                                                             json.RawMessage
	EventPayload                                                                                      PayloadPointer
}

type RollbackCommand struct {
	ID, ChannelID, EventID, OutboxID, PublishCommandID, TenantID, UserID, CorrelationID string
	Request                                                                             behavior.RollbackRequest
	Actor                                                                               json.RawMessage
	EventPayload                                                                        PayloadPointer
}

type Result struct {
	ID, EventID string
	Hash        string
	Sequence    uint64
	Replayed    bool
}

// ResolveCurrent locks the tenant/profile/environment channel with the same
// transaction-scoped advisory key used by promotion, then returns the exact
// deployment a new Run must retain for its entire history.
func (store Store) ResolveCurrent(ctx context.Context, tx pgx.Tx, tenantID string, profile behavior.Profile, environment string) (behavior.ChannelBinding, error) {
	if tx == nil || tenantID == "" || !profile.Valid() || environment != "staging" && environment != "production" {
		return behavior.ChannelBinding{}, ErrCommand
	}
	var tenantContext string
	if err := tx.QueryRow(ctx, `SELECT current_setting('lites.tenant_id',true)`).Scan(&tenantContext); err != nil || tenantContext != tenantID {
		return behavior.ChannelBinding{}, errors.Join(err, ErrCommand)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1||':'||$2||':'||$3,0))`, tenantID, profile, environment); err != nil {
		return behavior.ChannelBinding{}, err
	}
	result := behavior.ChannelBinding{Profile: profile, Environment: environment}
	err := tx.QueryRow(ctx, `SELECT channel_id::text,sequence,snapshot_id,activated_at FROM agent.behavior_channel_deployments WHERE tenant_id=$1 AND profile_name=$2 AND environment=$3 ORDER BY sequence DESC LIMIT 1`, tenantID, profile, environment).Scan(&result.ChannelID, &result.Sequence, &result.SnapshotID, &result.ActivatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return behavior.ChannelBinding{}, ErrNoActiveChannel
	}
	if err != nil {
		return behavior.ChannelBinding{}, err
	}
	return result, nil
}

func (store Store) CreateSnapshot(ctx context.Context, command CreateSnapshotCommand) (Result, error) {
	if !store.valid() || !validCommon(command.ID, command.EventID, command.OutboxID, command.PublishCommandID, command.TenantID, command.UserID, command.CorrelationID, command.Actor, command.EventPayload) {
		return Result{}, ErrCommand
	}
	canonical, err := command.Manifest.Canonical()
	if err != nil {
		return Result{}, ErrCommand
	}
	manifest, err := json.Marshal(canonical)
	if err != nil {
		return Result{}, ErrCommand
	}
	hash, _ := canonical.Hash()
	snapshotID, _ := canonical.SnapshotID()
	now := store.now()
	return store.transact(ctx, command.TenantID, func(tx pgx.Tx) (Result, error) {
		tag, execErr := tx.Exec(ctx, `INSERT INTO agent.behavior_snapshots(id,tenant_id,snapshot_id,profile_name,manifest,manifest_hash,source_commit,created_by,created_event_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, command.ID, command.TenantID, snapshotID, canonical.Profile, manifest, hash, canonical.SourceCommit, command.UserID, command.EventID, now)
		if execErr != nil {
			return Result{}, mapError(execErr)
		}
		replayed := tag.RowsAffected() == 0
		if replayed {
			var exact bool
			if execErr = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.behavior_snapshots WHERE tenant_id=$1 AND id=$2 AND snapshot_id=$3 AND profile_name=$4 AND manifest=$5::jsonb AND manifest_hash=$6 AND source_commit=$7 AND created_by=$8 AND created_event_id=$9)`, command.TenantID, command.ID, snapshotID, canonical.Profile, manifest, hash, canonical.SourceCommit, command.UserID, command.EventID).Scan(&exact); execErr != nil || !exact {
				return Result{}, errors.Join(execErr, ErrConflict)
			}
		}
		input := publishInput(command.EventID, command.OutboxID, command.PublishCommandID, command.TenantID, command.UserID, "BehaviorSnapshotCreated", "behavior_snapshot", command.ID, 1, store.StoreEpoch, now, command.Actor, command.CorrelationID, command.EventPayload)
		if _, execErr = store.Appender.Append(ctx, tx, input); execErr != nil {
			return Result{}, execErr
		}
		return Result{ID: command.ID, EventID: command.EventID, Hash: hash, Replayed: replayed}, nil
	})
}

func (store Store) RecordEvaluation(ctx context.Context, command RecordEvaluationCommand) (Result, error) {
	if !store.valid() || !validCommon(command.ID, command.EventID, command.OutboxID, command.PublishCommandID, command.TenantID, command.UserID, command.CorrelationID, command.Actor, command.EventPayload) {
		return Result{}, ErrCommand
	}
	hash, err := command.Report.Hash()
	if err != nil {
		return Result{}, ErrCommand
	}
	report, err := json.Marshal(command.Report)
	if err != nil {
		return Result{}, ErrCommand
	}
	gate := command.Report.Evaluate()
	now := store.now()
	return store.transact(ctx, command.TenantID, func(tx pgx.Tx) (Result, error) {
		tag, execErr := tx.Exec(ctx, `INSERT INTO agent.behavior_evaluation_reports(id,tenant_id,report_id,profile_name,candidate_snapshot_id,baseline_snapshot_id,report,report_hash,passed,recorded_by,recorded_event_id,evaluated_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT DO NOTHING`, command.ID, command.TenantID, command.Report.ReportID, command.Report.Profile, command.Report.CandidateSnapshotID, command.Report.BaselineSnapshotID, report, hash, gate.Passed, command.UserID, command.EventID, command.Report.EvaluatedAt.UTC().Truncate(time.Microsecond), now)
		if execErr != nil {
			return Result{}, mapError(execErr)
		}
		replayed := tag.RowsAffected() == 0
		if replayed {
			var exact bool
			if execErr = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.behavior_evaluation_reports WHERE tenant_id=$1 AND id=$2 AND report_id=$3 AND report_hash=$4 AND passed=$5 AND recorded_by=$6 AND recorded_event_id=$7)`, command.TenantID, command.ID, command.Report.ReportID, hash, gate.Passed, command.UserID, command.EventID).Scan(&exact); execErr != nil || !exact {
				return Result{}, errors.Join(execErr, ErrConflict)
			}
		}
		input := publishInput(command.EventID, command.OutboxID, command.PublishCommandID, command.TenantID, command.UserID, "BehaviorEvaluationRecorded", "behavior_evaluation", command.ID, 1, store.StoreEpoch, now, command.Actor, command.CorrelationID, command.EventPayload)
		if _, execErr = store.Appender.Append(ctx, tx, input); execErr != nil {
			return Result{}, execErr
		}
		return Result{ID: command.ID, EventID: command.EventID, Hash: hash, Replayed: replayed}, nil
	})
}

func (store Store) Promote(ctx context.Context, command PromoteCommand) (Result, error) {
	if !store.valid() || !validCommon(command.ID, command.EventID, command.OutboxID, command.PublishCommandID, command.TenantID, command.UserID, command.CorrelationID, command.Actor, command.EventPayload) || command.ChannelID == "" || command.EvaluationID == "" || command.Request.TenantID != command.TenantID {
		return Result{}, ErrCommand
	}
	decision, err := store.ValidatePromotion(command.Manifest, command.Report, command.Request)
	if err != nil {
		return Result{}, errors.Join(err, ErrCommand)
	}
	now := store.now()
	approvals, _ := json.Marshal(command.Request.Approvals)
	rollout, _ := json.Marshal(command.Request.Rollout)
	autoRollback, _ := json.Marshal(command.Request.AutoRollback)
	return store.transact(ctx, command.TenantID, func(tx pgx.Tx) (Result, error) {
		tag, execErr := tx.Exec(ctx, `INSERT INTO agent.behavior_channel_deployments(id,tenant_id,channel_id,profile_name,environment,sequence,action,snapshot_id,previous_snapshot_id,evaluation_report_id,evaluation_report_hash,promotion_hash,rollout_policy,auto_rollback_policy,approvals,activation_event_id,activated_by,activated_at) VALUES($1,$2,$3,$4,$5,$6,'promote',$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) ON CONFLICT DO NOTHING`, command.ID, command.TenantID, command.ChannelID, command.Request.Profile, command.Request.Environment, command.Request.Sequence, command.Request.CandidateSnapshotID, command.Request.PreviousSnapshotID, command.EvaluationID, command.Request.EvaluationReportHash, decision.PromotionHash, rollout, autoRollback, approvals, command.EventID, command.UserID, now)
		if execErr != nil {
			return Result{}, mapError(execErr)
		}
		replayed := tag.RowsAffected() == 0
		if replayed {
			var exact bool
			if execErr = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.behavior_channel_deployments WHERE tenant_id=$1 AND id=$2 AND channel_id=$3 AND profile_name=$4 AND environment=$5 AND sequence=$6 AND action='promote' AND snapshot_id=$7 AND previous_snapshot_id=$8 AND evaluation_report_id=$9 AND evaluation_report_hash=$10 AND promotion_hash=$11 AND approvals=$12::jsonb AND activation_event_id=$13 AND activated_by=$14)`, command.TenantID, command.ID, command.ChannelID, command.Request.Profile, command.Request.Environment, command.Request.Sequence, command.Request.CandidateSnapshotID, command.Request.PreviousSnapshotID, command.EvaluationID, command.Request.EvaluationReportHash, decision.PromotionHash, approvals, command.EventID, command.UserID).Scan(&exact); execErr != nil || !exact {
				return Result{}, errors.Join(execErr, ErrConflict)
			}
		}
		input := publishInput(command.EventID, command.OutboxID, command.PublishCommandID, command.TenantID, command.UserID, "BehaviorSnapshotPromoted", "behavior_channel", command.ChannelID, command.Request.Sequence, store.StoreEpoch, now, command.Actor, command.CorrelationID, command.EventPayload)
		if _, execErr = store.Appender.Append(ctx, tx, input); execErr != nil {
			return Result{}, execErr
		}
		return Result{ID: command.ID, EventID: command.EventID, Hash: decision.PromotionHash, Sequence: command.Request.Sequence, Replayed: replayed}, nil
	})
}

func (store Store) Rollback(ctx context.Context, command RollbackCommand) (Result, error) {
	if !store.valid() || !validCommon(command.ID, command.EventID, command.OutboxID, command.PublishCommandID, command.TenantID, command.UserID, command.CorrelationID, command.Actor, command.EventPayload) || command.ChannelID == "" || command.Request.TenantID != command.TenantID {
		return Result{}, ErrCommand
	}
	rollbackHash, err := store.ValidateRollback(command.Request)
	if err != nil {
		return Result{}, errors.Join(err, ErrCommand)
	}
	now := store.now()
	return store.transact(ctx, command.TenantID, func(tx pgx.Tx) (Result, error) {
		tag, execErr := tx.Exec(ctx, `INSERT INTO agent.behavior_channel_deployments(id,tenant_id,channel_id,profile_name,environment,sequence,action,snapshot_id,previous_snapshot_id,approvals,incident_evidence_hash,rollback_hash,rollback_trigger,rollback_observed_value,rollback_threshold,automation_key_id,automation_signature,rollback_occurred_at,activation_event_id,activated_at) VALUES($1,$2,$3,$4,$5,$6,'rollback',$7,$8,'[]'::jsonb,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) ON CONFLICT DO NOTHING`, command.ID, command.TenantID, command.ChannelID, command.Request.Profile, command.Request.Environment, command.Request.Sequence, command.Request.ToSnapshotID, command.Request.FromSnapshotID, command.Request.IncidentEvidenceHash, rollbackHash, command.Request.Trigger, command.Request.ObservedValue, command.Request.Threshold, command.Request.AutomationKeyID, command.Request.Signature, command.Request.OccurredAt.UTC().Truncate(time.Microsecond), command.EventID, now)
		if execErr != nil {
			return Result{}, mapError(execErr)
		}
		replayed := tag.RowsAffected() == 0
		if replayed {
			var exact bool
			if execErr = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.behavior_channel_deployments WHERE tenant_id=$1 AND id=$2 AND channel_id=$3 AND profile_name=$4 AND environment=$5 AND sequence=$6 AND action='rollback' AND snapshot_id=$7 AND previous_snapshot_id=$8 AND incident_evidence_hash=$9 AND rollback_hash=$10 AND rollback_trigger=$11 AND rollback_observed_value=$12 AND rollback_threshold=$13 AND automation_key_id=$14 AND automation_signature=$15 AND rollback_occurred_at=$16 AND activation_event_id=$17)`, command.TenantID, command.ID, command.ChannelID, command.Request.Profile, command.Request.Environment, command.Request.Sequence, command.Request.ToSnapshotID, command.Request.FromSnapshotID, command.Request.IncidentEvidenceHash, rollbackHash, command.Request.Trigger, command.Request.ObservedValue, command.Request.Threshold, command.Request.AutomationKeyID, command.Request.Signature, command.Request.OccurredAt.UTC().Truncate(time.Microsecond), command.EventID).Scan(&exact); execErr != nil || !exact {
				return Result{}, errors.Join(execErr, ErrConflict)
			}
		}
		input := publishInput(command.EventID, command.OutboxID, command.PublishCommandID, command.TenantID, command.UserID, "BehaviorSnapshotRolledBack", "behavior_channel", command.ChannelID, command.Request.Sequence, store.StoreEpoch, now, command.Actor, command.CorrelationID, command.EventPayload)
		if _, execErr = store.Appender.Append(ctx, tx, input); execErr != nil {
			return Result{}, execErr
		}
		return Result{ID: command.ID, EventID: command.EventID, Hash: rollbackHash, Sequence: command.Request.Sequence, Replayed: replayed}, nil
	})
}

func (store Store) transact(ctx context.Context, tenantID string, operation func(pgx.Tx) (Result, error)) (Result, error) {
	if err := store.requireEpoch(ctx); err != nil {
		return Result{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return Result{}, err
	}
	result, err := operation(tx)
	if err != nil {
		return Result{}, err
	}
	return result, tx.Commit(ctx)
}

func (store Store) valid() bool {
	return store.Pool != nil && store.Epochs != nil && store.StoreEpoch != ""
}

func (store Store) activePublicKeys(now time.Time) map[string]ed25519.PublicKey {
	if store.PublicKeyWindows == nil {
		return store.PublicKeys
	}
	active := make(map[string]ed25519.PublicKey, len(store.PublicKeys))
	for keyID, key := range store.PublicKeys {
		window, ok := store.PublicKeyWindows[keyID]
		if ok && !window.NotBefore.IsZero() && window.NotAfter.After(window.NotBefore) && !now.Before(window.NotBefore) && now.Before(window.NotAfter) {
			active[keyID] = key
		}
	}
	return active
}

// ValidatePromotion runs every cryptographic, evaluation, validity-window and
// key-purpose check without mutating state. Application services use it before
// reserving an idempotency record; Promote repeats it at the commit boundary.
func (store Store) ValidatePromotion(manifest behavior.Manifest, report behavior.EvaluationReport, request behavior.PromotionRequest) (behavior.PromotionDecision, error) {
	now := store.now()
	if !store.validPromotionKeyPurposes(request) {
		return behavior.PromotionDecision{}, behavior.ErrAuthorization
	}
	return behavior.VerifyPromotion(manifest, report, request, store.activePublicKeys(now), now)
}

// ValidateRollback is the non-mutating counterpart of Rollback.
func (store Store) ValidateRollback(request behavior.RollbackRequest) (string, error) {
	now := store.now()
	if !store.validRollbackKeyPurpose(request) {
		return "", behavior.ErrAuthorization
	}
	return behavior.VerifyRollback(request, store.activePublicKeys(now), now)
}

func (store Store) validPromotionKeyPurposes(request behavior.PromotionRequest) bool {
	if store.PublicKeyPurposes == nil {
		return true
	}
	for _, approval := range request.Approvals {
		if store.PublicKeyPurposes[approval.KeyID] != approval.Role {
			return false
		}
	}
	return len(request.Approvals) == 2
}

func (store Store) validRollbackKeyPurpose(request behavior.RollbackRequest) bool {
	return store.PublicKeyPurposes == nil || store.PublicKeyPurposes[request.AutomationKeyID] == "rollback_automation"
}

func (store Store) requireEpoch(ctx context.Context) error {
	epoch, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || epoch == "" {
		return ErrConfiguration
	}
	if epoch != store.StoreEpoch {
		return ErrStaleEpoch
	}
	return nil
}

func (store Store) now() time.Time {
	value := time.Now().UTC()
	if store.Now != nil {
		value = store.Now().UTC()
	}
	return value.Truncate(time.Microsecond)
}

func validCommon(id, eventID, outboxID, publishCommandID, tenantID, userID, correlationID string, actor json.RawMessage, payload PayloadPointer) bool {
	var value map[string]any
	return id != "" && eventID != "" && outboxID != "" && publishCommandID != "" && tenantID != "" && userID != "" && correlationID != "" && payload.Ref != "" && payload.Hash != "" && json.Unmarshal(actor, &value) == nil && value != nil
}

func publishInput(eventID, outboxID, publishCommandID, tenantID, userID, eventType, aggregateKind, aggregateID string, aggregateVersion uint64, storeEpoch string, at time.Time, actor json.RawMessage, correlationID string, payload PayloadPointer) eventpostgres.Input {
	return eventpostgres.Input{
		Event:    eventpostgres.Event{ID: eventID, TenantID: tenantID, UserID: userID, EventType: eventType, SchemaVersion: 1, AggregateKind: aggregateKind, AggregateID: aggregateID, AggregateVersion: aggregateVersion, StoreEpoch: storeEpoch, OccurredAt: at, Actor: actor, CorrelationID: correlationID, PayloadRef: payload.Ref, PayloadHash: payload.Hash},
		Commands: []eventpostgres.OutboxCommand{{ID: outboxID, CommandID: publishCommandID, CommandType: "events.publish", PayloadRef: payload.Ref, PayloadHash: payload.Hash}},
	}
}

func mapError(err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && (databaseError.Code == "40001" || databaseError.Code == "23514" || databaseError.Code == "23505" || databaseError.Code == "23503") {
		return errors.Join(ErrConflict, err)
	}
	return err
}
