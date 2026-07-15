package postgres

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

const cancellationEventPayloadClass = "event-payload"

// RunCancellationService converges only LLM work whose external-send outcome
// is known. Prepared attempts are abandoned and their reservations released;
// dispatching and unreconciled outcome_unknown attempts remain blockers.
type RunCancellationService struct {
	Store    Store
	Payloads payload.Store
}

type cancellationPreparedProvider struct {
	attemptID, llmAttemptID, providerAttemptID, requestHash string
	reservationID, bucketID, operationKey                   string
	ordinal                                                 int
	reservedUnits                                           uint64
	prepareTokenExpiresAt                                   time.Time
}

type cancellationFinalizableLLM struct {
	attemptID, runID, attemptKey, contextManifestHash string
	streamGeneration                                  uint64
	providerAttemptIDs                                []string
	totalInputTokens, totalOutputTokens               uint64
	totalCostMicrounits                               uint64
}

func (service RunCancellationService) ConvergeRunCancellation(ctx context.Context, tenantID, runID, cancellationID, storeEpoch string) error {
	if !service.Store.valid() || service.Payloads == nil || tenantID == "" || runID == "" || cancellationID == "" || storeEpoch == "" {
		return ErrConfiguration
	}
	if storeEpoch != service.Store.StoreEpoch {
		return ErrStaleEpoch
	}
	if err := service.Store.requireEpoch(ctx); err != nil {
		return err
	}
	correlationID, err := ids.DeterministicUUID(service.Store.IDKey, "llm-run-cancellation-correlation", cancellationID)
	if err != nil {
		return ErrConfiguration
	}
	actor := json.RawMessage(`{"kind":"service","id":"llm-cancellation-converger"}`)
	prepared, err := service.loadPrepared(ctx, tenantID, runID, cancellationID, storeEpoch)
	if err != nil {
		return err
	}
	for _, candidate := range prepared {
		now := service.Store.now()
		providerIDs, identifierErr := service.Store.eventIDs("provider-attempt-abandoned", candidate.attemptID, 2)
		if identifierErr != nil {
			return ErrConfiguration
		}
		usageEventID, usageLedgerID, identifierErr := usageReleaseIdentifiers(service.Store.Billing.IDKey, candidate.reservationID)
		if identifierErr != nil {
			return ErrConfiguration
		}
		abandoned, putErr := service.putJSON(ctx, tenantID, providerIDs.Event, map[string]any{
			"subject_id": candidate.attemptID, "subject_version": 2,
			"llm_attempt_id": candidate.llmAttemptID, "provider_attempt_id": candidate.providerAttemptID,
			"ordinal": candidate.ordinal, "request_hash": candidate.requestHash,
			"reason_code": "explicit_cancel", "prepare_token_expires_at": candidate.prepareTokenExpiresAt,
			"abandoned_at": now,
		})
		if putErr != nil {
			return putErr
		}
		released, putErr := service.putJSON(ctx, tenantID, usageEventID, map[string]any{
			"subject_id": candidate.reservationID, "subject_version": 2,
			"units": 0, "cost_microunits": 0, "reservation_id": candidate.reservationID,
			"bucket_id": candidate.bucketID, "operation_key": candidate.operationKey,
			"released_units": candidate.reservedUnits, "ledger_entry_id": usageLedgerID,
			"reason_code": "explicit_cancel",
		})
		if putErr != nil {
			return putErr
		}
		if _, err = service.Store.CancelPreparedProviderAttempt(ctx, CancelPreparedProviderAttemptCommand{
			AttemptID: candidate.attemptID, TenantID: tenantID, CancellationID: cancellationID,
			CorrelationID: correlationID, Actor: actor, AbandonedEvent: abandoned, ReleasedEvent: released,
		}); err != nil {
			return err
		}
	}
	finalizable, err := service.loadFinalizable(ctx, tenantID, runID, cancellationID, storeEpoch)
	if err != nil {
		return err
	}
	for _, candidate := range finalizable {
		now := service.Store.now()
		finalizedIDs, identifierErr := service.Store.eventIDs("llm-attempt-finalized", candidate.attemptID, 2)
		if identifierErr != nil {
			return ErrConfiguration
		}
		finalized, putErr := service.putJSON(ctx, tenantID, finalizedIDs.Event, map[string]any{
			"subject_id": candidate.attemptID, "subject_version": 2,
			"run_id": candidate.runID, "attempt_key": candidate.attemptKey,
			"stream_generation": candidate.streamGeneration, "context_manifest_hash": candidate.contextManifestHash,
			"status": "cancelled", "selected_provider_attempt_id": nil,
			"provider_attempt_ids": candidate.providerAttemptIDs,
			"total_input_tokens":   candidate.totalInputTokens, "total_output_tokens": candidate.totalOutputTokens,
			"total_cost_microunits": candidate.totalCostMicrounits,
			"result_hash":           nil, "failure_code": "run_cancelled", "finalized_at": now,
		})
		if putErr != nil {
			return putErr
		}
		if _, err = service.Store.FinalizeLLMAttempt(ctx, FinalizeLLMAttemptCommand{
			AttemptID: candidate.attemptID, TenantID: tenantID, Status: "cancelled", FailureCode: "run_cancelled",
			CorrelationID: correlationID, Actor: actor, FinalizedEvent: finalized,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (service RunCancellationService) loadPrepared(ctx context.Context, tenantID, runID, cancellationID, storeEpoch string) ([]cancellationPreparedProvider, error) {
	tx, err := service.Store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT p.id::text,p.llm_attempt_id::text,p.provider_attempt_id,p.ordinal,p.request_hash,p.prepare_token_expires_at,
		u.id::text,u.bucket_id::text,u.operation_key,u.reserved_units
		FROM agent.llm_provider_attempts p
		JOIN agent.llm_attempts l ON l.tenant_id=p.tenant_id AND l.id=p.llm_attempt_id
		JOIN agent.runs r ON r.tenant_id=l.tenant_id AND r.id=l.run_id
		JOIN agent.run_cancellations c ON c.tenant_id=r.tenant_id AND c.id=r.active_cancellation_id
		JOIN contracts.usage_reservations u ON u.tenant_id=p.tenant_id AND u.id=p.usage_reservation_id
		WHERE p.tenant_id=$1 AND l.run_id=$2 AND c.id=$3 AND c.store_epoch=$4
		  AND c.status='terminating' AND r.cancel_requested_at IS NOT NULL
		  AND l.status='running' AND p.status='prepared' AND p.version=1 AND u.status='reserved'
		ORDER BY p.id`, tenantID, runID, cancellationID, storeEpoch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []cancellationPreparedProvider
	for rows.Next() {
		var value cancellationPreparedProvider
		if err = rows.Scan(&value.attemptID, &value.llmAttemptID, &value.providerAttemptID, &value.ordinal, &value.requestHash, &value.prepareTokenExpiresAt, &value.reservationID, &value.bucketID, &value.operationKey, &value.reservedUnits); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return values, tx.Commit(ctx)
}

func (service RunCancellationService) loadFinalizable(ctx context.Context, tenantID, runID, cancellationID, storeEpoch string) ([]cancellationFinalizableLLM, error) {
	tx, err := service.Store.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT l.id::text,l.run_id::text,l.attempt_key,l.stream_generation,l.context_manifest_hash,
		array_agg(p.id::text ORDER BY p.ordinal),sum(p.input_tokens),sum(p.output_tokens),sum(p.cost_microunits)
		FROM agent.llm_attempts l
		JOIN agent.runs r ON r.tenant_id=l.tenant_id AND r.id=l.run_id
		JOIN agent.run_cancellations c ON c.tenant_id=r.tenant_id AND c.id=r.active_cancellation_id
		JOIN agent.llm_provider_attempts p ON p.tenant_id=l.tenant_id AND p.llm_attempt_id=l.id
		WHERE l.tenant_id=$1 AND l.run_id=$2 AND c.id=$3 AND c.store_epoch=$4
		  AND c.status='terminating' AND r.cancel_requested_at IS NOT NULL AND l.status='running'
		GROUP BY l.id,l.run_id,l.attempt_key,l.stream_generation,l.context_manifest_hash
		HAVING count(*) FILTER (WHERE p.status IN ('prepared','dispatching') OR (p.status='outcome_unknown' AND p.version=3))=0
		ORDER BY l.id`, tenantID, runID, cancellationID, storeEpoch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []cancellationFinalizableLLM
	for rows.Next() {
		var value cancellationFinalizableLLM
		if err = rows.Scan(&value.attemptID, &value.runID, &value.attemptKey, &value.streamGeneration, &value.contextManifestHash, &value.providerAttemptIDs, &value.totalInputTokens, &value.totalOutputTokens, &value.totalCostMicrounits); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return values, tx.Commit(ctx)
}

func (service RunCancellationService) putJSON(ctx context.Context, tenantID, objectID string, value any) (PayloadPointer, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return PayloadPointer{}, err
	}
	manifest, err := service.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: cancellationEventPayloadClass, ContentType: "application/json"}, encoded)
	if err != nil {
		return PayloadPointer{}, err
	}
	return PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, nil
}

func usageReleaseIdentifiers(key []byte, reservationID string) (string, string, error) {
	seed := reservationID + ":" + strconv.FormatUint(2, 10)
	eventID, err := ids.DeterministicUUID(key, "usage-released:event", seed)
	if err != nil {
		return "", "", err
	}
	ledgerID, err := ids.DeterministicUUID(key, "usage-released:ledger", seed)
	return eventID, ledgerID, err
}
