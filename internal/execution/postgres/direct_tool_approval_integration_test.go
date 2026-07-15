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
	executionapi "github.com/langshift/lites/internal/execution/api"
	"github.com/langshift/lites/internal/payload"
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
	const sessionID = "f4900000-0000-4000-8000-000000000009"
	seedCancellationIdentity(t, ctx, admin, tenantID, userID, "direct-approval@example.invalid", now)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,'member','active',$4)`, membershipID, tenantID, userID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.sessions(id,user_id,active_tenant_id,token_hash,csrf_secret_hash,ip_hash,user_agent_hash,last_seen_at,expires_at,reauthenticated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$8)`, sessionID, userID, tenantID, bytes.Repeat([]byte{0x51}, 32), bytes.Repeat([]byte{0x52}, 32), bytes.Repeat([]byte{0x53}, 32), bytes.Repeat([]byte{0x54}, 32), now, now.Add(time.Hour)); err != nil {
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
	granted, err := store.DecideApproval(ctx, DecideApprovalCommand{
		ApprovalID: tool.ApprovalID, TenantID: tenantID, DecisionID: "f4900000-0000-4000-8000-000000000010",
		ActorUserID: userID, SessionID: sessionID, Decision: "approve", Mode: "user",
		ProposalHash: request.ProposalHash, PermissionSnapshot: permission, ExpectedApprovalVersion: 1, TargetVersion: 1,
		ReauthenticatedAt: now, Actor: json.RawMessage(`{"kind":"user"}`), CorrelationID: correlationID,
		DecisionEvent: pointer("approval-granted"),
	})
	if err != nil || granted.Status != "granted" || granted.Version != 2 {
		t.Fatalf("grant=%#v error=%v", granted, err)
	}
	authorize := AuthorizeDirectToolCommand{
		TenantID: tenantID, ApprovalID: tool.ApprovalID, ApprovalEventID: granted.EventID,
		ProposalHash: request.ProposalHash, PermissionSnapshot: permission, ExpectedApprovalVersion: granted.Version, ExpectedToolVersion: 1,
		Actor: json.RawMessage(`{"kind":"service","name":"approval-control-plane"}`), CorrelationID: correlationID,
		ToolRequestedEvent: pointer("tool-requested"), GroupAuthorizedEvent: pointer("group-authorized"), RunWaitingToolEvent: pointer("run-waiting-tool"),
	}
	invalid := authorize
	invalid.ProposalHash = strings.Repeat("9", 64)
	if _, err = store.AuthorizeDirectTool(ctx, invalid); !errors.Is(err, ErrDirectApprovalAuthorization) {
		t.Fatalf("substituted proposal error=%v", err)
	}
	authorized, err := store.AuthorizeDirectTool(ctx, authorize)
	if err != nil || authorized.Replayed || !authorized.GroupActivated || authorized.ToolVersion != 2 || authorized.RunVersion != proposed.RunVersion+1 || authorized.CommandID == "" || authorized.JobID == "" {
		t.Fatalf("authorization=%#v error=%v", authorized, err)
	}
	replay, err := store.AuthorizeDirectTool(ctx, authorize)
	if err != nil || !replay.Replayed || replay.CommandID != authorized.CommandID || replay.JobID != authorized.JobID || replay.ToolVersion != authorized.ToolVersion {
		t.Fatalf("authorization replay=%#v first=%#v error=%v", replay, authorized, err)
	}
	var authorizedRunStatus, authorizedToolStatus, authorizedGroupKind, authorizedContinuation, storedExecuteRef, storedExecuteHash, outboxExecuteRef, outboxExecuteHash string
	var authorizedRunVersion, authorizedToolVersion, executeCount, requestedCount, groupEventCount, waitingToolEventCount int
	err = admin.QueryRow(ctx, `SELECT r.status,r.run_version,t.status,t.tool_call_version,g.group_kind,g.continuation_kind,p.execute_command_ref,p.execute_command_hash,o.payload_ref,o.payload_hash,
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_id=$5 AND command_type='ExecuteToolCall'),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='tool_call' AND aggregate_id=$3 AND event_type='ToolCallRequested'),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='parallel_group' AND aggregate_id=$4 AND event_type='DirectApprovalGroupAuthorized'),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND event_type='RunWaitingTool')
		FROM agent.runs r JOIN agent.tool_calls t ON t.tenant_id=r.tenant_id AND t.run_id=r.id AND t.id=$3
		JOIN agent.parallel_groups g ON g.tenant_id=r.tenant_id AND g.id=$4
		JOIN agent.tool_proposals p ON p.tenant_id=t.tenant_id AND p.tool_call_id=t.id
		JOIN agent.outbox o ON o.tenant_id=t.tenant_id AND o.command_id=$5 AND o.command_type='ExecuteToolCall'
		WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, runID, tool.ToolCallID, proposed.GroupID, authorized.CommandID).Scan(&authorizedRunStatus, &authorizedRunVersion, &authorizedToolStatus, &authorizedToolVersion, &authorizedGroupKind, &authorizedContinuation, &storedExecuteRef, &storedExecuteHash, &outboxExecuteRef, &outboxExecuteHash, &executeCount, &requestedCount, &groupEventCount, &waitingToolEventCount)
	if err != nil {
		t.Fatal(err)
	}
	if authorizedRunStatus != "waiting_tool" || authorizedRunVersion != int(authorized.RunVersion) || authorizedToolStatus != "requested" || authorizedToolVersion != 2 || authorizedGroupKind != "execution" || authorizedContinuation != "resume" || storedExecuteRef != request.ExecuteCommand.Ref || storedExecuteHash != request.ExecuteCommand.Hash || outboxExecuteRef != storedExecuteRef || outboxExecuteHash != storedExecuteHash || executeCount != 1 || requestedCount != 1 || groupEventCount != 1 || waitingToolEventCount != 1 {
		t.Fatalf("authorized run=%s/v%d tool=%s/v%d group=%s/%s command=%s/%s stored=%s/%s counts=%d/%d/%d/%d", authorizedRunStatus, authorizedRunVersion, authorizedToolStatus, authorizedToolVersion, authorizedGroupKind, authorizedContinuation, outboxExecuteRef, outboxExecuteHash, storedExecuteRef, storedExecuteHash, executeCount, requestedCount, groupEventCount, waitingToolEventCount)
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

func TestApprovalControlServiceRejectsDirectGroupAtomically(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)
	tenantID, userID, runID := fixtureID("d9", 1), fixtureID("d9", 2), fixtureID("d9", 3)
	conversationID, correlationID, storeEpoch := fixtureID("d9", 4), fixtureID("d9", 5), fixtureID("d9", 6)
	membershipID, sessionID := fixtureID("d9", 7), fixtureID("d9", 8)
	seedCancellationIdentity(t, ctx, admin, tenantID, userID, "direct-reject@example.invalid", now)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,'member','active',$4)`, membershipID, tenantID, userID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.sessions(id,user_id,active_tenant_id,token_hash,csrf_secret_hash,ip_hash,user_agent_hash,last_seen_at,expires_at,reauthenticated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$8)`, sessionID, userID, tenantID, bytes.Repeat([]byte{0x61}, 32), bytes.Repeat([]byte{0x62}, 32), bytes.Repeat([]byte{0x63}, 32), bytes.Repeat([]byte{0x64}, 32), now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	permission := fmt.Sprintf("membership:%s:v1:role:member", membershipID)
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{0x69}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "direct-tool-rejection", Pepper: bytes.Repeat([]byte{0x6a}, 32)}, LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver}
	pointer := func(name string) PayloadPointer {
		return PayloadPointer{Ref: "encrypted://direct-rejection/" + name, Hash: "direct-rejection-" + name}
	}
	accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(2 * time.Hour), BehaviorProfile: "route_planner", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":16}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: pointer("accepted"), QueuedEvent: pointer("queued"), StartCommand: pointer("start"), QueueClass: "interactive", ResourceClass: "llm", Priority: 70, CostUnits: 4, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: pointer("start").Ref, PayloadHash: pointer("start").Hash}, ConsumerName: "agent-run-worker", WorkerID: "direct-rejection-worker", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: pointer("run-started"), AttemptStartedEvent: pointer("attempt-started"), AttemptExpiredEvent: pointer("attempt-expired")})
	if err != nil {
		t.Fatal(err)
	}
	request := DirectApprovalToolRequest{ToolName: "github_delete_repository", DescriptorSnapshotID: "github_delete_repository@sha256:v1", NormalizedInputRef: "encrypted://direct-rejection/input", RequestHash: strings.Repeat("6", 64), EffectClass: "reconcilable_write", EffectKey: "repo:delete:1", EffectScope: "tenant:github:installation:2", ProviderID: "github", PolicySnapshot: "guardrail:production:v1", ScopeSnapshot: PayloadPointer{Ref: "encrypted://direct-rejection/scope", Hash: strings.Repeat("7", 64)}, ExecuteCommand: PayloadPointer{Ref: "encrypted://direct-rejection/execute", Hash: strings.Repeat("8", 64)}, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 90, CostUnits: 3, MaxAttempts: 3, ToolProposedEvent: pointer("tool-proposed"), ApprovalRequestedEvent: pointer("approval-requested"), NotifyApproval: pointer("notify")}
	request.ProposalHash, err = directToolProposalHash(request, permission)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := request
	secondRequest.ToolName = "github_delete_branch"
	secondRequest.DescriptorSnapshotID = "github_delete_branch@sha256:v1"
	secondRequest.NormalizedInputRef = "encrypted://direct-rejection/input-2"
	secondRequest.RequestHash = strings.Repeat("9", 64)
	secondRequest.EffectKey = "branch:delete:2"
	secondRequest.ScopeSnapshot = PayloadPointer{Ref: "encrypted://direct-rejection/scope-2", Hash: strings.Repeat("a", 64)}
	secondRequest.ExecuteCommand = PayloadPointer{Ref: "encrypted://direct-rejection/execute-2", Hash: strings.Repeat("b", 64)}
	secondRequest.ToolProposedEvent, secondRequest.ApprovalRequestedEvent, secondRequest.NotifyApproval = pointer("tool-proposed-2"), pointer("approval-requested-2"), pointer("notify-2")
	secondRequest.ProposalHash, err = directToolProposalHash(secondRequest, permission)
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := store.ProposeDirectTools(ctx, ProposeDirectToolsCommand{Claim: claim, ExpectedRunVersion: claim.RunVersion, StepID: "reject-dangerous-write", ToolRequests: []DirectApprovalToolRequest{request, secondRequest}, PermissionSnapshot: permission, ApprovalExpiresAt: now.Add(30 * time.Minute), PlanResultHash: "provider-result-reject", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunWaitingApprovalEvent: pointer("run-waiting-approval"), AttemptCompletedEvent: pointer("attempt-completed"), NotifyQueueClass: "interactive", NotifyResourceClass: "notification", NotifyPriority: 50, NotifyCostUnits: 1, NotifyMaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	blobs := &repairServiceBlobs{values: map[string][]byte{}}
	payloads := payload.EnvelopeStore{Keys: repairServiceKeyProvider{key: payload.Key{ID: "direct-rejection-v1", Material: bytes.Repeat([]byte{0x6d}, 32)}}, Blobs: blobs}
	service := ApprovalControlService{Pool: pool, Store: store, Payloads: payloads, IDKey: store.IDKey, IdempotencyKeyPepper: bytes.Repeat([]byte{0x6b}, 32), RequestDigestPepper: bytes.Repeat([]byte{0x6c}, 32), IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	approved, err := service.DecideApproval(ctx, executionapi.DecideApprovalCommand{RequestID: correlationID, ClientRequestID: "client-direct-approve-d9", IdempotencyKey: "direct-approve-idempotency-d9-0001", TenantID: tenantID, UserID: userID, SessionID: sessionID, ApprovalID: proposed.Tools[0].ApprovalID, Decision: "approve", ProposalHash: request.ProposalHash, Mode: "user", ExpectedApprovalVersion: 1, TargetVersion: 1})
	if err != nil || approved.Status != "granted" || approved.Version != 2 {
		t.Fatalf("partial approval=%#v error=%v", approved, err)
	}
	var partiallyRequested, pendingExecuteJobs, partialRunWaiting int
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent.tool_calls WHERE tenant_id=$1 AND id=$2 AND status='requested'),(SELECT count(*) FROM agent.jobs j JOIN agent.tool_calls t ON t.tenant_id=j.tenant_id AND t.pending_command_id=j.command_id WHERE t.tenant_id=$1 AND t.id=$2 AND j.status='pending'),(SELECT count(*) FROM agent.runs WHERE tenant_id=$1 AND id=$3 AND status='waiting_approval')`, tenantID, proposed.Tools[0].ToolCallID, runID).Scan(&partiallyRequested, &pendingExecuteJobs, &partialRunWaiting); err != nil || partiallyRequested != 1 || pendingExecuteJobs != 1 || partialRunWaiting != 1 {
		t.Fatalf("partial authorization requested=%d jobs=%d run_waiting=%d error=%v", partiallyRequested, pendingExecuteJobs, partialRunWaiting, err)
	}
	decision := executionapi.DecideApprovalCommand{RequestID: correlationID, ClientRequestID: "client-direct-reject-d9", IdempotencyKey: "direct-reject-idempotency-d9-0001", TenantID: tenantID, UserID: userID, SessionID: sessionID, ApprovalID: proposed.Tools[1].ApprovalID, Decision: "reject", ProposalHash: secondRequest.ProposalHash, Mode: "user", ExpectedApprovalVersion: 1, TargetVersion: 1}
	result, err := service.DecideApproval(ctx, decision)
	if err != nil || result.Status != "rejected" || result.Version != 2 {
		t.Fatalf("decision=%#v error=%v", result, err)
	}
	replay, err := service.DecideApproval(ctx, decision)
	if err != nil || replay != result {
		t.Fatalf("replay=%#v first=%#v error=%v", replay, result, err)
	}
	var runStatus, approvalStatus string
	var runVersion, groupJoined, cancelledTools, executeCommands, cancelledExecuteJobs, cancelledToolEvents, cancelledRunEvents, idempotencyRows int
	err = admin.QueryRow(ctx, `SELECT r.status,r.run_version,a.status,(g.joined::int),
		(SELECT count(*) FROM agent.tool_calls t JOIN agent.parallel_group_members m ON m.tenant_id=t.tenant_id AND m.tool_call_id=t.id WHERE m.tenant_id=$1 AND m.group_id=$5 AND t.status='cancelled'),
		(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND aggregate_kind='tool_call' AND command_type='ExecuteToolCall' AND aggregate_id IN ($3,$6)),
		(SELECT count(*) FROM agent.jobs j JOIN agent.outbox o ON o.tenant_id=j.tenant_id AND o.command_id=j.command_id WHERE j.tenant_id=$1 AND o.command_type='ExecuteToolCall' AND o.aggregate_id IN ($3,$6) AND j.status='cancelled'),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='tool_call' AND aggregate_id IN ($3,$6) AND event_type='ToolCallCancelled'),
		(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$2 AND event_type='RunCancelled'),
		(SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND operation_id='approvals.decide' AND status='completed')
		FROM agent.runs r JOIN agent.approvals a ON a.tenant_id=r.tenant_id AND a.run_id=r.id AND a.id=$4
		JOIN agent.parallel_groups g ON g.tenant_id=r.tenant_id AND g.id=$5 WHERE r.tenant_id=$1 AND r.id=$2`, tenantID, runID, proposed.Tools[0].ToolCallID, proposed.Tools[1].ApprovalID, proposed.GroupID, proposed.Tools[1].ToolCallID).Scan(&runStatus, &runVersion, &approvalStatus, &groupJoined, &cancelledTools, &executeCommands, &cancelledExecuteJobs, &cancelledToolEvents, &cancelledRunEvents, &idempotencyRows)
	if err != nil {
		t.Fatal(err)
	}
	if runStatus != "cancelled" || runVersion != int(proposed.RunVersion+1) || approvalStatus != "rejected" || groupJoined != 0 || cancelledTools != 2 || executeCommands != 1 || cancelledExecuteJobs != 1 || cancelledToolEvents != 2 || cancelledRunEvents != 1 || idempotencyRows != 2 {
		t.Fatalf("run=%s/v%d approval=%s joined=%d tools=%d execute=%d cancelled_jobs=%d events=%d/%d idempotency=%d", runStatus, runVersion, approvalStatus, groupJoined, cancelledTools, executeCommands, cancelledExecuteJobs, cancelledToolEvents, cancelledRunEvents, idempotencyRows)
	}
}
