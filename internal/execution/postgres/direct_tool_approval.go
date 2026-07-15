package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

var ErrDirectApprovalConflict = errors.New("direct tool approval conflicts with durable state")

type DirectApprovalToolRequest struct {
	ToolName, DescriptorSnapshotID, NormalizedInputRef, RequestHash string
	EffectClass, EffectKey, EffectScope, ProviderID                 string
	PolicySnapshot, ProposalHash                                    string
	ScopeSnapshot, ExecuteCommand                                   PayloadPointer
	QueueClass, ResourceClass                                       string
	Priority, MaxAttempts                                           int
	CostUnits                                                       int64
	ToolProposedEvent, ApprovalRequestedEvent, NotifyApproval       PayloadPointer
}

type ProposeDirectToolsCommand struct {
	Claim                   RunClaim
	ExpectedRunVersion      uint64
	StepID                  string
	ToolRequests            []DirectApprovalToolRequest
	PermissionSnapshot      string
	ApprovalExpiresAt       time.Time
	PlanResultHash          string
	Actor                   json.RawMessage
	CorrelationID           string
	AssistantMessage        *RunMessageInput
	RunWaitingApprovalEvent PayloadPointer
	AttemptCompletedEvent   PayloadPointer
	NotifyQueueClass        string
	NotifyResourceClass     string
	NotifyPriority          int
	NotifyCostUnits         int64
	NotifyMaxAttempts       int
}

type ProposedDirectTool struct {
	ProposalID, ToolCallID, ApprovalID, NotificationCommandID string
	ProposalHash                                              string
}

type DirectToolsProposed struct {
	RunID, GroupID string
	RunVersion     uint64
	Tools          []ProposedDirectTool
	CompletedAt    time.Time
}

type directApprovalToolIDs struct {
	proposal, toolCall, effect, approval           string
	toolEvent, toolOutbox, toolPublish             string
	approvalEvent, approvalOutbox, approvalPublish string
	notifyOutbox, notifyCommand, notifyJob         string
}

type directApprovalGroupIDs struct {
	group, runEvent, runOutbox, runPublish string
}

// ProposeDirectTools persists the exact no-preview proposal. No execute
// command is emitted; approval later dispatches only the encrypted command
// frozen in tool_proposals after revalidating every scope binding.
func (store RunStore) ProposeDirectTools(ctx context.Context, command ProposeDirectToolsCommand) (DirectToolsProposed, error) {
	claim := command.Claim
	if !store.validClaim() || !validRunClaim(claim) || !validProposeDirectTools(command) {
		return DirectToolsProposed{}, ErrConfiguration
	}
	if err := statemachine.Runs.ValidateTransition(statemachine.RunExecuting, statemachine.RunWaitingApproval); err != nil {
		return DirectToolsProposed{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, claim.StoreEpoch); err != nil {
		return DirectToolsProposed{}, err
	}
	groupIDs, toolIDs, err := store.directApprovalIdentifiers(claim.RunID, claim.RunVersion, command.StepID, command.ToolRequests)
	if err != nil {
		return DirectToolsProposed{}, ErrConfiguration
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return DirectToolsProposed{}, ErrExecutionRightConflict
	}
	now := store.claimNow()
	command.ApprovalExpiresAt = command.ApprovalExpiresAt.UTC().Truncate(time.Microsecond)
	if !command.ApprovalExpiresAt.After(now) || command.ApprovalExpiresAt.After(now.Add(24*time.Hour)) {
		return DirectToolsProposed{}, ErrInvalidCommand
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return DirectToolsProposed{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return DirectToolsProposed{}, err
	}
	if err = lockRunInbox(ctx, tx, claim, digest[:], now); err != nil {
		return DirectToolsProposed{}, err
	}
	var userID string
	var dueAt time.Time
	err = tx.QueryRow(ctx, `SELECT user_id::text,due_at FROM agent.runs WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND status='executing' AND run_version=$4 AND active_command_id=$5 AND active_attempt_id=$6 AND current_fence=$7 AND lease_token_hash=$8 AND lease_expires_at=$9 AND lease_expires_at>$10 AND cancel_requested_at IS NULL FOR UPDATE`, claim.RunID, claim.TenantID, claim.UserID, claim.RunVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&userID, &dueAt)
	if err != nil {
		return DirectToolsProposed{}, ErrExecutionRightConflict
	}
	permissionSnapshot, err := loadPermissionSnapshot(ctx, tx, claim.TenantID, userID)
	if err != nil || permissionSnapshot != command.PermissionSnapshot {
		return DirectToolsProposed{}, ErrApprovalNotActionable
	}
	for _, request := range command.ToolRequests {
		expected, hashErr := directToolProposalHash(request, permissionSnapshot)
		if hashErr != nil || expected != request.ProposalHash {
			return DirectToolsProposed{}, ErrInvalidCommand
		}
	}
	var attemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at>$8 FOR UPDATE`, claim.AttemptID, claim.TenantID, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&attemptVersion)
	if err != nil {
		return DirectToolsProposed{}, ErrExecutionRightConflict
	}
	nextRunVersion := claim.RunVersion + 1
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET status='waiting_approval',run_version=$1,active_command_id=NULL,active_attempt_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND status='executing' AND run_version=$5 AND active_command_id=$6 AND active_attempt_id=$7 AND current_fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10 AND lease_expires_at>$2 AND cancel_requested_at IS NULL`, nextRunVersion, now, claim.RunID, claim.TenantID, claim.RunVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return DirectToolsProposed{}, ErrExecutionRightConflict
	}
	if command.AssistantMessage != nil {
		if _, _, err = store.appendRunMessageInTx(ctx, tx, claim, *command.AssistantMessage, now, claim.CommandID, command.Actor, command.CorrelationID); err != nil {
			return DirectToolsProposed{}, err
		}
	}
	count := len(command.ToolRequests)
	if _, err = tx.Exec(ctx, `INSERT INTO agent.parallel_groups(id,tenant_id,run_id,join_policy,required_count,group_kind,step_id,quorum_count,continuation_kind,created_at,updated_at) VALUES($1,$2,$3,'all',$4,'approval_direct',$5,$4,'request_approval',$6,$6)`, groupIDs.group, claim.TenantID, claim.RunID, count, command.StepID, now); err != nil {
		return DirectToolsProposed{}, err
	}
	result := DirectToolsProposed{RunID: claim.RunID, GroupID: groupIDs.group, RunVersion: nextRunVersion, Tools: make([]ProposedDirectTool, 0, count), CompletedAt: now}
	causationID := claim.CommandID
	lastApprovalEvent := claim.CommandID
	for index, request := range command.ToolRequests {
		identifier := toolIDs[index]
		var effectKey any
		if request.EffectKey != "" {
			effectKey = request.EffectKey
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.tool_calls(id,tenant_id,user_id,run_id,status,tool_call_version,tool_name,descriptor_snapshot_id,normalized_input_ref,request_hash,effect_class,effect_key,approval_scope_hash,execution_mode,created_at,updated_at) VALUES($1,$2,$3,$4,'awaiting_approval',1,$5,$6,$7,$8,$9,$10,$11,'worker_runtime',$12,$12)`, identifier.toolCall, claim.TenantID, userID, claim.RunID, request.ToolName, request.DescriptorSnapshotID, request.NormalizedInputRef, request.RequestHash, request.EffectClass, effectKey, request.ProposalHash, now); err != nil {
			return DirectToolsProposed{}, err
		}
		if request.EffectClass != "read_only" {
			if _, err = tx.Exec(ctx, `INSERT INTO agent.tool_effects(id,tenant_id,effect_scope,provider_id,tool_name,effect_key,request_hash,status,tool_call_id,run_id,effect_class,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,'prepared',$8,$9,$10,$11,$11)`, identifier.effect, claim.TenantID, request.EffectScope, request.ProviderID, request.ToolName, request.EffectKey, request.RequestHash, identifier.toolCall, claim.RunID, request.EffectClass, now); err != nil {
				return DirectToolsProposed{}, err
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.parallel_group_members(tenant_id,group_id,tool_call_id,required,created_at) VALUES($1,$2,$3,true,$4)`, claim.TenantID, groupIDs.group, identifier.toolCall, now); err != nil {
			return DirectToolsProposed{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.approvals(id,tenant_id,run_id,tool_call_id,approval_kind,proposal_hash,target_version,permission_snapshot,status,expires_at,requested_by,created_at,updated_at) VALUES($1,$2,$3,$4,'tool_execution',$5,1,$6,'pending',$7,$8,$9,$9)`, identifier.approval, claim.TenantID, claim.RunID, identifier.toolCall, request.ProposalHash, permissionSnapshot, command.ApprovalExpiresAt, userID, now); err != nil {
			return DirectToolsProposed{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.tool_proposals(id,tenant_id,run_id,group_id,tool_call_id,approval_id,proposal_hash,policy_snapshot,scope_snapshot_ref,scope_snapshot_hash,execute_command_ref,execute_command_hash,queue_class,resource_class,priority,cost_units,max_attempts,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`, identifier.proposal, claim.TenantID, claim.RunID, groupIDs.group, identifier.toolCall, identifier.approval, request.ProposalHash, request.PolicySnapshot, request.ScopeSnapshot.Ref, request.ScopeSnapshot.Hash, request.ExecuteCommand.Ref, request.ExecuteCommand.Hash, request.QueueClass, request.ResourceClass, request.Priority, request.CostUnits, request.MaxAttempts, now); err != nil {
			return DirectToolsProposed{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$9,$9,$9)`, identifier.notifyJob, claim.TenantID, identifier.notifyCommand, command.NotifyQueueClass, command.NotifyResourceClass, command.NotifyPriority, command.NotifyCostUnits, command.NotifyMaxAttempts, now, dueAt); err != nil {
			return DirectToolsProposed{}, err
		}
		toolEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: identifier.toolEvent, TenantID: claim.TenantID, UserID: userID, EventType: "ToolCallProposed", SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: identifier.toolCall, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: request.ToolProposedEvent.Ref, PayloadHash: request.ToolProposedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifier.toolOutbox, CommandID: identifier.toolPublish, CommandType: "events.publish", PayloadRef: request.ToolProposedEvent.Ref, PayloadHash: request.ToolProposedEvent.Hash}}}
		if _, err = store.Appender.Append(ctx, tx, toolEvent); err != nil {
			return DirectToolsProposed{}, err
		}
		toolCausation := identifier.toolEvent
		approvalEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: identifier.approvalEvent, TenantID: claim.TenantID, UserID: userID, EventType: "ApprovalRequested", SchemaVersion: 1, AggregateKind: "approval", AggregateID: identifier.approval, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &toolCausation, CorrelationID: command.CorrelationID, PayloadRef: request.ApprovalRequestedEvent.Ref, PayloadHash: request.ApprovalRequestedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifier.approvalOutbox, CommandID: identifier.approvalPublish, CommandType: "events.publish", PayloadRef: request.ApprovalRequestedEvent.Ref, PayloadHash: request.ApprovalRequestedEvent.Hash}, {ID: identifier.notifyOutbox, CommandID: identifier.notifyCommand, CommandType: "NotifyApproval", TargetAggregateKind: "approval", TargetAggregateID: identifier.approval, PayloadRef: request.NotifyApproval.Ref, PayloadHash: request.NotifyApproval.Hash}}}
		if _, err = store.Appender.Append(ctx, tx, approvalEvent); err != nil {
			return DirectToolsProposed{}, err
		}
		lastApprovalEvent = identifier.approvalEvent
		result.Tools = append(result.Tools, ProposedDirectTool{ProposalID: identifier.proposal, ToolCallID: identifier.toolCall, ApprovalID: identifier.approval, NotificationCommandID: identifier.notifyCommand, ProposalHash: request.ProposalHash})
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET status='completed',completed_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND store_epoch=$4 AND consumer_name=$5 AND command_id=$6 AND request_hash=$7 AND status='running' AND owner_attempt_id=$8 AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$1`, now, claim.InboxID, claim.TenantID, claim.StoreEpoch, claim.ConsumerName, claim.CommandID, claim.RequestHash, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return DirectToolsProposed{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status='succeeded',finished_at=$2,result_hash=$3,updated_at=$2 WHERE id=$4 AND tenant_id=$5 AND version=$6 AND job_id=$7 AND command_id=$8 AND status='running' AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$2`, attemptVersion+1, now, command.PlanResultHash, claim.AttemptID, claim.TenantID, attemptVersion, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return DirectToolsProposed{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='succeeded',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND command_id=$4 AND status='running'`, now, claim.JobID, claim.TenantID, claim.CommandID); updateErr != nil || tag.RowsAffected() != 1 {
		return DirectToolsProposed{}, ErrExecutionRightConflict
	}
	runCausation := lastApprovalEvent
	runEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: groupIDs.runEvent, TenantID: claim.TenantID, UserID: userID, EventType: "RunWaitingApproval", SchemaVersion: 1, AggregateKind: "run", AggregateID: claim.RunID, AggregateVersion: nextRunVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &runCausation, CorrelationID: command.CorrelationID, PayloadRef: command.RunWaitingApprovalEvent.Ref, PayloadHash: command.RunWaitingApprovalEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: groupIDs.runOutbox, CommandID: groupIDs.runPublish, CommandType: "events.publish", PayloadRef: command.RunWaitingApprovalEvent.Ref, PayloadHash: command.RunWaitingApprovalEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, runEvent); err != nil {
		return DirectToolsProposed{}, err
	}
	attemptIDs, err := store.completionEventIdentifiers(claim.AttemptID, statemachine.RunWaitingApproval)
	if err != nil {
		return DirectToolsProposed{}, err
	}
	attemptCausation := groupIDs.runEvent
	attemptEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: attemptIDs.attemptEvent, TenantID: claim.TenantID, UserID: userID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.AttemptID, AggregateVersion: attemptVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &attemptCausation, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: attemptIDs.attemptOutbox, CommandID: attemptIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, attemptEvent); err != nil {
		return DirectToolsProposed{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return DirectToolsProposed{}, err
	}
	return result, nil
}

func validProposeDirectTools(command ProposeDirectToolsCommand) bool {
	if command.ExpectedRunVersion != command.Claim.RunVersion || command.StepID == "" || len(command.StepID) > 256 || len(command.ToolRequests) < 1 || len(command.ToolRequests) > maximumParallelToolCalls || command.PermissionSnapshot == "" || command.ApprovalExpiresAt.IsZero() || command.PlanResultHash == "" || !validJSONObject(command.Actor) || command.CorrelationID == "" || command.AssistantMessage != nil && !validRunMessageInput(*command.AssistantMessage) || !validPointer(command.RunWaitingApprovalEvent) || !validPointer(command.AttemptCompletedEvent) || command.NotifyQueueClass != "interactive" && command.NotifyQueueClass != "background" || command.NotifyResourceClass == "" || command.NotifyPriority < 0 || command.NotifyPriority > 1000 || command.NotifyCostUnits < 1 || command.NotifyCostUnits > 1_000_000_000_000 || command.NotifyMaxAttempts < 1 || command.NotifyMaxAttempts > 100 {
		return false
	}
	seen := map[string]bool{}
	for _, request := range command.ToolRequests {
		if request.ToolName == "" || request.DescriptorSnapshotID == "" || request.NormalizedInputRef == "" || request.RequestHash == "" || request.PolicySnapshot == "" || !validSHA256(request.ProposalHash) || !validEffect(request.EffectClass, request.EffectKey, request.EffectScope, request.ProviderID) || !validPointer(request.ScopeSnapshot) || !validSHA256(request.ScopeSnapshot.Hash) || !validPointer(request.ExecuteCommand) || !validSHA256(request.ExecuteCommand.Hash) || request.QueueClass != "interactive" && request.QueueClass != "background" || request.ResourceClass == "" || request.Priority < 0 || request.Priority > 1000 || request.CostUnits < 1 || request.CostUnits > 1_000_000_000_000 || request.MaxAttempts < 1 || request.MaxAttempts > 100 || !validPointer(request.ToolProposedEvent) || !validPointer(request.ApprovalRequestedEvent) || !validPointer(request.NotifyApproval) || seen[request.RequestHash] {
			return false
		}
		seen[request.RequestHash] = true
	}
	return true
}

func directToolProposalHash(request DirectApprovalToolRequest, permissionSnapshot string) (string, error) {
	canonical, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		ToolName      string `json:"tool_name"`
		Descriptor    string `json:"descriptor_snapshot_id"`
		RequestHash   string `json:"request_hash"`
		EffectClass   string `json:"effect_class"`
		EffectKey     string `json:"effect_key,omitempty"`
		EffectScope   string `json:"effect_scope,omitempty"`
		ProviderID    string `json:"provider_id,omitempty"`
		Policy        string `json:"policy_snapshot"`
		ScopeHash     string `json:"scope_snapshot_hash"`
		CommandHash   string `json:"execute_command_hash"`
		Permission    string `json:"permission_snapshot"`
		QueueClass    string `json:"queue_class"`
		ResourceClass string `json:"resource_class"`
		Priority      int    `json:"priority"`
		CostUnits     int64  `json:"cost_units"`
		MaxAttempts   int    `json:"max_attempts"`
	}{1, request.ToolName, request.DescriptorSnapshotID, request.RequestHash, request.EffectClass, request.EffectKey, request.EffectScope, request.ProviderID, request.PolicySnapshot, request.ScopeSnapshot.Hash, request.ExecuteCommand.Hash, permissionSnapshot, request.QueueClass, request.ResourceClass, request.Priority, request.CostUnits, request.MaxAttempts})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func (store RunStore) directApprovalIdentifiers(runID string, runVersion uint64, stepID string, requests []DirectApprovalToolRequest) (directApprovalGroupIDs, []directApprovalToolIDs, error) {
	scope := fmt.Sprintf("%s\x00%d\x00%s", runID, runVersion, stepID)
	derive := func(domain, seed string) (string, error) { return ids.DeterministicUUID(store.IDKey, domain, seed) }
	group, err := derive("direct-approval-group", scope)
	if err != nil {
		return directApprovalGroupIDs{}, nil, err
	}
	groupValues := make([]string, 3)
	for index, domain := range []string{"direct-approval-run:event", "direct-approval-run:outbox", "direct-approval-run:publish"} {
		groupValues[index], err = derive(domain, scope)
		if err != nil {
			return directApprovalGroupIDs{}, nil, err
		}
	}
	groupIDs := directApprovalGroupIDs{group, groupValues[0], groupValues[1], groupValues[2]}
	result := make([]directApprovalToolIDs, len(requests))
	domains := []string{"proposal", "tool", "effect", "approval", "tool:event", "tool:outbox", "tool:publish", "approval:event", "approval:outbox", "approval:publish", "notify:outbox", "notify:command", "notify:job"}
	for index, request := range requests {
		seed := fmt.Sprintf("%s\x00%d\x00%s", scope, index, request.RequestHash)
		values := make([]string, len(domains))
		for domainIndex, domain := range domains {
			values[domainIndex], err = derive("direct-approval:"+domain, seed)
			if err != nil {
				return directApprovalGroupIDs{}, nil, err
			}
		}
		result[index] = directApprovalToolIDs{values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], values[8], values[9], values[10], values[11], values[12]}
	}
	return groupIDs, result, nil
}
