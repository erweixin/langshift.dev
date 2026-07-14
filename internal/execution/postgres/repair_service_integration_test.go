//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	executionapi "github.com/langshift/lites/internal/execution/api"
	"github.com/langshift/lites/internal/payload"
)

type repairServiceKeyProvider struct{ key payload.Key }

func (provider repairServiceKeyProvider) Current(context.Context, string) (payload.Key, error) {
	return provider.key, nil
}

func (provider repairServiceKeyProvider) ByID(_ context.Context, _ string, keyID string) (payload.Key, error) {
	if keyID != provider.key.ID {
		return payload.Key{}, payload.ErrInvalidKey
	}
	return provider.key, nil
}

type repairServiceBlobs struct {
	mu     sync.RWMutex
	values map[string][]byte
}

func (store *repairServiceBlobs) Put(_ context.Context, key string, value []byte) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	ref := "memory://" + key
	if prior, exists := store.values[ref]; exists {
		if bytes.Equal(prior, value) {
			return ref, nil
		}
		return "", errors.New("immutable repair payload conflict")
	}
	store.values[ref] = append([]byte(nil), value...)
	return ref, nil
}

func (store *repairServiceBlobs) Get(_ context.Context, ref string) ([]byte, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.values[ref]
	if !ok {
		return nil, errors.New("repair payload not found")
	}
	return append([]byte(nil), value...), nil
}

func TestRepairControlServiceIsDurableIdempotentAndAutoExecutesDualApproval(t *testing.T) {
	ctx := context.Background()
	admin := executionPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := executionPool(t, ctx, "LITES_TEST_AGENT_DATABASE_URL")
	defer pool.Close()
	fixture := prepareUnknownRepairFixture(t, ctx, admin, pool, "de")
	blobs := &repairServiceBlobs{values: map[string][]byte{}}
	payloads := payload.EnvelopeStore{Keys: repairServiceKeyProvider{key: payload.Key{ID: "repair-service-v1", Material: bytes.Repeat([]byte{0xe1}, 32)}}, Blobs: blobs}
	service := RepairControlService{
		Pool: pool, Store: fixture.store, Payloads: payloads, IDKey: fixture.store.IDKey,
		IdempotencyKeyPepper: bytes.Repeat([]byte{0xe2}, 32), IdempotencyTTL: 24 * time.Hour,
		RequestDigestPepper: bytes.Repeat([]byte{0xe3}, 32),
		ProposalTTL:         30 * time.Minute, Now: func() time.Time { return fixture.now },
		ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 2, ResumeMaxAttempts: 5,
	}
	proposalCommand := executionapi.ProposeRepairCommand{
		RequestID: fixture.correlationID, ClientRequestID: "repair-client-propose-de", IdempotencyKey: "repair-propose-key-de-0001",
		TenantID: fixture.tenantID, UserID: fixture.initiatorID, SessionID: fixture.initiatorSessionID,
		TargetID: fixture.toolCallID, TargetVersion: fixture.toolVersion, Resolution: "accepted_unknown",
		EvidenceHash: "sha256:evidence-de", Reason: "Provider evidence is exhausted; operators explicitly accept the residual duplication risk.",
	}
	const proposalContenders = 12
	proposalResults := make(chan executionapi.RepairResult, proposalContenders)
	proposalErrors := make(chan error, proposalContenders)
	var proposals sync.WaitGroup
	for range proposalContenders {
		proposals.Add(1)
		go func() {
			defer proposals.Done()
			result, proposeErr := service.ProposeRepair(ctx, proposalCommand)
			if proposeErr != nil {
				proposalErrors <- proposeErr
				return
			}
			proposalResults <- result
		}()
	}
	proposals.Wait()
	close(proposalResults)
	close(proposalErrors)
	for proposeErr := range proposalErrors {
		t.Fatalf("concurrent proposal error=%v", proposeErr)
	}
	var proposed executionapi.RepairResult
	for result := range proposalResults {
		if proposed.ID == "" {
			proposed = result
		}
		if result.ID != proposed.ID || result.Status != "proposed" || result.Version != 1 || !result.UpdatedAt.Equal(fixture.now) {
			t.Fatalf("concurrent proposed=%#v first=%#v", result, proposed)
		}
	}
	if proposed.ID == "" {
		t.Fatal("concurrent proposal returned no result")
	}
	var err error
	replayedProposal, err := service.ProposeRepair(ctx, proposalCommand)
	if err != nil || replayedProposal != proposed {
		t.Fatalf("proposal replay=%#v err=%v", replayedProposal, err)
	}
	conflict := proposalCommand
	conflict.Reason = "different request under the same idempotency key"
	if _, err = service.ProposeRepair(ctx, conflict); !errors.Is(err, executionapi.ErrIdempotencyConflict) {
		t.Fatalf("proposal idempotency conflict error=%v", err)
	}
	var proposalHash string
	if err = admin.QueryRow(ctx, `SELECT proposal_hash FROM agent.repair_commands WHERE tenant_id=$1 AND id=$2`, fixture.tenantID, proposed.ID).Scan(&proposalHash); err != nil {
		t.Fatal(err)
	}
	initiatorDecision := executionapi.DecideRepairCommand{
		RequestID: fixture.correlationID, ClientRequestID: "repair-client-self-approve-de", IdempotencyKey: "repair-self-approve-de-0001",
		TenantID: fixture.tenantID, UserID: fixture.initiatorID, SessionID: fixture.initiatorSessionID, RepairID: proposed.ID,
		Decision: "approve", ProposalHash: proposalHash, ExpectedRepairVersion: 1, TargetVersion: fixture.toolVersion,
		PermissionSnapshot: "role=admin;policy=repair-v1",
	}
	if _, err = service.DecideRepair(ctx, initiatorDecision); !errors.Is(err, executionapi.ErrPermissionDenied) {
		t.Fatalf("initiator self-approval error=%v", err)
	}
	firstCommand := executionapi.DecideRepairCommand{
		RequestID: fixture.correlationID, ClientRequestID: "repair-client-approve-one-de", IdempotencyKey: "repair-approve-key-de-0001",
		TenantID: fixture.tenantID, UserID: fixture.approverOneID, SessionID: fixture.sessionOneID, RepairID: proposed.ID,
		Decision: "approve", ProposalHash: proposalHash, ExpectedRepairVersion: 1, TargetVersion: fixture.toolVersion,
		PermissionSnapshot: "role=owner;policy=repair-v1",
	}
	secondCommand := executionapi.DecideRepairCommand{
		RequestID: fixture.correlationID, ClientRequestID: "repair-client-approve-two-de", IdempotencyKey: "repair-approve-key-de-0002",
		TenantID: fixture.tenantID, UserID: fixture.approverTwoID, SessionID: fixture.sessionTwoID, RepairID: proposed.ID,
		Decision: "approve", ProposalHash: proposalHash, ExpectedRepairVersion: 1, TargetVersion: fixture.toolVersion,
		PermissionSnapshot: "role=admin;policy=repair-v1",
	}
	decisionCommands := []executionapi.DecideRepairCommand{firstCommand, secondCommand}
	type decisionOutcome struct {
		index  int
		result executionapi.RepairResult
		err    error
	}
	decisionOutcomes := make(chan decisionOutcome, len(decisionCommands))
	for index, decisionCommand := range decisionCommands {
		go func() {
			result, decisionErr := service.DecideRepair(ctx, decisionCommand)
			decisionOutcomes <- decisionOutcome{index: index, result: result, err: decisionErr}
		}()
	}
	decisionResults := make([]executionapi.RepairResult, len(decisionCommands))
	proposedDecisions, executedDecisions := 0, 0
	for range decisionCommands {
		outcome := <-decisionOutcomes
		if outcome.err != nil {
			t.Fatalf("concurrent approval %d error=%v", outcome.index, outcome.err)
		}
		decisionResults[outcome.index] = outcome.result
		switch {
		case outcome.result.Status == "proposed" && outcome.result.Version == 1:
			proposedDecisions++
		case outcome.result.Status == "executed" && outcome.result.Version == 3:
			executedDecisions++
		default:
			t.Fatalf("concurrent approval %d result=%#v", outcome.index, outcome.result)
		}
	}
	if proposedDecisions != 1 || executedDecisions != 1 {
		t.Fatalf("approval outcomes proposed=%d executed=%d results=%#v", proposedDecisions, executedDecisions, decisionResults)
	}
	for index, decisionCommand := range decisionCommands {
		replayed, replayErr := service.DecideRepair(ctx, decisionCommand)
		if replayErr != nil || replayed != decisionResults[index] {
			t.Fatalf("approval %d replay=%#v want=%#v err=%v", index, replayed, decisionResults[index], replayErr)
		}
	}

	approvedEventID, _, _, err := repairApprovedEventIDs(fixture.store.IDKey, proposed.ID)
	if err != nil {
		t.Fatal(err)
	}
	var approvedRef, approvedHash, repairStatus, toolStatus, effectStatus string
	var idempotencyRows, approvalRows, manualEvents int
	err = admin.QueryRow(ctx, `SELECT e.payload_ref,e.payload_hash,r.status,t.status,te.status,(SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND status='completed' AND operation_id IN ('repair.propose','repair.decide')),(SELECT count(*) FROM agent.repair_approvals WHERE tenant_id=$1 AND repair_command_id=$2 AND decision='approve'),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='tool_call' AND aggregate_id=$3 AND event_type='ToolCallManuallyResolved') FROM agent.events e JOIN agent.repair_commands r ON r.tenant_id=e.tenant_id AND r.id=$2 JOIN agent.tool_calls t ON t.tenant_id=r.tenant_id AND t.id=r.target_id JOIN agent.tool_effects te ON te.tenant_id=t.tenant_id AND te.tool_call_id=t.id WHERE e.tenant_id=$1 AND e.id=$4`, fixture.tenantID, proposed.ID, fixture.toolCallID, approvedEventID).Scan(&approvedRef, &approvedHash, &repairStatus, &toolStatus, &effectStatus, &idempotencyRows, &approvalRows, &manualEvents)
	if err != nil {
		t.Fatal(err)
	}
	if repairStatus != "executed" || toolStatus != "resolved_unknown" || effectStatus != "accepted_unknown" || idempotencyRows != 3 || approvalRows != 2 || manualEvents != 1 {
		t.Fatalf("repair=%s tool=%s effect=%s idempotency=%d approvals=%d manual=%d", repairStatus, toolStatus, effectStatus, idempotencyRows, approvalRows, manualEvents)
	}
	encoded, err := payloads.Get(ctx, payload.Descriptor{TenantID: fixture.tenantID, ObjectID: approvedEventID, Class: repairEventClass, ContentType: "application/json"}, payload.Manifest{Ref: approvedRef, Hash: approvedHash})
	if err != nil {
		t.Fatal(err)
	}
	var approvedPayload struct {
		ApprovalEventIDs []string `json:"approval_event_ids"`
	}
	if err = json.Unmarshal(encoded, &approvedPayload); err != nil || len(approvedPayload.ApprovalEventIDs) != 2 || approvedPayload.ApprovalEventIDs[0] == approvedPayload.ApprovalEventIDs[1] {
		t.Fatalf("approved payload=%s err=%v", encoded, err)
	}
}
