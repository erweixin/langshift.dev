package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/eventbus/natsjs"
	"github.com/langshift/lites/internal/eventstore/epoch"
	"github.com/langshift/lites/internal/execution/dispatchloop"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/execution/scheduler"
	"github.com/langshift/lites/internal/observability"
	"github.com/langshift/lites/internal/security/opaque"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/metric"
)

type config struct {
	databaseURL, healthAddress, epochURL, epochTokenFile, epochCAFile                                        string
	natsURLs                                                                                                 []string
	natsName, natsCredentialsFile, natsCAFile, natsCertFile, natsKeyFile, streamName                         string
	schedulerConfigFile, resourcePepperFile, dispatchPepperFile, owner                                       string
	environment, version, region, otlpEndpoint, otlpCAFile, otlpTokenFile, otlpTLSName                       string
	allowInsecure                                                                                            bool
	streamReplicas, batchLimit                                                                               int
	streamMaxBytes                                                                                           int64
	streamMaxAge, duplicateWindow, interval, resourceLeaseTTL, dispatchLeaseTTL, redeliveryDelay, retryDelay time.Duration
	traceRatio                                                                                               float64
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg, err := loadConfig()
	if err == nil {
		err = run(ctx, cfg, logger)
	}
	if err != nil {
		logger.Error("agent scheduler stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, cfg config, logger *slog.Logger) error {
	policy, resources, err := loadPolicy(cfg.schedulerConfigFile)
	if err != nil {
		return err
	}
	resourcePepper, err := readBase64(cfg.resourcePepperFile)
	if err != nil {
		return fmt.Errorf("resource pepper: %w", err)
	}
	dispatchPepper, err := readBase64(cfg.dispatchPepperFile)
	if err != nil {
		return fmt.Errorf("dispatch pepper: %w", err)
	}
	if string(resourcePepper) == string(dispatchPepper) {
		return errors.New("scheduler pepper purposes must use different secrets")
	}
	token, err := readSecret(cfg.epochTokenFile)
	if err != nil {
		return err
	}
	epochClient, err := tlsHTTPClient(cfg.epochCAFile, cfg.allowInsecure)
	if err != nil {
		return err
	}
	authority := epoch.HTTPAuthority{Endpoint: cfg.epochURL, BearerToken: token, Client: epochClient, AllowInsecureLoopback: cfg.allowInsecure}
	poolConfig, err := pgxpool.ParseConfig(cfg.databaseURL)
	if err != nil {
		return err
	}
	poolConfig.MinConns, poolConfig.MaxConns = 2, 16
	poolConfig.MaxConnLifetime, poolConfig.MaxConnIdleTime = 30*time.Minute, 5*time.Minute
	pool, err := pgxpool.NewWithConfig(parent, poolConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	connection, js, err := natsjs.Connect(natsjs.ConnectionConfig{URLs: cfg.natsURLs, Name: cfg.natsName, CredentialsFile: cfg.natsCredentialsFile, RootCAFile: cfg.natsCAFile, ClientCertificateFile: cfg.natsCertFile, ClientKeyFile: cfg.natsKeyFile, ConnectTimeout: 5 * time.Second, ReconnectWait: 2 * time.Second, AllowInsecureDevelopment: cfg.allowInsecure, OnDisconnect: func(err error) { logger.Warn("nats disconnected", "error", err) }})
	if err != nil {
		return err
	}
	defer connection.Close()
	telemetry, err := observability.New(parent, observability.Config{ServiceName: "agent-scheduler", ServiceVersion: cfg.version, Environment: cfg.environment, Region: cfg.region, OTLPEndpoint: cfg.otlpEndpoint, OTLPRootCAFile: cfg.otlpCAFile, OTLPBearerTokenFile: cfg.otlpTokenFile, OTLPTLSServerName: cfg.otlpTLSName, TraceSampleRatio: cfg.traceRatio, AllowInsecureDevelopment: cfg.allowInsecure})
	if err != nil {
		return err
	}
	store := executionpostgres.SchedulerStore{Pool: pool, Epochs: authority, ResourceTokens: opaque.Manager{Purpose: "scheduler-resource-lease", Pepper: resourcePepper}, DispatchTokens: opaque.Manager{Purpose: "scheduler-dispatch-lease", Pepper: dispatchPepper}, ResourceLeaseTTL: cfg.resourceLeaseTTL, DispatchLeaseTTL: cfg.dispatchLeaseTTL, RedeliveryDelay: cfg.redeliveryDelay, RetryDelay: cfg.retryDelay}
	meter := telemetry.Meter("github.com/langshift/lites/cmd/agent-scheduler")
	activeRuns := &atomic.Int64{}
	activeRunsObservedAt := &atomic.Int64{}
	if err = refreshActiveRuns(parent, pool, activeRuns, activeRunsObservedAt); err != nil {
		return fmt.Errorf("observe active runs: %w", err)
	}
	if _, err = meter.Int64ObservableGauge("runs.active", metric.WithUnit("{run}"), metric.WithInt64Callback(func(_ context.Context, observer metric.Int64Observer) error {
		observer.Observe(activeRuns.Load())
		return nil
	})); err != nil {
		return err
	}
	if _, err = meter.Int64ObservableGauge("runs.active_observed_at", metric.WithUnit("s"), metric.WithInt64Callback(func(_ context.Context, observer metric.Int64Observer) error {
		observer.Observe(activeRunsObservedAt.Load())
		return nil
	})); err != nil {
		return err
	}
	planned, _ := meter.Int64Counter("scheduler.dispatch.planned")
	published, _ := meter.Int64Counter("scheduler.dispatch.published")
	deferred, _ := meter.Int64Counter("scheduler.dispatch.deferred")
	failures, _ := meter.Int64Counter("scheduler.cycles.failed")
	loop := dispatchloop.Service{Store: store, Publisher: natsjs.DispatchBroker{Publisher: js, Stream: cfg.streamName, RetryWait: 250 * time.Millisecond, RetryAttempts: 3}, Epochs: authority, Config: policy, Owner: cfg.owner, Resources: resources, BatchLimit: cfg.batchLimit, Interval: cfg.interval, Observe: func(value dispatchloop.Observation) {
		planned.Add(parent, int64(value.Planned))
		published.Add(parent, int64(value.Published))
		deferred.Add(parent, int64(value.Deferred))
		for _, wait := range value.QueueWaits {
			telemetry.AgentMetrics().ObserveQueueWait(parent, wait.Duration, wait.QueueClass)
		}
		if value.Err != nil {
			failures.Add(parent, 1)
			logger.Error("scheduler cycle", "resource", value.Resource, "error", value.Err)
		} else if value.Planned > 0 {
			logger.Info("scheduler dispatch", "resource", value.Resource, "planned", value.Planned, "published", value.Published, "deferred", value.Deferred)
		}
	}}
	server := healthServer(cfg.healthAddress, pool, connection, js, authority, telemetry.MetricsHandler())
	ctx, stop := context.WithCancel(parent)
	defer stop()
	go observeActiveRuns(ctx, pool, activeRuns, activeRunsObservedAt, logger)
	errs := make(chan error, 2)
	go func() {
		err := server.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	go func() { errs <- loop.Run(ctx) }()
	logger.Info("agent scheduler ready", "owner", cfg.owner, "resources", resources)
	var runErr error
	select {
	case <-parent.Done():
	case runErr = <-errs:
	}
	stop()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	_ = server.Shutdown(shutdown)
	_ = connection.Drain()
	if err := telemetry.Shutdown(shutdown); runErr == nil {
		runErr = err
	}
	return runErr
}

func refreshActiveRuns(parent context.Context, pool *pgxpool.Pool, value, observedAt *atomic.Int64) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	var count int64
	if err := pool.QueryRow(ctx, `SELECT agent.scheduler_active_run_count()`).Scan(&count); err != nil {
		return err
	}
	if count < 0 {
		return errors.New("active run observation returned a negative value")
	}
	value.Store(count)
	observedAt.Store(time.Now().Unix())
	return nil
}

func observeActiveRuns(ctx context.Context, pool *pgxpool.Pool, value, observedAt *atomic.Int64, logger *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := refreshActiveRuns(ctx, pool, value, observedAt); err != nil && ctx.Err() == nil {
				logger.Error("active run observation failed", "error", err)
			}
		}
	}
}

func healthServer(address string, pool *pgxpool.Pool, nc *nats.Conn, js jetstream.JetStream, authority epoch.HTTPAuthority, metrics http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	mux.HandleFunc("GET /live", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if pool.Ping(ctx) != nil || natsjs.Ready(ctx, nc, js) != nil {
			http.Error(w, "not ready", 503)
			return
		}
		if value, err := authority.CurrentStoreEpoch(ctx); err != nil || value == "" {
			http.Error(w, "not ready", 503)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
}

func loadPolicy(path string) (scheduler.Config, []string, error) {
	file, err := os.Open(path)
	if err != nil {
		return scheduler.Config{}, nil, err
	}
	defer file.Close()
	var policy scheduler.Config
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&policy); err != nil {
		return policy, nil, err
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return policy, nil, errors.New("scheduler policy has trailing data")
	}
	resources := make([]string, 0, len(policy.Resources))
	for name := range policy.Resources {
		resources = append(resources, name)
	}
	sort.Strings(resources)
	if len(resources) == 0 || policy.BatchLimit < 1 || policy.BatchLimit > 1000 || scheduler.ValidateConfig(policy) != nil {
		return policy, nil, errors.New("scheduler policy is invalid")
	}
	return policy, resources, nil
}

func loadConfig() (config, error) {
	host, _ := os.Hostname()
	cfg := config{databaseURL: secretEnv("DATABASE_URL_FILE"), healthAddress: env("HEALTH_ADDRESS", "127.0.0.1:8089"), epochURL: os.Getenv("STORE_EPOCH_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochCAFile: os.Getenv("STORE_EPOCH_ROOT_CA_FILE"), natsURLs: split(os.Getenv("NATS_URLS")), natsName: env("NATS_CLIENT_NAME", "lites-agent-scheduler"), natsCredentialsFile: os.Getenv("NATS_CREDENTIALS_FILE"), natsCAFile: os.Getenv("NATS_ROOT_CA_FILE"), natsCertFile: os.Getenv("NATS_CLIENT_CERT_FILE"), natsKeyFile: os.Getenv("NATS_CLIENT_KEY_FILE"), streamName: env("NATS_COMMAND_STREAM", "LITES_COMMANDS"), schedulerConfigFile: os.Getenv("SCHEDULER_CONFIG_FILE"), resourcePepperFile: os.Getenv("SCHEDULER_RESOURCE_PEPPER_FILE"), dispatchPepperFile: os.Getenv("SCHEDULER_DISPATCH_PEPPER_FILE"), owner: env("SCHEDULER_OWNER", host), environment: os.Getenv("LITES_ENVIRONMENT"), version: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"), otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), allowInsecure: boolEnv("ALLOW_INSECURE_DEVELOPMENT"), streamReplicas: intEnv("NATS_STREAM_REPLICAS", 3), batchLimit: intEnv("SCHEDULER_BATCH_LIMIT", 1000), streamMaxBytes: int64Env("NATS_STREAM_MAX_BYTES", 100<<30), streamMaxAge: durationEnv("NATS_STREAM_MAX_AGE", 7*24*time.Hour), duplicateWindow: durationEnv("NATS_DUPLICATE_WINDOW", 10*time.Minute), interval: durationEnv("SCHEDULER_INTERVAL", 100*time.Millisecond), resourceLeaseTTL: durationEnv("SCHEDULER_RESOURCE_LEASE_TTL", 5*time.Second), dispatchLeaseTTL: durationEnv("SCHEDULER_DISPATCH_LEASE_TTL", 30*time.Second), redeliveryDelay: durationEnv("SCHEDULER_REDELIVERY_DELAY", time.Minute), retryDelay: durationEnv("SCHEDULER_RETRY_DELAY", time.Second), traceRatio: floatEnv("TRACE_SAMPLE_RATIO", .1)}
	if cfg.databaseURL == "" || cfg.epochURL == "" || len(cfg.natsURLs) == 0 || cfg.schedulerConfigFile == "" || cfg.resourcePepperFile == "" || cfg.dispatchPepperFile == "" || cfg.owner == "" || cfg.environment == "" || cfg.version == "" || cfg.region == "" || cfg.batchLimit < 1 || cfg.batchLimit > 1000 || cfg.streamReplicas < 1 || cfg.interval <= 0 || (cfg.natsCertFile == "") != (cfg.natsKeyFile == "") {
		return cfg, errors.New("required scheduler configuration is missing or invalid")
	}
	if !cfg.allowInsecure && (os.Getenv("DATABASE_URL_FILE") == "" || cfg.streamReplicas < 3 || cfg.epochTokenFile == "" || cfg.epochCAFile == "" || cfg.natsCAFile == "" || cfg.natsCertFile == "" || cfg.otlpEndpoint == "" || cfg.otlpCAFile == "" || cfg.otlpTokenFile == "") {
		return cfg, errors.New("production scheduler requires file credentials, TLS, authenticated telemetry, and three JetStream replicas")
	}
	return cfg, nil
}

func tlsHTTPClient(caFile string, insecure bool) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	if caFile != "" {
		body, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, err
		}
		if !roots.AppendCertsFromPEM(body) {
			return nil, errors.New("epoch CA is empty")
		}
		tlsConfig.RootCAs = roots
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	if insecure && caFile == "" {
		transport.TLSClientConfig = nil
	}
	return &http.Client{Transport: transport, Timeout: 3 * time.Second}, nil
}
func readSecret(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	body, err := os.ReadFile(path)
	if err != nil || len(body) > 8192 {
		return "", errors.New("secret unreadable")
	}
	return strings.TrimSpace(string(body)), nil
}
func readBase64(path string) ([]byte, error) {
	value, err := readSecret(path)
	if err != nil {
		return nil, err
	}
	body, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(body) < 32 {
		return nil, errors.New("secret must be base64 and at least 32 bytes")
	}
	return body, nil
}
func secretEnv(name string) string {
	value, err := readSecret(os.Getenv(name))
	if err != nil {
		return ""
	}
	return value
}
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func split(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
func intEnv(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err == nil {
		return value
	}
	return fallback
}
func int64Env(name string, fallback int64) int64 {
	value, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err == nil {
		return value
	}
	return fallback
}
func floatEnv(name string, fallback float64) float64 {
	value, err := strconv.ParseFloat(os.Getenv(name), 64)
	if err == nil {
		return value
	}
	return fallback
}
func boolEnv(name string) bool { value, _ := strconv.ParseBool(os.Getenv(name)); return value }
func durationEnv(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(os.Getenv(name))
	if err == nil {
		return value
	}
	return fallback
}
