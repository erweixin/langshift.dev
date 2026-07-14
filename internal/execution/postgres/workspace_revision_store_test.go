package postgres

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestWorkspaceRevisionCommandValidation(t *testing.T) {
	prepare := PrepareWorkspaceRevisionCommand{
		RevisionID: "revision", TenantID: "tenant", ToolCallID: "tool", WorkspaceID: "workspace",
		ExpectedToolVersion: 3, BaseRevision: "base", PreparedRevision: "prepared", PreparedHash: "prepared-hash",
		EffectKey: "effect", ProposalHash: "proposal", Actor: json.RawMessage(`{"kind":"service"}`),
		CorrelationID: "correlation", PreparedEvent: PayloadPointer{Ref: "encrypted://prepared", Hash: "prepared-event"},
	}
	if !validPrepareWorkspaceRevision(prepare) {
		t.Fatal("valid preparation command rejected")
	}
	prepare.ProposalHash = ""
	if validPrepareWorkspaceRevision(prepare) {
		t.Fatal("preparation without content binding accepted")
	}

	authorize := AuthorizeWorkspaceRevisionCommand{
		RevisionID: "revision", TenantID: "tenant", ApprovalID: "approval", ApprovalEventID: "approval-event",
		ProposalHash: "proposal", PermissionSnapshot: "permission", ExpectedRevisionVersion: 1,
		ExpectedApprovalVersion: 2, ExpectedToolVersion: 4, QueueClass: "interactive", ResourceClass: "workspace-publisher",
		Priority: 80, CostUnits: 2, MaxAttempts: 5, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation",
		AuthorizedEvent:    PayloadPointer{Ref: "encrypted://authorized", Hash: "authorized"},
		ToolRequestedEvent: PayloadPointer{Ref: "encrypted://requested", Hash: "requested"},
		ExecuteCommand:     PayloadPointer{Ref: "encrypted://execute", Hash: "execute"},
	}
	if !validAuthorizeWorkspaceRevision(authorize) {
		t.Fatal("valid authorization command rejected")
	}
	authorize.PermissionSnapshot = ""
	if validAuthorizeWorkspaceRevision(authorize) {
		t.Fatal("authorization without permission binding accepted")
	}
}

func TestWorkspaceRevisionIdentifiersAreStableAndDomainSeparated(t *testing.T) {
	store := RunStore{IDKey: bytes.Repeat([]byte{0x7a}, 32)}
	preparedOne, err := store.workspacePreparedIDs("revision-one")
	if err != nil {
		t.Fatal(err)
	}
	preparedReplay, err := store.workspacePreparedIDs("revision-one")
	if err != nil || preparedReplay != preparedOne {
		t.Fatalf("prepared identifiers are not stable: %#v %#v err=%v", preparedOne, preparedReplay, err)
	}
	authorizedOne, err := store.workspaceAuthorizationIDs("revision-one")
	if err != nil {
		t.Fatal(err)
	}
	authorizedTwo, err := store.workspaceAuthorizationIDs("revision-two")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{preparedOne.event: true, preparedOne.outbox: true, preparedOne.publish: true}
	for _, value := range []string{authorizedOne.authorizationEvent, authorizedOne.authorizationPublishOutbox, authorizedOne.authorizationPublish, authorizedOne.executeOutbox, authorizedOne.executeCommand, authorizedOne.job, authorizedOne.toolEvent, authorizedOne.toolPublishOutbox, authorizedOne.toolPublish} {
		if seen[value] {
			t.Fatalf("identifier domains collided at %s", value)
		}
		seen[value] = true
	}
	if authorizedOne == authorizedTwo {
		t.Fatal("different revisions produced identical authorization identifiers")
	}
}
