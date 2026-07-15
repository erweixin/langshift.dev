package toolworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	runtimeclient "github.com/langshift/lites/internal/runtime/client"
	"github.com/langshift/lites/internal/runtime/controller"
	"github.com/langshift/lites/internal/runtime/guest"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
	"github.com/langshift/lites/internal/toolregistry"
)

const sandboxToolRunner = "/opt/lites/bin/tool-runner"

var (
	ErrSandboxConfiguration = errors.New("sandbox executor configuration is invalid")
	ErrSandboxProtocol      = errors.New("sandbox tool runner protocol is invalid")
	ErrSandboxExecution     = errors.New("sandbox execution failed")
)

type SandboxHost interface {
	Execute(context.Context, runtimeclient.ExecuteRequest) (controller.Executed, error)
	Terminate(context.Context, string, string) (runtimepostgres.LifecycleResult, error)
}

type SandboxSession struct {
	TenantID, SessionID, CapabilityToken, ProvisionLease string
	Host                                                 SandboxHost
}

type SandboxAcquireRequest struct {
	Execution Execution
	Snapshot  toolregistry.Snapshot
	Command   guest.ExecuteRequest
	RequestID string
}

// SandboxBroker owns current policy selection, RuntimeSession capability
// issuance, host capacity selection and provisioning. It must return the same
// fenced session for an exact attempt replay.
type SandboxBroker interface {
	Acquire(context.Context, SandboxAcquireRequest) (SandboxSession, error)
}

type SandboxExecutor struct {
	Registry              *toolregistry.Registry
	Broker                SandboxBroker
	DefaultReconcileAfter time.Duration
	CleanupTimeout        time.Duration
	Now                   func() time.Time
}

type sandboxRunnerResult struct {
	SchemaVersion       int             `json:"schema_version"`
	Status              ResultStatus    `json:"status"`
	Output              json.RawMessage `json:"output,omitempty"`
	Failure             Failure         `json:"failure"`
	ExternalResourceRef string          `json:"external_resource_ref,omitempty"`
}

func (executor SandboxExecutor) Execute(ctx context.Context, execution Execution) (Outcome, error) {
	if executor.Registry == nil || nilInterface(executor.Broker) || executor.Registry.Hash() == "" || executor.DefaultReconcileAfter < time.Second || executor.DefaultReconcileAfter > 24*time.Hour || executor.CleanupTimeout != 0 && (executor.CleanupTimeout < time.Second || executor.CleanupTimeout > time.Minute) {
		return Outcome{}, ErrSandboxConfiguration
	}
	snapshot, err := executor.Registry.Resolve(execution.Payload.DescriptorSnapshotID, execution.Payload.DescriptorHash)
	if err != nil || !matchesSandboxExecution(snapshot, execution) {
		return Outcome{}, errors.Join(ErrExecutionBinding, err)
	}
	normalized, inputHash, requestHash, err := executor.Registry.NormalizeInput(snapshot.SnapshotID, snapshot.Hash, execution.Input)
	if err != nil || !bytes.Equal(normalized, execution.Input) || inputHash != execution.Payload.NormalizedInputHash || requestHash != execution.Payload.RequestHash {
		return Outcome{}, errors.Join(ErrExecutionBinding, err)
	}
	timeout, err := time.ParseDuration(snapshot.Descriptor.Resources.Timeout)
	if err != nil || timeout < time.Second || timeout > time.Hour {
		return Outcome{}, ErrSandboxConfiguration
	}
	deadline := executor.now().Add(timeout)
	if execution.Claim.LeaseExpiresAt.Before(deadline) {
		deadline = execution.Claim.LeaseExpiresAt
	}
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if !deadline.After(executor.now().Add(time.Second)) {
		return Outcome{}, errors.Join(ErrSandboxExecution, context.DeadlineExceeded)
	}
	maximumOutput := int64(snapshot.Descriptor.Resources.MaximumOutput)
	if maximumOutput > 16<<20 {
		maximumOutput = 16 << 20
	}
	requestID := "sandbox:" + execution.Claim.AttemptID
	command := guest.ExecuteRequest{
		Argv:             []string{sandboxToolRunner, "--tool", snapshot.Descriptor.Name, "--snapshot", snapshot.SnapshotID, "--handler", snapshot.Descriptor.Handler},
		WorkingDirectory: "/workspace", Environment: []guest.EnvironmentVariable{{Name: "LITES_REQUEST_ID", Value: requestID}},
		Stdin: normalized, DeadlineUnixMillis: deadline.UnixMilli(), MaximumOutputBytes: maximumOutput,
	}
	session, err := executor.Broker.Acquire(ctx, SandboxAcquireRequest{Execution: execution, Snapshot: snapshot, Command: command, RequestID: requestID})
	if err != nil {
		return Outcome{}, err
	}
	if session.TenantID != execution.Claim.TenantID || session.SessionID == "" || session.CapabilityToken == "" || session.ProvisionLease == "" || nilInterface(session.Host) {
		return Outcome{}, ErrSandboxConfiguration
	}
	executed, executeErr := session.Host.Execute(ctx, runtimeclient.ExecuteRequest{
		TenantID: session.TenantID, SessionID: session.SessionID, CapabilityToken: session.CapabilityToken,
		ProvisionLease: session.ProvisionLease, RequestID: requestID, Command: command,
	})
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), executor.cleanupTimeout())
	_, cleanupErr := session.Host.Terminate(cleanupCtx, session.SessionID, "tool_execution_complete")
	cancel()
	if cleanupErr != nil && !runtimeAlreadyTerminated(cleanupErr) {
		return Outcome{}, errors.Join(ErrSandboxExecution, executeErr, cleanupErr)
	}
	translator := RegistryExecutor{registry: executor.Registry, defaultReconcileAfter: executor.DefaultReconcileAfter}
	if executeErr != nil {
		if snapshot.Descriptor.EffectClass == "read_only" {
			return Outcome{}, errors.Join(ErrSandboxExecution, executeErr)
		}
		return translator.unknown(snapshot, execution, Failure{Code: "runtime_outcome_unknown", SafeMessage: "The sandbox operation may have been applied and is being reconciled."}), nil
	}
	if executed.Result.FailureCode != "" {
		if snapshot.Descriptor.EffectClass == "read_only" {
			return Outcome{}, errors.Join(ErrSandboxExecution, errors.New(executed.Result.FailureCode))
		}
		return translator.unknown(snapshot, execution, Failure{Code: "sandbox_process_outcome_unknown", SafeMessage: "The sandbox process stopped without a durable tool result and is being reconciled."}), nil
	}
	result, err := decodeSandboxRunnerResult(executed.Stdout)
	if err != nil {
		if snapshot.Descriptor.EffectClass == "read_only" {
			return Outcome{}, err
		}
		return translator.unknown(snapshot, execution, Failure{Code: "sandbox_result_protocol_unknown", SafeMessage: "The sandbox result could not be verified and is being reconciled."}), nil
	}
	return translator.translate(snapshot, execution, HandlerResult{Status: result.Status, Output: result.Output, Failure: result.Failure, ExternalResourceRef: result.ExternalResourceRef})
}

type DispatchExecutor struct {
	Registry *toolregistry.Registry
	Trusted  Executor
	Sandbox  Executor
}

func (dispatcher DispatchExecutor) Execute(ctx context.Context, execution Execution) (Outcome, error) {
	if dispatcher.Registry == nil || dispatcher.Registry.Hash() == "" || nilInterface(dispatcher.Trusted) || nilInterface(dispatcher.Sandbox) {
		return Outcome{}, ErrSandboxConfiguration
	}
	snapshot, err := dispatcher.Registry.Resolve(execution.Payload.DescriptorSnapshotID, execution.Payload.DescriptorHash)
	if err != nil {
		return Outcome{}, errors.Join(ErrExecutionBinding, err)
	}
	if snapshot.Descriptor.ExecutionKind != toolregistry.ExecutionWorker {
		return Outcome{}, ErrExecutionBinding
	}
	if snapshot.Descriptor.TrustTier == "trusted" {
		return dispatcher.Trusted.Execute(ctx, execution)
	}
	return dispatcher.Sandbox.Execute(ctx, execution)
}

func decodeSandboxRunnerResult(value []byte) (sandboxRunnerResult, error) {
	var result sandboxRunnerResult
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF || result.SchemaVersion != 1 || result.Status != ResultSucceeded && result.Status != ResultFailed && result.Status != ResultOutcomeUnknown {
		return sandboxRunnerResult{}, ErrSandboxProtocol
	}
	return result, nil
}

func matchesSandboxExecution(snapshot toolregistry.Snapshot, execution Execution) bool {
	descriptor, command, claim := snapshot.Descriptor, execution.Payload, execution.Claim
	return descriptor.ExecutionKind == toolregistry.ExecutionWorker && descriptor.TrustTier != "trusted" && descriptor.Name == command.ToolName && descriptor.EffectClass == command.EffectClass &&
		claim.ToolCallID == command.ToolCallID && claim.RunID == command.RunID && claim.EffectClass == command.EffectClass &&
		claim.Binding.ToolName == command.ToolName && claim.Binding.DescriptorSnapshotID == command.DescriptorSnapshotID &&
		claim.Binding.NormalizedInputRef == command.Input.Ref && claim.Binding.RequestHash == command.RequestHash && claim.Binding.EffectKey == command.EffectKey &&
		claim.Binding.EffectScope == command.EffectScope && claim.Binding.ProviderID == command.ProviderID
}

func runtimeAlreadyTerminated(err error) bool {
	var failure runtimeclient.HTTPError
	return errors.As(err, &failure) && failure.StatusCode == http.StatusConflict
}

func (executor SandboxExecutor) cleanupTimeout() time.Duration {
	if executor.CleanupTimeout >= time.Second && executor.CleanupTimeout <= time.Minute {
		return executor.CleanupTimeout
	}
	return 30 * time.Second
}

func (executor SandboxExecutor) now() time.Time {
	if executor.Now != nil {
		return executor.Now().UTC()
	}
	return time.Now().UTC()
}
