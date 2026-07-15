package toolworker

import (
	"context"
	"errors"
	"testing"
	"time"

	runtimeclient "github.com/langshift/lites/internal/runtime/client"
	"github.com/langshift/lites/internal/runtime/controller"
	"github.com/langshift/lites/internal/runtime/guest"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
)

type sandboxBrokerStub struct {
	session SandboxSession
	request SandboxAcquireRequest
}

func (broker *sandboxBrokerStub) Acquire(_ context.Context, request SandboxAcquireRequest) (SandboxSession, error) {
	broker.request = request
	return broker.session, nil
}

type sandboxHostStub struct {
	executed     runtimeclient.ExecuteRequest
	result       controller.Executed
	executeErr   error
	terminations int
}

func (host *sandboxHostStub) Execute(_ context.Context, request runtimeclient.ExecuteRequest) (controller.Executed, error) {
	host.executed = request
	return host.result, host.executeErr
}

func (host *sandboxHostStub) Terminate(_ context.Context, sessionID, _ string) (runtimepostgres.LifecycleResult, error) {
	host.terminations++
	return runtimepostgres.LifecycleResult{SessionID: sessionID, Status: "terminated"}, nil
}

func TestSandboxExecutorRunsPinnedGuestProtocolAndValidatesOutput(t *testing.T) {
	registry, snapshot := workerRegistryWithTrust(t, "read_only", "30s", "semi_trusted")
	execution := registryExecution(t, registry, snapshot)
	execution.Claim.LeaseExpiresAt = time.Now().Add(time.Minute)
	runnerOutput := []byte(`{"schema_version":1,"status":"succeeded","output":{"ok":true},"failure":{"retryable":false}}`)
	host := &sandboxHostStub{result: controller.Executed{Result: guest.ExecutionResult{ExitCode: 0, StdoutBytes: int64(len(runnerOutput)), StartedUnixMillis: 1, FinishedUnixMillis: 2}, Stdout: runnerOutput}}
	broker := &sandboxBrokerStub{session: SandboxSession{TenantID: execution.Claim.TenantID, SessionID: "runtime-session-1", CapabilityToken: "signed-capability", ProvisionLease: "provision-lease", Host: host}}
	executor := SandboxExecutor{Registry: &registry, Broker: broker, DefaultReconcileAfter: time.Minute, CleanupTimeout: time.Second}
	outcome, err := executor.Execute(t.Context(), execution)
	if err != nil || outcome.State != "succeeded" || string(outcome.Result) != `{"ok":true}` {
		t.Fatalf("Execute()=%#v err=%v", outcome, err)
	}
	if broker.request.Command.Argv[0] != sandboxToolRunner || string(broker.request.Command.Stdin) != string(execution.Input) || host.executed.RequestID != "sandbox:"+execution.Claim.AttemptID || host.terminations != 1 {
		t.Fatalf("broker=%#v host=%#v", broker.request, host.executed)
	}
}

func TestSandboxExecutorFailsWriteClosedOnRuntimeOutcomeUnknown(t *testing.T) {
	registry, snapshot := workerRegistryWithTrust(t, "idempotent_write", "30s", "untrusted")
	execution := registryExecution(t, registry, snapshot)
	execution.Claim.LeaseExpiresAt = time.Now().Add(time.Minute)
	host := &sandboxHostStub{executeErr: errors.New("runtime response lost")}
	broker := &sandboxBrokerStub{session: SandboxSession{TenantID: execution.Claim.TenantID, SessionID: "runtime-session-1", CapabilityToken: "signed-capability", ProvisionLease: "provision-lease", Host: host}}
	outcome, err := (SandboxExecutor{Registry: &registry, Broker: broker, DefaultReconcileAfter: time.Minute}).Execute(t.Context(), execution)
	if err != nil || outcome.State != "outcome_unknown" || outcome.EffectDisposition != EffectUnknown || outcome.ReconcileAfter != 17*time.Second || host.terminations != 1 {
		t.Fatalf("Execute()=%#v terminations=%d err=%v", outcome, host.terminations, err)
	}
}

func TestSandboxExecutorRejectsMalformedReadResult(t *testing.T) {
	registry, snapshot := workerRegistryWithTrust(t, "read_only", "30s", "semi_trusted")
	execution := registryExecution(t, registry, snapshot)
	execution.Claim.LeaseExpiresAt = time.Now().Add(time.Minute)
	host := &sandboxHostStub{result: controller.Executed{Result: guest.ExecutionResult{ExitCode: 0, StdoutBytes: 2, StartedUnixMillis: 1, FinishedUnixMillis: 2}, Stdout: []byte(`{}`)}}
	broker := &sandboxBrokerStub{session: SandboxSession{TenantID: execution.Claim.TenantID, SessionID: "runtime-session-1", CapabilityToken: "signed-capability", ProvisionLease: "provision-lease", Host: host}}
	_, err := (SandboxExecutor{Registry: &registry, Broker: broker, DefaultReconcileAfter: time.Minute}).Execute(t.Context(), execution)
	if !errors.Is(err, ErrSandboxProtocol) {
		t.Fatalf("malformed result error=%v", err)
	}
}
