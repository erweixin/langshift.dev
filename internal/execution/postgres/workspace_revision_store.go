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

var (
	ErrWorkspaceConflict      = errors.New("workspace revision conflicts with durable state")
	ErrWorkspaceNotActionable = errors.New("workspace revision is not actionable")
	ErrWorkspaceAuthorization = errors.New("workspace revision authorization is invalid")
)

type PrepareWorkspaceRevisionCommand struct {
	RevisionID, TenantID, ToolCallID, WorkspaceID string
	ExpectedToolVersion                           uint64
	BaseRevision, PreparedRevision, PreparedHash  string
	EffectKey, ProposalHash, CorrelationID        string
	Actor                                         json.RawMessage
	PreparedEvent                                 PayloadPointer
}

type PreparedWorkspaceRevision struct {
	RevisionID, Status, PreparedHash string
	Version, ToolVersion             uint64
	UpdatedAt                        time.Time
	Replayed                         bool
}

type AuthorizeWorkspaceRevisionCommand struct {
	RevisionID, TenantID, ApprovalID, ApprovalEventID   string
	ProposalHash, PermissionSnapshot                    string
	ExpectedRevisionVersion, ExpectedApprovalVersion    uint64
	ExpectedToolVersion                                 uint64
	QueueClass, ResourceClass                           string
	Priority                                            int
	CostUnits                                           int64
	MaxAttempts                                         int
	Actor                                               json.RawMessage
	CorrelationID                                       string
	AuthorizedEvent, ToolRequestedEvent, ExecuteCommand PayloadPointer
}

type AuthorizedWorkspaceRevision struct {
	RevisionID, Status, AuthorizationEventID string
	CommitCommandID, JobID                   string
	Version, ToolVersion                     uint64
	UpdatedAt                                time.Time
	Replayed                                 bool
}

func (store RunStore) PrepareWorkspaceRevision(ctx context.Context, command PrepareWorkspaceRevisionCommand) (PreparedWorkspaceRevision, error) {
	if !store.valid() || !validPrepareWorkspaceRevision(command) {
		return PreparedWorkspaceRevision{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, store.StoreEpoch); err != nil {
		return PreparedWorkspaceRevision{}, err
	}
	idsForEvent, err := store.workspacePreparedIDs(command.RevisionID)
	if err != nil {
		return PreparedWorkspaceRevision{}, err
	}
	now := store.claimNow()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return PreparedWorkspaceRevision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return PreparedWorkspaceRevision{}, err
	}
	if replay, found, replayErr := store.loadPreparedWorkspaceReplay(ctx, tx, command, idsForEvent.event); replayErr != nil {
		return PreparedWorkspaceRevision{}, replayErr
	} else if found {
		if err = tx.Commit(ctx); err != nil {
			return PreparedWorkspaceRevision{}, err
		}
		return replay, nil
	}
	var userID, status, effectKey string
	var toolVersion uint64
	err = tx.QueryRow(ctx, `SELECT user_id::text,status,tool_call_version,effect_key FROM agent.tool_calls WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.ToolCallID).Scan(&userID, &status, &toolVersion, &effectKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PreparedWorkspaceRevision{}, ErrWorkspaceNotActionable
		}
		return PreparedWorkspaceRevision{}, err
	}
	if status != "preparing_approval" || toolVersion != command.ExpectedToolVersion || effectKey != command.EffectKey {
		return PreparedWorkspaceRevision{}, ErrWorkspaceNotActionable
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.workspace_revision_commits(id,tenant_id,tool_call_id,workspace_id,base_revision,prepared_revision,prepared_hash,effect_key,proposal_hash,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'prepared',$10,$10)`, command.RevisionID, command.TenantID, command.ToolCallID, command.WorkspaceID, command.BaseRevision, command.PreparedRevision, command.PreparedHash, command.EffectKey, command.ProposalHash, now); err != nil {
		return PreparedWorkspaceRevision{}, err
	}
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: idsForEvent.event, TenantID: command.TenantID, UserID: userID, EventType: "WorkspaceRevisionPrepared", SchemaVersion: 1, AggregateKind: "workspace_revision", AggregateID: command.RevisionID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.PreparedEvent.Ref, PayloadHash: command.PreparedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: idsForEvent.outbox, CommandID: idsForEvent.publish, CommandType: "events.publish", PayloadRef: command.PreparedEvent.Ref, PayloadHash: command.PreparedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return PreparedWorkspaceRevision{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PreparedWorkspaceRevision{}, err
	}
	return PreparedWorkspaceRevision{RevisionID: command.RevisionID, Status: "prepared", PreparedHash: command.PreparedHash, Version: 1, ToolVersion: toolVersion, UpdatedAt: now}, nil
}

func (store RunStore) AuthorizeWorkspaceRevision(ctx context.Context, command AuthorizeWorkspaceRevisionCommand) (AuthorizedWorkspaceRevision, error) {
	if !store.valid() || !validAuthorizeWorkspaceRevision(command) {
		return AuthorizedWorkspaceRevision{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, store.StoreEpoch); err != nil {
		return AuthorizedWorkspaceRevision{}, err
	}
	identifiers, err := store.workspaceAuthorizationIDs(command.RevisionID)
	if err != nil {
		return AuthorizedWorkspaceRevision{}, err
	}
	now := store.claimNow()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return AuthorizedWorkspaceRevision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return AuthorizedWorkspaceRevision{}, err
	}
	if replay, found, replayErr := store.loadAuthorizedWorkspaceReplay(ctx, tx, command, identifiers); replayErr != nil {
		return AuthorizedWorkspaceRevision{}, replayErr
	} else if found {
		if err = tx.Commit(ctx); err != nil {
			return AuthorizedWorkspaceRevision{}, err
		}
		return replay, nil
	}
	var approvalStatus, approvalProposal, approvalPermission, approvalRunID, approvalToolID, requestedBy string
	var approvalVersion, approvalTargetVersion uint64
	err = tx.QueryRow(ctx, `SELECT status,version,proposal_hash,permission_snapshot,target_version,run_id::text,tool_call_id::text,requested_by::text FROM agent.approvals WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.ApprovalID).Scan(&approvalStatus, &approvalVersion, &approvalProposal, &approvalPermission, &approvalTargetVersion, &approvalRunID, &approvalToolID, &requestedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthorizedWorkspaceRevision{}, ErrWorkspaceAuthorization
	}
	if err != nil {
		return AuthorizedWorkspaceRevision{}, err
	}
	if approvalStatus != "granted" || approvalVersion != command.ExpectedApprovalVersion || approvalProposal != command.ProposalHash || approvalPermission != command.PermissionSnapshot || approvalTargetVersion != command.ExpectedToolVersion {
		return AuthorizedWorkspaceRevision{}, ErrWorkspaceAuthorization
	}
	var grantExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='ApprovalGranted' AND aggregate_kind='approval' AND aggregate_id=$3 AND aggregate_version=$4 AND store_epoch=$5)`, command.TenantID, command.ApprovalEventID, command.ApprovalID, approvalVersion, store.StoreEpoch).Scan(&grantExists); err != nil {
		return AuthorizedWorkspaceRevision{}, err
	}
	if !grantExists {
		return AuthorizedWorkspaceRevision{}, ErrWorkspaceAuthorization
	}
	var workspaceStatus, toolCallID, workspaceProposal, effectKey string
	var workspaceVersion uint64
	err = tx.QueryRow(ctx, `SELECT status,version,tool_call_id::text,proposal_hash,effect_key FROM agent.workspace_revision_commits WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.RevisionID).Scan(&workspaceStatus, &workspaceVersion, &toolCallID, &workspaceProposal, &effectKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AuthorizedWorkspaceRevision{}, ErrWorkspaceNotActionable
		}
		return AuthorizedWorkspaceRevision{}, err
	}
	if workspaceStatus != "prepared" || workspaceVersion != command.ExpectedRevisionVersion || toolCallID != approvalToolID || workspaceProposal != command.ProposalHash {
		return AuthorizedWorkspaceRevision{}, ErrWorkspaceNotActionable
	}
	var toolStatus, toolEffectKey, userID, runID string
	var toolVersion uint64
	err = tx.QueryRow(ctx, `SELECT status,tool_call_version,effect_key,user_id::text,run_id::text FROM agent.tool_calls WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, toolCallID).Scan(&toolStatus, &toolVersion, &toolEffectKey, &userID, &runID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AuthorizedWorkspaceRevision{}, ErrWorkspaceAuthorization
		}
		return AuthorizedWorkspaceRevision{}, err
	}
	if toolStatus != "awaiting_approval" || toolVersion != command.ExpectedToolVersion || toolEffectKey != effectKey || userID != requestedBy || runID != approvalRunID {
		return AuthorizedWorkspaceRevision{}, ErrWorkspaceAuthorization
	}
	currentPermission, err := loadPermissionSnapshot(ctx, tx, command.TenantID, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AuthorizedWorkspaceRevision{}, ErrWorkspaceAuthorization
		}
		return AuthorizedWorkspaceRevision{}, err
	}
	if currentPermission != command.PermissionSnapshot {
		return AuthorizedWorkspaceRevision{}, ErrWorkspaceAuthorization
	}
	if err = statemachine.ToolCalls.ValidateTransition(statemachine.ToolCallAwaitingApproval, statemachine.ToolCallRequested); err != nil {
		return AuthorizedWorkspaceRevision{}, ErrInvalidCommand
	}
	var dueAt time.Time
	var cancelRequested *time.Time
	if err = tx.QueryRow(ctx, `SELECT due_at,cancel_requested_at FROM agent.runs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, runID).Scan(&dueAt, &cancelRequested); err != nil {
		return AuthorizedWorkspaceRevision{}, err
	}
	if cancelRequested != nil || !dueAt.After(now) {
		return AuthorizedWorkspaceRevision{}, ErrWorkspaceNotActionable
	}
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_revision_commits SET version=version+1,status='authorized',approval_id=$1,approval_version=$2,approval_event_id=$3,authorization_event_id=$4,commit_command_id=$5,authorized_at=$6,updated_at=$6 WHERE tenant_id=$7 AND id=$8 AND version=$9 AND status='prepared'`, command.ApprovalID, approvalVersion, command.ApprovalEventID, identifiers.authorizationEvent, identifiers.executeCommand, now, command.TenantID, command.RevisionID, workspaceVersion)
	if err != nil {
		return AuthorizedWorkspaceRevision{}, err
	}
	if tag.RowsAffected() != 1 {
		return AuthorizedWorkspaceRevision{}, ErrWorkspaceConflict
	}
	nextToolVersion := toolVersion + 1
	tag, err = tx.Exec(ctx, `UPDATE agent.tool_calls SET status='requested',tool_call_version=$1,pending_command_id=$2,updated_at=$3 WHERE tenant_id=$4 AND id=$5 AND status='awaiting_approval' AND tool_call_version=$6 AND pending_command_id IS NULL AND active_command_id IS NULL`, nextToolVersion, identifiers.executeCommand, now, command.TenantID, toolCallID, toolVersion)
	if err != nil {
		return AuthorizedWorkspaceRevision{}, err
	}
	if tag.RowsAffected() != 1 {
		return AuthorizedWorkspaceRevision{}, ErrWorkspaceConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$9,$9,$9)`, identifiers.job, command.TenantID, identifiers.executeCommand, command.QueueClass, command.ResourceClass, command.Priority, command.CostUnits, command.MaxAttempts, now, dueAt); err != nil {
		return AuthorizedWorkspaceRevision{}, err
	}
	authorized := eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.authorizationEvent, TenantID: command.TenantID, UserID: userID, EventType: "WorkspaceRevisionCommitAuthorized", SchemaVersion: 1, AggregateKind: "workspace_revision", AggregateID: command.RevisionID, AggregateVersion: workspaceVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.AuthorizedEvent.Ref, PayloadHash: command.AuthorizedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifiers.executeOutbox, CommandID: identifiers.executeCommand, CommandType: "ExecuteToolCall", TargetAggregateKind: "tool_call", TargetAggregateID: toolCallID, PayloadRef: command.ExecuteCommand.Ref, PayloadHash: command.ExecuteCommand.Hash}, {ID: identifiers.authorizationPublishOutbox, CommandID: identifiers.authorizationPublish, CommandType: "events.publish", PayloadRef: command.AuthorizedEvent.Ref, PayloadHash: command.AuthorizedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, authorized); err != nil {
		return AuthorizedWorkspaceRevision{}, err
	}
	causationID := identifiers.authorizationEvent
	toolRequested := eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.toolEvent, TenantID: command.TenantID, UserID: userID, EventType: "ToolCallRequested", SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: toolCallID, AggregateVersion: nextToolVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.ToolRequestedEvent.Ref, PayloadHash: command.ToolRequestedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifiers.toolPublishOutbox, CommandID: identifiers.toolPublish, CommandType: "events.publish", PayloadRef: command.ToolRequestedEvent.Ref, PayloadHash: command.ToolRequestedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, toolRequested); err != nil {
		return AuthorizedWorkspaceRevision{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return AuthorizedWorkspaceRevision{}, err
	}
	return AuthorizedWorkspaceRevision{RevisionID: command.RevisionID, Status: "authorized", AuthorizationEventID: identifiers.authorizationEvent, CommitCommandID: identifiers.executeCommand, JobID: identifiers.job, Version: workspaceVersion + 1, ToolVersion: nextToolVersion, UpdatedAt: now}, nil
}

type workspacePreparedIDs struct{ event, outbox, publish string }

type workspaceAuthorizationIDs struct {
	authorizationEvent, authorizationPublishOutbox, authorizationPublish string
	executeOutbox, executeCommand, job                                   string
	toolEvent, toolPublishOutbox, toolPublish                            string
}

func (store RunStore) workspacePreparedIDs(revisionID string) (workspacePreparedIDs, error) {
	values := make([]string, 3)
	for index, domain := range []string{"workspace-prepared:event", "workspace-prepared:outbox", "workspace-prepared:publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain, revisionID)
		if err != nil {
			return workspacePreparedIDs{}, err
		}
		values[index] = value
	}
	return workspacePreparedIDs{values[0], values[1], values[2]}, nil
}

func (store RunStore) workspaceAuthorizationIDs(revisionID string) (workspaceAuthorizationIDs, error) {
	domains := []string{"workspace-authorized:event", "workspace-authorized:publish-outbox", "workspace-authorized:publish", "workspace-authorized:execute-outbox", "workspace-authorized:execute", "workspace-authorized:job", "workspace-authorized:tool-event", "workspace-authorized:tool-publish-outbox", "workspace-authorized:tool-publish"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(store.IDKey, domain, revisionID)
		if err != nil {
			return workspaceAuthorizationIDs{}, err
		}
		values[index] = value
	}
	return workspaceAuthorizationIDs{values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7], values[8]}, nil
}

func (store RunStore) loadPreparedWorkspaceReplay(ctx context.Context, tx pgx.Tx, command PrepareWorkspaceRevisionCommand, eventID string) (PreparedWorkspaceRevision, bool, error) {
	var result PreparedWorkspaceRevision
	var toolID, workspaceID, baseRevision, preparedRevision, effectKey, proposalHash string
	err := tx.QueryRow(ctx, `SELECT id::text,status,version,prepared_hash,updated_at,tool_call_id::text,workspace_id::text,base_revision,prepared_revision,effect_key,proposal_hash FROM agent.workspace_revision_commits WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.RevisionID).Scan(&result.RevisionID, &result.Status, &result.Version, &result.PreparedHash, &result.UpdatedAt, &toolID, &workspaceID, &baseRevision, &preparedRevision, &effectKey, &proposalHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return PreparedWorkspaceRevision{}, false, nil
	}
	if err != nil {
		return PreparedWorkspaceRevision{}, false, err
	}
	if toolID != command.ToolCallID || workspaceID != command.WorkspaceID || baseRevision != command.BaseRevision || preparedRevision != command.PreparedRevision || result.PreparedHash != command.PreparedHash || effectKey != command.EffectKey || proposalHash != command.ProposalHash {
		return PreparedWorkspaceRevision{}, false, ErrWorkspaceConflict
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='WorkspaceRevisionPrepared' AND aggregate_kind='workspace_revision' AND aggregate_id=$3 AND aggregate_version=1 AND store_epoch=$4)`, command.TenantID, eventID, command.RevisionID, store.StoreEpoch).Scan(&exists); err != nil {
		return PreparedWorkspaceRevision{}, false, err
	}
	if !exists {
		return PreparedWorkspaceRevision{}, false, ErrWorkspaceConflict
	}
	result.ToolVersion = command.ExpectedToolVersion
	result.Replayed = true
	return result, true, nil
}

func (store RunStore) loadAuthorizedWorkspaceReplay(ctx context.Context, tx pgx.Tx, command AuthorizeWorkspaceRevisionCommand, identifiers workspaceAuthorizationIDs) (AuthorizedWorkspaceRevision, bool, error) {
	var result AuthorizedWorkspaceRevision
	var approvalID, approvalEventID, proposalHash, commitCommandID, authorizationEventID string
	var approvalVersion uint64
	err := tx.QueryRow(ctx, `SELECT id::text,status,version,updated_at,COALESCE(approval_id::text,''),COALESCE(approval_version,0),COALESCE(approval_event_id::text,''),COALESCE(proposal_hash,''),COALESCE(commit_command_id::text,''),COALESCE(authorization_event_id::text,'') FROM agent.workspace_revision_commits WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.RevisionID).Scan(&result.RevisionID, &result.Status, &result.Version, &result.UpdatedAt, &approvalID, &approvalVersion, &approvalEventID, &proposalHash, &commitCommandID, &authorizationEventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthorizedWorkspaceRevision{}, false, nil
	}
	if err != nil {
		return AuthorizedWorkspaceRevision{}, false, err
	}
	if approvalID == "" {
		return AuthorizedWorkspaceRevision{}, false, nil
	}
	if approvalID != command.ApprovalID || approvalVersion != command.ExpectedApprovalVersion || approvalEventID != command.ApprovalEventID || proposalHash != command.ProposalHash || commitCommandID != identifiers.executeCommand || authorizationEventID != identifiers.authorizationEvent {
		return AuthorizedWorkspaceRevision{}, false, ErrWorkspaceConflict
	}
	var eventExists, commandExists, toolEventExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='WorkspaceRevisionCommitAuthorized' AND aggregate_kind='workspace_revision' AND aggregate_id=$3 AND aggregate_version=2 AND store_epoch=$4),EXISTS(SELECT 1 FROM agent.outbox WHERE tenant_id=$1 AND command_id=$5 AND command_type='ExecuteToolCall' AND aggregate_kind='tool_call'),EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$6 AND event_type='ToolCallRequested' AND aggregate_version=$7 AND store_epoch=$4)`, command.TenantID, identifiers.authorizationEvent, command.RevisionID, store.StoreEpoch, identifiers.executeCommand, identifiers.toolEvent, command.ExpectedToolVersion+1).Scan(&eventExists, &commandExists, &toolEventExists); err != nil {
		return AuthorizedWorkspaceRevision{}, false, err
	}
	if !eventExists || !commandExists || !toolEventExists {
		return AuthorizedWorkspaceRevision{}, false, ErrWorkspaceConflict
	}
	result.AuthorizationEventID = identifiers.authorizationEvent
	result.CommitCommandID = identifiers.executeCommand
	result.JobID = identifiers.job
	result.ToolVersion = command.ExpectedToolVersion + 1
	result.Replayed = true
	return result, true, nil
}

func validPrepareWorkspaceRevision(command PrepareWorkspaceRevisionCommand) bool {
	return command.RevisionID != "" && command.TenantID != "" && command.ToolCallID != "" && command.WorkspaceID != "" && command.ExpectedToolVersion > 0 && command.BaseRevision != "" && command.PreparedRevision != "" && command.PreparedHash != "" && command.EffectKey != "" && command.ProposalHash != "" && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.PreparedEvent)
}

func validAuthorizeWorkspaceRevision(command AuthorizeWorkspaceRevisionCommand) bool {
	queue := command.QueueClass == "interactive" || command.QueueClass == "background"
	return command.RevisionID != "" && command.TenantID != "" && command.ApprovalID != "" && command.ApprovalEventID != "" && command.ProposalHash != "" && command.PermissionSnapshot != "" && command.ExpectedRevisionVersion > 0 && command.ExpectedApprovalVersion > 0 && command.ExpectedToolVersion > 0 && queue && command.ResourceClass != "" && command.Priority >= 0 && command.Priority <= 1000 && command.CostUnits > 0 && command.MaxAttempts > 0 && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.AuthorizedEvent) && validPointer(command.ToolRequestedEvent) && validPointer(command.ExecuteCommand)
}
