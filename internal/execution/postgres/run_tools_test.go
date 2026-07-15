package postgres

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestToolPlanValidationAndIdentifiers(t *testing.T) {
	claim := RunClaim{RunID: "run", TenantID: "tenant", UserID: "user", StoreEpoch: "epoch", RunVersion: 3, CommandID: "command", ConsumerName: "agent", RequestHash: "start-hash", JobID: "job", InboxID: "inbox", AttemptID: "attempt", Fence: 1, LeaseToken: "token", LeaseExpiresAt: time.Now().Add(time.Minute)}
	request := ToolRequest{ToolName: "web_search", DescriptorSnapshotID: "web_search@v1", NormalizedInputRef: "encrypted://input", RequestHash: "request-one", EffectClass: "read_only", Required: true, QueueClass: "interactive", ResourceClass: "tool-network", Priority: 50, CostUnits: 2, MaxAttempts: 3, RequestedEvent: PayloadPointer{Ref: "encrypted://event", Hash: "event"}, ExecuteCommand: PayloadPointer{Ref: "encrypted://command", Hash: "command"}}
	valid := RequestToolsCommand{Claim: claim, ExpectedRunVersion: 3, StepID: "step-1", JoinPolicy: "all", QuorumCount: 1, ToolRequests: []ToolRequest{request}, PlanResultHash: "plan-result", Actor: json.RawMessage(`{"kind":"service"}`), CorrelationID: "correlation", AttemptCompletedEvent: PayloadPointer{Ref: "encrypted://attempt", Hash: "attempt"}}
	if !validRequestTools(valid) {
		t.Fatal("valid tool plan rejected")
	}
	previewRequest := request
	previewRequest.RequiresPreview = true
	previewRequest.EffectClass = "reconcilable_write"
	previewRequest.EffectKey = "workspace:preview"
	previewRequest.EffectScope = "tenant:workspace:preview"
	previewRequest.ProviderID = "workspace"
	previewRequest.ExecuteCommand = PayloadPointer{}
	previewRequest.PreviewCommand = PayloadPointer{Ref: "encrypted://preview", Hash: "preview"}
	preview := valid
	preview.ToolRequests = []ToolRequest{previewRequest}
	if !validRequestTools(preview) {
		t.Fatal("valid preview plan rejected")
	}
	inlineRequest := request
	inlineRequest.ToolName = "memory_write"
	inlineRequest.EffectClass = "idempotent_write"
	inlineRequest.EffectKey = "memory:preference:1"
	inlineRequest.EffectScope = "tenant:user:1"
	inlineRequest.ProviderID = "platform-memory"
	inlineRequest.ExecutionMode = "inline_platform"
	inlineRequest.InlineInput = struct{}{}
	inlineRequest.SucceededEvent = PayloadPointer{Ref: "encrypted://inline-succeeded", Hash: "inline-succeeded"}
	inlineRequest.ExecuteCommand = PayloadPointer{}
	inlineRequest.QueueClass = ""
	inlineRequest.ResourceClass = ""
	inlineRequest.Priority = 0
	inlineRequest.CostUnits = 0
	inlineRequest.MaxAttempts = 0
	inline := valid
	inline.ToolRequests = []ToolRequest{inlineRequest}
	inline.GroupJoinedEvent = PayloadPointer{Ref: "encrypted://inline-group", Hash: "inline-group"}
	inline.RunResumeQueuedEvent = PayloadPointer{Ref: "encrypted://inline-resume-queued", Hash: "inline-resume-queued"}
	inline.ResumeCommand = PayloadPointer{Ref: "encrypted://inline-resume", Hash: "inline-resume"}
	inline.ResumeQueueClass = "interactive"
	inline.ResumeResourceClass = "llm"
	inline.ResumePriority = 50
	inline.ResumeCostUnits = 1
	inline.ResumeMaxAttempts = 5
	if !validRequestTools(inline) {
		t.Fatal("valid inline platform tool plan rejected")
	}
	invalidInline := inline
	invalidInline.ToolRequests = append([]ToolRequest(nil), inline.ToolRequests...)
	invalidInline.ToolRequests[0].ExecuteCommand = request.ExecuteCommand
	if validRequestTools(invalidInline) {
		t.Fatal("inline platform tool with worker command accepted")
	}
	mixed := preview
	mixed.ToolRequests = []ToolRequest{previewRequest, request}
	mixed.QuorumCount = 2
	if validRequestTools(mixed) {
		t.Fatal("mixed execution and approval-preview group accepted")
	}
	previewAny := preview
	previewAny.JoinPolicy = "any"
	if validRequestTools(previewAny) {
		t.Fatal("approval-preview group with non-all join accepted")
	}
	invalidPreview := preview
	invalidPreview.ToolRequests = append([]ToolRequest(nil), preview.ToolRequests...)
	invalidPreview.ToolRequests[0].ExecuteCommand = request.ExecuteCommand
	if validRequestTools(invalidPreview) {
		t.Fatal("preview plan with execute command accepted")
	}
	for name, mutate := range map[string]func(*RequestToolsCommand){
		"no required": func(command *RequestToolsCommand) { command.ToolRequests[0].Required = false },
		"bad quorum":  func(command *RequestToolsCommand) { command.QuorumCount = 2 },
		"write no key": func(command *RequestToolsCommand) {
			command.ToolRequests[0].EffectClass = "idempotent_write"
		},
		"write no scope": func(command *RequestToolsCommand) {
			command.ToolRequests[0].EffectClass = "idempotent_write"
			command.ToolRequests[0].EffectKey = "effect"
			command.ToolRequests[0].ProviderID = "provider"
		},
		"read with key":      func(command *RequestToolsCommand) { command.ToolRequests[0].EffectKey = "unexpected" },
		"read with provider": func(command *RequestToolsCommand) { command.ToolRequests[0].ProviderID = "unexpected" },
		"unknown queue": func(command *RequestToolsCommand) {
			command.ToolRequests[0].QueueClass = "urgent"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.ToolRequests = append([]ToolRequest(nil), valid.ToolRequests...)
			mutate(&candidate)
			if validRequestTools(candidate) {
				t.Fatal("invalid tool plan accepted")
			}
		})
	}
	duplicate := valid
	duplicate.ToolRequests = []ToolRequest{request, request}
	duplicate.QuorumCount = 2
	if validRequestTools(duplicate) {
		t.Fatal("duplicate request hash accepted")
	}
	store := RunStore{IDKey: bytes.Repeat([]byte{0x91}, 32)}
	first, firstGroup, err := store.toolRequestIdentifiers(claim.RunID, claim.RunVersion, valid.ToolRequests)
	if err != nil {
		t.Fatal(err)
	}
	second, secondGroup, err := store.toolRequestIdentifiers(claim.RunID, claim.RunVersion, valid.ToolRequests)
	if err != nil || firstGroup != secondGroup || first[0] != second[0] {
		t.Fatalf("unstable identifiers first=%#v/%s second=%#v/%s error=%v", first, firstGroup, second, secondGroup, err)
	}
	previewIDs, previewGroup, err := store.toolRequestIdentifiers(claim.RunID, claim.RunVersion, preview.ToolRequests)
	if err != nil || previewIDs[0] == first[0] || previewGroup == firstGroup {
		t.Fatalf("preview identifiers are not mode separated: %#v/%s %#v/%s error=%v", previewIDs, previewGroup, first, firstGroup, err)
	}
	seen := map[string]bool{firstGroup: true}
	for _, value := range []string{first[0].toolCall, first[0].effect, first[0].command, first[0].job, first[0].event, first[0].publishOutbox, first[0].publishCommand, first[0].executeOutbox} {
		if seen[value] {
			t.Fatalf("identifier domain collision: %s", value)
		}
		seen[value] = true
	}
	completion, err := store.inlineToolCompletionIdentifiers(first[0].toolCall)
	if err != nil || completion.event == "" || completion.event == first[0].event || completion.outbox == completion.publish {
		t.Fatalf("invalid inline completion identifiers: %#v error=%v", completion, err)
	}
}
