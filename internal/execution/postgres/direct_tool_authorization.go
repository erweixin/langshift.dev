package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

var ErrDirectApprovalAuthorization = errors.New("direct tool approval authorization is invalid")

type AuthorizeDirectToolCommand struct {
	TenantID, ApprovalID, ApprovalEventID, ProposalHash, PermissionSnapshot string
	ExpectedApprovalVersion, ExpectedToolVersion                            uint64
	Actor                                                                   json.RawMessage
	CorrelationID                                                           string
	ToolRequestedEvent, GroupAuthorizedEvent, RunWaitingToolEvent           PayloadPointer
}

type AuthorizedDirectTool struct {
	ApprovalID, ToolCallID, CommandID, JobID string
	ToolVersion, RunVersion, GroupVersion    uint64
	GroupActivated                           bool
	UpdatedAt                                time.Time
	Replayed                                 bool
}

type directAuthorizationIDs struct {
	executeOutbox, executeCommand, job    string
	toolEvent, toolOutbox, toolPublish    string
	groupEvent, groupOutbox, groupPublish string
	runEvent, runOutbox, runPublish       string
}

// AuthorizeDirectTool dispatches only the execute command frozen in the
// append-only proposal. The API caller cannot replace its payload or runtime
// routing. Approval, grant event, target version, permission and the complete
// proposal hash are revalidated under the same serializable transaction.
func (store RunStore) AuthorizeDirectTool(ctx context.Context, command AuthorizeDirectToolCommand) (AuthorizedDirectTool, error) {
	if !store.valid() || !validAuthorizeDirectTool(command) {
		return AuthorizedDirectTool{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, store.StoreEpoch); err != nil {
		return AuthorizedDirectTool{}, err
	}
	identifier, err := store.directAuthorizationIdentifiers(command.ApprovalID)
	if err != nil {
		return AuthorizedDirectTool{}, ErrConfiguration
	}
	now := store.claimNow()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return AuthorizedDirectTool{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return AuthorizedDirectTool{}, err
	}

	var approvalStatus, approvalProposal, approvalPermission, runID, toolID, requestedBy string
	var approvalVersion, targetVersion uint64
	err = tx.QueryRow(ctx, `SELECT status,version,proposal_hash,permission_snapshot,target_version,run_id::text,tool_call_id::text,requested_by::text FROM agent.approvals WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.ApprovalID).Scan(&approvalStatus, &approvalVersion, &approvalProposal, &approvalPermission, &targetVersion, &runID, &toolID, &requestedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthorizedDirectTool{}, ErrDirectApprovalAuthorization
	}
	if err != nil {
		return AuthorizedDirectTool{}, err
	}
	if approvalStatus != "granted" || approvalVersion != command.ExpectedApprovalVersion || approvalProposal != command.ProposalHash || approvalPermission != command.PermissionSnapshot || targetVersion != command.ExpectedToolVersion {
		return AuthorizedDirectTool{}, ErrDirectApprovalAuthorization
	}
	var grantExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='ApprovalGranted' AND aggregate_kind='approval' AND aggregate_id=$3 AND aggregate_version=$4 AND store_epoch=$5)`, command.TenantID, command.ApprovalEventID, command.ApprovalID, approvalVersion, store.StoreEpoch).Scan(&grantExists); err != nil {
		return AuthorizedDirectTool{}, err
	}
	if !grantExists {
		return AuthorizedDirectTool{}, ErrDirectApprovalAuthorization
	}

	var proposalID, proposalHash, policySnapshot, scopeRef, scopeHash, executeRef, executeHash string
	var queueClass, resourceClass, toolStatus, toolName, descriptorID, inputRef, requestHash, effectClass string
	var effectKey sql.NullString
	var effectScope, providerID sql.NullString
	var priority, maxAttempts int
	var costUnits int64
	var toolVersion uint64
	var pendingCommand sql.NullString
	err = tx.QueryRow(ctx, `SELECT p.id::text,p.proposal_hash,p.policy_snapshot,p.scope_snapshot_ref,p.scope_snapshot_hash,p.execute_command_ref,p.execute_command_hash,p.queue_class,p.resource_class,p.priority,p.cost_units,p.max_attempts,t.status,t.tool_call_version,t.tool_name,t.descriptor_snapshot_id,t.normalized_input_ref,t.request_hash,t.effect_class,t.effect_key,t.pending_command_id,e.effect_scope,e.provider_id FROM agent.tool_proposals p JOIN agent.tool_calls t ON t.tenant_id=p.tenant_id AND t.id=p.tool_call_id LEFT JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id WHERE p.tenant_id=$1 AND p.approval_id=$2 AND p.run_id=$3 AND p.tool_call_id=$4 FOR UPDATE OF t`, command.TenantID, command.ApprovalID, runID, toolID).Scan(&proposalID, &proposalHash, &policySnapshot, &scopeRef, &scopeHash, &executeRef, &executeHash, &queueClass, &resourceClass, &priority, &costUnits, &maxAttempts, &toolStatus, &toolVersion, &toolName, &descriptorID, &inputRef, &requestHash, &effectClass, &effectKey, &pendingCommand, &effectScope, &providerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthorizedDirectTool{}, ErrDirectApprovalAuthorization
	}
	if err != nil {
		return AuthorizedDirectTool{}, err
	}
	currentPermission, err := loadPermissionSnapshot(ctx, tx, command.TenantID, requestedBy)
	if err != nil || currentPermission != command.PermissionSnapshot {
		return AuthorizedDirectTool{}, ErrDirectApprovalAuthorization
	}
	request := DirectApprovalToolRequest{
		ToolName: toolName, DescriptorSnapshotID: descriptorID, NormalizedInputRef: inputRef, RequestHash: requestHash,
		EffectClass: effectClass, EffectKey: effectKey.String, EffectScope: effectScope.String, ProviderID: providerID.String,
		PolicySnapshot: policySnapshot, ScopeSnapshot: PayloadPointer{Ref: scopeRef, Hash: scopeHash},
		ExecuteCommand: PayloadPointer{Ref: executeRef, Hash: executeHash}, QueueClass: queueClass, ResourceClass: resourceClass,
		Priority: priority, CostUnits: costUnits, MaxAttempts: maxAttempts,
	}
	expectedProposal, hashErr := directToolProposalHash(request, currentPermission)
	if hashErr != nil || proposalID == "" || proposalHash != command.ProposalHash || expectedProposal != proposalHash {
		return AuthorizedDirectTool{}, ErrDirectApprovalAuthorization
	}

	var groupID, groupKind, continuationKind string
	var groupVersion uint64
	var groupJoined bool
	err = tx.QueryRow(ctx, `SELECT g.id::text,g.version,g.group_kind,g.continuation_kind,g.joined FROM agent.parallel_group_members m JOIN agent.parallel_groups g ON g.tenant_id=m.tenant_id AND g.id=m.group_id WHERE m.tenant_id=$1 AND m.tool_call_id=$2 FOR UPDATE OF g`, command.TenantID, toolID).Scan(&groupID, &groupVersion, &groupKind, &continuationKind, &groupJoined)
	if err != nil || groupJoined {
		return AuthorizedDirectTool{}, ErrDirectApprovalAuthorization
	}
	var runStatus string
	var runVersion uint64
	var dueAt time.Time
	var cancelRequested *time.Time
	if err = tx.QueryRow(ctx, `SELECT status,run_version,due_at,cancel_requested_at FROM agent.runs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, runID).Scan(&runStatus, &runVersion, &dueAt, &cancelRequested); err != nil {
		return AuthorizedDirectTool{}, err
	}

	if toolStatus == string(statemachine.ToolCallRequested) && pendingCommand.Valid && pendingCommand.String == identifier.executeCommand {
		result, replayErr := store.loadDirectAuthorizationReplay(ctx, tx, command, identifier, toolID, runVersion, groupVersion, now)
		if replayErr != nil {
			return AuthorizedDirectTool{}, replayErr
		}
		if err = tx.Commit(ctx); err != nil {
			return AuthorizedDirectTool{}, err
		}
		return result, nil
	}
	if toolStatus != string(statemachine.ToolCallAwaitingApproval) || toolVersion != command.ExpectedToolVersion || pendingCommand.Valid || groupKind != "approval_direct" || continuationKind != "request_approval" || runStatus != string(statemachine.RunWaitingApproval) || cancelRequested != nil || !dueAt.After(now) {
		return AuthorizedDirectTool{}, ErrDirectApprovalAuthorization
	}
	if err = statemachine.ToolCalls.ValidateTransition(statemachine.ToolCallAwaitingApproval, statemachine.ToolCallRequested); err != nil {
		return AuthorizedDirectTool{}, ErrInvalidCommand
	}
	nextToolVersion := toolVersion + 1
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.tool_calls SET status='requested',tool_call_version=$1,pending_command_id=$2,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND status='awaiting_approval' AND tool_call_version=$6 AND pending_command_id IS NULL AND active_command_id IS NULL`, nextToolVersion, identifier.executeCommand, now, command.TenantID, toolID, toolVersion); updateErr != nil || tag.RowsAffected() != 1 {
		return AuthorizedDirectTool{}, ErrDirectApprovalConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$9,$9,$9)`, identifier.job, command.TenantID, identifier.executeCommand, queueClass, resourceClass, priority, costUnits, maxAttempts, now, dueAt); err != nil {
		return AuthorizedDirectTool{}, err
	}
	approvalCausation := command.ApprovalEventID
	toolEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: identifier.toolEvent, TenantID: command.TenantID, UserID: requestedBy, EventType: "ToolCallRequested", SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: toolID, AggregateVersion: nextToolVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &approvalCausation, CorrelationID: command.CorrelationID, PayloadRef: command.ToolRequestedEvent.Ref, PayloadHash: command.ToolRequestedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifier.executeOutbox, CommandID: identifier.executeCommand, CommandType: "ExecuteToolCall", TargetAggregateKind: "tool_call", TargetAggregateID: toolID, PayloadRef: executeRef, PayloadHash: executeHash}, {ID: identifier.toolOutbox, CommandID: identifier.toolPublish, CommandType: "events.publish", PayloadRef: command.ToolRequestedEvent.Ref, PayloadHash: command.ToolRequestedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, toolEvent); err != nil {
		return AuthorizedDirectTool{}, err
	}

	var requiredCount, requestedCount int
	if err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE m.required),count(*) FILTER (WHERE m.required AND t.status='requested') FROM agent.parallel_group_members m JOIN agent.tool_calls t ON t.tenant_id=m.tenant_id AND t.id=m.tool_call_id WHERE m.tenant_id=$1 AND m.group_id=$2`, command.TenantID, groupID).Scan(&requiredCount, &requestedCount); err != nil {
		return AuthorizedDirectTool{}, err
	}
	activated := requiredCount > 0 && requiredCount == requestedCount
	if activated {
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.parallel_groups SET version=version+1,group_kind='execution',continuation_kind='resume',updated_at=$1 WHERE tenant_id=$2 AND id=$3 AND version=$4 AND group_kind='approval_direct' AND continuation_kind='request_approval' AND joined=false`, now, command.TenantID, groupID, groupVersion); updateErr != nil || tag.RowsAffected() != 1 {
			return AuthorizedDirectTool{}, ErrDirectApprovalConflict
		}
		groupVersion++
		nextRunVersion := runVersion + 1
		if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET status='waiting_tool',run_version=$1,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND status='waiting_approval' AND run_version=$5 AND cancel_requested_at IS NULL AND due_at>$2`, nextRunVersion, now, command.TenantID, runID, runVersion); updateErr != nil || tag.RowsAffected() != 1 {
			return AuthorizedDirectTool{}, ErrDirectApprovalConflict
		}
		runVersion = nextRunVersion
		toolCausation := identifier.toolEvent
		groupEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: identifier.groupEvent, TenantID: command.TenantID, UserID: requestedBy, EventType: "DirectApprovalGroupAuthorized", SchemaVersion: 1, AggregateKind: "parallel_group", AggregateID: groupID, AggregateVersion: groupVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &toolCausation, CorrelationID: command.CorrelationID, PayloadRef: command.GroupAuthorizedEvent.Ref, PayloadHash: command.GroupAuthorizedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifier.groupOutbox, CommandID: identifier.groupPublish, CommandType: "events.publish", PayloadRef: command.GroupAuthorizedEvent.Ref, PayloadHash: command.GroupAuthorizedEvent.Hash}}}
		if _, err = store.Appender.Append(ctx, tx, groupEvent); err != nil {
			return AuthorizedDirectTool{}, err
		}
		groupCausation := identifier.groupEvent
		runEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: identifier.runEvent, TenantID: command.TenantID, UserID: requestedBy, EventType: "RunWaitingTool", SchemaVersion: 1, AggregateKind: "run", AggregateID: runID, AggregateVersion: runVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &groupCausation, CorrelationID: command.CorrelationID, PayloadRef: command.RunWaitingToolEvent.Ref, PayloadHash: command.RunWaitingToolEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifier.runOutbox, CommandID: identifier.runPublish, CommandType: "events.publish", PayloadRef: command.RunWaitingToolEvent.Ref, PayloadHash: command.RunWaitingToolEvent.Hash}}}
		if _, err = store.Appender.Append(ctx, tx, runEvent); err != nil {
			return AuthorizedDirectTool{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return AuthorizedDirectTool{}, err
	}
	return AuthorizedDirectTool{ApprovalID: command.ApprovalID, ToolCallID: toolID, CommandID: identifier.executeCommand, JobID: identifier.job, ToolVersion: nextToolVersion, RunVersion: runVersion, GroupVersion: groupVersion, GroupActivated: activated, UpdatedAt: now}, nil
}

func (store RunStore) loadDirectAuthorizationReplay(ctx context.Context, tx pgx.Tx, command AuthorizeDirectToolCommand, identifier directAuthorizationIDs, toolID string, runVersion, groupVersion uint64, now time.Time) (AuthorizedDirectTool, error) {
	var toolVersion uint64
	var eventExists, commandExists, jobExists bool
	err := tx.QueryRow(ctx, `SELECT t.tool_call_version,EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND aggregate_kind='tool_call' AND aggregate_id=$3 AND event_type='ToolCallRequested' AND aggregate_version=t.tool_call_version AND store_epoch=$4),EXISTS(SELECT 1 FROM agent.outbox WHERE tenant_id=$1 AND id=$5 AND command_id=$6 AND command_type='ExecuteToolCall' AND aggregate_kind='tool_call' AND aggregate_id=$3),EXISTS(SELECT 1 FROM agent.jobs WHERE tenant_id=$1 AND id=$7 AND command_id=$6) FROM agent.tool_calls t WHERE t.tenant_id=$1 AND t.id=$3`, command.TenantID, identifier.toolEvent, toolID, store.StoreEpoch, identifier.executeOutbox, identifier.executeCommand, identifier.job).Scan(&toolVersion, &eventExists, &commandExists, &jobExists)
	if err != nil || !eventExists || !commandExists || !jobExists || toolVersion != command.ExpectedToolVersion+1 {
		return AuthorizedDirectTool{}, ErrDirectApprovalConflict
	}
	return AuthorizedDirectTool{ApprovalID: command.ApprovalID, ToolCallID: toolID, CommandID: identifier.executeCommand, JobID: identifier.job, ToolVersion: toolVersion, RunVersion: runVersion, GroupVersion: groupVersion, GroupActivated: true, UpdatedAt: now, Replayed: true}, nil
}

func (store RunStore) directAuthorizationIdentifiers(approvalID string) (directAuthorizationIDs, error) {
	domains := []string{"execute-outbox", "execute-command", "job", "tool-event", "tool-outbox", "tool-publish", "group-event", "group-outbox", "group-publish", "run-event", "run-outbox", "run-publish"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(store.IDKey, "direct-approval-authorize:"+domain, approvalID)
		if err != nil {
			return directAuthorizationIDs{}, err
		}
		values[index] = value
	}
	return directAuthorizationIDs{values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], values[8], values[9], values[10], values[11]}, nil
}

func validAuthorizeDirectTool(command AuthorizeDirectToolCommand) bool {
	return command.TenantID != "" && command.ApprovalID != "" && command.ApprovalEventID != "" && validSHA256(command.ProposalHash) && command.PermissionSnapshot != "" && command.ExpectedApprovalVersion > 1 && command.ExpectedToolVersion > 0 && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.ToolRequestedEvent) && validPointer(command.GroupAuthorizedEvent) && validPointer(command.RunWaitingToolEvent)
}
