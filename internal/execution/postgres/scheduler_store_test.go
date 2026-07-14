package postgres

import (
	"errors"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/scheduler"
)

func TestSchedulerStoreFailsClosedBeforeDatabaseAccess(t *testing.T) {
	config := scheduler.Config{Resources: map[string]scheduler.ResourcePolicy{"llm": {Capacity: 1, HighPriorityMaxPercentage: 100, QuantumUnits: 1}}, DefaultTenant: scheduler.TenantPolicy{Weight: 1, ActiveConcurrencyCap: 1, BurstUnits: 1, RefillUnitsPerSecond: 1}, Tenants: map[string]scheduler.TenantPolicy{}, BatchLimit: 1}
	if _, err := (SchedulerStore{}).PlanResource(t.Context(), config, scheduler.Active{}, "llm", "scheduler", "epoch", 1); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("invalid store: %v", err)
	}
	claim := DispatchClaim{Candidate: SchedulerCandidate{Job: scheduler.Job{ID: "job", TenantID: "tenant-a", ResourceClass: "llm"}, Command: eventpostgres.PublishedCommand{OutboxID: "outbox", TenantID: "tenant-b", CommandID: "command", CommandType: "StartAgentRun", AggregateKind: "run", AggregateID: "run", StoreEpoch: "epoch", PayloadRef: "encrypted://command", PayloadHash: "hash"}}, LeaseToken: "token", LeaseExpiresAt: time.Now().Add(time.Minute)}
	if validDispatchClaim(claim) {
		t.Fatal("cross-tenant dispatch claim accepted")
	}
}
