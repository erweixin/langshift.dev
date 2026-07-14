package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
)

type ExecuteToolEffectRepairCommand struct {
	RepairID, TenantID, ProposalHash, EvidenceHash string
	ExpectedRepairVersion, ExpectedToolVersion     uint64
	ExpectedEffectVersion                          uint64
	ExternalResourceRef                            string
	Actor                                          json.RawMessage
	CorrelationID                                  string
	ManuallyResolvedEvent                          PayloadPointer
	ToolCompletedEvent                             PayloadPointer
	RepairExecutedEvent                            PayloadPointer
	GroupJoinedEvent                               PayloadPointer
	RunResumeQueuedEvent                           PayloadPointer
	ResumeCommand                                  PayloadPointer
	ResumeQueueClass, ResumeResourceClass          string
	ResumePriority                                 int
	ResumeCostUnits                                int64
	ResumeMaxAttempts                              int
}

type RepairedTool struct {
	RepairID, ToolCallID, EffectID, GroupID, RunID string
	Resolution, Status                             string
	RepairVersion, ToolCallVersion, EffectVersion  uint64
	RunVersion                                     uint64
	ContinuationID, ResumeCommandID                string
	Resumed, Replayed                              bool
}

// ExecuteToolEffectRepair commits the approved human resolution, the effect
// ledger convergence and the ordinary tool-group join as one transaction.
func (store RunStore) ExecuteToolEffectRepair(ctx context.Context, command ExecuteToolEffectRepairCommand) (RepairedTool, error) {
	if !store.valid() || !validExecuteToolEffectRepair(command) {
		return RepairedTool{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, store.StoreEpoch); err != nil {
		return RepairedTool{}, err
	}
	manualIDs, terminalIDs, executedIDs, err := repairExecutionEventIDs(store.IDKey, command.RepairID)
	if err != nil {
		return RepairedTool{}, err
	}
	now := store.claimNow()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RepairedTool{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return RepairedTool{}, err
	}

	var repairStatus, resolution, proposalHash, evidenceHash, targetID, effectKey, residualRiskRef string
	var repairVersion, targetVersion, effectVersion uint64
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT status,version,resolution,proposal_hash,evidence_hash,target_id::text,target_version,effect_version,effect_key,COALESCE(residual_risk_ref,''),expires_at FROM agent.repair_commands WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, command.RepairID, command.TenantID).Scan(&repairStatus, &repairVersion, &resolution, &proposalHash, &evidenceHash, &targetID, &targetVersion, &effectVersion, &effectKey, &residualRiskRef, &expiresAt)
	if err != nil || proposalHash != command.ProposalHash || evidenceHash != command.EvidenceHash || targetVersion != command.ExpectedToolVersion || effectVersion != command.ExpectedEffectVersion {
		return RepairedTool{}, ErrRepairNotActionable
	}
	targetStatus, ledgerStatus, finalToolVersion, finalEffectVersion, resultEventID, valid := repairOutcome(resolution, targetVersion, effectVersion, manualIDs.event, terminalIDs.event)
	if !valid || (resolution != "confirmed_occurred" && command.ExternalResourceRef != "") {
		return RepairedTool{}, ErrInvalidCommand
	}
	if repairStatus == "executed" {
		if repairVersion != command.ExpectedRepairVersion+1 {
			return RepairedTool{}, ErrRepairConflict
		}
		result, replayErr := store.replayExecutedToolEffectRepair(ctx, tx, command, targetID, effectKey, residualRiskRef, resolution, targetStatus, ledgerStatus, finalToolVersion, finalEffectVersion, resultEventID, executedIDs.event)
		if replayErr != nil {
			return RepairedTool{}, replayErr
		}
		if err = tx.Commit(ctx); err != nil {
			return RepairedTool{}, err
		}
		return result, nil
	}
	if repairStatus != "approved" || repairVersion != command.ExpectedRepairVersion || !expiresAt.After(now) {
		return RepairedTool{}, ErrRepairNotActionable
	}
	var approvals, rejections int
	if err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE decision='approve'),count(*) FILTER (WHERE decision='reject') FROM agent.repair_approvals WHERE tenant_id=$1 AND repair_command_id=$2`, command.TenantID, command.RepairID).Scan(&approvals, &rejections); err != nil {
		return RepairedTool{}, err
	}
	if approvals != 2 || rejections != 0 {
		return RepairedTool{}, ErrRepairAuthorization
	}

	var userID, runID, groupID, toolStatus, toolEffectKey, effectID, effectStatus string
	var lockedToolVersion, lockedEffectVersion uint64
	err = tx.QueryRow(ctx, `SELECT t.user_id::text,t.run_id::text,m.group_id::text,t.status,t.tool_call_version,t.effect_key,e.id::text,e.status,e.version FROM agent.tool_calls t JOIN agent.parallel_group_members m ON m.tenant_id=t.tenant_id AND m.tool_call_id=t.id JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id WHERE t.tenant_id=$1 AND t.id=$2 FOR UPDATE OF t,e`, command.TenantID, targetID).Scan(&userID, &runID, &groupID, &toolStatus, &lockedToolVersion, &toolEffectKey, &effectID, &effectStatus, &lockedEffectVersion)
	if err != nil || toolStatus != "outcome_unknown" || effectStatus != "outcome_unknown" || lockedToolVersion != targetVersion || lockedEffectVersion != effectVersion || toolEffectKey != effectKey {
		return RepairedTool{}, ErrRepairNotActionable
	}
	if _, err = tx.Exec(ctx, `UPDATE agent.jobs j SET version=j.version+1,status='cancelled',dispatch_lease_hash=NULL,dispatch_lease_expires_at=NULL,delivery_due_at=NULL,redelivery_requested_at=NULL,redelivery_reason=NULL,updated_at=$1 FROM agent.outbox o WHERE o.tenant_id=j.tenant_id AND o.command_id=j.command_id AND o.tenant_id=$2 AND o.aggregate_kind='tool_call' AND o.aggregate_id=$3 AND o.command_type='ReconcileToolEffect' AND j.status='pending'`, now, command.TenantID, targetID); err != nil {
		return RepairedTool{}, err
	}
	var activeReconciliation bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.jobs j JOIN agent.outbox o ON o.tenant_id=j.tenant_id AND o.command_id=j.command_id WHERE o.tenant_id=$1 AND o.aggregate_kind='tool_call' AND o.aggregate_id=$2 AND o.command_type='ReconcileToolEffect' AND j.status='running')`, command.TenantID, targetID).Scan(&activeReconciliation); err != nil {
		return RepairedTool{}, err
	}
	if activeReconciliation {
		return RepairedTool{}, ErrRepairNotActionable
	}

	confirmedAt := (*time.Time)(nil)
	if resolution == "confirmed_occurred" {
		confirmedAt = &now
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_effects SET version=$1,status=$2,external_resource_ref=COALESCE(NULLIF($3,''),external_resource_ref),reconciliation_due_at=NULL,confirmed_at=$4,result_event_id=$5,residual_risk_ref=NULLIF($6,''),updated_at=$7 WHERE id=$8 AND tenant_id=$9 AND version=$10 AND status='outcome_unknown' AND effect_key=$11`, finalEffectVersion, ledgerStatus, command.ExternalResourceRef, confirmedAt, resultEventID, residualRiskRef, now, effectID, command.TenantID, effectVersion, effectKey); updateErr != nil || tag.RowsAffected() != 1 {
		return RepairedTool{}, ErrRepairConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_calls SET status=$1,tool_call_version=$2,result_event_id=$3,updated_at=$4 WHERE id=$5 AND tenant_id=$6 AND status='outcome_unknown' AND tool_call_version=$7 AND effect_key=$8`, targetStatus, finalToolVersion, resultEventID, now, targetID, command.TenantID, targetVersion, effectKey); updateErr != nil || tag.RowsAffected() != 1 {
		return RepairedTool{}, ErrRepairConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.repair_commands SET version=version+1,status='executed',executed_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND version=$4 AND status='approved' AND expires_at>$1`, now, command.RepairID, command.TenantID, repairVersion); updateErr != nil || tag.RowsAffected() != 1 {
		return RepairedTool{}, ErrRepairConflict
	}

	join, err := store.joinCompletedTool(ctx, tx, toolJoinInput{TenantID: command.TenantID, UserID: userID, RunID: runID, GroupID: groupID, ToolEventID: resultEventID, Actor: command.Actor, CorrelationID: command.CorrelationID, GroupJoinedEvent: command.GroupJoinedEvent, RunResumeQueuedEvent: command.RunResumeQueuedEvent, ResumeCommand: command.ResumeCommand, ResumeQueueClass: command.ResumeQueueClass, ResumeResourceClass: command.ResumeResourceClass, ResumePriority: command.ResumePriority, ResumeCostUnits: command.ResumeCostUnits, ResumeMaxAttempts: command.ResumeMaxAttempts, Now: now})
	if err != nil {
		return RepairedTool{}, err
	}
	approvedEventID, _, _, err := repairApprovedEventIDs(store.IDKey, command.RepairID)
	if err != nil {
		return RepairedTool{}, err
	}
	manual := eventpostgres.Input{Event: eventpostgres.Event{ID: manualIDs.event, TenantID: command.TenantID, UserID: userID, EventType: "ToolCallManuallyResolved", SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: targetID, AggregateVersion: targetVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &approvedEventID, CorrelationID: command.CorrelationID, PayloadRef: command.ManuallyResolvedEvent.Ref, PayloadHash: command.ManuallyResolvedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: manualIDs.outbox, CommandID: manualIDs.publish, CommandType: "events.publish", PayloadRef: command.ManuallyResolvedEvent.Ref, PayloadHash: command.ManuallyResolvedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, manual); err != nil {
		return RepairedTool{}, err
	}
	if resolution != "accepted_unknown" {
		terminalType := "ToolCallSucceeded"
		if resolution == "confirmed_not_occurred" {
			terminalType = "ToolCallFailed"
		}
		causationID := manualIDs.event
		terminal := eventpostgres.Input{Event: eventpostgres.Event{ID: terminalIDs.event, TenantID: command.TenantID, UserID: userID, EventType: terminalType, SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: targetID, AggregateVersion: finalToolVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.ToolCompletedEvent.Ref, PayloadHash: command.ToolCompletedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: terminalIDs.outbox, CommandID: terminalIDs.publish, CommandType: "events.publish", PayloadRef: command.ToolCompletedEvent.Ref, PayloadHash: command.ToolCompletedEvent.Hash}}}
		if _, err = store.Appender.Append(ctx, tx, terminal); err != nil {
			return RepairedTool{}, err
		}
	}
	repairCausationID := resultEventID
	executed := eventpostgres.Input{Event: eventpostgres.Event{ID: executedIDs.event, TenantID: command.TenantID, UserID: userID, EventType: "RepairCommandExecuted", SchemaVersion: 1, AggregateKind: "repair_command", AggregateID: command.RepairID, AggregateVersion: repairVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &repairCausationID, CorrelationID: command.CorrelationID, PayloadRef: command.RepairExecutedEvent.Ref, PayloadHash: command.RepairExecutedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: executedIDs.outbox, CommandID: executedIDs.publish, CommandType: "events.publish", PayloadRef: command.RepairExecutedEvent.Ref, PayloadHash: command.RepairExecutedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, executed); err != nil {
		return RepairedTool{}, err
	}
	if join.GroupEvent != nil {
		if _, err = store.Appender.Append(ctx, tx, *join.GroupEvent); err != nil {
			return RepairedTool{}, err
		}
	}
	if join.RunEvent != nil {
		if _, err = store.Appender.Append(ctx, tx, *join.RunEvent); err != nil {
			return RepairedTool{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return RepairedTool{}, err
	}
	return RepairedTool{RepairID: command.RepairID, ToolCallID: targetID, EffectID: effectID, GroupID: groupID, RunID: runID, Resolution: resolution, Status: targetStatus, RepairVersion: repairVersion + 1, ToolCallVersion: finalToolVersion, EffectVersion: finalEffectVersion, RunVersion: join.RunVersion, ContinuationID: join.ContinuationID, ResumeCommandID: join.ResumeCommandID, Resumed: join.Resumed}, nil
}

func (store RunStore) replayExecutedToolEffectRepair(ctx context.Context, tx pgx.Tx, command ExecuteToolEffectRepairCommand, targetID, effectKey, residualRiskRef, resolution, targetStatus, ledgerStatus string, finalToolVersion, finalEffectVersion uint64, resultEventID, executedEventID string) (RepairedTool, error) {
	var userID, runID, groupID, toolStatus, toolResultEvent, effectID, effectStatus, effectResultEvent, durableRisk, durableEffectKey, externalResourceRef string
	var toolVersion, effectVersion, runVersion uint64
	err := tx.QueryRow(ctx, `SELECT t.user_id::text,t.run_id::text,m.group_id::text,t.status,t.tool_call_version,t.result_event_id::text,e.id::text,e.status,e.version,e.result_event_id::text,COALESCE(e.residual_risk_ref,''),e.effect_key,COALESCE(e.external_resource_ref,''),r.run_version FROM agent.tool_calls t JOIN agent.parallel_group_members m ON m.tenant_id=t.tenant_id AND m.tool_call_id=t.id JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id JOIN agent.runs r ON r.tenant_id=t.tenant_id AND r.id=t.run_id WHERE t.tenant_id=$1 AND t.id=$2`, command.TenantID, targetID).Scan(&userID, &runID, &groupID, &toolStatus, &toolVersion, &toolResultEvent, &effectID, &effectStatus, &effectVersion, &effectResultEvent, &durableRisk, &durableEffectKey, &externalResourceRef, &runVersion)
	if err != nil || toolStatus != targetStatus || toolVersion != finalToolVersion || toolResultEvent != resultEventID || effectStatus != ledgerStatus || effectVersion != finalEffectVersion || effectResultEvent != resultEventID || durableRisk != residualRiskRef || durableEffectKey != effectKey || (command.ExternalResourceRef != "" && externalResourceRef != command.ExternalResourceRef) {
		return RepairedTool{}, ErrRepairConflict
	}
	var executedExists, joinedByRepair bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE id=$1 AND tenant_id=$2 AND aggregate_kind='repair_command' AND aggregate_id=$3 AND aggregate_version=$4 AND event_type='RepairCommandExecuted'),EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$2 AND aggregate_kind='parallel_group' AND aggregate_id=$5 AND event_type='ToolGroupJoined' AND causation_id=$6)`, executedEventID, command.TenantID, command.RepairID, command.ExpectedRepairVersion+1, groupID, resultEventID).Scan(&executedExists, &joinedByRepair); err != nil || !executedExists {
		return RepairedTool{}, ErrRepairConflict
	}
	result := RepairedTool{RepairID: command.RepairID, ToolCallID: targetID, EffectID: effectID, GroupID: groupID, RunID: runID, Resolution: resolution, Status: targetStatus, RepairVersion: command.ExpectedRepairVersion + 1, ToolCallVersion: toolVersion, EffectVersion: effectVersion, RunVersion: runVersion, Replayed: true, Resumed: joinedByRepair}
	if joinedByRepair {
		if err = tx.QueryRow(ctx, `SELECT c.id::text,c.command_id::text FROM agent.parallel_groups g JOIN agent.continuations c ON c.tenant_id=g.tenant_id AND c.id=g.continuation_id WHERE g.tenant_id=$1 AND g.id=$2`, command.TenantID, groupID).Scan(&result.ContinuationID, &result.ResumeCommandID); err != nil {
			return RepairedTool{}, ErrRepairConflict
		}
	}
	return result, nil
}

type repairEventIDs struct{ event, outbox, publish string }

func repairExecutionEventIDs(key []byte, repairID string) (repairEventIDs, repairEventIDs, repairEventIDs, error) {
	manualEvent, manualOutbox, manualPublish, err := threeRepairIDs(key, repairID, "repair-manual-resolution-event", "repair-manual-resolution-outbox", "repair-manual-resolution-publish")
	if err != nil {
		return repairEventIDs{}, repairEventIDs{}, repairEventIDs{}, err
	}
	terminalEvent, terminalOutbox, terminalPublish, err := threeRepairIDs(key, repairID, "repair-terminal-tool-event", "repair-terminal-tool-outbox", "repair-terminal-tool-publish")
	if err != nil {
		return repairEventIDs{}, repairEventIDs{}, repairEventIDs{}, err
	}
	executedEvent, executedOutbox, executedPublish, err := threeRepairIDs(key, repairID, "repair-executed-event", "repair-executed-outbox", "repair-executed-publish")
	return repairEventIDs{manualEvent, manualOutbox, manualPublish}, repairEventIDs{terminalEvent, terminalOutbox, terminalPublish}, repairEventIDs{executedEvent, executedOutbox, executedPublish}, err
}

func repairOutcome(resolution string, toolVersion, effectVersion uint64, manualEventID, terminalEventID string) (string, string, uint64, uint64, string, bool) {
	switch resolution {
	case "confirmed_occurred":
		return string(statemachine.ToolCallSucceeded), "confirmed", toolVersion + 2, effectVersion + 1, terminalEventID, true
	case "confirmed_not_occurred":
		return string(statemachine.ToolCallFailed), "failed", toolVersion + 2, effectVersion + 1, terminalEventID, true
	case "accepted_unknown":
		return string(statemachine.ToolCallResolvedUnknown), "accepted_unknown", toolVersion + 1, effectVersion + 1, manualEventID, true
	default:
		return "", "", 0, 0, "", false
	}
}

func validExecuteToolEffectRepair(command ExecuteToolEffectRepairCommand) bool {
	return command.RepairID != "" && command.TenantID != "" && command.ProposalHash != "" && command.EvidenceHash != "" && command.ExpectedRepairVersion > 0 && command.ExpectedToolVersion > 0 && command.ExpectedEffectVersion > 0 && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.ManuallyResolvedEvent) && validPointer(command.ToolCompletedEvent) && validPointer(command.RepairExecutedEvent) && validPointer(command.GroupJoinedEvent) && validPointer(command.RunResumeQueuedEvent) && validPointer(command.ResumeCommand) && (command.ResumeQueueClass == "interactive" || command.ResumeQueueClass == "background") && command.ResumeResourceClass != "" && command.ResumePriority >= 0 && command.ResumePriority <= 1000 && command.ResumeCostUnits > 0 && command.ResumeCostUnits <= 1_000_000_000_000 && command.ResumeMaxAttempts > 0 && command.ResumeMaxAttempts <= 100
}
