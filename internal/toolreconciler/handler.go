package toolreconciler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/payload"
)

var (
	ErrHandlerConfiguration = errors.New("tool reconciliation handler configuration is invalid")
	ErrCommand              = errors.New("tool reconciliation command is invalid")
	ErrLookupResult         = errors.New("tool reconciliation lookup result is invalid")
	ErrHeartbeatLost        = errors.New("tool reconciliation lease was lost")
)

type ReconciliationStore interface {
	ClaimReconciliation(context.Context, executionpostgres.ClaimReconciliationCommand) (executionpostgres.ReconciliationClaim, error)
	HeartbeatReconciliation(context.Context, executionpostgres.ReconciliationClaim) (executionpostgres.ReconciliationClaim, error)
	CompleteReconciliation(context.Context, executionpostgres.CompleteReconciliationCommand) (executionpostgres.CompletedTool, error)
	DeferReconciliation(context.Context, executionpostgres.DeferReconciliationCommand) (executionpostgres.DeferredReconciliation, error)
	EscalateReconciliation(context.Context, executionpostgres.EscalateReconciliationCommand) (executionpostgres.EscalatedReconciliation, error)
}

type Handler struct {
	Payloads          payload.Store
	Store             ReconciliationStore
	Lookup            LookupExecutor
	ConsumerName      string
	WorkerID          string
	Actor             json.RawMessage
	IDKey             []byte
	HeartbeatInterval time.Duration
	MaximumCommand    int
	MaximumEvidence   int
	MaximumRounds     int
	RetryDelay        time.Duration
	MaximumRetryDelay time.Duration
	Resume            Schedule
	Retry             Schedule
	Now               func() time.Time
	Metrics           ReconciliationMetrics
}

type ReconciliationMetrics interface {
	AddLeaseHeartbeats(context.Context, int64, string)
	AddToolCall(context.Context, string, bool)
	AddToolUnknown(context.Context, bool)
}

func (handler Handler) Handle(ctx context.Context, delivered eventpostgres.DeliveredCommand) error {
	if !handler.valid() {
		return ErrHandlerConfiguration
	}
	command, err := handler.load(ctx, delivered)
	if err != nil {
		return errors.Join(eventpostgres.ErrDeliveryConflict, err)
	}
	pointers, err := handler.claimEvidence(ctx, delivered, command)
	if err != nil {
		return err
	}
	claim, err := handler.Store.ClaimReconciliation(ctx, executionpostgres.ClaimReconciliationCommand{Command: delivered, ConsumerName: handler.ConsumerName, WorkerID: handler.WorkerID, Actor: handler.Actor, CorrelationID: command.CorrelationID, AttemptStartedEvent: pointers.started, AttemptExpiredEvent: pointers.expired})
	if err != nil {
		return mapClaimError(err)
	}
	if !matchesClaim(command, claim) {
		return errors.Join(eventpostgres.ErrDeliveryConflict, ErrCommand)
	}
	result, live, lookupErr := handler.lookupWithHeartbeat(ctx, LookupRequest{Command: command, Claim: claim})
	if lookupErr != nil {
		if errors.Is(lookupErr, ErrHeartbeatLost) || ctx.Err() != nil {
			return lookupErr
		}
		result = LookupResult{Disposition: LookupInconclusive, Evidence: []byte(`{"schema_version":1,"status":"inconclusive","reason_code":"provider_lookup_unavailable"}`)}
	}
	if !handler.validResult(result) {
		return ErrLookupResult
	}
	if result.Disposition == LookupInconclusive {
		if command.ReconciliationRound >= handler.MaximumRounds {
			return handler.escalate(ctx, command, live, result)
		}
		return handler.deferLookup(ctx, command, live, result)
	}
	return handler.complete(ctx, command, live, result)
}

type claimEvidencePointers struct {
	started, expired executionpostgres.PayloadPointer
}

func (handler Handler) claimEvidence(ctx context.Context, delivered eventpostgres.DeliveredCommand, command CommandPayload) (claimEvidencePointers, error) {
	base := map[string]any{"schema_version": 1, "tool_call_id": command.ToolCallID, "effect_id": command.EffectID, "command_id": delivered.CommandID, "tool_name": command.ToolName, "descriptor_snapshot_id": command.DescriptorSnapshotID, "descriptor_hash": command.DescriptorHash, "effect_class": command.EffectClass, "provider_request_id": command.ProviderRequestID, "reconciliation_round": command.ReconciliationRound, "worker_id": handler.WorkerID}
	started, err := handler.putEvent(ctx, delivered.TenantID, delivered.CommandID+":attempt-started", "job_attempt_started", base)
	if err != nil {
		return claimEvidencePointers{}, err
	}
	expired, err := handler.putEvent(ctx, delivered.TenantID, delivered.CommandID+":attempt-expired", "job_attempt_expired", base)
	return claimEvidencePointers{started: started, expired: expired}, err
}

func (handler Handler) lookupWithHeartbeat(ctx context.Context, request LookupRequest) (LookupResult, executionpostgres.ReconciliationClaim, error) {
	lookupCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type completed struct {
		result LookupResult
		err    error
	}
	finished := make(chan completed, 1)
	go func() {
		result, err := handler.Lookup.Execute(lookupCtx, request)
		finished <- completed{result: result, err: err}
	}()
	ticker := time.NewTicker(handler.HeartbeatInterval)
	defer ticker.Stop()
	claim := request.Claim
	for {
		select {
		case value := <-finished:
			return value.result, claim, value.err
		case <-ticker.C:
			live, err := handler.Store.HeartbeatReconciliation(ctx, claim)
			if err != nil {
				cancel()
				return LookupResult{}, claim, errors.Join(ErrHeartbeatLost, err)
			}
			claim = live
			if handler.Metrics != nil {
				handler.Metrics.AddLeaseHeartbeats(ctx, 1, "reconciliation")
			}
		case <-ctx.Done():
			return LookupResult{}, claim, ctx.Err()
		}
	}
}

func (handler Handler) complete(ctx context.Context, command CommandPayload, claim executionpostgres.ReconciliationClaim, result LookupResult) error {
	target := statemachine.ToolCallSucceeded
	if result.Disposition == LookupNotApplied {
		target = statemachine.ToolCallFailed
	}
	base := handler.resultEvidence(command, claim, result, string(target))
	toolEvent, err := handler.putEvent(ctx, claim.TenantID, claim.AttemptID+":tool-reconciled", "tool_effect_reconciled", base)
	if err != nil {
		return err
	}
	attemptEvent, err := handler.putEvent(ctx, claim.TenantID, claim.AttemptID+":attempt-completed", "job_attempt_completed", base)
	if err != nil {
		return err
	}
	groupEvent, err := handler.putEvent(ctx, claim.TenantID, claim.AttemptID+":group-joined", "parallel_group_joined", base)
	if err != nil {
		return err
	}
	resumeEvent, err := handler.putEvent(ctx, claim.TenantID, claim.AttemptID+":run-resume-queued", "run_resume_queued", base)
	if err != nil {
		return err
	}
	resume, err := handler.resumeCommand(ctx, command, claim)
	if err != nil {
		return err
	}
	_, err = handler.Store.CompleteReconciliation(ctx, executionpostgres.CompleteReconciliationCommand{Claim: claim, ExpectedToolVersion: claim.ToolCallVersion, ExpectedEffectVersion: claim.EffectVersion, TargetState: target, ResultHash: bytesHash(result.Evidence), ExternalResourceRef: result.ExternalResourceRef, Actor: handler.Actor, CorrelationID: command.CorrelationID, ToolCompletedEvent: toolEvent, AttemptCompletedEvent: attemptEvent, GroupJoinedEvent: groupEvent, RunResumeQueuedEvent: resumeEvent, ResumeCommand: resume, ResumeQueueClass: handler.Resume.QueueClass, ResumeResourceClass: handler.Resume.ResourceClass, ResumePriority: handler.Resume.Priority, ResumeCostUnits: handler.Resume.CostUnits, ResumeMaxAttempts: handler.Resume.MaxAttempts})
	if err == nil && handler.Metrics != nil {
		handler.Metrics.AddToolUnknown(ctx, !handler.now().After(command.OutcomeUnknownAt.Add(15*time.Minute)))
	}
	return err
}

func (handler Handler) deferLookup(ctx context.Context, command CommandPayload, claim executionpostgres.ReconciliationClaim, result LookupResult) error {
	nextDueAt := handler.now().Add(handler.retryDelay(command.ReconciliationRound)).UTC().Truncate(time.Microsecond)
	nextCommandID, err := executionpostgres.ReconcileToolEffectRetryCommandID(handler.IDKey, claim.EffectID, claim.EffectVersion+1)
	if err != nil {
		return err
	}
	next := command
	next.ReconciliationDueAt = nextDueAt
	next.ReconciliationRound++
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	manifest, err := handler.Payloads.Put(ctx, payload.Descriptor{TenantID: claim.TenantID, ObjectID: nextCommandID, Class: "tool-reconciliation-command", ContentType: "application/json"}, encoded)
	if err != nil || manifest.Ref == "" || manifest.Hash == "" {
		return errors.Join(err, ErrHandlerConfiguration)
	}
	base := handler.resultEvidence(command, claim, result, "outcome_unknown")
	attemptEvent, err := handler.putEvent(ctx, claim.TenantID, claim.AttemptID+":attempt-inconclusive", "job_attempt_completed", base)
	if err != nil {
		return err
	}
	_, err = handler.Store.DeferReconciliation(ctx, executionpostgres.DeferReconciliationCommand{Claim: claim, ExpectedEffectVersion: claim.EffectVersion, ResultHash: bytesHash(result.Evidence), NextDueAt: nextDueAt, Actor: handler.Actor, CorrelationID: command.CorrelationID, AttemptCompletedEvent: attemptEvent, NextReconcileCommand: executionpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, QueueClass: handler.Retry.QueueClass, ResourceClass: handler.Retry.ResourceClass, Priority: handler.Retry.Priority, CostUnits: handler.Retry.CostUnits, MaxAttempts: handler.Retry.MaxAttempts})
	return err
}

func (handler Handler) escalate(ctx context.Context, command CommandPayload, claim executionpostgres.ReconciliationClaim, result LookupResult) error {
	base := handler.resultEvidence(command, claim, result, "manual_review_required")
	base["automatic_reconciliation_exhausted"] = true
	attemptEvent, err := handler.putEvent(ctx, claim.TenantID, claim.AttemptID+":manual-review-required", "job_attempt_completed", base)
	if err != nil {
		return err
	}
	_, err = handler.Store.EscalateReconciliation(ctx, executionpostgres.EscalateReconciliationCommand{Claim: claim, ExpectedEffectVersion: claim.EffectVersion, ResultHash: bytesHash(result.Evidence), Actor: handler.Actor, CorrelationID: command.CorrelationID, AttemptCompletedEvent: attemptEvent})
	if err == nil && handler.Metrics != nil {
		handler.Metrics.AddToolUnknown(ctx, false)
	}
	return err
}

func (handler Handler) resultEvidence(command CommandPayload, claim executionpostgres.ReconciliationClaim, result LookupResult, target string) map[string]any {
	return map[string]any{"schema_version": 1, "tool_call_id": claim.ToolCallID, "run_id": claim.RunID, "effect_id": claim.EffectID, "attempt_id": claim.AttemptID, "fence": claim.Fence, "tool_name": command.ToolName, "descriptor_snapshot_id": command.DescriptorSnapshotID, "descriptor_hash": command.DescriptorHash, "effect_class": claim.EffectClass, "effect_key": claim.EffectKey, "effect_scope": claim.EffectScope, "provider_id": claim.ProviderID, "provider_request_id": claim.ProviderRequestID, "reconciliation_round": command.ReconciliationRound, "disposition": result.Disposition, "external_resource_ref": result.ExternalResourceRef, "target_state": target, "provider_evidence": json.RawMessage(result.Evidence)}
}

func (handler Handler) resumeCommand(ctx context.Context, command CommandPayload, claim executionpostgres.ReconciliationClaim) (executionpostgres.PayloadPointer, error) {
	commandID, err := executionpostgres.ResumeAgentCommandID(handler.IDKey, claim.GroupID)
	if err != nil {
		return executionpostgres.PayloadPointer{}, err
	}
	encoded, err := json.Marshal(map[string]any{"schema_version": 1, "run_id": claim.RunID, "correlation_id": command.CorrelationID})
	if err != nil {
		return executionpostgres.PayloadPointer{}, err
	}
	manifest, err := handler.Payloads.Put(ctx, payload.Descriptor{TenantID: claim.TenantID, ObjectID: commandID, Class: "agent-run-command", ContentType: "application/json"}, encoded)
	if err != nil || manifest.Ref == "" || manifest.Hash == "" {
		return executionpostgres.PayloadPointer{}, errors.Join(err, ErrHandlerConfiguration)
	}
	return executionpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, nil
}

func (handler Handler) putEvent(ctx context.Context, tenantID, objectID, eventType string, data any) (executionpostgres.PayloadPointer, error) {
	encoded, err := json.Marshal(map[string]any{"event_type": eventType, "data": data})
	if err != nil {
		return executionpostgres.PayloadPointer{}, err
	}
	manifest, err := handler.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: "event-payload", ContentType: "application/json"}, encoded)
	if err != nil || manifest.Ref == "" || manifest.Hash == "" {
		return executionpostgres.PayloadPointer{}, errors.Join(err, ErrHandlerConfiguration)
	}
	return executionpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, nil
}

func (handler Handler) load(ctx context.Context, delivered eventpostgres.DeliveredCommand) (CommandPayload, error) {
	if delivered.CommandType != "ReconcileToolEffect" || delivered.AggregateKind != "tool_call" || delivered.AggregateID == "" || delivered.CommandID == "" || delivered.TenantID == "" || delivered.QueueGeneration < 1 || delivered.DispatchVersion < 1 {
		return CommandPayload{}, ErrCommand
	}
	encoded, err := handler.Payloads.Get(ctx, payload.Descriptor{TenantID: delivered.TenantID, ObjectID: delivered.CommandID, Class: "tool-reconciliation-command", ContentType: "application/json"}, payload.Manifest{Ref: delivered.PayloadRef, Hash: delivered.PayloadHash})
	if err != nil || len(encoded) == 0 || len(encoded) > handler.MaximumCommand {
		return CommandPayload{}, ErrCommand
	}
	var command CommandPayload
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&command) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validCommand(command, delivered, handler.MaximumRounds) {
		return CommandPayload{}, ErrCommand
	}
	return command, nil
}

func validCommand(command CommandPayload, delivered eventpostgres.DeliveredCommand, maximumRounds int) bool {
	return command.SchemaVersion == 1 && command.TenantID == delivered.TenantID && command.ToolCallID == delivered.AggregateID && command.RunID != "" && command.EffectID != "" && command.ToolName != "" && command.DescriptorSnapshotID != "" && sha256Pattern.MatchString(command.DescriptorHash) && command.EffectClass == automaticallyReconcilableEffectClass && command.EffectKey != "" && command.EffectScope != "" && command.ProviderID != "" && command.ProviderRequestID != "" && command.RequestHash != "" && command.TerminalToolVersion > 0 && !command.OutcomeUnknownAt.IsZero() && !command.ReconciliationDueAt.Before(command.OutcomeUnknownAt) && command.ReconciliationRound >= 1 && command.ReconciliationRound <= maximumRounds && command.CorrelationID != ""
}

func matchesClaim(command CommandPayload, claim executionpostgres.ReconciliationClaim) bool {
	return claim.TenantID == command.TenantID && claim.ToolCallID == command.ToolCallID && claim.RunID == command.RunID && claim.EffectID == command.EffectID && claim.ToolCallVersion == command.TerminalToolVersion && claim.EffectClass == command.EffectClass && claim.EffectKey == command.EffectKey && claim.EffectScope == command.EffectScope && claim.ProviderID == command.ProviderID && claim.ProviderRequestID == command.ProviderRequestID && claim.ReconciliationDueAt.Equal(command.ReconciliationDueAt)
}

func (handler Handler) validResult(result LookupResult) bool {
	if len(result.Evidence) == 0 || len(result.Evidence) > handler.MaximumEvidence || !json.Valid(result.Evidence) || strings.ContainsAny(result.ExternalResourceRef, "\x00\r\n") || len(result.ExternalResourceRef) > 4096 {
		return false
	}
	switch result.Disposition {
	case LookupConfirmed:
		return result.ExternalResourceRef != ""
	case LookupNotApplied, LookupInconclusive:
		return result.ExternalResourceRef == ""
	default:
		return false
	}
}

func (handler Handler) valid() bool {
	var actor map[string]any
	return handler.Payloads != nil && handler.Store != nil && !nilLookup(handler.Lookup) && handler.ConsumerName != "" && handler.WorkerID != "" && len(handler.IDKey) >= 32 && json.Unmarshal(handler.Actor, &actor) == nil && actor != nil && handler.HeartbeatInterval > 0 && handler.MaximumCommand >= 1 && handler.MaximumCommand <= 16<<20 && handler.MaximumEvidence >= 1 && handler.MaximumEvidence <= 16<<20 && handler.MaximumRounds >= 1 && handler.MaximumRounds <= 100 && handler.RetryDelay >= time.Second && handler.RetryDelay <= 24*time.Hour && handler.MaximumRetryDelay >= handler.RetryDelay && handler.MaximumRetryDelay <= 7*24*time.Hour && validSchedule(handler.Resume) && validSchedule(handler.Retry)
}

func (handler Handler) retryDelay(round int) time.Duration {
	delay := handler.RetryDelay
	for step := 1; step < round && delay < handler.MaximumRetryDelay; step++ {
		if delay > handler.MaximumRetryDelay/2 {
			return handler.MaximumRetryDelay
		}
		delay *= 2
	}
	if delay > handler.MaximumRetryDelay {
		return handler.MaximumRetryDelay
	}
	return delay
}

func (handler Handler) now() time.Time {
	if handler.Now != nil {
		return handler.Now().UTC()
	}
	return time.Now().UTC()
}

func bytesHash(value []byte) string {
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
	case errors.Is(err, executionpostgres.ErrClaimConflict), errors.Is(err, executionpostgres.ErrReconciliationNotClaimable):
		return errors.Join(eventpostgres.ErrDeliveryConflict, err)
	default:
		return fmt.Errorf("claim tool reconciliation: %w", err)
	}
}
