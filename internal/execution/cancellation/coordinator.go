// Package cancellation coordinates durable Run cancellation recovery across
// replicas. It owns only scheduling and isolation; business transitions remain
// in the PostgreSQL execution, LLM and Runtime protocols.
package cancellation

import (
	"context"
	"errors"

	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
)

var ErrConfiguration = errors.New("run cancellation coordinator configuration is invalid")

type Reconciler interface {
	ListDueTenantIDs(context.Context, string, string, int, int, int) ([]string, error)
	ListDueCancellationIDs(context.Context, string, string, string, int) ([]string, error)
	ReconcileDueCancellation(context.Context, string, string, string) (executionpostgres.ReconciledRunCancellation, error)
	ListDueChildGroupCancellationIDs(context.Context, string, string, string, int) ([]string, error)
	ReconcileDueChildGroupCancellation(context.Context, string, string, string) (executionpostgres.CancelledChildGroupRemainder, error)
}

type Locker interface {
	WithShardLock(context.Context, int, int, func(context.Context) error) (bool, error)
	WithTenantLock(context.Context, string, func(context.Context) error) (bool, error)
}

type Coordinator struct {
	Reconciler       Reconciler
	Locker           Locker
	StoreEpoch       string
	ShardCount       int
	TenantPage       int
	CancellationPage int
}

type Result struct {
	Shards, ShardContended    int
	Tenants, TenantContended  int
	Cancellations             int
	ChildGroupCancellations   int
	Settled, Deferred, Failed int
}

func (coordinator Coordinator) RunOnce(ctx context.Context) (Result, error) {
	if coordinator.Reconciler == nil || coordinator.Locker == nil || coordinator.StoreEpoch == "" || coordinator.ShardCount < 1 || coordinator.ShardCount > 256 || coordinator.TenantPage < 1 || coordinator.TenantPage > 5000 || coordinator.CancellationPage < 1 || coordinator.CancellationPage > 500 {
		return Result{}, ErrConfiguration
	}
	var result Result
	var failures error
	for shardIndex := 0; shardIndex < coordinator.ShardCount; shardIndex++ {
		index := shardIndex
		locked, err := coordinator.Locker.WithShardLock(ctx, index, coordinator.ShardCount, func(lockCtx context.Context) error {
			result.Shards++
			return coordinator.runShard(lockCtx, index, &result)
		})
		if err != nil {
			failures = errors.Join(failures, err)
		}
		if !locked {
			result.ShardContended++
		}
		if err = ctx.Err(); err != nil {
			return result, errors.Join(failures, err)
		}
	}
	return result, failures
}

func (coordinator Coordinator) runShard(ctx context.Context, shardIndex int, result *Result) error {
	var tenantCursor string
	var failures error
	for {
		tenants, err := coordinator.Reconciler.ListDueTenantIDs(ctx, coordinator.StoreEpoch, tenantCursor, coordinator.TenantPage, shardIndex, coordinator.ShardCount)
		if err != nil {
			return errors.Join(failures, err)
		}
		for _, tenantID := range tenants {
			tenantCursor = tenantID
			locked, lockErr := coordinator.Locker.WithTenantLock(ctx, tenantID, func(lockCtx context.Context) error {
				result.Tenants++
				return coordinator.runTenant(lockCtx, tenantID, result)
			})
			if lockErr != nil {
				failures = errors.Join(failures, lockErr)
			}
			if !locked {
				result.TenantContended++
			}
		}
		if len(tenants) < coordinator.TenantPage {
			return failures
		}
	}
}

func (coordinator Coordinator) runTenant(ctx context.Context, tenantID string, result *Result) error {
	var cancellationCursor string
	var failures error
	for {
		cancellations, err := coordinator.Reconciler.ListDueCancellationIDs(ctx, tenantID, coordinator.StoreEpoch, cancellationCursor, coordinator.CancellationPage)
		if err != nil {
			return errors.Join(failures, err)
		}
		for _, cancellationID := range cancellations {
			cancellationCursor = cancellationID
			result.Cancellations++
			reconciled, reconcileErr := coordinator.Reconciler.ReconcileDueCancellation(ctx, tenantID, cancellationID, coordinator.StoreEpoch)
			if reconcileErr != nil {
				result.Failed++
				failures = errors.Join(failures, reconcileErr)
				continue
			}
			if reconciled.Settled {
				result.Settled++
			} else {
				result.Deferred++
			}
		}
		if len(cancellations) < coordinator.CancellationPage {
			break
		}
	}
	var groupCursor string
	for {
		groups, err := coordinator.Reconciler.ListDueChildGroupCancellationIDs(ctx, tenantID, coordinator.StoreEpoch, groupCursor, coordinator.CancellationPage)
		if err != nil {
			return errors.Join(failures, err)
		}
		for _, cancellationID := range groups {
			groupCursor = cancellationID
			result.ChildGroupCancellations++
			reconciled, reconcileErr := coordinator.Reconciler.ReconcileDueChildGroupCancellation(ctx, tenantID, cancellationID, coordinator.StoreEpoch)
			if reconcileErr != nil {
				result.Failed++
				failures = errors.Join(failures, reconcileErr)
				continue
			}
			if reconciled.Complete {
				result.Settled++
			} else {
				result.Deferred++
			}
		}
		if len(groups) < coordinator.CancellationPage {
			return failures
		}
	}
}
