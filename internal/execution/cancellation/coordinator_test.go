package cancellation

import (
	"context"
	"errors"
	"testing"

	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
)

type reconcilerStub struct {
	tenants       map[int][]string
	cancellations map[string][]string
	childGroups   map[string][]string
	fail          map[string]error
	visited       []string
}

func (stub *reconcilerStub) ListDueTenantIDs(_ context.Context, _ string, after string, limit, shard, _ int) ([]string, error) {
	return pageAfter(stub.tenants[shard], after, limit), nil
}

func (stub *reconcilerStub) ListDueCancellationIDs(_ context.Context, tenant, _ string, after string, limit int) ([]string, error) {
	return pageAfter(stub.cancellations[tenant], after, limit), nil
}

func (stub *reconcilerStub) ReconcileDueCancellation(_ context.Context, tenant, cancellation, _ string) (executionpostgres.ReconciledRunCancellation, error) {
	key := tenant + "/" + cancellation
	stub.visited = append(stub.visited, key)
	if err := stub.fail[key]; err != nil {
		return executionpostgres.ReconciledRunCancellation{}, err
	}
	return executionpostgres.ReconciledRunCancellation{CancellationID: cancellation, Settled: cancellation != "deferred"}, nil
}

func (stub *reconcilerStub) ListDueChildGroupCancellationIDs(_ context.Context, tenant, _ string, after string, limit int) ([]string, error) {
	return pageAfter(stub.childGroups[tenant], after, limit), nil
}

func (stub *reconcilerStub) ReconcileDueChildGroupCancellation(_ context.Context, tenant, cancellation, _ string) (executionpostgres.CancelledChildGroupRemainder, error) {
	key := tenant + "/group/" + cancellation
	stub.visited = append(stub.visited, key)
	if err := stub.fail[key]; err != nil {
		return executionpostgres.CancelledChildGroupRemainder{}, err
	}
	return executionpostgres.CancelledChildGroupRemainder{CancellationID: cancellation, Complete: cancellation != "group-deferred"}, nil
}

type lockerStub struct {
	shardContended  map[int]bool
	tenantContended map[string]bool
}

func (stub lockerStub) WithShardLock(ctx context.Context, shard, _ int, operation func(context.Context) error) (bool, error) {
	if stub.shardContended[shard] {
		return false, nil
	}
	return true, operation(ctx)
}

func (stub lockerStub) WithTenantLock(ctx context.Context, tenant string, operation func(context.Context) error) (bool, error) {
	if stub.tenantContended[tenant] {
		return false, nil
	}
	return true, operation(ctx)
}

func TestCoordinatorPaginatesIsolatesFailuresAndAccountsContention(t *testing.T) {
	failure := errors.New("dependency unavailable")
	reconciler := &reconcilerStub{
		tenants: map[int][]string{0: {"tenant-a", "tenant-b"}, 1: {"tenant-c"}},
		cancellations: map[string][]string{
			"tenant-a": {"cancel-1", "cancel-2", "deferred"},
			"tenant-b": {"cancel-3"},
			"tenant-c": {"cancel-4"},
		},
		childGroups: map[string][]string{"tenant-a": {"group-1", "group-deferred"}},
		fail:        map[string]error{"tenant-a/cancel-2": failure},
	}
	coordinator := Coordinator{
		Reconciler: reconciler, Locker: lockerStub{shardContended: map[int]bool{1: true}, tenantContended: map[string]bool{"tenant-b": true}},
		StoreEpoch: "epoch", ShardCount: 2, TenantPage: 1, CancellationPage: 1,
	}
	result, err := coordinator.RunOnce(context.Background())
	if !errors.Is(err, failure) {
		t.Fatalf("RunOnce() error=%v", err)
	}
	if result.Shards != 1 || result.ShardContended != 1 || result.Tenants != 1 || result.TenantContended != 1 || result.Cancellations != 3 || result.ChildGroupCancellations != 2 || result.Settled != 2 || result.Deferred != 2 || result.Failed != 1 {
		t.Fatalf("RunOnce() result=%#v", result)
	}
	want := []string{"tenant-a/cancel-1", "tenant-a/cancel-2", "tenant-a/deferred", "tenant-a/group/group-1", "tenant-a/group/group-deferred"}
	if len(reconciler.visited) != len(want) {
		t.Fatalf("visited=%v want=%v", reconciler.visited, want)
	}
	for index := range want {
		if reconciler.visited[index] != want[index] {
			t.Fatalf("visited=%v want=%v", reconciler.visited, want)
		}
	}
}

func TestCoordinatorRejectsInvalidBounds(t *testing.T) {
	coordinator := Coordinator{Reconciler: &reconcilerStub{}, Locker: lockerStub{}, StoreEpoch: "epoch", ShardCount: 1, TenantPage: 1, CancellationPage: 0}
	if _, err := coordinator.RunOnce(context.Background()); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("invalid coordinator error=%v", err)
	}
	if locked, err := (PostgresLocker{}).WithShardLock(context.Background(), 1, 1, func(context.Context) error { return nil }); locked || !errors.Is(err, ErrConfiguration) {
		t.Fatalf("invalid shard lock=%v error=%v", locked, err)
	}
}

func pageAfter(values []string, after string, limit int) []string {
	start := 0
	for start < len(values) && values[start] <= after {
		start++
	}
	end := start + limit
	if end > len(values) {
		end = len(values)
	}
	return append([]string(nil), values[start:end]...)
}
