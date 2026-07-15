package sweeper

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/langshift/lites/internal/payload"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
)

type storeStub struct {
	states   []runtimepostgres.RecoveryState
	commands []runtimepostgres.RecoveryTerminationCommand
}

func (store *storeStub) ListRecoveryTenants(context.Context, string, int, int, int, time.Time) ([]string, error) {
	return []string{"tenant-1"}, nil
}
func (store *storeStub) ListDueSessions(_ context.Context, tenant, after string, _ int, _ time.Time) ([]runtimepostgres.RecoveryState, error) {
	var result []runtimepostgres.RecoveryState
	for _, state := range store.states {
		if state.TenantID == tenant && state.SessionID > after {
			result = append(result, state)
		}
	}
	return result, nil
}
func (store *storeStub) RequestRecoveryTermination(_ context.Context, command runtimepostgres.RecoveryTerminationCommand) (runtimepostgres.LifecycleResult, error) {
	store.commands = append(store.commands, command)
	return runtimepostgres.LifecycleResult{Status: "termination_requested", Version: command.ExpectedVersion + 1}, nil
}

type payloadStub struct{}

func (payloadStub) Put(_ context.Context, descriptor payload.Descriptor, value []byte) (payload.Manifest, error) {
	return payload.Manifest{Ref: "encrypted://" + descriptor.ObjectID, Hash: "envelope-hash"}, nil
}
func (payloadStub) Get(context.Context, payload.Descriptor, payload.Manifest) ([]byte, error) {
	return nil, nil
}

func TestSweeperUsesStableDurableDeadlineAndNeverCompletesCleanup(t *testing.T) {
	at := time.Date(2026, 7, 15, 21, 0, 0, 0, time.UTC)
	deadline := at.Add(-time.Second)
	store := &storeStub{states: []runtimepostgres.RecoveryState{{
		RecoveryIdentity: runtimepostgres.RecoveryIdentity{TenantID: "tenant-1", SessionID: "session-1", AllocationID: "allocation-1", ProvisionAttemptID: "attempt-1", HostID: "host-1", MachineID: "machine-1", GuestCID: 42},
		SessionVersion:   3, AllocationVersion: 2, ProvisionFence: 1, SessionStatus: "ready", AllocationStatus: "active",
		IdleDeadline: sql.NullTime{Time: deadline, Valid: true}, ExecutionDeadline: at.Add(time.Hour),
	}}}
	sweeper := Sweeper{Store: store, Payloads: payloadStub{}, Now: func() time.Time { return at }, ShardCount: 1, TenantPage: 100, SessionPage: 100}
	result, err := sweeper.RunOnce(context.Background())
	if err != nil || result.Tenants != 1 || result.Requested != 1 || len(store.commands) != 1 {
		t.Fatalf("RunOnce()=%#v err=%v commands=%d", result, err, len(store.commands))
	}
	command := store.commands[0]
	if command.Authority != runtimepostgres.RecoveryDeadline || !command.ObservedAt.Equal(deadline) || command.Reason != "idle_deadline_exceeded" || len(command.HostControlHash) != 0 {
		t.Fatalf("deadline command=%#v", command)
	}
}
