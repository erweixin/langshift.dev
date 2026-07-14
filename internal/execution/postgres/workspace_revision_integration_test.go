//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestWorkspaceRevisionPreparationAndAuthorizationAreContentBound(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	fixture := prepareApprovalFixture(t, ctx, admin, pool, "e7")

	var preparingVersion uint64
	if err := admin.QueryRow(ctx, `UPDATE agent.tool_calls SET status='preparing_approval',tool_call_version=tool_call_version+1,active_command_id=(SELECT command_id FROM agent.outbox WHERE tenant_id=$1 AND aggregate_kind='tool_call' AND aggregate_id=$2 AND command_type='ExecuteToolCall' ORDER BY created_at LIMIT 1),active_attempt_id=(SELECT execution_attempt_id FROM agent.tool_effects WHERE tenant_id=$1 AND tool_call_id=$2),current_fence=current_fence+1,lease_token_hash=decode(repeat('ab',32),'hex'),lease_expires_at=$3,updated_at=$4 WHERE tenant_id=$1 AND id=$2 RETURNING tool_call_version`, fixture.tenantID, fixture.toolCallID, fixture.now.Add(2*time.Minute), fixture.now).Scan(&preparingVersion); err != nil {
		t.Fatal(err)
	}
	proposalHash := "workspace-proposal-e7"
	prepare := PrepareWorkspaceRevisionCommand{
		RevisionID: fixtureID("e7", 51), TenantID: fixture.tenantID, ToolCallID: fixture.toolCallID,
		WorkspaceID: fixtureID("e7", 50), ExpectedToolVersion: preparingVersion,
		BaseRevision: "git:base-e7", PreparedRevision: "git:prepared-e7", PreparedHash: "sha256:prepared-e7",
		EffectKey: fixture.effectKey, ProposalHash: proposalHash,
		Actor: json.RawMessage(`{"kind":"service","name":"workspace-preparer"}`), CorrelationID: fixture.correlationID,
		PreparedEvent: repairPointer("e7", "workspace-prepared"),
	}
	prepared, err := fixture.store.PrepareWorkspaceRevision(ctx, prepare)
	if err != nil || prepared.Status != "prepared" || prepared.Version != 1 || prepared.Replayed || prepared.PreparedHash != prepare.PreparedHash {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}

	if err = admin.QueryRow(ctx, `UPDATE agent.tool_calls SET status='awaiting_approval',tool_call_version=tool_call_version+1,active_command_id=NULL,active_attempt_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,updated_at=$3 WHERE tenant_id=$1 AND id=$2 RETURNING tool_call_version`, fixture.tenantID, fixture.toolCallID, fixture.now.Add(time.Second)).Scan(&fixture.toolVersion); err != nil {
		t.Fatal(err)
	}
	prepareReplay, err := fixture.store.PrepareWorkspaceRevision(ctx, prepare)
	if err != nil || !prepareReplay.Replayed || prepareReplay.Version != 1 || prepareReplay.PreparedHash != prepare.PreparedHash {
		t.Fatalf("prepare replay=%#v err=%v", prepareReplay, err)
	}
	conflictingPrepare := prepare
	conflictingPrepare.ProposalHash = "workspace-proposal-substituted"
	if _, err = fixture.store.PrepareWorkspaceRevision(ctx, conflictingPrepare); !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("conflicting prepare error=%v", err)
	}

	request := RequestApprovalCommand{
		ApprovalID: fixtureID("e7", 54), TenantID: fixture.tenantID, RunID: fixture.runID,
		ToolCallID: fixture.toolCallID, ApprovalKind: "tool_execution", ProposalHash: proposalHash,
		PermissionSnapshot: fixture.permissionSnapshot, TargetVersion: fixture.toolVersion,
		RequestedBy: fixture.targetUserID, ExpiresAt: fixture.now.Add(20 * time.Minute),
		Actor: json.RawMessage(`{"kind":"service","name":"approval-control-plane"}`), CorrelationID: fixture.correlationID,
		RequestedEvent: repairPointer("e7", "approval-requested"),
	}
	if _, err = fixture.store.RequestApproval(ctx, request); err != nil {
		t.Fatal(err)
	}
	decision := DecideApprovalCommand{
		ApprovalID: request.ApprovalID, TenantID: fixture.tenantID, DecisionID: fixtureID("e7", 55),
		ActorUserID: fixture.approverOneID, SessionID: fixture.sessionOneID, Decision: "approve", Mode: "admin",
		ProposalHash: proposalHash, PermissionSnapshot: fixture.permissionSnapshot,
		ExpectedApprovalVersion: 1, TargetVersion: fixture.toolVersion, ReauthenticatedAt: fixture.now,
		Actor: json.RawMessage(`{"kind":"user","role":"owner"}`), CorrelationID: fixture.correlationID,
		DecisionEvent: repairPointer("e7", "approval-granted"),
	}
	granted, err := fixture.store.DecideApproval(ctx, decision)
	if err != nil || granted.Status != "granted" || granted.Version != 2 || granted.EventID == "" {
		t.Fatalf("granted=%#v err=%v", granted, err)
	}
	authorize := AuthorizeWorkspaceRevisionCommand{
		RevisionID: prepare.RevisionID, TenantID: fixture.tenantID, ApprovalID: request.ApprovalID,
		ApprovalEventID: granted.EventID, ProposalHash: proposalHash, PermissionSnapshot: fixture.permissionSnapshot,
		ExpectedRevisionVersion: 1, ExpectedApprovalVersion: 2, ExpectedToolVersion: fixture.toolVersion,
		QueueClass: "interactive", ResourceClass: "workspace-publisher", Priority: 80, CostUnits: 2, MaxAttempts: 5,
		Actor: json.RawMessage(`{"kind":"service","name":"workspace-control-plane"}`), CorrelationID: fixture.correlationID,
		AuthorizedEvent: repairPointer("e7", "workspace-authorized"), ToolRequestedEvent: repairPointer("e7", "commit-requested"),
		ExecuteCommand: repairPointer("e7", "commit-command"),
	}
	invalid := authorize
	invalid.PermissionSnapshot = "membership:substituted:v99:role:owner"
	if _, err = fixture.store.AuthorizeWorkspaceRevision(ctx, invalid); !errors.Is(err, ErrWorkspaceAuthorization) {
		t.Fatalf("invalid authorization error=%v", err)
	}
	var unchangedStatus string
	var unchangedVersion uint64
	if err = admin.QueryRow(ctx, `SELECT status,version FROM agent.workspace_revision_commits WHERE tenant_id=$1 AND id=$2`, fixture.tenantID, prepare.RevisionID).Scan(&unchangedStatus, &unchangedVersion); err != nil || unchangedStatus != "prepared" || unchangedVersion != 1 {
		t.Fatalf("authorization failure changed revision: %s/v%d err=%v", unchangedStatus, unchangedVersion, err)
	}

	authorized, err := fixture.store.AuthorizeWorkspaceRevision(ctx, authorize)
	if err != nil || authorized.Status != "authorized" || authorized.Version != 2 || authorized.ToolVersion != fixture.toolVersion+1 || authorized.Replayed || authorized.CommitCommandID == "" || authorized.JobID == "" {
		t.Fatalf("authorized=%#v err=%v", authorized, err)
	}
	replay, err := fixture.store.AuthorizeWorkspaceRevision(ctx, authorize)
	if err != nil || !replay.Replayed || replay.AuthorizationEventID != authorized.AuthorizationEventID || replay.CommitCommandID != authorized.CommitCommandID || replay.JobID != authorized.JobID || replay.Version != authorized.Version || replay.ToolVersion != authorized.ToolVersion {
		t.Fatalf("authorization replay=%#v first=%#v err=%v", replay, authorized, err)
	}

	var revisionStatus, storedProposal, toolStatus, pendingCommand, jobStatus string
	var revisionVersion, toolVersion, revisionEvents, executeCommands, publishCommands int
	err = admin.QueryRow(ctx, `SELECT w.status,w.version,w.proposal_hash,t.status,t.tool_call_version,t.pending_command_id::text,j.status,(SELECT count(*) FROM agent.events WHERE tenant_id=w.tenant_id AND aggregate_kind='workspace_revision' AND aggregate_id=w.id),(SELECT count(*) FROM agent.outbox WHERE tenant_id=w.tenant_id AND command_id=w.commit_command_id AND command_type='ExecuteToolCall'),(SELECT count(*) FROM agent.outbox WHERE tenant_id=w.tenant_id AND aggregate_kind='workspace_revision' AND aggregate_id=w.id AND command_type='events.publish') FROM agent.workspace_revision_commits w JOIN agent.tool_calls t ON t.tenant_id=w.tenant_id AND t.id=w.tool_call_id JOIN agent.jobs j ON j.tenant_id=w.tenant_id AND j.command_id=w.commit_command_id WHERE w.tenant_id=$1 AND w.id=$2`, fixture.tenantID, prepare.RevisionID).Scan(&revisionStatus, &revisionVersion, &storedProposal, &toolStatus, &toolVersion, &pendingCommand, &jobStatus, &revisionEvents, &executeCommands, &publishCommands)
	if err != nil {
		t.Fatal(err)
	}
	if revisionStatus != "authorized" || revisionVersion != 2 || storedProposal != proposalHash || toolStatus != "requested" || toolVersion != int(fixture.toolVersion+1) || pendingCommand != authorized.CommitCommandID || jobStatus != "pending" || revisionEvents != 2 || executeCommands != 1 || publishCommands != 2 {
		t.Fatalf("revision=%s/v%d proposal=%s tool=%s/v%d command=%s job=%s counts=%d/%d/%d", revisionStatus, revisionVersion, storedProposal, toolStatus, toolVersion, pendingCommand, jobStatus, revisionEvents, executeCommands, publishCommands)
	}
}
