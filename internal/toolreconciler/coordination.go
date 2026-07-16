package toolreconciler

import (
	"context"
	"time"

	"github.com/langshift/lites/internal/payload"
)

type ShardLocker interface {
	WithShardLock(context.Context, int, int, func(context.Context) error) (bool, error)
}

type Coordinator struct {
	Store    Store
	Payloads payload.Store
	Locker   interface {
		TenantLocker
		ShardLocker
	}
	IDKey          []byte
	StoreEpoch     string
	Now            func() time.Time
	ReconcileDelay time.Duration
	Reconcile      Schedule
	MaximumCommand int
	ShardCount     int
	TenantPage     int
	EffectPage     int
	Metrics        ReconciliationMetrics
}

type CoordinationResult struct {
	Shards, Contended int
	Sweep             Result
}

func (coordinator Coordinator) RunOnce(ctx context.Context) (CoordinationResult, error) {
	if coordinator.Store == nil || coordinator.Payloads == nil || coordinator.Locker == nil || coordinator.ShardCount < 1 || coordinator.ShardCount > 256 {
		return CoordinationResult{}, ErrConfiguration
	}
	result := CoordinationResult{}
	for shard := 0; shard < coordinator.ShardCount; shard++ {
		index := shard
		locked, err := coordinator.Locker.WithShardLock(ctx, index, coordinator.ShardCount, func(lockCtx context.Context) error {
			current, sweepErr := (Sweeper{Store: coordinator.Store, Payloads: coordinator.Payloads, Locker: coordinator.Locker, IDKey: coordinator.IDKey, StoreEpoch: coordinator.StoreEpoch, Now: coordinator.Now, ReconcileDelay: coordinator.ReconcileDelay, Reconcile: coordinator.Reconcile, MaximumCommand: coordinator.MaximumCommand, ShardIndex: index, ShardCount: coordinator.ShardCount, TenantPage: coordinator.TenantPage, EffectPage: coordinator.EffectPage, Metrics: coordinator.Metrics}).RunOnce(lockCtx)
			result.Shards++
			result.Sweep.Tenants += current.Tenants
			result.Sweep.Swept += current.Swept
			result.Sweep.Contended += current.Contended
			result.Sweep.ManualReviewRequired += current.ManualReviewRequired
			return sweepErr
		})
		if err != nil {
			return result, err
		}
		if !locked {
			result.Contended++
		}
		if err = ctx.Err(); err != nil {
			return result, err
		}
	}
	return result, nil
}
