package sweeper

import (
	"context"

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
	ShardCount  int
	TenantPage  int
	SessionPage int
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
			current, sweepErr := (Sweeper{Store: coordinator.Store, Payloads: coordinator.Payloads, Locker: coordinator.Locker, ShardIndex: index, ShardCount: coordinator.ShardCount, TenantPage: coordinator.TenantPage, SessionPage: coordinator.SessionPage}).RunOnce(lockCtx)
			result.Shards++
			result.Sweep.Tenants += current.Tenants
			result.Sweep.Requested += current.Requested
			result.Sweep.Contended += current.Contended
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
