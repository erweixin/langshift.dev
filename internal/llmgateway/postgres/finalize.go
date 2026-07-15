package postgres

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

type FinalizeLLMAttemptCommand struct {
	AttemptID, TenantID, Status, SelectedProviderAttemptID string
	ResultHash, FailureCode, CorrelationID                 string
	Actor                                                  json.RawMessage
	FinalizedEvent                                         PayloadPointer
}

type FinalizedLLMAttempt struct {
	AttemptID, Status, SelectedProviderAttemptID string
	ProviderAttemptIDs                           []string
	TotalInputTokens, TotalOutputTokens          uint64
	TotalCostMicrounits                          uint64
	Version                                      uint64
	FinalizedAt                                  time.Time
	Replayed                                     bool
}

func (store Store) FinalizeLLMAttempt(ctx context.Context, command FinalizeLLMAttemptCommand) (FinalizedLLMAttempt, error) {
	if !store.valid() || !validFinalize(command) {
		return FinalizedLLMAttempt{}, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return FinalizedLLMAttempt{}, err
	}
	now := store.now()
	identifiers, err := store.eventIDs("llm-attempt-finalized", command.AttemptID, 2)
	if err != nil {
		return FinalizedLLMAttempt{}, ErrConfiguration
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return FinalizedLLMAttempt{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return FinalizedLLMAttempt{}, err
	}
	var userID, currentStatus, contextHash string
	var version uint64
	var selected, resultHash, failureCode, finalizedEvent *string
	var finalizedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT user_id::text,status,version,context_manifest_hash,selected_provider_attempt_id::text,result_hash,failure_code,finalized_event_id::text,finalized_at FROM agent.llm_attempts WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.AttemptID).Scan(&userID, &currentStatus, &version, &contextHash, &selected, &resultHash, &failureCode, &finalizedEvent, &finalizedAt)
	if err != nil {
		return FinalizedLLMAttempt{}, ErrAttemptConflict
	}
	if currentStatus != "running" {
		if currentStatus == command.Status && version == 2 && equalOptional(selected, command.SelectedProviderAttemptID) && equalOptional(resultHash, command.ResultHash) && equalOptional(failureCode, command.FailureCode) && equalOptional(finalizedEvent, identifiers.Event) && finalizedAt != nil {
			result, loadErr := loadFinalized(ctx, tx, command.TenantID, command.AttemptID)
			if loadErr != nil {
				return FinalizedLLMAttempt{}, loadErr
			}
			result.Replayed = true
			if err = tx.Commit(ctx); err != nil {
				return FinalizedLLMAttempt{}, err
			}
			return result, nil
		}
		return FinalizedLLMAttempt{}, ErrAttemptConflict
	}
	rows, err := tx.Query(ctx, `SELECT id::text,status,version,input_tokens,output_tokens,cost_microunits,visible_output_started_at FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND llm_attempt_id=$2 ORDER BY ordinal FOR SHARE`, command.TenantID, command.AttemptID)
	if err != nil {
		return FinalizedLLMAttempt{}, err
	}
	result := FinalizedLLMAttempt{AttemptID: command.AttemptID, Status: command.Status, SelectedProviderAttemptID: command.SelectedProviderAttemptID, Version: 2, FinalizedAt: now}
	selectedStatus := ""
	selectedVisible := false
	for rows.Next() {
		var providerID, providerStatus string
		var providerVersion uint64
		var input, output, cost uint64
		var visibleAt *time.Time
		if err = rows.Scan(&providerID, &providerStatus, &providerVersion, &input, &output, &cost, &visibleAt); err != nil {
			rows.Close()
			return FinalizedLLMAttempt{}, err
		}
		if providerStatus == "prepared" || providerStatus == "dispatching" {
			rows.Close()
			return FinalizedLLMAttempt{}, ErrNotFinalizable
		}
		if providerStatus == "outcome_unknown" && providerVersion == 3 {
			rows.Close()
			return FinalizedLLMAttempt{}, ErrNotFinalizable
		}
		result.ProviderAttemptIDs = append(result.ProviderAttemptIDs, providerID)
		result.TotalInputTokens += input
		result.TotalOutputTokens += output
		result.TotalCostMicrounits += cost
		if providerID == command.SelectedProviderAttemptID {
			selectedStatus = providerStatus
			selectedVisible = visibleAt != nil
		}
	}
	if err = rows.Err(); err != nil {
		return FinalizedLLMAttempt{}, err
	}
	rows.Close()
	if (len(result.ProviderAttemptIDs) == 0 && command.Status != "failed" && command.Status != "cancelled") ||
		(command.Status == "completed" && selectedStatus != "completed") ||
		(command.Status == "partial_visible" && (selectedStatus == "" || selectedStatus == "abandoned" || !selectedVisible)) {
		return FinalizedLLMAttempt{}, ErrNotFinalizable
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.llm_attempts SET version=2,status=$1,selected_provider_attempt_id=NULLIF($2,'')::uuid,result_hash=NULLIF($3,''),failure_code=NULLIF($4,''),total_input_tokens=$5,total_output_tokens=$6,total_cost_microunits=$7,finalized_event_id=$8,finalized_at=$9,updated_at=$9 WHERE tenant_id=$10 AND id=$11 AND status='running' AND version=1`, command.Status, command.SelectedProviderAttemptID, command.ResultHash, command.FailureCode, result.TotalInputTokens, result.TotalOutputTokens, result.TotalCostMicrounits, identifiers.Event, now, command.TenantID, command.AttemptID)
	if err != nil || tag.RowsAffected() != 1 {
		return FinalizedLLMAttempt{}, ErrAttemptConflict
	}
	event := publishEvent(identifiers, eventpostgres.Event{TenantID: command.TenantID, UserID: userID, EventType: "LLMAttemptFinalized", SchemaVersion: 2, AggregateKind: "llm_attempt", AggregateID: command.AttemptID, AggregateVersion: 2, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID}, command.FinalizedEvent)
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return FinalizedLLMAttempt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return FinalizedLLMAttempt{}, err
	}
	return result, nil
}

func loadFinalized(ctx context.Context, tx pgx.Tx, tenantID, attemptID string) (FinalizedLLMAttempt, error) {
	var result FinalizedLLMAttempt
	var selected *string
	err := tx.QueryRow(ctx, `SELECT id::text,status,selected_provider_attempt_id::text,total_input_tokens,total_output_tokens,total_cost_microunits,version,finalized_at FROM agent.llm_attempts WHERE tenant_id=$1 AND id=$2`, tenantID, attemptID).Scan(&result.AttemptID, &result.Status, &selected, &result.TotalInputTokens, &result.TotalOutputTokens, &result.TotalCostMicrounits, &result.Version, &result.FinalizedAt)
	if err != nil {
		return FinalizedLLMAttempt{}, err
	}
	result.SelectedProviderAttemptID = deref(selected)
	rows, err := tx.Query(ctx, `SELECT id::text FROM agent.llm_provider_attempts WHERE tenant_id=$1 AND llm_attempt_id=$2 ORDER BY ordinal`, tenantID, attemptID)
	if err != nil {
		return FinalizedLLMAttempt{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return FinalizedLLMAttempt{}, err
		}
		result.ProviderAttemptIDs = append(result.ProviderAttemptIDs, id)
	}
	return result, rows.Err()
}

func validFinalize(command FinalizeLLMAttemptCommand) bool {
	if command.AttemptID == "" || command.TenantID == "" || command.CorrelationID == "" || !validActor(command.Actor) || !validPointer(command.FinalizedEvent) {
		return false
	}
	switch command.Status {
	case "completed":
		return command.SelectedProviderAttemptID != "" && command.ResultHash != "" && command.FailureCode == ""
	case "partial_visible":
		return command.SelectedProviderAttemptID != "" && command.ResultHash != "" && command.FailureCode != ""
	case "failed", "cancelled":
		return command.SelectedProviderAttemptID == "" && command.ResultHash == "" && command.FailureCode != ""
	default:
		return false
	}
}

type RecoveryTenantScan struct {
	StoreEpoch string
	After      string
	Limit      int
	ShardIndex int
	ShardCount int
}

func (store Store) ListRecoverableTenantIDs(ctx context.Context, scan RecoveryTenantScan) ([]string, error) {
	if !store.valid() || scan.StoreEpoch == "" || scan.Limit < 1 || scan.Limit > 5000 || scan.ShardCount < 1 || scan.ShardIndex < 0 || scan.ShardIndex >= scan.ShardCount {
		return nil, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil || scan.StoreEpoch != store.StoreEpoch {
		return nil, ErrStaleEpoch
	}
	rows, err := store.Pool.Query(ctx, `SELECT tenant_id FROM agent.list_recoverable_llm_provider_tenants($1,NULLIF($2,'')::uuid,$3,$4,$5,$6)`, scan.StoreEpoch, scan.After, scan.Limit, scan.ShardIndex, scan.ShardCount, store.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var tenantID string
		if err = rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		result = append(result, tenantID)
	}
	sort.Strings(result)
	return result, rows.Err()
}
