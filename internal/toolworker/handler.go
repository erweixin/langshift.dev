// Package toolworker owns the durable Tool Worker command lifecycle. It
// verifies encrypted command and normalized-input bindings, re-evaluates the
// deny-only execution overlay, installs a fenced ToolCall execution right,
// heartbeats that right, and commits one evidence-backed outcome.
package toolworker

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

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/toolreconciler"
)

const commandSchemaVersion = 1

var (
	ErrConfiguration = errors.New("tool worker configuration is invalid")
	ErrCommand       = errors.New("tool worker command is invalid")
	ErrPolicy        = errors.New("tool worker policy decision is invalid")
	ErrOutcome       = errors.New("tool worker outcome is invalid")
	ErrHeartbeatLost = errors.New("tool worker execution lease was lost")
)

type ToolStore interface {
	ClaimTool(context.Context, executionpostgres.ClaimToolCommand) (executionpostgres.ToolClaim, error)
	HeartbeatTool(context.Context, executionpostgres.ToolClaim) (executionpostgres.ToolClaim, error)
	CompleteReadOnlyTool(context.Context, executionpostgres.CompleteToolCommand) (executionpostgres.CompletedTool, error)
	CompleteEffectTool(context.Context, executionpostgres.CompleteToolCommand, executionpostgres.EffectCompletion) (executionpostgres.CompletedTool, error)
}

// CommandPayload contains only immutable execution metadata. The normalized
// input remains a separately encrypted, content-addressed object.
type CommandPayload struct {
	SchemaVersion        int              `json:"schema_version"`
	ToolCallID           string           `json:"tool_call_id"`
	RunID                string           `json:"run_id"`
	UserID               string           `json:"user_id"`
	CorrelationID        string           `json:"correlation_id"`
	ToolName             string           `json:"tool_name"`
	DescriptorSnapshotID string           `json:"descriptor_snapshot_id"`
	DescriptorHash       string           `json:"descriptor_hash"`
	Input                payload.Manifest `json:"input"`
	NormalizedInputHash  string           `json:"normalized_input_hash"`
	RequestHash          string           `json:"request_hash"`
	EffectClass          string           `json:"effect_class"`
	EffectKey            string           `json:"effect_key,omitempty"`
	EffectScope          string           `json:"effect_scope,omitempty"`
	ProviderID           string           `json:"provider_id,omitempty"`
	PermissionSnapshot   string           `json:"permission_snapshot"`
}

type ProposedExecution struct {
	Command eventpostgres.DeliveredCommand
	Payload CommandPayload
	Input   json.RawMessage
}

type PolicyDecision struct {
	Allowed        bool   `json:"allowed"`
	SnapshotID     string `json:"snapshot_id"`
	SnapshotHash   string `json:"snapshot_hash"`
	ReasonCode     string `json:"reason_code,omitempty"`
	OverlayVersion uint64 `json:"overlay_version"`
}

// PolicyEvaluator must load the current deny-only overlay for every delivery,
// including a reclaim. An error occurs before claim and is therefore safe to
// retry without granting an execution right.
type PolicyEvaluator interface {
	Evaluate(context.Context, ProposedExecution) (PolicyDecision, error)
}

type Execution struct {
	ProposedExecution
	Claim  executionpostgres.ToolClaim
	Policy PolicyDecision
}

type Executor interface {
	// Execute resolves the exact immutable descriptor hash, revalidates input
	// and output schemas, and performs the tool operation. Infrastructure
	// errors must be returned as errors; known external outcomes are returned
	// explicitly so the worker never guesses whether a write happened.
	Execute(context.Context, Execution) (Outcome, error)
}

type Outcome struct {
	State               statemachine.ToolCallState
	Result              json.RawMessage
	ExternalResourceRef string
	EffectDisposition   EffectDisposition
	ReconcileAfter      time.Duration
}

type EffectDisposition string

const (
	EffectConfirmed  EffectDisposition = "confirmed"
	EffectNotApplied EffectDisposition = "not_applied"
	EffectUnknown    EffectDisposition = "unknown"
)

type Schedule struct {
	QueueClass, ResourceClass string
	Priority, MaxAttempts     int
	CostUnits                 int64
}

type Handler struct {
	Payloads          payload.Store
	Tools             ToolStore
	Policy            PolicyEvaluator
	Executor          Executor
	ConsumerName      string
	WorkerID          string
	Actor             json.RawMessage
	IDKey             []byte
	HeartbeatInterval time.Duration
	MaximumCommand    int
	MaximumInput      int
	MaximumResult     int
	Resume            Schedule
	Reconcile         Schedule
	ReconcileDelay    time.Duration
	Now               func() time.Time
	Metrics           WorkerMetrics
}

type WorkerMetrics interface {
	AddLeaseHeartbeats(context.Context, int64, string)
	AddToolCall(context.Context, string, bool)
}

func (handler Handler) Handle(ctx context.Context, delivered eventpostgres.DeliveredCommand) error {
	if err := handler.validate(); err != nil {
		return err
	}
	command, input, err := handler.load(ctx, delivered)
	if err != nil {
		return errors.Join(eventpostgres.ErrDeliveryConflict, err)
	}
	proposed := ProposedExecution{Command: delivered, Payload: command, Input: input}
	decision, err := handler.Policy.Evaluate(ctx, proposed)
	if err != nil {
		return fmt.Errorf("evaluate tool execution overlay: %w", err)
	}
	if !validPolicyDecision(decision) {
		return errors.Join(eventpostgres.ErrDeliveryConflict, ErrPolicy)
	}
	pointers, err := handler.claimEvidence(ctx, proposed, decision)
	if err != nil {
		return err
	}
	binding := executionpostgres.ToolBinding{
		ToolName: command.ToolName, DescriptorSnapshotID: command.DescriptorSnapshotID,
		NormalizedInputRef: command.Input.Ref, RequestHash: command.RequestHash,
		EffectClass: command.EffectClass, EffectKey: command.EffectKey,
		EffectScope: command.EffectScope, ProviderID: command.ProviderID,
	}
	claim, err := handler.Tools.ClaimTool(ctx, executionpostgres.ClaimToolCommand{
		Command: delivered, ConsumerName: handler.ConsumerName, WorkerID: handler.WorkerID,
		ExpectedBinding: &binding, ExpectedPermissionSnapshot: command.PermissionSnapshot,
		Actor: handler.Actor, CorrelationID: command.CorrelationID,
		ToolStartedEvent: pointers.tool, AttemptStartedEvent: pointers.attempt,
		AttemptExpiredEvent: pointers.expired,
	})
	if err != nil {
		return mapClaimError(err)
	}
	if !decision.Allowed {
		result, marshalErr := json.Marshal(map[string]any{
			"schema_version": 1, "status": "denied", "reason_code": decision.ReasonCode,
			"policy_snapshot_id": decision.SnapshotID, "policy_snapshot_hash": decision.SnapshotHash,
			"overlay_version": decision.OverlayVersion,
		})
		if marshalErr != nil {
			return marshalErr
		}
		disposition := EffectDisposition("")
		if claim.EffectClass != "read_only" {
			disposition = EffectNotApplied
		}
		return handler.commit(ctx, command, decision, claim, Outcome{State: statemachine.ToolCallFailed, Result: result, EffectDisposition: disposition})
	}
	outcome, live, err := handler.executeWithHeartbeat(ctx, Execution{ProposedExecution: proposed, Claim: claim, Policy: decision})
	if err != nil {
		return err
	}
	return handler.commit(ctx, command, decision, live, outcome)
}

func (handler Handler) executeWithHeartbeat(ctx context.Context, execution Execution) (Outcome, executionpostgres.ToolClaim, error) {
	executionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		outcome Outcome
		err     error
	}
	finished := make(chan result, 1)
	go func() {
		outcome, err := handler.Executor.Execute(executionCtx, execution)
		finished <- result{outcome: outcome, err: err}
	}()
	ticker := time.NewTicker(handler.HeartbeatInterval)
	defer ticker.Stop()
	claim := execution.Claim
	for {
		select {
		case result := <-finished:
			return result.outcome, claim, result.err
		case <-ticker.C:
			live, err := handler.Tools.HeartbeatTool(ctx, claim)
			if err != nil {
				cancel()
				return Outcome{}, claim, errors.Join(ErrHeartbeatLost, err)
			}
			claim = live
			if handler.Metrics != nil {
				handler.Metrics.AddLeaseHeartbeats(ctx, 1, "tool")
			}
		case <-ctx.Done():
			return Outcome{}, claim, ctx.Err()
		}
	}
}

func (handler Handler) commit(ctx context.Context, command CommandPayload, decision PolicyDecision, claim executionpostgres.ToolClaim, outcome Outcome) error {
	if !validOutcome(claim.EffectClass, outcome, handler.maximumResult()) {
		return ErrOutcome
	}
	resultHash := plaintextHash(outcome.Result)
	resultManifest, err := handler.Payloads.Put(ctx, payload.Descriptor{TenantID: claim.TenantID, ObjectID: claim.ToolCallID, Class: "tool-result", ContentType: "application/json"}, outcome.Result)
	if err != nil {
		return err
	}
	base := map[string]any{
		"schema_version": 1, "tool_call_id": claim.ToolCallID, "run_id": claim.RunID,
		"attempt_id": claim.AttemptID, "fence": claim.Fence, "tool_name": claim.Binding.ToolName,
		"descriptor_snapshot_id": claim.Binding.DescriptorSnapshotID, "request_hash": claim.Binding.RequestHash,
		"effect_class": claim.EffectClass, "provider_request_id": claim.ProviderRequestID,
		"result_ref": resultManifest.Ref, "result_payload_hash": resultManifest.Hash, "result_hash": resultHash,
		"target_state": outcome.State, "external_resource_ref": outcome.ExternalResourceRef,
		"effect_disposition": outcome.EffectDisposition,
		"policy_snapshot_id": decision.SnapshotID, "policy_snapshot_hash": decision.SnapshotHash,
		"overlay_version": decision.OverlayVersion,
	}
	if outcome.State == statemachine.ToolCallOutcomeUnknown && !isAutomaticallyReconcilable(claim.EffectClass) {
		base["manual_review_required"] = true
		base["manual_review_reason"] = "effect_class_requires_human_resolution"
	}
	toolEvent, err := handler.putEvent(ctx, claim.TenantID, claim.AttemptID+":tool-completed", base, "tool_call_completed")
	if err != nil {
		return err
	}
	attemptEvent, err := handler.putEvent(ctx, claim.TenantID, claim.AttemptID+":attempt-completed", base, "job_attempt_completed")
	if err != nil {
		return err
	}
	groupEvent, err := handler.putEvent(ctx, claim.TenantID, claim.AttemptID+":group-joined", base, "parallel_group_joined")
	if err != nil {
		return err
	}
	resumeEvent, err := handler.putEvent(ctx, claim.TenantID, claim.AttemptID+":run-resume-queued", base, "run_resume_queued")
	if err != nil {
		return err
	}
	resumeCommandID, err := executionpostgres.ResumeAgentCommandID(handler.IDKey, claim.GroupID)
	if err != nil {
		return err
	}
	resumePayload, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		RunID         string `json:"run_id"`
		CorrelationID string `json:"correlation_id"`
	}{commandSchemaVersion, claim.RunID, command.CorrelationID})
	if err != nil {
		return err
	}
	resumeManifest, err := handler.Payloads.Put(ctx, payload.Descriptor{TenantID: claim.TenantID, ObjectID: resumeCommandID, Class: "agent-run-command", ContentType: "application/json"}, resumePayload)
	if err != nil {
		return err
	}
	complete := executionpostgres.CompleteToolCommand{
		Claim: claim, ExpectedToolVersion: claim.ToolCallVersion, TargetState: outcome.State,
		ResultHash: resultHash, Actor: handler.Actor, CorrelationID: command.CorrelationID,
		ToolCompletedEvent: toolEvent, AttemptCompletedEvent: attemptEvent,
		GroupJoinedEvent: groupEvent, RunResumeQueuedEvent: resumeEvent,
		ResumeCommand:    executionpostgres.PayloadPointer{Ref: resumeManifest.Ref, Hash: resumeManifest.Hash},
		ResumeQueueClass: handler.Resume.QueueClass, ResumeResourceClass: handler.Resume.ResourceClass,
		ResumePriority: handler.Resume.Priority, ResumeCostUnits: handler.Resume.CostUnits,
		ResumeMaxAttempts: handler.Resume.MaxAttempts,
	}
	if claim.EffectClass == "read_only" {
		_, err = handler.Tools.CompleteReadOnlyTool(ctx, complete)
		handler.observeToolCall(ctx, claim, outcome, err)
		return err
	}
	effect := executionpostgres.EffectCompletion{ExternalResourceRef: outcome.ExternalResourceRef}
	if outcome.State == statemachine.ToolCallOutcomeUnknown {
		reconcileAfter := outcome.ReconcileAfter
		if reconcileAfter <= 0 {
			reconcileAfter = handler.ReconcileDelay
		}
		unknownAt := handler.now().UTC()
		dueAt := unknownAt.Add(reconcileAfter).UTC()
		effect.ReconciliationDueAt = dueAt
		if !isAutomaticallyReconcilable(claim.EffectClass) {
			_, err = handler.Tools.CompleteEffectTool(ctx, complete, effect)
			handler.observeToolCall(ctx, claim, outcome, err)
			return err
		}
		terminalVersion := claim.ToolCallVersion + 1
		reconcileCommandID, idErr := executionpostgres.ReconcileToolEffectCommandID(handler.IDKey, claim.EffectID, terminalVersion)
		if idErr != nil {
			return idErr
		}
		reconcilePayload, marshalErr := json.Marshal(toolreconciler.CommandPayload{
			SchemaVersion: 1, TenantID: claim.TenantID, RunID: claim.RunID,
			ToolCallID: claim.ToolCallID, EffectID: claim.EffectID,
			ToolName: command.ToolName, DescriptorSnapshotID: command.DescriptorSnapshotID,
			DescriptorHash: command.DescriptorHash, EffectClass: claim.EffectClass,
			EffectKey: command.EffectKey, EffectScope: command.EffectScope, ProviderID: command.ProviderID,
			ProviderRequestID: claim.ProviderRequestID, RequestHash: command.RequestHash,
			TerminalToolVersion: terminalVersion, ReconciliationDueAt: dueAt, OutcomeUnknownAt: unknownAt,
			ReconciliationRound: 1, CorrelationID: command.CorrelationID,
		})
		if marshalErr != nil {
			return marshalErr
		}
		manifest, putErr := handler.Payloads.Put(ctx, payload.Descriptor{TenantID: claim.TenantID, ObjectID: reconcileCommandID, Class: "tool-reconciliation-command", ContentType: "application/json"}, reconcilePayload)
		if putErr != nil {
			return putErr
		}
		effect.ReconcileCommand = executionpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}
		effect.ReconcileQueueClass = handler.Reconcile.QueueClass
		effect.ReconcileResource = handler.Reconcile.ResourceClass
		effect.ReconcilePriority = handler.Reconcile.Priority
		effect.ReconcileCostUnits = handler.Reconcile.CostUnits
		effect.ReconcileAttempts = handler.Reconcile.MaxAttempts
	}
	_, err = handler.Tools.CompleteEffectTool(ctx, complete, effect)
	handler.observeToolCall(ctx, claim, outcome, err)
	return err
}

func (handler Handler) observeToolCall(ctx context.Context, claim executionpostgres.ToolClaim, outcome Outcome, commitErr error) {
	if handler.Metrics == nil || commitErr != nil {
		return
	}
	value := "failed"
	switch outcome.State {
	case statemachine.ToolCallSucceeded:
		value = "succeeded"
	case statemachine.ToolCallOutcomeUnknown:
		value = "unknown"
	}
	handler.Metrics.AddToolCall(ctx, value, claim.EffectClass != "read_only")
}

func isAutomaticallyReconcilable(effectClass string) bool {
	return effectClass == "reconcilable_write"
}

type claimPointers struct {
	tool, attempt, expired executionpostgres.PayloadPointer
}

func (handler Handler) claimEvidence(ctx context.Context, proposed ProposedExecution, decision PolicyDecision) (claimPointers, error) {
	base := map[string]any{
		"schema_version": 1, "tool_call_id": proposed.Command.AggregateID,
		"command_id": proposed.Command.CommandID, "run_id": proposed.Payload.RunID,
		"tool_name": proposed.Payload.ToolName, "descriptor_snapshot_id": proposed.Payload.DescriptorSnapshotID,
		"descriptor_hash": proposed.Payload.DescriptorHash, "request_hash": proposed.Payload.RequestHash,
		"effect_class": proposed.Payload.EffectClass, "correlation_id": proposed.Payload.CorrelationID,
		"worker_id": handler.WorkerID, "queue_generation": proposed.Command.QueueGeneration,
		"dispatch_version": proposed.Command.DispatchVersion, "policy": decision,
	}
	tool, err := handler.putEvent(ctx, proposed.Command.TenantID, proposed.Command.CommandID+":tool-started", base, "tool_call_started")
	if err != nil {
		return claimPointers{}, err
	}
	attempt, err := handler.putEvent(ctx, proposed.Command.TenantID, proposed.Command.CommandID+":attempt-started", base, "job_attempt_started")
	if err != nil {
		return claimPointers{}, err
	}
	expired, err := handler.putEvent(ctx, proposed.Command.TenantID, proposed.Command.CommandID+":attempt-expired", base, "job_attempt_expired")
	return claimPointers{tool: tool, attempt: attempt, expired: expired}, err
}

func (handler Handler) load(ctx context.Context, delivered eventpostgres.DeliveredCommand) (CommandPayload, json.RawMessage, error) {
	if delivered.CommandType != "ExecuteToolCall" || delivered.AggregateKind != "tool_call" || delivered.AggregateID == "" || delivered.CommandID == "" || delivered.TenantID == "" || delivered.QueueGeneration < 1 || delivered.DispatchVersion < 1 {
		return CommandPayload{}, nil, ErrCommand
	}
	descriptor := payload.Descriptor{TenantID: delivered.TenantID, ObjectID: delivered.CommandID, Class: "tool-execute-command", ContentType: "application/json"}
	encoded, err := handler.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: delivered.PayloadRef, Hash: delivered.PayloadHash})
	if err != nil || len(encoded) == 0 || len(encoded) > handler.maximumCommand() {
		return CommandPayload{}, nil, ErrCommand
	}
	var command CommandPayload
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&command); err != nil {
		return CommandPayload{}, nil, ErrCommand
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || !validCommand(command, delivered) {
		return CommandPayload{}, nil, ErrCommand
	}
	inputDescriptor := payload.Descriptor{TenantID: delivered.TenantID, ObjectID: delivered.AggregateID, Class: "tool-normalized-input", ContentType: "application/json"}
	input, err := handler.Payloads.Get(ctx, inputDescriptor, command.Input)
	if err != nil || len(input) == 0 || len(input) > handler.maximumInput() || !json.Valid(input) || plaintextHash(input) != command.NormalizedInputHash {
		return CommandPayload{}, nil, ErrCommand
	}
	return command, append(json.RawMessage(nil), input...), nil
}

func validCommand(command CommandPayload, delivered eventpostgres.DeliveredCommand) bool {
	binding := executionpostgres.ToolBinding{
		ToolName: command.ToolName, DescriptorSnapshotID: command.DescriptorSnapshotID,
		NormalizedInputRef: command.Input.Ref, RequestHash: command.RequestHash,
		EffectClass: command.EffectClass, EffectKey: command.EffectKey,
		EffectScope: command.EffectScope, ProviderID: command.ProviderID,
	}
	return command.SchemaVersion == commandSchemaVersion && command.ToolCallID == delivered.AggregateID && command.RunID != "" && command.UserID != "" && command.PermissionSnapshot != "" && command.CorrelationID != "" && command.DescriptorHash != "" && command.Input.Ref != "" && command.Input.Hash != "" && command.NormalizedInputHash != "" && validBinding(binding)
}

func validBinding(binding executionpostgres.ToolBinding) bool {
	base := binding.ToolName != "" && binding.DescriptorSnapshotID != "" && binding.NormalizedInputRef != "" && binding.RequestHash != ""
	read := binding.EffectClass == "read_only" && binding.EffectKey == "" && binding.EffectScope == "" && binding.ProviderID == ""
	write := (binding.EffectClass == "idempotent_write" || binding.EffectClass == "reconcilable_write" || binding.EffectClass == "compensatable_write" || binding.EffectClass == "irreversible_write") && binding.EffectKey != "" && binding.EffectScope != "" && binding.ProviderID != ""
	return base && (read || write)
}

func validPolicyDecision(decision PolicyDecision) bool {
	return decision.SnapshotID != "" && decision.SnapshotHash != "" && decision.OverlayVersion > 0 && (decision.Allowed || decision.ReasonCode != "")
}

func validOutcome(effectClass string, outcome Outcome, maximum int) bool {
	if len(outcome.Result) == 0 || len(outcome.Result) > maximum || !json.Valid(outcome.Result) || outcome.ReconcileAfter < 0 || outcome.ReconcileAfter > 24*time.Hour {
		return false
	}
	if effectClass == "read_only" {
		return outcome.EffectDisposition == "" && outcome.ExternalResourceRef == "" && (outcome.State == statemachine.ToolCallSucceeded || outcome.State == statemachine.ToolCallFailed)
	}
	switch outcome.State {
	case statemachine.ToolCallSucceeded:
		return outcome.EffectDisposition == EffectConfirmed
	case statemachine.ToolCallFailed:
		return outcome.EffectDisposition == EffectNotApplied && outcome.ExternalResourceRef == ""
	case statemachine.ToolCallOutcomeUnknown:
		return outcome.EffectDisposition == EffectUnknown
	default:
		return false
	}
}

func (handler Handler) putEvent(ctx context.Context, tenantID, objectID string, value any, eventType string) (executionpostgres.PayloadPointer, error) {
	encoded, err := json.Marshal(struct {
		EventType string `json:"event_type"`
		Data      any    `json:"data"`
	}{eventType, value})
	if err != nil {
		return executionpostgres.PayloadPointer{}, err
	}
	manifest, err := handler.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: "event-payload", ContentType: "application/json"}, encoded)
	return executionpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, err
}

func (handler Handler) validate() error {
	var actor map[string]any
	if handler.Payloads == nil || handler.Tools == nil || handler.Policy == nil || handler.Executor == nil || handler.ConsumerName == "" || handler.WorkerID == "" || handler.HeartbeatInterval <= 0 || len(handler.IDKey) < 32 || json.Unmarshal(handler.Actor, &actor) != nil || actor == nil || handler.maximumCommand() < 1 || handler.maximumInput() < 1 || handler.maximumResult() < 1 || !validSchedule(handler.Resume) || !validSchedule(handler.Reconcile) || handler.ReconcileDelay <= 0 || handler.ReconcileDelay > 24*time.Hour {
		return ErrConfiguration
	}
	return nil
}

func validSchedule(schedule Schedule) bool {
	return (schedule.QueueClass == "interactive" || schedule.QueueClass == "background") && schedule.ResourceClass != "" && schedule.Priority >= 0 && schedule.Priority <= 1000 && schedule.CostUnits > 0 && schedule.CostUnits <= 1_000_000_000_000 && schedule.MaxAttempts > 0 && schedule.MaxAttempts <= 100
}

func (handler Handler) maximumCommand() int {
	if handler.MaximumCommand == 0 {
		return 1 << 20
	}
	return handler.MaximumCommand
}

func (handler Handler) maximumInput() int {
	if handler.MaximumInput == 0 {
		return 1 << 20
	}
	return handler.MaximumInput
}

func (handler Handler) maximumResult() int {
	if handler.MaximumResult == 0 {
		return 4 << 20
	}
	return handler.MaximumResult
}

func (handler Handler) now() time.Time {
	if handler.Now != nil {
		return handler.Now().UTC()
	}
	return time.Now().UTC()
}

func plaintextHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func mapClaimError(err error) error {
	switch {
	case errors.Is(err, executionpostgres.ErrClaimCompleted):
		return nil
	case errors.Is(err, executionpostgres.ErrClaimBusy):
		return errors.Join(eventpostgres.ErrDeliveryBusy, err)
	case errors.Is(err, executionpostgres.ErrStaleEpoch):
		return errors.Join(eventpostgres.ErrStaleStoreEpoch, err)
	case errors.Is(err, executionpostgres.ErrClaimConflict), errors.Is(err, executionpostgres.ErrToolNotClaimable), errors.Is(err, executionpostgres.ErrToolEffectNeedsReconciliation):
		return errors.Join(eventpostgres.ErrDeliveryConflict, err)
	default:
		return fmt.Errorf("claim tool call: %w", err)
	}
}
