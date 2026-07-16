package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

const cancellationReconciliationDelay = 5 * time.Second

type RequestRunCancellationCommand struct {
	CancellationID, TenantID, UserID, RunID string
	ExpectedRunVersion                      uint64
	Reason, RequestHash, CorrelationID      string
	Actor                                   json.RawMessage
	RequestEvent, SettlementEvent           PayloadPointer
	AttemptCancelledEvent                   PayloadPointer
	ReconcileCommand                        PayloadPointer
	PropagateCommand                        PayloadPointer
}

type RunCancellationResult struct {
	CancellationID     string                `json:"cancellation_id"`
	CancelGeneration   uint64                `json:"cancel_generation"`
	CancellationStatus string                `json:"cancellation_status"`
	RunID              string                `json:"run_id"`
	RunVersion         uint64                `json:"run_version"`
	RunStatus          statemachine.RunState `json:"run_status"`
	RequestEventID     string                `json:"request_event_id"`
	SettlementEventID  string                `json:"settlement_event_id,omitempty"`
	ReconcileCommandID string                `json:"reconcile_command_id,omitempty"`
	UpdatedAt          time.Time             `json:"updated_at"`
	Settled            bool                  `json:"settled"`
	Replayed           bool                  `json:"replayed"`
}

type cancellationIDs struct {
	requestEvent, requestOutbox, requestPublish          string
	settlementEvent, settlementOutbox, settlementPublish string
	reconcileOutbox, reconcileCommand, reconcileJob      string
}

type cancellationPropagationIDs struct{ outbox, command, job string }

type cancellationAttemptSettlement struct {
	TenantID, UserID, StoreEpoch, CorrelationID string
	Actor                                       json.RawMessage
	Payload                                     PayloadPointer
}

// RequestCancellation records the durable cancellation generation before it
// invalidates execution rights. A Run becomes terminal only when the barrier
// has no ToolCall, LLM, runtime, or child-group blockers.
func (store RunStore) RequestCancellation(ctx context.Context, command RequestRunCancellationCommand) (RunCancellationResult, error) {
	if !store.validClaim() || !validRequestRunCancellation(command) {
		return RunCancellationResult{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, store.StoreEpoch); err != nil {
		return RunCancellationResult{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RunCancellationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := store.RequestCancellationInTx(ctx, tx, command)
	if err != nil {
		return RunCancellationResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RunCancellationResult{}, err
	}
	return result, nil
}

// RequestCancellationInTx lets the public API idempotency response and the
// cancellation barrier share one commit. Epoch validation remains the
// caller's responsibility and must happen before opening the transaction.
func (store RunStore) RequestCancellationInTx(ctx context.Context, tx pgx.Tx, command RequestRunCancellationCommand) (RunCancellationResult, error) {
	if tx == nil || !store.validClaimCore() || store.LeaseTTL <= 0 || !validRequestRunCancellation(command) {
		return RunCancellationResult{}, ErrInvalidCommand
	}
	identifiers, err := store.cancellationIdentifiers(command.CancellationID)
	if err != nil {
		return RunCancellationResult{}, ErrConfiguration
	}
	now := store.claimNow()
	reconcileAt := now.Add(cancellationReconciliationDelay).UTC().Truncate(time.Microsecond)
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return RunCancellationResult{}, err
	}

	// Every Worker terminal/heartbeat path locks inbox before Run. Observe the
	// current execution tuple and take that same first lock before locking Run,
	// otherwise cancellation can deadlock with a Worker that already owns inbox.
	var observedCommand, observedAttempt sql.NullString
	if err = tx.QueryRow(ctx, `SELECT active_command_id::text,active_attempt_id::text FROM agent.runs WHERE id=$1 AND tenant_id=$2 AND user_id=$3`, command.RunID, command.TenantID, command.UserID).Scan(&observedCommand, &observedAttempt); errors.Is(err, pgx.ErrNoRows) {
		return RunCancellationResult{}, ErrRunConflict
	} else if err != nil {
		return RunCancellationResult{}, err
	}
	if observedCommand.Valid != observedAttempt.Valid {
		return RunCancellationResult{}, ErrRunConflict
	}
	if observedCommand.Valid {
		var inboxID string
		if err = tx.QueryRow(ctx, `SELECT id::text FROM agent.inbox WHERE tenant_id=$1 AND command_id=$2 AND owner_attempt_id=$3 AND status='running' FOR UPDATE`, command.TenantID, observedCommand.String, observedAttempt.String).Scan(&inboxID); err != nil {
			return RunCancellationResult{}, ErrRunConflict
		}
	}

	var runStatus string
	var runVersion, cancelGeneration, currentFence uint64
	var activeCancellation, pendingCommand, activeCommand, activeAttempt sql.NullString
	err = tx.QueryRow(ctx, `SELECT status,run_version,cancel_generation,current_fence,active_cancellation_id::text,pending_command_id::text,active_command_id::text,active_attempt_id::text FROM agent.runs WHERE id=$1 AND tenant_id=$2 AND user_id=$3 FOR UPDATE`, command.RunID, command.TenantID, command.UserID).Scan(&runStatus, &runVersion, &cancelGeneration, &currentFence, &activeCancellation, &pendingCommand, &activeCommand, &activeAttempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunCancellationResult{}, ErrRunConflict
	}
	if err != nil {
		return RunCancellationResult{}, err
	}
	if activeCommand.Valid != observedCommand.Valid || activeAttempt.Valid != observedAttempt.Valid || activeCommand.Valid && (activeCommand.String != observedCommand.String || activeAttempt.String != observedAttempt.String) {
		return RunCancellationResult{}, ErrRunConflict
	}
	if activeCancellation.Valid {
		result, exact, replayErr := loadCancellationReplay(ctx, tx, command, runStatus, runVersion, activeCancellation.String)
		if replayErr != nil {
			return RunCancellationResult{}, replayErr
		}
		if !exact {
			return RunCancellationResult{}, ErrRunConflict
		}
		return result, nil
	}
	state := statemachine.RunState(runStatus)
	if statemachine.Runs.IsTerminal(state) {
		return RunCancellationResult{RunID: command.RunID, RunVersion: runVersion, RunStatus: state, UpdatedAt: now, Settled: true}, nil
	}
	if runVersion != command.ExpectedRunVersion || cancelGeneration == ^uint64(0) {
		return RunCancellationResult{}, ErrRunConflict
	}
	nextGeneration := cancelGeneration + 1
	blockers, err := countCancellationBlockers(ctx, tx, command.TenantID, command.RunID)
	if err != nil {
		return RunCancellationResult{}, err
	}
	settled := blockers == 0
	var directChildren int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND parent_run_id=$2 AND status NOT IN ('succeeded','failed','cancelled','expired')`, command.TenantID, command.RunID).Scan(&directChildren); err != nil {
		return RunCancellationResult{}, err
	}
	needsPropagation := directChildren > 0
	propagation, err := store.cancellationPropagationIdentifiers(command.CancellationID)
	if err != nil {
		return RunCancellationResult{}, err
	}
	cancellationStatus := "terminating"
	if settled {
		cancellationStatus = "settled"
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent.run_cancellations(id,tenant_id,run_id,root_cancellation_id,parent_cancellation_id,cancel_generation,status,requested_by,requested_at,reason,store_epoch,request_hash,request_event_id,request_payload_ref,request_payload_hash,settlement_payload_ref,settlement_payload_hash,attempt_cancelled_payload_ref,attempt_cancelled_payload_hash,reconciliation_due_at,propagation_complete,propagation_command_id,propagation_payload_ref,propagation_payload_hash,created_at,updated_at) VALUES($1,$2,$3,$1,NULL,$4,'requested',$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,NULLIF($19,'')::uuid,NULLIF($20,''),NULLIF($21,''),$6,$6)`, command.CancellationID, command.TenantID, command.RunID, nextGeneration, command.UserID, now, command.Reason, store.StoreEpoch, command.RequestHash, identifiers.requestEvent, command.RequestEvent.Ref, command.RequestEvent.Hash, command.SettlementEvent.Ref, command.SettlementEvent.Hash, command.AttemptCancelledEvent.Ref, command.AttemptCancelledEvent.Hash, reconcileAt, !needsPropagation, propagationValue(needsPropagation, propagation.command), propagationValue(needsPropagation, command.PropagateCommand.Ref), propagationValue(needsPropagation, command.PropagateCommand.Hash))
	if err != nil {
		return RunCancellationResult{}, err
	}

	requestCommands := []eventpostgres.OutboxCommand{{ID: identifiers.requestOutbox, CommandID: identifiers.requestPublish, CommandType: "events.publish", PayloadRef: command.RequestEvent.Ref, PayloadHash: command.RequestEvent.Hash}}
	if !settled {
		requestCommands = append(requestCommands, eventpostgres.OutboxCommand{ID: identifiers.reconcileOutbox, CommandID: identifiers.reconcileCommand, CommandType: "ReconcileRunCancellation", PayloadRef: command.ReconcileCommand.Ref, PayloadHash: command.ReconcileCommand.Hash})
		if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,'background','run-cancellation',90,1,100,'pending',$4,$5,$4,$6,$6)`, identifiers.reconcileJob, command.TenantID, identifiers.reconcileCommand, reconcileAt, reconcileAt.Add(24*time.Hour), now); err != nil {
			return RunCancellationResult{}, err
		}
	}
	if needsPropagation {
		requestCommands = append(requestCommands, eventpostgres.OutboxCommand{ID: propagation.outbox, CommandID: propagation.command, CommandType: "PropagateRunCancellation", PayloadRef: command.PropagateCommand.Ref, PayloadHash: command.PropagateCommand.Hash})
		if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,'background','run-cancellation-propagation',95,1,100,'pending',$4,$5,$4,$4,$4)`, propagation.job, command.TenantID, propagation.command, now, now.Add(24*time.Hour)); err != nil {
			return RunCancellationResult{}, err
		}
	}
	request := eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.requestEvent, TenantID: command.TenantID, UserID: command.UserID, EventType: "RunCancellationRequested", SchemaVersion: 1, AggregateKind: "run_cancellation", AggregateID: command.CancellationID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.RequestEvent.Ref, PayloadHash: command.RequestEvent.Hash}, Commands: requestCommands}
	if _, err = store.Appender.Append(ctx, tx, request); err != nil {
		return RunCancellationResult{}, err
	}

	nextFence := currentFence
	if runStatus == string(statemachine.RunExecuting) {
		nextFence++
	}
	nextRunVersion := runVersion
	if settled {
		nextRunVersion++
		attemptEvent, settleErr := store.settleRunExecutionRight(ctx, tx, cancellationAttemptSettlement{TenantID: command.TenantID, UserID: command.UserID, StoreEpoch: store.StoreEpoch, CorrelationID: command.CorrelationID, Actor: command.Actor, Payload: command.AttemptCancelledEvent}, activeCommand, activeAttempt, identifiers.requestEvent, now)
		if settleErr != nil {
			return RunCancellationResult{}, settleErr
		}
		if attemptEvent != nil {
			if _, err = store.Appender.Append(ctx, tx, *attemptEvent); err != nil {
				return RunCancellationResult{}, err
			}
		}
		if pendingCommand.Valid {
			if _, err = tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='cancelled',updated_at=$1 WHERE tenant_id=$2 AND command_id=$3 AND status='pending'`, now, command.TenantID, pendingCommand.String); err != nil {
				return RunCancellationResult{}, err
			}
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET status='cancelled',run_version=$1,pending_command_id=NULL,active_command_id=NULL,active_attempt_id=NULL,current_fence=$2,lease_token_hash=NULL,lease_expires_at=NULL,cancel_requested_at=$3,cancel_generation=$4,active_cancellation_id=$5,updated_at=$3 WHERE id=$6 AND tenant_id=$7 AND run_version=$8 AND cancel_requested_at IS NULL`, nextRunVersion, nextFence, now, nextGeneration, command.CancellationID, command.RunID, command.TenantID, runVersion); updateErr != nil || tag.RowsAffected() != 1 {
			return RunCancellationResult{}, ErrRunConflict
		}
		causationID := identifiers.requestEvent
		terminal := eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.settlementEvent, TenantID: command.TenantID, UserID: command.UserID, EventType: "RunCancelled", SchemaVersion: 1, AggregateKind: "run", AggregateID: command.RunID, AggregateVersion: nextRunVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.SettlementEvent.Ref, PayloadHash: command.SettlementEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifiers.settlementOutbox, CommandID: identifiers.settlementPublish, CommandType: "events.publish", PayloadRef: command.SettlementEvent.Ref, PayloadHash: command.SettlementEvent.Hash}}}
		if _, err = store.Appender.Append(ctx, tx, terminal); err != nil {
			return RunCancellationResult{}, err
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.run_cancellations SET version=version+1,status='settled',settled_at=$1,settlement_event_id=$2,updated_at=$1 WHERE id=$3 AND tenant_id=$4 AND version=1 AND status='requested'`, now, identifiers.settlementEvent, command.CancellationID, command.TenantID); updateErr != nil || tag.RowsAffected() != 1 {
			return RunCancellationResult{}, ErrRunConflict
		}
	} else {
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.run_cancellations SET version=version+1,status='terminating',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND version=1 AND status='requested'`, now, command.CancellationID, command.TenantID); updateErr != nil || tag.RowsAffected() != 1 {
			return RunCancellationResult{}, ErrRunConflict
		}
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET current_fence=$1,cancel_requested_at=$2,cancel_generation=$3,active_cancellation_id=$4,updated_at=$2 WHERE id=$5 AND tenant_id=$6 AND run_version=$7 AND cancel_requested_at IS NULL`, nextFence, now, nextGeneration, command.CancellationID, command.RunID, command.TenantID, runVersion); updateErr != nil || tag.RowsAffected() != 1 {
			return RunCancellationResult{}, ErrRunConflict
		}
	}
	result := RunCancellationResult{CancellationID: command.CancellationID, CancelGeneration: nextGeneration, CancellationStatus: cancellationStatus, RunID: command.RunID, RunVersion: nextRunVersion, RunStatus: state, RequestEventID: identifiers.requestEvent, UpdatedAt: now, Settled: settled}
	if settled {
		result.RunStatus = statemachine.RunCancelled
		result.SettlementEventID = identifiers.settlementEvent
	} else {
		result.ReconcileCommandID = identifiers.reconcileCommand
	}
	return result, nil
}

func countCancellationBlockers(ctx context.Context, tx pgx.Tx, tenantID, runID string) (int, error) {
	var tools, llm, runtimes, children int
	err := tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agent.tool_calls WHERE tenant_id=$1 AND run_id=$2 AND status NOT IN ('succeeded','failed','cancelled','resolved_unknown')),
		(SELECT count(*) FROM agent.llm_attempts WHERE tenant_id=$1 AND run_id=$2 AND status='running'),
		(SELECT count(*) FROM agent.runtime_sessions WHERE tenant_id=$1 AND run_id=$2 AND status NOT IN ('terminated','failed')),
		(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND parent_run_id=$2 AND status NOT IN ('succeeded','failed','cancelled','expired'))`, tenantID, runID).Scan(&tools, &llm, &runtimes, &children)
	return tools + llm + runtimes + children, err
}

func (store RunStore) settleRunExecutionRight(ctx context.Context, tx pgx.Tx, settlement cancellationAttemptSettlement, activeCommand, activeAttempt sql.NullString, causationID string, now time.Time) (*eventpostgres.Input, error) {
	if !activeCommand.Valid && !activeAttempt.Valid {
		return nil, nil
	}
	if activeCommand.Valid != activeAttempt.Valid {
		return nil, ErrRunConflict
	}
	var attemptVersion uint64
	var jobID, inboxID string
	if err := tx.QueryRow(ctx, `SELECT a.version,j.id::text,i.id::text FROM agent.job_attempts a JOIN agent.jobs j ON j.tenant_id=a.tenant_id AND j.id=a.job_id AND j.command_id=a.command_id JOIN agent.inbox i ON i.tenant_id=a.tenant_id AND i.command_id=a.command_id AND i.owner_attempt_id=a.id WHERE a.tenant_id=$1 AND a.id=$2 AND a.command_id=$3 AND a.status='running' AND j.status='running' AND i.status='running' FOR UPDATE OF a,j,i`, settlement.TenantID, activeAttempt.String, activeCommand.String).Scan(&attemptVersion, &jobID, &inboxID); err != nil {
		return nil, ErrRunConflict
	}
	if tag, err := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status='abandoned',finished_at=$2,result_hash='run_cancelled',updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND version=$5 AND status='running'`, attemptVersion+1, now, activeAttempt.String, settlement.TenantID, attemptVersion); err != nil || tag.RowsAffected() != 1 {
		return nil, ErrRunConflict
	}
	if tag, err := tx.Exec(ctx, `UPDATE agent.inbox SET status='completed',completed_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND status='running'`, now, inboxID, settlement.TenantID); err != nil || tag.RowsAffected() != 1 {
		return nil, ErrRunConflict
	}
	if tag, err := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='cancelled',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND status='running'`, now, jobID, settlement.TenantID); err != nil || tag.RowsAffected() != 1 {
		return nil, ErrRunConflict
	}
	eventIDs, err := store.completionEventIdentifiers(activeAttempt.String, statemachine.RunCancelled)
	if err != nil {
		return nil, err
	}
	attemptEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.attemptEvent, TenantID: settlement.TenantID, UserID: settlement.UserID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: activeAttempt.String, AggregateVersion: attemptVersion + 1, StoreEpoch: settlement.StoreEpoch, OccurredAt: now, Actor: settlement.Actor, CausationID: &causationID, CorrelationID: settlement.CorrelationID, PayloadRef: settlement.Payload.Ref, PayloadHash: settlement.Payload.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.attemptOutbox, CommandID: eventIDs.attemptPublish, CommandType: "events.publish", PayloadRef: settlement.Payload.Ref, PayloadHash: settlement.Payload.Hash}}}
	return &attemptEvent, nil
}

func loadCancellationReplay(ctx context.Context, tx pgx.Tx, command RequestRunCancellationCommand, runStatus string, runVersion uint64, activeID string) (RunCancellationResult, bool, error) {
	if activeID != command.CancellationID {
		return RunCancellationResult{}, false, nil
	}
	var status, requestHash, requestEventID string
	var settlementEventID sql.NullString
	var generation uint64
	var updatedAt time.Time
	err := tx.QueryRow(ctx, `SELECT status,request_hash,request_event_id::text,settlement_event_id::text,cancel_generation,updated_at FROM agent.run_cancellations WHERE id=$1 AND tenant_id=$2 AND run_id=$3 FOR UPDATE`, activeID, command.TenantID, command.RunID).Scan(&status, &requestHash, &requestEventID, &settlementEventID, &generation, &updatedAt)
	if err != nil {
		return RunCancellationResult{}, false, err
	}
	if requestHash != command.RequestHash {
		return RunCancellationResult{}, false, nil
	}
	result := RunCancellationResult{CancellationID: activeID, CancelGeneration: generation, CancellationStatus: status, RunID: command.RunID, RunVersion: runVersion, RunStatus: statemachine.RunState(runStatus), RequestEventID: requestEventID, UpdatedAt: updatedAt, Settled: status == "settled", Replayed: true}
	if settlementEventID.Valid {
		result.SettlementEventID = settlementEventID.String
	}
	return result, true, nil
}

func (store RunStore) cancellationIdentifiers(cancellationID string) (cancellationIDs, error) {
	domains := []string{"run-cancellation-requested-event", "run-cancellation-requested-publish-outbox", "run-cancellation-requested-publish-command", "run-cancelled-event", "run-cancelled-publish-outbox", "run-cancelled-publish-command", "run-cancellation-reconcile-outbox", "run-cancellation-reconcile-command", "run-cancellation-reconcile-job"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(store.IDKey, domain, cancellationID)
		if err != nil {
			return cancellationIDs{}, err
		}
		values[index] = value
	}
	return cancellationIDs{values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], values[8]}, nil
}

func (store RunStore) cancellationPropagationIdentifiers(cancellationID string) (cancellationPropagationIDs, error) {
	values := make([]string, 3)
	for index, domain := range []string{"run-cancellation-propagate-outbox", "run-cancellation-propagate-command", "run-cancellation-propagate-job"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain, cancellationID)
		if err != nil {
			return cancellationPropagationIDs{}, err
		}
		values[index] = value
	}
	return cancellationPropagationIDs{values[0], values[1], values[2]}, nil
}

func propagationValue(enabled bool, value string) string {
	if enabled {
		return value
	}
	return ""
}

func validRequestRunCancellation(command RequestRunCancellationCommand) bool {
	return command.CancellationID != "" && command.TenantID != "" && command.UserID != "" && command.RunID != "" && command.ExpectedRunVersion > 0 && len(command.Reason) > 0 && len(command.Reason) <= 1000 && validSHA256Hex(command.RequestHash) && command.CorrelationID != "" && validJSONObject(command.Actor) && validSHA256Pointer(command.RequestEvent) && validSHA256Pointer(command.SettlementEvent) && validSHA256Pointer(command.AttemptCancelledEvent) && validSHA256Pointer(command.ReconcileCommand) && validSHA256Pointer(command.PropagateCommand)
}

func validSHA256Pointer(pointer PayloadPointer) bool {
	return pointer.Ref != "" && validSHA256Hex(pointer.Hash)
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}
