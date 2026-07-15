package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/runtime/firecracker"
	"github.com/langshift/lites/internal/runtime/guest"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
)

type storeStub struct {
	mu                          sync.Mutex
	ready, requested, completed int
	lastReady                   runtimepostgres.ReadyCommand
}

func (store *storeStub) BeginProvision(context.Context, runtimepostgres.ProvisionCommand) (runtimepostgres.ProvisionResult, error) {
	return runtimepostgres.ProvisionResult{
		TenantID: "20000000-0000-0000-0000-000000000001", SessionID: "80000000-0000-0000-0000-000000000001", AllocationID: "80000000-0000-0000-0000-000000000002",
		ProvisionAttemptID: "60000000-0000-0000-0000-000000000001", MachineID: "runtime-machine-1", GuestCID: 42, Version: 2,
		Policy: runtimepostgres.RuntimePolicy{PolicyHash: string(bytes.Repeat([]byte{'a'}, 64)), KernelDigest: "sha256:" + string(bytes.Repeat([]byte{'b'}, 64)), RootFSDigest: "sha256:" + string(bytes.Repeat([]byte{'c'}, 64)), VCPUCount: 2, MemoryMiB: 512, DiskMiB: 4096},
	}, nil
}
func (store *storeStub) MarkReady(_ context.Context, command runtimepostgres.ReadyCommand) (runtimepostgres.LifecycleResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.ready++
	store.lastReady = command
	return runtimepostgres.LifecycleResult{SessionID: command.SessionID, Status: "ready", Version: 3, EventID: "ready-event"}, nil
}
func (store *storeStub) BeginExecution(_ context.Context, command runtimepostgres.BeginExecutionCommand) (runtimepostgres.RuntimeExecution, error) {
	return runtimepostgres.RuntimeExecution{
		ID: "81000000-0000-4000-8000-000000000001", TenantID: command.TenantID, SessionID: command.SessionID,
		RequestID: command.RequestID, RequestHash: command.RequestHash, Status: "running", Version: 1,
		StartedSessionVersion: command.ExpectedVersion + 1, StartedEventID: "execution-started-event",
		Lifecycle: runtimepostgres.LifecycleResult{SessionID: command.SessionID, Status: "running", Version: command.ExpectedVersion + 1},
	}, nil
}
func (store *storeStub) CompleteExecution(_ context.Context, command runtimepostgres.FinishExecutionCommand) (runtimepostgres.RuntimeExecution, error) {
	return runtimepostgres.RuntimeExecution{
		ID: "81000000-0000-4000-8000-000000000001", TenantID: command.TenantID, SessionID: command.SessionID,
		RequestID: command.RequestID, RequestHash: command.RequestHash, Status: "completed", Version: 2,
		StartedSessionVersion: command.ExpectedVersion, FinishedSessionVersion: command.ExpectedVersion + 1,
		Outcome: command.Payload, OutcomeHash: command.OutcomeHash, OutcomeManifest: command.OutcomeManifest,
		Lifecycle: runtimepostgres.LifecycleResult{SessionID: command.SessionID, Status: "idle", Version: command.ExpectedVersion + 1},
	}, nil
}
func (store *storeStub) MarkExecutionOutcomeUnknown(_ context.Context, command runtimepostgres.FinishExecutionCommand) (runtimepostgres.RuntimeExecution, error) {
	return runtimepostgres.RuntimeExecution{
		ID: "81000000-0000-4000-8000-000000000001", TenantID: command.TenantID, SessionID: command.SessionID,
		RequestID: command.RequestID, RequestHash: command.RequestHash, Status: "outcome_unknown", Version: 2,
		StartedSessionVersion: command.ExpectedVersion, FinishedSessionVersion: command.ExpectedVersion + 1,
		Outcome: command.Payload, OutcomeHash: command.OutcomeHash, OutcomeManifest: command.OutcomeManifest, FailureCode: command.FailureCode,
		Lifecycle: runtimepostgres.LifecycleResult{SessionID: command.SessionID, Status: "termination_requested", Version: command.ExpectedVersion + 1},
	}, nil
}
func (store *storeStub) RequestTermination(_ context.Context, command runtimepostgres.TerminationCommand) (runtimepostgres.LifecycleResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.requested++
	return runtimepostgres.LifecycleResult{SessionID: command.SessionID, Status: "termination_requested", Version: command.ExpectedVersion + 1}, nil
}
func (store *storeStub) CompleteTermination(_ context.Context, command runtimepostgres.TerminatedCommand) (runtimepostgres.LifecycleResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.completed++
	var usage map[string]any
	if json.Unmarshal(command.UsageManifest, &usage) != nil || usage["schema_version"] != float64(1) {
		return runtimepostgres.LifecycleResult{}, errors.New("invalid usage")
	}
	return runtimepostgres.LifecycleResult{SessionID: command.SessionID, Status: "terminated", Version: command.ExpectedVersion + 1}, nil
}

type payloadStub struct{}

func (payloadStub) Put(_ context.Context, descriptor payload.Descriptor, value []byte) (payload.Manifest, error) {
	if descriptor.TenantID == "" || len(value) == 0 {
		return payload.Manifest{}, errors.New("invalid payload")
	}
	return payload.Manifest{Ref: "encrypted://runtime/" + descriptor.ObjectID, Hash: "encrypted-envelope-hash"}, nil
}
func (payloadStub) Get(context.Context, payload.Descriptor, payload.Manifest) ([]byte, error) {
	return nil, errors.New("unused")
}

type machineStub struct {
	stopped bool
	done    chan struct{}
}

func (machine *machineStub) Stop(context.Context) error {
	if !machine.stopped {
		machine.stopped = true
		select {
		case <-machine.done:
		default:
			close(machine.done)
		}
	}
	return nil
}
func (machine *machineStub) Done() <-chan struct{} { return machine.done }
func (machine *machineStub) ExitError() error      { return nil }
func (machine *machineStub) Identity() (firecracker.ProcessIdentity, error) {
	return firecracker.ProcessIdentity{PID: 1234, StartTicks: 98765, BootID: "00000000-0000-4000-8000-000000000001", Executable: "/usr/bin/firecracker"}, nil
}

type ownershipStub struct {
	mu                   sync.Mutex
	records              map[string]firecracker.OwnershipRecord
	cleaned              map[string]bool
	cleanups             int
	claims, activations  int
	createErr, verifyErr error
}

func newOwnershipStub() *ownershipStub {
	return &ownershipStub{records: make(map[string]firecracker.OwnershipRecord), cleaned: make(map[string]bool)}
}
func (store *ownershipStub) Create(record firecracker.OwnershipRecord) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.createErr != nil {
		return store.createErr
	}
	if _, exists := store.records[record.MachineID]; exists {
		return firecracker.ErrOwnershipConflict
	}
	store.records[record.MachineID] = record
	return nil
}
func (store *ownershipStub) ClaimVacant(record firecracker.OwnershipRecord) error {
	if err := store.Create(record); err != nil {
		return err
	}
	store.mu.Lock()
	store.claims++
	store.mu.Unlock()
	return nil
}
func (store *ownershipStub) Activate(record firecracker.OwnershipRecord, identity firecracker.ProcessIdentity) (firecracker.OwnershipRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, exists := store.records[record.MachineID]
	if !exists || current.SessionID != record.SessionID {
		return firecracker.OwnershipRecord{}, firecracker.ErrOwnershipIntegrity
	}
	record.Phase = firecracker.OwnershipPhaseActive
	record.Process = identity
	store.records[record.MachineID] = record
	store.activations++
	return record, nil
}
func (store *ownershipStub) ExpectedRoot(machineID string) (string, error) {
	if machineID == "" {
		return "", firecracker.ErrOwnershipIntegrity
	}
	return "/jail/" + machineID + "/root", nil
}
func (store *ownershipStub) Verify(record firecracker.OwnershipRecord) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.verifyErr != nil {
		return store.verifyErr
	}
	current, exists := store.records[record.MachineID]
	if !exists || current.SessionID != record.SessionID {
		return firecracker.ErrOwnershipIntegrity
	}
	return nil
}
func (store *ownershipStub) Scan() ([]firecracker.OwnershipRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]firecracker.OwnershipRecord, 0, len(store.records))
	for _, record := range store.records {
		result = append(result, record)
	}
	return result, nil
}
func (store *ownershipStub) Cleanup(record firecracker.OwnershipRecord) error {
	if err := store.Verify(record); err != nil {
		return err
	}
	store.mu.Lock()
	store.cleaned[record.MachineID] = true
	store.cleanups++
	store.mu.Unlock()
	return nil
}
func (store *ownershipStub) CleanupPrepared(record firecracker.OwnershipRecord) (bool, error) {
	if err := store.Verify(record); err != nil {
		return false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.cleaned[record.MachineID], nil
}
func (store *ownershipStub) Remove(record firecracker.OwnershipRecord) error {
	if err := store.Verify(record); err != nil {
		return err
	}
	store.mu.Lock()
	delete(store.records, record.MachineID)
	delete(store.cleaned, record.MachineID)
	store.mu.Unlock()
	return nil
}

type recoveryStoreStub struct {
	mu                   sync.Mutex
	states               map[string]runtimepostgres.RecoveryState
	requested, completed int
	completeErrors       []error
}

func (store *recoveryStoreStub) InspectOwnedMachine(_ context.Context, identity runtimepostgres.RecoveryIdentity, _ []byte) (runtimepostgres.RecoveryState, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	state, ok := store.states[identity.SessionID]
	if !ok || state.RecoveryIdentity != identity {
		return runtimepostgres.RecoveryState{}, runtimepostgres.ErrSessionConflict
	}
	return state, nil
}
func (store *recoveryStoreStub) ListHostMachines(_ context.Context, hostID string, _ []byte, after string, limit int) ([]runtimepostgres.RecoveryState, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]runtimepostgres.RecoveryState, 0, len(store.states))
	for _, state := range store.states {
		if state.HostID == hostID && state.SessionID > after && state.SessionStatus != "terminated" && state.SessionStatus != "failed" {
			result = append(result, state)
		}
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
func (store *recoveryStoreStub) RequestRecoveryTermination(_ context.Context, command runtimepostgres.RecoveryTerminationCommand) (runtimepostgres.LifecycleResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.requested++
	state := store.states[command.SessionID]
	state.SessionVersion = command.ExpectedVersion + 1
	state.SessionStatus = "termination_requested"
	state.AllocationVersion++
	state.AllocationStatus = "releasing"
	store.states[command.SessionID] = state
	return runtimepostgres.LifecycleResult{SessionID: command.SessionID, Status: "termination_requested", Version: state.SessionVersion}, nil
}
func (store *recoveryStoreStub) CompleteRecoveryTermination(_ context.Context, command runtimepostgres.RecoveryTerminatedCommand) (runtimepostgres.LifecycleResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.completed++
	if len(store.completeErrors) > 0 {
		err := store.completeErrors[0]
		store.completeErrors = store.completeErrors[1:]
		if err != nil {
			return runtimepostgres.LifecycleResult{}, err
		}
	}
	state := store.states[command.SessionID]
	state.SessionVersion = command.ExpectedVersion + 1
	state.SessionStatus = "terminated"
	state.AllocationVersion++
	state.AllocationStatus = "released"
	store.states[command.SessionID] = state
	return runtimepostgres.LifecycleResult{SessionID: command.SessionID, Status: "terminated", Version: state.SessionVersion}, nil
}

func TestControllerRetriesDurableCompletionFromArchivedCleanupEvidence(t *testing.T) {
	record := recoveryRecord("80000000-0000-4000-8000-000000000031", "80000000-0000-4000-8000-000000000032", "runtime-machine-4", 4234)
	identity := recoveryIdentity(record)
	recovery := &recoveryStoreStub{
		states: map[string]runtimepostgres.RecoveryState{record.SessionID: {
			RecoveryIdentity: identity, SessionVersion: 4, AllocationVersion: 3, ProvisionFence: 1,
			SessionStatus: "termination_requested", AllocationStatus: "releasing",
		}},
		completeErrors: []error{errors.New("database completion outcome unknown")},
	}
	owners := newOwnershipStub()
	owners.records[record.MachineID] = record
	owners.cleaned[record.MachineID] = true
	controller := &Controller{Store: &storeStub{}, Recovery: recovery, Payloads: payloadStub{}, Ownership: owners, Now: func() time.Time {
		return time.Date(2026, 7, 15, 17, 30, 0, 0, time.UTC)
	}}
	request := ReconcileRequest{HostID: record.HostID, HostControlHash: bytes.Repeat([]byte{0x55}, 32)}
	if completed, err := controller.ReconcileTerminations(context.Background(), request); err == nil || completed != 0 {
		t.Fatalf("first ReconcileTerminations()=%d err=%v", completed, err)
	}
	if _, exists := owners.records[record.MachineID]; !exists || !owners.cleaned[record.MachineID] {
		t.Fatal("cleanup evidence was discarded after unknown database outcome")
	}
	if completed, err := controller.ReconcileTerminations(context.Background(), request); err != nil || completed != 1 {
		t.Fatalf("retry ReconcileTerminations()=%d err=%v", completed, err)
	}
	if _, exists := owners.records[record.MachineID]; exists || recovery.states[record.SessionID].SessionStatus != "terminated" {
		t.Fatal("cleanup evidence did not converge with durable termination")
	}
}

func TestControllerFailsClosedBeforeHostMutationWhenOwnershipCannotBePersisted(t *testing.T) {
	store := &storeStub{}
	machine := &machineStub{done: make(chan struct{})}
	now := time.Date(2026, 7, 15, 16, 15, 0, 0, time.UTC)
	ownershipFailure := errors.New("ownership fsync failed")
	owners := newOwnershipStub()
	owners.createErr = ownershipFailure
	controller := &Controller{Store: store, Payloads: payloadStub{}, Ownership: owners, Random: bytes.NewReader(bytes.Repeat([]byte{0x45}, 32)), Now: func() time.Time { return now }}
	controller.stage = func(context.Context, firecracker.StageRequest) (stagedMachine, error) {
		return stagedMachine{spec: firecracker.Spec{}, root: "/jail/runtime-machine-1/root", vsockPath: "/jail/run/guest.vsock", cleanup: func() error { return nil }}, nil
	}
	controller.start = func(context.Context, firecracker.Spec) (machineProcess, error) { return machine, nil }
	controller.probe = func(context.Context, string, string, string) (guest.Attestation, error) {
		t.Fatal("guest must not be probed without durable host ownership")
		return guest.Attestation{}, nil
	}
	if _, err := controller.Provision(context.Background(), validProvisionRequest(now)); !errors.Is(err, ownershipFailure) || machine.stopped || store.ready != 0 || store.requested != 1 || store.completed != 1 {
		t.Fatalf("Provision() error=%v stopped=%v calls=%d/%d/%d", err, machine.stopped, store.ready, store.requested, store.completed)
	}
}

func TestControllerKeepsCapacityReleasingWhenOwnershipEvidenceIsInvalid(t *testing.T) {
	store := &storeStub{}
	machine := &machineStub{done: make(chan struct{})}
	now := time.Date(2026, 7, 15, 16, 20, 0, 0, time.UTC)
	owners := newOwnershipStub()
	controller := &Controller{Store: store, Payloads: payloadStub{}, Ownership: owners, Random: bytes.NewReader(bytes.Repeat([]byte{0x46}, 32)), Now: func() time.Time { return now }}
	controller.stage = func(context.Context, firecracker.StageRequest) (stagedMachine, error) {
		return stagedMachine{spec: firecracker.Spec{}, root: "/jail/runtime-machine-1/root", vsockPath: "/jail/run/guest.vsock", cleanup: func() error { t.Fatal("untrusted ownership directory was cleaned"); return nil }}, nil
	}
	controller.start = func(context.Context, firecracker.Spec) (machineProcess, error) { return machine, nil }
	controller.probe = func(_ context.Context, _ string, _ string, challenge string) (guest.Attestation, error) {
		return guest.Attestation{Challenge: challenge, GuestAgentBuild: "lites-runtime-guest-agent.v1", UserID: 1000, GroupID: 1000, BootUnixMillis: now.UnixMilli()}, nil
	}
	provisioned, err := controller.Provision(context.Background(), validProvisionRequest(now))
	if err != nil {
		t.Fatal(err)
	}
	owners.verifyErr = firecracker.ErrOwnershipIntegrity
	now = now.Add(time.Second)
	result, err := controller.Terminate(context.Background(), provisioned.Provision.SessionID, "completed")
	if !errors.Is(err, firecracker.ErrOwnershipIntegrity) || result.Status != "termination_requested" || store.requested != 1 || store.completed != 0 {
		t.Fatalf("Terminate()=%#v err=%v calls=%d/%d", result, err, store.requested, store.completed)
	}
}

func TestControllerRecoversReadyMachineAndTerminatesInterruptedProvision(t *testing.T) {
	now := time.Date(2026, 7, 15, 16, 25, 0, 0, time.UTC)
	readyRecord := recoveryRecord("80000000-0000-4000-8000-000000000001", "80000000-0000-4000-8000-000000000002", "runtime-machine-1", 1234)
	provisioningRecord := recoveryRecord("80000000-0000-4000-8000-000000000011", "80000000-0000-4000-8000-000000000012", "runtime-machine-2", 2234)
	orphanRecord := recoveryRecord("80000000-0000-4000-8000-000000000021", "80000000-0000-4000-8000-000000000022", "runtime-machine-3", 3234)
	owners := newOwnershipStub()
	owners.records[readyRecord.MachineID] = readyRecord
	owners.records[provisioningRecord.MachineID] = provisioningRecord
	owners.records[orphanRecord.MachineID] = orphanRecord
	recovery := &recoveryStoreStub{states: map[string]runtimepostgres.RecoveryState{
		readyRecord.SessionID: {
			RecoveryIdentity: runtimepostgres.RecoveryIdentity{TenantID: readyRecord.TenantID, SessionID: readyRecord.SessionID, AllocationID: readyRecord.AllocationID, ProvisionAttemptID: readyRecord.ProvisionAttemptID, HostID: readyRecord.HostID, MachineID: readyRecord.MachineID, GuestCID: readyRecord.GuestCID},
			SessionVersion:   3, AllocationVersion: 2, ProvisionFence: 1, SessionStatus: "ready", AllocationStatus: "active",
		},
		provisioningRecord.SessionID: {
			RecoveryIdentity: runtimepostgres.RecoveryIdentity{TenantID: provisioningRecord.TenantID, SessionID: provisioningRecord.SessionID, AllocationID: provisioningRecord.AllocationID, ProvisionAttemptID: provisioningRecord.ProvisionAttemptID, HostID: provisioningRecord.HostID, MachineID: provisioningRecord.MachineID, GuestCID: provisioningRecord.GuestCID},
			SessionVersion:   2, AllocationVersion: 1, ProvisionFence: 1, SessionStatus: "provisioning", AllocationStatus: "provisioning",
		},
	}}
	readyProcess := &machineStub{done: make(chan struct{})}
	controller := &Controller{Store: &storeStub{}, Recovery: recovery, Payloads: payloadStub{}, Ownership: owners, Now: func() time.Time { return now }}
	controller.processAlive = func(identity firecracker.ProcessIdentity, _ []string) (bool, error) { return identity.PID == 1234, nil }
	controller.adopt = func(identity firecracker.ProcessIdentity, _ []string, _ time.Duration) (machineProcess, error) {
		if identity.PID != 1234 {
			t.Fatalf("unexpected PID adopted: %d", identity.PID)
		}
		return readyProcess, nil
	}
	result, err := controller.Recover(context.Background(), RecoverRequest{HostID: "runtime-host-1", HostControlHash: bytes.Repeat([]byte{0x51}, 32), AllowedExecutables: []string{"/usr/bin/firecracker", "/usr/bin/jailer"}, StopGrace: time.Second})
	if err != nil || result.Adopted != 1 || result.Terminated != 1 || result.Cleaned != 1 || recovery.requested != 1 || recovery.completed != 1 {
		t.Fatalf("Recover()=%#v err=%v calls=%d/%d", result, err, recovery.requested, recovery.completed)
	}
	if _, exists := owners.records[provisioningRecord.MachineID]; exists {
		t.Fatal("interrupted provisioning ownership survived cleanup")
	}
	now = now.Add(time.Second)
	recovery.mu.Lock()
	deadlineState := recovery.states[readyRecord.SessionID]
	deadlineState.SessionVersion++
	deadlineState.AllocationVersion++
	deadlineState.SessionStatus = "termination_requested"
	deadlineState.AllocationStatus = "releasing"
	recovery.states[readyRecord.SessionID] = deadlineState
	recovery.mu.Unlock()
	completed, err := controller.ReconcileTerminations(context.Background(), ReconcileRequest{HostID: "runtime-host-1", HostControlHash: bytes.Repeat([]byte{0x51}, 32)})
	if err != nil || completed != 1 || !readyProcess.stopped || recovery.requested != 1 || recovery.completed != 2 {
		t.Fatalf("ReconcileTerminations()=%d err=%v stopped=%v calls=%d/%d", completed, err, readyProcess.stopped, recovery.requested, recovery.completed)
	}
}

func recoveryRecord(sessionID, allocationID, machineID string, pid int) firecracker.OwnershipRecord {
	return firecracker.OwnershipRecord{
		SchemaVersion: 1, HostID: "runtime-host-1", TenantID: "20000000-0000-4000-8000-000000000001",
		SessionID: sessionID, AllocationID: allocationID, ProvisionAttemptID: "60000000-0000-4000-8000-000000000001",
		MachineID: machineID, GuestCID: 42, Root: "/jail/" + machineID + "/root",
		Process:   firecracker.ProcessIdentity{PID: pid, StartTicks: uint64(pid), BootID: "00000000-0000-4000-8000-000000000001", Executable: "/usr/bin/firecracker"},
		CreatedAt: time.Date(2026, 7, 15, 16, 0, 0, 0, time.UTC),
	}
}

func TestControllerAttestsPersistsReadyAndTerminatesOwnedMachine(t *testing.T) {
	store := &storeStub{}
	machine := &machineStub{done: make(chan struct{})}
	now := time.Date(2026, 7, 15, 16, 0, 0, 0, time.UTC)
	owners := newOwnershipStub()
	controller := &Controller{Store: store, Payloads: payloadStub{}, Ownership: owners, Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)), Now: func() time.Time { return now }}
	controller.stage = func(context.Context, firecracker.StageRequest) (stagedMachine, error) {
		return stagedMachine{spec: firecracker.Spec{}, root: "/jail/runtime-machine-1/root", vsockPath: "/jail/run/guest.vsock", cleanup: func() error { return nil }}, nil
	}
	controller.start = func(context.Context, firecracker.Spec) (machineProcess, error) { return machine, nil }
	controller.probe = func(_ context.Context, _ string, _ string, challenge string) (guest.Attestation, error) {
		return guest.Attestation{Challenge: challenge, GuestAgentBuild: "lites-runtime-guest-agent.v1", UserID: 1000, GroupID: 1000, BootUnixMillis: now.UnixMilli()}, nil
	}
	request := validProvisionRequest(now)
	provisioned, err := controller.Provision(context.Background(), request)
	if err != nil || provisioned.Ready.Version != 3 || store.ready != 1 || store.lastReady.BootReceiptHash == store.lastReady.Payload.Hash || len(store.lastReady.BootReceiptHash) != 64 {
		t.Fatalf("Provision() = %#v, %v, ready=%#v", provisioned, err, store.lastReady)
	}
	now = now.Add(time.Second)
	terminated, err := controller.Terminate(context.Background(), provisioned.Provision.SessionID, "completed")
	if err != nil || terminated.Status != "terminated" || !machine.stopped || owners.cleanups != 1 || store.requested != 1 || store.completed != 1 {
		t.Fatalf("Terminate() = %#v, %v stopped=%v cleaned=%d calls=%d/%d", terminated, err, machine.stopped, owners.cleanups, store.requested, store.completed)
	}
	if len(owners.records) != 0 {
		t.Fatal("ownership record survived successful cleanup")
	}
}

func TestControllerPersistsReservationBeforeStageAndActivatesBeforeProbe(t *testing.T) {
	store := &storeStub{}
	machine := &machineStub{done: make(chan struct{})}
	now := time.Date(2026, 7, 15, 16, 10, 0, 0, time.UTC)
	owners := newOwnershipStub()
	controller := &Controller{Store: store, Payloads: payloadStub{}, Ownership: owners, Random: bytes.NewReader(bytes.Repeat([]byte{0x47}, 32)), Now: func() time.Time { return now }}
	controller.stage = func(_ context.Context, request firecracker.StageRequest) (stagedMachine, error) {
		owners.mu.Lock()
		reserved, exists := owners.records[request.MachineID]
		owners.mu.Unlock()
		if !exists || reserved.Phase != firecracker.OwnershipPhaseReserved || reserved.Process != (firecracker.ProcessIdentity{}) {
			t.Fatal("stage began without a durable reserved ownership record")
		}
		return stagedMachine{spec: firecracker.Spec{}, root: reserved.Root, vsockPath: "/jail/run/guest.vsock"}, nil
	}
	controller.start = func(context.Context, firecracker.Spec) (machineProcess, error) { return machine, nil }
	controller.probe = func(_ context.Context, _ string, _ string, challenge string) (guest.Attestation, error) {
		owners.mu.Lock()
		active := owners.records["runtime-machine-1"]
		owners.mu.Unlock()
		if active.Phase != firecracker.OwnershipPhaseActive || active.Process.PID != 1234 {
			t.Fatal("guest probe began before exact process ownership activation")
		}
		return guest.Attestation{Challenge: challenge, GuestAgentBuild: "lites-runtime-guest-agent.v1", UserID: 1000, GroupID: 1000, BootUnixMillis: now.UnixMilli()}, nil
	}
	if _, err := controller.Provision(context.Background(), validProvisionRequest(now)); err != nil || owners.activations != 1 {
		t.Fatalf("Provision() err=%v activations=%d", err, owners.activations)
	}
}

func TestControllerClaimsOnlyVacantInterruptedProvisionAndKillsItsCgroup(t *testing.T) {
	now := time.Date(2026, 7, 15, 16, 12, 0, 0, time.UTC)
	identity := runtimepostgres.RecoveryIdentity{
		TenantID: "20000000-0000-4000-8000-000000000001", SessionID: "80000000-0000-4000-8000-000000000041",
		AllocationID: "80000000-0000-4000-8000-000000000042", ProvisionAttemptID: "60000000-0000-4000-8000-000000000001",
		HostID: "runtime-host-1", MachineID: "runtime-machine-5", GuestCID: 47,
	}
	recovery := &recoveryStoreStub{states: map[string]runtimepostgres.RecoveryState{identity.SessionID: {
		RecoveryIdentity: identity, SessionVersion: 2, AllocationVersion: 1, ProvisionFence: 1,
		SessionStatus: "provisioning", AllocationStatus: "provisioning",
	}}}
	owners := newOwnershipStub()
	killed := 0
	controller := &Controller{Store: &storeStub{}, Recovery: recovery, Payloads: payloadStub{}, Ownership: owners, Now: func() time.Time { return now }}
	controller.killCgroup = func(_ context.Context, machineID string) error {
		if machineID != identity.MachineID {
			t.Fatalf("unexpected cgroup machine %q", machineID)
		}
		killed++
		return nil
	}
	controller.processAlive = func(firecracker.ProcessIdentity, []string) (bool, error) {
		t.Fatal("reserved ownership must not be interpreted as a process identity")
		return false, nil
	}
	result, err := controller.Recover(context.Background(), RecoverRequest{
		HostID: identity.HostID, HostControlHash: bytes.Repeat([]byte{0x58}, 32),
		AllowedExecutables: []string{"/usr/bin/firecracker"}, StopGrace: time.Second,
	})
	if err != nil || result.Terminated != 1 || killed != 1 || owners.claims != 1 || recovery.states[identity.SessionID].SessionStatus != "terminated" {
		t.Fatalf("Recover()=%#v err=%v killed=%d claims=%d state=%s", result, err, killed, owners.claims, recovery.states[identity.SessionID].SessionStatus)
	}
}

func TestControllerRejectsMissingOwnershipForReadyMachine(t *testing.T) {
	identity := runtimepostgres.RecoveryIdentity{
		TenantID: "20000000-0000-4000-8000-000000000001", SessionID: "80000000-0000-4000-8000-000000000051",
		AllocationID: "80000000-0000-4000-8000-000000000052", ProvisionAttemptID: "60000000-0000-4000-8000-000000000001",
		HostID: "runtime-host-1", MachineID: "runtime-machine-6", GuestCID: 48,
	}
	recovery := &recoveryStoreStub{states: map[string]runtimepostgres.RecoveryState{identity.SessionID: {
		RecoveryIdentity: identity, SessionVersion: 3, AllocationVersion: 2, ProvisionFence: 1,
		SessionStatus: "ready", AllocationStatus: "active",
	}}}
	owners := newOwnershipStub()
	controller := &Controller{Store: &storeStub{}, Recovery: recovery, Payloads: payloadStub{}, Ownership: owners}
	_, err := controller.Recover(context.Background(), RecoverRequest{
		HostID: identity.HostID, HostControlHash: bytes.Repeat([]byte{0x59}, 32),
		AllowedExecutables: []string{"/usr/bin/firecracker"}, StopGrace: time.Second,
	})
	if !errors.Is(err, firecracker.ErrOwnershipIntegrity) || owners.claims != 0 {
		t.Fatalf("Recover() err=%v claims=%d", err, owners.claims)
	}
}

func TestControllerAttestationFailureStopsCleansAndReleasesCapacityState(t *testing.T) {
	store := &storeStub{}
	machine := &machineStub{done: make(chan struct{})}
	now := time.Date(2026, 7, 15, 16, 30, 0, 0, time.UTC)
	owners := newOwnershipStub()
	controller := &Controller{Store: store, Payloads: payloadStub{}, Ownership: owners, Random: bytes.NewReader(bytes.Repeat([]byte{0x43}, 32)), Now: func() time.Time { return now }}
	controller.stage = func(context.Context, firecracker.StageRequest) (stagedMachine, error) {
		return stagedMachine{spec: firecracker.Spec{}, root: "/jail/runtime-machine-1/root", vsockPath: "/jail/run/guest.vsock", cleanup: func() error { return nil }}, nil
	}
	controller.start = func(context.Context, firecracker.Spec) (machineProcess, error) { return machine, nil }
	probeFailure := errors.New("guest did not attest")
	controller.probe = func(context.Context, string, string, string) (guest.Attestation, error) {
		return guest.Attestation{}, probeFailure
	}
	if _, err := controller.Provision(context.Background(), validProvisionRequest(now)); !errors.Is(err, probeFailure) || !machine.stopped || owners.cleanups != 1 || store.requested != 1 || store.completed != 1 || store.ready != 0 {
		t.Fatalf("failed Provision() = %v stopped=%v cleaned=%d calls=%d/%d/%d", err, machine.stopped, owners.cleanups, store.requested, store.completed, store.ready)
	}
}

func TestControllerUnexpectedExitConvergesToDurableTermination(t *testing.T) {
	store := &storeStub{}
	machine := &machineStub{done: make(chan struct{})}
	now := time.Date(2026, 7, 15, 17, 0, 0, 0, time.UTC)
	owners := newOwnershipStub()
	controller := &Controller{Store: store, Payloads: payloadStub{}, Ownership: owners, Random: bytes.NewReader(bytes.Repeat([]byte{0x44}, 32)), Now: func() time.Time { return now }}
	controller.stage = func(context.Context, firecracker.StageRequest) (stagedMachine, error) {
		return stagedMachine{spec: firecracker.Spec{}, root: "/jail/runtime-machine-1/root", vsockPath: "/jail/run/guest.vsock", cleanup: func() error { return nil }}, nil
	}
	controller.start = func(context.Context, firecracker.Spec) (machineProcess, error) { return machine, nil }
	controller.probe = func(_ context.Context, _ string, _ string, challenge string) (guest.Attestation, error) {
		return guest.Attestation{Challenge: challenge, GuestAgentBuild: "lites-runtime-guest-agent.v1", UserID: 1000, GroupID: 1000, BootUnixMillis: now.UnixMilli()}, nil
	}
	provisioned, err := controller.Provision(context.Background(), validProvisionRequest(now))
	if err != nil {
		t.Fatal(err)
	}
	close(machine.done)
	deadline := time.Now().Add(time.Second)
	var requested, completed int
	for {
		store.mu.Lock()
		requested, completed = store.requested, store.completed
		store.mu.Unlock()
		owners.mu.Lock()
		cleanups := owners.cleanups
		owners.mu.Unlock()
		if completed == 1 && cleanups == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if requested != 1 || completed != 1 {
		t.Fatalf("unexpected exit calls=%d/%d", requested, completed)
	}
	if _, err = controller.Terminate(context.Background(), provisioned.Provision.SessionID, "again"); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("terminated machine remained owned: %v", err)
	}
}

func validProvisionRequest(now time.Time) ProvisionRequest {
	return ProvisionRequest{Command: runtimepostgres.ProvisionCommand{
		TenantID: "20000000-0000-0000-0000-000000000001", CapabilityToken: "signed-capability", HostID: "runtime-host-1", MachineID: "runtime-machine-1", GuestCID: 42,
		ProvisionLease: "opaque-provision-lease", ProvisionLeaseExpiresAt: now.Add(time.Minute),
		Payload: runtimepostgres.PayloadPointer{Ref: "encrypted://runtime/provision", Hash: "provision-hash"}, Actor: json.RawMessage(`{"kind":"system"}`), CorrelationID: "a0000000-0000-0000-0000-000000000001",
	}, ScratchRate: firecracker.RateLimit{Bandwidth: firecracker.TokenBucket{Size: 1024, RefillMillis: 1000}, Operations: firecracker.TokenBucket{Size: 100, RefillMillis: 1000}}}
}
