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
	"github.com/langshift/lites/internal/eventstore/epoch"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/objectstore/s3store"
	"github.com/langshift/lites/internal/observability"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/payload/vaultkeys"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
	"github.com/langshift/lites/internal/runtime/sweeper"
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
		logger.Error("runtime sweeper stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configuration config, logger *slog.Logger) error {
	idKey, err := readBase64Key(configuration.idKeyFile)
	if err != nil {
		return errors.New("load runtime id key")
	}
	tokenPepper, err := readBase64Key(configuration.tokenPepperFile)
	if err != nil {
		return errors.New("load runtime token pepper")
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
	poolConfig, err := pgxpool.ParseConfig(configuration.databaseURL)
	if err != nil {
		return errors.New("parse database configuration")
	}
	poolConfig.MaxConns, poolConfig.MinConns = 16, 3
	poolConfig.MaxConnLifetime, poolConfig.MaxConnIdleTime = 30*time.Minute, 5*time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("connect database")
	}
	defer pool.Close()
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
	dependencyErr := pool.Ping(dependencyCtx)
	if dependencyErr == nil {
		dependencyErr = blobs.Ready(dependencyCtx)
	}
	if dependencyErr == nil {
		dependencyErr = vaultReader.Ready(dependencyCtx)
	}
	dependencyCancel()
	if dependencyErr != nil {
		return errors.New("runtime sweeper dependency is not ready")
	}
	telemetry, err := observability.New(ctx, observability.Config{ServiceName: "runtime-sweeper", ServiceVersion: configuration.serviceVersion, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpBearerTokenFile, TraceSampleRatio: configuration.traceSampleRatio, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure observability")
	}
	store := runtimepostgres.Store{Pool: pool, Appender: eventpostgres.Appender{}, Epochs: authority, StoreEpoch: storeEpoch, IDKey: idKey, TokenPepper: tokenPepper}
	locker := sweeper.PostgresLocker{Pool: pool, UnlockTimeout: configuration.unlockTimeout}
	coordinator := sweeper.Coordinator{Store: store, Payloads: payloadStore, Locker: locker, ShardCount: configuration.shardCount, TenantPage: configuration.tenantPage, SessionPage: configuration.sessionPage}
	meter := telemetry.Meter("github.com/langshift/lites/cmd/runtime-sweeper")
	cycleCounter, _ := meter.Int64Counter("runtime.sweeper.cycles")
	requestCounter, _ := meter.Int64Counter("runtime.sweeper.terminations.requested")
	contentionCounter, _ := meter.Int64Counter("runtime.sweeper.locks.contended")
	errorCounter, _ := meter.Int64Counter("runtime.sweeper.cycles.failed")
	ready := &atomic.Bool{}
	ready.Store(true)
	health := healthServer(configuration.healthAddress, ready, pool, authority, storeEpoch, blobs, vaultReader, telemetry.MetricsHandler())
	errorsChannel := make(chan error, 1)
	go func() {
		if serveErr := health.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errorsChannel <- serveErr
		}
	}()
	logger.Info("runtime sweeper ready", "store_epoch", storeEpoch, "shards", configuration.shardCount)
	var runErr error
	for runErr == nil {
		cycleCtx, cycleCancel := context.WithTimeout(ctx, configuration.cycleTimeout)
		result, cycleErr := coordinator.RunOnce(cycleCtx)
		cycleCancel()
		cycleCounter.Add(ctx, 1)
		if cycleErr != nil {
			errorCounter.Add(ctx, 1)
			runErr = cycleErr
			break
		}
		requestCounter.Add(ctx, int64(result.Sweep.Requested))
		contentionCounter.Add(ctx, int64(result.Contended+result.Sweep.Contended))
		if result.Sweep.Requested > 0 {
			logger.Info("runtime deadlines requested termination", "shards", result.Shards, "tenants", result.Sweep.Tenants, "requested", result.Sweep.Requested, "contended", result.Contended+result.Sweep.Contended)
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
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	_ = health.Shutdown(shutdownCtx)
	if telemetryErr := telemetry.Shutdown(shutdownCtx); telemetryErr != nil && (runErr == nil || errors.Is(runErr, context.Canceled)) {
		runErr = telemetryErr
	}
	if errors.Is(runErr, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return runErr
}

func healthServer(address string, ready *atomic.Bool, pool *pgxpool.Pool, authority epoch.HTTPAuthority, expected string, blobs s3store.Store, vault *vaultkeys.ClientReader, metrics http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	mux.HandleFunc("GET /live", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /ready", func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
		defer cancel()
		current, err := authority.CurrentStoreEpoch(ctx)
		if !ready.Load() || err != nil || current != expected || pool.Ping(ctx) != nil || blobs.Ready(ctx) != nil || vault.Ready(ctx) != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: 4 * time.Second, IdleTimeout: 30 * time.Second}
}

func dependencyHTTPClient(caFile, certFile, keyFile, serverName string, allow bool) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: serverName}
	if caFile != "" {
		contents, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(contents) {
			return nil, errors.New("invalid CA")
		}
		tlsConfig.RootCAs = roots
	}
	if certFile != "" {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig, Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true}
	if allow && caFile == "" && certFile == "" {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}, nil
}
