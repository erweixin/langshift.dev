package postgres

import (
	"bytes"
	"testing"
)

func TestPublicPlanningIdentifiersMatchKernelIdentifiers(t *testing.T) {
	store := RunStore{IDKey: bytes.Repeat([]byte{0xc1}, 32)}
	requests := []ToolRequest{{RequestHash: "request-one"}, {RequestHash: "request-two", RequiresPreview: false}}
	internal, _, err := store.toolRequestIdentifiers("run-1", 7, requests)
	if err != nil {
		t.Fatal(err)
	}
	for index, request := range requests {
		planned, planErr := ToolRequestPlanningIDs(store.IDKey, "run-1", 7, index, request.RequestHash, false)
		if planErr != nil || planned.ToolCallID != internal[index].toolCall || planned.CommandID != internal[index].command {
			t.Fatalf("tool ids drifted index=%d planned=%#v internal=%#v err=%v", index, planned, internal[index], planErr)
		}
	}
	previewRequests := []ToolRequest{{RequestHash: "preview-one", RequiresPreview: true}}
	previewInternal, _, err := store.toolRequestIdentifiers("run-1", 8, previewRequests)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := ToolRequestPlanningIDs(store.IDKey, "run-1", 8, 0, "preview-one", true)
	if err != nil || preview.ToolCallID != previewInternal[0].toolCall || preview.CommandID != previewInternal[0].command {
		t.Fatalf("preview ids drifted planned=%#v internal=%#v err=%v", preview, previewInternal[0], err)
	}
	directRequests := []DirectApprovalToolRequest{{RequestHash: "direct-one"}}
	_, directInternal, err := store.directApprovalIdentifiers("run-1", 9, "step-1", directRequests)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := DirectApprovalPlanningIDs(store.IDKey, "run-1", 9, "step-1", 0, "direct-one")
	if err != nil || direct.ToolCallID != directInternal[0].toolCall || direct.ApprovalID != directInternal[0].approval {
		t.Fatalf("direct ids drifted planned=%#v internal=%#v err=%v", direct, directInternal[0], err)
	}
	if direct.NotifyCommandID != directInternal[0].notifyCommand {
		t.Fatalf("direct notify id drifted planned=%#v internal=%#v", direct, directInternal[0])
	}
	authorized, err := store.directAuthorizationIdentifiers(direct.ApprovalID)
	if err != nil || direct.ExecuteCommandID != authorized.executeCommand {
		t.Fatalf("direct execute id drifted planned=%#v internal=%#v err=%v", direct, authorized, err)
	}
	children := []ChildRunRequest{{RunID: "child-1", RequestHash: "child-request"}}
	_, _, childInternal, err := store.childSpawnIdentifiers("run-1", 10, "step-child", children)
	if err != nil {
		t.Fatal(err)
	}
	childToolID, err := ChildToolCallPlanningID(store.IDKey, "run-1", 10, "step-child", 0, "child-1", "child-request")
	if err != nil || childToolID != childInternal[0].toolCall {
		t.Fatalf("child id drifted planned=%s internal=%#v err=%v", childToolID, childInternal[0], err)
	}
	runInternal, err := store.identifiers("child-1")
	if err != nil {
		t.Fatal(err)
	}
	startID, err := RunStartCommandID(store.IDKey, "child-1")
	if err != nil || startID != runInternal.startCommand {
		t.Fatalf("start id drifted planned=%s internal=%s err=%v", startID, runInternal.startCommand, err)
	}
}
