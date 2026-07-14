package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

type repairExecutionSnapshot struct {
	repairSnapshot
	UserID, RunID, GroupID, EffectID                        string
	RunVersion, GroupVersion                                uint64
	JoinPolicy                                              string
	RequiredCount, QuorumCount, TerminalCount, SuccessCount int
	GroupJoined                                             bool
}

func (service RepairControlService) executeApprovedRepair(ctx context.Context, tenantID, repairID, correlationID string) (RepairedTool, error) {
	snapshot, err := service.loadRepairExecutionSnapshot(ctx, tenantID, repairID)
	if err != nil {
		return RepairedTool{}, err
	}
	if snapshot.Status == "executed" {
		// Re-enter the Store replay path so event/projection integrity is still
		// checked instead of trusting the projection alone.
	} else if snapshot.Status != "approved" {
		return RepairedTool{}, ErrRepairNotActionable
	}
	manualIDs, terminalIDs, executedIDs, err := repairExecutionEventIDs(service.Store.IDKey, repairID)
	if err != nil {
		return RepairedTool{}, err
	}
	toolStatus, _, finalToolVersion, _, _, valid := repairOutcome(snapshot.Resolution, snapshot.TargetVersion, snapshot.EffectVersion, manualIDs.event, terminalIDs.event)
	if !valid {
		return RepairedTool{}, ErrInvalidCommand
	}
	manualPayload, err := service.putJSON(ctx, tenantID, manualIDs.event, repairEventClass, map[string]any{
		"subject_id": snapshot.TargetID, "subject_version": snapshot.TargetVersion + 1, "command_id": nil,
		"tool_call_id": snapshot.TargetID, "tool_call_version": snapshot.TargetVersion + 1, "effect_key": snapshot.EffectKey,
		"resolution": snapshot.Resolution, "repair_command_id": snapshot.ID, "residual_risk_ref": nullableString(snapshot.ResidualRiskRef),
	})
	if err != nil {
		return RepairedTool{}, err
	}
	terminalValue := map[string]any{"subject_id": snapshot.TargetID, "subject_version": finalToolVersion, "previous_state": "outcome_unknown", "new_state": toolStatus, "reason_code": "repair_confirmed_not_occurred", "command_id": nil}
	if snapshot.Resolution == "confirmed_occurred" {
		terminalValue = map[string]any{"subject_id": snapshot.TargetID, "subject_version": finalToolVersion, "command_id": nil, "tool_call_id": snapshot.TargetID, "run_id": snapshot.RunID, "tool_call_version": finalToolVersion, "result_hash": snapshot.EvidenceHash, "effect_status": "confirmed", "effect_key": snapshot.EffectKey}
	}
	terminalPayload, err := service.putJSON(ctx, tenantID, terminalIDs.event, repairEventClass, terminalValue)
	if err != nil {
		return RepairedTool{}, err
	}
	resultEventIDs := []string{manualIDs.event}
	if snapshot.Resolution != "accepted_unknown" {
		resultEventIDs = append(resultEventIDs, terminalIDs.event)
	}
	executedPayload, err := service.putJSON(ctx, tenantID, executedIDs.event, repairEventClass, map[string]any{
		"subject_id": snapshot.ID, "subject_version": snapshot.Version + 1, "command_id": nil, "proposal_hash": snapshot.ProposalHash,
		"repair_command_id": snapshot.ID, "target_kind": "tool_call", "target_id": snapshot.TargetID,
		"previous_target_version": snapshot.TargetVersion, "result_event_ids": resultEventIDs,
	})
	if err != nil {
		return RepairedTool{}, err
	}
	continuation, err := service.Store.continuationIdentifiers(snapshot.GroupID)
	if err != nil {
		return RepairedTool{}, err
	}
	predictedTerminal := snapshot.TerminalCount + 1
	predictedSuccess := snapshot.SuccessCount
	if snapshot.Resolution == "confirmed_occurred" {
		predictedSuccess++
	}
	groupPayload, err := service.putJSON(ctx, tenantID, continuation.groupEvent, repairEventClass, map[string]any{
		"subject_id": snapshot.GroupID, "subject_version": snapshot.GroupVersion + 1, "parallel_group_id": snapshot.GroupID,
		"run_id": snapshot.RunID, "join_policy": snapshot.JoinPolicy, "required_count": snapshot.RequiredCount,
		"terminal_count": predictedTerminal, "succeeded_count": predictedSuccess, "continuation_id": continuation.continuation,
	})
	if err != nil {
		return RepairedTool{}, err
	}
	runPayload, err := service.putJSON(ctx, tenantID, continuation.runEvent, repairEventClass, map[string]any{
		"subject_id": snapshot.RunID, "subject_version": snapshot.RunVersion + 1, "command_id": nil, "run_id": snapshot.RunID,
		"run_version": snapshot.RunVersion + 1, "continuation_id": continuation.continuation,
		"pending_command_id": continuation.command, "join_group_id": snapshot.GroupID,
	})
	if err != nil {
		return RepairedTool{}, err
	}
	resumePayload, err := service.putJSON(ctx, tenantID, continuation.command, "resume-agent-command", map[string]any{
		"command_id": continuation.command, "tenant_id": tenantID, "user_id": snapshot.UserID, "run_id": snapshot.RunID,
		"run_version": snapshot.RunVersion + 1, "continuation_id": continuation.continuation, "join_group_id": snapshot.GroupID,
	})
	if err != nil {
		return RepairedTool{}, err
	}
	actor, _ := json.Marshal(map[string]string{"kind": "service", "name": "repair-control-plane"})
	return service.Store.ExecuteToolEffectRepair(ctx, ExecuteToolEffectRepairCommand{
		RepairID: snapshot.ID, TenantID: tenantID, ProposalHash: snapshot.ProposalHash, EvidenceHash: snapshot.EvidenceHash,
		ExpectedRepairVersion: approvedRepairVersion(snapshot), ExpectedToolVersion: snapshot.TargetVersion, ExpectedEffectVersion: snapshot.EffectVersion,
		Actor: actor, CorrelationID: correlationID, ManuallyResolvedEvent: manualPayload, ToolCompletedEvent: terminalPayload,
		RepairExecutedEvent: executedPayload, GroupJoinedEvent: groupPayload, RunResumeQueuedEvent: runPayload, ResumeCommand: resumePayload,
		ResumeQueueClass: service.resumeQueueClass(), ResumeResourceClass: service.resumeResourceClass(), ResumePriority: service.resumePriority(),
		ResumeCostUnits: service.resumeCostUnits(), ResumeMaxAttempts: service.resumeMaxAttempts(),
	})
}

func (service RepairControlService) loadRepairExecutionSnapshot(ctx context.Context, tenantID, repairID string) (repairExecutionSnapshot, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return repairExecutionSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return repairExecutionSnapshot{}, err
	}
	var value repairExecutionSnapshot
	err = tx.QueryRow(ctx, `SELECT rc.id::text,rc.status,rc.version,rc.resolution,rc.proposal_hash,rc.evidence_hash,rc.target_id::text,rc.target_version,rc.effect_version,rc.effect_key,COALESCE(rc.residual_risk_ref,''),rc.initiator_user_id::text,rc.updated_at,rc.expires_at,t.user_id::text,t.run_id::text,m.group_id::text,e.id::text,r.run_version,g.version,g.join_policy,g.required_count,g.quorum_count,g.joined,(SELECT count(*) FROM agent.parallel_group_members pm JOIN agent.tool_calls pt ON pt.tenant_id=pm.tenant_id AND pt.id=pm.tool_call_id WHERE pm.tenant_id=g.tenant_id AND pm.group_id=g.id AND pm.required AND pt.status IN ('succeeded','failed','cancelled','resolved_unknown')),(SELECT count(*) FROM agent.parallel_group_members pm JOIN agent.tool_calls pt ON pt.tenant_id=pm.tenant_id AND pt.id=pm.tool_call_id WHERE pm.tenant_id=g.tenant_id AND pm.group_id=g.id AND pm.required AND pt.status='succeeded') FROM agent.repair_commands rc JOIN agent.tool_calls t ON t.tenant_id=rc.tenant_id AND t.id=rc.target_id JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id JOIN agent.parallel_group_members m ON m.tenant_id=t.tenant_id AND m.tool_call_id=t.id JOIN agent.parallel_groups g ON g.tenant_id=m.tenant_id AND g.id=m.group_id JOIN agent.runs r ON r.tenant_id=t.tenant_id AND r.id=t.run_id WHERE rc.tenant_id=$1 AND rc.id=$2`, tenantID, repairID).Scan(
		&value.ID, &value.Status, &value.Version, &value.Resolution, &value.ProposalHash, &value.EvidenceHash, &value.TargetID,
		&value.TargetVersion, &value.EffectVersion, &value.EffectKey, &value.ResidualRiskRef, &value.InitiatorID, &value.UpdatedAt,
		&value.ExpiresAt, &value.UserID, &value.RunID, &value.GroupID, &value.EffectID, &value.RunVersion, &value.GroupVersion,
		&value.JoinPolicy, &value.RequiredCount, &value.QuorumCount, &value.GroupJoined, &value.TerminalCount, &value.SuccessCount,
	)
	if err != nil {
		return repairExecutionSnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return repairExecutionSnapshot{}, err
	}
	return value, nil
}

func approvedRepairVersion(snapshot repairExecutionSnapshot) uint64 {
	if snapshot.Status == "executed" && snapshot.Version > 1 {
		return snapshot.Version - 1
	}
	return snapshot.Version
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
