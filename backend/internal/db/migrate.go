package db

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func RunMigrations(databaseURL string, migrationsDir string) error {
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if migrationsDir == "" {
		migrationsDir = detectMigrationsDir()
	}

	sqlDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer sqlDB.Close()

	if err := sqlDB.Ping(); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	goose.SetDialect("postgres")
	if err := goose.Up(sqlDB, migrationsDir); err != nil {
		return fmt.Errorf("run migrations from %s: %w", migrationsDir, err)
	}
	return nil
}

func detectMigrationsDir() string {
	candidates := []string{"migrations", "backend/migrations"}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return "migrations"
}
