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
		close(machine.done)
	}
	return nil
}
func (machine *machineStub) Done() <-chan struct{} { return machine.done }
func (machine *machineStub) ExitError() error      { return nil }

func TestControllerAttestsPersistsReadyAndTerminatesOwnedMachine(t *testing.T) {
	store := &storeStub{}
	machine := &machineStub{done: make(chan struct{})}
	cleaned := 0
	now := time.Date(2026, 7, 15, 16, 0, 0, 0, time.UTC)
	controller := &Controller{Store: store, Payloads: payloadStub{}, Random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)), Now: func() time.Time { return now }}
	controller.stage = func(context.Context, firecracker.StageRequest) (stagedMachine, error) {
		return stagedMachine{spec: firecracker.Spec{}, vsockPath: "/jail/run/guest.vsock", cleanup: func() error { cleaned++; return nil }}, nil
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
}

func TestControllerAttestationFailureStopsCleansAndReleasesCapacityState(t *testing.T) {
	store := &storeStub{}
	machine := &machineStub{done: make(chan struct{})}
	cleaned := 0
	now := time.Date(2026, 7, 15, 16, 30, 0, 0, time.UTC)
	controller := &Controller{Store: store, Payloads: payloadStub{}, Random: bytes.NewReader(bytes.Repeat([]byte{0x43}, 32)), Now: func() time.Time { return now }}
	controller.stage = func(context.Context, firecracker.StageRequest) (stagedMachine, error) {
		return stagedMachine{spec: firecracker.Spec{}, vsockPath: "/jail/run/guest.vsock", cleanup: func() error { cleaned++; return nil }}, nil
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

func validProvisionRequest(now time.Time) ProvisionRequest {
	return ProvisionRequest{Command: runtimepostgres.ProvisionCommand{
		TenantID: "20000000-0000-0000-0000-000000000001", CapabilityToken: "signed-capability", HostID: "runtime-host-1", MachineID: "runtime-machine-1", GuestCID: 42,
		ProvisionLease: "opaque-provision-lease", ProvisionLeaseExpiresAt: now.Add(time.Minute),
		Payload: runtimepostgres.PayloadPointer{Ref: "encrypted://runtime/provision", Hash: "provision-hash"}, Actor: json.RawMessage(`{"kind":"system"}`), CorrelationID: "a0000000-0000-0000-0000-000000000001",
	}, ScratchRate: firecracker.RateLimit{Bandwidth: firecracker.TokenBucket{Size: 1024, RefillMillis: 1000}, Operations: firecracker.TokenBucket{Size: 100, RefillMillis: 1000}}}
}
