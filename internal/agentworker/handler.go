// Package agentworker owns the durable Agent Worker command lifecycle. It
// binds scheduler-admitted commands to encrypted payloads, installs the Run
// execution fence, keeps that fence alive, and commits one terminal outcome.
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

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/payload"
)

const commandSchemaVersion = 1

var (
	ErrConfiguration = errors.New("agent worker configuration is invalid")
	ErrCommand       = errors.New("agent worker command is invalid")
	ErrOutcome       = errors.New("agent worker outcome is invalid")
	ErrHeartbeatLost = errors.New("agent worker execution lease was lost")
)

type RunStore interface {
	ClaimStart(context.Context, executionpostgres.ClaimRunCommand) (executionpostgres.RunClaim, error)
	HeartbeatRun(context.Context, executionpostgres.RunClaim) (executionpostgres.RunClaim, error)
	CompleteRunTerminal(context.Context, executionpostgres.CompleteRunCommand) (executionpostgres.CompletedRun, error)
	CompleteRunWithMessage(context.Context, executionpostgres.CompleteRunMessageCommand) (executionpostgres.CompletedRunMessage, error)
	RequestTools(context.Context, executionpostgres.RequestToolsCommand) (executionpostgres.ToolsRequested, error)
	ProposeDirectTools(context.Context, executionpostgres.ProposeDirectToolsCommand) (executionpostgres.DirectToolsProposed, error)
	SpawnChildRuns(context.Context, executionpostgres.SpawnChildRunsCommand) (executionpostgres.ChildRunsSpawned, error)
}

// CommandPayload is deliberately small. Mutable context never rides through
// JetStream; Runner builds a manifest from durable snapshot bindings after the
// Run fence has been installed.
type CommandPayload struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	CorrelationID string `json:"correlation_id"`
}

type Execution struct {
	Command eventpostgres.DeliveredCommand
	Payload CommandPayload
	Claim   executionpostgres.RunClaim
	// TurnIndex is scoped to one Run execution claim. The first model turn is
	// one; validation or repair turns increment it without reusing an attempt.
	TurnIndex uint64
}

type Runner interface {
	Execute(context.Context, Execution) (Outcome, error)
}

type Outcome struct {
	State        statemachine.RunState
	ResultHash   string
	RunEvent     any
	AttemptEvent any
	Child        *ChildOutcome
	MessageID    string
	Message      *MessageDocument
	Tools        *executionpostgres.RequestToolsCommand
	Approvals    *executionpostgres.ProposeDirectToolsCommand
	Children     *executionpostgres.SpawnChildRunsCommand
}

type ChildOutcome struct {
	ResultSummary, CompletedEvent, GroupJoinedEvent any
	RunResumeQueuedEvent, ResumeCommand             any
	CancelRemainingCommand                          any
	ResumeQueueClass, ResumeResourceClass           string
	ResumePriority, ResumeMaxAttempts               int
	ResumeCostUnits                                 int64
}

type Handler struct {
	Payloads          payload.Store
	Runs              RunStore
	Runner            Runner
	ConsumerName      string
	WorkerID          string
	Actor             json.RawMessage
	HeartbeatInterval time.Duration
	MaximumCommand    int
}

func (handler Handler) Handle(ctx context.Context, delivered eventpostgres.DeliveredCommand) error {
	if err := handler.validate(); err != nil {
		return err
	}
	command, err := handler.loadCommand(ctx, delivered)
	if err != nil {
		return errors.Join(eventpostgres.ErrDeliveryConflict, err)
	}
	claimPointers, err := handler.claimEvidence(ctx, delivered, command)
	if err != nil {
		return err
	}
	claim, err := handler.Runs.ClaimStart(ctx, executionpostgres.ClaimRunCommand{
		Command: delivered, ConsumerName: handler.ConsumerName, WorkerID: handler.WorkerID,
		Actor: handler.Actor, CorrelationID: command.CorrelationID,
		RunEvent: claimPointers.run, AttemptStartedEvent: claimPointers.attempt,
		AttemptExpiredEvent: claimPointers.expired,
	})
	if err != nil {
		return mapClaimError(err)
	}
	outcome, liveClaim, err := handler.executeWithHeartbeat(ctx, Execution{Command: delivered, Payload: command, Claim: claim, TurnIndex: 1})
	if err != nil {
		return err
	}
	return handler.commit(ctx, liveClaim, command, outcome)
}

func (handler Handler) commit(ctx context.Context, claim executionpostgres.RunClaim, command CommandPayload, outcome Outcome) error {
	actions := 0
	if statemachine.Runs.IsTerminal(outcome.State) {
		actions++
	}
	if outcome.Tools != nil {
		actions++
	}
	if outcome.Approvals != nil {
		actions++
	}
	if outcome.Children != nil {
		actions++
	}
	if actions != 1 {
		return ErrOutcome
	}
	if outcome.Tools != nil {
		if outcome.State != statemachine.RunWaitingTool || outcome.ResultHash != "" || outcome.RunEvent != nil || outcome.AttemptEvent != nil || outcome.Child != nil || outcome.Message == nil || outcome.MessageID == "" {
			return ErrOutcome
		}
		message, err := handler.runMessageInput(ctx, claim, outcome.MessageID, outcome.Message)
		if err != nil {
			return err
		}
		planned := *outcome.Tools
		planned.Claim, planned.ExpectedRunVersion = claim, claim.RunVersion
		planned.Actor, planned.CorrelationID = handler.Actor, command.CorrelationID
		planned.AssistantMessage = &message
		_, err = handler.Runs.RequestTools(ctx, planned)
		return err
	}
	if outcome.Approvals != nil {
		if outcome.State != statemachine.RunWaitingApproval || outcome.ResultHash != "" || outcome.RunEvent != nil || outcome.AttemptEvent != nil || outcome.Child != nil || outcome.Message == nil || outcome.MessageID == "" {
			return ErrOutcome
		}
		message, err := handler.runMessageInput(ctx, claim, outcome.MessageID, outcome.Message)
		if err != nil {
			return err
		}
		planned := *outcome.Approvals
		planned.Claim, planned.ExpectedRunVersion = claim, claim.RunVersion
		planned.Actor, planned.CorrelationID = handler.Actor, command.CorrelationID
		planned.AssistantMessage = &message
		_, err = handler.Runs.ProposeDirectTools(ctx, planned)
		return err
	}
	if outcome.Children != nil {
		if outcome.State != statemachine.RunWaitingChild || outcome.ResultHash != "" || outcome.RunEvent != nil || outcome.AttemptEvent != nil || outcome.Child != nil || outcome.Message == nil || outcome.MessageID == "" {
			return ErrOutcome
		}
		message, err := handler.runMessageInput(ctx, claim, outcome.MessageID, outcome.Message)
		if err != nil {
			return err
		}
		planned := *outcome.Children
		planned.Claim, planned.ExpectedRunVersion = claim, claim.RunVersion
		planned.Actor, planned.CorrelationID = handler.Actor, command.CorrelationID
		planned.AssistantMessage = &message
		_, err = handler.Runs.SpawnChildRuns(ctx, planned)
		return err
	}
	completion, err := handler.completion(ctx, claim, command, outcome)
	if err != nil {
		return err
	}
	if outcome.State != statemachine.RunSucceeded {
		if outcome.Message != nil || outcome.MessageID != "" {
			return ErrOutcome
		}
		_, err = handler.Runs.CompleteRunTerminal(ctx, completion)
		return err
	}
	message, err := handler.runMessageInput(ctx, claim, outcome.MessageID, outcome.Message)
	if err != nil {
		return err
	}
	_, err = handler.Runs.CompleteRunWithMessage(ctx, executionpostgres.CompleteRunMessageCommand{
		Completion: completion, MessageID: message.MessageID, Message: message.Message,
		ContentHash: message.ContentHash, FinalizedEvent: message.FinalizedEvent,
	})
	return err
}

func (handler Handler) runMessageInput(ctx context.Context, claim executionpostgres.RunClaim, messageID string, document *MessageDocument) (executionpostgres.RunMessageInput, error) {
	message, contentHash, err := handler.putMessage(ctx, claim, messageID, document)
	if err != nil {
		return executionpostgres.RunMessageInput{}, err
	}
	finalized, err := handler.putEvent(ctx, claim.TenantID, messageID+":finalized", map[string]any{
		"message_id": messageID, "run_id": claim.RunID, "content_hash": contentHash,
	}, "run_message_finalized")
	if err != nil {
		return executionpostgres.RunMessageInput{}, err
	}
	return executionpostgres.RunMessageInput{MessageID: messageID, Message: message, ContentHash: contentHash, FinalizedEvent: finalized}, nil
}

func (handler Handler) executeWithHeartbeat(ctx context.Context, execution Execution) (Outcome, executionpostgres.RunClaim, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		outcome Outcome
		err     error
	}
	finished := make(chan result, 1)
	go func() {
		outcome, err := handler.Runner.Execute(runCtx, execution)
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
			live, err := handler.Runs.HeartbeatRun(ctx, claim)
			if err != nil {
				cancel()
				return Outcome{}, claim, errors.Join(ErrHeartbeatLost, err)
			}
			claim = live
		case <-ctx.Done():
			return Outcome{}, claim, ctx.Err()
		}
	}
}

type claimEvidencePointers struct {
	run, attempt, expired executionpostgres.PayloadPointer
}

func (handler Handler) claimEvidence(ctx context.Context, command eventpostgres.DeliveredCommand, payloadValue CommandPayload) (claimEvidencePointers, error) {
	base := map[string]any{
		"schema_version": 1, "run_id": command.AggregateID, "command_id": command.CommandID,
		"command_type": command.CommandType, "correlation_id": payloadValue.CorrelationID,
		"worker_id": handler.WorkerID, "queue_generation": command.QueueGeneration,
		"dispatch_version": command.DispatchVersion,
	}
	run, err := handler.putEvent(ctx, command.TenantID, command.CommandID+":run-claimed", base, "run_claimed")
	if err != nil {
		return claimEvidencePointers{}, err
	}
	attempt, err := handler.putEvent(ctx, command.TenantID, command.CommandID+":attempt-started", base, "job_attempt_started")
	if err != nil {
		return claimEvidencePointers{}, err
	}
	expired, err := handler.putEvent(ctx, command.TenantID, command.CommandID+":attempt-expired", base, "job_attempt_expired")
	return claimEvidencePointers{run: run, attempt: attempt, expired: expired}, err
}

func (handler Handler) completion(ctx context.Context, claim executionpostgres.RunClaim, command CommandPayload, outcome Outcome) (executionpostgres.CompleteRunCommand, error) {
	if !statemachine.Runs.IsTerminal(outcome.State) || outcome.ResultHash == "" || outcome.RunEvent == nil && outcome.Child == nil || outcome.AttemptEvent == nil || outcome.Tools != nil || outcome.Approvals != nil || outcome.Children != nil || outcome.State == statemachine.RunSucceeded && (outcome.MessageID == "" || outcome.Message == nil) {
		return executionpostgres.CompleteRunCommand{}, ErrOutcome
	}
	attempt, err := handler.putEvent(ctx, claim.TenantID, claim.AttemptID+":attempt-completed", outcome.AttemptEvent, "job_attempt_completed")
	if err != nil {
		return executionpostgres.CompleteRunCommand{}, err
	}
	result := executionpostgres.CompleteRunCommand{Claim: claim, ExpectedRunVersion: claim.RunVersion, TargetState: outcome.State, ResultHash: outcome.ResultHash, Actor: handler.Actor, CorrelationID: command.CorrelationID, AttemptCompletedEvent: attempt}
	if outcome.Child == nil {
		run, putErr := handler.putEvent(ctx, claim.TenantID, claim.AttemptID+":run-terminal", outcome.RunEvent, "run_terminal")
		result.RunEvent = run
		return result, putErr
	}
	if outcome.RunEvent != nil {
		return executionpostgres.CompleteRunCommand{}, ErrOutcome
	}
	child, err := handler.childCompletion(ctx, claim, *outcome.Child)
	if err != nil {
		return executionpostgres.CompleteRunCommand{}, err
	}
	result.Child = &child
	return result, nil
}

func (handler Handler) putMessage(ctx context.Context, claim executionpostgres.RunClaim, messageID string, document *MessageDocument) (executionpostgres.PayloadPointer, string, error) {
	if messageID == "" || document == nil || document.SchemaVersion != 1 || document.Role != "assistant" || len(document.Content) == 0 || len(document.Content) > 1024 {
		return executionpostgres.PayloadPointer{}, "", ErrOutcome
	}
	for _, block := range document.Content {
		if block.Type == "" {
			return executionpostgres.PayloadPointer{}, "", ErrOutcome
		}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return executionpostgres.PayloadPointer{}, "", err
	}
	digest := sha256.Sum256(encoded)
	contentHash := hex.EncodeToString(digest[:])
	manifest, err := handler.Payloads.Put(ctx, payload.Descriptor{TenantID: claim.TenantID, ObjectID: messageID, Class: "run-message", ContentType: "application/json"}, encoded)
	return executionpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, contentHash, err
}

func (handler Handler) childCompletion(ctx context.Context, claim executionpostgres.RunClaim, child ChildOutcome) (executionpostgres.ChildRunCompletion, error) {
	values := []struct {
		name  string
		value any
	}{
		{"result-summary", child.ResultSummary}, {"child-completed", child.CompletedEvent},
		{"group-joined", child.GroupJoinedEvent}, {"run-resume-queued", child.RunResumeQueuedEvent},
		{"resume-command", child.ResumeCommand}, {"cancel-remaining-command", child.CancelRemainingCommand},
	}
	pointers := make([]executionpostgres.PayloadPointer, len(values))
	for index, value := range values {
		if value.value == nil {
			return executionpostgres.ChildRunCompletion{}, ErrOutcome
		}
		pointer, err := handler.putEvent(ctx, claim.TenantID, claim.AttemptID+":"+value.name, value.value, value.name)
		if err != nil {
			return executionpostgres.ChildRunCompletion{}, err
		}
		pointers[index] = pointer
	}
	return executionpostgres.ChildRunCompletion{
		ResultSummary: pointers[0], CompletedEvent: pointers[1], GroupJoinedEvent: pointers[2],
		RunResumeQueuedEvent: pointers[3], ResumeCommand: pointers[4], CancelRemainingCommand: pointers[5],
		ResumeQueueClass: child.ResumeQueueClass, ResumeResourceClass: child.ResumeResourceClass,
		ResumePriority: child.ResumePriority, ResumeCostUnits: child.ResumeCostUnits, ResumeMaxAttempts: child.ResumeMaxAttempts,
	}, nil
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

func (handler Handler) loadCommand(ctx context.Context, delivered eventpostgres.DeliveredCommand) (CommandPayload, error) {
	descriptor := payload.Descriptor{TenantID: delivered.TenantID, ObjectID: delivered.CommandID, Class: "agent-run-command", ContentType: "application/json"}
	encoded, err := handler.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: delivered.PayloadRef, Hash: delivered.PayloadHash})
	if err != nil || len(encoded) == 0 || len(encoded) > handler.maximumCommand() {
		return CommandPayload{}, ErrCommand
	}
	var command CommandPayload
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&command); err != nil {
		return CommandPayload{}, ErrCommand
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return CommandPayload{}, ErrCommand
	}
	if command.SchemaVersion != commandSchemaVersion || command.RunID != delivered.AggregateID || command.CorrelationID == "" || delivered.AggregateKind != "run" || delivered.QueueGeneration < 1 || delivered.DispatchVersion < 1 {
		return CommandPayload{}, ErrCommand
	}
	return command, nil
}

func (handler Handler) validate() error {
	var actor map[string]any
	if handler.Payloads == nil || handler.Runs == nil || handler.Runner == nil || handler.ConsumerName == "" || handler.WorkerID == "" || handler.HeartbeatInterval <= 0 || json.Unmarshal(handler.Actor, &actor) != nil || actor == nil || handler.maximumCommand() < 1 {
		return ErrConfiguration
	}
	return nil
}

func (handler Handler) maximumCommand() int {
	if handler.MaximumCommand == 0 {
		return 1 << 20
	}
	return handler.MaximumCommand
}

func mapClaimError(err error) error {
	switch {
	case errors.Is(err, executionpostgres.ErrClaimCompleted):
		return nil
	case errors.Is(err, executionpostgres.ErrClaimBusy):
		return errors.Join(eventpostgres.ErrDeliveryBusy, err)
	case errors.Is(err, executionpostgres.ErrStaleEpoch):
		return errors.Join(eventpostgres.ErrStaleStoreEpoch, err)
	case errors.Is(err, executionpostgres.ErrClaimConflict), errors.Is(err, executionpostgres.ErrRunNotClaimable):
		return errors.Join(eventpostgres.ErrDeliveryConflict, err)
	default:
		return fmt.Errorf("claim agent run: %w", err)
	}
}
