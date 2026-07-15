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
	createErr, verifyErr error
}

func newOwnershipStub() *ownershipStub {
	return &ownershipStub{records: make(map[string]firecracker.OwnershipRecord)}
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

func TestControllerFailsClosedBeforeAttestationWhenOwnershipCannotBePersisted(t *testing.T) {
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
	if _, err := controller.Provision(context.Background(), validProvisionRequest(now)); !errors.Is(err, ownershipFailure) || !machine.stopped || store.ready != 0 || store.requested != 1 || store.completed != 1 {
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

func TestControllerAttestsPersistsReadyAndTerminatesOwnedMachine(t *testing.T) {
	store := &storeStub{}
	machine := &machineStub{done: make(chan struct{})}
	cleaned := 0
	now := time.Date(2026, 7, 15, 16, 0, 0, 0, time.UTC)
	owners := newOwnershipStub()
	controller := &Controller{Store: store, Payloads: payloadStub{}, Ownership: owners, Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)), Now: func() time.Time { return now }}
	controller.stage = func(context.Context, firecracker.StageRequest) (stagedMachine, error) {
		return stagedMachine{spec: firecracker.Spec{}, root: "/jail/runtime-machine-1/root", vsockPath: "/jail/run/guest.vsock", cleanup: func() error {
			cleaned++
			owners.mu.Lock()
			delete(owners.records, "runtime-machine-1")
			owners.mu.Unlock()
			return nil
		}}, nil
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
	if err != nil || terminated.Status != "terminated" || !machine.stopped || cleaned != 1 || store.requested != 1 || store.completed != 1 {
		t.Fatalf("Terminate() = %#v, %v stopped=%v cleaned=%d calls=%d/%d", terminated, err, machine.stopped, cleaned, store.requested, store.completed)
	}
	if len(owners.records) != 0 {
		t.Fatal("ownership record survived successful cleanup")
	}
}

func TestControllerAttestationFailureStopsCleansAndReleasesCapacityState(t *testing.T) {
	store := &storeStub{}
	machine := &machineStub{done: make(chan struct{})}
	cleaned := 0
	now := time.Date(2026, 7, 15, 16, 30, 0, 0, time.UTC)
	controller := &Controller{Store: store, Payloads: payloadStub{}, Ownership: newOwnershipStub(), Random: bytes.NewReader(bytes.Repeat([]byte{0x43}, 32)), Now: func() time.Time { return now }}
	controller.stage = func(context.Context, firecracker.StageRequest) (stagedMachine, error) {
		return stagedMachine{spec: firecracker.Spec{}, root: "/jail/runtime-machine-1/root", vsockPath: "/jail/run/guest.vsock", cleanup: func() error { cleaned++; return nil }}, nil
	}
	controller.start = func(context.Context, firecracker.Spec) (machineProcess, error) { return machine, nil }
	probeFailure := errors.New("guest did not attest")
	controller.probe = func(context.Context, string, string, string) (guest.Attestation, error) {
		return guest.Attestation{}, probeFailure
	}
	if _, err := controller.Provision(context.Background(), validProvisionRequest(now)); !errors.Is(err, probeFailure) || !machine.stopped || cleaned != 1 || store.requested != 1 || store.completed != 1 || store.ready != 0 {
		t.Fatalf("failed Provision() = %v stopped=%v cleaned=%d calls=%d/%d/%d", err, machine.stopped, cleaned, store.requested, store.completed, store.ready)
	}
}

func TestControllerUnexpectedExitConvergesToDurableTermination(t *testing.T) {
	store := &storeStub{}
	machine := &machineStub{done: make(chan struct{})}
	cleaned := make(chan struct{}, 1)
	now := time.Date(2026, 7, 15, 17, 0, 0, 0, time.UTC)
	controller := &Controller{Store: store, Payloads: payloadStub{}, Ownership: newOwnershipStub(), Random: bytes.NewReader(bytes.Repeat([]byte{0x44}, 32)), Now: func() time.Time { return now }}
	controller.stage = func(context.Context, firecracker.StageRequest) (stagedMachine, error) {
		return stagedMachine{spec: firecracker.Spec{}, root: "/jail/runtime-machine-1/root", vsockPath: "/jail/run/guest.vsock", cleanup: func() error { cleaned <- struct{}{}; return nil }}, nil
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
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("unexpected VMM exit was not cleaned")
	}
	deadline := time.Now().Add(time.Second)
	var requested, completed int
	for {
		store.mu.Lock()
		requested, completed = store.requested, store.completed
		store.mu.Unlock()
		if completed == 1 || time.Now().After(deadline) {
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
