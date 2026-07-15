package postgres

import (
	"fmt"

	"github.com/langshift/lites/internal/platform/ids"
)

// PlannedToolRequestIDs are the aggregate and command IDs AgentWorker must use
// when encrypting the payloads later consumed by execution workers.
type PlannedToolRequestIDs struct {
	ToolCallID string
	CommandID  string
}

func ToolRequestPlanningIDs(idKey []byte, runID string, runVersion uint64, index int, requestHash string, preview bool) (PlannedToolRequestIDs, error) {
	if len(idKey) < 32 || runID == "" || runVersion == 0 || index < 0 || requestHash == "" {
		return PlannedToolRequestIDs{}, ErrInvalidCommand
	}
	scope := fmt.Sprintf("%s\x00%d\x00%d\x00%s", runID, runVersion, index, requestHash)
	toolDomain, commandDomain := "tool-call", "execute-tool-command"
	if preview {
		toolDomain, commandDomain = "preview-tool-call", "prepare-tool-preview-command"
	}
	toolCallID, err := ids.DeterministicUUID(idKey, toolDomain, scope)
	if err != nil {
		return PlannedToolRequestIDs{}, err
	}
	commandID, err := ids.DeterministicUUID(idKey, commandDomain, scope)
	return PlannedToolRequestIDs{ToolCallID: toolCallID, CommandID: commandID}, err
}

type PlannedDirectApprovalIDs struct {
	ToolCallID, ApprovalID, ExecuteCommandID, NotifyCommandID string
}

func DirectApprovalPlanningIDs(idKey []byte, runID string, runVersion uint64, stepID string, index int, requestHash string) (PlannedDirectApprovalIDs, error) {
	if len(idKey) < 32 || runID == "" || runVersion == 0 || stepID == "" || index < 0 || requestHash == "" {
		return PlannedDirectApprovalIDs{}, ErrInvalidCommand
	}
	scope := fmt.Sprintf("%s\x00%d\x00%s", runID, runVersion, stepID)
	seed := fmt.Sprintf("%s\x00%d\x00%s", scope, index, requestHash)
	toolCallID, err := ids.DeterministicUUID(idKey, "direct-approval:tool", seed)
	if err != nil {
		return PlannedDirectApprovalIDs{}, err
	}
	approvalID, err := ids.DeterministicUUID(idKey, "direct-approval:approval", seed)
	if err != nil {
		return PlannedDirectApprovalIDs{}, err
	}
	executeCommandID, err := ids.DeterministicUUID(idKey, "direct-approval-authorize:execute-command", approvalID)
	if err != nil {
		return PlannedDirectApprovalIDs{}, err
	}
	notifyCommandID, err := ids.DeterministicUUID(idKey, "direct-approval:notify:command", seed)
	return PlannedDirectApprovalIDs{ToolCallID: toolCallID, ApprovalID: approvalID, ExecuteCommandID: executeCommandID, NotifyCommandID: notifyCommandID}, err
}

func ChildToolCallPlanningID(idKey []byte, parentRunID string, runVersion uint64, stepID string, index int, childRunID, requestHash string) (string, error) {
	if len(idKey) < 32 || parentRunID == "" || runVersion == 0 || stepID == "" || index < 0 || childRunID == "" || requestHash == "" {
		return "", ErrInvalidCommand
	}
	scope := fmt.Sprintf("%s\x00%d\x00%s\x00%d\x00%s\x00%s", parentRunID, runVersion, stepID, index, childRunID, requestHash)
	return ids.DeterministicUUID(idKey, "spawn-child-tool-call", scope)
}

func RunStartCommandID(idKey []byte, runID string) (string, error) {
	if len(idKey) < 32 || runID == "" {
		return "", ErrInvalidCommand
	}
	return ids.DeterministicUUID(idKey, "run-start-command", runID)
}
