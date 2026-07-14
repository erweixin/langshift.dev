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
	"github.com/langshift/lites/internal/payload"
)

func TestApprovalRequestAndDecisionAreDurableExactReplays(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	fixture := prepareApprovalFixture(t, ctx, admin, pool, "e1")

	request := RequestApprovalCommand{
		ApprovalID: fixtureID("e1", 40), TenantID: fixture.tenantID, RunID: fixture.runID,
		ToolCallID: fixture.toolCallID, ApprovalKind: "tool_execution", ProposalHash: "approval-proposal-e1",
		PermissionSnapshot: fixture.permissionSnapshot, TargetVersion: fixture.toolVersion,
		RequestedBy: fixture.targetUserID, ExpiresAt: fixture.now.Add(20 * time.Minute),
		Actor:         json.RawMessage(`{"kind":"service","name":"approval-control-plane"}`),
		CorrelationID: fixture.correlationID, RequestedEvent: repairPointer("e1", "approval-requested"),
	}
	const contenders = 12
	var wait sync.WaitGroup
	results := make(chan RequestedApproval, contenders)
	errs := make(chan error, contenders)
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := fixture.store.RequestApproval(ctx, request)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("request approval: %v", err)
	}
	fresh, replayed := 0, 0
	for result := range results {
		if result.Status != "pending" || result.Version != 1 || result.TargetVersion != fixture.toolVersion {
			t.Fatalf("request result=%#v", result)
		}
		if result.Replayed {
			replayed++
		} else {
			fresh++
		}
	}
	if fresh != 1 || replayed != contenders-1 {
		t.Fatalf("request fresh=%d replayed=%d", fresh, replayed)
	}

	decision := DecideApprovalCommand{
		ApprovalID: request.ApprovalID, TenantID: fixture.tenantID, DecisionID: fixtureID("e1", 41),
		ActorUserID: fixture.approverOneID, SessionID: fixture.sessionOneID, Decision: "approve", Mode: "admin",
		ProposalHash: request.ProposalHash, PermissionSnapshot: fixture.permissionSnapshot,
		ExpectedApprovalVersion: 1, TargetVersion: fixture.toolVersion,
		Actor: json.RawMessage(`{"kind":"user","role":"owner"}`), CorrelationID: fixture.correlationID,
		DecisionEvent: repairPointer("e1", "approval-granted"),
	}
	decided, err := fixture.store.DecideApproval(ctx, decision)
	if err != nil || decided.Status != "granted" || decided.Version != 2 || decided.Replayed {
		t.Fatalf("decision=%#v err=%v", decided, err)
	}
	if _, err = admin.Exec(ctx, `UPDATE agent.tool_calls SET status='failed',tool_call_version=tool_call_version+1,updated_at=$3 WHERE tenant_id=$1 AND id=$2`, fixture.tenantID, fixture.toolCallID, fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	fixture.store.Now = func() time.Time { return fixture.now.Add(25 * time.Minute) }
	requestReplay, err := fixture.store.RequestApproval(ctx, request)
	if err != nil || !requestReplay.Replayed || requestReplay.Status != "granted" || requestReplay.Version != 2 {
		t.Fatalf("request replay after target advance=%#v err=%v", requestReplay, err)
	}
	decisionReplay, err := fixture.store.DecideApproval(ctx, decision)
	if err != nil || !decisionReplay.Replayed || decisionReplay.Status != "granted" || decisionReplay.Version != 2 || !decisionReplay.UpdatedAt.Equal(decided.UpdatedAt) {
		t.Fatalf("decision replay after target advance=%#v err=%v", decisionReplay, err)
	}
	conflictingRequest := request
	conflictingRequest.ProposalHash = "different-proposal"
	if _, err = fixture.store.RequestApproval(ctx, conflictingRequest); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("conflicting request error=%v", err)
	}
	conflictingDecision := decision
	conflictingDecision.Mode = "user"
	if _, err = fixture.store.DecideApproval(ctx, conflictingDecision); !errors.Is(err, ErrApprovalConflict) {
		t.Fatalf("conflicting decision error=%v", err)
	}

	var status, decisionMode, eventType string
	var version, approvals, decisions, events, outbox int
	err = admin.QueryRow(ctx, `SELECT a.status,a.version,d.decision_mode,e.event_type,(SELECT count(*) FROM agent.approvals WHERE tenant_id=a.tenant_id AND id=a.id),(SELECT count(*) FROM agent.approval_decisions WHERE tenant_id=a.tenant_id AND approval_id=a.id),(SELECT count(*) FROM agent.events WHERE tenant_id=a.tenant_id AND aggregate_kind='approval' AND aggregate_id=a.id),(SELECT count(*) FROM agent.outbox WHERE tenant_id=a.tenant_id AND aggregate_kind='approval' AND aggregate_id=a.id) FROM agent.approvals a JOIN agent.approval_decisions d ON d.tenant_id=a.tenant_id AND d.approval_id=a.id JOIN agent.events e ON e.id=d.decision_event_id WHERE a.tenant_id=$1 AND a.id=$2`, fixture.tenantID, request.ApprovalID).Scan(&status, &version, &decisionMode, &eventType, &approvals, &decisions, &events, &outbox)
	if err != nil {
		t.Fatal(err)
	}
	if status != "granted" || version != 2 || decisionMode != "admin" || eventType != "ApprovalGranted" || approvals != 1 || decisions != 1 || events != 2 || outbox != 2 {
		t.Fatalf("durable approval=%s/v%d mode=%s event=%s counts=%d/%d/%d/%d", status, version, decisionMode, eventType, approvals, decisions, events, outbox)
	}
}

func TestApprovalDecisionRejectsStalePermissionAndReauthentication(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	fixture := prepareApprovalFixture(t, ctx, admin, pool, "e2")
	request := RequestApprovalCommand{ApprovalID: fixtureID("e2", 40), TenantID: fixture.tenantID, RunID: fixture.runID, ToolCallID: fixture.toolCallID, ApprovalKind: "tool_execution", ProposalHash: "approval-proposal-e2", PermissionSnapshot: fixture.permissionSnapshot, TargetVersion: fixture.toolVersion, RequestedBy: fixture.targetUserID, ExpiresAt: fixture.now.Add(20 * time.Minute), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: fixture.correlationID, RequestedEvent: repairPointer("e2", "approval-requested")}
	if _, err := fixture.store.RequestApproval(ctx, request); err != nil {
		t.Fatal(err)
	}
	decision := DecideApprovalCommand{ApprovalID: request.ApprovalID, TenantID: fixture.tenantID, DecisionID: fixtureID("e2", 41), ActorUserID: fixture.approverOneID, SessionID: fixture.sessionOneID, Decision: "approve", Mode: "admin", ProposalHash: request.ProposalHash, PermissionSnapshot: fixture.permissionSnapshot, ExpectedApprovalVersion: 1, TargetVersion: fixture.toolVersion, Actor: json.RawMessage(`{"kind":"user","role":"owner"}`), CorrelationID: fixture.correlationID, DecisionEvent: repairPointer("e2", "approval-granted")}
	if _, err := admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$2 WHERE id=$1`, fixture.sessionOneID, fixture.now.Add(-approvalReauthenticationMaxAge-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DecideApproval(ctx, decision); !errors.Is(err, ErrApprovalAuthorization) {
		t.Fatalf("stale reauthentication error=%v", err)
	}
	if _, err := admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$2 WHERE id=$1`, fixture.sessionOneID, fixture.now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE identity.memberships SET version=version+1,updated_at=$3 WHERE tenant_id=$1 AND user_id=$2`, fixture.tenantID, fixture.targetUserID, fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DecideApproval(ctx, decision); !errors.Is(err, ErrApprovalNotActionable) {
		t.Fatalf("stale permission error=%v", err)
	}
	var status string
	var version, decisions int
	if err := admin.QueryRow(ctx, `SELECT status,version,(SELECT count(*) FROM agent.approval_decisions WHERE tenant_id=$1 AND approval_id=$2) FROM agent.approvals WHERE tenant_id=$1 AND id=$2`, fixture.tenantID, request.ApprovalID).Scan(&status, &version, &decisions); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || version != 1 || decisions != 0 {
		t.Fatalf("approval changed after denied decisions: %s/v%d decisions=%d", status, version, decisions)
	}
}

func TestApprovalExpiryIsEpochFencedAndExactlyOnce(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	fixture := prepareApprovalFixture(t, ctx, admin, pool, "e3")
	request := RequestApprovalCommand{ApprovalID: fixtureID("e3", 40), TenantID: fixture.tenantID, RunID: fixture.runID, ToolCallID: fixture.toolCallID, ApprovalKind: "tool_execution", ProposalHash: "approval-proposal-e3", PermissionSnapshot: fixture.permissionSnapshot, TargetVersion: fixture.toolVersion, RequestedBy: fixture.targetUserID, ExpiresAt: fixture.now.Add(time.Minute), Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: fixture.correlationID, RequestedEvent: repairPointer("e3", "approval-requested")}
	if _, err := fixture.store.RequestApproval(ctx, request); err != nil {
		t.Fatal(err)
	}
	blobs := &repairServiceBlobs{values: map[string][]byte{}}
	payloads := payload.EnvelopeStore{Keys: repairServiceKeyProvider{key: payload.Key{ID: "approval-expiry-v1", Material: bytes.Repeat([]byte{0x73}, 32)}}, Blobs: blobs}
	expiredNow := fixture.now.Add(2 * time.Minute)
	service := ApprovalControlService{Pool: pool, Store: fixture.store, Payloads: payloads, IDKey: fixture.store.IDKey, Now: func() time.Time { return expiredNow }}
	if _, err := service.ListExpiredApprovalTenantIDs(ctx, fixtureID("e3", 99), "", 100, 0, 1); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("stale epoch error=%v", err)
	}
	tenants, err := service.ListExpiredApprovalTenantIDs(ctx, fixture.store.StoreEpoch, "", 100, 0, 1)
	if err != nil || len(tenants) != 1 || tenants[0] != fixture.tenantID {
		t.Fatalf("expired tenants=%v err=%v", tenants, err)
	}
	approvals, err := service.ListExpiredApprovalIDs(ctx, fixture.tenantID, fixture.store.StoreEpoch, "", 100)
	if err != nil || len(approvals) != 1 || approvals[0] != request.ApprovalID {
		t.Fatalf("expired approvals=%v err=%v", approvals, err)
	}
	first, err := service.ExpireApproval(ctx, fixture.tenantID, request.ApprovalID, fixture.store.StoreEpoch, fixture.correlationID)
	if err != nil || first.Status != "expired" || first.Version != 2 || first.Replayed || !first.ExpiredAt.Equal(expiredNow) {
		t.Fatalf("first expiry=%#v err=%v", first, err)
	}
	const replays = 12
	results := make(chan ExpiredApproval, replays)
	errs := make(chan error, replays)
	for range replays {
		go func() {
			result, replayErr := service.ExpireApproval(ctx, fixture.tenantID, request.ApprovalID, fixture.store.StoreEpoch, fixture.correlationID)
			if replayErr != nil {
				errs <- replayErr
				return
			}
			results <- result
		}()
	}
	for range replays {
		select {
		case replayErr := <-errs:
			t.Fatal(replayErr)
		case result := <-results:
			if !result.Replayed || result.EventID != first.EventID || result.Version != first.Version || !result.ExpiredAt.Equal(first.ExpiredAt) {
				t.Fatalf("expiry replay=%#v first=%#v", result, first)
			}
		}
	}
	var status, eventType string
	var version, eventCount, outboxCount int
	var expiredAt time.Time
	err = admin.QueryRow(ctx, `SELECT a.status,a.version,a.expired_at,e.event_type,(SELECT count(*) FROM agent.events WHERE tenant_id=a.tenant_id AND aggregate_kind='approval' AND aggregate_id=a.id AND event_type='ApprovalExpired'),(SELECT count(*) FROM agent.outbox WHERE tenant_id=a.tenant_id AND aggregate_kind='approval' AND aggregate_id=a.id AND command_type='events.publish') FROM agent.approvals a JOIN agent.events e ON e.tenant_id=a.tenant_id AND e.aggregate_kind='approval' AND e.aggregate_id=a.id AND e.aggregate_version=2 WHERE a.tenant_id=$1 AND a.id=$2`, fixture.tenantID, request.ApprovalID).Scan(&status, &version, &expiredAt, &eventType, &eventCount, &outboxCount)
	if err != nil {
		t.Fatal(err)
	}
	if status != "expired" || version != 2 || !expiredAt.Equal(expiredNow) || eventType != "ApprovalExpired" || eventCount != 1 || outboxCount != 2 {
		t.Fatalf("expired approval=%s/v%d at=%s event=%s counts=%d/%d", status, version, expiredAt, eventType, eventCount, outboxCount)
	}
}

type approvalFixture struct {
	unknownRepairFixture
	permissionSnapshot string
}

func prepareApprovalFixture(t *testing.T, ctx context.Context, admin, pool *pgxpool.Pool, prefix string) approvalFixture {
	t.Helper()
	fixture := prepareUnknownRepairFixture(t, ctx, admin, pool, prefix)
	membershipID := fixtureID(prefix, 16)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,'member','active',$4)`, membershipID, fixture.tenantID, fixture.targetUserID, fixture.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `UPDATE agent.tool_calls SET status='awaiting_approval',tool_call_version=tool_call_version+1,updated_at=$3 WHERE tenant_id=$1 AND id=$2 RETURNING tool_call_version`, fixture.tenantID, fixture.toolCallID, fixture.now).Scan(&fixture.toolVersion); err != nil {
		t.Fatal(err)
	}
	return approvalFixture{unknownRepairFixture: fixture, permissionSnapshot: fmt.Sprintf("membership:%s:v1:role:member", membershipID)}
}
