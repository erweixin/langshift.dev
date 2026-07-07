package testsupport

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"lites/backend/internal/db"
)

func NewMigratedPool(t testing.TB, ctx context.Context, schemaPrefix string) *pgxpool.Pool {
	t.Helper()

	rawURL := os.Getenv("LITES_TEST_DATABASE_URL")
	if rawURL == "" {
		t.Skip("set LITES_TEST_DATABASE_URL to run integration tests")
	}

	admin, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatalf("connect admin database: %v", err)
	}

	schema := fmt.Sprintf("%s_%d", schemaPrefix, time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatalf("create test schema: %v", err)
	}

	testURL, err := SearchPathURL(rawURL, schema)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
		t.Fatalf("build test database url: %v", err)
	}
	if err := db.RunMigrations(testURL, MigrationsDir(t)); err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
		t.Fatalf("run migrations: %v", err)
	}

	pool, err := db.NewPool(ctx, testURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
		t.Fatalf("connect test database: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
	})

	return pool
}

func SearchPathURL(rawURL string, schema string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func MigrationsDir(t testing.TB) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve caller path")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..", "migrations")
}
