package postgres

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestRepairOutcomeAndIdentifiers(t *testing.T) {
	store := RunStore{IDKey: bytes.Repeat([]byte{0xd1}, 32)}
	manual, terminal, executed, err := repairExecutionEventIDs(store.IDKey, "repair")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, id := range []string{manual.event, manual.outbox, manual.publish, terminal.event, terminal.outbox, terminal.publish, executed.event, executed.outbox, executed.publish} {
		if id == "" || seen[id] {
			t.Fatalf("invalid or colliding repair identifier %q", id)
		}
		seen[id] = true
	}
	tests := []struct {
		resolution, toolStatus, effectStatus, resultEvent string
		toolVersion, effectVersion                        uint64
	}{
		{"confirmed_occurred", "succeeded", "confirmed", terminal.event, 9, 12},
		{"confirmed_not_occurred", "failed", "failed", terminal.event, 9, 12},
		{"accepted_unknown", "resolved_unknown", "accepted_unknown", manual.event, 8, 12},
	}
	for _, test := range tests {
		toolStatus, effectStatus, toolVersion, effectVersion, eventID, valid := repairOutcome(test.resolution, 7, 11, manual.event, terminal.event)
		if !valid || toolStatus != test.toolStatus || effectStatus != test.effectStatus || toolVersion != test.toolVersion || effectVersion != test.effectVersion || eventID != test.resultEvent {
			t.Fatalf("resolution %s mapped to %s/%s/v%d/v%d/%s valid=%v", test.resolution, toolStatus, effectStatus, toolVersion, effectVersion, eventID, valid)
		}
	}
	if _, _, _, _, _, valid := repairOutcome("invented", 1, 1, manual.event, terminal.event); valid {
		t.Fatal("unknown repair resolution accepted")
	}
}

func TestRepairCommandValidation(t *testing.T) {
	now := time.Now().UTC()
	base := ProposeToolEffectRepairCommand{RepairID: "repair", TenantID: "tenant", InitiatorUserID: "initiator", ToolCallID: "tool", ExpectedToolVersion: 3, ExpectedEffectVersion: 4, EffectKey: "effect", Resolution: "accepted_unknown", ProposalHash: "proposal", EvidenceHash: "evidence", ResidualRiskRef: "encrypted://risk", ExpiresAt: now.Add(time.Hour), Actor: json.RawMessage(`{"kind":"user"}`), CorrelationID: "correlation", ProposedEvent: PayloadPointer{Ref: "encrypted://proposed", Hash: "proposed"}}
	if !validRepairProposal(base) {
		t.Fatal("valid accepted-unknown proposal rejected")
	}
	base.ResidualRiskRef = ""
	if validRepairProposal(base) {
		t.Fatal("accepted-unknown proposal without residual risk accepted")
	}
	base.Resolution = "confirmed_occurred"
	if !validRepairProposal(base) {
		t.Fatal("valid confirmed proposal rejected")
	}

	decision := DecideRepairCommand{RepairID: "repair", TenantID: "tenant", ApproverUserID: "approver", SessionID: "session", ApprovalID: "approval", Decision: "reject", ProposalHash: "proposal", ExpectedRepairVersion: 1, ExpectedToolVersion: 3, ExpectedEffectVersion: 4, PermissionSnapshot: "permission", ReauthenticatedAt: now, Actor: json.RawMessage(`{"kind":"user"}`), CorrelationID: "correlation", DecisionEvent: PayloadPointer{Ref: "encrypted://decision", Hash: "decision"}}
	if !validRepairDecision(decision) {
		t.Fatal("valid rejection without approved payload rejected")
	}
	decision.Decision = "approve"
	if validRepairDecision(decision) {
		t.Fatal("approval without RepairCommandApproved payload accepted")
	}
}
