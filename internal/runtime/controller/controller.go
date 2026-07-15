// Package controller binds durable Runtime state to host-local Firecracker
// operations. Database capacity is reserved before host mutation, and every
// successful boot is challenge-attested before the session becomes ready.
package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/runtime/firecracker"
	"github.com/langshift/lites/internal/runtime/guest"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
)

var (
	ErrConfiguration  = errors.New("runtime controller configuration is invalid")
	ErrOutcomeUnknown = errors.New("runtime readiness outcome is unknown")
	ErrNotOwned       = errors.New("runtime machine is not owned by this controller")
)

type RuntimeStore interface {
	BeginProvision(context.Context, runtimepostgres.ProvisionCommand) (runtimepostgres.ProvisionResult, error)
	MarkReady(context.Context, runtimepostgres.ReadyCommand) (runtimepostgres.LifecycleResult, error)
	RequestTermination(context.Context, runtimepostgres.TerminationCommand) (runtimepostgres.LifecycleResult, error)
	CompleteTermination(context.Context, runtimepostgres.TerminatedCommand) (runtimepostgres.LifecycleResult, error)
}

type machineProcess interface {
	Stop(context.Context) error
	Done() <-chan struct{}
	ExitError() error
}

type stagedMachine struct {
	spec      firecracker.Spec
	vsockPath string
	cleanup   func() error
}

type activeMachine struct {
	process machineProcess
	staged  stagedMachine
	request ProvisionRequest
	result  runtimepostgres.ProvisionResult
	version uint64
	mu      sync.Mutex
	closing bool
}

type Controller struct {
	Store    RuntimeStore
	Payloads payload.Store
	Stager   firecracker.Stager
	Runner   firecracker.Runner
	Random   io.Reader
	Now      func() time.Time
	OnError  func(error)

	stage func(context.Context, firecracker.StageRequest) (stagedMachine, error)
	start func(context.Context, firecracker.Spec) (machineProcess, error)
	probe func(context.Context, string, string, string) (guest.Attestation, error)

	mu     sync.Mutex
	active map[string]*activeMachine
}

type ProvisionRequest struct {
	Command     runtimepostgres.ProvisionCommand
	ScratchRate firecracker.RateLimit
}

type Provisioned struct {
	Provision runtimepostgres.ProvisionResult
	Ready     runtimepostgres.LifecycleResult
}

type bootReceipt struct {
	SchemaVersion      int               `json:"schema_version"`
	SessionID          string            `json:"session_id"`
	AllocationID       string            `json:"allocation_id"`
	ProvisionAttemptID string            `json:"provision_attempt_id"`
	MachineID          string            `json:"machine_id"`
	GuestCID           uint32            `json:"guest_cid"`
	PolicyHash         string            `json:"policy_hash"`
	KernelDigest       string            `json:"kernel_digest"`
	RootFSDigest       string            `json:"rootfs_digest"`
	Challenge          string            `json:"challenge"`
	Attestation        guest.Attestation `json:"attestation"`
	AttestedAt         time.Time         `json:"attested_at"`
}

type controlReceipt struct {
	SchemaVersion int       `json:"schema_version"`
	SessionID     string    `json:"session_id"`
	Reason        string    `json:"reason"`
	OccurredAt    time.Time `json:"occurred_at"`
}

func (controller *Controller) Provision(ctx context.Context, request ProvisionRequest) (Provisioned, error) {
	if controller == nil || controller.Store == nil || controller.Payloads == nil || request.Command.TenantID == "" {
		return Provisioned{}, ErrConfiguration
	}
	if controller.containsCommandMachine(request.Command.MachineID) {
		return Provisioned{}, ErrNotOwned
	}
	provision, err := controller.Store.BeginProvision(ctx, request.Command)
	if err != nil {
		return Provisioned{}, err
	}
	if provision.TenantID != request.Command.TenantID {
		return Provisioned{}, ErrConfiguration
	}
	stage := controller.stageMachine
	staged, err := stage(ctx, firecracker.StageRequest{
		MachineID: request.Command.MachineID, GuestCID: request.Command.GuestCID,
		KernelDigest: provision.Policy.KernelDigest, RootFSDigest: provision.Policy.RootFSDigest,
		VCPUCount: provision.Policy.VCPUCount, MemoryMiB: provision.Policy.MemoryMiB, DiskMiB: provision.Policy.DiskMiB,
		ScratchRate: request.ScratchRate,
	})
	if err != nil {
		return Provisioned{}, errors.Join(err, controller.abortProvision(ctx, request, provision, nil, stagedMachine{}, "staging_failed"))
	}
	start := controller.startMachine
	process, err := start(ctx, staged.spec)
	if err != nil {
		return Provisioned{}, errors.Join(err, controller.abortProvision(ctx, request, provision, nil, staged, "vmm_start_failed"))
	}
	challengeBytes := make([]byte, 32)
	random := controller.Random
	if random == nil {
		random = rand.Reader
	}
	if _, err = io.ReadFull(random, challengeBytes); err != nil {
		return Provisioned{}, errors.Join(err, controller.abortProvision(ctx, request, provision, process, staged, "challenge_failed"))
	}
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes)
	probe := controller.probeGuest
	attestation, err := probe(ctx, staged.vsockPath, "probe:"+provision.SessionID, challenge)
	if err != nil {
		return Provisioned{}, errors.Join(err, controller.abortProvision(ctx, request, provision, process, staged, "guest_attestation_failed"))
	}
	attestedAt := controller.now()
	pointer, receiptHash, err := controller.putReceipt(ctx, provision.SessionID+":boot:3", request.Command.TenantID, bootReceipt{
		SchemaVersion: 1, SessionID: provision.SessionID, AllocationID: provision.AllocationID, ProvisionAttemptID: provision.ProvisionAttemptID,
		MachineID: provision.MachineID, GuestCID: provision.GuestCID, PolicyHash: provision.Policy.PolicyHash,
		KernelDigest: provision.Policy.KernelDigest, RootFSDigest: provision.Policy.RootFSDigest,
		Challenge: challenge, Attestation: attestation, AttestedAt: attestedAt,
	})
	if err != nil {
		return Provisioned{}, errors.Join(err, controller.abortProvision(ctx, request, provision, process, staged, "boot_receipt_failed"))
	}
	readyCommand := runtimepostgres.ReadyCommand{LifecycleCommand: runtimepostgres.LifecycleCommand{
		TenantID: request.Command.TenantID, SessionID: provision.SessionID, ProvisionAttemptID: provision.ProvisionAttemptID,
		ProvisionFence: 1, ExpectedVersion: 2, LeaseToken: request.Command.ProvisionLease, ObservedAt: attestedAt,
		Payload: pointer, Actor: request.Command.Actor, CorrelationID: request.Command.CorrelationID,
	}, BootReceiptHash: receiptHash}
	ready, err := controller.Store.MarkReady(ctx, readyCommand)
	if err != nil {
		// The same event pointer and receipt make an unknown commit retry exact.
		ready, err = controller.Store.MarkReady(ctx, readyCommand)
	}
	active := &activeMachine{process: process, staged: staged, request: request, result: provision, version: 3}
	if err != nil {
		if controller.remember(provision.SessionID, active) {
			controller.watch(provision.SessionID, active)
		}
		return Provisioned{Provision: provision}, errors.Join(ErrOutcomeUnknown, err)
	}
	active.version = ready.Version
	if !controller.remember(provision.SessionID, active) {
		return Provisioned{}, errors.Join(ErrConfiguration, controller.abortProvision(ctx, request, provision, process, staged, "duplicate_machine_owner"))
	}
	controller.watch(provision.SessionID, active)
	return Provisioned{Provision: provision, Ready: ready}, nil
}

func (controller *Controller) Terminate(ctx context.Context, sessionID, reason string) (runtimepostgres.LifecycleResult, error) {
	active := controller.take(sessionID)
	if active == nil || reason == "" {
		return runtimepostgres.LifecycleResult{}, ErrNotOwned
	}
	active.mu.Lock()
	if active.closing {
		active.mu.Unlock()
		return runtimepostgres.LifecycleResult{}, ErrNotOwned
	}
	active.closing = true
	active.mu.Unlock()
	at := controller.now()
	pointer, _, err := controller.putReceipt(ctx, sessionID+":termination", active.request.Command.TenantID, controlReceipt{SchemaVersion: 1, SessionID: sessionID, Reason: reason, OccurredAt: at})
	if err != nil {
		active.mu.Lock()
		active.closing = false
		active.mu.Unlock()
		controller.remember(sessionID, active)
		return runtimepostgres.LifecycleResult{}, err
	}
	requested, err := controller.Store.RequestTermination(ctx, runtimepostgres.TerminationCommand{LifecycleCommand: runtimepostgres.LifecycleCommand{
		TenantID: active.request.Command.TenantID, SessionID: sessionID, ProvisionAttemptID: active.result.ProvisionAttemptID,
		ProvisionFence: 1, ExpectedVersion: active.version, LeaseToken: active.request.Command.ProvisionLease, ObservedAt: at,
		Payload: pointer, Actor: active.request.Command.Actor, CorrelationID: active.request.Command.CorrelationID,
	}, Reason: reason})
	if err != nil {
		active.mu.Lock()
		active.closing = false
		active.mu.Unlock()
		controller.remember(sessionID, active)
		return runtimepostgres.LifecycleResult{}, err
	}
	stopErr := active.process.Stop(ctx)
	cleanupErr := active.staged.cleanup()
	completedAt := controller.now()
	completePointer, cleanupHash, receiptErr := controller.putReceipt(ctx, sessionID+":terminated", active.request.Command.TenantID, controlReceipt{SchemaVersion: 1, SessionID: sessionID, Reason: reason, OccurredAt: completedAt})
	if receiptErr != nil {
		return runtimepostgres.LifecycleResult{}, errors.Join(stopErr, cleanupErr, receiptErr)
	}
	terminated, completeErr := controller.Store.CompleteTermination(ctx, runtimepostgres.TerminatedCommand{LifecycleCommand: runtimepostgres.LifecycleCommand{
		TenantID: active.request.Command.TenantID, SessionID: sessionID, ProvisionAttemptID: active.result.ProvisionAttemptID,
		ProvisionFence: 1, ExpectedVersion: requested.Version, LeaseToken: active.request.Command.ProvisionLease, ObservedAt: completedAt,
		Payload: completePointer, Actor: active.request.Command.Actor, CorrelationID: active.request.Command.CorrelationID,
	}, CleanupReceiptHash: cleanupHash, UsageManifest: json.RawMessage(`{"schema_version":1,"vcpu_millis":0}`)})
	return terminated, errors.Join(stopErr, cleanupErr, completeErr)
}

func (controller *Controller) abortProvision(ctx context.Context, request ProvisionRequest, provision runtimepostgres.ProvisionResult, process machineProcess, staged stagedMachine, reason string) error {
	var stopErr, cleanupErr error
	if process != nil {
		stopErr = process.Stop(ctx)
	}
	if staged.cleanup != nil {
		cleanupErr = staged.cleanup()
	}
	at := controller.now()
	pointer, _, err := controller.putReceipt(ctx, provision.SessionID+":abort", request.Command.TenantID, controlReceipt{SchemaVersion: 1, SessionID: provision.SessionID, Reason: reason, OccurredAt: at})
	if err != nil {
		return errors.Join(stopErr, cleanupErr, err)
	}
	requested, err := controller.Store.RequestTermination(ctx, runtimepostgres.TerminationCommand{LifecycleCommand: runtimepostgres.LifecycleCommand{
		TenantID: request.Command.TenantID, SessionID: provision.SessionID, ProvisionAttemptID: provision.ProvisionAttemptID,
		ProvisionFence: 1, ExpectedVersion: 2, LeaseToken: request.Command.ProvisionLease, ObservedAt: at,
		Payload: pointer, Actor: request.Command.Actor, CorrelationID: request.Command.CorrelationID,
	}, Reason: reason})
	if err != nil {
		return errors.Join(stopErr, cleanupErr, err)
	}
	completedAt := at.Add(time.Microsecond)
	completePointer, cleanupHash, receiptErr := controller.putReceipt(ctx, provision.SessionID+":aborted", request.Command.TenantID, controlReceipt{SchemaVersion: 1, SessionID: provision.SessionID, Reason: reason, OccurredAt: completedAt})
	if receiptErr != nil {
		return errors.Join(stopErr, cleanupErr, receiptErr)
	}
	_, completeErr := controller.Store.CompleteTermination(ctx, runtimepostgres.TerminatedCommand{LifecycleCommand: runtimepostgres.LifecycleCommand{
		TenantID: request.Command.TenantID, SessionID: provision.SessionID, ProvisionAttemptID: provision.ProvisionAttemptID,
		ProvisionFence: 1, ExpectedVersion: requested.Version, LeaseToken: request.Command.ProvisionLease, ObservedAt: completedAt,
		Payload: completePointer, Actor: request.Command.Actor, CorrelationID: request.Command.CorrelationID,
	}, CleanupReceiptHash: cleanupHash, UsageManifest: json.RawMessage(`{"schema_version":1,"vcpu_millis":0}`)})
	return errors.Join(stopErr, cleanupErr, completeErr)
}

func (controller *Controller) stageMachine(ctx context.Context, request firecracker.StageRequest) (stagedMachine, error) {
	if controller.stage != nil {
		return controller.stage(ctx, request)
	}
	machine, err := controller.Stager.Stage(ctx, request)
	if err != nil {
		return stagedMachine{}, err
	}
	return stagedMachine{spec: machine.Spec, vsockPath: filepath.Join(machine.Root, "run", "guest.vsock"), cleanup: machine.Cleanup}, nil
}

func (controller *Controller) startMachine(ctx context.Context, spec firecracker.Spec) (machineProcess, error) {
	if controller.start != nil {
		return controller.start(ctx, spec)
	}
	return controller.Runner.Start(ctx, spec)
}

func (controller *Controller) probeGuest(ctx context.Context, socketPath, requestID, challenge string) (guest.Attestation, error) {
	if controller.probe != nil {
		return controller.probe(ctx, socketPath, requestID, challenge)
	}
	client := firecracker.GuestClient{Connector: firecracker.VSockConnector{SocketPath: socketPath, Timeout: 5 * time.Second}, WriteTimeout: 5 * time.Second}
	return client.Probe(ctx, requestID, challenge)
}

func (controller *Controller) putReceipt(ctx context.Context, objectID, tenantID string, value any) (runtimepostgres.PayloadPointer, string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return runtimepostgres.PayloadPointer{}, "", err
	}
	digest := sha256.Sum256(encoded)
	manifest, err := controller.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: "runtime-control-evidence", ContentType: "application/json"}, encoded)
	if err != nil || manifest.Ref == "" || manifest.Hash == "" {
		return runtimepostgres.PayloadPointer{}, "", errors.Join(err, ErrConfiguration)
	}
	return runtimepostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, hex.EncodeToString(digest[:]), nil
}

func (controller *Controller) now() time.Time {
	if controller.Now != nil {
		return controller.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (controller *Controller) remember(sessionID string, machine *activeMachine) bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.active == nil {
		controller.active = make(map[string]*activeMachine)
	}
	if _, exists := controller.active[sessionID]; exists {
		return false
	}
	controller.active[sessionID] = machine
	return true
}

func (controller *Controller) take(sessionID string) *activeMachine {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	machine := controller.active[sessionID]
	delete(controller.active, sessionID)
	return machine
}

func (controller *Controller) containsCommandMachine(machineID string) bool {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	for _, machine := range controller.active {
		if machine.request.Command.MachineID == machineID {
			return true
		}
	}
	return false
}

func (controller *Controller) watch(sessionID string, machine *activeMachine) {
	go func() {
		<-machine.process.Done()
		machine.mu.Lock()
		closing := machine.closing
		machine.mu.Unlock()
		if closing {
			return
		}
		result, err := controller.Terminate(context.Background(), sessionID, "unexpected_vmm_exit")
		if err != nil && result.Status != "terminated" && controller.OnError != nil {
			controller.OnError(err)
		}
	}()
}
