//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestProposeDirectToolsAtomicallyWaitsWithoutExecuteDispatch(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC)
	const tenantID = "f4900000-0000-4000-8000-000000000001"
	const userID = "f4900000-0000-4000-8000-000000000002"
	const runID = "f4900000-0000-4000-8000-000000000003"
	const conversationID = "f4900000-0000-4000-8000-000000000004"
	const correlationID = "f4900000-0000-4000-8000-000000000005"
	const storeEpoch = "f4900000-0000-4000-8000-000000000006"
	const membershipID = "f4900000-0000-4000-8000-000000000007"
	seedCancellationIdentity(t, ctx, admin, tenantID, userID, "direct-approval@example.invalid", now)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,'member','active',$4)`, membershipID, tenantID, userID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	permission := fmt.Sprintf("membership:%s:v1:role:member", membershipID)
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{0x49}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "direct-tool-approval", Pepper: bytes.Repeat([]byte{0x4a}, 32)}, LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver}
	pointer := func(name string) PayloadPointer {
		return PayloadPointer{Ref: "encrypted://direct-approval/" + name, Hash: "direct-approval-" + name}
	}
	accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(2 * time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: pointer("accepted"), QueuedEvent: pointer("queued"), StartCommand: pointer("start"), QueueClass: "interactive", ResourceClass: "llm", Priority: 70, CostUnits: 4, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: pointer("start").Ref, PayloadHash: pointer("start").Hash}, ConsumerName: "agent-run-worker", WorkerID: "direct-approval-worker", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: pointer("run-started"), AttemptStartedEvent: pointer("attempt-started"), AttemptExpiredEvent: pointer("attempt-expired")})
	if err != nil {
		t.Fatal(err)
	}
	request := DirectApprovalToolRequest{
		ToolName: "github_create_issue", DescriptorSnapshotID: "github_create_issue@sha256:v1",
		NormalizedInputRef: "encrypted://direct-approval/input", RequestHash: strings.Repeat("1", 64),
		EffectClass: "idempotent_write", EffectKey: "issue:direct:1", EffectScope: "tenant:github:installation:1", ProviderID: "github",
		PolicySnapshot: "guardrail:production:v1", ScopeSnapshot: PayloadPointer{Ref: "encrypted://direct-approval/scope", Hash: strings.Repeat("2", 64)},
		ExecuteCommand: PayloadPointer{Ref: "encrypted://direct-approval/execute", Hash: strings.Repeat("3", 64)},
		QueueClass:     "interactive", ResourceClass: "tool-network", Priority: 60, CostUnits: 2, MaxAttempts: 5,
		ToolProposedEvent: pointer("tool-proposed"), ApprovalRequestedEvent: pointer("approval-requested"), NotifyApproval: pointer("notify-approval"),
	}
	request.ProposalHash, err = directToolProposalHash(request, permission)
	if err != nil {
		t.Fatal(err)
	}
	command := ProposeDirectToolsCommand{
		Claim: claim, ExpectedRunVersion: claim.RunVersion, StepID: "direct-dangerous-write",
		ToolRequests: []DirectApprovalToolRequest{request}, PermissionSnapshot: permission,
		ApprovalExpiresAt: now.Add(30 * time.Minute), PlanResultHash: "provider-result-direct",
		Actor: json.RawMessage(`{"kind":"service","name":"agent-worker"}`), CorrelationID: correlationID,
		AssistantMessage:        &RunMessageInput{MessageID: "f4900000-0000-4000-8000-000000000008", Message: PayloadPointer{Ref: "encrypted://direct-approval/assistant", Hash: strings.Repeat("4", 64)}, ContentHash: strings.Repeat("5", 64), FinalizedEvent: pointer("message-finalized")},
		RunWaitingApprovalEvent: pointer("run-waiting-approval"), AttemptCompletedEvent: pointer("attempt-completed"),
		NotifyQueueClass: "interactive", NotifyResourceClass: "notification", NotifyPriority: 50, NotifyCostUnits: 1, NotifyMaxAttempts: 8,
	}
	const contenders = 16
	results := make(chan DirectToolsProposed, contenders)
	errorsFound := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, proposeErr := store.ProposeDirectTools(ctx, command)
			if proposeErr != nil {
				errorsFound <- proposeErr
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	var proposed DirectToolsProposed
	successes, fenced := 0, 0
	for result := range results {
		proposed, successes = result, successes+1
	}
	for proposeErr := range errorsFound {
		if !errors.Is(proposeErr, ErrExecutionRightConflict) {
			t.Fatalf("unexpected contender error: %v", proposeErr)
		}
		fenced++
	}
	if successes != 1 || fenced != contenders-1 || proposed.RunVersion != claim.RunVersion+1 || len(proposed.Tools) != 1 {
		t.Fatalf("proposed=%#v successes=%d fenced=%d", proposed, successes, fenced)
	}
	tool := proposed.Tools[0]
	var runStatus, toolStatus, approvalStatus, inboxStatus, attemptStatus, jobStatus, groupKind, continuationKind string
	var runVersion, toolVersion, approvalVersion, proposals, effects, members, notifyCommands, executeCommands, messages, proposedEvents, approvalEvents, runEvents int
	err = admin.QueryRow(ctx, `SELECT r.status,r.run_version,t.status,t.tool_call_version,a.status,a.version,i.status,ja.status,j.status,g.group_kind,g.continuation_kind,
		(SELECT count(*) FROM agent.tool_proposals WHERE tenant_id=$1 AND id=$8 AND proposal_hash=$9 AND execute_command_hash=$10 AND scope_snapshot_hash=$11),
		(SELECT count(*) FROM agent.tool_effects WHERE tenant_id=$1 AND tool_call_id=$4 AND status='prepared'),
		(SELECT count(*) FROM agent.parallel_group_members WHERE tenant_id=$1 AND group_id=$7 AND tool_call_id=$4 AND required),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='NotifyApproval' AND aggregate_id=$5),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='ExecuteToolCall' AND aggregate_id=$4),
		(SELECT count(*) FROM agent.run_messages WHERE tenant_id=$1 AND run_id=$2 AND id=$12),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='tool_call' AND aggregate_id=$4 AND event_type='ToolCallProposed'),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='approval' AND aggregate_id=$5 AND event_type='ApprovalRequested'),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND event_type='RunWaitingApproval')
		FROM agent.runs r JOIN agent.tool_calls t ON t.tenant_id=r.tenant_id AND t.run_id=r.id AND t.id=$4
		JOIN agent.approvals a ON a.tenant_id=t.tenant_id AND a.tool_call_id=t.id AND a.id=$5
		JOIN agent.parallel_groups g ON g.tenant_id=t.tenant_id AND g.id=$7
		JOIN agent.inbox i ON i.tenant_id=t.tenant_id AND i.id=$3
		JOIN agent.job_attempts ja ON ja.tenant_id=t.tenant_id AND ja.id=$6
		JOIN agent.jobs j ON j.tenant_id=t.tenant_id AND j.id=ja.job_id
		WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, runID, claim.InboxID, tool.ToolCallID, tool.ApprovalID, claim.AttemptID, proposed.GroupID, tool.ProposalID, request.ProposalHash, request.ExecuteCommand.Hash, request.ScopeSnapshot.Hash, command.AssistantMessage.MessageID).Scan(&runStatus, &runVersion, &toolStatus, &toolVersion, &approvalStatus, &approvalVersion, &inboxStatus, &attemptStatus, &jobStatus, &groupKind, &continuationKind, &proposals, &effects, &members, &notifyCommands, &executeCommands, &messages, &proposedEvents, &approvalEvents, &runEvents)
	if err != nil {
		t.Fatal(err)
	}
	if runStatus != "waiting_approval" || runVersion != int(proposed.RunVersion) || toolStatus != "awaiting_approval" || toolVersion != 1 || approvalStatus != "pending" || approvalVersion != 1 || inboxStatus != "completed" || attemptStatus != "succeeded" || jobStatus != "succeeded" || groupKind != "approval_direct" || continuationKind != "request_approval" || proposals != 1 || effects != 1 || members != 1 || notifyCommands != 1 || executeCommands != 0 || messages != 1 || proposedEvents != 1 || approvalEvents != 1 || runEvents != 1 {
		t.Fatalf("run=%s/v%d tool=%s/v%d approval=%s/v%d inbox=%s attempt=%s job=%s group=%s/%s proposals=%d effects=%d members=%d notify=%d execute=%d messages=%d events=%d/%d/%d", runStatus, runVersion, toolStatus, toolVersion, approvalStatus, approvalVersion, inboxStatus, attemptStatus, jobStatus, groupKind, continuationKind, proposals, effects, members, notifyCommands, executeCommands, messages, proposedEvents, approvalEvents, runEvents)
	}
	if _, err = admin.Exec(ctx, `UPDATE agent.tool_proposals SET priority=priority+1 WHERE tenant_id=$1 AND id=$2`, tenantID, tool.ProposalID); err == nil {
		t.Fatal("append-only proposal was mutable")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, "f4900000-0000-4000-8000-000000000099"); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent.tool_proposals WHERE id=$1`, tool.ProposalID).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("cross-tenant visible=%d error=%v", visible, err)
	}
}
