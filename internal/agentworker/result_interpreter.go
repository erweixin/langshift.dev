package agentworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/platform/ids"
)

var (
	ErrResultConfiguration = errors.New("agent result interpreter configuration is invalid")
	ErrResultIntegrity     = errors.New("agent provider result is not bound to its durable turn")
	ErrResultRepair        = errors.New("agent provider result requires a bounded repair turn")
	ErrResultPlan          = errors.New("agent result action plan is invalid")
)

type ToolExecutionKind string

const (
	ToolExecutionWorker ToolExecutionKind = "worker"
	ToolExecutionChild  ToolExecutionKind = "child_run"
)

type ResolvedTool struct {
	Name, DescriptorSnapshotID, DescriptorHash string
	ExecutionKind                              ToolExecutionKind
	ManualApprovalRequired                     bool
	RequiresPreview                            bool
}

type ValidatedToolCall struct {
	ProviderCallID string
	Tool           ResolvedTool
	Input          json.RawMessage
	RequestHash    string
}

// ToolResolver must resolve only the exact immutable binding supplied by the
// context manifest, validate the input schema, return canonical JSON, and
// apply current deny-only policy overlays. It cannot upgrade permissions.
type ToolResolver interface {
	ResolveAndNormalize(context.Context, Execution, llmpostgres.SnapshotBinding, provider.Tool, provider.ToolCall) (ResolvedTool, json.RawMessage, string, error)
}

// ResultPlanBuilder converts validated calls into one of the execution
// kernel's atomic commands. It also receives the exact assistant tool-use
// message so the plan can bind continuation context to the provider result.
type ResultPlanBuilder interface {
	BuildTools(context.Context, Execution, TurnResult, MessageDocument, []ValidatedToolCall) (executionpostgres.RequestToolsCommand, error)
	BuildChildren(context.Context, Execution, TurnResult, MessageDocument, []ValidatedToolCall) (executionpostgres.SpawnChildRunsCommand, error)
}

type ResultInterpreter struct {
	Tools    ToolResolver
	Plans    ResultPlanBuilder
	IDKey    []byte
	MaxCalls int
}

func (interpreter ResultInterpreter) Interpret(ctx context.Context, execution Execution, turn TurnResult) (Outcome, error) {
	if interpreter.Tools == nil || interpreter.Plans == nil || len(interpreter.IDKey) < 32 || interpreter.maximumCalls() < 1 || interpreter.maximumCalls() > 32 {
		return Outcome{}, ErrResultConfiguration
	}
	if err := validateDurableTurn(execution, turn); err != nil {
		return Outcome{}, err
	}
	result := turn.Provider
	if len(result.ToolCalls) == 0 {
		if result.FinishReason != "stop" || result.Text == "" || result.Refusal != "" {
			return Outcome{}, ErrResultRepair
		}
		messageID, err := ids.DeterministicUUID(interpreter.IDKey, "agent-result-message", execution.Claim.AttemptID+"\x00"+result.ResponseHash)
		if err != nil {
			return Outcome{}, ErrResultConfiguration
		}
		return Outcome{
			State: statemachine.RunSucceeded, ResultHash: result.ResponseHash,
			RunEvent:     map[string]any{"llm_attempt_id": turn.Plan.LLMAttemptID, "provider_attempt_id": turn.SelectedAttemptID, "message_id": messageID, "response_hash": result.ResponseHash},
			AttemptEvent: map[string]any{"llm_attempt_id": turn.Plan.LLMAttemptID, "provider_attempt_id": turn.SelectedAttemptID, "input_tokens": result.InputTokens, "output_tokens": result.OutputTokens},
			MessageID:    messageID,
			Message:      &MessageDocument{SchemaVersion: 1, Role: "assistant", Content: []provider.ContentBlock{{Type: "text", Text: result.Text}}},
		}, nil
	}
	if result.FinishReason != "tool_use" || result.Refusal != "" || len(result.ToolCalls) > interpreter.maximumCalls() {
		return Outcome{}, ErrResultRepair
	}
	bindings, err := frozenToolBindings(turn.Plan)
	if err != nil {
		return Outcome{}, err
	}
	validated := make([]ValidatedToolCall, 0, len(result.ToolCalls))
	message := MessageDocument{SchemaVersion: 1, Role: "assistant"}
	if result.Text != "" {
		message.Content = append(message.Content, provider.ContentBlock{Type: "text", Text: result.Text})
	}
	seenCallIDs := map[string]bool{}
	kind := ToolExecutionKind("")
	for _, call := range result.ToolCalls {
		frozen, ok := bindings[call.Name]
		if call.ID == "" || call.Name == "" || len(call.Input) == 0 || seenCallIDs[call.ID] || !ok {
			return Outcome{}, ErrResultRepair
		}
		binding, advertised := frozen.SnapshotBinding, frozen.Tool
		resolved, normalized, requestHash, resolveErr := interpreter.Tools.ResolveAndNormalize(ctx, execution, binding, advertised, call)
		if resolveErr != nil {
			return Outcome{}, errors.Join(ErrResultRepair, resolveErr)
		}
		if !validResolvedTool(resolved, binding, call.Name) || len(normalized) == 0 || requestHash == "" || !json.Valid(normalized) {
			return Outcome{}, ErrResultIntegrity
		}
		if kind == "" {
			kind = resolved.ExecutionKind
		} else if kind != resolved.ExecutionKind {
			return Outcome{}, errors.Join(ErrResultRepair, fmt.Errorf("mixed tool execution kinds are not atomic"))
		}
		seenCallIDs[call.ID] = true
		validated = append(validated, ValidatedToolCall{ProviderCallID: call.ID, Tool: resolved, Input: append(json.RawMessage(nil), normalized...), RequestHash: requestHash})
		message.Content = append(message.Content, provider.ContentBlock{Type: "tool_use", ToolUseID: call.ID, ToolName: call.Name, ToolInput: append(json.RawMessage(nil), normalized...)})
	}
	messageID, err := ids.DeterministicUUID(interpreter.IDKey, "agent-result-message", execution.Claim.AttemptID+"\x00"+result.ResponseHash)
	if err != nil {
		return Outcome{}, ErrResultConfiguration
	}
	switch kind {
	case ToolExecutionWorker:
		plan, planErr := interpreter.Plans.BuildTools(ctx, execution, turn, message, validated)
		if planErr != nil || !validInterpretedToolPlan(plan, validated, result.ResponseHash) {
			return Outcome{}, errors.Join(ErrResultPlan, planErr)
		}
		return Outcome{State: statemachine.RunWaitingTool, MessageID: messageID, Message: &message, Tools: &plan}, nil
	case ToolExecutionChild:
		plan, planErr := interpreter.Plans.BuildChildren(ctx, execution, turn, message, validated)
		if planErr != nil || !validInterpretedChildPlan(plan, validated, result.ResponseHash) {
			return Outcome{}, errors.Join(ErrResultPlan, planErr)
		}
		return Outcome{State: statemachine.RunWaitingChild, MessageID: messageID, Message: &message, Children: &plan}, nil
	default:
		return Outcome{}, ErrResultIntegrity
	}
}

type TurnExecutor interface {
	Execute(context.Context, Execution) (TurnResult, error)
}

type TurnResultInterpreter interface {
	Interpret(context.Context, Execution, TurnResult) (Outcome, error)
}

type ProductionRunner struct {
	Turns   TurnExecutor
	Results TurnResultInterpreter
}

func (runner ProductionRunner) Execute(ctx context.Context, execution Execution) (Outcome, error) {
	if runner.Turns == nil || runner.Results == nil {
		return Outcome{}, ErrResultConfiguration
	}
	turn, err := runner.Turns.Execute(ctx, execution)
	if err != nil {
		return Outcome{}, err
	}
	return runner.Results.Interpret(ctx, execution, turn)
}

func validateDurableTurn(execution Execution, turn TurnResult) error {
	result := turn.Provider
	if turn.Plan.LLMAttemptID == "" || turn.Plan.LLMAttemptID != turn.Finalized.AttemptID || turn.Finalized.Status != "completed" || turn.SelectedAttemptID == "" || turn.SelectedAttemptID != turn.Finalized.SelectedProviderAttemptID || execution.Claim.RunID != execution.Payload.RunID || result.Status != "completed" || result.ProviderRequestID == "" || result.Model == "" || result.RequestHash == "" || result.ResponseHash == "" {
		return ErrResultIntegrity
	}
	return nil
}

func frozenToolBindings(plan TurnPlan) (map[string]struct {
	llmpostgres.SnapshotBinding
	provider.Tool
}, error) {
	if len(plan.Request.Tools) != len(plan.Manifest.Tools) {
		return nil, ErrResultIntegrity
	}
	result := make(map[string]struct {
		llmpostgres.SnapshotBinding
		provider.Tool
	}, len(plan.Request.Tools))
	for index, tool := range plan.Request.Tools {
		binding := plan.Manifest.Tools[index]
		if tool.Name == "" || binding.ID == "" || binding.Hash == "" {
			return nil, ErrResultIntegrity
		}
		if _, exists := result[tool.Name]; exists {
			return nil, ErrResultIntegrity
		}
		result[tool.Name] = struct {
			llmpostgres.SnapshotBinding
			provider.Tool
		}{binding, tool}
	}
	return result, nil
}

func validResolvedTool(tool ResolvedTool, binding llmpostgres.SnapshotBinding, name string) bool {
	return tool.Name == name && tool.DescriptorSnapshotID == binding.ID && tool.DescriptorHash == binding.Hash && (tool.ExecutionKind == ToolExecutionWorker || tool.ExecutionKind == ToolExecutionChild) && (!tool.RequiresPreview || tool.ManualApprovalRequired)
}

func validInterpretedToolPlan(plan executionpostgres.RequestToolsCommand, calls []ValidatedToolCall, resultHash string) bool {
	if plan.PlanResultHash != resultHash || len(plan.ToolRequests) != len(calls) {
		return false
	}
	for index, request := range plan.ToolRequests {
		call := calls[index]
		if request.ToolName != call.Tool.Name || request.DescriptorSnapshotID != call.Tool.DescriptorSnapshotID || request.RequestHash != call.RequestHash || request.RequiresPreview != call.Tool.RequiresPreview {
			return false
		}
		if call.Tool.ManualApprovalRequired && !request.RequiresPreview {
			return false
		}
	}
	return true
}

func validInterpretedChildPlan(plan executionpostgres.SpawnChildRunsCommand, calls []ValidatedToolCall, resultHash string) bool {
	if plan.PlanResultHash != resultHash || len(plan.Children) != len(calls) {
		return false
	}
	for index, child := range plan.Children {
		call := calls[index]
		if child.DescriptorSnapshotID != call.Tool.DescriptorSnapshotID || child.RequestHash != call.RequestHash {
			return false
		}
	}
	return true
}

func (interpreter ResultInterpreter) maximumCalls() int {
	if interpreter.MaxCalls == 0 {
		return 32
	}
	return interpreter.MaxCalls
}
