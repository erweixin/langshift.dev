package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	billingpostgres "github.com/langshift/lites/internal/billing/postgres"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

type RecordProviderResultCommand struct {
	AttemptID, TenantID, CompletionToken string
	Status, ResponseHash, UsageStatus    string
	ProviderRequestID, ErrorClass        string
	InputTokens, OutputTokens            uint64
	CostMicrounits                       uint64
	BillableUnits                        uint64
	VisibleOutputStartedAt               *time.Time
	ReconciliationDueAt                  *time.Time
	CorrelationID                        string
	Actor                                json.RawMessage
	RecordedEvent                        PayloadPointer
	SettledEvent                         PayloadPointer
}

type ProviderResult struct {
	AttemptID, Status, UsageStatus string
	Version                        uint64
	FinishedAt                     time.Time
}

type MarkOutcomeUnknownCommand struct {
	AttemptID, TenantID, ReasonCode, CorrelationID string
	ReconciliationDueAt                            time.Time
	Actor                                          json.RawMessage
	RecordedEvent                                  PayloadPointer
}

// MarkDispatchOutcomeUnknown closes an expired dispatch authorization without
// ever issuing another authorization for the same physical attempt.
func (store Store) MarkDispatchOutcomeUnknown(ctx context.Context, command MarkOutcomeUnknownCommand) (ProviderResult, error) {
	if !store.valid() || command.AttemptID == "" || command.TenantID == "" || command.ReasonCode == "" || command.CorrelationID == "" || !validActor(command.Actor) || !validPointer(command.RecordedEvent) {
		return ProviderResult{}, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return ProviderResult{}, err
	}
	now := store.now()
	command.ReconciliationDueAt = command.ReconciliationDueAt.UTC().Truncate(time.Microsecond)
	if !command.ReconciliationDueAt.After(now) {
		return ProviderResult{}, ErrInvalidCommand
	}
	identifiers, err := store.eventIDs("provider-attempt-recorded", command.AttemptID, 3)
	if err != nil {
		return ProviderResult{}, ErrConfiguration
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ProviderResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return ProviderResult{}, err
	}
	var userID string
	var deadline time.Time
	err = tx.QueryRow(ctx, `SELECT user_id::text,completion_deadline FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND id=$2 AND status='dispatching' AND version=2 FOR UPDATE`, command.TenantID, command.AttemptID).Scan(&userID, &deadline)
	if err != nil || deadline.After(now) {
		return ProviderResult{}, ErrProviderConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.llm_provider_attempts SET version=3,status='outcome_unknown',completion_token_hash=NULL,usage_status='unknown',error_class=$1,reconciliation_due_at=$2,finished_at=$3,recorded_event_id=$4,updated_at=$3 WHERE tenant_id=$5 AND id=$6 AND status='dispatching' AND version=2 AND completion_deadline<=$3`, command.ReasonCode, command.ReconciliationDueAt, now, identifiers.Event, command.TenantID, command.AttemptID)
	if err != nil || tag.RowsAffected() != 1 {
		return ProviderResult{}, ErrProviderConflict
	}
	event := publishEvent(identifiers, eventpostgres.Event{TenantID: command.TenantID, UserID: userID, EventType: "ProviderAttemptRecorded", SchemaVersion: 2, AggregateKind: "provider_attempt", AggregateID: command.AttemptID, AggregateVersion: 3, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID}, command.RecordedEvent)
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return ProviderResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ProviderResult{}, err
	}
	return ProviderResult{AttemptID: command.AttemptID, Status: "outcome_unknown", UsageStatus: "unknown", Version: 3, FinishedAt: now}, nil
}

func (store Store) RecordProviderResult(ctx context.Context, command RecordProviderResultCommand) (ProviderResult, error) {
	if !store.valid() || !validResult(command) {
		return ProviderResult{}, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return ProviderResult{}, err
	}
	digest, err := store.completionTokens().Digest(command.CompletionToken)
	if err != nil {
		return ProviderResult{}, ErrDispatchToken
	}
	now := store.now()
	visibleAt := truncateOptional(command.VisibleOutputStartedAt)
	reconciliationDue := truncateOptional(command.ReconciliationDueAt)
	if command.Status == "outcome_unknown" && (reconciliationDue == nil || !reconciliationDue.After(now)) {
		return ProviderResult{}, ErrInvalidCommand
	}
	identifiers, err := store.eventIDs("provider-attempt-recorded", command.AttemptID, 3)
	if err != nil {
		return ProviderResult{}, ErrConfiguration
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ProviderResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return ProviderResult{}, err
	}
	var userID, status string
	var version uint64
	var storedDigest []byte
	var cost providerCostBinding
	err = tx.QueryRow(ctx, `SELECT user_id::text,status,version,completion_token_hash,provider_attempt_id,provider_id,model_id,model_version,pricing_version,request_hash,byok,usage_reservation_id::text FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.AttemptID).Scan(&userID, &status, &version, &storedDigest, &cost.ProviderAttemptID, &cost.ProviderID, &cost.ModelID, &cost.ModelVersion, &cost.PricingVersion, &cost.RequestHash, &cost.BYOK, &cost.ReservationID)
	if err != nil || status != "dispatching" || version != 2 || !bytes.Equal(storedDigest, digest[:]) {
		return ProviderResult{}, ErrDispatchToken
	}
	if cost.BYOK && command.CostMicrounits != 0 {
		return ProviderResult{}, ErrInvalidCommand
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.llm_provider_attempts SET version=3,status=$1,completion_token_hash=NULL,response_hash=NULLIF($2,''),usage_status=$3,provider_request_id=NULLIF($4,''),error_class=NULLIF($5,''),input_tokens=$6,output_tokens=$7,cost_microunits=$8,visible_output_started_at=$9,reconciliation_due_at=$10,finished_at=$11,recorded_event_id=$12,updated_at=$11 WHERE tenant_id=$13 AND id=$14 AND status='dispatching' AND version=2 AND completion_token_hash=$15`, command.Status, command.ResponseHash, command.UsageStatus, command.ProviderRequestID, command.ErrorClass, command.InputTokens, command.OutputTokens, command.CostMicrounits, visibleAt, reconciliationDue, now, identifiers.Event, command.TenantID, command.AttemptID, digest[:])
	if err != nil || tag.RowsAffected() != 1 {
		return ProviderResult{}, ErrProviderConflict
	}
	if command.Status != "outcome_unknown" {
		if _, err = store.Billing.SettleInTx(ctx, tx, billingpostgres.SettleCommand{ReservationID: cost.ReservationID, TenantID: command.TenantID, ProviderAttemptID: command.AttemptID, ActualUnits: command.BillableUnits, CorrelationID: command.CorrelationID, CausationID: identifiers.Event, Actor: command.Actor, SettledEvent: billingpostgres.PayloadPointer{Ref: command.SettledEvent.Ref, Hash: command.SettledEvent.Hash}}); err != nil {
			return ProviderResult{}, err
		}
		cost.ResponseHash, cost.UsageStatus = command.ResponseHash, command.UsageStatus
		if err = store.recordProviderCost(ctx, tx, identifiers.Event, command.TenantID, userID, command.AttemptID, cost, command.InputTokens, command.OutputTokens, command.CostMicrounits, now); err != nil {
			return ProviderResult{}, err
		}
	}
	event := publishEvent(identifiers, eventpostgres.Event{TenantID: command.TenantID, UserID: userID, EventType: "ProviderAttemptRecorded", SchemaVersion: 2, AggregateKind: "provider_attempt", AggregateID: command.AttemptID, AggregateVersion: 3, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID}, command.RecordedEvent)
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return ProviderResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ProviderResult{}, err
	}
	return ProviderResult{AttemptID: command.AttemptID, Status: command.Status, UsageStatus: command.UsageStatus, Version: 3, FinishedAt: now}, nil
}

type AbandonProviderAttemptCommand struct {
	AttemptID, TenantID, ReasonCode, CorrelationID string
	Actor                                          json.RawMessage
	AbandonedEvent                                 PayloadPointer
	ReleasedEvent                                  PayloadPointer
}

type CancelPreparedProviderAttemptCommand struct {
	AttemptID, TenantID, CancellationID, CorrelationID string
	Actor                                              json.RawMessage
	AbandonedEvent                                     PayloadPointer
	ReleasedEvent                                      PayloadPointer
}

// CancelPreparedProviderAttempt revokes an undispatched physical attempt only
// after the parent Run has entered the durable cancellation barrier. A
// dispatching attempt is never handled here because the provider may already
// have received it; that path must retain outcome reconciliation authority.
func (store Store) CancelPreparedProviderAttempt(ctx context.Context, command CancelPreparedProviderAttemptCommand) (ProviderResult, error) {
	if !store.valid() || command.AttemptID == "" || command.TenantID == "" || command.CancellationID == "" || command.CorrelationID == "" || !validActor(command.Actor) || !validPointer(command.AbandonedEvent) || !validPointer(command.ReleasedEvent) {
		return ProviderResult{}, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return ProviderResult{}, err
	}
	now := store.now()
	identifiers, err := store.eventIDs("provider-attempt-abandoned", command.AttemptID, 2)
	if err != nil {
		return ProviderResult{}, ErrConfiguration
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ProviderResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return ProviderResult{}, err
	}
	var llmAttemptID, requestEventID string
	err = tx.QueryRow(ctx, `SELECT l.id::text,c.request_event_id::text
		FROM agent.llm_attempts l
		JOIN agent.runs r ON r.tenant_id=l.tenant_id AND r.id=l.run_id
		JOIN agent.run_cancellations c ON c.tenant_id=r.tenant_id AND c.id=r.active_cancellation_id
		WHERE l.tenant_id=$1 AND l.id=(SELECT p.llm_attempt_id FROM agent.llm_provider_attempts p WHERE p.tenant_id=$1 AND p.id=$2)
		  AND c.id=$3 AND c.store_epoch=$4 AND c.status='terminating'
		  AND r.cancel_requested_at IS NOT NULL AND l.status='running'
		FOR UPDATE OF l,r,c`, command.TenantID, command.AttemptID, command.CancellationID, store.StoreEpoch).
		Scan(&llmAttemptID, &requestEventID)
	if err != nil {
		return ProviderResult{}, ErrProviderConflict
	}
	var userID, reservationID, lockedLLMAttempt string
	err = tx.QueryRow(ctx, `SELECT user_id::text,usage_reservation_id::text,llm_attempt_id::text
		FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND id=$2 AND status='prepared' AND version=1 FOR UPDATE`, command.TenantID, command.AttemptID).
		Scan(&userID, &reservationID, &lockedLLMAttempt)
	if err != nil || lockedLLMAttempt != llmAttemptID {
		return ProviderResult{}, ErrProviderConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.llm_provider_attempts
		SET version=2,status='abandoned',prepare_token_hash=NULL,usage_status='unavailable',error_class='explicit_cancel',
		  finished_at=$1,abandoned_event_id=$2,updated_at=$1
		WHERE tenant_id=$3 AND id=$4 AND status='prepared' AND version=1`, now, identifiers.Event, command.TenantID, command.AttemptID)
	if err != nil || tag.RowsAffected() != 1 {
		return ProviderResult{}, ErrProviderConflict
	}
	if _, err = store.Billing.ReleaseInTx(ctx, tx, billingpostgres.ReleaseCommand{ReservationID: reservationID, TenantID: command.TenantID, ReasonCode: "explicit_cancel", CorrelationID: command.CorrelationID, CausationID: identifiers.Event, Actor: command.Actor, ReleasedEvent: billingpostgres.PayloadPointer{Ref: command.ReleasedEvent.Ref, Hash: command.ReleasedEvent.Hash}}); err != nil {
		return ProviderResult{}, err
	}
	causationID := requestEventID
	event := publishEvent(identifiers, eventpostgres.Event{TenantID: command.TenantID, UserID: userID, EventType: "ProviderAttemptAbandoned", SchemaVersion: 1, AggregateKind: "provider_attempt", AggregateID: command.AttemptID, AggregateVersion: 2, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID}, command.AbandonedEvent)
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return ProviderResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ProviderResult{}, err
	}
	return ProviderResult{AttemptID: command.AttemptID, Status: "abandoned", UsageStatus: "unavailable", Version: 2, FinishedAt: now}, nil
}

func (store Store) AbandonExpiredProviderAttempt(ctx context.Context, command AbandonProviderAttemptCommand) (ProviderResult, error) {
	if !store.valid() || command.AttemptID == "" || command.TenantID == "" || command.ReasonCode == "" || command.CorrelationID == "" || !validActor(command.Actor) || !validPointer(command.AbandonedEvent) || !validPointer(command.ReleasedEvent) {
		return ProviderResult{}, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return ProviderResult{}, err
	}
	now := store.now()
	identifiers, err := store.eventIDs("provider-attempt-abandoned", command.AttemptID, 2)
	if err != nil {
		return ProviderResult{}, ErrConfiguration
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ProviderResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return ProviderResult{}, err
	}
	var userID, reservationID string
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT user_id::text,prepare_token_expires_at,usage_reservation_id::text FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND id=$2 AND status='prepared' AND version=1 FOR UPDATE`, command.TenantID, command.AttemptID).Scan(&userID, &expiresAt, &reservationID)
	if err != nil || expiresAt.After(now) {
		return ProviderResult{}, ErrProviderConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.llm_provider_attempts SET version=2,status='abandoned',prepare_token_hash=NULL,usage_status='unavailable',error_class=$1,finished_at=$2,abandoned_event_id=$3,updated_at=$2 WHERE tenant_id=$4 AND id=$5 AND status='prepared' AND version=1 AND prepare_token_expires_at<=$2`, command.ReasonCode, now, identifiers.Event, command.TenantID, command.AttemptID)
	if err != nil || tag.RowsAffected() != 1 {
		return ProviderResult{}, ErrProviderConflict
	}
	if _, err = store.Billing.ReleaseInTx(ctx, tx, billingpostgres.ReleaseCommand{ReservationID: reservationID, TenantID: command.TenantID, ReasonCode: command.ReasonCode, CorrelationID: command.CorrelationID, CausationID: identifiers.Event, Actor: command.Actor, ReleasedEvent: billingpostgres.PayloadPointer{Ref: command.ReleasedEvent.Ref, Hash: command.ReleasedEvent.Hash}}); err != nil {
		return ProviderResult{}, err
	}
	event := publishEvent(identifiers, eventpostgres.Event{TenantID: command.TenantID, UserID: userID, EventType: "ProviderAttemptAbandoned", SchemaVersion: 1, AggregateKind: "provider_attempt", AggregateID: command.AttemptID, AggregateVersion: 2, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID}, command.AbandonedEvent)
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return ProviderResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ProviderResult{}, err
	}
	return ProviderResult{AttemptID: command.AttemptID, Status: "abandoned", UsageStatus: "unavailable", Version: 2, FinishedAt: now}, nil
}

type ReconcileProviderUsageCommand struct {
	AttemptID, TenantID, ProviderRequestID, EvidenceHash string
	InputTokens, OutputTokens                            uint64
	CostMicrounits                                       uint64
	BillableUnits                                        uint64
	CorrelationID                                        string
	Actor                                                json.RawMessage
	ReconciledEvent                                      PayloadPointer
	SettledEvent                                         PayloadPointer
}

func (store Store) ReconcileProviderUsage(ctx context.Context, command ReconcileProviderUsageCommand) (ProviderResult, error) {
	if !store.valid() || command.AttemptID == "" || command.TenantID == "" || command.ProviderRequestID == "" || command.EvidenceHash == "" || command.CorrelationID == "" || !validActor(command.Actor) || !validPointer(command.ReconciledEvent) || !validPointer(command.SettledEvent) {
		return ProviderResult{}, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return ProviderResult{}, err
	}
	now := store.now()
	identifiers, err := store.eventIDs("provider-attempt-reconciled", command.AttemptID, 4)
	if err != nil {
		return ProviderResult{}, ErrConfiguration
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ProviderResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return ProviderResult{}, err
	}
	var userID string
	var cost providerCostBinding
	err = tx.QueryRow(ctx, `SELECT user_id::text,provider_attempt_id,provider_id,model_id,model_version,pricing_version,request_hash,byok,usage_reservation_id::text FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND id=$2 AND status='outcome_unknown' AND version=3 AND usage_status='unknown' FOR UPDATE`, command.TenantID, command.AttemptID).Scan(&userID, &cost.ProviderAttemptID, &cost.ProviderID, &cost.ModelID, &cost.ModelVersion, &cost.PricingVersion, &cost.RequestHash, &cost.BYOK, &cost.ReservationID)
	if err != nil {
		return ProviderResult{}, ErrProviderConflict
	}
	if cost.BYOK && command.CostMicrounits != 0 {
		return ProviderResult{}, ErrInvalidCommand
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.llm_provider_attempts SET version=4,usage_status='confirmed',provider_request_id=$1,input_tokens=$2,output_tokens=$3,cost_microunits=$4,reconciled_at=$5,reconciliation_evidence_hash=$6,reconciled_event_id=$7,updated_at=$5 WHERE tenant_id=$8 AND id=$9 AND status='outcome_unknown' AND version=3 AND usage_status='unknown'`, command.ProviderRequestID, command.InputTokens, command.OutputTokens, command.CostMicrounits, now, command.EvidenceHash, identifiers.Event, command.TenantID, command.AttemptID)
	if err != nil || tag.RowsAffected() != 1 {
		return ProviderResult{}, ErrProviderConflict
	}
	if _, err = store.Billing.SettleInTx(ctx, tx, billingpostgres.SettleCommand{ReservationID: cost.ReservationID, TenantID: command.TenantID, ProviderAttemptID: command.AttemptID, ActualUnits: command.BillableUnits, CorrelationID: command.CorrelationID, CausationID: identifiers.Event, Actor: command.Actor, SettledEvent: billingpostgres.PayloadPointer{Ref: command.SettledEvent.Ref, Hash: command.SettledEvent.Hash}}); err != nil {
		return ProviderResult{}, err
	}
	cost.UsageStatus = "confirmed"
	if err = store.recordProviderCost(ctx, tx, identifiers.Event, command.TenantID, userID, command.AttemptID, cost, command.InputTokens, command.OutputTokens, command.CostMicrounits, now); err != nil {
		return ProviderResult{}, err
	}
	event := publishEvent(identifiers, eventpostgres.Event{TenantID: command.TenantID, UserID: userID, EventType: "ProviderAttemptReconciled", SchemaVersion: 1, AggregateKind: "provider_attempt", AggregateID: command.AttemptID, AggregateVersion: 4, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID}, command.ReconciledEvent)
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return ProviderResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ProviderResult{}, err
	}
	return ProviderResult{AttemptID: command.AttemptID, Status: "outcome_unknown", UsageStatus: "confirmed", Version: 4, FinishedAt: now}, nil
}

func validResult(command RecordProviderResultCommand) bool {
	if command.AttemptID == "" || command.TenantID == "" || command.CompletionToken == "" || command.CorrelationID == "" || !validActor(command.Actor) || !validPointer(command.RecordedEvent) {
		return false
	}
	switch command.Status {
	case "completed":
		return command.ResponseHash != "" && command.ErrorClass == "" && containsString([]string{"confirmed", "estimated"}, command.UsageStatus) && command.ReconciliationDueAt == nil && validPointer(command.SettledEvent)
	case "failed", "cancelled":
		return command.ErrorClass != "" && containsString([]string{"confirmed", "estimated"}, command.UsageStatus) && command.ReconciliationDueAt == nil && validPointer(command.SettledEvent)
	case "outcome_unknown":
		return command.ResponseHash == "" && command.ErrorClass != "" && command.UsageStatus == "unknown" && command.ReconciliationDueAt != nil && command.BillableUnits == 0 && !validPointer(command.SettledEvent)
	default:
		return false
	}
}

type providerCostBinding struct {
	ProviderAttemptID, ProviderID, ModelID, ModelVersion   string
	PricingVersion, RequestHash, ResponseHash, UsageStatus string
	ReservationID                                          string
	BYOK                                                   bool
}

func (store Store) recordProviderCost(ctx context.Context, tx pgx.Tx, recordedEventID, tenantID, userID, attemptID string, binding providerCostBinding, inputTokens, outputTokens, costMicrounits uint64, now time.Time) error {
	costID, err := ids.DeterministicUUID(store.IDKey, "provider-cost", attemptID)
	if err != nil {
		return ErrConfiguration
	}
	_, err = tx.Exec(ctx, `INSERT INTO contracts.provider_costs(id,tenant_id,user_id,provider_attempt_id,provider_id,model_id,input_tokens,output_tokens,cost_microunits,byok,recorded_at,provider_attempt_row_id,model_version,pricing_version,request_hash,response_hash,usage_status,reservation_id,recorded_event_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,NULLIF($16,''),$17,$18,$19)`, costID, tenantID, userID, binding.ProviderAttemptID, binding.ProviderID, binding.ModelID, inputTokens, outputTokens, costMicrounits, binding.BYOK, now, attemptID, binding.ModelVersion, binding.PricingVersion, binding.RequestHash, binding.ResponseHash, binding.UsageStatus, binding.ReservationID, recordedEventID)
	if err != nil {
		return ErrProviderConflict
	}
	return nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func truncateOptional(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	canonical := value.UTC().Truncate(time.Microsecond)
	return &canonical
}
