//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestWorkspacePublishIsFencedHeartbeatCoherentAndAtomicallyCompleted(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()

	const prefix = "f1"
	userID, tenantID, sessionID := fixtureID(prefix, 1), fixtureID(prefix, 2), fixtureID(prefix, 3)
	runID, conversationID := fixtureID(prefix, 4), fixtureID(prefix, 5)
	correlationID, storeEpoch := fixtureID(prefix, 6), fixtureID(prefix, 7)
	now := time.Date(2026, time.July, 15, 8, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'workspace-publisher@example.invalid','en','active')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'enterprise','Workspace Publisher','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	membershipID := fixtureID(prefix, 8)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,'owner','active',$4)`, membershipID, tenantID, userID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	tokenHash, csrfHash := sha256.Sum256([]byte("token:"+sessionID)), sha256.Sum256([]byte("csrf:"+sessionID))
	ipHash, userAgentHash := sha256.Sum256([]byte("ip:"+sessionID)), sha256.Sum256([]byte("user-agent:"+sessionID))
	if _, err := admin.Exec(ctx, `INSERT INTO identity.sessions(id,user_id,active_tenant_id,token_hash,csrf_secret_hash,ip_hash,user_agent_hash,last_seen_at,expires_at,reauthenticated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$8)`, sessionID, userID, tenantID, tokenHash[:], csrfHash[:], ipHash[:], userAgentHash[:], now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	clock := now
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return clock }}, IDKey: bytes.Repeat([]byte{0x15}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return clock }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "workspace-publish", Pepper: bytes.Repeat([]byte{0x16}, 32)}, LeaseTTL: 2 * time.Minute}
	accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(time.Hour), ProfileSnapshotID: "artifact_builder@sha256:f1", BudgetSnapshot: json.RawMessage(`{"max_steps":8}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: repairPointer(prefix, "run-accepted"), QueuedEvent: repairPointer(prefix, "run-queued"), StartCommand: repairPointer(prefix, "start-run"), QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 2, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	runClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: repairPointer(prefix, "start-run").Ref, PayloadHash: repairPointer(prefix, "start-run").Hash}, ConsumerName: "workspace-agent", WorkerID: "workspace-agent-f1", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: repairPointer(prefix, "run-started"), AttemptStartedEvent: repairPointer(prefix, "run-attempt-started"), AttemptExpiredEvent: repairPointer(prefix, "run-attempt-expired")})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := store.RequestTools(ctx, RequestToolsCommand{Claim: runClaim, ExpectedRunVersion: runClaim.RunVersion, StepID: "workspace-f1", JoinPolicy: "all", QuorumCount: 1, PlanResultHash: "workspace-plan-f1", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: repairPointer(prefix, "run-attempt-completed"), ToolRequests: []ToolRequest{{ToolName: "workspace_publish", DescriptorSnapshotID: "workspace_publish@sha256:f1", NormalizedInputRef: repairPointer(prefix, "workspace-input").Ref, RequestHash: "workspace-request-f1", EffectClass: "reconcilable_write", EffectKey: "workspace:project:f1", EffectScope: "tenant:workspace:f1", ProviderID: "workspace-service", Required: true, QueueClass: "interactive", ResourceClass: "workspace-publisher", Priority: 80, CostUnits: 2, MaxAttempts: 5, RequestedEvent: repairPointer(prefix, "preview-requested"), ExecuteCommand: repairPointer(prefix, "preview-command")}}})
	if err != nil {
		t.Fatal(err)
	}
	tool := requested.ToolCalls[0]

	previewAttemptID, previewInboxID := fixtureID(prefix, 20), fixtureID(prefix, 21)
	previewLease := bytes.Repeat([]byte{0x17}, 32)
	previewExpiry := now.Add(2 * time.Minute)
	var previewJobID string
	if err = admin.QueryRow(ctx, `UPDATE agent.jobs SET status='running',updated_at=$3 WHERE tenant_id=$1 AND command_id=$2 RETURNING id::text`, tenantID, tool.CommandID, now).Scan(&previewJobID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO agent.job_attempts(id,tenant_id,job_id,command_id,fence,lease_token_hash,lease_expires_at,worker_id,status,started_at,created_at,updated_at) VALUES($1,$2,$3,$4,1,$5,$6,'workspace-preview','running',$7,$7,$7)`, previewAttemptID, tenantID, previewJobID, tool.CommandID, previewLease, previewExpiry, now); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO agent.inbox(id,tenant_id,store_epoch,consumer_name,command_id,status,owner_attempt_id,fence,lease_token_hash,lease_expires_at,request_hash,created_at,updated_at) VALUES($1,$2,$3,'workspace-preview',$4,'running',$5,1,$6,$7,$8,$9,$9)`, previewInboxID, tenantID, storeEpoch, tool.CommandID, previewAttemptID, previewLease, previewExpiry, repairPointer(prefix, "preview-command").Hash, now); err != nil {
		t.Fatal(err)
	}
	var previewToolVersion uint64
	if err = admin.QueryRow(ctx, `UPDATE agent.tool_calls SET status='preparing_approval',tool_call_version=tool_call_version+1,pending_command_id=NULL,active_command_id=$3,active_attempt_id=$4,current_fence=1,lease_token_hash=$5,lease_expires_at=$6,updated_at=$7 WHERE tenant_id=$1 AND id=$2 RETURNING tool_call_version`, tenantID, tool.ToolCallID, tool.CommandID, previewAttemptID, previewLease, previewExpiry, now).Scan(&previewToolVersion); err != nil {
		t.Fatal(err)
	}

	proposalHash := "workspace-proposal-f1"
	prepare := PrepareWorkspaceRevisionCommand{RevisionID: fixtureID(prefix, 22), TenantID: tenantID, ToolCallID: tool.ToolCallID, WorkspaceID: fixtureID(prefix, 23), ExpectedToolVersion: previewToolVersion, BaseRevision: "git:base-f1", PreparedRevision: "git:prepared-f1", PreparedHash: "sha256:prepared-f1", EffectKey: "workspace:project:f1", ProposalHash: proposalHash, Actor: json.RawMessage(`{"kind":"service","name":"workspace-preview"}`), CorrelationID: correlationID, PreparedEvent: repairPointer(prefix, "workspace-prepared")}
	if _, err = store.PrepareWorkspaceRevision(ctx, prepare); err != nil {
		t.Fatal(err)
	}
	var awaitingVersion uint64
	if err = admin.QueryRow(ctx, `UPDATE agent.tool_calls SET status='awaiting_approval',tool_call_version=tool_call_version+1,active_command_id=NULL,active_attempt_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=$3 WHERE tenant_id=$1 AND id=$2 RETURNING tool_call_version`, tenantID, tool.ToolCallID, now.Add(time.Second)).Scan(&awaitingVersion); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `UPDATE agent.inbox SET status='completed',completed_at=$1,updated_at=$1 WHERE id=$2`, now.Add(time.Second), previewInboxID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `UPDATE agent.job_attempts SET version=version+1,status='succeeded',finished_at=$1,result_hash='workspace-preview-f1',updated_at=$1 WHERE id=$2`, now.Add(time.Second), previewAttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `UPDATE agent.jobs SET version=version+1,status='succeeded',updated_at=$1 WHERE id=$2`, now.Add(time.Second), previewJobID); err != nil {
		t.Fatal(err)
	}

	permissionSnapshot := "membership:" + membershipID + ":v1:role:owner"
	request := RequestApprovalCommand{ApprovalID: fixtureID(prefix, 24), TenantID: tenantID, RunID: runID, ToolCallID: tool.ToolCallID, ApprovalKind: "tool_execution", ProposalHash: proposalHash, PermissionSnapshot: permissionSnapshot, TargetVersion: awaitingVersion, RequestedBy: userID, ExpiresAt: now.Add(20 * time.Minute), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RequestedEvent: repairPointer(prefix, "approval-requested")}
	if _, err = store.RequestApproval(ctx, request); err != nil {
		t.Fatal(err)
	}
	decision := DecideApprovalCommand{ApprovalID: request.ApprovalID, TenantID: tenantID, DecisionID: fixtureID(prefix, 25), ActorUserID: userID, SessionID: sessionID, Decision: "approve", Mode: "admin", ProposalHash: proposalHash, PermissionSnapshot: permissionSnapshot, ExpectedApprovalVersion: 1, TargetVersion: awaitingVersion, ReauthenticatedAt: now, Actor: json.RawMessage(`{"kind":"user","role":"owner"}`), CorrelationID: correlationID, DecisionEvent: repairPointer(prefix, "approval-granted")}
	granted, err := store.DecideApproval(ctx, decision)
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := store.AuthorizeWorkspaceRevision(ctx, AuthorizeWorkspaceRevisionCommand{RevisionID: prepare.RevisionID, TenantID: tenantID, ApprovalID: request.ApprovalID, ApprovalEventID: granted.EventID, ProposalHash: proposalHash, PermissionSnapshot: permissionSnapshot, ExpectedRevisionVersion: 1, ExpectedApprovalVersion: 2, ExpectedToolVersion: awaitingVersion, QueueClass: "interactive", ResourceClass: "workspace-publisher", Priority: 80, CostUnits: 2, MaxAttempts: 5, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AuthorizedEvent: repairPointer(prefix, "workspace-authorized"), ToolRequestedEvent: repairPointer(prefix, "commit-requested"), ExecuteCommand: repairPointer(prefix, "commit-command")})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimTool(ctx, ClaimToolCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: authorized.CommitCommandID, CommandType: "ExecuteToolCall", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: repairPointer(prefix, "commit-command").Ref, PayloadHash: repairPointer(prefix, "commit-command").Hash}, ConsumerName: "workspace-publisher", WorkerID: "workspace-publisher-f1", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolStartedEvent: repairPointer(prefix, "commit-tool-started"), AttemptStartedEvent: repairPointer(prefix, "commit-attempt-started"), AttemptExpiredEvent: repairPointer(prefix, "commit-attempt-expired")})
	if err != nil {
		t.Fatal(err)
	}
	beginCommand := BeginWorkspacePublishCommand{RevisionID: prepare.RevisionID, ExpectedRevisionVersion: 2, Claim: claim, Actor: json.RawMessage(`{"kind":"service","name":"workspace-publisher"}`), CorrelationID: correlationID, StartedEvent: repairPointer(prefix, "workspace-publish-started")}
	publish, err := store.BeginWorkspacePublish(ctx, beginCommand)
	if err != nil || publish.Status != "publishing" || publish.Version != 3 || publish.Replayed {
		t.Fatalf("publish=%#v err=%v", publish, err)
	}
	replay, err := store.BeginWorkspacePublish(ctx, beginCommand)
	if err != nil || !replay.Replayed || replay.EventID != publish.EventID || replay.Version != publish.Version {
		t.Fatalf("begin replay=%#v err=%v", replay, err)
	}
	clock = now.Add(30 * time.Second)
	publish, err = store.HeartbeatWorkspacePublish(ctx, publish)
	if err != nil || publish.Version != 4 || !publish.LeaseExpiresAt.After(claim.LeaseExpiresAt) {
		t.Fatalf("heartbeat=%#v err=%v", publish, err)
	}

	completeTool := CompleteToolCommand{Claim: publish.Tool, ExpectedToolVersion: publish.Tool.ToolCallVersion, TargetState: statemachine.ToolCallSucceeded, ResultHash: "workspace-published-f1", Actor: json.RawMessage(`{"kind":"service","name":"workspace-publisher"}`), CorrelationID: correlationID, ToolCompletedEvent: repairPointer(prefix, "tool-succeeded"), AttemptCompletedEvent: repairPointer(prefix, "commit-attempt-completed"), GroupJoinedEvent: repairPointer(prefix, "group-joined"), RunResumeQueuedEvent: repairPointer(prefix, "run-resume-queued"), ResumeCommand: repairPointer(prefix, "resume-command"), ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 1, ResumeMaxAttempts: 5}
	completeCommand := CompleteWorkspacePublishCommand{RevisionID: prepare.RevisionID, ExpectedRevisionVersion: publish.Version, Tool: completeTool, PublishedRevision: "git:published-f1", PublishedHash: "sha256:published-f1", ObservedRevision: "git:published-f1", CompletionEvent: repairPointer(prefix, "workspace-committed")}
	completed, err := store.CompleteWorkspacePublish(ctx, completeCommand, EffectCompletion{ExternalResourceRef: "workspace://f1/git:published-f1"})
	if err != nil || completed.Status != "confirmed" || completed.Version != 5 || completed.Tool.Status != statemachine.ToolCallSucceeded || completed.Replayed {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	completedReplay, err := store.CompleteWorkspacePublish(ctx, completeCommand, EffectCompletion{ExternalResourceRef: "workspace://f1/git:published-f1"})
	if err != nil || !completedReplay.Replayed || completedReplay.EventID != completed.EventID || completedReplay.Version != completed.Version {
		t.Fatalf("completion replay=%#v err=%v", completedReplay, err)
	}
	if _, err = store.CompleteWorkspacePublish(ctx, completeCommand, EffectCompletion{ExternalResourceRef: "workspace://f1/substituted"}); !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("conflicting completion replay error=%v", err)
	}

	var revisionStatus, toolStatus, effectStatus, jobStatus string
	var revisionVersion, toolVersion, revisionEvents, publishEvents int
	err = admin.QueryRow(ctx, `SELECT w.status,w.version,t.status,t.tool_call_version,e.status,j.status,(SELECT count(*) FROM agent.events WHERE tenant_id=w.tenant_id AND aggregate_kind='workspace_revision' AND aggregate_id=w.id),(SELECT count(*) FROM agent.outbox WHERE tenant_id=w.tenant_id AND aggregate_kind='workspace_revision' AND aggregate_id=w.id AND command_type='events.publish') FROM agent.workspace_revision_commits w JOIN agent.tool_calls t ON t.tenant_id=w.tenant_id AND t.id=w.tool_call_id JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id JOIN agent.jobs j ON j.tenant_id=t.tenant_id AND j.command_id=w.commit_command_id WHERE w.tenant_id=$1 AND w.id=$2`, tenantID, prepare.RevisionID).Scan(&revisionStatus, &revisionVersion, &toolStatus, &toolVersion, &effectStatus, &jobStatus, &revisionEvents, &publishEvents)
	if err != nil {
		t.Fatal(err)
	}
	if revisionStatus != "confirmed" || revisionVersion != 5 || toolStatus != "succeeded" || toolVersion != int(publish.Tool.ToolCallVersion+1) || effectStatus != "confirmed" || jobStatus != "succeeded" || revisionEvents != 4 || publishEvents != 4 {
		t.Fatalf("revision=%s/v%d tool=%s/v%d effect=%s job=%s events=%d/%d", revisionStatus, revisionVersion, toolStatus, toolVersion, effectStatus, jobStatus, revisionEvents, publishEvents)
	}
}
