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
	seedExecutionBehavior(t, ctx, admin, tenantID, userID, "artifact_builder", now)

	clock := now
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return clock }}, IDKey: bytes.Repeat([]byte{0x15}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return clock }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "workspace-publish", Pepper: bytes.Repeat([]byte{0x16}, 32)}, LeaseTTL: 2 * time.Minute, Behavior: integrationBehaviorResolver}
	accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: userID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(time.Hour), BehaviorProfile: "artifact_builder", BehaviorEnvironment: "production", BudgetSnapshot: json.RawMessage(`{"max_steps":8}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: repairPointer(prefix, "run-accepted"), QueuedEvent: repairPointer(prefix, "run-queued"), StartCommand: repairPointer(prefix, "start-run"), QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 2, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	runClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: repairPointer(prefix, "start-run").Ref, PayloadHash: repairPointer(prefix, "start-run").Hash}, ConsumerName: "workspace-agent", WorkerID: "workspace-agent-f1", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: repairPointer(prefix, "run-started"), AttemptStartedEvent: repairPointer(prefix, "run-attempt-started"), AttemptExpiredEvent: repairPointer(prefix, "run-attempt-expired")})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := store.RequestTools(ctx, RequestToolsCommand{Claim: runClaim, ExpectedRunVersion: runClaim.RunVersion, StepID: "workspace-f1", JoinPolicy: "all", QuorumCount: 1, PlanResultHash: "workspace-plan-f1", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: repairPointer(prefix, "run-attempt-completed"), ToolRequests: []ToolRequest{{ToolName: "workspace_publish", DescriptorSnapshotID: "workspace_publish@sha256:f1", NormalizedInputRef: repairPointer(prefix, "workspace-input").Ref, RequestHash: "workspace-request-f1", EffectClass: "reconcilable_write", EffectKey: "workspace:project:f1", EffectScope: "tenant:workspace:f1", ProviderID: "workspace-service", Required: true, QueueClass: "interactive", ResourceClass: "workspace-publisher", Priority: 80, CostUnits: 2, MaxAttempts: 5, RequiresPreview: true, RequestedEvent: repairPointer(prefix, "preview-requested"), PreviewCommand: repairPointer(prefix, "preview-command")}}})
	if err != nil {
		t.Fatal(err)
	}
	tool := requested.ToolCalls[0]

	previewClaim, err := store.ClaimToolPreview(ctx, ClaimToolPreviewCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: tool.CommandID, CommandType: "PrepareToolPreview", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: repairPointer(prefix, "preview-command").Ref, PayloadHash: repairPointer(prefix, "preview-command").Hash}, ConsumerName: "workspace-preview", WorkerID: "workspace-preview-f1", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, PreviewStartedEvent: repairPointer(prefix, "preview-started"), AttemptStartedEvent: repairPointer(prefix, "preview-attempt-started"), AttemptExpiredEvent: repairPointer(prefix, "preview-attempt-expired")})
	if err != nil {
		t.Fatal(err)
	}
	previewToolVersion := previewClaim.ToolCallVersion
	expiredPreview := previewClaim
	clock = previewClaim.LeaseExpiresAt
	previewClaim, err = store.ClaimToolPreview(ctx, ClaimToolPreviewCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: tool.CommandID, CommandType: "PrepareToolPreview", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: repairPointer(prefix, "preview-command").Ref, PayloadHash: repairPointer(prefix, "preview-command").Hash}, ConsumerName: "workspace-preview", WorkerID: "workspace-preview-f1-reclaim", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, PreviewStartedEvent: repairPointer(prefix, "preview-started"), AttemptStartedEvent: repairPointer(prefix, "preview-attempt-started"), AttemptExpiredEvent: repairPointer(prefix, "preview-attempt-expired")})
	if err != nil || previewClaim.Fence != expiredPreview.Fence+1 || previewClaim.AttemptID == expiredPreview.AttemptID || previewClaim.ToolCallVersion != previewToolVersion {
		t.Fatalf("preview reclaim=%#v err=%v", previewClaim, err)
	}
	if _, err = store.HeartbeatToolPreview(ctx, expiredPreview); !errors.Is(err, ErrExecutionRightConflict) {
		t.Fatalf("expired preview heartbeat error=%v", err)
	}
	clock = clock.Add(30 * time.Second)
	previewClaim, err = store.HeartbeatToolPreview(ctx, previewClaim)
	if err != nil || !previewClaim.LeaseExpiresAt.After(expiredPreview.LeaseExpiresAt) {
		t.Fatalf("preview heartbeat=%#v err=%v", previewClaim, err)
	}

	proposalHash := "workspace-proposal-f1"
	revisionID := fixtureID(prefix, 22)
	completePreview := CompleteWorkspacePreparationCommand{Claim: previewClaim, ExpectedToolVersion: previewToolVersion, RevisionID: revisionID, WorkspaceID: fixtureID(prefix, 23), BaseRevision: "git:base-f1", PreparedRevision: "git:prepared-f1", PreparedHash: "sha256:prepared-f1", ProposalHash: proposalHash, ApprovalID: fixtureID(prefix, 24), ApprovalExpiresAt: now.Add(20 * time.Minute), ResultHash: "workspace-preview-f1", Actor: json.RawMessage(`{"kind":"service","name":"workspace-preview"}`), CorrelationID: correlationID, PreparedEvent: repairPointer(prefix, "workspace-prepared"), ToolProposedEvent: repairPointer(prefix, "tool-proposed"), ApprovalRequested: repairPointer(prefix, "approval-requested"), RunWaitingApproval: repairPointer(prefix, "run-waiting-approval"), AttemptCompleted: repairPointer(prefix, "preview-attempt-completed"), NotifyApproval: repairPointer(prefix, "notify-approval"), NotifyQueueClass: "interactive", NotifyResourceClass: "notification", NotifyPriority: 70, NotifyCostUnits: 1, NotifyMaxAttempts: 8}
	prepared, err := store.CompleteWorkspacePreparation(ctx, completePreview)
	if err != nil || prepared.RevisionVersion != 1 || prepared.ToolVersion != previewToolVersion+1 || prepared.RunStatus != statemachine.RunWaitingApproval || prepared.NotificationCommandID == "" {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
	preparedReplay, err := store.CompleteWorkspacePreparation(ctx, completePreview)
	if err != nil || !preparedReplay.Replayed || preparedReplay.NotificationCommandID != prepared.NotificationCommandID {
		t.Fatalf("prepared replay=%#v err=%v", preparedReplay, err)
	}
	conflictingPreview := completePreview
	conflictingPreview.ResultHash = "workspace-preview-substituted-f1"
	if _, err = store.CompleteWorkspacePreparation(ctx, conflictingPreview); !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("conflicting preview completion error=%v", err)
	}
	awaitingVersion := prepared.ToolVersion
	permissionSnapshot := prepared.PermissionSnapshot
	decision := DecideApprovalCommand{ApprovalID: prepared.ApprovalID, TenantID: tenantID, DecisionID: fixtureID(prefix, 25), ActorUserID: userID, SessionID: sessionID, Decision: "approve", Mode: "admin", ProposalHash: proposalHash, PermissionSnapshot: permissionSnapshot, ExpectedApprovalVersion: 1, TargetVersion: awaitingVersion, ReauthenticatedAt: now, Actor: json.RawMessage(`{"kind":"user","role":"owner"}`), CorrelationID: correlationID, DecisionEvent: repairPointer(prefix, "approval-granted")}
	granted, err := store.DecideApproval(ctx, decision)
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := store.AuthorizeWorkspaceRevision(ctx, AuthorizeWorkspaceRevisionCommand{RevisionID: revisionID, TenantID: tenantID, ApprovalID: prepared.ApprovalID, ApprovalEventID: granted.EventID, ProposalHash: proposalHash, PermissionSnapshot: permissionSnapshot, ExpectedRevisionVersion: 1, ExpectedApprovalVersion: 2, ExpectedToolVersion: awaitingVersion, QueueClass: "interactive", ResourceClass: "workspace-publisher", Priority: 80, CostUnits: 2, MaxAttempts: 5, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AuthorizedEvent: repairPointer(prefix, "workspace-authorized"), ToolRequestedEvent: repairPointer(prefix, "commit-requested"), ExecuteCommand: repairPointer(prefix, "commit-command"), GroupAuthorizedEvent: repairPointer(prefix, "group-authorized"), RunWaitingToolEvent: repairPointer(prefix, "run-waiting-tool")})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimTool(ctx, ClaimToolCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: authorized.CommitCommandID, CommandType: "ExecuteToolCall", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: repairPointer(prefix, "commit-command").Ref, PayloadHash: repairPointer(prefix, "commit-command").Hash}, ConsumerName: "workspace-publisher", WorkerID: "workspace-publisher-f1", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolStartedEvent: repairPointer(prefix, "commit-tool-started"), AttemptStartedEvent: repairPointer(prefix, "commit-attempt-started"), AttemptExpiredEvent: repairPointer(prefix, "commit-attempt-expired")})
	if err != nil {
		t.Fatal(err)
	}
	beginCommand := BeginWorkspacePublishCommand{RevisionID: revisionID, ExpectedRevisionVersion: 2, Claim: claim, Actor: json.RawMessage(`{"kind":"service","name":"workspace-publisher"}`), CorrelationID: correlationID, StartedEvent: repairPointer(prefix, "workspace-publish-started")}
	publish, err := store.BeginWorkspacePublish(ctx, beginCommand)
	if err != nil || publish.Status != "publishing" || publish.Version != 3 || publish.Replayed {
		t.Fatalf("publish=%#v err=%v", publish, err)
	}
	replay, err := store.BeginWorkspacePublish(ctx, beginCommand)
	if err != nil || !replay.Replayed || replay.EventID != publish.EventID || replay.Version != publish.Version {
		t.Fatalf("begin replay=%#v err=%v", replay, err)
	}
	clock = clock.Add(30 * time.Second)
	publish, err = store.HeartbeatWorkspacePublish(ctx, publish)
	if err != nil || publish.Version != 4 || !publish.LeaseExpiresAt.After(claim.LeaseExpiresAt) {
		t.Fatalf("heartbeat=%#v err=%v", publish, err)
	}

	clock = publish.LeaseExpiresAt
	candidates, err := store.ListExpiredEffectCandidates(ctx, tenantID, storeEpoch, "", 10)
	if err != nil || len(candidates) != 1 || candidates[0].AttemptID != publish.AttemptID {
		t.Fatalf("expired workspace candidates=%#v err=%v", candidates, err)
	}
	reconcileDue := clock.Add(time.Minute)
	sweepEffect := SweepExpiredToolEffectCommand{Candidate: candidates[0], ResultHash: "workspace-publish-lease-expired-f1", Actor: json.RawMessage(`{"kind":"service","name":"workspace-sweeper"}`), CorrelationID: correlationID, OutcomeUnknownEvent: repairPointer(prefix, "tool-outcome-unknown"), AttemptExpiredEvent: repairPointer(prefix, "commit-attempt-expired"), Reconciliation: EffectCompletion{ReconciliationDueAt: reconcileDue, ReconcileCommand: repairPointer(prefix, "reconcile-workspace"), ReconcileQueueClass: "background", ReconcileResource: "tool-reconciliation", ReconcilePriority: 40, ReconcileCostUnits: 1, ReconcileAttempts: 8}}
	unknown, err := store.SweepExpiredWorkspacePublish(ctx, SweepExpiredWorkspacePublishCommand{RevisionID: revisionID, ExpectedRevisionVersion: publish.Version, Effect: sweepEffect, ObservedRevision: "git:base-f1", OutcomeUnknownEvent: repairPointer(prefix, "workspace-outcome-unknown"), Actor: json.RawMessage(`{"kind":"service","name":"workspace-sweeper"}`), CorrelationID: correlationID})
	if err != nil || unknown.Status != "outcome_unknown" || unknown.Version != 5 || unknown.Effect.ReconcileCommandID == "" {
		t.Fatalf("unknown=%#v err=%v", unknown, err)
	}

	clock = reconcileDue
	reconcileCommand := ClaimReconciliationCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: unknown.Effect.ReconcileCommandID, CommandType: "ReconcileToolEffect", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: repairPointer(prefix, "reconcile-workspace").Ref, PayloadHash: repairPointer(prefix, "reconcile-workspace").Hash}, ConsumerName: "workspace-reconciler", WorkerID: "workspace-reconciler-f1", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptStartedEvent: repairPointer(prefix, "reconcile-attempt-started"), AttemptExpiredEvent: repairPointer(prefix, "reconcile-attempt-expired")}
	workspaceReconcile, err := store.ClaimWorkspaceReconciliation(ctx, ClaimWorkspaceReconciliationCommand{RevisionID: revisionID, ExpectedRevisionVersion: unknown.Version, Reconciliation: reconcileCommand})
	if err != nil || workspaceReconcile.Version != 5 || workspaceReconcile.Claim.EffectVersion == 0 {
		t.Fatalf("workspace reconciliation claim=%#v err=%v", workspaceReconcile, err)
	}
	nextDue := clock.Add(time.Minute)
	deferCommand := DeferReconciliationCommand{Claim: workspaceReconcile.Claim, ExpectedEffectVersion: workspaceReconcile.Claim.EffectVersion, ResultHash: "workspace-still-unknown-f1", NextDueAt: nextDue, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: repairPointer(prefix, "reconcile-inconclusive"), NextReconcileCommand: repairPointer(prefix, "reconcile-workspace-next"), QueueClass: "background", ResourceClass: "tool-reconciliation", Priority: 40, CostUnits: 1, MaxAttempts: 8}
	deferred, err := store.DeferWorkspaceReconciliation(ctx, DeferWorkspaceReconciliationCommand{Workspace: workspaceReconcile, Reconciliation: deferCommand, ObservedRevision: "git:other-head-f1", DeferredEvent: repairPointer(prefix, "workspace-still-unknown"), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID})
	if err != nil || deferred.Status != "outcome_unknown" || deferred.Version != 6 || deferred.Reconciliation.NextCommandID == "" || !deferred.DueAt.Equal(nextDue) {
		t.Fatalf("workspace reconciliation deferred=%#v err=%v", deferred, err)
	}

	clock = nextDue
	retryCommand := ClaimReconciliationCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: deferred.Reconciliation.NextCommandID, CommandType: "ReconcileToolEffect", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: repairPointer(prefix, "reconcile-workspace-next").Ref, PayloadHash: repairPointer(prefix, "reconcile-workspace-next").Hash}, ConsumerName: "workspace-reconciler", WorkerID: "workspace-reconciler-f1-retry", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptStartedEvent: repairPointer(prefix, "reconcile-retry-started"), AttemptExpiredEvent: repairPointer(prefix, "reconcile-retry-expired")}
	retry, err := store.ClaimWorkspaceReconciliation(ctx, ClaimWorkspaceReconciliationCommand{RevisionID: revisionID, ExpectedRevisionVersion: deferred.Version, Reconciliation: retryCommand})
	if err != nil || retry.Version != 6 || retry.Claim.EffectVersion != deferred.Reconciliation.EffectVersion {
		t.Fatalf("workspace reconciliation retry=%#v err=%v", retry, err)
	}
	resolve := CompleteReconciliationCommand{Claim: retry.Claim, ExpectedToolVersion: retry.Claim.ToolCallVersion, ExpectedEffectVersion: retry.Claim.EffectVersion, TargetState: statemachine.ToolCallSucceeded, ResultHash: "workspace-confirmed-f1", ExternalResourceRef: "workspace://f1/git:published-f1", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolCompletedEvent: repairPointer(prefix, "tool-succeeded"), AttemptCompletedEvent: repairPointer(prefix, "reconcile-attempt-completed"), GroupJoinedEvent: repairPointer(prefix, "group-joined"), RunResumeQueuedEvent: repairPointer(prefix, "run-resume-queued"), ResumeCommand: repairPointer(prefix, "resume-command"), ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 1, ResumeMaxAttempts: 5}
	resolved, err := store.CompleteWorkspaceReconciliation(ctx, CompleteWorkspaceReconciliationCommand{Workspace: retry, Reconciliation: resolve, PublishedRevision: "git:published-f1", PublishedHash: "sha256:published-f1", ObservedRevision: "git:published-f1", CompletionEvent: repairPointer(prefix, "workspace-committed")})
	if err != nil || resolved.Status != "confirmed" || resolved.Version != 7 || resolved.Tool.Status != statemachine.ToolCallSucceeded {
		t.Fatalf("workspace reconciliation resolved=%#v err=%v", resolved, err)
	}

	var revisionStatus, toolStatus, effectStatus, jobStatus string
	var revisionVersion, toolVersion, revisionEvents, publishEvents, reconciliationAttempts int
	err = admin.QueryRow(ctx, `SELECT w.status,w.version,t.status,t.tool_call_version,e.status,j.status,w.reconciliation_attempts,(SELECT count(*) FROM agent.events WHERE tenant_id=w.tenant_id AND aggregate_kind='workspace_revision' AND aggregate_id=w.id),(SELECT count(*) FROM agent.outbox WHERE tenant_id=w.tenant_id AND aggregate_kind='workspace_revision' AND aggregate_id=w.id AND command_type='events.publish') FROM agent.workspace_revision_commits w JOIN agent.tool_calls t ON t.tenant_id=w.tenant_id AND t.id=w.tool_call_id JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id JOIN agent.jobs j ON j.tenant_id=t.tenant_id AND j.command_id=w.commit_command_id WHERE w.tenant_id=$1 AND w.id=$2`, tenantID, revisionID).Scan(&revisionStatus, &revisionVersion, &toolStatus, &toolVersion, &effectStatus, &jobStatus, &reconciliationAttempts, &revisionEvents, &publishEvents)
	if err != nil {
		t.Fatal(err)
	}
	if revisionStatus != "confirmed" || revisionVersion != 7 || toolStatus != "succeeded" || toolVersion != int(publish.Tool.ToolCallVersion+2) || effectStatus != "confirmed" || jobStatus != "succeeded" || reconciliationAttempts != 2 || revisionEvents != 6 || publishEvents != 6 {
		t.Fatalf("revision=%s/v%d tool=%s/v%d effect=%s job=%s reconciliations=%d events=%d/%d", revisionStatus, revisionVersion, toolStatus, toolVersion, effectStatus, jobStatus, reconciliationAttempts, revisionEvents, publishEvents)
	}
}
