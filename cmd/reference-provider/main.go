package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/langshift/lites/internal/capacity/referenceprovider"
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
		logger.Error("reference provider stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, configuration config, logger *slog.Logger) error {
	token, err := readSecret(configuration.bearerTokenFile, 16<<10)
	if err != nil {
		return errors.New("load reference provider bearer token")
	}
	handler, err := referenceprovider.New(referenceprovider.Config{
		Model: configuration.model, BearerToken: token, LocalHold: configuration.localHold, ReplayHold: configuration.replayHold,
		RuntimeArtifactBytes: configuration.runtimeArtifactBytes, RuntimeHoldMilliseconds: configuration.runtimeHoldMilliseconds,
		MaximumRequestBytes: configuration.maximumRequestBytes, MaximumConcurrentRequests: configuration.maximumConcurrentRequests,
		ReplayRateNumerator: configuration.replayRateNumerator,
	})
	if err != nil {
		return err
	}
	ready := &atomic.Bool{}
	ready.Store(true)
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/livez", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	healthMux.HandleFunc("/readyz", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !ready.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	api := &http.Server{Addr: configuration.serverAddress, Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
	health := &http.Server{Addr: configuration.healthAddress, Handler: healthMux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	errorsChannel := make(chan error, 2)
	go func() {
		errorsChannel <- api.ListenAndServeTLS(configuration.tlsCertificateFile, configuration.tlsKeyFile)
	}()
	go func() { errorsChannel <- health.ListenAndServe() }()
	logger.Info("reference provider ready", "model", configuration.model, "environment", configuration.environment, "region", configuration.region, "version", configuration.version)
	var runErr error
	select {
	case <-parent.Done():
	case runErr = <-errorsChannel:
		if errors.Is(runErr, http.ErrServerClosed) {
			runErr = nil
		}
	}
	ready.Store(false)
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = api.Shutdown(shutdown); runErr == nil && err != nil {
		runErr = err
	}
	if err = health.Shutdown(shutdown); runErr == nil && err != nil {
		runErr = err
	}
	return runErr
}

func readSecret(path string, maximum int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximum {
		return "", errors.New("secret file is invalid")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSuffix(string(encoded), "\n")
	if len(value) < 32 || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("secret value is invalid")
	}
	return value, nil
}
