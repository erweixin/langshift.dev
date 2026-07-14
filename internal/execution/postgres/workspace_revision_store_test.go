package postgres

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/langshift/lites/internal/execution/statemachine"
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

func TestWorkspacePublishCommandValidationAndIdentifiers(t *testing.T) {
	claim := ToolClaim{ToolCallID: "tool", RunID: "run", GroupID: "group", TenantID: "tenant", UserID: "user", StoreEpoch: "epoch", ToolCallVersion: 5, EffectID: "effect", EffectClass: "reconcilable_write", ProviderRequestID: "effect", CommandID: "command", ConsumerName: "workspace-publisher", RequestHash: "request", JobID: "job", InboxID: "inbox", AttemptID: "attempt", Fence: 2, LeaseToken: "token", LeaseExpiresAt: time.Now().Add(time.Minute)}
	begin := BeginWorkspacePublishCommand{RevisionID: "revision", ExpectedRevisionVersion: 2, Claim: claim, Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", StartedEvent: PayloadPointer{Ref: "encrypted://started", Hash: "started"}}
	if !validBeginWorkspacePublish(begin) {
		t.Fatal("valid workspace publish command rejected")
	}
	begin.Claim.EffectClass = "idempotent_write"
	if validBeginWorkspacePublish(begin) {
		t.Fatal("non-reconcilable workspace publish accepted")
	}
	tool := CompleteToolCommand{Claim: claim, ExpectedToolVersion: claim.ToolCallVersion, TargetState: statemachine.ToolCallSucceeded, ResultHash: "result", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", ToolCompletedEvent: PayloadPointer{Ref: "encrypted://tool", Hash: "tool"}, AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://attempt", Hash: "attempt"}, GroupJoinedEvent: PayloadPointer{Ref: "encrypted://group", Hash: "group"}, RunResumeQueuedEvent: PayloadPointer{Ref: "encrypted://run", Hash: "run"}, ResumeCommand: PayloadPointer{Ref: "encrypted://resume", Hash: "resume"}, ResumeQueueClass: "interactive", ResumeResourceClass: "llm", ResumePriority: 50, ResumeCostUnits: 1, ResumeMaxAttempts: 5}
	complete := CompleteWorkspacePublishCommand{RevisionID: "revision", ExpectedRevisionVersion: 3, Tool: tool, PublishedRevision: "published", PublishedHash: "published-hash", ObservedRevision: "published", CompletionEvent: PayloadPointer{Ref: "encrypted://committed", Hash: "committed"}}
	if !validCompleteWorkspacePublish(complete, EffectCompletion{ExternalResourceRef: "workspace://published"}) {
		t.Fatal("valid workspace completion rejected")
	}
	complete.PublishedHash = ""
	if validCompleteWorkspacePublish(complete, EffectCompletion{ExternalResourceRef: "workspace://published"}) {
		t.Fatal("confirmed workspace completion without content hash accepted")
	}
	store := RunStore{IDKey: bytes.Repeat([]byte{0x7b}, 32)}
	first, err := store.workspacePublishIDs("confirmed", "revision", "attempt")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.workspacePublishIDs("confirmed", "revision", "attempt")
	if err != nil || first != second {
		t.Fatalf("workspace publish identifiers are unstable: %#v %#v err=%v", first, second, err)
	}
	started, err := store.workspacePublishIDs("started", "revision", "attempt")
	if err != nil || started.event == first.event || started.outbox == first.outbox || started.publish == first.publish {
		t.Fatalf("workspace publish stages are not domain separated: %#v %#v err=%v", started, first, err)
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
