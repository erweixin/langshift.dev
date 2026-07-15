package agentworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/langshift/lites/internal/behavior"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	"github.com/langshift/lites/internal/toolworker"
)

var ErrProductionPlan = errors.New("production agent result plan is invalid")

type PlanSchedule struct {
	QueueClass, ResourceClass string
	Priority, MaxAttempts     int
	CostUnits                 int64
}

type ProductionResultPlanBuilder struct {
	Payloads     payload.Store
	IDKey        []byte
	ApprovalTTL  time.Duration
	Notification PlanSchedule
	Now          func() time.Time
	MaximumCalls int
}

func (builder ProductionResultPlanBuilder) BuildTools(ctx context.Context, execution Execution, turn TurnResult, _ MessageDocument, calls []ValidatedToolCall) (executionpostgres.RequestToolsCommand, error) {
	if !builder.valid() || !validPlanInput(execution, turn, calls, builder.maximumCalls()) {
		return executionpostgres.RequestToolsCommand{}, ErrProductionPlan
	}
	stepID, err := builder.stepID(execution, turn, "tools")
	if err != nil {
		return executionpostgres.RequestToolsCommand{}, err
	}
	preview := calls[0].Tool.RequiresPreview
	plan := executionpostgres.RequestToolsCommand{StepID: stepID, JoinPolicy: "all", QuorumCount: len(calls), PlanResultHash: turn.Provider.ResponseHash}
	for index, call := range calls {
		if call.Tool.ExecutionKind != ToolExecutionWorker || call.Tool.ManualApprovalRequired != call.Tool.RequiresPreview || call.Tool.RequiresPreview != preview {
			return executionpostgres.RequestToolsCommand{}, ErrProductionPlan
		}
		planned, idErr := executionpostgres.ToolRequestPlanningIDs(builder.IDKey, execution.Claim.RunID, execution.Claim.RunVersion, index, call.RequestHash, preview)
		if idErr != nil {
			return executionpostgres.RequestToolsCommand{}, idErr
		}
		input, inputHash, putErr := builder.putInput(ctx, execution.Claim.TenantID, planned.ToolCallID, call.Input)
		if putErr != nil {
			return executionpostgres.RequestToolsCommand{}, putErr
		}
		commandClass := "tool-execute-command"
		if preview {
			commandClass = "tool-preview-command"
		}
		command, putErr := builder.putToolCommand(ctx, execution.Claim.TenantID, planned.ToolCallID, planned.CommandID, commandClass, execution, call, input, inputHash)
		if putErr != nil {
			return executionpostgres.RequestToolsCommand{}, putErr
		}
		eventType := "tool_call_requested"
		if preview {
			eventType = "tool_call_preview_requested"
		}
		requested, putErr := builder.putEvent(ctx, execution.Claim.TenantID, planned.ToolCallID+":requested", eventType, builder.toolEvidence(execution, turn, call, planned.ToolCallID, input, inputHash))
		if putErr != nil {
			return executionpostgres.RequestToolsCommand{}, putErr
		}
		request := executionpostgres.ToolRequest{
			ToolName: call.Tool.Name, DescriptorSnapshotID: call.Tool.DescriptorSnapshotID,
			NormalizedInputRef: input.Ref, RequestHash: call.RequestHash,
			EffectClass: call.Tool.EffectClass, EffectKey: call.Tool.EffectKey,
			EffectScope: call.Tool.EffectScope, ProviderID: call.Tool.ProviderID,
			Required: true, QueueClass: call.Tool.QueueClass, ResourceClass: call.Tool.ResourceClass,
			Priority: call.Tool.Priority, CostUnits: call.Tool.CostUnits, MaxAttempts: call.Tool.MaxAttempts,
			RequiresPreview: preview, RequestedEvent: requested,
		}
		if preview {
			request.PreviewCommand = command
		} else {
			request.ExecuteCommand = command
		}
		plan.ToolRequests = append(plan.ToolRequests, request)
	}
	plan.AttemptCompletedEvent, err = builder.putEvent(ctx, execution.Claim.TenantID, execution.Claim.AttemptID+":tool-plan-completed", "job_attempt_completed", map[string]any{"run_id": execution.Claim.RunID, "step_id": stepID, "response_hash": turn.Provider.ResponseHash, "tool_count": len(calls)})
	return plan, err
}

func (builder ProductionResultPlanBuilder) BuildApprovals(ctx context.Context, execution Execution, turn TurnResult, _ MessageDocument, calls []ValidatedToolCall) (executionpostgres.ProposeDirectToolsCommand, error) {
	if !builder.valid() || !validPlanInput(execution, turn, calls, builder.maximumCalls()) || builder.ApprovalTTL < time.Minute || builder.ApprovalTTL > 24*time.Hour || !validPlanSchedule(builder.Notification) {
		return executionpostgres.ProposeDirectToolsCommand{}, ErrProductionPlan
	}
	stepID, err := builder.stepID(execution, turn, "direct-approval")
	if err != nil {
		return executionpostgres.ProposeDirectToolsCommand{}, err
	}
	permission := calls[0].Tool.PermissionSnapshot
	plan := executionpostgres.ProposeDirectToolsCommand{
		StepID: stepID, PermissionSnapshot: permission, ApprovalExpiresAt: builder.now().Add(builder.ApprovalTTL),
		PlanResultHash: turn.Provider.ResponseHash, NotifyQueueClass: builder.Notification.QueueClass,
		NotifyResourceClass: builder.Notification.ResourceClass, NotifyPriority: builder.Notification.Priority,
		NotifyCostUnits: builder.Notification.CostUnits, NotifyMaxAttempts: builder.Notification.MaxAttempts,
	}
	for index, call := range calls {
		if call.Tool.ExecutionKind != ToolExecutionWorker || !call.Tool.ManualApprovalRequired || call.Tool.RequiresPreview || call.Tool.PermissionSnapshot != permission {
			return executionpostgres.ProposeDirectToolsCommand{}, ErrProductionPlan
		}
		planned, idErr := executionpostgres.DirectApprovalPlanningIDs(builder.IDKey, execution.Claim.RunID, execution.Claim.RunVersion, stepID, index, call.RequestHash)
		if idErr != nil {
			return executionpostgres.ProposeDirectToolsCommand{}, idErr
		}
		input, inputHash, putErr := builder.putInput(ctx, execution.Claim.TenantID, planned.ToolCallID, call.Input)
		if putErr != nil {
			return executionpostgres.ProposeDirectToolsCommand{}, putErr
		}
		execute, putErr := builder.putToolCommand(ctx, execution.Claim.TenantID, planned.ToolCallID, planned.ExecuteCommandID, "tool-execute-command", execution, call, input, inputHash)
		if putErr != nil {
			return executionpostgres.ProposeDirectToolsCommand{}, putErr
		}
		policy := policySnapshot(call.Tool)
		scope, putErr := builder.putJSON(ctx, payload.Descriptor{TenantID: execution.Claim.TenantID, ObjectID: planned.ApprovalID, Class: "approval-scope", ContentType: "application/json"}, map[string]any{
			"schema_version": 1, "run_id": execution.Claim.RunID, "tool_call_id": planned.ToolCallID,
			"tool_name": call.Tool.Name, "descriptor_snapshot_id": call.Tool.DescriptorSnapshotID,
			"descriptor_hash": call.Tool.DescriptorHash, "normalized_input_ref": input.Ref,
			"normalized_input_hash": inputHash, "request_hash": call.RequestHash,
			"effect_class": call.Tool.EffectClass, "effect_key": call.Tool.EffectKey,
			"effect_scope": call.Tool.EffectScope, "provider_id": call.Tool.ProviderID,
			"policy_snapshot": policy, "permission_snapshot": permission,
		})
		if putErr != nil {
			return executionpostgres.ProposeDirectToolsCommand{}, putErr
		}
		request := executionpostgres.DirectApprovalToolRequest{
			ToolName: call.Tool.Name, DescriptorSnapshotID: call.Tool.DescriptorSnapshotID,
			NormalizedInputRef: input.Ref, RequestHash: call.RequestHash,
			EffectClass: call.Tool.EffectClass, EffectKey: call.Tool.EffectKey,
			EffectScope: call.Tool.EffectScope, ProviderID: call.Tool.ProviderID,
			PolicySnapshot: policy, ScopeSnapshot: scope, ExecuteCommand: execute,
			QueueClass: call.Tool.QueueClass, ResourceClass: call.Tool.ResourceClass,
			Priority: call.Tool.Priority, CostUnits: call.Tool.CostUnits, MaxAttempts: call.Tool.MaxAttempts,
		}
		request.ProposalHash, err = executionpostgres.DirectToolProposalHash(request, permission)
		if err != nil {
			return executionpostgres.ProposeDirectToolsCommand{}, err
		}
		evidence := builder.toolEvidence(execution, turn, call, planned.ToolCallID, input, inputHash)
		evidence["approval_id"], evidence["proposal_hash"], evidence["scope_snapshot_hash"] = planned.ApprovalID, request.ProposalHash, scope.Hash
		request.ToolProposedEvent, err = builder.putEvent(ctx, execution.Claim.TenantID, planned.ToolCallID+":proposed", "tool_call_proposed", evidence)
		if err != nil {
			return executionpostgres.ProposeDirectToolsCommand{}, err
		}
		request.ApprovalRequestedEvent, err = builder.putEvent(ctx, execution.Claim.TenantID, planned.ApprovalID+":requested", "approval_requested", evidence)
		if err != nil {
			return executionpostgres.ProposeDirectToolsCommand{}, err
		}
		request.NotifyApproval, err = builder.putJSON(ctx, payload.Descriptor{TenantID: execution.Claim.TenantID, ObjectID: planned.NotifyCommandID, Class: "approval-notification-command", ContentType: "application/json"}, map[string]any{"schema_version": 1, "approval_id": planned.ApprovalID, "run_id": execution.Claim.RunID, "correlation_id": execution.Payload.CorrelationID, "proposal_hash": request.ProposalHash})
		if err != nil {
			return executionpostgres.ProposeDirectToolsCommand{}, err
		}
		plan.ToolRequests = append(plan.ToolRequests, request)
	}
	plan.RunWaitingApprovalEvent, err = builder.putEvent(ctx, execution.Claim.TenantID, execution.Claim.RunID+":"+stepID+":waiting-approval", "run_waiting_approval", map[string]any{"run_id": execution.Claim.RunID, "step_id": stepID, "tool_count": len(calls)})
	if err != nil {
		return executionpostgres.ProposeDirectToolsCommand{}, err
	}
	plan.AttemptCompletedEvent, err = builder.putEvent(ctx, execution.Claim.TenantID, execution.Claim.AttemptID+":approval-plan-completed", "job_attempt_completed", map[string]any{"run_id": execution.Claim.RunID, "step_id": stepID, "response_hash": turn.Provider.ResponseHash})
	return plan, err
}

type childPlanInput struct {
	Profile          string          `json:"profile"`
	Environment      string          `json:"environment"`
	Budget           json.RawMessage `json:"budget"`
	BudgetMicrounits int64           `json:"budget_microunits"`
	TimeoutSeconds   int64           `json:"timeout_seconds"`
	Required         bool            `json:"required"`
}

func (builder ProductionResultPlanBuilder) BuildChildren(ctx context.Context, execution Execution, turn TurnResult, _ MessageDocument, calls []ValidatedToolCall) (executionpostgres.SpawnChildRunsCommand, error) {
	if !builder.valid() || !validPlanInput(execution, turn, calls, 10) {
		return executionpostgres.SpawnChildRunsCommand{}, ErrProductionPlan
	}
	stepID, err := builder.stepID(execution, turn, "children")
	if err != nil {
		return executionpostgres.SpawnChildRunsCommand{}, err
	}
	plan := executionpostgres.SpawnChildRunsCommand{StepID: stepID, JoinPolicy: "all", PlanResultHash: turn.Provider.ResponseHash}
	required := 0
	for index, call := range calls {
		if call.Tool.ExecutionKind != ToolExecutionChild || call.Tool.Name != "spawn_agent_run" || call.Tool.EffectClass != "read_only" {
			return executionpostgres.SpawnChildRunsCommand{}, ErrProductionPlan
		}
		input, decodeErr := decodeChildInput(call.Input)
		if decodeErr != nil {
			return executionpostgres.SpawnChildRunsCommand{}, decodeErr
		}
		childRunID, idErr := ids.DeterministicUUID(builder.IDKey, "agent-child-run", execution.Claim.AttemptID+"\x00"+call.ProviderCallID+"\x00"+call.RequestHash)
		if idErr != nil {
			return executionpostgres.SpawnChildRunsCommand{}, idErr
		}
		toolCallID, idErr := executionpostgres.ChildToolCallPlanningID(builder.IDKey, execution.Claim.RunID, execution.Claim.RunVersion, stepID, index, childRunID, call.RequestHash)
		if idErr != nil {
			return executionpostgres.SpawnChildRunsCommand{}, idErr
		}
		inputManifest, inputHash, putErr := builder.putInput(ctx, execution.Claim.TenantID, toolCallID, call.Input)
		if putErr != nil {
			return executionpostgres.SpawnChildRunsCommand{}, putErr
		}
		startCommandID, idErr := executionpostgres.RunStartCommandID(builder.IDKey, childRunID)
		if idErr != nil {
			return executionpostgres.SpawnChildRunsCommand{}, idErr
		}
		start, putErr := builder.putJSON(ctx, payload.Descriptor{TenantID: execution.Claim.TenantID, ObjectID: startCommandID, Class: "agent-run-command", ContentType: "application/json"}, CommandPayload{SchemaVersion: 1, RunID: childRunID, CorrelationID: execution.Payload.CorrelationID})
		if putErr != nil {
			return executionpostgres.SpawnChildRunsCommand{}, putErr
		}
		evidence := builder.toolEvidence(execution, turn, call, toolCallID, inputManifest, inputHash)
		evidence["child_run_id"], evidence["behavior_profile"], evidence["behavior_environment"] = childRunID, input.Profile, input.Environment
		toolEvent, putErr := builder.putEvent(ctx, execution.Claim.TenantID, toolCallID+":succeeded", "tool_call_succeeded", evidence)
		if putErr != nil {
			return executionpostgres.SpawnChildRunsCommand{}, putErr
		}
		accepted, putErr := builder.putEvent(ctx, execution.Claim.TenantID, childRunID+":accepted", "run_accepted", evidence)
		if putErr != nil {
			return executionpostgres.SpawnChildRunsCommand{}, putErr
		}
		queued, putErr := builder.putEvent(ctx, execution.Claim.TenantID, childRunID+":queued", "run_queued", evidence)
		if putErr != nil {
			return executionpostgres.SpawnChildRunsCommand{}, putErr
		}
		plan.Children = append(plan.Children, executionpostgres.ChildRunRequest{
			RunID: childRunID, RequestHash: call.RequestHash, DescriptorSnapshotID: call.Tool.DescriptorSnapshotID,
			NormalizedInputRef: inputManifest.Ref, BehaviorProfile: input.Profile, BehaviorEnvironment: input.Environment,
			BudgetSnapshot: input.Budget, BudgetMicrounits: input.BudgetMicrounits,
			DueAt: builder.now().Add(time.Duration(input.TimeoutSeconds) * time.Second), Required: input.Required,
			QueueClass: call.Tool.QueueClass, ResourceClass: call.Tool.ResourceClass,
			Priority: call.Tool.Priority, CostUnits: call.Tool.CostUnits, MaxAttempts: call.Tool.MaxAttempts,
			ToolSucceededEvent: toolEvent, AcceptedEvent: accepted, QueuedEvent: queued, StartCommand: start,
		})
		if input.Required {
			required++
		}
	}
	if required == 0 {
		return executionpostgres.SpawnChildRunsCommand{}, ErrProductionPlan
	}
	plan.QuorumCount = required
	plan.AttemptCompletedEvent, err = builder.putEvent(ctx, execution.Claim.TenantID, execution.Claim.AttemptID+":child-plan-completed", "job_attempt_completed", map[string]any{"run_id": execution.Claim.RunID, "step_id": stepID, "response_hash": turn.Provider.ResponseHash})
	if err != nil {
		return executionpostgres.SpawnChildRunsCommand{}, err
	}
	plan.ParentWaitingEvent, err = builder.putEvent(ctx, execution.Claim.TenantID, execution.Claim.RunID+":"+stepID+":waiting-child", "run_waiting_child", map[string]any{"run_id": execution.Claim.RunID, "step_id": stepID, "child_count": len(calls)})
	return plan, err
}

func decodeChildInput(encoded json.RawMessage) (childPlanInput, error) {
	var input childPlanInput
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || !behavior.Profile(input.Profile).Valid() || input.Environment != "staging" && input.Environment != "production" || len(input.Budget) == 0 || !json.Valid(input.Budget) || input.BudgetMicrounits < 1 || input.TimeoutSeconds < 5 || input.TimeoutSeconds > 3600 {
		return childPlanInput{}, ErrProductionPlan
	}
	var budget struct {
		MaxCostMicrounits int64 `json:"max_cost_microunits"`
	}
	if json.Unmarshal(input.Budget, &budget) != nil || budget.MaxCostMicrounits != input.BudgetMicrounits {
		return childPlanInput{}, ErrProductionPlan
	}
	return input, nil
}

func (builder ProductionResultPlanBuilder) putToolCommand(ctx context.Context, tenantID, toolCallID, commandID, class string, execution Execution, call ValidatedToolCall, input payload.Manifest, inputHash string) (executionpostgres.PayloadPointer, error) {
	return builder.putJSON(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: commandID, Class: class, ContentType: "application/json"}, toolworker.CommandPayload{
		SchemaVersion: 1, ToolCallID: toolCallID, RunID: execution.Claim.RunID,
		CorrelationID: execution.Payload.CorrelationID, ToolName: call.Tool.Name,
		DescriptorSnapshotID: call.Tool.DescriptorSnapshotID, DescriptorHash: call.Tool.DescriptorHash,
		Input: input, NormalizedInputHash: inputHash, RequestHash: call.RequestHash,
		EffectClass: call.Tool.EffectClass, EffectKey: call.Tool.EffectKey,
		EffectScope: call.Tool.EffectScope, ProviderID: call.Tool.ProviderID,
	})
}

func (builder ProductionResultPlanBuilder) putInput(ctx context.Context, tenantID, toolCallID string, input json.RawMessage) (payload.Manifest, string, error) {
	hash := sha256Hex(input)
	manifest, err := builder.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: toolCallID, Class: "tool-normalized-input", ContentType: "application/json"}, input)
	return manifest, hash, err
}

func (builder ProductionResultPlanBuilder) putEvent(ctx context.Context, tenantID, objectID, eventType string, data any) (executionpostgres.PayloadPointer, error) {
	return builder.putJSON(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: "event-payload", ContentType: "application/json"}, struct {
		EventType string `json:"event_type"`
		Data      any    `json:"data"`
	}{eventType, data})
}

func (builder ProductionResultPlanBuilder) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (executionpostgres.PayloadPointer, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return executionpostgres.PayloadPointer{}, err
	}
	manifest, err := builder.Payloads.Put(ctx, descriptor, encoded)
	return executionpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, err
}

func (builder ProductionResultPlanBuilder) toolEvidence(execution Execution, turn TurnResult, call ValidatedToolCall, toolCallID string, input payload.Manifest, inputHash string) map[string]any {
	return map[string]any{
		"schema_version": 1, "run_id": execution.Claim.RunID, "tool_call_id": toolCallID,
		"provider_call_id": call.ProviderCallID, "llm_attempt_id": turn.Plan.LLMAttemptID,
		"provider_attempt_id": turn.SelectedAttemptID, "tool_name": call.Tool.Name,
		"descriptor_snapshot_id": call.Tool.DescriptorSnapshotID, "descriptor_hash": call.Tool.DescriptorHash,
		"normalized_input_ref": input.Ref, "normalized_input_hash": inputHash,
		"request_hash": call.RequestHash, "effect_class": call.Tool.EffectClass,
		"policy_snapshot": policySnapshot(call.Tool), "permission_snapshot": call.Tool.PermissionSnapshot,
	}
}

func policySnapshot(tool ResolvedTool) string {
	return fmt.Sprintf("%s:v%d:sha256:%s", tool.PolicySnapshotID, tool.PolicyVersion, tool.PolicyHash)
}

func validPlanInput(execution Execution, turn TurnResult, calls []ValidatedToolCall, maximum int) bool {
	return execution.Claim.RunID != "" && execution.Claim.RunVersion > 0 && execution.Claim.AttemptID != "" && execution.Payload.CorrelationID != "" && turn.Provider.ResponseHash != "" && len(calls) > 0 && len(calls) <= maximum
}

func (builder ProductionResultPlanBuilder) valid() bool {
	return builder.Payloads != nil && len(builder.IDKey) >= 32 && builder.maximumCalls() > 0 && builder.maximumCalls() <= 32
}

func (builder ProductionResultPlanBuilder) maximumCalls() int {
	if builder.MaximumCalls == 0 {
		return 32
	}
	return builder.MaximumCalls
}

func validPlanSchedule(schedule PlanSchedule) bool {
	return (schedule.QueueClass == "interactive" || schedule.QueueClass == "background") && schedule.ResourceClass != "" && schedule.Priority >= 0 && schedule.Priority <= 1000 && schedule.CostUnits > 0 && schedule.CostUnits <= 1_000_000_000_000 && schedule.MaxAttempts > 0 && schedule.MaxAttempts <= 100
}

func (builder ProductionResultPlanBuilder) stepID(execution Execution, turn TurnResult, kind string) (string, error) {
	return ids.DeterministicUUID(builder.IDKey, "agent-result-step:"+kind, execution.Claim.AttemptID+"\x00"+turn.Provider.ResponseHash)
}

func (builder ProductionResultPlanBuilder) now() time.Time {
	if builder.Now != nil {
		return builder.Now().UTC()
	}
	return time.Now().UTC()
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
