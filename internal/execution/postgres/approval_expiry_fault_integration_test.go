//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/langshift/lites/internal/payload"
)

func TestApprovalExpiryFaultInjection100(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	fixture := prepareApprovalFixture(t, ctx, admin, pool, "fe")

	const repetitions = 100
	requests := make([]RequestApprovalCommand, 0, repetitions)
	for iteration := 0; iteration < repetitions; iteration++ {
		request := RequestApprovalCommand{
			ApprovalID: fixtureID("fe", 100+iteration), TenantID: fixture.tenantID,
			RunID: fixture.runID, ToolCallID: fixture.toolCallID, ApprovalKind: "tool_execution",
			ProposalHash:       fmt.Sprintf("approval-expiry-fault-%03d", iteration),
			PermissionSnapshot: fixture.permissionSnapshot, TargetVersion: fixture.toolVersion,
			RequestedBy: fixture.targetUserID, ExpiresAt: fixture.now.Add(time.Minute),
			Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: fixture.correlationID,
			RequestedEvent: repairPointer("fe", fmt.Sprintf("approval-requested-%03d", iteration)),
		}
		if _, err := fixture.store.RequestApproval(ctx, request); err != nil {
			t.Fatalf("iteration %d request: %v", iteration, err)
		}
		requests = append(requests, request)
	}

	blobs := &repairServiceBlobs{values: map[string][]byte{}}
	payloads := payload.EnvelopeStore{Keys: repairServiceKeyProvider{key: payload.Key{ID: "approval-expiry-fault-v1", Material: bytes.Repeat([]byte{0xfe}, 32)}}, Blobs: blobs}
	expiredNow := fixture.now.Add(2 * time.Minute)
	service := ApprovalControlService{Pool: pool, Store: fixture.store, Payloads: payloads, IDKey: fixture.store.IDKey, Now: func() time.Time { return expiredNow }}
	approvalIDs, err := service.ListExpiredApprovalIDs(ctx, fixture.tenantID, fixture.store.StoreEpoch, "", repetitions)
	if err != nil || len(approvalIDs) != repetitions {
		t.Fatalf("expired approvals=%d error=%v", len(approvalIDs), err)
	}

	var expired, expiryReplays, lateApprovalsRejected int
	for iteration, request := range requests {
		first, expireErr := service.ExpireApproval(ctx, fixture.tenantID, request.ApprovalID, fixture.store.StoreEpoch, fixture.correlationID)
		if expireErr != nil || first.Status != "expired" || first.Version != 2 || first.Replayed || !first.ExpiredAt.Equal(expiredNow) {
			t.Fatalf("iteration %d expiry=%#v error=%v", iteration, first, expireErr)
		}
		expired++
		replay, replayErr := service.ExpireApproval(ctx, fixture.tenantID, request.ApprovalID, fixture.store.StoreEpoch, fixture.correlationID)
		if replayErr != nil || !replay.Replayed || replay.EventID != first.EventID || replay.Version != first.Version || !replay.ExpiredAt.Equal(first.ExpiredAt) {
			t.Fatalf("iteration %d replay=%#v first=%#v error=%v", iteration, replay, first, replayErr)
		}
		expiryReplays++

		decision := DecideApprovalCommand{
			ApprovalID: request.ApprovalID, TenantID: fixture.tenantID, DecisionID: fixtureID("fe", 300+iteration),
			ActorUserID: fixture.approverOneID, SessionID: fixture.sessionOneID, Decision: "approve", Mode: "admin",
			ProposalHash: request.ProposalHash, PermissionSnapshot: fixture.permissionSnapshot,
			ExpectedApprovalVersion: 1, TargetVersion: fixture.toolVersion, ReauthenticatedAt: fixture.now,
			Actor: json.RawMessage(`{"kind":"user","role":"owner"}`), CorrelationID: fixture.correlationID,
			DecisionEvent: repairPointer("fe", fmt.Sprintf("late-approval-%03d", iteration)),
		}
		if _, decisionErr := fixture.store.DecideApproval(ctx, decision); !errors.Is(decisionErr, ErrApprovalNotActionable) {
			t.Fatalf("iteration %d late approval error=%v", iteration, decisionErr)
		}
		lateApprovalsRejected++

		var status string
		var version, expiryEvents, decisions int
		if err = admin.QueryRow(ctx, `SELECT status,version,
			(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='approval' AND aggregate_id=$2 AND event_type='ApprovalExpired'),
			(SELECT count(*) FROM agent.approval_decisions WHERE tenant_id=$1 AND approval_id=$2)
			FROM agent.approvals WHERE tenant_id=$1 AND id=$2`, fixture.tenantID, request.ApprovalID).Scan(&status, &version, &expiryEvents, &decisions); err != nil {
			t.Fatalf("iteration %d inspect: %v", iteration, err)
		}
		if status != "expired" || version != 2 || expiryEvents != 1 || decisions != 0 {
			t.Fatalf("iteration %d approval=%s/v%d expiry_events=%d decisions=%d", iteration, status, version, expiryEvents, decisions)
		}
	}
	t.Logf("fault_injection={\"scenario\":\"approval_expiry\",\"repetitions\":%d,\"approvals_expired\":%d,\"expiry_replays\":%d,\"late_approvals_rejected\":%d,\"duplicate_expiry_events\":0,\"approval_bypasses\":0,\"lost_event_facts\":0}", repetitions, expired, expiryReplays, lateApprovalsRejected)
}
