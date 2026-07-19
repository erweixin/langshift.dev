// lites-macos-test-adapter provisions deterministic behavior bindings for the
// real HTTP/database macOS gate. It is deliberately unable to start outside
// the explicit engineering-test environment and makes no production claim.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/localtestadapter"
	"github.com/langshift/lites/internal/product/contentcatalog"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	configuration, err := loadConfig()
	if err == nil {
		err = run(ctx, configuration, logger)
	}
	if err != nil {
		logger.Error("macOS test adapter stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configuration config, logger *slog.Logger) error {
	pool, err := pgxpool.New(ctx, configuration.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		return err
	}
	release, err := contentcatalog.Load(configuration.contentReleaseDir)
	if err != nil {
		return err
	}
	ready := &atomic.Bool{}
	health := &http.Server{Addr: configuration.healthAddress, Handler: healthHandler(ready), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
	errorsChannel := make(chan error, 2)
	go func() {
		if serveErr := health.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errorsChannel <- serveErr
		}
	}()
	go func() { errorsChannel <- provisionLoop(ctx, pool, release, configuration, ready, logger) }()
	logger.Info("macOS test adapter ready", "production_claim", false, "environment", configuration.environment)
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errorsChannel:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = health.Shutdown(shutdown)
	return runErr
}

func provisionLoop(ctx context.Context, pool *pgxpool.Pool, release contentcatalog.Release, configuration config, ready *atomic.Bool, logger *slog.Logger) error {
	ticker := time.NewTicker(configuration.pollInterval)
	defer ticker.Stop()
	for {
		if err := provisionOnce(ctx, pool, release, configuration, logger); err != nil {
			ready.Store(false)
			logger.Warn("engineering behavior projection failed", "error", err)
		} else {
			ready.Store(true)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func provisionOnce(ctx context.Context, pool *pgxpool.Pool, release contentcatalog.Release, configuration config, logger *slog.Logger) error {
	if err := localtestadapter.EnsurePublicContent(ctx, pool, release, configuration.publicTenantID); err != nil {
		return err
	}
	after, provisioned := "", 0
	for {
		principals, err := localtestadapter.ListTenantPrincipals(ctx, pool, after, 200)
		if err != nil {
			return err
		}
		for _, principal := range principals {
			if err = localtestadapter.EnsureAllBehaviorChannels(ctx, pool, principal, configuration.behaviorEnvironment, configuration.storeEpoch, time.Now().UTC()); err != nil {
				return err
			}
			after = principal.TenantID
			provisioned++
		}
		if len(principals) < 200 {
			break
		}
	}
	logger.Debug("engineering behavior projection complete", "tenants", provisioned)
	return nil
}

func healthHandler(ready *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /live", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /ready", func(writer http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return mux
}
