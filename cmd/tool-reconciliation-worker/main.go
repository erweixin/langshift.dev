package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/eventbus/natsjs"
	"github.com/langshift/lites/internal/eventstore/epoch"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/objectstore/s3store"
	"github.com/langshift/lites/internal/observability"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/payload/vaultkeys"
	"github.com/langshift/lites/internal/security/opaque"
	"github.com/langshift/lites/internal/toolreconciler"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
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
		logger.Error("tool reconciliation worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, configuration config, logger *slog.Logger) error {
	executionIDKey, err := readBase64(configuration.executionIDKeyFile, 32)
	if err != nil {
		return errors.New("load execution ID key")
	}
	executionPepper, err := readBase64(configuration.executionLeasePepperFile, 32)
	if err != nil {
		return errors.New("load execution lease pepper")
	}
	epochToken := ""
	if configuration.epochTokenFile != "" {
		epochToken, err = readSecret(configuration.epochTokenFile, 8192)
		if err != nil {
			return errors.New("load store epoch credential")
		}
	}
	epochHTTP, err := dependencyHTTPClient(configuration.epochCAFile, configuration.epochCertFile, configuration.epochKeyFile, configuration.epochTLSName, configuration.allowInsecure, 3*time.Second)
	if err != nil {
		return errors.New("configure store epoch transport")
	}
	authority := epoch.HTTPAuthority{Endpoint: configuration.epochURL, BearerToken: epochToken, Client: epochHTTP, AllowInsecureLoopback: configuration.allowInsecure}
	epochCtx, epochCancel := context.WithTimeout(parent, 3*time.Second)
	storeEpoch, err := authority.CurrentStoreEpoch(epochCtx)
	epochCancel()
	if err != nil || storeEpoch == "" {
		return errors.New("read authoritative store epoch")
	}
	pool, err := openPool(parent, configuration.databaseURL, 32, 4)
	if err != nil {
		return errors.New("connect execution database")
	}
	defer pool.Close()
	s3Client, err := s3store.NewClient(parent, s3store.ClientConfig{Region: configuration.s3Region, Endpoint: configuration.s3Endpoint, UsePathStyle: configuration.s3PathStyle, AllowInsecureDevelopment: configuration.allowInsecure})
	if err != nil {
		return errors.New("configure object store")
	}
	blobs := s3store.Store{Client: s3Client, Bucket: configuration.payloadBucket, Prefix: configuration.payloadPrefix, MaxBytes: 32 << 20, ServerSideEncryption: configuration.s3Encryption, KMSKeyID: configuration.s3KMSKeyID, RequireDigestMetadata: true}
	vaultReader, err := vaultkeys.NewClientReader(vaultkeys.ClientConfig{Address: configuration.vaultAddress, Namespace: configuration.vaultNamespace, Mount: configuration.vaultMount, TokenFile: configuration.vaultTokenFile, CACertificateFile: configuration.vaultCAFile, ClientCertificateFile: configuration.vaultCertFile, ClientKeyFile: configuration.vaultKeyFile, TLSServerName: configuration.vaultTLSName, AllowInsecureDevelopment: configuration.allowInsecure})
	if err != nil {
		return errors.New("configure Vault")
	}
	payloads := payload.EnvelopeStore{Keys: vaultkeys.Provider{KV: vaultReader, Prefix: configuration.vaultKeyPrefix}, Blobs: blobs}
	registrations, err := loadLookupRegistrations(configuration.adapterFile, configuration.lookupTimeout, configuration.maximumEvidence)
	if err != nil {
		return err
	}
	connection, js, err := natsjs.Connect(natsjs.ConnectionConfig{URLs: configuration.natsURLs, Name: configuration.natsName, CredentialsFile: configuration.natsCredentialsFile, RootCAFile: configuration.natsCAFile, ClientCertificateFile: configuration.natsCertFile, ClientKeyFile: configuration.natsKeyFile, ConnectTimeout: 5 * time.Second, ReconnectWait: 2 * time.Second, AllowInsecureDevelopment: configuration.allowInsecure, OnDisconnect: func(err error) { logger.Warn("nats disconnected", "error", err) }, OnReconnect: func(url string) { logger.Info("nats reconnected", "url", url) }})
	if err != nil {
		return errors.New("connect NATS")
	}
	defer connection.Close()
	subject, _ := natsjs.DispatchSubjectFor("ReconcileToolEffect")
	backoff := []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}
	consumerSource, err := (natsjs.DurableConsumer{Stream: configuration.streamName, Name: configuration.consumerName, FilterSubjects: []string{subject}, Replicas: configuration.streamReplicas, AckWait: configuration.ackWait, Backoff: backoff, MaxDeliver: configuration.maxDeliver, MaxAckPending: configuration.maxAckPending, MaxRequestBatch: configuration.concurrency, MaxRequestMaxBytes: 64 << 10}).Provision(parent, js)
	if err != nil {
		return errors.New("provision tool reconciliation durable consumer")
	}
	telemetry, err := observability.New(parent, observability.Config{ServiceName: "tool-reconciliation-worker", ServiceVersion: configuration.version, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpTokenFile, TraceSampleRatio: configuration.traceRatio, AllowInsecureDevelopment: configuration.allowInsecure})
	if err != nil {
		return errors.New("configure observability")
	}
	payloads.Metrics = telemetry.AgentMetrics()
	actor := json.RawMessage(`{"kind":"service","name":"tool-reconciliation-worker"}`)
	runStore := executionpostgres.RunStore{Pool: pool, Appender: eventpostgres.Appender{Observer: telemetry.AgentMetrics()}, IDKey: executionIDKey, StoreEpoch: storeEpoch, Epochs: authority, Tokens: opaque.Manager{Purpose: "tool-reconciliation-lease-v1", Pepper: executionPepper}, LeaseTTL: configuration.executionLeaseTTL, RequireDispatchFence: true}
	production, err := toolreconciler.NewProductionRuntime(toolreconciler.ProductionConfig{ArtifactPath: configuration.artifactPath, ArtifactFileHash: configuration.artifactHash, Payloads: payloads, Store: runStore, LookupRegistrations: registrations, ConsumerName: configuration.consumerName, WorkerID: configuration.workerID, Actor: actor, IDKey: executionIDKey, HeartbeatInterval: configuration.reconciliationHeartbeat, MaximumCommand: configuration.maximumCommand, MaximumEvidence: configuration.maximumEvidence, MaximumRounds: configuration.maximumRounds, RetryDelay: configuration.reconciliationRetryDelay, MaximumRetryDelay: configuration.maximumReconciliationRetryDelay, Resume: toolreconciler.Schedule{QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 4, MaxAttempts: 5}, Retry: toolreconciler.Schedule{QueueClass: "background", ResourceClass: "tool-reconciliation", Priority: 40, CostUnits: 1, MaxAttempts: 20}, Metrics: telemetry.AgentMetrics()})
	if err != nil {
		return fmt.Errorf("compose production tool reconciliation worker: %w", err)
	}
	locker := toolreconciler.PostgresLocker{Pool: pool, UnlockTimeout: configuration.sweeperUnlockTimeout}
	coordinator := toolreconciler.Coordinator{Store: runStore, Payloads: payloads, Locker: locker, IDKey: executionIDKey, StoreEpoch: storeEpoch, ReconcileDelay: configuration.abandonedReconcileDelay, Reconcile: toolreconciler.Schedule{QueueClass: "background", ResourceClass: "tool-reconciliation", Priority: 40, CostUnits: 1, MaxAttempts: 20}, MaximumCommand: configuration.maximumCommand, ShardCount: configuration.shardCount, TenantPage: configuration.tenantPage, EffectPage: configuration.effectPage, Metrics: telemetry.AgentMetrics()}
	dependencyCtx, dependencyCancel := context.WithTimeout(parent, 5*time.Second)
	dependencyErr := dependenciesReady(dependencyCtx, pool, connection, js, authority, storeEpoch, blobs.Ready, vaultReader.Ready)
	dependencyCancel()
	if dependencyErr != nil {
		return errors.New("tool reconciliation dependency is not ready")
	}
	meter := telemetry.Meter("github.com/langshift/lites/cmd/tool-reconciliation-worker")
	deliveryFailures, _ := meter.Int64Counter("tool_reconciliation.deliveries.failed")
	sweepCycles, _ := meter.Int64Counter("tool_effect_sweeper.cycles")
	sweptEffects, _ := meter.Int64Counter("tool_effect_sweeper.effects.swept")
	manualEffects, _ := meter.Int64Counter("tool_effect_sweeper.effects.manual_review_required")
	lockContention, _ := meter.Int64Counter("tool_effect_sweeper.locks.contended")
	consumer := natsjs.Consumer{Source: consumerSource, Handle: production.Handler.Handle, Dispatch: true, Concurrency: configuration.concurrency, PullExpires: configuration.pullExpires, HeartbeatInterval: configuration.messageHeartbeat, BusyDelay: configuration.busyDelay, RetryDelay: configuration.natsRetryDelay, AckTimeout: configuration.ackTimeout, OnError: func(_ context.Context, failure error) {
		deliveryFailures.Add(parent, 1)
		logger.Error("tool reconciliation delivery", "error", failure)
	}}
	ready := &atomic.Bool{}
	ready.Store(true)
	health := healthServer(configuration.healthAddress, ready, pool, connection, js, authority, storeEpoch, blobs.Ready, vaultReader.Ready, telemetry.MetricsHandler())
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	errs := make(chan error, 3)
	go func() {
		if serveErr := health.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errs <- serveErr
		}
	}()
	go func() { errs <- consumer.Run(ctx) }()
	go func() {
		for {
			cycleCtx, cycleCancel := context.WithTimeout(ctx, configuration.sweeperCycleTimeout)
			result, cycleErr := coordinator.RunOnce(cycleCtx)
			cycleCancel()
			sweepCycles.Add(ctx, 1)
			if cycleErr != nil {
				errs <- cycleErr
				return
			}
			sweptEffects.Add(ctx, int64(result.Sweep.Swept))
			manualEffects.Add(ctx, int64(result.Sweep.ManualReviewRequired))
			lockContention.Add(ctx, int64(result.Contended+result.Sweep.Contended))
			if result.Sweep.Swept > 0 || result.Sweep.ManualReviewRequired > 0 {
				logger.Info("tool effect sweep", "swept", result.Sweep.Swept, "manual_review_required", result.Sweep.ManualReviewRequired, "tenants", result.Sweep.Tenants)
			}
			timer := time.NewTimer(configuration.sweeperInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	logger.Info("tool reconciliation worker ready", "consumer", configuration.consumerName, "worker_id", configuration.workerID, "registry_hash", production.Artifact.RegistryHash, "store_epoch", storeEpoch, "shards", configuration.shardCount)
	var runErr error
	select {
	case <-parent.Done():
	case runErr = <-errs:
	}
	ready.Store(false)
	cancel()
	shutdown, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	_ = health.Shutdown(shutdown)
	_ = connection.Drain()
	if telemetryErr := telemetry.Shutdown(shutdown); runErr == nil {
		runErr = telemetryErr
	}
	if errors.Is(runErr, context.Canceled) && parent.Err() != nil {
		return nil
	}
	return runErr
}

func openPool(ctx context.Context, databaseURL string, maximum, minimum int32) (*pgxpool.Pool, error) {
	configuration, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	configuration.MaxConns, configuration.MinConns = maximum, minimum
	configuration.MaxConnLifetime, configuration.MaxConnIdleTime = 30*time.Minute, 5*time.Minute
	return pgxpool.NewWithConfig(ctx, configuration)
}

func dependencyHTTPClient(caFile, certFile, keyFile, serverName string, allowInsecure bool, timeout time.Duration) (*http.Client, error) {
	tlsConfiguration := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: serverName}
	if caFile != "" {
		contents, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, err
		}
		if !roots.AppendCertsFromPEM(contents) {
			return nil, errors.New("dependency CA contains no certificates")
		}
		tlsConfiguration.RootCAs = roots
	}
	if certFile != "" {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		tlsConfiguration.Certificates = []tls.Certificate{certificate}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns, transport.MaxIdleConnsPerHost, transport.ResponseHeaderTimeout = 128, 32, timeout
	if allowInsecure && caFile == "" && certFile == "" {
		transport.TLSClientConfig = nil
	} else {
		transport.TLSClientConfig = tlsConfiguration
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

type readiness func(context.Context) error

func dependenciesReady(ctx context.Context, pool *pgxpool.Pool, connection *nats.Conn, js jetstream.JetStream, authority epoch.HTTPAuthority, expectedEpoch string, checks ...readiness) error {
	if pool.Ping(ctx) != nil || natsjs.Ready(ctx, connection, js) != nil {
		return errors.New("database or NATS unavailable")
	}
	if current, err := authority.CurrentStoreEpoch(ctx); err != nil || current != expectedEpoch {
		return errors.New("store epoch changed")
	}
	for _, check := range checks {
		if check == nil || check(ctx) != nil {
			return errors.New("encrypted payload dependency unavailable")
		}
	}
	return nil
}

func healthServer(address string, ready *atomic.Bool, pool *pgxpool.Pool, connection *nats.Conn, js jetstream.JetStream, authority epoch.HTTPAuthority, expectedEpoch string, blobReady, vaultReady readiness, metrics http.Handler) *http.Server {
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
		if !ready.Load() || dependenciesReady(ctx, pool, connection, js, authority, expectedEpoch, blobReady, vaultReady) != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
}
