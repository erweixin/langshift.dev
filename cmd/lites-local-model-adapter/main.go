// lites-local-model-adapter serves deterministic fixture outputs for the real
// HTTP/database macOS gate. It is intentionally unavailable outside the exact
// engineering-test environment.
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/langshift/lites/internal/localmodeladapter"
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
		logger.Error("local model adapter stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configuration config, logger *slog.Logger) error {
	token, err := readSecret(configuration.tokenFile, 16<<10)
	if err != nil {
		return err
	}
	handler, err := localmodeladapter.New(localmodeladapter.Config{Model: configuration.model, BearerToken: token, MaximumRequestBytes: configuration.maximumRequestBytes})
	if err != nil {
		return err
	}
	server := &http.Server{Addr: configuration.address, Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	errorsChannel := make(chan error, 1)
	go func() {
		serveErr := server.ListenAndServeTLS(configuration.certificateFile, configuration.keyFile)
		if !errors.Is(serveErr, http.ErrServerClosed) {
			errorsChannel <- serveErr
		}
	}()
	logger.Info("local model adapter ready", "model", configuration.model, "fixture_validated", true, "production_claim", false)
	select {
	case <-ctx.Done():
	case err = <-errorsChannel:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	return err
}

func readSecret(path string, maximum int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	value := strings.TrimSpace(string(contents))
	if err != nil || int64(len(contents)) > maximum || len(value) < 32 || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("model adapter token is invalid")
	}
	return value, nil
}
