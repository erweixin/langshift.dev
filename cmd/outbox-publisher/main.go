package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/eventbus/natsjs"
	"github.com/langshift/lites/internal/eventstore/epoch"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/observability"
	"github.com/langshift/lites/internal/security/opaque"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type config struct {
	databaseURL, databaseURLFile, healthAddress, epochURL, epochTokenFile, epochRootCAFile, epochCertFile, epochKeyFile string
	natsURLs                                                                                                            []string
	natsName, natsCredentialsFile, natsRootCAFile, natsCertFile, natsKeyFile, streamName                                string
	pepperFile                                                                                                          string
	environment, serviceVersion, region                                                                                 string
	otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName, otlpBearerTokenFile                               string
	allowInsecureDevelopment                                                                                            bool
	streamReplicas, shardIndex, shardCount, tenantPageSize, batchSize                                                   int
	streamMaxBytes                                                                                                      int64
	streamMaxAge, duplicateWindow, pollInterval, leaseTTL, retryBase, retryLimit                                        time.Duration
	traceSampleRatio                                                                                                    float64
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	configuration, err := loadConfig()
	if err != nil {
		logger.Error("configuration rejected", "error", err)
		os.Exit(2)
	}
	if err = run(ctx, configuration, logger); err != nil {
		logger.Error("outbox publisher stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, configuration config, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	pepper, err := readBase64Secret(configuration.pepperFile, 32)
	if err != nil {
		return fmt.Errorf("load outbox lease pepper: %w", err)
	}
	epochToken, err := readOptionalSecret(configuration.epochTokenFile)
	if err != nil {
		return fmt.Errorf("load epoch authority credential: %w", err)
	}
	if !configuration.allowInsecureDevelopment && epochToken == "" && configuration.epochCertFile == "" {
		return errors.New("epoch authority credential file is empty")
	}
	epochClient, err := newEpochClient(configuration)
	if err != nil {
		return err
	}
	authority := epoch.HTTPAuthority{Endpoint: configuration.epochURL, BearerToken: epochToken, Client: epochClient, AllowInsecureLoopback: configuration.allowInsecureDevelopment}
	poolConfig, err := pgxpool.ParseConfig(configuration.databaseURL)
	if err != nil {
		return fmt.Errorf("parse database configuration: %w", err)
	}
	poolConfig.MaxConns = 12
	poolConfig.MinConns = 2
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	connection, js, err := natsjs.Connect(natsjs.ConnectionConfig{URLs: configuration.natsURLs, Name: configuration.natsName, CredentialsFile: configuration.natsCredentialsFile, RootCAFile: configuration.natsRootCAFile, ClientCertificateFile: configuration.natsCertFile, ClientKeyFile: configuration.natsKeyFile, ConnectTimeout: 5 * time.Second, ReconnectWait: 2 * time.Second, AllowInsecureDevelopment: configuration.allowInsecureDevelopment, OnDisconnect: func(err error) { logger.Warn("nats disconnected", "error", err) }, OnReconnect: func(url string) { logger.Info("nats reconnected", "url", url) }, OnClosed: func(err error) { logger.Error("nats connection closed", "error", err) }})
	if err != nil {
		return fmt.Errorf("connect nats: %w", err)
	}
	defer connection.Close()
	provisionCtx, provisionCancel := context.WithTimeout(ctx, 15*time.Second)
	err = (natsjs.CommandStream{Name: configuration.streamName, Replicas: configuration.streamReplicas, MaxAge: configuration.streamMaxAge, MaxBytes: configuration.streamMaxBytes, DuplicateWindow: configuration.duplicateWindow}).Provision(provisionCtx, js)
	provisionCancel()
	if err != nil {
		return fmt.Errorf("provision command stream: %w", err)
	}
	store := eventpostgres.OutboxStore{Pool: pool, Epochs: authority, Tokens: opaque.Manager{Purpose: "outbox-publisher-lease", Pepper: pepper}, LeaseTTL: configuration.leaseTTL, RetryBase: configuration.retryBase, RetryLimit: configuration.retryLimit}
	broker := natsjs.Broker{Publisher: js, Stream: configuration.streamName, RetryWait: 250 * time.Millisecond, RetryAttempts: 3}
	telemetry, err := observability.New(ctx, observability.Config{ServiceName: "outbox-publisher", ServiceVersion: configuration.serviceVersion, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpBearerTokenFile, TraceSampleRatio: configuration.traceSampleRatio, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure observability")
	}
	meter := telemetry.Meter("github.com/langshift/lites/cmd/outbox-publisher")
	publishedCounter, _ := meter.Int64Counter("outbox.commands.published")
	deferredCounter, _ := meter.Int64Counter("outbox.commands.deferred")
	errorCounter, _ := meter.Int64Counter("outbox.cycles.failed")
	runner := natsjs.OutboxRunner{Store: store, Broker: broker, PollInterval: configuration.pollInterval, TenantPageSize: configuration.tenantPageSize, BatchSize: configuration.batchSize, ShardIndex: configuration.shardIndex, ShardCount: configuration.shardCount, Observe: func(observation natsjs.PublishObservation) {
		if observation.Err != nil {
			errorCounter.Add(ctx, 1)
			logger.Error("outbox publish cycle", "error", observation.Err)
		} else if observation.Result.Claimed > 0 {
			publishedCounter.Add(ctx, int64(observation.Result.Published))
			deferredCounter.Add(ctx, int64(observation.Result.Deferred))
			logger.Info("outbox publish batch", "claimed", observation.Result.Claimed, "published", observation.Result.Published, "deferred", observation.Result.Deferred)
		}
	}}
	health := healthServer(configuration.healthAddress, pool, connection, js, authority, telemetry.MetricsHandler())
	errorsChannel := make(chan error, 2)
	go func() {
		logger.Info("health server listening", "address", configuration.healthAddress)
		if serveErr := health.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errorsChannel <- serveErr
		}
	}()
	go func() { errorsChannel <- runner.Run(ctx) }()
	logger.Info("outbox publisher ready", "stream", configuration.streamName, "shard_index", configuration.shardIndex, "shard_count", configuration.shardCount)
	var runErr error
	select {
	case <-parent.Done():
	case runErr = <-errorsChannel:
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	_ = health.Shutdown(shutdownCtx)
	if err = connection.Drain(); err != nil && runErr == nil {
		runErr = err
	}
	if telemetryErr := telemetry.Shutdown(shutdownCtx); telemetryErr != nil && runErr == nil {
		runErr = telemetryErr
	}
	return runErr
}

func healthServer(address string, pool *pgxpool.Pool, connection *nats.Conn, js jetstream.JetStream, authority eventpostgres.EpochAuthority, metrics http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	mux.HandleFunc("GET /live", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /ready", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		if pool == nil || authority == nil || pool.Ping(ctx) != nil || natsjs.Ready(ctx, connection, js) != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		if current, err := authority.CurrentStoreEpoch(ctx); err != nil || current == "" {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
}

func loadConfig() (config, error) {
	if err := validateTypedEnvironment(); err != nil {
		return config{}, err
	}
	databaseURLFile := os.Getenv("DATABASE_URL_FILE")
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURLFile != "" {
		if databaseURL != "" {
			return config{}, errors.New("DATABASE_URL and DATABASE_URL_FILE are mutually exclusive")
		}
		var err error
		databaseURL, err = readOptionalSecret(databaseURLFile)
		if err != nil {
			return config{}, errors.New("DATABASE_URL_FILE is unreadable")
		}
	}
	value := config{
		databaseURL: databaseURL, databaseURLFile: databaseURLFile, healthAddress: envString("HEALTH_ADDRESS", "127.0.0.1:8081"), epochURL: os.Getenv("STORE_EPOCH_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochRootCAFile: os.Getenv("STORE_EPOCH_ROOT_CA_FILE"), epochCertFile: os.Getenv("STORE_EPOCH_CLIENT_CERT_FILE"), epochKeyFile: os.Getenv("STORE_EPOCH_CLIENT_KEY_FILE"),
		natsURLs: splitNonempty(os.Getenv("NATS_URLS")), natsName: envString("NATS_CLIENT_NAME", "lites-outbox-publisher"), natsCredentialsFile: os.Getenv("NATS_CREDENTIALS_FILE"), natsRootCAFile: os.Getenv("NATS_ROOT_CA_FILE"), natsCertFile: os.Getenv("NATS_CLIENT_CERT_FILE"), natsKeyFile: os.Getenv("NATS_CLIENT_KEY_FILE"), streamName: envString("NATS_COMMAND_STREAM", "LITES_COMMANDS"), pepperFile: os.Getenv("OUTBOX_LEASE_PEPPER_FILE"),
		allowInsecureDevelopment: envBool("ALLOW_INSECURE_DEVELOPMENT", false), streamReplicas: envInt("NATS_STREAM_REPLICAS", 3), shardIndex: envInt("PUBLISHER_SHARD_INDEX", 0), shardCount: envInt("PUBLISHER_SHARD_COUNT", 1), tenantPageSize: envInt("PUBLISHER_TENANT_PAGE_SIZE", 500), batchSize: envInt("PUBLISHER_BATCH_SIZE", 100), streamMaxBytes: envInt64("NATS_STREAM_MAX_BYTES", 100<<30), streamMaxAge: envDuration("NATS_STREAM_MAX_AGE", 7*24*time.Hour), duplicateWindow: envDuration("NATS_DUPLICATE_WINDOW", 10*time.Minute), pollInterval: envDuration("PUBLISHER_POLL_INTERVAL", 500*time.Millisecond), leaseTTL: envDuration("PUBLISHER_LEASE_TTL", 30*time.Second), retryBase: envDuration("PUBLISHER_RETRY_BASE", time.Second), retryLimit: envDuration("PUBLISHER_RETRY_LIMIT", 5*time.Minute),
		environment: os.Getenv("LITES_ENVIRONMENT"), serviceVersion: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"), otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpBearerTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"), traceSampleRatio: envFloat("TRACE_SAMPLE_RATIO", 0.1),
	}
	if value.databaseURL == "" || len(value.natsURLs) == 0 || value.epochURL == "" || value.pepperFile == "" || value.environment == "" || value.serviceVersion == "" || value.region == "" || value.traceSampleRatio < 0 || value.traceSampleRatio > 1 || value.streamReplicas < 1 || value.shardCount < 1 || value.shardIndex < 0 || value.shardIndex >= value.shardCount || value.tenantPageSize < 1 || value.tenantPageSize > 5000 || value.batchSize < 1 || value.batchSize > 500 || value.streamMaxBytes <= 0 || value.streamMaxAge <= 0 || value.duplicateWindow <= 0 || value.pollInterval <= 0 || value.leaseTTL <= 0 || value.retryBase <= 0 || value.retryLimit < value.retryBase || (value.epochCertFile == "") != (value.epochKeyFile == "") || (value.natsCertFile == "") != (value.natsKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return config{}, errors.New("required runtime configuration is missing or invalid")
	}
	if !value.allowInsecureDevelopment && (value.databaseURLFile == "" || value.streamReplicas < 3 || (value.epochTokenFile == "" && value.epochCertFile == "") || value.otlpEndpoint == "" || (value.otlpBearerTokenFile == "" && value.otlpCertFile == "")) {
		return config{}, errors.New("production requires file-backed database credentials, a three-replica stream, and authenticated epoch authority")
	}
	return value, nil
}

func validateTypedEnvironment() error {
	integerNames := []string{"NATS_STREAM_REPLICAS", "PUBLISHER_SHARD_INDEX", "PUBLISHER_SHARD_COUNT", "PUBLISHER_TENANT_PAGE_SIZE", "PUBLISHER_BATCH_SIZE"}
	for _, name := range integerNames {
		if value, exists := os.LookupEnv(name); exists {
			if _, err := strconv.Atoi(value); err != nil {
				return fmt.Errorf("%s must be an integer", name)
			}
		}
	}
	if value, exists := os.LookupEnv("NATS_STREAM_MAX_BYTES"); exists {
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return errors.New("NATS_STREAM_MAX_BYTES must be an integer")
		}
	}
	if value, exists := os.LookupEnv("ALLOW_INSECURE_DEVELOPMENT"); exists {
		if _, err := strconv.ParseBool(value); err != nil {
			return errors.New("ALLOW_INSECURE_DEVELOPMENT must be a boolean")
		}
	}
	if value, exists := os.LookupEnv("TRACE_SAMPLE_RATIO"); exists {
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return errors.New("TRACE_SAMPLE_RATIO must be a number")
		}
	}
	durationNames := []string{"NATS_STREAM_MAX_AGE", "NATS_DUPLICATE_WINDOW", "PUBLISHER_POLL_INTERVAL", "PUBLISHER_LEASE_TTL", "PUBLISHER_RETRY_BASE", "PUBLISHER_RETRY_LIMIT"}
	for _, name := range durationNames {
		if value, exists := os.LookupEnv(name); exists {
			if _, err := time.ParseDuration(value); err != nil {
				return fmt.Errorf("%s must be a duration", name)
			}
		}
	}
	return nil
}

func newEpochClient(configuration config) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	if configuration.epochRootCAFile != "" {
		contents, err := os.ReadFile(configuration.epochRootCAFile)
		if err != nil {
			return nil, fmt.Errorf("load epoch authority CA: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system roots: %w", err)
		}
		if !roots.AppendCertsFromPEM(contents) {
			return nil, errors.New("epoch authority CA contains no certificates")
		}
		tlsConfig.RootCAs = roots
	}
	if configuration.epochCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(configuration.epochCertFile, configuration.epochKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load epoch authority client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: transport, Timeout: 3 * time.Second}, nil
}

func readBase64Secret(path string, minimum int) ([]byte, error) {
	encoded, err := readOptionalSecret(path)
	if err != nil || encoded == "" {
		return nil, errors.New("secret file is empty or unreadable")
	}
	value, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(value) < minimum {
		return nil, errors.New("secret does not meet encoding or length requirements")
	}
	return value, nil
}

func readOptionalSecret(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(contents) > 8192 {
		return "", errors.New("secret file exceeds limit or is unreadable")
	}
	return strings.TrimSpace(string(contents)), nil
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err == nil {
		return value
	}
	return fallback
}
func envInt64(name string, fallback int64) int64 {
	value, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err == nil {
		return value
	}
	return fallback
}
func envBool(name string, fallback bool) bool {
	value, err := strconv.ParseBool(os.Getenv(name))
	if err == nil {
		return value
	}
	return fallback
}
func envDuration(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(os.Getenv(name))
	if err == nil {
		return value
	}
	return fallback
}
func envFloat(name string, fallback float64) float64 {
	value, err := strconv.ParseFloat(os.Getenv(name), 64)
	if err == nil {
		return value
	}
	return fallback
}
func splitNonempty(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
