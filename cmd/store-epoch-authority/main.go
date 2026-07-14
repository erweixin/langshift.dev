package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/langshift/lites/internal/observability"
)

var storeEpochPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type authorityHandler struct {
	epochFile string
	tokenHash [sha256.Size]byte
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	configuration, err := loadConfig()
	if err != nil {
		logger.Error("configuration rejected", "error", err)
		os.Exit(2)
	}
	if err = run(ctx, configuration, logger); err != nil {
		logger.Error("store epoch authority stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, configuration config, logger *slog.Logger) error {
	bearerToken, err := readCredential(configuration.bearerTokenFile, 8192)
	if err != nil || len(bearerToken) < 32 {
		return errors.New("store epoch bearer token is unavailable or too short")
	}
	handler := authorityHandler{epochFile: configuration.epochFile, tokenHash: sha256.Sum256([]byte(bearerToken))}
	if _, err = handler.readEpoch(); err != nil {
		return err
	}
	tlsConfig, err := configuration.serverTLS()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	telemetry, err := observability.New(ctx, observability.Config{ServiceName: "store-epoch-authority", ServiceVersion: configuration.serviceVersion, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpBearerTokenFile, TraceSampleRatio: configuration.traceSampleRatio, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure observability")
	}
	application := telemetry.WrapHTTP(handler)
	server := &http.Server{Addr: configuration.listenAddress, Handler: application, TLSConfig: tlsConfig, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	health := handler.healthServer(configuration.healthAddress, telemetry.MetricsHandler())
	errChannel := make(chan error, 2)
	go func() {
		if serveErr := health.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errChannel <- serveErr
		}
	}()
	go func() {
		listener, listenErr := net.Listen("tcp", configuration.listenAddress)
		if listenErr != nil {
			errChannel <- listenErr
			return
		}
		var serveErr error
		if tlsConfig == nil {
			serveErr = server.Serve(listener)
		} else {
			serveErr = server.ServeTLS(listener, "", "")
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errChannel <- serveErr
		}
	}()
	logger.Info("store epoch authority ready", "address", configuration.listenAddress)
	var runErr error
	select {
	case <-parent.Done():
	case runErr = <-errChannel:
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	_ = health.Shutdown(shutdownCtx)
	if telemetryErr := telemetry.Shutdown(shutdownCtx); telemetryErr != nil && runErr == nil {
		runErr = telemetryErr
	}
	return runErr
}

func (handler authorityHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method != http.MethodGet || request.URL.Path != "/v1/store-epoch" || request.URL.RawQuery != "" {
		http.NotFound(writer, request)
		return
	}
	values := request.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		handler.unauthorized(writer)
		return
	}
	candidate := sha256.Sum256([]byte(strings.TrimPrefix(values[0], "Bearer ")))
	if subtle.ConstantTimeCompare(candidate[:], handler.tokenHash[:]) != 1 {
		handler.unauthorized(writer)
		return
	}
	storeEpoch, err := handler.readEpoch()
	if err != nil {
		http.Error(writer, "authority unavailable", http.StatusServiceUnavailable)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(struct {
		StoreEpoch string `json:"store_epoch"`
	}{StoreEpoch: storeEpoch})
}

func (handler authorityHandler) unauthorized(writer http.ResponseWriter) {
	writer.Header().Set("WWW-Authenticate", `Bearer realm="store-epoch"`)
	http.Error(writer, "unauthorized", http.StatusUnauthorized)
}

func (handler authorityHandler) readEpoch() (string, error) {
	value, err := readCredential(handler.epochFile, 128)
	if err != nil || !storeEpochPattern.MatchString(value) {
		return "", errors.New("store epoch file is unavailable or invalid")
	}
	return value, nil
}

func (handler authorityHandler) healthServer(address string, metrics http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	mux.HandleFunc("GET /live", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /ready", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		if _, err := handler.readEpoch(); err != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
}
