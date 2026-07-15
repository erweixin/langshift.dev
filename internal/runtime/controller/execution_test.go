package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/runtime/firecracker"
	"github.com/langshift/lites/internal/runtime/guest"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
)

type executionStoreStub struct {
	*storeStub
	mu       sync.Mutex
	terminal *runtimepostgres.RuntimeExecution
	begins   int
	marks    int
}

func (store *executionStoreStub) BeginExecution(_ context.Context, command runtimepostgres.BeginExecutionCommand) (runtimepostgres.RuntimeExecution, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.begins++
	if store.terminal != nil {
		result := *store.terminal
		result.Replayed = true
		return result, nil
	}
	return runtimepostgres.RuntimeExecution{
		ID: "81000000-0000-4000-8000-000000000001", TenantID: command.TenantID, SessionID: command.SessionID,
		RequestID: command.RequestID, RequestHash: command.RequestHash, Status: "running", Version: 1,
		StartedSessionVersion: command.ExpectedVersion + 1, StartedEventID: "execution-started-event",
		Lifecycle: runtimepostgres.LifecycleResult{SessionID: command.SessionID, Status: "running", Version: command.ExpectedVersion + 1},
	}, nil
}

func (store *executionStoreStub) CompleteExecution(_ context.Context, command runtimepostgres.FinishExecutionCommand) (runtimepostgres.RuntimeExecution, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := runtimepostgres.RuntimeExecution{
		ID: "81000000-0000-4000-8000-000000000001", TenantID: command.TenantID, SessionID: command.SessionID,
		RequestID: command.RequestID, RequestHash: command.RequestHash, Status: "completed", Version: 2,
		StartedSessionVersion: command.ExpectedVersion, FinishedSessionVersion: command.ExpectedVersion + 1,
		Outcome: command.Payload, OutcomeHash: command.OutcomeHash, OutcomeManifest: command.OutcomeManifest,
		Lifecycle: runtimepostgres.LifecycleResult{SessionID: command.SessionID, Status: "idle", Version: command.ExpectedVersion + 1},
	}
	store.terminal = &result
	return result, nil
}

func (store *executionStoreStub) MarkExecutionOutcomeUnknown(_ context.Context, command runtimepostgres.FinishExecutionCommand) (runtimepostgres.RuntimeExecution, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.marks++
	result := runtimepostgres.RuntimeExecution{
		ID: "81000000-0000-4000-8000-000000000001", TenantID: command.TenantID, SessionID: command.SessionID,
		RequestID: command.RequestID, RequestHash: command.RequestHash, Status: "outcome_unknown", Version: 2,
		StartedSessionVersion: command.ExpectedVersion, FinishedSessionVersion: command.ExpectedVersion + 1,
		Outcome: command.Payload, OutcomeHash: command.OutcomeHash, OutcomeManifest: command.OutcomeManifest, FailureCode: command.FailureCode,
		Lifecycle: runtimepostgres.LifecycleResult{SessionID: command.SessionID, Status: "termination_requested", Version: command.ExpectedVersion + 1},
	}
	store.terminal = &result
	return result, nil
}

type executionPayloadStore struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (store *executionPayloadStore) Put(_ context.Context, descriptor payload.Descriptor, value []byte) (payload.Manifest, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.values == nil {
		store.values = make(map[string][]byte)
	}
	digest := sha256.Sum256(value)
	ref := "memory://" + descriptor.Class + "/" + descriptor.ObjectID + "/" + hex.EncodeToString(digest[:])
	store.values[ref] = append([]byte(nil), value...)
	return payload.Manifest{Ref: ref, Hash: hex.EncodeToString(digest[:])}, nil
}

func (store *executionPayloadStore) Get(_ context.Context, _ payload.Descriptor, manifest payload.Manifest) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.values[manifest.Ref]
	if !ok {
		return nil, errors.New("payload missing")
	}
	return append([]byte(nil), value...), nil
}

func TestExecutePersistsKnownResultAndReplaysWithoutGuest(t *testing.T) {
	now := time.Now().UTC()
	store := &executionStoreStub{storeStub: &storeStub{}}
	payloads := &executionPayloadStore{}
	guestCalls := 0
	controller, _, _ := executionControllerFixture(now, store, payloads)
	controller.execute = func(_ context.Context, _ string, requestID string, _ guest.ExecuteRequest, sink guest.FrameSink) (guest.ExecutionResult, error) {
		guestCalls++
		if err := sink(guest.Frame{Kind: guest.FrameStdout, RequestID: requestID, Chunk: []byte(`{"ok":true}`)}); err != nil {
			return guest.ExecutionResult{}, err
		}
		return guest.ExecutionResult{ExitCode: 0, StdoutBytes: 11, StartedUnixMillis: now.UnixMilli(), FinishedUnixMillis: now.Add(time.Millisecond).UnixMilli()}, nil
	}
	request := validControllerExecuteRequest(now)
	first, err := controller.Execute(t.Context(), request)
	if err != nil || first.Execution.Status != "completed" || string(first.Stdout) != `{"ok":true}` || guestCalls != 1 {
		t.Fatalf("first Execute()=%#v calls=%d err=%v", first, guestCalls, err)
	}
	second, err := controller.Execute(t.Context(), request)
	if err != nil || !second.Execution.Replayed || string(second.Stdout) != string(first.Stdout) || guestCalls != 1 || store.begins != 2 {
		t.Fatalf("replayed Execute()=%#v calls=%d begins=%d err=%v", second, guestCalls, store.begins, err)
	}
}

func TestExecuteTransportUnknownSettlesLedgerAndDestroysMachine(t *testing.T) {
	now := time.Now().UTC()
	store := &executionStoreStub{storeStub: &storeStub{}}
	controller, machine, owners := executionControllerFixture(now, store, &executionPayloadStore{})
	controller.execute = func(context.Context, string, string, guest.ExecuteRequest, guest.FrameSink) (guest.ExecutionResult, error) {
		return guest.ExecutionResult{}, errors.New("vsock reset")
	}
	_, err := controller.Execute(t.Context(), validControllerExecuteRequest(now))
	if !errors.Is(err, ErrOutcomeUnknown) || store.marks != 1 || !machine.stopped || controller.lookup("80000000-0000-4000-8000-000000000001") != nil {
		t.Fatalf("Execute() err=%v marks=%d stopped=%v", err, store.marks, machine.stopped)
	}
	if _, exists := owners.records["runtime-machine-1"]; exists || store.requested != 0 || store.completed != 1 {
		t.Fatalf("machine cleanup incomplete owner=%v requested=%d completed=%d", exists, store.requested, store.completed)
	}
}

func TestTerminationTimeoutReturnsMachineToRecoveryUntilExecutionSettles(t *testing.T) {
	now := time.Now().UTC()
	store := &executionStoreStub{storeStub: &storeStub{}}
	controller, machine, _ := executionControllerFixture(now, store, &executionPayloadStore{})
	started, release := make(chan struct{}), make(chan struct{})
	controller.execute = func(context.Context, string, string, guest.ExecuteRequest, guest.FrameSink) (guest.ExecutionResult, error) {
		close(started)
		<-release
		return guest.ExecutionResult{}, errors.New("transport closed")
	}
	executionDone := make(chan error, 1)
	go func() {
		_, err := controller.Execute(t.Context(), validControllerExecuteRequest(now))
		executionDone <- err
	}()
	<-started
	terminationCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := controller.Terminate(terminationCtx, "80000000-0000-4000-8000-000000000001", "run_cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Terminate() error=%v", err)
	}
	if controller.lookup("80000000-0000-4000-8000-000000000001") == nil {
		t.Fatal("termination timeout orphaned the active ownership record")
	}
	close(release)
	if err := <-executionDone; !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Execute() error=%v", err)
	}
	if !machine.stopped || controller.lookup("80000000-0000-4000-8000-000000000001") != nil {
		t.Fatal("execution settlement did not finish deferred termination")
	}
}

func executionControllerFixture(now time.Time, store *executionStoreStub, payloads payload.Store) (*Controller, *machineStub, *ownershipStub) {
	machine := &machineStub{done: make(chan struct{})}
	owners := newOwnershipStub()
	owner := firecracker.OwnershipRecord{SchemaVersion: 2, Phase: firecracker.OwnershipPhaseActive, HostID: "runtime-host-1", TenantID: "20000000-0000-4000-8000-000000000001", SessionID: "80000000-0000-4000-8000-000000000001", AllocationID: "80000000-0000-4000-8000-000000000002", ProvisionAttemptID: "60000000-0000-4000-8000-000000000001", MachineID: "runtime-machine-1", GuestCID: 42, Root: "/jail/runtime-machine-1/root"}
	owners.records[owner.MachineID] = owner
	active := &activeMachine{
		process: machine, staged: stagedMachine{vsockPath: "/jail/runtime-machine-1/root/run/guest.vsock", cleanup: func() error { return owners.Cleanup(owner) }},
		request: ProvisionRequest{Command: runtimepostgres.ProvisionCommand{TenantID: owner.TenantID, HostID: owner.HostID, MachineID: owner.MachineID, GuestCID: owner.GuestCID, ProvisionLease: "provision-lease", Actor: []byte(`{"kind":"system"}`), CorrelationID: owner.SessionID}},
		result:  runtimepostgres.ProvisionResult{TenantID: owner.TenantID, SessionID: owner.SessionID, AllocationID: owner.AllocationID, ProvisionAttemptID: owner.ProvisionAttemptID, MachineID: owner.MachineID, GuestCID: owner.GuestCID},
		version: 3, owner: owner,
	}
	controller := &Controller{Store: store, Payloads: payloads, Ownership: owners, MaximumExecutionDuration: time.Hour, Now: func() time.Time { return now }, active: map[string]*activeMachine{owner.SessionID: active}, killCgroup: func(context.Context, string) error { return nil }}
	return controller, machine, owners
}

func validControllerExecuteRequest(now time.Time) ExecuteRequest {
	return ExecuteRequest{
		TenantID: "20000000-0000-4000-8000-000000000001", SessionID: "80000000-0000-4000-8000-000000000001",
		CapabilityToken: "signed-capability", ProvisionLease: "provision-lease", RequestID: "runtime-request-0001",
		Command: guest.ExecuteRequest{Argv: []string{"/usr/bin/tool"}, WorkingDirectory: "/workspace", DeadlineUnixMillis: now.Add(time.Minute).UnixMilli(), MaximumOutputBytes: 1 << 20},
	}
}
