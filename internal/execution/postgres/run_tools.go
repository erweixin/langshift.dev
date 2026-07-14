package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

const maximumParallelToolCalls = 32

type ToolRequest struct {
	ToolName             string
	DescriptorSnapshotID string
	NormalizedInputRef   string
	RequestHash          string
	EffectClass          string
	EffectKey            string
	EffectScope          string
	ProviderID           string
	Required             bool
	QueueClass           string
	ResourceClass        string
	Priority             int
	CostUnits            int64
	MaxAttempts          int
	RequiresPreview      bool
	RequestedEvent       PayloadPointer
	ExecuteCommand       PayloadPointer
	PreviewCommand       PayloadPointer
}

type RequestToolsCommand struct {
	Claim                 RunClaim
	ExpectedRunVersion    uint64
	StepID                string
	JoinPolicy            string
	QuorumCount           int
	ToolRequests          []ToolRequest
	PlanResultHash        string
	Actor                 json.RawMessage
	CorrelationID         string
	AttemptCompletedEvent PayloadPointer
}

type RequestedToolCall struct {
	ToolCallID, CommandID, JobID, EventID string
	Required                              bool
}

type ToolsRequested struct {
	RunID, GroupID string
	RunVersion     uint64
	ToolCalls      []RequestedToolCall
	CompletedAt    time.Time
}

type toolRequestIDs struct {
	toolCall, effect, command, job, event, publishOutbox, publishCommand, executeOutbox string
}

// RequestTools commits an entire AgentWorker tool plan. There is no state in
// which the Run waits without a complete group, or a ToolCall exists without
// its durable command and Scheduler Job.
func (store RunStore) RequestTools(ctx context.Context, command RequestToolsCommand) (ToolsRequested, error) {
	claim := command.Claim
	if !store.validClaim() || !validRunClaim(claim) {
		return ToolsRequested{}, ErrConfiguration
	}
	if !validRequestTools(command) {
		return ToolsRequested{}, ErrInvalidCommand
	}
	if err := statemachine.Runs.ValidateTransition(statemachine.RunExecuting, statemachine.RunWaitingTool); err != nil {
		return ToolsRequested{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, claim.StoreEpoch); err != nil {
		return ToolsRequested{}, err
	}
	identifiers, groupID, err := store.toolRequestIdentifiers(claim.RunID, command.ExpectedRunVersion, command.ToolRequests)
	if err != nil {
		return ToolsRequested{}, err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return ToolsRequested{}, ErrExecutionRightConflict
	}
	now := store.claimNow()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ToolsRequested{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return ToolsRequested{}, err
	}
	if err = lockRunInbox(ctx, tx, claim, digest[:], now); err != nil {
		return ToolsRequested{}, err
	}
	var userID string
	var dueAt time.Time
	err = tx.QueryRow(ctx, `SELECT user_id::text,due_at FROM agent.runs WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND status='executing' AND run_version=$4 AND active_command_id=$5 AND active_attempt_id=$6 AND current_fence=$7 AND lease_token_hash=$8 AND lease_expires_at=$9 AND lease_expires_at>$10 AND cancel_requested_at IS NULL FOR UPDATE`, claim.RunID, claim.TenantID, claim.UserID, command.ExpectedRunVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&userID, &dueAt)
	if err != nil {
		return ToolsRequested{}, ErrExecutionRightConflict
	}
	var attemptVersion uint64
	err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at>$8 FOR UPDATE`, claim.AttemptID, claim.TenantID, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&attemptVersion)
	if err != nil {
		return ToolsRequested{}, ErrExecutionRightConflict
	}
	nextRunVersion := command.ExpectedRunVersion + 1
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET status='waiting_tool',run_version=$1,active_command_id=NULL,active_attempt_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND status='executing' AND run_version=$5 AND active_command_id=$6 AND active_attempt_id=$7 AND current_fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10 AND lease_expires_at>$2 AND cancel_requested_at IS NULL`, nextRunVersion, now, claim.RunID, claim.TenantID, command.ExpectedRunVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return ToolsRequested{}, ErrExecutionRightConflict
	}
	requiredCount := requiredToolCount(command.ToolRequests)
	groupKind, continuationKind := "execution", "resume"
	if command.ToolRequests[0].RequiresPreview {
		groupKind, continuationKind = "approval_preview", "request_approval"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.parallel_groups(id,tenant_id,run_id,join_policy,required_count,group_kind,step_id,quorum_count,continuation_kind,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)`, groupID, claim.TenantID, claim.RunID, command.JoinPolicy, requiredCount, groupKind, command.StepID, command.QuorumCount, continuationKind, now); err != nil {
		return ToolsRequested{}, err
	}
	result := ToolsRequested{RunID: claim.RunID, GroupID: groupID, RunVersion: nextRunVersion, ToolCalls: make([]RequestedToolCall, 0, len(command.ToolRequests)), CompletedAt: now}
	causationID := claim.CommandID
	for index, request := range command.ToolRequests {
		idsForRequest := identifiers[index]
		toolStatus, eventType, commandType, commandPayload := "requested", "ToolCallRequested", "ExecuteToolCall", request.ExecuteCommand
		if request.RequiresPreview {
			toolStatus, eventType, commandType, commandPayload = "preview_requested", "ToolCallPreviewRequested", "PrepareToolPreview", request.PreviewCommand
		}
		var effectKey any
		if request.EffectKey != "" {
			effectKey = request.EffectKey
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.tool_calls(id,tenant_id,user_id,run_id,status,tool_call_version,tool_name,descriptor_snapshot_id,normalized_input_ref,request_hash,effect_class,effect_key,pending_command_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,1,$6,$7,$8,$9,$10,$11,$12,$13,$13)`, idsForRequest.toolCall, claim.TenantID, userID, claim.RunID, toolStatus, request.ToolName, request.DescriptorSnapshotID, request.NormalizedInputRef, request.RequestHash, request.EffectClass, effectKey, idsForRequest.command, now); err != nil {
			return ToolsRequested{}, err
		}
		if request.EffectClass != "read_only" {
			if _, err = tx.Exec(ctx, `INSERT INTO agent.tool_effects(id,tenant_id,effect_scope,provider_id,tool_name,effect_key,request_hash,status,tool_call_id,run_id,effect_class,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,'prepared',$8,$9,$10,$11,$11)`, idsForRequest.effect, claim.TenantID, request.EffectScope, request.ProviderID, request.ToolName, request.EffectKey, request.RequestHash, idsForRequest.toolCall, claim.RunID, request.EffectClass, now); err != nil {
				return ToolsRequested{}, err
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.parallel_group_members(tenant_id,group_id,tool_call_id,required,created_at) VALUES($1,$2,$3,$4,$5)`, claim.TenantID, groupID, idsForRequest.toolCall, request.Required, now); err != nil {
			return ToolsRequested{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.jobs(id,tenant_id,command_id,queue_class,resource_class,priority,cost_units,max_attempts,status,available_at,due_at,enqueued_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$9,$9,$9)`, idsForRequest.job, claim.TenantID, idsForRequest.command, request.QueueClass, request.ResourceClass, request.Priority, request.CostUnits, request.MaxAttempts, now, dueAt); err != nil {
			return ToolsRequested{}, err
		}
		event := eventpostgres.Input{Event: eventpostgres.Event{ID: idsForRequest.event, TenantID: claim.TenantID, UserID: userID, EventType: eventType, SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: idsForRequest.toolCall, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: request.RequestedEvent.Ref, PayloadHash: request.RequestedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: idsForRequest.publishOutbox, CommandID: idsForRequest.publishCommand, CommandType: "events.publish", PayloadRef: request.RequestedEvent.Ref, PayloadHash: request.RequestedEvent.Hash}, {ID: idsForRequest.executeOutbox, CommandID: idsForRequest.command, CommandType: commandType, PayloadRef: commandPayload.Ref, PayloadHash: commandPayload.Hash}}}
		if _, err = store.Appender.Append(ctx, tx, event); err != nil {
			return ToolsRequested{}, err
		}
		result.ToolCalls = append(result.ToolCalls, RequestedToolCall{ToolCallID: idsForRequest.toolCall, CommandID: idsForRequest.command, JobID: idsForRequest.job, EventID: idsForRequest.event, Required: request.Required})
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.inbox SET status='completed',completed_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND store_epoch=$4 AND consumer_name=$5 AND command_id=$6 AND request_hash=$7 AND status='running' AND owner_attempt_id=$8 AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$1`, now, claim.InboxID, claim.TenantID, claim.StoreEpoch, claim.ConsumerName, claim.CommandID, claim.RequestHash, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return ToolsRequested{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status='succeeded',finished_at=$2,result_hash=$3,updated_at=$2 WHERE id=$4 AND tenant_id=$5 AND version=$6 AND job_id=$7 AND command_id=$8 AND status='running' AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$2`, attemptVersion+1, now, command.PlanResultHash, claim.AttemptID, claim.TenantID, attemptVersion, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return ToolsRequested{}, ErrExecutionRightConflict
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='succeeded',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND command_id=$4 AND status='running'`, now, claim.JobID, claim.TenantID, claim.CommandID); updateErr != nil || tag.RowsAffected() != 1 {
		return ToolsRequested{}, ErrExecutionRightConflict
	}
	completionIDs, err := store.completionEventIdentifiers(claim.AttemptID, statemachine.RunWaitingTool)
	if err != nil {
		return ToolsRequested{}, err
	}
	attemptEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: completionIDs.attemptEvent, TenantID: claim.TenantID, UserID: userID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.AttemptID, AggregateVersion: attemptVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: completionIDs.attemptOutbox, CommandID: completionIDs.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, attemptEvent); err != nil {
		return ToolsRequested{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ToolsRequested{}, err
	}
	return result, nil
}

func validRequestTools(command RequestToolsCommand) bool {
	if command.ExpectedRunVersion != command.Claim.RunVersion || command.StepID == "" || len(command.StepID) > 256 || len(command.ToolRequests) < 1 || len(command.ToolRequests) > maximumParallelToolCalls || command.PlanResultHash == "" || !validJSONObject(command.Actor) || command.CorrelationID == "" || !validPointer(command.AttemptCompletedEvent) {
		return false
	}
	required := requiredToolCount(command.ToolRequests)
	if required < 1 || command.QuorumCount < 1 || command.QuorumCount > required || command.JoinPolicy != "all" && command.JoinPolicy != "any" && command.JoinPolicy != "quorum" || command.JoinPolicy == "all" && command.QuorumCount != required || command.JoinPolicy == "any" && command.QuorumCount != 1 {
		return false
	}
	seenHashes := map[string]bool{}
	previewMode := command.ToolRequests[0].RequiresPreview
	if previewMode && command.JoinPolicy != "all" {
		return false
	}
	for _, request := range command.ToolRequests {
		if request.RequiresPreview != previewMode {
			return false
		}
		if request.RequiresPreview && request.EffectClass == "read_only" {
			return false
		}
		commandValid := !request.RequiresPreview && validPointer(request.ExecuteCommand) && request.PreviewCommand == (PayloadPointer{}) || request.RequiresPreview && validPointer(request.PreviewCommand) && request.ExecuteCommand == (PayloadPointer{})
		if request.ToolName == "" || request.DescriptorSnapshotID == "" || request.NormalizedInputRef == "" || request.RequestHash == "" || !validEffect(request.EffectClass, request.EffectKey, request.EffectScope, request.ProviderID) || request.QueueClass != "interactive" && request.QueueClass != "background" || request.ResourceClass == "" || request.Priority < 0 || request.Priority > 1000 || request.CostUnits < 1 || request.CostUnits > 1_000_000_000_000 || request.MaxAttempts < 1 || request.MaxAttempts > 100 || !validPointer(request.RequestedEvent) || !commandValid {
			return false
		}
		if seenHashes[request.RequestHash] {
			return false
		}
		seenHashes[request.RequestHash] = true
	}
	return true
}

func validEffect(class, key, scope, provider string) bool {
	known := class == "read_only" || class == "idempotent_write" || class == "reconcilable_write" || class == "compensatable_write" || class == "irreversible_write"
	return known && (class == "read_only" && key == "" && scope == "" && provider == "" || class != "read_only" && key != "" && scope != "" && provider != "")
}

func requiredToolCount(requests []ToolRequest) int {
	count := 0
	for _, request := range requests {
		if request.Required {
			count++
		}
	}
	return count
}

func (store RunStore) toolRequestIdentifiers(runID string, runVersion uint64, requests []ToolRequest) ([]toolRequestIDs, string, error) {
	scope := fmt.Sprintf("%s\x00%d", runID, runVersion)
	groupDomain := "parallel-tool-group"
	if requests[0].RequiresPreview {
		groupDomain = "parallel-approval-preview-group"
	}
	groupID, err := ids.DeterministicUUID(store.IDKey, groupDomain, scope)
	if err != nil {
		return nil, "", err
	}
	result := make([]toolRequestIDs, len(requests))
	for index, request := range requests {
		requestScope := fmt.Sprintf("%s\x00%d\x00%s", scope, index, request.RequestHash)
		domains := []string{"tool-call", "tool-effect", "execute-tool-command", "execute-tool-job", "tool-call-requested-event", "tool-call-requested-publish-outbox", "tool-call-requested-publish-command", "execute-tool-outbox"}
		if request.RequiresPreview {
			domains = []string{"preview-tool-call", "preview-tool-effect", "prepare-tool-preview-command", "prepare-tool-preview-job", "tool-call-preview-requested-event", "tool-call-preview-requested-publish-outbox", "tool-call-preview-requested-publish-command", "prepare-tool-preview-outbox"}
		}
		values := make([]string, len(domains))
		for domainIndex, domain := range domains {
			values[domainIndex], err = ids.DeterministicUUID(store.IDKey, domain, requestScope)
			if err != nil {
				return nil, "", err
			}
		}
		result[index] = toolRequestIDs{values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7]}
	}
	return result, groupID, nil
}
