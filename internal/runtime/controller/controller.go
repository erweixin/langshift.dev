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

type RecoveryRuntimeStore interface {
	InspectOwnedMachine(context.Context, runtimepostgres.RecoveryIdentity, []byte) (runtimepostgres.RecoveryState, error)
	ListHostMachines(context.Context, string, []byte, string, int) ([]runtimepostgres.RecoveryState, error)
	RequestRecoveryTermination(context.Context, runtimepostgres.RecoveryTerminationCommand) (runtimepostgres.LifecycleResult, error)
	CompleteRecoveryTermination(context.Context, runtimepostgres.RecoveryTerminatedCommand) (runtimepostgres.LifecycleResult, error)
}

type machineProcess interface {
	Stop(context.Context) error
	Done() <-chan struct{}
	ExitError() error
	Identity() (firecracker.ProcessIdentity, error)
}

type ownershipStore interface {
	Create(firecracker.OwnershipRecord) error
	Verify(firecracker.OwnershipRecord) error
	Scan() ([]firecracker.OwnershipRecord, error)
	Cleanup(firecracker.OwnershipRecord) error
	CleanupPrepared(firecracker.OwnershipRecord) (bool, error)
	Remove(firecracker.OwnershipRecord) error
}

type stagedMachine struct {
	spec      firecracker.Spec
	root      string
	vsockPath string
	cleanup   func() error
}

type activeMachine struct {
	process         machineProcess
	staged          stagedMachine
	request         ProvisionRequest
	result          runtimepostgres.ProvisionResult
	version         uint64
	owner           firecracker.OwnershipRecord
	recovered       bool
	hostControlHash []byte
	mu              sync.Mutex
	closing         bool
}

type Controller struct {
	Store     RuntimeStore
	Recovery  RecoveryRuntimeStore
	Payloads  payload.Store
	Stager    firecracker.Stager
	Runner    firecracker.Runner
	Ownership ownershipStore
	Random    io.Reader
	Now       func() time.Time
	OnError   func(error)

	stage        func(context.Context, firecracker.StageRequest) (stagedMachine, error)
	start        func(context.Context, firecracker.Spec) (machineProcess, error)
	probe        func(context.Context, string, string, string) (guest.Attestation, error)
	processAlive func(firecracker.ProcessIdentity, []string) (bool, error)
	adopt        func(firecracker.ProcessIdentity, []string, time.Duration) (machineProcess, error)

	mu     sync.Mutex
	active map[string]*activeMachine
}

type ProvisionRequest struct {
	Command     runtimepostgres.ProvisionCommand
	ScratchRate firecracker.RateLimit
}

type Provisioned struct {
	Provision runtimepostgres.ProvisionResult `json:"provision"`
	Ready     runtimepostgres.LifecycleResult `json:"ready"`
}

type RecoverRequest struct {
	HostID             string
	HostControlHash    []byte
	AllowedExecutables []string
	StopGrace          time.Duration
}

type RecoverResult struct {
	Adopted, Terminated, Cleaned int
}

type ReconcileRequest struct {
	HostID          string
	HostControlHash []byte
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
	if controller == nil || controller.Store == nil || controller.Payloads == nil || controller.Ownership == nil || request.Command.TenantID == "" {
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
		return Provisioned{}, errors.Join(err, controller.abortProvision(ctx, request, provision, nil, stagedMachine{}, nil, "staging_failed"))
	}
	start := controller.startMachine
	process, err := start(ctx, staged.spec)
	if err != nil {
		return Provisioned{}, errors.Join(err, controller.abortProvision(ctx, request, provision, nil, staged, nil, "vmm_start_failed"))
	}
	identity, err := process.Identity()
	if err != nil {
		return Provisioned{}, errors.Join(err, controller.abortProvision(ctx, request, provision, process, staged, nil, "vmm_identity_failed"))
	}
	owner := firecracker.OwnershipRecord{
		SchemaVersion: 1, HostID: request.Command.HostID, TenantID: request.Command.TenantID,
		SessionID: provision.SessionID, AllocationID: provision.AllocationID, ProvisionAttemptID: provision.ProvisionAttemptID,
		MachineID: provision.MachineID, GuestCID: provision.GuestCID, Root: staged.root, Process: identity, CreatedAt: controller.now(),
	}
	if err = controller.Ownership.Create(owner); err != nil {
		return Provisioned{}, errors.Join(err, controller.abortProvision(ctx, request, provision, process, staged, nil, "ownership_record_failed"))
	}
	challengeBytes := make([]byte, 32)
	random := controller.Random
	if random == nil {
		random = rand.Reader
	}
	if _, err = io.ReadFull(random, challengeBytes); err != nil {
		return Provisioned{}, errors.Join(err, controller.abortProvision(ctx, request, provision, process, staged, &owner, "challenge_failed"))
	}
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes)
	probe := controller.probeGuest
	attestation, err := probe(ctx, staged.vsockPath, "probe:"+provision.SessionID, challenge)
	if err != nil {
		return Provisioned{}, errors.Join(err, controller.abortProvision(ctx, request, provision, process, staged, &owner, "guest_attestation_failed"))
	}
	attestedAt := controller.now()
	pointer, receiptHash, err := controller.putReceipt(ctx, provision.SessionID+":boot:3", request.Command.TenantID, bootReceipt{
		SchemaVersion: 1, SessionID: provision.SessionID, AllocationID: provision.AllocationID, ProvisionAttemptID: provision.ProvisionAttemptID,
		MachineID: provision.MachineID, GuestCID: provision.GuestCID, PolicyHash: provision.Policy.PolicyHash,
		KernelDigest: provision.Policy.KernelDigest, RootFSDigest: provision.Policy.RootFSDigest,
		Challenge: challenge, Attestation: attestation, AttestedAt: attestedAt,
	})
	if err != nil {
		return Provisioned{}, errors.Join(err, controller.abortProvision(ctx, request, provision, process, staged, &owner, "boot_receipt_failed"))
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
	staged.cleanup = func() error { return controller.Ownership.Cleanup(owner) }
	active := &activeMachine{process: process, staged: staged, request: request, result: provision, version: 3, owner: owner}
	if err != nil {
		if controller.remember(provision.SessionID, active) {
			controller.watch(provision.SessionID, active)
		}
		return Provisioned{Provision: provision}, errors.Join(ErrOutcomeUnknown, err)
	}
	active.version = ready.Version
	if !controller.remember(provision.SessionID, active) {
		return Provisioned{}, errors.Join(ErrConfiguration, controller.abortProvision(ctx, request, provision, process, staged, &owner, "duplicate_machine_owner"))
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
	var requested runtimepostgres.LifecycleResult
	if active.recovered {
		if controller.Recovery == nil {
			err = ErrConfiguration
		} else {
			requested, err = controller.Recovery.RequestRecoveryTermination(ctx, runtimepostgres.RecoveryTerminationCommand{LifecycleCommand: runtimepostgres.LifecycleCommand{
				TenantID: active.request.Command.TenantID, SessionID: sessionID, ExpectedVersion: active.version, ObservedAt: at,
				Payload: pointer, Actor: active.request.Command.Actor, CorrelationID: active.request.Command.CorrelationID,
			}, Authority: runtimepostgres.RecoveryOwned, Identity: recoveryIdentity(active.owner), HostControlHash: active.hostControlHash, Reason: reason})
		}
	} else {
		requested, err = controller.Store.RequestTermination(ctx, runtimepostgres.TerminationCommand{LifecycleCommand: runtimepostgres.LifecycleCommand{
			TenantID: active.request.Command.TenantID, SessionID: sessionID, ProvisionAttemptID: active.result.ProvisionAttemptID,
			ProvisionFence: 1, ExpectedVersion: active.version, LeaseToken: active.request.Command.ProvisionLease, ObservedAt: at,
			Payload: pointer, Actor: active.request.Command.Actor, CorrelationID: active.request.Command.CorrelationID,
		}, Reason: reason})
	}
	if err != nil {
		active.mu.Lock()
		active.closing = false
		active.mu.Unlock()
		controller.remember(sessionID, active)
		return runtimepostgres.LifecycleResult{}, err
	}
	stopErr := active.process.Stop(ctx)
	if stopErr != nil && !errors.Is(stopErr, firecracker.ErrVMMExited) {
		return requested, stopErr
	}
	if errors.Is(stopErr, firecracker.ErrVMMExited) {
		stopErr = nil
	}
	ownerErr := controller.Ownership.Verify(active.owner)
	if ownerErr != nil {
		return requested, ownerErr
	}
	completedAt := controller.now()
	completePointer, cleanupHash, receiptErr := controller.putReceipt(ctx, sessionID+":terminated", active.request.Command.TenantID, controlReceipt{SchemaVersion: 1, SessionID: sessionID, Reason: reason, OccurredAt: completedAt})
	if receiptErr != nil {
		return runtimepostgres.LifecycleResult{}, errors.Join(stopErr, receiptErr)
	}
	if cleanupErr := active.staged.cleanup(); cleanupErr != nil {
		return requested, cleanupErr
	}
	var terminated runtimepostgres.LifecycleResult
	var completeErr error
	if active.recovered {
		terminated, completeErr = controller.Recovery.CompleteRecoveryTermination(ctx, runtimepostgres.RecoveryTerminatedCommand{LifecycleCommand: runtimepostgres.LifecycleCommand{
			TenantID: active.request.Command.TenantID, SessionID: sessionID, ExpectedVersion: requested.Version, ObservedAt: completedAt,
			Payload: completePointer, Actor: active.request.Command.Actor, CorrelationID: active.request.Command.CorrelationID,
		}, Identity: recoveryIdentity(active.owner), HostControlHash: active.hostControlHash, CleanupReceiptHash: cleanupHash, UsageManifest: json.RawMessage(`{"schema_version":1,"vcpu_millis":0}`)})
	} else {
		terminated, completeErr = controller.Store.CompleteTermination(ctx, runtimepostgres.TerminatedCommand{LifecycleCommand: runtimepostgres.LifecycleCommand{
			TenantID: active.request.Command.TenantID, SessionID: sessionID, ProvisionAttemptID: active.result.ProvisionAttemptID,
			ProvisionFence: 1, ExpectedVersion: requested.Version, LeaseToken: active.request.Command.ProvisionLease, ObservedAt: completedAt,
			Payload: completePointer, Actor: active.request.Command.Actor, CorrelationID: active.request.Command.CorrelationID,
		}, CleanupReceiptHash: cleanupHash, UsageManifest: json.RawMessage(`{"schema_version":1,"vcpu_millis":0}`)})
	}
	if completeErr == nil {
		completeErr = controller.Ownership.Remove(active.owner)
	}
	return terminated, errors.Join(stopErr, completeErr)
}

func (controller *Controller) Recover(ctx context.Context, request RecoverRequest) (RecoverResult, error) {
	if controller == nil || controller.Store == nil || controller.Recovery == nil || controller.Payloads == nil || controller.Ownership == nil || request.HostID == "" || len(request.HostControlHash) != 32 || len(request.AllowedExecutables) == 0 || request.StopGrace <= 0 || request.StopGrace > 30*time.Second {
		return RecoverResult{}, ErrConfiguration
	}
	records, err := controller.Ownership.Scan()
	if err != nil {
		return RecoverResult{}, err
	}
	recordIndex := make(map[string]firecracker.OwnershipRecord, len(records))
	for _, record := range records {
		recordIndex[record.SessionID] = record
	}
	var after string
	for {
		inventory, inventoryErr := controller.Recovery.ListHostMachines(ctx, request.HostID, request.HostControlHash, after, 5000)
		if inventoryErr != nil {
			return RecoverResult{}, inventoryErr
		}
		for _, state := range inventory {
			record, exists := recordIndex[state.SessionID]
			if !exists || recoveryIdentity(record) != state.RecoveryIdentity {
				return RecoverResult{}, firecracker.ErrOwnershipIntegrity
			}
		}
		if len(inventory) < 5000 {
			break
		}
		after = inventory[len(inventory)-1].SessionID
	}
	result := RecoverResult{}
	for _, record := range records {
		if record.HostID != request.HostID {
			return result, firecracker.ErrOwnershipIntegrity
		}
		state, inspectErr := controller.Recovery.InspectOwnedMachine(ctx, recoveryIdentity(record), request.HostControlHash)
		if inspectErr != nil {
			if !errors.Is(inspectErr, runtimepostgres.ErrSessionConflict) {
				return result, inspectErr
			}
			if disposeErr := controller.disposeOrphan(ctx, record, request.AllowedExecutables, request.StopGrace); disposeErr != nil {
				return result, disposeErr
			}
			result.Cleaned++
			continue
		}
		alive := controller.processAlive
		if alive == nil {
			alive = firecracker.ProcessAlive
		}
		isAlive, aliveErr := alive(record.Process, request.AllowedExecutables)
		if aliveErr != nil {
			return result, aliveErr
		}
		var process machineProcess = deadMachine{identity: record.Process}
		if isAlive {
			adopt := controller.adopt
			if adopt == nil {
				adopt = func(identity firecracker.ProcessIdentity, executables []string, grace time.Duration) (machineProcess, error) {
					return firecracker.Adopt(identity, executables, grace)
				}
			}
			process, err = adopt(record.Process, request.AllowedExecutables, request.StopGrace)
			if err != nil {
				return result, err
			}
		}
		switch state.SessionStatus {
		case "ready", "running", "idle":
			cleaned, cleanupStateErr := controller.Ownership.CleanupPrepared(record)
			if cleanupStateErr != nil || cleaned {
				return result, errors.Join(cleanupStateErr, firecracker.ErrOwnershipIntegrity)
			}
			active := controller.recoveredMachine(record, state, process, request.HostControlHash)
			if isAlive {
				if !controller.remember(record.SessionID, active) {
					return result, ErrNotOwned
				}
				controller.watch(record.SessionID, active)
				result.Adopted++
				continue
			}
			if !controller.remember(record.SessionID, active) {
				return result, ErrNotOwned
			}
			if _, err = controller.Terminate(ctx, record.SessionID, "vmm_missing_after_restart"); err != nil {
				return result, err
			}
			result.Terminated++
		case "provisioning":
			cleaned, cleanupStateErr := controller.Ownership.CleanupPrepared(record)
			if cleanupStateErr != nil || cleaned {
				return result, errors.Join(cleanupStateErr, firecracker.ErrOwnershipIntegrity)
			}
			active := controller.recoveredMachine(record, state, process, request.HostControlHash)
			if !controller.remember(record.SessionID, active) {
				return result, ErrNotOwned
			}
			if _, err = controller.Terminate(ctx, record.SessionID, "provision_interrupted_by_restart"); err != nil {
				return result, err
			}
			result.Terminated++
		case "termination_requested":
			if err = controller.finishRecoveredTermination(ctx, record, state, process, request.HostControlHash); err != nil {
				return result, err
			}
			result.Terminated++
		case "terminated", "failed":
			if isAlive {
				if stopErr := process.Stop(ctx); stopErr != nil && !errors.Is(stopErr, firecracker.ErrVMMExited) {
					return result, stopErr
				}
			}
			if err = controller.Ownership.Cleanup(record); err != nil {
				return result, err
			}
			if err = controller.Ownership.Remove(record); err != nil {
				return result, err
			}
			result.Cleaned++
		default:
			return result, ErrConfiguration
		}
	}
	return result, nil
}

func (controller *Controller) disposeOrphan(ctx context.Context, record firecracker.OwnershipRecord, allowedExecutables []string, stopGrace time.Duration) error {
	alive := controller.processAlive
	if alive == nil {
		alive = firecracker.ProcessAlive
	}
	isAlive, err := alive(record.Process, allowedExecutables)
	if err != nil {
		return err
	}
	if isAlive {
		adopt := controller.adopt
		if adopt == nil {
			adopt = func(identity firecracker.ProcessIdentity, executables []string, grace time.Duration) (machineProcess, error) {
				return firecracker.Adopt(identity, executables, grace)
			}
		}
		process, adoptErr := adopt(record.Process, allowedExecutables, stopGrace)
		if adoptErr != nil {
			return adoptErr
		}
		if stopErr := process.Stop(ctx); stopErr != nil && !errors.Is(stopErr, firecracker.ErrVMMExited) {
			return stopErr
		}
	}
	if err = controller.Ownership.Cleanup(record); err != nil {
		return err
	}
	return controller.Ownership.Remove(record)
}

func (controller *Controller) ReconcileTerminations(ctx context.Context, request ReconcileRequest) (int, error) {
	if controller == nil || controller.Recovery == nil || controller.Ownership == nil || request.HostID == "" || len(request.HostControlHash) != 32 {
		return 0, ErrConfiguration
	}
	completed := 0
	records, scanErr := controller.Ownership.Scan()
	if scanErr != nil {
		return 0, scanErr
	}
	recordIndex := make(map[string]firecracker.OwnershipRecord, len(records))
	for _, record := range records {
		recordIndex[record.SessionID] = record
	}
	var after string
	for {
		inventory, err := controller.Recovery.ListHostMachines(ctx, request.HostID, request.HostControlHash, after, 5000)
		if err != nil {
			return completed, err
		}
		for _, state := range inventory {
			if state.SessionStatus != "termination_requested" {
				continue
			}
			active := controller.take(state.SessionID)
			if active == nil {
				record, exists := recordIndex[state.SessionID]
				if !exists || recoveryIdentity(record) != state.RecoveryIdentity {
					continue
				}
				cleaned, cleanupErr := controller.Ownership.CleanupPrepared(record)
				if cleanupErr != nil {
					return completed, cleanupErr
				}
				if !cleaned {
					continue
				}
				if err = controller.finishRecoveredTermination(ctx, record, state, deadMachine{identity: record.Process}, request.HostControlHash); err != nil {
					return completed, err
				}
				completed++
				continue
			}
			active.mu.Lock()
			active.closing = true
			active.mu.Unlock()
			if recoveryIdentity(active.owner) != state.RecoveryIdentity {
				return completed, firecracker.ErrOwnershipIntegrity
			}
			if err = controller.finishRecoveredTermination(ctx, active.owner, state, active.process, request.HostControlHash); err != nil {
				if controller.Ownership.Verify(active.owner) == nil {
					active.mu.Lock()
					active.closing = false
					active.mu.Unlock()
					controller.remember(state.SessionID, active)
				}
				return completed, err
			}
			completed++
		}
		if len(inventory) < 5000 {
			break
		}
		after = inventory[len(inventory)-1].SessionID
	}
	return completed, nil
}

func (controller *Controller) Drain(ctx context.Context, reason string) (int, error) {
	if controller == nil || reason == "" {
		return 0, ErrConfiguration
	}
	controller.mu.Lock()
	sessions := make([]string, 0, len(controller.active))
	for sessionID := range controller.active {
		sessions = append(sessions, sessionID)
	}
	controller.mu.Unlock()
	completed := 0
	for _, sessionID := range sessions {
		if _, err := controller.Terminate(ctx, sessionID, reason); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}

func (controller *Controller) recoveredMachine(record firecracker.OwnershipRecord, state runtimepostgres.RecoveryState, process machineProcess, hostControlHash []byte) *activeMachine {
	actor := json.RawMessage(`{"kind":"system","component":"runtime-host-agent"}`)
	return &activeMachine{
		process: process,
		staged:  stagedMachine{root: record.Root, cleanup: func() error { return controller.Ownership.Cleanup(record) }},
		request: ProvisionRequest{Command: runtimepostgres.ProvisionCommand{TenantID: record.TenantID, HostID: record.HostID, MachineID: record.MachineID, GuestCID: record.GuestCID, Actor: actor, CorrelationID: record.SessionID}},
		result:  runtimepostgres.ProvisionResult{TenantID: record.TenantID, SessionID: record.SessionID, AllocationID: record.AllocationID, ProvisionAttemptID: record.ProvisionAttemptID, MachineID: record.MachineID, GuestCID: record.GuestCID, Version: state.SessionVersion},
		version: state.SessionVersion, owner: record, recovered: true, hostControlHash: append([]byte(nil), hostControlHash...),
	}
}

func (controller *Controller) finishRecoveredTermination(ctx context.Context, record firecracker.OwnershipRecord, state runtimepostgres.RecoveryState, process machineProcess, hostControlHash []byte) error {
	stopErr := process.Stop(ctx)
	if stopErr != nil && !errors.Is(stopErr, firecracker.ErrVMMExited) {
		return stopErr
	}
	at := controller.now()
	pointer, cleanupHash, err := controller.putReceipt(ctx, record.SessionID+":recovered-terminated", record.TenantID, controlReceipt{SchemaVersion: 1, SessionID: record.SessionID, Reason: "recovered_termination", OccurredAt: at})
	if err != nil {
		return err
	}
	if err = controller.Ownership.Cleanup(record); err != nil {
		return err
	}
	_, err = controller.Recovery.CompleteRecoveryTermination(ctx, runtimepostgres.RecoveryTerminatedCommand{LifecycleCommand: runtimepostgres.LifecycleCommand{
		TenantID: record.TenantID, SessionID: record.SessionID, ExpectedVersion: state.SessionVersion, ObservedAt: at,
		Payload: pointer, Actor: json.RawMessage(`{"kind":"system","component":"runtime-host-agent"}`), CorrelationID: record.SessionID,
	}, Identity: recoveryIdentity(record), HostControlHash: hostControlHash, CleanupReceiptHash: cleanupHash, UsageManifest: json.RawMessage(`{"schema_version":1,"vcpu_millis":0}`)})
	if err != nil {
		return err
	}
	return controller.Ownership.Remove(record)
}

func recoveryIdentity(record firecracker.OwnershipRecord) runtimepostgres.RecoveryIdentity {
	return runtimepostgres.RecoveryIdentity{TenantID: record.TenantID, SessionID: record.SessionID, AllocationID: record.AllocationID, ProvisionAttemptID: record.ProvisionAttemptID, HostID: record.HostID, MachineID: record.MachineID, GuestCID: record.GuestCID}
}

type deadMachine struct{ identity firecracker.ProcessIdentity }

func (machine deadMachine) Stop(context.Context) error { return firecracker.ErrVMMExited }
func (machine deadMachine) Done() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
func (machine deadMachine) ExitError() error { return firecracker.ErrVMMExited }
func (machine deadMachine) Identity() (firecracker.ProcessIdentity, error) {
	return machine.identity, nil
}

func (controller *Controller) abortProvision(ctx context.Context, request ProvisionRequest, provision runtimepostgres.ProvisionResult, process machineProcess, staged stagedMachine, owner *firecracker.OwnershipRecord, reason string) error {
	at := controller.now()
	pointer, _, err := controller.putReceipt(ctx, provision.SessionID+":abort", request.Command.TenantID, controlReceipt{SchemaVersion: 1, SessionID: provision.SessionID, Reason: reason, OccurredAt: at})
	if err != nil {
		return err
	}
	requested, err := controller.Store.RequestTermination(ctx, runtimepostgres.TerminationCommand{LifecycleCommand: runtimepostgres.LifecycleCommand{
		TenantID: request.Command.TenantID, SessionID: provision.SessionID, ProvisionAttemptID: provision.ProvisionAttemptID,
		ProvisionFence: 1, ExpectedVersion: 2, LeaseToken: request.Command.ProvisionLease, ObservedAt: at,
		Payload: pointer, Actor: request.Command.Actor, CorrelationID: request.Command.CorrelationID,
	}, Reason: reason})
	if err != nil {
		return err
	}
	var stopErr, ownerErr, cleanupErr error
	if process != nil {
		stopErr = process.Stop(ctx)
	}
	stopped := process == nil || stopErr == nil || errors.Is(stopErr, firecracker.ErrVMMExited)
	if owner != nil && stopped {
		ownerErr = controller.Ownership.Verify(*owner)
	}
	completedAt := at.Add(time.Microsecond)
	completePointer, cleanupHash, receiptErr := controller.putReceipt(ctx, provision.SessionID+":aborted", request.Command.TenantID, controlReceipt{SchemaVersion: 1, SessionID: provision.SessionID, Reason: reason, OccurredAt: completedAt})
	if receiptErr != nil {
		return errors.Join(stopErr, ownerErr, receiptErr)
	}
	if stopped && ownerErr == nil {
		if owner != nil {
			cleanupErr = controller.Ownership.Cleanup(*owner)
		} else if staged.cleanup != nil {
			cleanupErr = staged.cleanup()
		}
	}
	if !stopped || ownerErr != nil || cleanupErr != nil {
		return errors.Join(stopErr, ownerErr, cleanupErr)
	}
	_, completeErr := controller.Store.CompleteTermination(ctx, runtimepostgres.TerminatedCommand{LifecycleCommand: runtimepostgres.LifecycleCommand{
		TenantID: request.Command.TenantID, SessionID: provision.SessionID, ProvisionAttemptID: provision.ProvisionAttemptID,
		ProvisionFence: 1, ExpectedVersion: requested.Version, LeaseToken: request.Command.ProvisionLease, ObservedAt: completedAt,
		Payload: completePointer, Actor: request.Command.Actor, CorrelationID: request.Command.CorrelationID,
	}, CleanupReceiptHash: cleanupHash, UsageManifest: json.RawMessage(`{"schema_version":1,"vcpu_millis":0}`)})
	if completeErr == nil && owner != nil {
		completeErr = controller.Ownership.Remove(*owner)
	}
	return errors.Join(stopErr, ownerErr, cleanupErr, completeErr)
}

func (controller *Controller) stageMachine(ctx context.Context, request firecracker.StageRequest) (stagedMachine, error) {
	if controller.stage != nil {
		return controller.stage(ctx, request)
	}
	machine, err := controller.Stager.Stage(ctx, request)
	if err != nil {
		return stagedMachine{}, err
	}
	return stagedMachine{spec: machine.Spec, root: machine.Root, vsockPath: filepath.Join(machine.Root, "run", "guest.vsock"), cleanup: machine.Cleanup}, nil
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
