package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/runtime/firecracker"
	"github.com/langshift/lites/internal/runtime/guest"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
)

const (
	runtimeExecutionInputClass  = "runtime-execution-input"
	runtimeExecutionResultClass = "runtime-execution-result"
)

type ExecuteRequest struct {
	TenantID, SessionID, CapabilityToken, ProvisionLease string
	RequestID                                            string
	Command                                              guest.ExecuteRequest
}

type Executed struct {
	Execution runtimepostgres.RuntimeExecution `json:"execution"`
	Result    guest.ExecutionResult            `json:"result"`
	Stdout    []byte                           `json:"stdout,omitempty"`
	Stderr    []byte                           `json:"stderr,omitempty"`
}

type executionInputEvidence struct {
	SchemaVersion int                  `json:"schema_version"`
	SessionID     string               `json:"session_id"`
	RequestID     string               `json:"request_id"`
	Command       guest.ExecuteRequest `json:"command"`
}

type executionResultEvidence struct {
	SchemaVersion int                   `json:"schema_version"`
	SessionID     string                `json:"session_id"`
	RequestID     string                `json:"request_id"`
	Kind          string                `json:"kind"`
	Result        guest.ExecutionResult `json:"result"`
	Stdout        []byte                `json:"stdout,omitempty"`
	Stderr        []byte                `json:"stderr,omitempty"`
	ObservedAt    time.Time             `json:"observed_at"`
}

func (controller *Controller) Execute(ctx context.Context, request ExecuteRequest) (Executed, error) {
	if controller == nil || controller.Store == nil || controller.Payloads == nil || controller.Ownership == nil || controller.MaximumExecutionDuration <= 0 || controller.MaximumExecutionDuration > time.Hour || request.TenantID == "" || request.SessionID == "" || request.CapabilityToken == "" || request.ProvisionLease == "" || request.RequestID == "" || request.Command.Validate() != nil {
		return Executed{}, ErrConfiguration
	}
	deadline := time.UnixMilli(request.Command.DeadlineUnixMillis)
	if !deadline.After(controller.now()) || deadline.After(controller.now().Add(controller.MaximumExecutionDuration)) {
		return Executed{}, runtimepostgres.ErrInvalidCommand
	}
	active := controller.lookup(request.SessionID)
	if active == nil {
		return Executed{}, ErrNotOwned
	}
	active.mu.Lock()
	if active.closing || active.executing || active.request.Command.TenantID != request.TenantID {
		active.mu.Unlock()
		return Executed{}, ErrNotOwned
	}
	commandBytes, err := json.Marshal(request.Command)
	if err != nil {
		active.mu.Unlock()
		return Executed{}, ErrConfiguration
	}
	requestDigest := sha256.Sum256(commandBytes)
	inputPointer, _, err := controller.putExecutionPayload(ctx, request.TenantID, executionInputObjectID(request.SessionID, request.RequestID), runtimeExecutionInputClass, executionInputEvidence{
		SchemaVersion: 1, SessionID: request.SessionID, RequestID: request.RequestID, Command: request.Command,
	})
	if err != nil {
		active.mu.Unlock()
		return Executed{}, err
	}
	begin := runtimepostgres.BeginExecutionCommand{LifecycleCommand: runtimepostgres.LifecycleCommand{
		TenantID: request.TenantID, SessionID: request.SessionID, ProvisionAttemptID: active.result.ProvisionAttemptID,
		ProvisionFence: 1, ExpectedVersion: active.version, LeaseToken: request.ProvisionLease, ObservedAt: controller.now(),
		Payload: inputPointer, Actor: active.request.Command.Actor, CorrelationID: active.request.Command.CorrelationID,
	}, CapabilityToken: request.CapabilityToken, RequestID: request.RequestID, RequestHash: hex.EncodeToString(requestDigest[:])}
	execution, err := controller.Store.BeginExecution(ctx, begin)
	if err != nil {
		active.mu.Unlock()
		return Executed{}, err
	}
	if execution.Status == "completed" {
		active.version = execution.FinishedSessionVersion
		active.mu.Unlock()
		return controller.replayExecution(ctx, request, execution)
	}
	if execution.Status == "outcome_unknown" {
		active.version = execution.FinishedSessionVersion
		active.mu.Unlock()
		return Executed{}, ErrOutcomeUnknown
	}
	active.version = execution.StartedSessionVersion
	if execution.Replayed {
		active.mu.Unlock()
		return controller.settleUnknownExecution(ctx, active, request, begin, execution, "execution_replay_without_owner")
	}
	executionCtx, cancel := context.WithDeadline(ctx, deadline)
	active.executing, active.executionCancel, active.executionDone = true, cancel, make(chan struct{})
	active.mu.Unlock()

	var stdout, stderr []byte
	result, executeErr := controller.executeGuest(executionCtx, active.staged.vsockPath, request.RequestID, request.Command, func(frame guest.Frame) error {
		switch frame.Kind {
		case guest.FrameStdout:
			stdout = append(stdout, frame.Chunk...)
		case guest.FrameStderr:
			stderr = append(stderr, frame.Chunk...)
		}
		return nil
	})
	cancel()
	if executeErr != nil {
		return controller.settleUnknownExecution(context.WithoutCancel(ctx), active, request, begin, execution, "guest_transport_unknown")
	}

	durableCtx, durableCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer durableCancel()
	resultPointer, outcomeHash, persistErr := controller.putExecutionPayload(durableCtx, request.TenantID, executionResultObjectID(request.SessionID, request.RequestID), runtimeExecutionResultClass, executionResultEvidence{
		SchemaVersion: 1, SessionID: request.SessionID, RequestID: request.RequestID, Kind: "guest_result", Result: result,
		Stdout: stdout, Stderr: stderr, ObservedAt: controller.now(),
	})
	if persistErr != nil {
		controller.finishExecutionSlot(active, active.version, nil)
		return Executed{}, errors.Join(ErrOutcomeUnknown, persistErr, controller.terminateUnlessClosing(durableCtx, active, request.SessionID, "execution_result_persistence_failed"))
	}
	manifest, err := json.Marshal(runtimepostgres.ExecutionOutcomeManifest{
		SchemaVersion: 1, RequestID: request.RequestID, Kind: "guest_result", ExitCode: &result.ExitCode,
		FailureCode: result.FailureCode, TimedOut: result.TimedOut, OutputTruncated: result.OutputTruncated,
		StdoutBytes: result.StdoutBytes, StderrBytes: result.StderrBytes, UserCPUTimeMillis: result.UserCPUTimeMillis,
		SystemCPUTimeMillis: result.SystemCPUTimeMillis, StartedUnixMillis: result.StartedUnixMillis, FinishedUnixMillis: result.FinishedUnixMillis,
	})
	if err != nil {
		controller.finishExecutionSlot(active, active.version, nil)
		return Executed{}, errors.Join(ErrOutcomeUnknown, err, controller.terminateUnlessClosing(durableCtx, active, request.SessionID, "execution_manifest_failed"))
	}
	finish := runtimepostgres.FinishExecutionCommand{LifecycleCommand: begin.LifecycleCommand, RequestID: request.RequestID, RequestHash: begin.RequestHash, OutcomeHash: outcomeHash, OutcomeManifest: manifest}
	finish.ExpectedVersion, finish.ObservedAt, finish.Payload = execution.StartedSessionVersion, controller.now(), resultPointer
	completed, completeErr := controller.Store.CompleteExecution(durableCtx, finish)
	if completeErr != nil {
		completed, completeErr = controller.Store.CompleteExecution(durableCtx, finish)
	}
	if completeErr != nil {
		controller.finishExecutionSlot(active, active.version, nil)
		return Executed{}, errors.Join(ErrOutcomeUnknown, completeErr, controller.terminateUnlessClosing(durableCtx, active, request.SessionID, "execution_completion_unknown"))
	}
	controller.finishExecutionSlot(active, completed.FinishedSessionVersion, nil)
	return Executed{Execution: completed, Result: result, Stdout: stdout, Stderr: stderr}, nil
}

func (controller *Controller) settleUnknownExecution(ctx context.Context, active *activeMachine, request ExecuteRequest, begin runtimepostgres.BeginExecutionCommand, execution runtimepostgres.RuntimeExecution, reason string) (Executed, error) {
	durableCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	pointer, outcomeHash, evidenceErr := controller.putExecutionPayload(durableCtx, request.TenantID, executionResultObjectID(request.SessionID, request.RequestID), runtimeExecutionResultClass, executionResultEvidence{
		SchemaVersion: 1, SessionID: request.SessionID, RequestID: request.RequestID, Kind: "transport_unknown", ObservedAt: controller.now(),
	})
	if evidenceErr != nil {
		controller.finishExecutionSlot(active, active.version, nil)
		return Executed{}, errors.Join(ErrOutcomeUnknown, evidenceErr, controller.terminateUnlessClosing(durableCtx, active, request.SessionID, reason))
	}
	manifest, err := json.Marshal(runtimepostgres.ExecutionOutcomeManifest{SchemaVersion: 1, RequestID: request.RequestID, Kind: "transport_unknown", FailureCode: "transport_failed", ObservedUnixMillis: controller.now().UnixMilli(), TerminationReason: reason})
	if err != nil {
		controller.finishExecutionSlot(active, active.version, nil)
		return Executed{}, errors.Join(ErrOutcomeUnknown, err, controller.terminateUnlessClosing(durableCtx, active, request.SessionID, reason))
	}
	finish := runtimepostgres.FinishExecutionCommand{LifecycleCommand: begin.LifecycleCommand, RequestID: request.RequestID, RequestHash: begin.RequestHash, OutcomeHash: outcomeHash, OutcomeManifest: manifest, FailureCode: "transport_failed"}
	finish.ExpectedVersion, finish.ObservedAt, finish.Payload = execution.StartedSessionVersion, controller.now(), pointer
	unknown, markErr := controller.Store.MarkExecutionOutcomeUnknown(durableCtx, finish)
	if markErr == nil {
		termination := unknown.Lifecycle
		controller.finishExecutionSlot(active, unknown.FinishedSessionVersion, &termination)
	} else {
		controller.finishExecutionSlot(active, active.version, nil)
	}
	terminateErr := controller.terminateUnlessClosing(durableCtx, active, request.SessionID, reason)
	return Executed{}, errors.Join(ErrOutcomeUnknown, markErr, terminateErr)
}

func (controller *Controller) executeGuest(ctx context.Context, socketPath, requestID string, request guest.ExecuteRequest, sink guest.FrameSink) (guest.ExecutionResult, error) {
	if controller.execute != nil {
		return controller.execute(ctx, socketPath, requestID, request, sink)
	}
	client := firecracker.GuestClient{Connector: firecracker.VSockConnector{SocketPath: socketPath, Timeout: 5 * time.Second}, WriteTimeout: 5 * time.Second}
	return client.Execute(ctx, requestID, request, sink)
}

func (controller *Controller) replayExecution(ctx context.Context, request ExecuteRequest, execution runtimepostgres.RuntimeExecution) (Executed, error) {
	encoded, err := controller.Payloads.Get(ctx, payload.Descriptor{TenantID: request.TenantID, ObjectID: executionResultObjectID(request.SessionID, request.RequestID), Class: runtimeExecutionResultClass, ContentType: "application/json"}, payload.Manifest{Ref: execution.Outcome.Ref, Hash: execution.Outcome.Hash})
	if err != nil || sha256Hex(encoded) != execution.OutcomeHash {
		return Executed{}, errors.Join(err, ErrOutcomeUnknown)
	}
	var evidence executionResultEvidence
	decoderErr := json.Unmarshal(encoded, &evidence)
	if decoderErr != nil || evidence.SchemaVersion != 1 || evidence.SessionID != request.SessionID || evidence.RequestID != request.RequestID || evidence.Kind != "guest_result" {
		return Executed{}, errors.Join(decoderErr, ErrOutcomeUnknown)
	}
	execution.Replayed = true
	return Executed{Execution: execution, Result: evidence.Result, Stdout: evidence.Stdout, Stderr: evidence.Stderr}, nil
}

func (controller *Controller) putExecutionPayload(ctx context.Context, tenantID, objectID, class string, value any) (runtimepostgres.PayloadPointer, string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return runtimepostgres.PayloadPointer{}, "", err
	}
	manifest, err := controller.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, encoded)
	if err != nil || manifest.Ref == "" || manifest.Hash == "" {
		return runtimepostgres.PayloadPointer{}, "", errors.Join(err, ErrConfiguration)
	}
	return runtimepostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, sha256Hex(encoded), nil
}

func (controller *Controller) finishExecutionSlot(active *activeMachine, version uint64, termination *runtimepostgres.LifecycleResult) {
	active.mu.Lock()
	active.version, active.executing, active.executionCancel = version, false, nil
	if termination != nil {
		copyValue := *termination
		active.termination = &copyValue
	}
	if active.executionDone != nil {
		close(active.executionDone)
		active.executionDone = nil
	}
	active.mu.Unlock()
}

func (controller *Controller) terminateUnlessClosing(ctx context.Context, active *activeMachine, sessionID, reason string) error {
	active.mu.Lock()
	closing := active.closing
	active.mu.Unlock()
	if closing {
		return nil
	}
	_, err := controller.Terminate(ctx, sessionID, reason)
	return err
}

func (controller *Controller) lookup(sessionID string) *activeMachine {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.active[sessionID]
}

func executionInputObjectID(sessionID, requestID string) string {
	return sessionID + ":execution:" + requestID + ":input"
}

func executionResultObjectID(sessionID, requestID string) string {
	return sessionID + ":execution:" + requestID + ":result"
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
