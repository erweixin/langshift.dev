package migrations

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const advisoryLockID int64 = 0x4c495445534d4947

type Runner struct {
	Connection *pgx.Conn
	Manifest   Manifest
	Now        func() time.Time
}

type Result struct {
	Direction       string  `json:"direction"`
	PreviousVersion int64   `json:"previous_version"`
	CurrentVersion  int64   `json:"current_version"`
	Applied         []int64 `json:"applied,omitempty"`
	RolledBack      []int64 `json:"rolled_back,omitempty"`
}

type appliedMigration struct {
	Version  int64
	Name     string
	UpSHA256 string
}

func (runner Runner) Run(ctx context.Context, direction string, steps int) (Result, error) {
	if runner.Connection == nil || len(runner.Manifest.Migrations) == 0 || (direction != "up" && direction != "down" && direction != "status") || steps < 0 || (direction == "down" && steps == 0) {
		return Result{}, ErrInvalidCommand
	}
	if _, err := runner.Connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockID); err != nil {
		return Result{}, err
	}
	defer func() {
		_, _ = runner.Connection.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, advisoryLockID)
	}()
	if err := runner.ensureHistory(ctx); err != nil {
		return Result{}, err
	}
	applied, err := runner.readAndVerifyState(ctx)
	if err != nil {
		return Result{}, err
	}
	previous := int64(len(applied))
	result := Result{Direction: direction, PreviousVersion: previous, CurrentVersion: previous}
	switch direction {
	case "status":
		return result, nil
	case "up":
		remaining := len(runner.Manifest.Migrations) - len(applied)
		if steps == 0 || steps > remaining {
			steps = remaining
		}
		for index := len(applied); index < len(applied)+steps; index++ {
			migration := runner.Manifest.Migrations[index]
			if err = runner.apply(ctx, migration); err != nil {
				return result, fmt.Errorf("apply migration %d: %w", migration.Version, err)
			}
			result.Applied = append(result.Applied, migration.Version)
			result.CurrentVersion = migration.Version
		}
		return result, nil
	case "down":
		plan, planErr := rollbackPlan(runner.Manifest.Migrations, len(applied), steps)
		if planErr != nil {
			return result, planErr
		}
		for _, migration := range plan {
			if err = runner.rollback(ctx, migration); err != nil {
				return result, fmt.Errorf("rollback migration %d: %w", migration.Version, err)
			}
			result.RolledBack = append(result.RolledBack, migration.Version)
			result.CurrentVersion = migration.Version - 1
		}
		return result, nil
	default:
		return Result{}, ErrInvalidCommand
	}
}

func rollbackPlan(migrations []Migration, applied, steps int) ([]Migration, error) {
	if applied < 0 || applied > len(migrations) || steps < 1 {
		return nil, ErrInvalidCommand
	}
	if steps > applied {
		steps = applied
	}
	plan := make([]Migration, 0, steps)
	for index := applied - 1; index >= applied-steps; index-- {
		migration := migrations[index]
		if !migration.Reversible {
			return nil, fmt.Errorf("migration %d: %w", migration.Version, ErrIrreversible)
		}
		plan = append(plan, migration)
	}
	return plan, nil
}

func (runner Runner) ensureHistory(ctx context.Context) error {
	_, err := runner.Connection.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.lites_schema_migrations (version bigint PRIMARY KEY CHECK (version > 0), name text NOT NULL, up_sha256 text NOT NULL CHECK (up_sha256 ~ '^[0-9a-f]{64}$'), down_sha256 text, applied_at timestamptz NOT NULL, execution_milliseconds bigint NOT NULL CHECK (execution_milliseconds >= 0))`)
	return err
}

func (runner Runner) readAndVerifyState(ctx context.Context) ([]appliedMigration, error) {
	rows, err := runner.Connection.Query(ctx, `SELECT version,name,up_sha256 FROM public.lites_schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var applied []appliedMigration
	for rows.Next() {
		var record appliedMigration
		if err = rows.Scan(&record.Version, &record.Name, &record.UpSHA256); err != nil {
			return nil, err
		}
		applied = append(applied, record)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(applied) > len(runner.Manifest.Migrations) {
		return nil, ErrState
	}
	for index, record := range applied {
		expected := runner.Manifest.Migrations[index]
		if record.Version != int64(index+1) || record.Name != expected.Name || record.UpSHA256 != expected.UpSHA256 {
			return nil, ErrState
		}
	}
	if len(applied) == 0 {
		var exists bool
		if err = runner.Connection.QueryRow(ctx, `SELECT to_regnamespace('identity') IS NOT NULL OR to_regnamespace('agent') IS NOT NULL OR to_regnamespace('product') IS NOT NULL OR to_regnamespace('contracts') IS NOT NULL`).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			return nil, ErrUntracked
		}
	}
	return applied, nil
}

func (runner Runner) apply(ctx context.Context, migration Migration) error {
	started := time.Now()
	tx, err := runner.Connection.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, migration.upSQL); err != nil {
		return err
	}
	appliedAt := time.Now().UTC()
	if runner.Now != nil {
		appliedAt = runner.Now().UTC()
	}
	elapsed := time.Since(started).Milliseconds()
	if _, err = tx.Exec(ctx, `INSERT INTO public.lites_schema_migrations(version,name,up_sha256,down_sha256,applied_at,execution_milliseconds) VALUES($1,$2,$3,$4,$5,$6)`, migration.Version, migration.Name, migration.UpSHA256, nullableDigest(migration.DownSHA256), appliedAt, elapsed); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (runner Runner) rollback(ctx context.Context, migration Migration) error {
	tx, err := runner.Connection.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, migration.downSQL); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `DELETE FROM public.lites_schema_migrations WHERE version=$1 AND up_sha256=$2`, migration.Version, migration.UpSHA256)
	if err != nil || command.RowsAffected() != 1 {
		if err != nil {
			return err
		}
		return ErrState
	}
	return tx.Commit(ctx)
}

func nullableDigest(value string) any {
	if value == "" {
		return nil
	}
	return value
}
