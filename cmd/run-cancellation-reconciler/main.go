package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	billingpostgres "github.com/langshift/lites/internal/billing/postgres"
	"github.com/langshift/lites/internal/eventstore/epoch"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/execution/cancellation"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/objectstore/s3store"
	"github.com/langshift/lites/internal/observability"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/payload/vaultkeys"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
	"github.com/langshift/lites/internal/security/opaque"
)

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
		logger.Error("run cancellation reconciler stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configuration config, logger *slog.Logger) error {
	executionIDKey, err := readBase64Key(configuration.executionIDKeyFile)
	if err != nil {
		return errors.New("load execution id key")
	}
	executionPepper, err := readBase64Key(configuration.executionLeasePepperFile)
	if err != nil {
		return errors.New("load execution lease pepper")
	}
	llmIDKey, err := readBase64Key(configuration.llmIDKeyFile)
	if err != nil {
		return errors.New("load LLM id key")
	}
	llmPepper, err := readBase64Key(configuration.llmTokenPepperFile)
	if err != nil {
		return errors.New("load LLM token pepper")
	}
	billingIDKey, err := readBase64Key(configuration.billingIDKeyFile)
	if err != nil {
		return errors.New("load billing id key")
	}
	runtimeIDKey, err := readBase64Key(configuration.runtimeIDKeyFile)
	if err != nil {
		return errors.New("load Runtime id key")
	}
	runtimePepper, err := readBase64Key(configuration.runtimeTokenPepperFile)
	if err != nil {
		return errors.New("load Runtime token pepper")
	}
	epochToken := ""
	if configuration.epochTokenFile != "" {
		epochToken, err = readSecret(configuration.epochTokenFile, 8192)
		if err != nil {
			return errors.New("load epoch credential")
		}
	}
	epochClient, err := dependencyHTTPClient(configuration.epochCAFile, configuration.epochCertFile, configuration.epochKeyFile, configuration.epochTLSName, configuration.allowInsecureDevelopment)
	if err != nil {
		return errors.New("configure epoch transport")
	}
	authority := epoch.HTTPAuthority{Endpoint: configuration.epochURL, BearerToken: epochToken, Client: epochClient, AllowInsecureLoopback: configuration.allowInsecureDevelopment}
	epochCtx, epochCancel := context.WithTimeout(ctx, 3*time.Second)
	storeEpoch, err := authority.CurrentStoreEpoch(epochCtx)
	epochCancel()
	if err != nil {
		return errors.New("read authoritative store epoch")
	}
	agentPool, err := openPool(ctx, configuration.databaseURL, 32, 4)
	if err != nil {
		return errors.New("connect execution database")
	}
	defer agentPool.Close()
	runtimePool, err := openPool(ctx, configuration.runtimeDatabaseURL, 16, 3)
	if err != nil {
		return errors.New("connect Runtime database")
	}
	defer runtimePool.Close()
	s3Client, err := s3store.NewClient(ctx, s3store.ClientConfig{Region: configuration.s3Region, Endpoint: configuration.s3Endpoint, UsePathStyle: configuration.s3PathStyle, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure object store")
	}
	blobs := s3store.Store{Client: s3Client, Bucket: configuration.payloadBucket, Prefix: configuration.payloadPrefix, MaxBytes: 1 << 20, ServerSideEncryption: configuration.s3Encryption, KMSKeyID: configuration.s3KMSKeyID, RequireDigestMetadata: true}
	vaultReader, err := vaultkeys.NewClientReader(vaultkeys.ClientConfig{Address: configuration.vaultAddress, Namespace: configuration.vaultNamespace, Mount: configuration.vaultMount, TokenFile: configuration.vaultTokenFile, CACertificateFile: configuration.vaultCAFile, ClientCertificateFile: configuration.vaultCertFile, ClientKeyFile: configuration.vaultKeyFile, TLSServerName: configuration.vaultTLSName, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure Vault")
	}
	payloadStore := payload.EnvelopeStore{Keys: vaultkeys.Provider{KV: vaultReader, Prefix: configuration.keyPrefix}, Blobs: blobs}
	dependencyCtx, dependencyCancel := context.WithTimeout(ctx, 5*time.Second)
	dependencyErr := agentPool.Ping(dependencyCtx)
	if dependencyErr == nil {
		dependencyErr = runtimePool.Ping(dependencyCtx)
	}
	if dependencyErr == nil {
		dependencyErr = blobs.Ready(dependencyCtx)
	}
	if dependencyErr == nil {
		dependencyErr = vaultReader.Ready(dependencyCtx)
	}
	dependencyCancel()
	if dependencyErr != nil {
		return errors.New("run cancellation dependency is not ready")
	}
	telemetry, err := observability.New(ctx, observability.Config{ServiceName: "run-cancellation-reconciler", ServiceVersion: configuration.serviceVersion, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpBearerTokenFile, TraceSampleRatio: configuration.traceSampleRatio, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure observability")
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = telemetry.Shutdown(shutdown)
	}()
	appender := eventpostgres.Appender{}
	billingStore := billingpostgres.Store{Pool: agentPool, Appender: appender, Epochs: authority, StoreEpoch: storeEpoch, IDKey: billingIDKey}
	llmStore := llmpostgres.Store{Pool: agentPool, Appender: appender, Epochs: authority, StoreEpoch: storeEpoch, IDKey: llmIDKey, TokenPepper: llmPepper, Billing: billingStore}
	runtimeStore := runtimepostgres.Store{Pool: runtimePool, Appender: appender, Epochs: authority, StoreEpoch: storeEpoch, IDKey: runtimeIDKey, TokenPepper: runtimePepper}
	runStore := executionpostgres.RunStore{Pool: agentPool, Appender: appender, IDKey: executionIDKey, StoreEpoch: storeEpoch, Epochs: authority, Tokens: opaque.Manager{Purpose: "run-execution-lease-v1", Pepper: executionPepper}, LeaseTTL: configuration.executionLeaseTTL}
	reconciler := executionpostgres.RunCancellationReconcilerService{
		Store: runStore, Payloads: payloadStore, IDKey: executionIDKey,
		Dependencies: []executionpostgres.RunCancellationDependencyConverger{
			llmpostgres.RunCancellationService{Store: llmStore, Payloads: payloadStore},
			runtimepostgres.RunCancellationService{Store: runtimeStore, Payloads: payloadStore},
		},
	}
	coordinator := cancellation.Coordinator{Reconciler: reconciler, Locker: cancellation.PostgresLocker{Pool: agentPool, UnlockTimeout: configuration.unlockTimeout}, StoreEpoch: storeEpoch, ShardCount: configuration.shardCount, TenantPage: configuration.tenantPage, CancellationPage: configuration.cancellationPage}
	meter := telemetry.Meter("github.com/langshift/lites/cmd/run-cancellation-reconciler")
	cycleCounter, _ := meter.Int64Counter("run_cancellation.reconciler.cycles")
	settledCounter, _ := meter.Int64Counter("run_cancellation.reconciler.settled")
	deferredCounter, _ := meter.Int64Counter("run_cancellation.reconciler.deferred")
	failureCounter, _ := meter.Int64Counter("run_cancellation.reconciler.failed")
	childGroupCounter, _ := meter.Int64Counter("run_cancellation.reconciler.child_groups")
	contentionCounter, _ := meter.Int64Counter("run_cancellation.reconciler.lock_contention")
	ready := &atomic.Bool{}
	ready.Store(true)
	health := healthServer(configuration.healthAddress, ready, agentPool, runtimePool, authority, storeEpoch, blobs, vaultReader, telemetry.MetricsHandler())
	errorsChannel := make(chan error, 1)
	go func() {
		if serveErr := health.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errorsChannel <- serveErr
		}
	}()
	logger.Info("run cancellation reconciler ready", "store_epoch", storeEpoch, "shards", configuration.shardCount)
	var runErr error
	for runErr == nil {
		cycleCtx, cycleCancel := context.WithTimeout(ctx, configuration.cycleTimeout)
		result, cycleErr := coordinator.RunOnce(cycleCtx)
		cycleCancel()
		cycleCounter.Add(ctx, 1)
		settledCounter.Add(ctx, int64(result.Settled))
		deferredCounter.Add(ctx, int64(result.Deferred))
		failureCounter.Add(ctx, int64(result.Failed))
		childGroupCounter.Add(ctx, int64(result.ChildGroupCancellations))
		contentionCounter.Add(ctx, int64(result.ShardContended+result.TenantContended))
		if cycleErr != nil {
			logger.Error("run cancellation reconciliation cycle degraded", "error", cycleErr, "cancellations", result.Cancellations, "child_group_cancellations", result.ChildGroupCancellations, "failed", result.Failed)
		} else if result.Cancellations > 0 || result.ChildGroupCancellations > 0 {
			logger.Info("run cancellations reconciled", "cancellations", result.Cancellations, "child_group_cancellations", result.ChildGroupCancellations, "settled", result.Settled, "deferred", result.Deferred)
		}
		timer := time.NewTimer(configuration.interval)
		select {
		case <-ctx.Done():
			runErr = ctx.Err()
		case runErr = <-errorsChannel:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	ready.Store(false)
	shutdown, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	_ = health.Shutdown(shutdown)
	if errors.Is(runErr, context.Canceled) && ctx.Err() != nil {
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

func healthServer(address string, ready *atomic.Bool, agentPool, runtimePool *pgxpool.Pool, authority epoch.HTTPAuthority, expected string, blobs s3store.Store, vault *vaultkeys.ClientReader, metrics http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	mux.HandleFunc("GET /live", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /ready", func(writer http.ResponseWriter, request *http.Request) {
		check, cancel := context.WithTimeout(request.Context(), 3*time.Second)
		defer cancel()
		current, err := authority.CurrentStoreEpoch(check)
		if !ready.Load() || err != nil || current != expected || agentPool.Ping(check) != nil || runtimePool.Ping(check) != nil || blobs.Ready(check) != nil || vault.Ready(check) != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: 4 * time.Second, IdleTimeout: 30 * time.Second}
}

func dependencyHTTPClient(caFile, certFile, keyFile, serverName string, allow bool) (*http.Client, error) {
	tlsConfiguration := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: serverName}
	if caFile != "" {
		contents, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(contents) {
			return nil, errors.New("invalid CA")
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
	transport := &http.Transport{TLSClientConfig: tlsConfiguration, Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true}
	if allow && caFile == "" && certFile == "" {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}, nil
}
