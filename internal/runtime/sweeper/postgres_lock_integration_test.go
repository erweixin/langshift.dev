//go:build integration

package sweeper

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresLockerExcludesConcurrentTenantOwnersAndReleasesSessionLock(t *testing.T) {
	ctx := context.Background()
	databaseURL := os.Getenv("LITES_TEST_RUNTIME_SWEEPER_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("LITES_TEST_RUNTIME_SWEEPER_DATABASE_URL is required")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var canRegisterHost bool
	if err = pool.QueryRow(ctx, `SELECT has_function_privilege(current_user,'agent.runtime_register_host(text,text,text,text,text,text,text,bytea,integer,integer,integer,integer,timestamptz,timestamptz)','EXECUTE')`).Scan(&canRegisterHost); err != nil {
		t.Fatal(err)
	}
	if canRegisterHost {
		t.Fatal("runtime sweeper can control host registration")
	}
	locker := PostgresLocker{Pool: pool, UnlockTimeout: time.Second}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		locked, lockErr := locker.WithTenantLock(ctx, "20000000-0000-0000-0000-000000009299", func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
		if lockErr == nil && !locked {
			lockErr = ErrConfiguration
		}
		done <- lockErr
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first tenant lock was not acquired")
	}
	locked, err := locker.WithTenantLock(ctx, "20000000-0000-0000-0000-000000009299", func(context.Context) error {
		t.Fatal("contending operation executed")
		return nil
	})
	if err != nil || locked {
		t.Fatalf("contending lock=%v err=%v", locked, err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	locked, err = locker.WithTenantLock(ctx, "20000000-0000-0000-0000-000000009299", func(context.Context) error { return nil })
	if err != nil || !locked {
		t.Fatalf("released lock=%v err=%v", locked, err)
	}
}
