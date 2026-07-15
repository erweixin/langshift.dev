package toolworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/langshift/lites/internal/execution/statemachine"
	"github.com/langshift/lites/internal/toolregistry"
)

var (
	ErrExecutorConfiguration = errors.New("tool registry executor configuration is invalid")
	ErrExecutionBinding      = errors.New("tool execution does not match its immutable descriptor")
	ErrHandlerExecution      = errors.New("tool handler infrastructure failure")
	ErrHandlerResult         = errors.New("tool handler returned an invalid result")
	ErrHandlerPanic          = errors.New("tool handler panicked")
)

var resultCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)

type ResultStatus string

const (
	ResultSucceeded      ResultStatus = "succeeded"
	ResultFailed         ResultStatus = "failed"
	ResultOutcomeUnknown ResultStatus = "outcome_unknown"
)

// Invocation is the complete immutable authority passed to code-declared
// handlers. Handlers must use ProviderRequestID as the downstream idempotency
// identifier and must not derive a replacement effect key.
type Invocation struct {
	TenantID, UserID, RunID, ToolCallID, AttemptID string
	ProviderRequestID                              string
	EffectKey, EffectScope, ProviderID             string
	Snapshot                                       toolregistry.Snapshot
	Input                                          json.RawMessage
}

type Failure struct {
	Code        string
	SafeMessage string
	Retryable   bool
}

// HandlerResult reports a known external outcome. A failed result asserts that
// the effect boundary was not crossed. Any uncertain write must explicitly use
// ResultOutcomeUnknown (or return an unclassified error, which fails safe to
// outcome_unknown).
type HandlerResult struct {
	Status              ResultStatus
	Output              json.RawMessage
	Failure             Failure
	ExternalResourceRef string
}

type ToolHandler interface {
	Invoke(context.Context, Invocation) (HandlerResult, error)
}

type HandlerRegistration struct {
	Name    string
	Handler ToolHandler
}

// BeforeEffectError marks an infrastructure failure that is proven to have
// occurred before an external write could start. It is the only write-handler
// error that may be retried without first entering reconciliation.
type BeforeEffectError struct{ Err error }

func (failure BeforeEffectError) Error() string {
	if failure.Err == nil {
		return "tool failed before effect boundary"
	}
	return failure.Err.Error()
}

func (failure BeforeEffectError) Unwrap() error { return failure.Err }

func BeforeEffect(err error) error { return BeforeEffectError{Err: err} }

type RegistryExecutor struct {
	registry              *toolregistry.Registry
	handlers              map[string]ToolHandler
	defaultReconcileAfter time.Duration
}

// NewRegistryExecutor freezes the handler table and requires exact coverage of
// every worker-backed descriptor. This turns catalog/worker image drift into a
// startup failure rather than a command-time partial outage.
func NewRegistryExecutor(registry *toolregistry.Registry, registrations []HandlerRegistration, defaultReconcileAfter time.Duration) (*RegistryExecutor, error) {
	if registry == nil || registry.Hash() == "" || defaultReconcileAfter < time.Second || defaultReconcileAfter > 24*time.Hour {
		return nil, ErrExecutorConfiguration
	}
	required := map[string]struct{}{}
	for _, snapshot := range registry.Snapshots() {
		if snapshot.Descriptor.ExecutionKind == toolregistry.ExecutionWorker {
			required[snapshot.Descriptor.Handler] = struct{}{}
		}
	}
	handlers := make(map[string]ToolHandler, len(registrations))
	for _, registration := range registrations {
		if _, needed := required[registration.Name]; !needed || registration.Name == "" || nilInterface(registration.Handler) {
			return nil, ErrExecutorConfiguration
		}
		if _, duplicate := handlers[registration.Name]; duplicate {
			return nil, ErrExecutorConfiguration
		}
		handlers[registration.Name] = registration.Handler
	}
	if len(handlers) != len(required) {
		return nil, ErrExecutorConfiguration
	}
	return &RegistryExecutor{registry: registry, handlers: handlers, defaultReconcileAfter: defaultReconcileAfter}, nil
}

func (executor *RegistryExecutor) Execute(ctx context.Context, execution Execution) (Outcome, error) {
	if executor == nil || executor.registry == nil {
		return Outcome{}, ErrExecutorConfiguration
	}
	snapshot, err := executor.registry.Resolve(execution.Payload.DescriptorSnapshotID, execution.Payload.DescriptorHash)
	if err != nil || !matchesExecution(snapshot, execution) {
		return Outcome{}, errors.Join(ErrExecutionBinding, err)
	}
	normalized, inputHash, requestHash, err := executor.registry.NormalizeInput(snapshot.SnapshotID, snapshot.Hash, execution.Input)
	if err != nil || !bytes.Equal(normalized, execution.Input) || inputHash != execution.Payload.NormalizedInputHash || requestHash != execution.Payload.RequestHash {
		return Outcome{}, errors.Join(ErrExecutionBinding, err)
	}
	handler, ok := executor.handlers[snapshot.Descriptor.Handler]
	if !ok || nilInterface(handler) {
		return Outcome{}, ErrExecutorConfiguration
	}
	timeout, err := time.ParseDuration(snapshot.Descriptor.Resources.Timeout)
	if err != nil {
		return Outcome{}, ErrExecutorConfiguration
	}
	invocation := Invocation{
		TenantID: execution.Claim.TenantID, UserID: execution.Claim.UserID,
		RunID: execution.Claim.RunID, ToolCallID: execution.Claim.ToolCallID,
		AttemptID: execution.Claim.AttemptID, ProviderRequestID: execution.Claim.ProviderRequestID,
		EffectKey: execution.Payload.EffectKey, EffectScope: execution.Payload.EffectScope,
		ProviderID: execution.Payload.ProviderID, Snapshot: snapshot,
		Input: append(json.RawMessage(nil), normalized...),
	}
	invokeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, invokeErr := invokeWithDeadline(invokeCtx, ctx, handler, invocation)
	if invokeErr != nil {
		var before BeforeEffectError
		if snapshot.Descriptor.EffectClass == "read_only" || errors.As(invokeErr, &before) {
			return Outcome{}, handlerExecutionError{cause: invokeErr}
		}
		code := "handler_error_after_effect_boundary"
		if errors.Is(invokeErr, context.DeadlineExceeded) {
			code = "handler_timeout_after_effect_boundary"
		} else if errors.Is(invokeErr, ErrHandlerPanic) {
			code = "handler_panic_after_effect_boundary"
		}
		return executor.unknown(snapshot, execution, Failure{Code: code, SafeMessage: "The external operation may have been applied and is being reconciled."}), nil
	}
	return executor.translate(snapshot, execution, result)
}

func (executor *RegistryExecutor) translate(snapshot toolregistry.Snapshot, execution Execution, result HandlerResult) (Outcome, error) {
	if !validExternalReference(result.ExternalResourceRef) {
		return Outcome{}, ErrHandlerResult
	}
	switch result.Status {
	case ResultSucceeded:
		if !zeroFailure(result.Failure) || len(result.Output) == 0 || snapshot.Descriptor.EffectClass == "read_only" && result.ExternalResourceRef != "" {
			return Outcome{}, ErrHandlerResult
		}
		output, _, err := executor.registry.ValidateOutput(snapshot.SnapshotID, snapshot.Hash, result.Output)
		if err != nil {
			return Outcome{}, errors.Join(ErrHandlerResult, err)
		}
		outcome := Outcome{State: statemachine.ToolCallSucceeded, Result: output, ExternalResourceRef: result.ExternalResourceRef}
		if snapshot.Descriptor.EffectClass != "read_only" {
			outcome.EffectDisposition = EffectConfirmed
		}
		return outcome, nil
	case ResultFailed:
		if len(result.Output) != 0 || result.ExternalResourceRef != "" || !validFailure(result.Failure) {
			return Outcome{}, ErrHandlerResult
		}
		encoded, err := encodeFailure(ResultFailed, result.Failure, execution.Claim.ProviderRequestID)
		if err != nil {
			return Outcome{}, err
		}
		outcome := Outcome{State: statemachine.ToolCallFailed, Result: encoded}
		if snapshot.Descriptor.EffectClass != "read_only" {
			outcome.EffectDisposition = EffectNotApplied
		}
		return outcome, nil
	case ResultOutcomeUnknown:
		if snapshot.Descriptor.EffectClass == "read_only" || len(result.Output) != 0 || !validFailure(result.Failure) {
			return Outcome{}, ErrHandlerResult
		}
		return executor.unknown(snapshot, execution, result.Failure, result.ExternalResourceRef), nil
	default:
		return Outcome{}, ErrHandlerResult
	}
}

func (executor *RegistryExecutor) unknown(snapshot toolregistry.Snapshot, execution Execution, failure Failure, externalResourceRef ...string) Outcome {
	ref := ""
	if len(externalResourceRef) == 1 {
		ref = externalResourceRef[0]
	}
	encoded, _ := encodeFailure(ResultOutcomeUnknown, failure, execution.Claim.ProviderRequestID)
	reconcileAfter := snapshot.ReconcileAfter
	if reconcileAfter <= 0 {
		reconcileAfter = executor.defaultReconcileAfter
	}
	return Outcome{
		State: statemachine.ToolCallOutcomeUnknown, Result: encoded,
		ExternalResourceRef: ref, EffectDisposition: EffectUnknown,
		ReconcileAfter: reconcileAfter,
	}
}

func matchesExecution(snapshot toolregistry.Snapshot, execution Execution) bool {
	descriptor, command, claim := snapshot.Descriptor, execution.Payload, execution.Claim
	return descriptor.ExecutionKind == toolregistry.ExecutionWorker && descriptor.Name == command.ToolName && descriptor.EffectClass == command.EffectClass &&
		claim.ToolCallID == command.ToolCallID && claim.RunID == command.RunID && claim.EffectClass == command.EffectClass &&
		claim.Binding.ToolName == command.ToolName && claim.Binding.DescriptorSnapshotID == command.DescriptorSnapshotID &&
		claim.Binding.NormalizedInputRef == command.Input.Ref && claim.Binding.RequestHash == command.RequestHash && claim.Binding.EffectKey == command.EffectKey &&
		claim.Binding.EffectScope == command.EffectScope && claim.Binding.ProviderID == command.ProviderID
}

func invokeHandler(ctx context.Context, handler ToolHandler, invocation Invocation) (result HandlerResult, err error) {
	defer func() {
		if recover() != nil {
			result = HandlerResult{}
			err = ErrHandlerPanic
		}
	}()
	return handler.Invoke(ctx, invocation)
}

func invokeWithDeadline(invokeCtx, parent context.Context, handler ToolHandler, invocation Invocation) (HandlerResult, error) {
	type invocationResult struct {
		result HandlerResult
		err    error
	}
	finished := make(chan invocationResult, 1)
	go func() {
		result, err := invokeHandler(invokeCtx, handler, invocation)
		finished <- invocationResult{result: result, err: err}
	}()
	select {
	case completed := <-finished:
		return completed.result, completed.err
	case <-invokeCtx.Done():
		if parent.Err() != nil {
			return HandlerResult{}, parent.Err()
		}
		return HandlerResult{}, context.DeadlineExceeded
	}
}

func encodeFailure(status ResultStatus, failure Failure, providerRequestID string) (json.RawMessage, error) {
	return json.Marshal(struct {
		SchemaVersion     int          `json:"schema_version"`
		Status            ResultStatus `json:"status"`
		Code              string       `json:"code"`
		Message           string       `json:"message"`
		Retryable         bool         `json:"retryable"`
		ProviderRequestID string       `json:"provider_request_id,omitempty"`
	}{1, status, failure.Code, failure.SafeMessage, failure.Retryable, providerRequestID})
}

func validFailure(failure Failure) bool {
	message := strings.TrimSpace(failure.SafeMessage)
	return resultCodePattern.MatchString(failure.Code) && message == failure.SafeMessage && message != "" && len(message) <= 1024 && !strings.ContainsAny(message, "\x00\r\n")
}

func zeroFailure(failure Failure) bool {
	return failure.Code == "" && failure.SafeMessage == "" && !failure.Retryable
}

func validExternalReference(reference string) bool {
	return len(reference) <= 2048 && !strings.ContainsAny(reference, "\x00\r\n")
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

type handlerExecutionError struct{ cause error }

func (handlerExecutionError) Error() string { return ErrHandlerExecution.Error() }
func (failure handlerExecutionError) Unwrap() error {
	return errors.Join(ErrHandlerExecution, failure.cause)
}
