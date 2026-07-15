package cancellation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresLocker struct {
	Pool          *pgxpool.Pool
	UnlockTimeout time.Duration
}

func (locker PostgresLocker) WithTenantLock(ctx context.Context, tenantID string, operation func(context.Context) error) (bool, error) {
	if tenantID == "" {
		return false, ErrConfiguration
	}
	return locker.withLock(ctx, "tenant:"+tenantID, operation)
}

func (locker PostgresLocker) WithShardLock(ctx context.Context, shardIndex, shardCount int, operation func(context.Context) error) (bool, error) {
	if shardCount < 1 || shardCount > 256 || shardIndex < 0 || shardIndex >= shardCount {
		return false, ErrConfiguration
	}
	return locker.withLock(ctx, fmt.Sprintf("shard:%d:%d", shardCount, shardIndex), operation)
}

func (locker PostgresLocker) withLock(ctx context.Context, identity string, operation func(context.Context) error) (bool, error) {
	if locker.Pool == nil || operation == nil || locker.UnlockTimeout <= 0 || locker.UnlockTimeout > 10*time.Second {
		return false, ErrConfiguration
	}
	connection, err := locker.Pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	released := false
	defer func() {
		if !released {
			connection.Release()
		}
	}()
	key := "run-cancellation-reconciler:v1:" + identity
	var locked bool
	if err = connection.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&locked); err != nil || !locked {
		return locked, err
	}
	operationErr := operation(ctx)
	unlockCtx, cancel := context.WithTimeout(context.Background(), locker.UnlockTimeout)
	defer cancel()
	var unlocked bool
	unlockErr := connection.QueryRow(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key).Scan(&unlocked)
	if unlockErr != nil || !unlocked {
		raw := connection.Hijack()
		released = true
		closeCtx, closeCancel := context.WithTimeout(context.Background(), locker.UnlockTimeout)
		closeErr := raw.Close(closeCtx)
		closeCancel()
		return true, errors.Join(operationErr, unlockErr, closeErr, errors.New("run cancellation advisory lock release failed"))
	}
	return true, operationErr
}
