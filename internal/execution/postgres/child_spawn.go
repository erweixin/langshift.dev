package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/langshift/lites/internal/behavior"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/platform/ids"
)

const maximumChildRuns = 10

type ChildRunRequest struct {
	RunID, RequestHash, DescriptorSnapshotID, NormalizedInputRef string
	BehaviorProfile                                              string
	BehaviorEnvironment                                          string
	BudgetSnapshot                                               json.RawMessage
	BudgetMicrounits                                             int64
	DueAt                                                        time.Time
	Required                                                     bool
	QueueClass, ResourceClass                                    string
	Priority, MaxAttempts                                        int
	CostUnits                                                    int64
	ToolSucceededEvent                                           PayloadPointer
	AcceptedEvent, QueuedEvent, StartCommand                     PayloadPointer
}

type SpawnChildRunsCommand struct {
	Claim                 RunClaim
	ExpectedRunVersion    uint64
	StepID, JoinPolicy    string
	QuorumCount           int
	Children              []ChildRunRequest
	PlanResultHash        string
	Actor                 json.RawMessage
	CorrelationID         string
	AttemptCompletedEvent PayloadPointer
	ParentWaitingEvent    PayloadPointer
}

type SpawnedChildRun struct {
	RunID, ToolCallID, StartCommandID, StartJobID, SpawnedEventID string
	Required                                                      bool
}

type ChildRunsSpawned struct {
	ParentRunID, RootRunID, GroupID string
	ParentRunVersion, Depth         uint64
	Children                        []SpawnedChildRun
	CompletedAt                     time.Time
}

type childSpawnIDs struct {
	toolCall, toolEvent, toolOutbox, toolPublish string
}

type childGroupSpawnIDs struct{ event, outbox, publish string }

// SpawnChildRuns is the only commit point for Agent-to-Agent delegation. The
// parent execution right, root quota, inline spawn ToolCalls, child group,
// Child Runs, Scheduler jobs, outbox commands, and attempt completion are one
// transaction; there is no durable state where a parent waits for a missing
// child or a child exists without StartAgentRun.
func (store RunStore) SpawnChildRuns(ctx context.Context, command SpawnChildRunsCommand) (ChildRunsSpawned, error) {
	claim := command.Claim
	if !store.validClaim() || !validRunClaim(claim) {
		return ChildRunsSpawned{}, ErrConfiguration
	}
	if !validSpawnChildRuns(command) {
		return ChildRunsSpawned{}, ErrInvalidCommand
	}
	if err := statemachine.Runs.ValidateTransition(statemachine.RunExecuting, statemachine.RunWaitingChild); err != nil {
		return ChildRunsSpawned{}, ErrInvalidCommand
	}
	if err := store.requireClaimEpoch(ctx, claim.StoreEpoch); err != nil {
		return ChildRunsSpawned{}, err
	}
	groupID, groupEventIDs, childIDs, err := store.childSpawnIdentifiers(claim.RunID, command.ExpectedRunVersion, command.StepID, command.Children)
	if err != nil {
		return ChildRunsSpawned{}, err
	}
	digest, err := store.Tokens.Digest(claim.LeaseToken)
	if err != nil {
		return ChildRunsSpawned{}, ErrExecutionRightConflict
	}
	now := store.claimNow()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ChildRunsSpawned{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, claim.TenantID); err != nil {
		return ChildRunsSpawned{}, err
	}
	if err = lockRunInbox(ctx, tx, claim, digest[:], now); err != nil {
		return ChildRunsSpawned{}, err
	}
	var userID, conversationID, rootRunID string
	var parentDueAt time.Time
	var parentDepth int
	var parentBudgetRaw json.RawMessage
	var parentInherited int64
	err = tx.QueryRow(ctx, `SELECT user_id::text,conversation_id::text,root_run_id::text,depth,due_at,budget_snapshot,inherited_budget_microunits FROM agent.runs WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND status='executing' AND run_version=$4 AND active_command_id=$5 AND active_attempt_id=$6 AND current_fence=$7 AND lease_token_hash=$8 AND lease_expires_at=$9 AND lease_expires_at>$10 AND cancel_requested_at IS NULL FOR UPDATE`, claim.RunID, claim.TenantID, claim.UserID, command.ExpectedRunVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&userID, &conversationID, &rootRunID, &parentDepth, &parentDueAt, &parentBudgetRaw, &parentInherited)
	if err != nil {
		return ChildRunsSpawned{}, ErrExecutionRightConflict
	}
	if parentDepth >= 5 {
		return ChildRunsSpawned{}, ErrInvalidCommand
	}
	var attemptVersion uint64
	if err = tx.QueryRow(ctx, `SELECT version FROM agent.job_attempts WHERE id=$1 AND tenant_id=$2 AND job_id=$3 AND command_id=$4 AND status='running' AND fence=$5 AND lease_token_hash=$6 AND lease_expires_at=$7 AND lease_expires_at>$8 FOR UPDATE`, claim.AttemptID, claim.TenantID, claim.JobID, claim.CommandID, claim.Fence, digest[:], claim.LeaseExpiresAt, now).Scan(&attemptVersion); err != nil {
		return ChildRunsSpawned{}, ErrExecutionRightConflict
	}
	var rootBudgetRaw json.RawMessage
	if err = tx.QueryRow(ctx, `SELECT budget_snapshot FROM agent.runs WHERE tenant_id=$1 AND id=$2 AND parent_run_id IS NULL FOR SHARE`, claim.TenantID, rootRunID).Scan(&rootBudgetRaw); err != nil {
		return ChildRunsSpawned{}, ErrRunConflict
	}
	rootBudget, ok := orchestrationBudget(rootBudgetRaw)
	if !ok {
		return ChildRunsSpawned{}, ErrInvalidCommand
	}
	parentBudget := rootBudget
	if parentDepth > 0 {
		parentBudget = parentInherited
	}
	requestedBudget := int64(0)
	for _, child := range command.Children {
		if child.BudgetMicrounits > math.MaxInt64-requestedBudget {
			return ChildRunsSpawned{}, ErrInvalidCommand
		}
		requestedBudget += child.BudgetMicrounits
		if child.DueAt.After(parentDueAt) || !child.DueAt.After(now) {
			return ChildRunsSpawned{}, ErrInvalidCommand
		}
	}
	var parentAllocated int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(inherited_budget_microunits),0) FROM agent.runs WHERE tenant_id=$1 AND parent_run_id=$2 AND status NOT IN ('succeeded','failed','cancelled','expired')`, claim.TenantID, claim.RunID).Scan(&parentAllocated); err != nil || requestedBudget > parentBudget-parentAllocated {
		return ChildRunsSpawned{}, ErrInvalidCommand
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.orchestration_quotas(tenant_id,root_run_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, claim.TenantID, rootRunID); err != nil {
		return ChildRunsSpawned{}, err
	}
	var quotaVersion, maxDepth, maxConcurrent, maxTotal, total, concurrent int
	var allocated int64
	if err = tx.QueryRow(ctx, `SELECT version,max_depth,max_concurrent_children,max_total_descendants,total_descendants,concurrent_children,allocated_budget_microunits FROM agent.orchestration_quotas WHERE tenant_id=$1 AND root_run_id=$2 FOR UPDATE`, claim.TenantID, rootRunID).Scan(&quotaVersion, &maxDepth, &maxConcurrent, &maxTotal, &total, &concurrent, &allocated); err != nil {
		return ChildRunsSpawned{}, err
	}
	count := len(command.Children)
	if parentDepth+1 > maxDepth || total > maxTotal-count || concurrent > maxConcurrent-count || allocated > rootBudget-requestedBudget {
		return ChildRunsSpawned{}, ErrInvalidCommand
	}
	nextRunVersion := command.ExpectedRunVersion + 1
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.runs SET status='waiting_child',run_version=$1,active_command_id=NULL,active_attempt_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=$2 WHERE id=$3 AND tenant_id=$4 AND status='executing' AND run_version=$5 AND active_command_id=$6 AND active_attempt_id=$7 AND current_fence=$8 AND lease_token_hash=$9 AND lease_expires_at=$10 AND lease_expires_at>$2 AND cancel_requested_at IS NULL`, nextRunVersion, now, claim.RunID, claim.TenantID, command.ExpectedRunVersion, claim.CommandID, claim.AttemptID, claim.Fence, digest[:], claim.LeaseExpiresAt); updateErr != nil || tag.RowsAffected() != 1 {
		return ChildRunsSpawned{}, ErrExecutionRightConflict
	}
	required := requiredChildCount(command.Children)
	if _, err = tx.Exec(ctx, `INSERT INTO agent.child_groups(id,tenant_id,parent_run_id,join_policy,required_count,step_id,quorum_count,continuation_kind,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,'resume_parent',$8,$8)`, groupID, claim.TenantID, claim.RunID, command.JoinPolicy, required, command.StepID, command.QuorumCount, now); err != nil {
		return ChildRunsSpawned{}, err
	}
	parentEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: groupEventIDs.event, TenantID: claim.TenantID, UserID: userID, EventType: "ChildRunSpawned", SchemaVersion: 1, AggregateKind: "run", AggregateID: claim.RunID, AggregateVersion: nextRunVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &claim.CommandID, CorrelationID: command.CorrelationID, PayloadRef: command.ParentWaitingEvent.Ref, PayloadHash: command.ParentWaitingEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: groupEventIDs.outbox, CommandID: groupEventIDs.publish, CommandType: "events.publish", PayloadRef: command.ParentWaitingEvent.Ref, PayloadHash: command.ParentWaitingEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, parentEvent); err != nil {
		return ChildRunsSpawned{}, err
	}
	result := ChildRunsSpawned{ParentRunID: claim.RunID, RootRunID: rootRunID, GroupID: groupID, ParentRunVersion: nextRunVersion, Depth: uint64(parentDepth + 1), Children: make([]SpawnedChildRun, 0, count), CompletedAt: now}
	causationID := claim.CommandID
	for index, child := range command.Children {
		identifier := childIDs[index]
		if _, err = tx.Exec(ctx, `INSERT INTO agent.tool_calls(id,tenant_id,user_id,run_id,status,tool_call_version,tool_name,descriptor_snapshot_id,normalized_input_ref,request_hash,effect_class,result_event_id,execution_mode,created_at,updated_at) VALUES($1,$2,$3,$4,'succeeded',1,'spawn_agent_run',$5,$6,$7,'read_only',$8,'inline_platform',$9,$9)`, identifier.toolCall, claim.TenantID, userID, claim.RunID, child.DescriptorSnapshotID, child.NormalizedInputRef, child.RequestHash, identifier.toolEvent, now); err != nil {
			return ChildRunsSpawned{}, err
		}
		toolEvent := eventpostgres.Input{Event: eventpostgres.Event{ID: identifier.toolEvent, TenantID: claim.TenantID, UserID: userID, EventType: "ToolCallSucceeded", SchemaVersion: 1, AggregateKind: "tool_call", AggregateID: identifier.toolCall, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: child.ToolSucceededEvent.Ref, PayloadHash: child.ToolSucceededEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifier.toolOutbox, CommandID: identifier.toolPublish, CommandType: "events.publish", PayloadRef: child.ToolSucceededEvent.Ref, PayloadHash: child.ToolSucceededEvent.Hash}}}
		if _, err = store.Appender.Append(ctx, tx, toolEvent); err != nil {
			return ChildRunsSpawned{}, err
		}
		accepted, acceptErr := store.AcceptInTx(ctx, tx, AcceptRunCommand{RunID: child.RunID, TenantID: claim.TenantID, UserID: userID, ConversationID: conversationID, CorrelationID: command.CorrelationID, DueAt: child.DueAt, BehaviorProfile: behavior.Profile(child.BehaviorProfile), BehaviorEnvironment: child.BehaviorEnvironment, BudgetSnapshot: child.BudgetSnapshot, Actor: command.Actor, AcceptedEvent: child.AcceptedEvent, QueuedEvent: child.QueuedEvent, StartCommand: child.StartCommand, QueueClass: child.QueueClass, ResourceClass: child.ResourceClass, Priority: child.Priority, CostUnits: child.CostUnits, MaxAttempts: child.MaxAttempts, ParentRunID: claim.RunID, RootRunID: rootRunID, SpawnToolCallID: identifier.toolCall, ChildGroupID: groupID, Depth: parentDepth + 1, InheritedBudgetMicrounits: child.BudgetMicrounits})
		if acceptErr != nil || accepted.Replayed {
			return ChildRunsSpawned{}, ErrRunConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent.child_group_members(tenant_id,group_id,child_run_id,required,created_at) VALUES($1,$2,$3,$4,$5)`, claim.TenantID, groupID, child.RunID, child.Required, now); err != nil {
			return ChildRunsSpawned{}, err
		}
		result.Children = append(result.Children, SpawnedChildRun{RunID: child.RunID, ToolCallID: identifier.toolCall, StartCommandID: accepted.StartCommandID, StartJobID: accepted.StartJobID, SpawnedEventID: groupEventIDs.event, Required: child.Required})
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE agent.orchestration_quotas SET version=version+1,total_descendants=total_descendants+$1,concurrent_children=concurrent_children+$1,allocated_budget_microunits=allocated_budget_microunits+$2,updated_at=$3 WHERE tenant_id=$4 AND root_run_id=$5 AND version=$6`, count, requestedBudget, now, claim.TenantID, rootRunID, quotaVersion); updateErr != nil || tag.RowsAffected() != 1 {
		return ChildRunsSpawned{}, ErrRunConflict
	}
	if err = store.completeSpawnAttempt(ctx, tx, command, attemptVersion, userID, digest[:], now); err != nil {
		return ChildRunsSpawned{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ChildRunsSpawned{}, err
	}
	return result, nil
}

func (store RunStore) completeSpawnAttempt(ctx context.Context, tx pgx.Tx, command SpawnChildRunsCommand, attemptVersion uint64, userID string, digest []byte, now time.Time) error {
	claim := command.Claim
	if tag, err := tx.Exec(ctx, `UPDATE agent.inbox SET status='completed',completed_at=$1,updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND store_epoch=$4 AND consumer_name=$5 AND command_id=$6 AND request_hash=$7 AND status='running' AND owner_attempt_id=$8 AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$1`, now, claim.InboxID, claim.TenantID, claim.StoreEpoch, claim.ConsumerName, claim.CommandID, claim.RequestHash, claim.AttemptID, claim.Fence, digest, claim.LeaseExpiresAt); err != nil || tag.RowsAffected() != 1 {
		return ErrExecutionRightConflict
	}
	if tag, err := tx.Exec(ctx, `UPDATE agent.job_attempts SET version=$1,status='succeeded',finished_at=$2,result_hash=$3,updated_at=$2 WHERE id=$4 AND tenant_id=$5 AND version=$6 AND job_id=$7 AND command_id=$8 AND status='running' AND fence=$9 AND lease_token_hash=$10 AND lease_expires_at=$11 AND lease_expires_at>$2`, attemptVersion+1, now, command.PlanResultHash, claim.AttemptID, claim.TenantID, attemptVersion, claim.JobID, claim.CommandID, claim.Fence, digest, claim.LeaseExpiresAt); err != nil || tag.RowsAffected() != 1 {
		return ErrExecutionRightConflict
	}
	if tag, err := tx.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='succeeded',updated_at=$1 WHERE id=$2 AND tenant_id=$3 AND command_id=$4 AND status='running'`, now, claim.JobID, claim.TenantID, claim.CommandID); err != nil || tag.RowsAffected() != 1 {
		return ErrExecutionRightConflict
	}
	identifier, err := store.completionEventIdentifiers(claim.AttemptID, statemachine.RunWaitingChild)
	if err != nil {
		return err
	}
	causationID := claim.CommandID
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: identifier.attemptEvent, TenantID: claim.TenantID, UserID: userID, EventType: "JobAttemptCompleted", SchemaVersion: 1, AggregateKind: "job_attempt", AggregateID: claim.AttemptID, AggregateVersion: attemptVersion + 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CausationID: &causationID, CorrelationID: command.CorrelationID, PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifier.attemptOutbox, CommandID: identifier.attemptPublish, CommandType: "events.publish", PayloadRef: command.AttemptCompletedEvent.Ref, PayloadHash: command.AttemptCompletedEvent.Hash}}}
	_, err = store.Appender.Append(ctx, tx, event)
	return err
}

func validSpawnChildRuns(command SpawnChildRunsCommand) bool {
	if command.ExpectedRunVersion != command.Claim.RunVersion || command.StepID == "" || len(command.StepID) > 256 || len(command.Children) < 1 || len(command.Children) > maximumChildRuns || command.PlanResultHash == "" || !validJSONObject(command.Actor) || command.CorrelationID == "" || !validPointer(command.AttemptCompletedEvent) || !validPointer(command.ParentWaitingEvent) {
		return false
	}
	required := requiredChildCount(command.Children)
	if required < 1 || command.QuorumCount < 1 || command.QuorumCount > required || command.JoinPolicy != "all" && command.JoinPolicy != "any" && command.JoinPolicy != "quorum" || command.JoinPolicy == "all" && command.QuorumCount != required || command.JoinPolicy == "any" && command.QuorumCount != 1 {
		return false
	}
	seen := map[string]bool{}
	for _, child := range command.Children {
		if child.RunID == "" || seen[child.RunID] || child.RequestHash == "" || child.DescriptorSnapshotID == "" || child.NormalizedInputRef == "" || !behavior.Profile(child.BehaviorProfile).Valid() || child.BehaviorEnvironment != "staging" && child.BehaviorEnvironment != "production" || !validJSONObject(child.BudgetSnapshot) || child.BudgetMicrounits < 1 || child.DueAt.IsZero() || !validPointer(child.ToolSucceededEvent) || !validPointer(child.AcceptedEvent) || !validPointer(child.QueuedEvent) || !validPointer(child.StartCommand) || child.QueueClass != "interactive" && child.QueueClass != "background" || child.ResourceClass == "" || child.Priority < 0 || child.Priority > 1000 || child.CostUnits < 1 || child.CostUnits > 1_000_000_000_000 || child.MaxAttempts < 1 || child.MaxAttempts > 100 {
			return false
		}
		seen[child.RunID] = true
	}
	return true
}

func requiredChildCount(children []ChildRunRequest) int {
	count := 0
	for _, child := range children {
		if child.Required {
			count++
		}
	}
	return count
}

func orchestrationBudget(raw json.RawMessage) (int64, bool) {
	var value struct {
		MaxCostMicrounits int64 `json:"max_cost_microunits"`
	}
	if err := json.Unmarshal(raw, &value); err != nil || value.MaxCostMicrounits <= 0 {
		return 0, false
	}
	return value.MaxCostMicrounits, true
}

func (store RunStore) childSpawnIdentifiers(parentRunID string, runVersion uint64, stepID string, children []ChildRunRequest) (string, childGroupSpawnIDs, []childSpawnIDs, error) {
	scope := fmt.Sprintf("%s\x00%d\x00%s", parentRunID, runVersion, stepID)
	groupID, err := ids.DeterministicUUID(store.IDKey, "child-group", scope)
	if err != nil {
		return "", childGroupSpawnIDs{}, nil, err
	}
	groupValues := make([]string, 3)
	for index, domain := range []string{"child-run-spawned-event", "child-run-spawned-publish-outbox", "child-run-spawned-publish-command"} {
		groupValues[index], err = ids.DeterministicUUID(store.IDKey, domain, scope)
		if err != nil {
			return "", childGroupSpawnIDs{}, nil, err
		}
	}
	groupEvents := childGroupSpawnIDs{groupValues[0], groupValues[1], groupValues[2]}
	result := make([]childSpawnIDs, len(children))
	for index, child := range children {
		childScope := fmt.Sprintf("%s\x00%d\x00%s\x00%s", scope, index, child.RunID, child.RequestHash)
		values := make([]string, 4)
		for domainIndex, domain := range []string{"spawn-child-tool-call", "spawn-child-tool-succeeded-event", "spawn-child-tool-publish-outbox", "spawn-child-tool-publish-command"} {
			values[domainIndex], err = ids.DeterministicUUID(store.IDKey, domain, childScope)
			if err != nil {
				return "", childGroupSpawnIDs{}, nil, err
			}
		}
		result[index] = childSpawnIDs{values[0], values[1], values[2], values[3]}
	}
	return groupID, groupEvents, result, nil
}
