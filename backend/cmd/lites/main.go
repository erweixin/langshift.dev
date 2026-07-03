package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"lites/backend/internal/api"
	"lites/backend/internal/config"
	"lites/backend/internal/db"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if err := run(ctx, os.Args[1:]); err != nil {
		logger.Error("lites exited", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	appConfig, err := config.Load()
	if err != nil {
		return err
	}

	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}

	switch command {
	case "serve":
		server := api.NewServer(api.ServerConfig{
			Addr: appConfig.HTTPAddr,
		})
		return server.ListenAndServe(ctx)
	case "migrate":
		return db.RunMigrations(appConfig.DatabaseURL, appConfig.MigrationsDir)
	case "help", "-h", "--help":
		fmt.Println("usage: lites [serve|migrate]")
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}
