package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

type CompleteWorkspacePreparationCommand struct {
	Claim               PreviewClaim
	ExpectedToolVersion uint64
	RevisionID          string
	WorkspaceID         string
	BaseRevision        string
	PreparedRevision    string
	PreparedHash        string
	ProposalHash        string
	ApprovalID          string
	ApprovalExpiresAt   time.Time
	ResultHash          string
	Actor               json.RawMessage
	CorrelationID       string
	PreparedEvent       PayloadPointer
	ToolProposedEvent   PayloadPointer
	ApprovalRequested   PayloadPointer
	RunWaitingApproval  PayloadPointer
	AttemptCompleted    PayloadPointer
	NotifyApproval      PayloadPointer
	NotifyQueueClass    string
	NotifyResourceClass string
	NotifyPriority      int
	NotifyCostUnits     int64
	NotifyMaxAttempts   int
}

type CompletedWorkspacePreparation struct {
	RevisionID, ApprovalID, PermissionSnapshot, NotificationCommandID string
	RevisionVersion, ApprovalVersion, ToolVersion, RunVersion         uint64
	RunStatus                                                         statemachine.RunState
	CompletedAt                                                       time.Time
	Replayed                                                          bool
}

type workspacePreparationCompletionIDs struct {
	toolEvent, toolOutbox, toolPublish     string
	runEvent, runOutbox, runPublish        string
	notifyOutbox, notifyCommand, notifyJob string
}

// CompleteWorkspacePreparation is the only production boundary from an
// isolated workspace preview into human approval. The prepared revision,
// immutable proposal, ToolCall, Approval, Run state, worker completion and
// notification command commit together under the exact preview lease.
func (store RunStore) CompleteWorkspacePreparation(ctx context.Context, command CompleteWorkspacePreparationCommand) (CompletedWorkspacePreparation, error) {
	claim := command.Claim
	if !store.validClaim() || !validPreviewClaim(claim) {
		return CompletedWorkspacePreparation{}, ErrConfiguration
	}
	if !validCompleteWorkspacePreparation(command) {
		return CompletedWorkspacePreparation{}, ErrInvalidCommand
	}
	if err := statemachine.ToolCalls.ValidateTransition(statemachine.ToolCallPreparingApproval, statemachine.ToolCallAwaitingApproval); err != nil {
		return CompletedWorkspacePreparation{}, ErrInvalidCommand
	}
	if err := statemachine.Runs.ValidateTransition(statemachine.RunWaitingTool, statemachine.RunWaitingApproval); err != nil {
		return CompletedWorkspacePreparation{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, claim.StoreEpoch); err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return CompletedWorkspacePreparation{}, ErrExecutionRightConflict
	}
	preparedIDs, err := store.workspacePreparedIDs(command.RevisionID)
	if err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	approvalIDs, err := store.approvalEventIDs("requested", command.ApprovalID)
	if err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	completionIDs, err := store.workspacePreparationCompletionIdentifiers(command.RevisionID, claim.GroupID)
	if err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	attemptIDs, err := store.completionEventIdentifiers(claim.AttemptID, statemachine.RunWaitingApproval)
	if err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	now := store.claimNow()
	command.ApprovalExpiresAt = command.ApprovalExpiresAt.UTC().Truncate(time.Microsecond)
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	if replay, found, replayErr := store.loadWorkspacePreparationReplay(ctx, tx, command, preparedIDs.event, approvalIDs.event, attemptIDs.attemptEvent, completionIDs); replayErr != nil {
		return CompletedWorkspacePreparation{}, replayErr
	} else if found {
		if err = tx.Commit(ctx); err != nil {
			return CompletedWorkspacePreparation{}, err
		}
		return replay, nil
	}
	if !command.ApprovalExpiresAt.After(now) || command.ApprovalExpiresAt.After(now.Add(24*time.Hour)) {
		return CompletedWorkspacePreparation{}, ErrInvalidCommand
	}
	if err = lockPreviewInbox(ctx, tx, claim, digest[:], now); err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	var userID, runID, effectKey, toolStatus string
	var toolVersion uint64
	err = tx.QueryRow(ctx, `SELECT user_id::text,run_id::text,effect_key,status,tool_call_version FROM agent.tool_calls WHERE tenant_id=$1 AND id=$2 AND user_id=$3 AND run_id=$4 AND status='preparing_approval' AND tool_call_version=$5 AND active_command_id=$6 AND active_attempt_id=$7 AND current_fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10 AND lease_expires_at>$11 FOR UPDATE`, claim.TenantID, claim.ToolCallID, claim.UserID, claim.RunID, command.ExpectedToolVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&userID, &runID, &effectKey, &toolStatus, &toolVersion)
	if err != nil || toolStatus != "preparing_approval" || effectKey != claim.EffectKey {
		return CompletedWorkspacePreparation{}, ErrExecutionRightConflict
	}
	var groupKind, continuationKind string
	var requiredCount int
	var joined bool
	err = tx.QueryRow(ctx, `SELECT group_kind,continuation_kind,required_count,joined FROM agent.parallel_groups WHERE tenant_id=$1 AND id=$2 AND run_id=$3 FOR UPDATE`, claim.TenantID, claim.GroupID, claim.RunID).Scan(&groupKind, &continuationKind, &requiredCount, &joined)
	if err != nil || groupKind != "approval_preview" || continuationKind != "request_approval" || requiredCount != 1 || joined {
		return CompletedWorkspacePreparation{}, ErrWorkspaceNotActionable
	}
	var memberRequired bool
	if err = tx.QueryRow(ctx, `SELECT required FROM agent.parallel_group_members WHERE tenant_id=$1 AND group_id=$2 AND tool_call_id=$3 FOR UPDATE`, claim.TenantID, claim.GroupID, claim.ToolCallID).Scan(&memberRequired); err != nil || !memberRequired {
		return CompletedWorkspacePreparation{}, ErrWorkspaceNotActionable
	}
	var runStatus string
	var runVersion uint64
	var dueAt time.Time
	var cancelRequested *time.Time
	err = tx.QueryRow(ctx, `SELECT status,run_version,due_at,cancel_requested_at FROM agent.runs WHERE tenant_id=$1 AND id=$2 AND user_id=$3 FOR UPDATE`, claim.TenantID, claim.RunID, claim.UserID).Scan(&runStatus, &runVersion, &dueAt, &cancelRequested)
	if err != nil || runStatus != string(statemachine.RunWaitingTool) || cancelRequested != nil || !dueAt.After(now) {
		return CompletedWorkspacePreparation{}, ErrWorkspaceNotActionable
	}
	var effectStatus, effectClass, ledgerKey string
	err = tx.QueryRow(ctx, `SELECT status,effect_class,effect_key FROM agent.tool_effects WHERE tenant_id=$1 AND id=$2 AND tool_call_id=$3 AND run_id=$4 FOR UPDATE`, claim.TenantID, claim.EffectID, claim.ToolCallID, claim.RunID).Scan(&effectStatus, &effectClass, &ledgerKey)
	if err != nil || effectStatus != "prepared" || effectClass != claim.EffectClass || ledgerKey != claim.EffectKey {
		return CompletedWorkspacePreparation{}, ErrExecutionRightConflict
	}
	var attemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at>$8 FOR UPDATE`, claim.AttemptID, claim.TenantID, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&attemptVersion)
	if err != nil {
		return CompletedWorkspacePreparation{}, ErrExecutionRightConflict
	}
	permissionSnapshot, err := loadPermissionSnapshot(ctx, tx, claim.TenantID, userID)
	if err != nil {
		return CompletedWorkspacePreparation{}, ErrWorkspaceNotActionable
	}
	nextToolVersion := toolVersion + 1
	nextRunVersion := runVersion + 1
	if _, err = tx.Exec(ctx, `INSERT INTO agent.workspace_revision_commits(id,tenant_id,tool_call_id,workspace_id,base_revision,prepared_revision,prepared_hash,effect_key,proposal_hash,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'prepared',$10,$10)`, command.RevisionID, claim.TenantID, claim.ToolCallID, command.WorkspaceID, command.BaseRevision, command.PreparedRevision, command.PreparedHash, claim.EffectKey, command.ProposalHash, now); err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_calls SET status='awaiting_approval',tool_call_version=$1,active_command_id=NULL,active_attempt_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND status='preparing_approval' AND tool_call_version=$5 AND active_command_id=$6 AND active_attempt_id=$7 AND current_fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10 AND lease_expires_at>$2`, nextToolVersion, now, claim.TenantID, claim.ToolCallID, toolVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedWorkspacePreparation{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET status='waiting_approval',run_version=$1,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND status='waiting_tool' AND run_version=$5 AND cancel_requested_at IS NULL AND due_at>$2`, nextRunVersion, now, claim.TenantID, claim.RunID, runVersion); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedWorkspacePreparation{}, ErrExecutionRightConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.approvals(id,tenant_id,run_id,tool_call_id,approval_kind,proposal_hash,target_version,permission_snapshot,status,expires_at,requested_by,created_at,updated_at) VALUES($1,$2,$3,$4,'tool_execution',$5,$6,$7,'pending',$8,$9,$10,$10)`, command.ApprovalID, claim.TenantID, claim.RunID, claim.ToolCallID, command.ProposalHash, nextToolVersion, permissionSnapshot, command.ApprovalExpiresAt, userID, now); err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET status='completed',completed_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND store_epoch=$4 AND consumer_name=$5 AND command_id=$6 AND request_hash=$7 AND status='running' AND owner_attempt_id=$8 AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$1`, now, claim.InboxID, claim.TenantID, claim.StoreEpoch, claim.ConsumerName, claim.CommandID, claim.RequestHash, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedWorkspacePreparation{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status='succeeded',finished_at=$2,result_hash=$3,updated_at=$2 WHERE id=$4 AND tenant_id=$5 AND version=$6 AND job_id=$7 AND command_id=$8 AND status='running' AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$2`, attemptVersion+1, now, command.ResultHash, claim.AttemptID, claim.TenantID, attemptVersion, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedWorkspacePreparation{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='succeeded',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND command_id=$4 AND status='running'`, now, claim.JobID, claim.TenantID, claim.CommandID); updateErr != nil || tag.RowsAffected() != 1 {
		return CompletedWorkspacePreparation{}, ErrExecutionRightConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$9,$9,$9)`, completionIDs.notifyJob, claim.TenantID, completionIDs.notifyCommand, command.NotifyQueueClass, command.NotifyResourceClass, command.NotifyPriority, command.NotifyCostUnits, command.NotifyMaxAttempts, now, dueAt); err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	causationID := claim.CommandID
	preparedEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: preparedIDs.event, TenantID: claim.TenantID, UserID: userID, EventType: "WorkspaceRevisionPrepared", SchemaVersion: 1, AggregateKind: "workspace_revision", AggregateID: command.RevisionID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.PreparedEvent.Ref, PayloadHash: command.PreparedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: preparedIDs.outbox, CommandID: preparedIDs.publish, CommandType: "events.publish", PayloadRef: command.PreparedEvent.Ref, PayloadHash: command.PreparedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, preparedEvent); err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	preparedCausation := preparedIDs.event
	toolEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: completionIDs.toolEvent, TenantID: claim.TenantID, UserID: userID, EventType: "ToolCallProposed", SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: claim.ToolCallID, AggregateVersion: nextToolVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &preparedCausation, CorrelationID: command.CorrelationID, PayloadRef: command.ToolProposedEvent.Ref, PayloadHash: command.ToolProposedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: completionIDs.toolOutbox, CommandID: completionIDs.toolPublish, CommandType: "events.publish", PayloadRef: command.ToolProposedEvent.Ref, PayloadHash: command.ToolProposedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, toolEvent); err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	toolCausation := completionIDs.toolEvent
	approvalEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: approvalIDs.event, TenantID: claim.TenantID, UserID: userID, EventType: "ApprovalRequested", SchemaVersion: 1, AggregateKind: "approval", AggregateID: command.ApprovalID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &toolCausation, CorrelationID: command.CorrelationID, PayloadRef: command.ApprovalRequested.Ref, PayloadHash: command.ApprovalRequested.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: approvalIDs.outbox, CommandID: approvalIDs.publish, CommandType: "events.publish", PayloadRef: command.ApprovalRequested.Ref, PayloadHash: command.ApprovalRequested.Hash}, {ID: completionIDs.notifyOutbox, CommandID: completionIDs.notifyCommand, CommandType: "NotifyApproval", TargetAggregateKind: "approval", TargetAggregateID: command.ApprovalID, PayloadRef: command.NotifyApproval.Ref, PayloadHash: command.NotifyApproval.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, approvalEvent); err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	approvalCausation := approvalIDs.event
	runEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: completionIDs.runEvent, TenantID: claim.TenantID, UserID: userID, EventType: "RunWaitingApproval", SchemaVersion: 1, AggregateKind: "run", AggregateID: claim.RunID, AggregateVersion: nextRunVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &approvalCausation, CorrelationID: command.CorrelationID, PayloadRef: command.RunWaitingApproval.Ref, PayloadHash: command.RunWaitingApproval.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: completionIDs.runOutbox, CommandID: completionIDs.runPublish, CommandType: "events.publish", PayloadRef: command.RunWaitingApproval.Ref, PayloadHash: command.RunWaitingApproval.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, runEvent); err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	attemptCausation := completionIDs.toolEvent
	attemptEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: attemptIDs.attemptEvent, TenantID: claim.TenantID, UserID: userID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.AttemptID, AggregateVersion: attemptVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &attemptCausation, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptCompleted.Ref, PayloadHash: command.AttemptCompleted.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: attemptIDs.attemptOutbox, CommandID: attemptIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptCompleted.Ref, PayloadHash: command.AttemptCompleted.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, attemptEvent); err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CompletedWorkspacePreparation{}, err
	}
	return CompletedWorkspacePreparation{RevisionID: command.RevisionID, ApprovalID: command.ApprovalID, PermissionSnapshot: permissionSnapshot, NotificationCommandID: completionIDs.notifyCommand, RevisionVersion: 1, ApprovalVersion: 1, ToolVersion: nextToolVersion, RunVersion: nextRunVersion, RunStatus: statemachine.RunWaitingApproval, CompletedAt: now}, nil
}

func (store RunStore) workspacePreparationCompletionIdentifiers(revisionID, groupID string) (workspacePreparationCompletionIDs, error) {
	domains := []string{"workspace-preview-proposed:event", "workspace-preview-proposed:outbox", "workspace-preview-proposed:publish", "workspace-preview-run:event", "workspace-preview-run:outbox", "workspace-preview-run:publish", "workspace-preview-notify:outbox", "workspace-preview-notify:command", "workspace-preview-notify:job"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		seed := revisionID
		if index >= 3 {
			seed = groupID
		}
		value, err := ids.DeterministicUUID(store.IDKey, domain, seed)
		if err != nil {
			return workspacePreparationCompletionIDs{}, err
		}
		values[index] = value
	}
	return workspacePreparationCompletionIDs{values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], values[8]}, nil
}

func (store RunStore) loadWorkspacePreparationReplay(ctx context.Context, tx pgx.Tx, command CompleteWorkspacePreparationCommand, preparedEventID, approvalEventID, attemptEventID string, completion workspacePreparationCompletionIDs) (CompletedWorkspacePreparation, bool, error) {
	var result CompletedWorkspacePreparation
	var toolID, workspaceID, baseRevision, preparedRevision, preparedHash, effectKey, proposalHash string
	var currentRevisionVersion uint64
	err := tx.QueryRow(ctx, `SELECT id::text,version,created_at,tool_call_id::text,workspace_id::text,base_revision,prepared_revision,prepared_hash,effect_key,proposal_hash FROM agent.workspace_revision_commits WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.Claim.TenantID, command.RevisionID).Scan(&result.RevisionID, &currentRevisionVersion, &result.CompletedAt, &toolID, &workspaceID, &baseRevision, &preparedRevision, &preparedHash, &effectKey, &proposalHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return CompletedWorkspacePreparation{}, false, nil
	}
	if err != nil {
		return CompletedWorkspacePreparation{}, false, err
	}
	if toolID != command.Claim.ToolCallID || workspaceID != command.WorkspaceID || baseRevision != command.BaseRevision || preparedRevision != command.PreparedRevision || preparedHash != command.PreparedHash || effectKey != command.Claim.EffectKey || proposalHash != command.ProposalHash {
		return CompletedWorkspacePreparation{}, false, ErrWorkspaceConflict
	}
	var approvalStatus, approvalProposal, permission, requestedBy string
	var currentApprovalVersion uint64
	var targetVersion uint64
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT status,version,proposal_hash,permission_snapshot,target_version,requested_by::text,expires_at FROM agent.approvals WHERE tenant_id=$1 AND id=$2 AND run_id=$3 AND tool_call_id=$4`, command.Claim.TenantID, command.ApprovalID, command.Claim.RunID, command.Claim.ToolCallID).Scan(&approvalStatus, &currentApprovalVersion, &approvalProposal, &permission, &targetVersion, &requestedBy, &expiresAt)
	if err != nil || approvalStatus == "" || currentApprovalVersion < 1 || currentRevisionVersion < 1 || approvalProposal != command.ProposalHash || requestedBy != command.Claim.UserID || !expiresAt.Equal(command.ApprovalExpiresAt) {
		return CompletedWorkspacePreparation{}, false, ErrWorkspaceConflict
	}
	var toolStatus, runStatus, inboxStatus, attemptStatus, jobStatus, attemptResultHash string
	var currentToolVersion, currentRunVersion uint64
	err = tx.QueryRow(ctx, `SELECT t.status,t.tool_call_version,r.status,r.run_version,i.status,a.status,a.result_hash,j.status FROM agent.tool_calls t JOIN agent.runs r ON r.tenant_id=t.tenant_id AND r.id=t.run_id JOIN agent.inbox i ON i.tenant_id=t.tenant_id AND i.id=$3 JOIN agent.job_attempts a ON a.tenant_id=t.tenant_id AND a.id=$4 JOIN agent.jobs j ON j.tenant_id=t.tenant_id AND j.id=$5 WHERE t.tenant_id=$1 AND t.id=$2`, command.Claim.TenantID, command.Claim.ToolCallID, command.Claim.InboxID, command.Claim.AttemptID, command.Claim.JobID).Scan(&toolStatus, &currentToolVersion, &runStatus, &currentRunVersion, &inboxStatus, &attemptStatus, &attemptResultHash, &jobStatus)
	if err != nil || toolStatus == "" || runStatus == "" || currentToolVersion < targetVersion || currentRunVersion < 1 || inboxStatus != "completed" || attemptStatus != "succeeded" || attemptResultHash != command.ResultHash || jobStatus != "succeeded" || targetVersion != command.ExpectedToolVersion+1 {
		return CompletedWorkspacePreparation{}, false, ErrWorkspaceConflict
	}
	var notifyQueue, notifyResource, notifyStatus string
	var notifyPriority, notifyMaxAttempts int
	var notifyCostUnits int64
	err = tx.QueryRow(ctx, `SELECT queue_class,resource_class,priority,cost_units,max_attempts,status FROM agent.jobs WHERE tenant_id=$1 AND id=$2 AND command_id=$3`, command.Claim.TenantID, completion.notifyJob, completion.notifyCommand).Scan(&notifyQueue, &notifyResource, &notifyPriority, &notifyCostUnits, &notifyMaxAttempts, &notifyStatus)
	if err != nil || notifyQueue != command.NotifyQueueClass || notifyResource != command.NotifyResourceClass || notifyPriority != command.NotifyPriority || notifyCostUnits != command.NotifyCostUnits || notifyMaxAttempts != command.NotifyMaxAttempts || notifyStatus == "" {
		return CompletedWorkspacePreparation{}, false, ErrWorkspaceConflict
	}
	var preparedExists, approvalExists, toolExists, runExists, attemptExists, notifyExists bool
	var originalRunVersion uint64
	err = tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='WorkspaceRevisionPrepared' AND aggregate_id=$3 AND aggregate_version=1 AND store_epoch=$4 AND payload_hash=$14),
		EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$5 AND event_type='ApprovalRequested' AND aggregate_id=$6 AND aggregate_version=1 AND store_epoch=$4 AND payload_hash=$15),
		EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$7 AND event_type='ToolCallProposed' AND aggregate_id=$8 AND aggregate_version=$9 AND store_epoch=$4 AND payload_hash=$16),
		EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$10 AND event_type='RunWaitingApproval' AND aggregate_id=$11 AND store_epoch=$4 AND payload_hash=$17),
		COALESCE((SELECT aggregate_version FROM agent.events WHERE tenant_id=$1 AND id=$10),0),
		EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$18 AND event_type='JobAttemptCompleted' AND aggregate_id=$19 AND store_epoch=$4 AND payload_hash=$20),
		EXISTS(SELECT 1 FROM agent.outbox WHERE tenant_id=$1 AND command_id=$12 AND command_type='NotifyApproval' AND aggregate_kind='approval' AND aggregate_id=$6 AND payload_hash=$13)`, command.Claim.TenantID, preparedEventID, command.RevisionID, store.StoreEpoch, approvalEventID, command.ApprovalID, completion.toolEvent, command.Claim.ToolCallID, targetVersion, completion.runEvent, command.Claim.RunID, completion.notifyCommand, command.NotifyApproval.Hash, command.PreparedEvent.Hash, command.ApprovalRequested.Hash, command.ToolProposedEvent.Hash, command.RunWaitingApproval.Hash, attemptEventID, command.Claim.AttemptID, command.AttemptCompleted.Hash).Scan(&preparedExists, &approvalExists, &toolExists, &runExists, &originalRunVersion, &attemptExists, &notifyExists)
	if err != nil || !preparedExists || !approvalExists || !toolExists || !runExists || !attemptExists || !notifyExists || originalRunVersion == 0 {
		return CompletedWorkspacePreparation{}, false, ErrWorkspaceConflict
	}
	result.ApprovalID, result.PermissionSnapshot, result.NotificationCommandID = command.ApprovalID, permission, completion.notifyCommand
	result.RevisionVersion, result.ApprovalVersion, result.ToolVersion, result.RunVersion = 1, 1, targetVersion, originalRunVersion
	result.RunStatus, result.Replayed = statemachine.RunWaitingApproval, true
	return result, true, nil
}

func validCompleteWorkspacePreparation(command CompleteWorkspacePreparationCommand) bool {
	queue := command.NotifyQueueClass == "interactive" || command.NotifyQueueClass == "background"
	return command.ExpectedToolVersion == command.Claim.ToolCallVersion && command.RevisionID != "" && command.WorkspaceID != "" && command.BaseRevision != "" && command.PreparedRevision != "" && command.PreparedHash != "" && command.ProposalHash != "" && command.ApprovalID != "" && !command.ApprovalExpiresAt.IsZero() && command.ResultHash != "" && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.PreparedEvent) && validPointer(command.ToolProposedEvent) && validPointer(command.ApprovalRequested) && validPointer(command.RunWaitingApproval) && validPointer(command.AttemptCompleted) && validPointer(command.NotifyApproval) && queue && command.NotifyResourceClass != "" && command.NotifyPriority >= 0 && command.NotifyPriority <= 1000 && command.NotifyCostUnits > 0 && command.NotifyCostUnits <= 1_000_000_000_000 && command.NotifyMaxAttempts > 0 && command.NotifyMaxAttempts <= 100
}
