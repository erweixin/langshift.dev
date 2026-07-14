//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/security/opaque"
)

type unknownRepairFixture struct {
	store                                                                        RunStore
	tenantID, targetUserID, initiatorID                                          string
	approverOneID, approverTwoID, initiatorSessionID, sessionOneID, sessionTwoID string
	runID, toolCallID, effectID, effectKey, correlationID                        string
	toolVersion, effectVersion                                                   uint64
	now                                                                          time.Time
}

func TestToolEffectRepairExecutesEveryResolutionExactlyOnce(t *testing.T) {
	tests := []struct {
		name, prefix, resolution, targetStatus, effectStatus, residualRisk, externalResource string
		toolAdvance                                                                          uint64
	}{
		{"confirmed occurred", "da", "confirmed_occurred", "succeeded", "confirmed", "", "provider://resource/repair", 2},
		{"confirmed not occurred", "db", "confirmed_not_occurred", "failed", "failed", "", "", 2},
		{"accepted unknown", "dc", "accepted_unknown", "resolved_unknown", "accepted_unknown", "encrypted://risk/accepted", "", 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
			defer admin.Close()
			pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
			defer pool.Close()
			fixture := prepareUnknownRepairFixture(t, ctx, admin, pool, test.prefix)
			repairID := fixtureID(test.prefix, 30)
			proposalHash := "proposal-" + test.prefix
			evidenceHash := "evidence-" + test.prefix
			proposal := ProposeToolEffectRepairCommand{RepairID: repairID, TenantID: fixture.tenantID, InitiatorUserID: fixture.initiatorID, InitiatorSessionID: fixture.initiatorSessionID, ToolCallID: fixture.toolCallID, ExpectedToolVersion: fixture.toolVersion, ExpectedEffectVersion: fixture.effectVersion, EffectKey: fixture.effectKey, Resolution: test.resolution, ProposalHash: proposalHash, EvidenceHash: evidenceHash, ResidualRiskRef: test.residualRisk, ExpiresAt: fixture.now.Add(30 * time.Minute), Actor: json.RawMessage(`{"kind":"user","role":"admin"}`), CorrelationID: fixture.correlationID, ProposedEvent: repairPointer(test.prefix, "proposed")}
			proposed, err := fixture.store.ProposeToolEffectRepair(ctx, proposal)
			if err != nil || proposed.Status != "proposed" || proposed.Version != 1 || proposed.Replayed {
				t.Fatalf("proposed=%#v err=%v", proposed, err)
			}
			firstCommand := repairDecisionCommand(fixture, repairID, fixture.approverOneID, fixture.sessionOneID, fixtureID(test.prefix, 31), proposalHash, "approve", test.prefix+"-one")
			first, err := fixture.store.DecideToolEffectRepair(ctx, firstCommand)
			if err != nil || first.Status != "proposed" || first.Version != 1 || first.ApprovalCount != 1 || first.Ready {
				t.Fatalf("first decision=%#v err=%v", first, err)
			}
			secondCommand := repairDecisionCommand(fixture, repairID, fixture.approverTwoID, fixture.sessionTwoID, fixtureID(test.prefix, 32), proposalHash, "approve", test.prefix+"-two")
			second, err := fixture.store.DecideToolEffectRepair(ctx, secondCommand)
			if err != nil || second.Status != "approved" || second.Version != 2 || second.ApprovalCount != 2 || !second.Ready {
				t.Fatalf("second decision=%#v err=%v", second, err)
			}

			execute := ExecuteToolEffectRepairCommand{RepairID: repairID, TenantID: fixture.tenantID, ProposalHash: proposalHash, EvidenceHash: evidenceHash, ExpectedRepairVersion: 2, ExpectedToolVersion: fixture.toolVersion, ExpectedEffectVersion: fixture.effectVersion, ExternalResourceRef: test.externalResource, Actor: json.RawMessage(`{"kind":"service","name":"repair-control-plane"}`), CorrelationID: fixture.correlationID, ManuallyResolvedEvent: repairPointer(test.prefix, "manually-resolved"), ToolCompletedEvent: repairPointer(test.prefix, "tool-completed"), RepairExecutedEvent: repairPointer(test.prefix, "executed"), GroupJoinedEvent: repairPointer(test.prefix, "group-joined"), RunResumeQueuedEvent: repairPointer(test.prefix, "resume-queued"), ResumeCommand: repairPointer(test.prefix, "resume-command"), ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 2, ResumeMaxAttempts: 5}
			const contenders = 32
			var wait sync.WaitGroup
			results := make(chan RepairedTool, contenders)
			errorsFound := make(chan error, contenders)
			for range contenders {
				wait.Add(1)
				go func() {
					defer wait.Done()
					result, executeErr := fixture.store.ExecuteToolEffectRepair(ctx, execute)
					if executeErr != nil {
						errorsFound <- executeErr
						return
					}
					results <- result
				}()
			}
			wait.Wait()
			close(results)
			close(errorsFound)
			for executeErr := range errorsFound {
				t.Fatalf("concurrent execute error=%v", executeErr)
			}
			fresh, replayed := 0, 0
			var durable RepairedTool
			for result := range results {
				durable = result
				if result.Replayed {
					replayed++
				} else {
					fresh++
				}
			}
			if fresh != 1 || replayed != contenders-1 || durable.Status != test.targetStatus || durable.Resolution != test.resolution || durable.ToolCallVersion != fixture.toolVersion+test.toolAdvance || durable.EffectVersion != fixture.effectVersion+1 || !durable.Resumed || durable.RunVersion != 5 {
				t.Fatalf("durable=%#v fresh=%d replayed=%d", durable, fresh, replayed)
			}

			var repairStatus, toolStatus, effectStatus, risk, resultEvent string
			var repairVersion, toolVersion, effectVersion, cancelledReconcileJobs, manualEvents, terminalEvents int
			var joined bool
			err = admin.QueryRow(ctx, `SELECT r.status,r.version,t.status,t.tool_call_version,e.status,e.version,COALESCE(e.residual_risk_ref,''),t.result_event_id::text,g.joined,(SELECT count(*) FROM agent.jobs j JOIN agent.outbox o ON o.tenant_id=j.tenant_id AND o.command_id=j.command_id WHERE o.tenant_id=$1 AND o.aggregate_kind='tool_call' AND o.aggregate_id=$2 AND o.command_type='ReconcileToolEffect' AND j.status='cancelled'),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='tool_call' AND aggregate_id=$2 AND event_type='ToolCallManuallyResolved'),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='tool_call' AND aggregate_id=$2 AND event_type IN ('ToolCallSucceeded','ToolCallFailed')) FROM agent.repair_commands r JOIN agent.tool_calls t ON t.tenant_id=r.tenant_id AND t.id=r.target_id JOIN agent.tool_effects e ON e.tenant_id=t.tenant_id AND e.tool_call_id=t.id JOIN agent.parallel_group_members m ON m.tenant_id=t.tenant_id AND m.tool_call_id=t.id JOIN agent.parallel_groups g ON g.tenant_id=m.tenant_id AND g.id=m.group_id WHERE r.tenant_id=$1 AND r.id=$3`, fixture.tenantID, fixture.toolCallID, repairID).Scan(&repairStatus, &repairVersion, &toolStatus, &toolVersion, &effectStatus, &effectVersion, &risk, &resultEvent, &joined, &cancelledReconcileJobs, &manualEvents, &terminalEvents)
			if err != nil {
				t.Fatal(err)
			}
			wantTerminalEvents := 1
			if test.resolution == "accepted_unknown" {
				wantTerminalEvents = 0
			}
			if repairStatus != "executed" || repairVersion != 3 || toolStatus != test.targetStatus || toolVersion != int(fixture.toolVersion+test.toolAdvance) || effectStatus != test.effectStatus || effectVersion != int(fixture.effectVersion+1) || risk != test.residualRisk || resultEvent == "" || !joined || cancelledReconcileJobs != 1 || manualEvents != 1 || terminalEvents != wantTerminalEvents {
				t.Fatalf("repair=%s/v%d tool=%s/v%d effect=%s/v%d risk=%q event=%s joined=%v cancelled=%d events=%d/%d", repairStatus, repairVersion, toolStatus, toolVersion, effectStatus, effectVersion, risk, resultEvent, joined, cancelledReconcileJobs, manualEvents, terminalEvents)
			}
			replayedProposal, err := fixture.store.ProposeToolEffectRepair(ctx, proposal)
			if err != nil || !replayedProposal.Replayed || replayedProposal.Status != "executed" || replayedProposal.Version != 3 {
				t.Fatalf("proposal terminal replay=%#v err=%v", replayedProposal, err)
			}
			replayedDecision, err := fixture.store.DecideToolEffectRepair(ctx, firstCommand)
			if err != nil || replayedDecision.Status != "executed" || replayedDecision.Version != 3 || replayedDecision.ApprovalCount != 2 || !replayedDecision.Ready {
				t.Fatalf("decision terminal replay=%#v err=%v", replayedDecision, err)
			}
		})
	}
}

func TestToolEffectRepairRejectionIsTerminalAndAudited(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	fixture := prepareUnknownRepairFixture(t, ctx, admin, pool, "dd")
	repairID := fixtureID("dd", 30)
	proposal := ProposeToolEffectRepairCommand{RepairID: repairID, TenantID: fixture.tenantID, InitiatorUserID: fixture.initiatorID, InitiatorSessionID: fixture.initiatorSessionID, ToolCallID: fixture.toolCallID, ExpectedToolVersion: fixture.toolVersion, ExpectedEffectVersion: fixture.effectVersion, EffectKey: fixture.effectKey, Resolution: "confirmed_occurred", ProposalHash: "proposal-dd", EvidenceHash: "evidence-dd", ExpiresAt: fixture.now.Add(30 * time.Minute), Actor: json.RawMessage(`{"kind":"user","role":"admin"}`), CorrelationID: fixture.correlationID, ProposedEvent: repairPointer("dd", "proposed")}
	if _, err := fixture.store.ProposeToolEffectRepair(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	reject := repairDecisionCommand(fixture, repairID, fixture.approverOneID, fixture.sessionOneID, fixtureID("dd", 31), proposal.ProposalHash, "reject", "dd-reject")
	reject.RepairApprovedEvent = PayloadPointer{}
	rejected, err := fixture.store.DecideToolEffectRepair(ctx, reject)
	if err != nil || rejected.Status != "rejected" || rejected.Version != 2 || rejected.Ready {
		t.Fatalf("rejected=%#v err=%v", rejected, err)
	}
	replay, err := fixture.store.DecideToolEffectRepair(ctx, reject)
	if err != nil || replay.Status != "rejected" || replay.Version != 2 || replay.ApprovalCount != 0 || replay.Ready {
		t.Fatalf("rejection replay=%#v err=%v", replay, err)
	}
	approve := repairDecisionCommand(fixture, repairID, fixture.approverTwoID, fixture.sessionTwoID, fixtureID("dd", 32), proposal.ProposalHash, "approve", "dd-late-approve")
	if _, err = fixture.store.DecideToolEffectRepair(ctx, approve); !errors.Is(err, ErrRepairNotActionable) {
		t.Fatalf("late approval error=%v", err)
	}
	var status, aggregateKind, eventType, decision, toolStatus, effectStatus string
	var version, aggregateVersion int
	if err = admin.QueryRow(ctx, `SELECT r.status,r.version,e.aggregate_kind,e.aggregate_version,e.event_type,a.decision,t.status,te.status FROM agent.repair_commands r JOIN agent.repair_approvals a ON a.tenant_id=r.tenant_id AND a.repair_command_id=r.id JOIN agent.events e ON e.id=a.approval_event_id JOIN agent.tool_calls t ON t.tenant_id=r.tenant_id AND t.id=r.target_id JOIN agent.tool_effects te ON te.tenant_id=t.tenant_id AND te.tool_call_id=t.id WHERE r.tenant_id=$1 AND r.id=$2`, fixture.tenantID, repairID).Scan(&status, &version, &aggregateKind, &aggregateVersion, &eventType, &decision, &toolStatus, &effectStatus); err != nil {
		t.Fatal(err)
	}
	if status != "rejected" || version != 2 || aggregateKind != "repair_command" || aggregateVersion != 2 || eventType != "ApprovalRejected" || decision != "reject" || toolStatus != "outcome_unknown" || effectStatus != "outcome_unknown" {
		t.Fatalf("repair=%s/v%d event=%s/v%d/%s decision=%s target=%s/%s", status, version, aggregateKind, aggregateVersion, eventType, decision, toolStatus, effectStatus)
	}
	proposalReplay, err := fixture.store.ProposeToolEffectRepair(ctx, proposal)
	if err != nil || !proposalReplay.Replayed || proposalReplay.Status != "rejected" || proposalReplay.Version != 2 {
		t.Fatalf("rejected proposal replay=%#v err=%v", proposalReplay, err)
	}
}

func prepareUnknownRepairFixture(t *testing.T, ctx context.Context, admin, pool *pgxpool.Pool, prefix string) unknownRepairFixture {
	t.Helper()
	targetUserID, initiatorID := fixtureID(prefix, 1), fixtureID(prefix, 2)
	approverOneID, approverTwoID := fixtureID(prefix, 3), fixtureID(prefix, 4)
	tenantID := fixtureID(prefix, 5)
	sessionOneID, sessionTwoID := fixtureID(prefix, 6), fixtureID(prefix, 7)
	initiatorSessionID := fixtureID(prefix, 15)
	runID, conversationID := fixtureID(prefix, 8), fixtureID(prefix, 9)
	correlationID, storeEpoch := fixtureID(prefix, 10), fixtureID(prefix, 11)
	now := time.Date(2026, time.July, 15, 3, int(prefix[1]-'a'), 0, 0, time.UTC)
	users := []struct{ id, email string }{
		{targetUserID, prefix + "-target@example.invalid"},
		{initiatorID, prefix + "-initiator@example.invalid"},
		{approverOneID, prefix + "-approver-one@example.invalid"},
		{approverTwoID, prefix + "-approver-two@example.invalid"},
	}
	for _, user := range users {
		if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,$2,'en','active')`, user.id, user.email); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'enterprise',$2,'active','US',$3)`, tenantID, "Repair "+prefix, targetUserID); err != nil {
		t.Fatal(err)
	}
	members := []struct{ id, userID, role string }{
		{fixtureID(prefix, 12), initiatorID, "admin"},
		{fixtureID(prefix, 13), approverOneID, "owner"},
		{fixtureID(prefix, 14), approverTwoID, "admin"},
	}
	for _, member := range members {
		if _, err := admin.Exec(ctx, `INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,$4,'active',$5)`, member.id, tenantID, member.userID, member.role, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	sessions := []struct {
		id, userID string
		marker     byte
	}{{initiatorSessionID, initiatorID, prefix[1] + 32}, {sessionOneID, approverOneID, prefix[1]}, {sessionTwoID, approverTwoID, prefix[1] + 16}}
	for _, session := range sessions {
		if _, err := admin.Exec(ctx, `INSERT INTO identity.sessions(id,user_id,active_tenant_id,token_hash,csrf_secret_hash,ip_hash,user_agent_hash,last_seen_at,expires_at,reauthenticated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$8)`, session.id, session.userID, tenantID, bytes.Repeat([]byte{session.marker}, 32), bytes.Repeat([]byte{session.marker + 1}, 32), bytes.Repeat([]byte{session.marker + 2}, 32), bytes.Repeat([]byte{session.marker + 3}, 32), now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	store := RunStore{Pool: pool, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, IDKey: bytes.Repeat([]byte{prefix[1]}, 32), StoreEpoch: storeEpoch, Now: func() time.Time { return now }, Epochs: executionEpochStub{epoch: storeEpoch}, Tokens: opaque.Manager{Purpose: "repair-tool-claim", Pepper: bytes.Repeat([]byte{prefix[1] + 1}, 32)}, LeaseTTL: 2 * time.Minute}
	accepted, err := store.Accept(ctx, AcceptRunCommand{RunID: runID, TenantID: tenantID, UserID: targetUserID, ConversationID: conversationID, CorrelationID: correlationID, DueAt: now.Add(time.Hour), ProfileSnapshotID: "repair@sha256:" + prefix, BudgetSnapshot: json.RawMessage(`{"max_steps":8}`), Actor: json.RawMessage(`{"kind":"user"}`), AcceptedEvent: repairPointer(prefix, "run-accepted"), QueuedEvent: repairPointer(prefix, "run-queued"), StartCommand: repairPointer(prefix, "start-command"), QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 2, MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	runClaim, err := store.ClaimStart(ctx, ClaimRunCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: accepted.StartCommandID, CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: runID, PayloadRef: repairPointer(prefix, "start-command").Ref, PayloadHash: repairPointer(prefix, "start-command").Hash}, ConsumerName: "repair-agent-worker", WorkerID: "repair-agent-" + prefix, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, RunEvent: repairPointer(prefix, "run-started"), AttemptStartedEvent: repairPointer(prefix, "run-attempt-started"), AttemptExpiredEvent: repairPointer(prefix, "run-attempt-expired")})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := store.RequestTools(ctx, RequestToolsCommand{Claim: runClaim, ExpectedRunVersion: runClaim.RunVersion, StepID: "repair-" + prefix, JoinPolicy: "all", QuorumCount: 1, PlanResultHash: "plan-" + prefix, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, AttemptCompletedEvent: repairPointer(prefix, "run-attempt-completed"), ToolRequests: []ToolRequest{{ToolName: "provider_write", DescriptorSnapshotID: "provider_write@sha256:" + prefix, NormalizedInputRef: repairPointer(prefix, "input").Ref, RequestHash: "request-" + prefix, EffectClass: "reconcilable_write", EffectKey: "effect-" + prefix, EffectScope: "tenant:repair:" + prefix, ProviderID: "repair-provider", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 60, CostUnits: 1, MaxAttempts: 5, RequestedEvent: repairPointer(prefix, "tool-requested"), ExecuteCommand: repairPointer(prefix, "execute-tool")}}})
	if err != nil {
		t.Fatal(err)
	}
	tool := requested.ToolCalls[0]
	claim, err := store.ClaimTool(ctx, ClaimToolCommand{Command: eventpostgres.DeliveredCommand{TenantID: tenantID, StoreEpoch: storeEpoch, CommandID: tool.CommandID, CommandType: "ExecuteToolCall", AggregateKind: "tool_call", AggregateID: tool.ToolCallID, PayloadRef: repairPointer(prefix, "execute-tool").Ref, PayloadHash: repairPointer(prefix, "execute-tool").Hash}, ConsumerName: "repair-tool-worker", WorkerID: "repair-tool-" + prefix, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolStartedEvent: repairPointer(prefix, "tool-started"), AttemptStartedEvent: repairPointer(prefix, "tool-attempt-started"), AttemptExpiredEvent: repairPointer(prefix, "tool-attempt-expired")})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := store.CompleteEffectTool(ctx, CompleteToolCommand{Claim: claim, ExpectedToolVersion: claim.ToolCallVersion, TargetState: statemachine.ToolCallOutcomeUnknown, ResultHash: "unknown-" + prefix, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: correlationID, ToolCompletedEvent: repairPointer(prefix, "outcome-unknown"), AttemptCompletedEvent: repairPointer(prefix, "tool-attempt-completed"), GroupJoinedEvent: repairPointer(prefix, "unused-group"), RunResumeQueuedEvent: repairPointer(prefix, "unused-resume"), ResumeCommand: repairPointer(prefix, "unused-command"), ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 1, ResumeMaxAttempts: 5}, EffectCompletion{ReconciliationDueAt: now.Add(10 * time.Minute), ReconcileCommand: repairPointer(prefix, "reconcile-command"), ReconcileQueueClass: "background", ReconcileResource: "tool-reconciliation", ReconcilePriority: 40, ReconcileCostUnits: 1, ReconcileAttempts: 8})
	if err != nil || completed.Status != statemachine.ToolCallOutcomeUnknown || completed.Resumed {
		t.Fatalf("outcome unknown=%#v err=%v", completed, err)
	}
	var effectID, effectKey string
	var effectVersion uint64
	if err = admin.QueryRow(ctx, `SELECT id::text,effect_key,version FROM agent.tool_effects WHERE tenant_id=$1 AND tool_call_id=$2 AND status='outcome_unknown'`, tenantID, tool.ToolCallID).Scan(&effectID, &effectKey, &effectVersion); err != nil {
		t.Fatal(err)
	}
	return unknownRepairFixture{store: store, tenantID: tenantID, targetUserID: targetUserID, initiatorID: initiatorID, approverOneID: approverOneID, approverTwoID: approverTwoID, initiatorSessionID: initiatorSessionID, sessionOneID: sessionOneID, sessionTwoID: sessionTwoID, runID: runID, toolCallID: tool.ToolCallID, effectID: effectID, effectKey: effectKey, correlationID: correlationID, toolVersion: completed.ToolCallVersion, effectVersion: effectVersion, now: now}
}

func repairDecisionCommand(fixture unknownRepairFixture, repairID, approverID, sessionID, approvalID, proposalHash, decision, suffix string) DecideRepairCommand {
	return DecideRepairCommand{RepairID: repairID, TenantID: fixture.tenantID, ApproverUserID: approverID, SessionID: sessionID, ApprovalID: approvalID, Decision: decision, ProposalHash: proposalHash, ExpectedRepairVersion: 1, ExpectedToolVersion: fixture.toolVersion, ExpectedEffectVersion: fixture.effectVersion, PermissionSnapshot: "permission-" + suffix, ReauthenticatedAt: fixture.now, Actor: json.RawMessage(`{"kind":"user","role":"admin"}`), CorrelationID: fixture.correlationID, DecisionEvent: repairPointer(suffix, "decision"), RepairApprovedEvent: repairPointer(suffix, "approved")}
}

func fixtureID(prefix string, suffix int) string {
	return fmt.Sprintf("%s000000-0000-4000-8000-%012d", prefix, suffix)
}

func repairPointer(prefix, name string) PayloadPointer {
	return PayloadPointer{Ref: "encrypted://repair/" + prefix + "/" + name, Hash: prefix + "-" + name}
}
