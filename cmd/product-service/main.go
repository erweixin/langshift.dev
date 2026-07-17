package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	behaviorpostgres "github.com/langshift/lites/internal/behavior/postgres"
	"github.com/langshift/lites/internal/eventstore/epoch"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/objectstore/s3store"
	"github.com/langshift/lites/internal/observability"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/payload/vaultkeys"
	productapi "github.com/langshift/lites/internal/product/api"
	"github.com/langshift/lites/internal/product/contentcatalog"
	productpostgres "github.com/langshift/lites/internal/product/postgres"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
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
		logger.Error("product service stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, configuration config, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	secrets, err := loadSecretBundle(configuration.secretBundleFile)
	if err != nil {
		return err
	}
	contentRelease, err := contentcatalog.Load(configuration.contentReleaseDirectory)
	if err != nil {
		return errors.New("load immutable product content release")
	}
	ontologySnapshotID := "ontology:" + contentRelease.Identity()
	contentSnapshotID := "content:" + contentRelease.Identity()
	trustedKeys, trustedWindows, err := trustedcontext.LoadPublicKeyring(configuration.trustedKeyringFile)
	if err != nil {
		return errors.New("load trusted context keyring")
	}
	epochToken := ""
	if configuration.epochTokenFile != "" {
		epochToken, err = readSecret(configuration.epochTokenFile, 8192)
		if err != nil {
			return errors.New("load epoch authority credential")
		}
	}
	epochClient, err := dependencyHTTPClient(configuration.epochCAFile, configuration.epochCertFile, configuration.epochKeyFile)
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
	poolConfig.MaxConns = int32(configuration.databaseMaxConnections)
	poolConfig.MinConns = 4
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
	blobs := s3store.Store{Client: s3Client, Bucket: configuration.payloadBucket, Prefix: configuration.payloadPrefix, MaxBytes: 4 << 20, ServerSideEncryption: configuration.s3Encryption, KMSKeyID: configuration.s3KMSKeyID, RequireDigestMetadata: true}
	vaultReader, err := vaultkeys.NewClientReader(vaultkeys.ClientConfig{Address: configuration.vaultAddress, Namespace: configuration.vaultNamespace, Mount: configuration.vaultMount, TokenFile: configuration.vaultTokenFile, CACertificateFile: configuration.vaultCAFile, ClientCertificateFile: configuration.vaultCertFile, ClientKeyFile: configuration.vaultKeyFile, TLSServerName: configuration.vaultTLSName, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure Vault")
	}
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
		return errors.New("product service dependency is not ready")
	}
	telemetry, err := observability.New(ctx, observability.Config{ServiceName: "product-service", ServiceVersion: configuration.serviceVersion, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpBearerTokenFile, TraceSampleRatio: configuration.traceSampleRatio, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure observability")
	}
	payloads := payload.EnvelopeStore{Keys: vaultkeys.Provider{KV: vaultReader, Prefix: configuration.vaultKeyPrefix}, Blobs: blobs}
	appender := eventpostgres.Appender{Observer: telemetry.AgentMetrics()}
	store := productpostgres.MissionFocusStore{Pool: pool, Appender: appender, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, Epochs: authority, Now: func() time.Time { return time.Now().UTC() }}
	service := productpostgres.MissionMutationService{Pool: pool, Store: store, Payloads: payloads, IDKey: secrets.IDKey, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper, IdempotencyTTL: configuration.idempotencyTTL, Now: func() time.Time { return time.Now().UTC() }}
	queries := productpostgres.MissionQueryService{Pool: pool, CursorKey: secrets.CursorKey}
	routeStore := productpostgres.RouteStore{Pool: pool, Appender: appender, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, Epochs: authority, Now: func() time.Time { return time.Now().UTC() }}
	behaviorStore := behaviorpostgres.Store{Pool: pool, Appender: appender, StoreEpoch: storeEpoch, Epochs: authority, Now: func() time.Time { return time.Now().UTC() }}
	runStore := executionpostgres.RunStore{Pool: pool, Appender: appender, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, Epochs: authority, Behavior: behaviorStore, Now: func() time.Time { return time.Now().UTC() }}
	routes := productpostgres.RouteService{Pool: pool, Store: routeStore, Payloads: payloads, IDKey: secrets.IDKey, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper, CursorKey: secrets.CursorKey, IdempotencyTTL: configuration.idempotencyTTL, BehaviorProfile: "route_planner", BehaviorEnvironment: configuration.routeBehaviorEnvironment, OntologySnapshotID: ontologySnapshotID, ContentSnapshotID: contentSnapshotID, Now: func() time.Time { return time.Now().UTC() }}
	tasks := productpostgres.DailyTaskService{Pool: pool, Appender: appender, Payloads: payloads, IDKey: secrets.IDKey, CursorKey: secrets.CursorKey, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper, StoreEpoch: storeEpoch, IdempotencyTTL: configuration.idempotencyTTL, Now: func() time.Time { return time.Now().UTC() }}
	submissions := productpostgres.SubmissionService{Pool: pool, Appender: appender, Payloads: payloads, IDKey: secrets.IDKey, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper, StoreEpoch: storeEpoch, IdempotencyTTL: configuration.idempotencyTTL, Now: func() time.Time { return time.Now().UTC() }}
	reviews := productpostgres.ReviewService{Pool: pool, Runs: runStore, Payloads: payloads, IDKey: secrets.IDKey, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper, BehaviorEnvironment: configuration.routeBehaviorEnvironment, RunTimeout: configuration.evaluatorRunTimeout, RunMaxSteps: configuration.evaluatorRunMaxSteps, RunMaxCostMicrounits: int64(configuration.evaluatorRunMaxCostMicrounits), RunMaxAttempts: configuration.evaluatorRunMaxAttempts, IdempotencyTTL: configuration.idempotencyTTL, Now: func() time.Time { return time.Now().UTC() }}
	evidence := productpostgres.EvidenceService{Pool: pool, Appender: appender, Payloads: payloads, IDKey: secrets.IDKey, CursorKey: secrets.CursorKey, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper, StoreEpoch: storeEpoch, IdempotencyTTL: configuration.idempotencyTTL, Now: func() time.Time { return time.Now().UTC() }}
	preferences := productpostgres.PreferencesService{Pool: pool, Appender: appender, Payloads: payloads, IDKey: secrets.IDKey, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper, StoreEpoch: storeEpoch, IdempotencyTTL: configuration.idempotencyTTL, Now: func() time.Time { return time.Now().UTC() }}
	reminders := productpostgres.ReminderService{Pool: pool, Appender: appender, Payloads: payloads, IDKey: secrets.IDKey, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper, StoreEpoch: storeEpoch, IdempotencyTTL: configuration.idempotencyTTL, Now: func() time.Time { return time.Now().UTC() }}
	projectStore := productpostgres.ProjectStore{Pool: pool, Appender: appender, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, Epochs: authority, Now: func() time.Time { return time.Now().UTC() }}
	projects := productpostgres.ProjectApplicationService{Pool: pool, Store: projectStore, Payloads: payloads, IDKey: secrets.IDKey, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper, CursorKey: secrets.CursorKey, IdempotencyTTL: configuration.idempotencyTTL, Now: func() time.Time { return time.Now().UTC() }}
	projectTests := productpostgres.ProjectTestGenerationService{Pool: pool, Runs: runStore, Payloads: payloads, IDKey: secrets.IDKey, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper, BehaviorEnvironment: configuration.routeBehaviorEnvironment, RunTimeout: configuration.evaluatorRunTimeout, RunMaxSteps: configuration.evaluatorRunMaxSteps, RunMaxCostMicrounits: int64(configuration.evaluatorRunMaxCostMicrounits), RunMaxAttempts: configuration.evaluatorRunMaxAttempts, IdempotencyTTL: configuration.idempotencyTTL, Now: func() time.Time { return time.Now().UTC() }}
	portfolioStore := productpostgres.PortfolioExportStore{Pool: pool, Appender: appender, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, Epochs: authority, Behavior: behaviorStore, Now: func() time.Time { return time.Now().UTC() }}
	portfolioExports := productpostgres.PortfolioExportService{Pool: pool, Store: portfolioStore, Payloads: payloads, IDKey: secrets.IDKey, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper, BehaviorEnvironment: configuration.routeBehaviorEnvironment, RunTimeout: configuration.artifactBuilderRunTimeout, RunMaxSteps: configuration.artifactBuilderRunMaxSteps, RunMaxCostMicrounits: int64(configuration.artifactBuilderRunMaxCostMicrounits), RunMaxAttempts: configuration.artifactBuilderRunMaxAttempts, IdempotencyTTL: configuration.idempotencyTTL, Now: func() time.Time { return time.Now().UTC() }}
	productMux := http.NewServeMux()
	missionHandler := productapi.MissionHandler{Queries: queries, Creates: service, Mutations: service}
	routeHandler := productapi.RouteHandler{Service: routes}
	taskHandler := productapi.DailyTaskHandler{Service: tasks}
	submissionHandler := productapi.SubmissionHandler{Service: submissions}
	reviewHandler := productapi.ReviewHandler{Service: reviews}
	evidenceHandler := productapi.EvidenceHandler{Service: evidence}
	preferencesHandler := productapi.PreferencesHandler{Service: preferences}
	reminderHandler := productapi.ReminderHandler{Service: reminders}
	projectHandler := productapi.ProjectHandler{Service: projects, Tests: projectTests}
	portfolioHandler := productapi.PortfolioHandler{Service: portfolioExports}
	productMux.Handle("/v1/missions", missionHandler)
	productMux.Handle("/v1/missions/", missionHandler)
	productMux.Handle("/v1/route-revisions", routeHandler)
	productMux.Handle("/v1/route-revisions/", routeHandler)
	productMux.Handle("/v1/daily-tasks", taskHandler)
	productMux.Handle("/v1/daily-tasks/", taskHandler)
	productMux.Handle("/v1/submissions", submissionHandler)
	productMux.Handle("/v1/reviews", reviewHandler)
	productMux.Handle("/v1/reviews/", reviewHandler)
	productMux.Handle("/v1/capability-evidence", evidenceHandler)
	productMux.Handle("/v1/preferences", preferencesHandler)
	productMux.Handle("/v1/reminder-schedules", reminderHandler)
	productMux.Handle("/v1/reminder-schedules/", reminderHandler)
	productMux.Handle("/v1/projects", projectHandler)
	productMux.Handle("/v1/projects/", projectHandler)
	productMux.Handle("/v1/portfolio-exports", portfolioHandler)
	productMux.Handle("/v1/portfolio-exports/", portfolioHandler)
	handler := serviceauth.Middleware{Verifier: trustedcontext.Verifier{Issuer: configuration.trustedIssuer, Audience: configuration.trustedAudience, Keys: trustedKeys, KeyWindows: trustedWindows, MaximumTTL: 2 * time.Minute, ClockSkew: 5 * time.Second}, Now: func() time.Time { return time.Now().UTC() }}.Wrap(productMux)
	tlsConfig, err := serverTLSConfig(configuration)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: configuration.listenAddress, Handler: telemetry.WrapHTTP(handler), TLSConfig: tlsConfig, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10}
	ready := &atomic.Bool{}
	ready.Store(true)
	healthServer := productHealth(configuration.healthAddress, ready, pool, authority, storeEpoch, blobs, vaultReader, telemetry.MetricsHandler())
	errChannel := make(chan error, 3)
	go serveHTTP(server, tlsConfig, errChannel)
	go func() {
		if serveErr := healthServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errChannel <- serveErr
		}
	}()
	go func() { errChannel <- monitorStoreEpoch(ctx, authority, storeEpoch, 5*time.Second, logger) }()
	logger.Info("product service ready", "address", configuration.listenAddress, "store_epoch", storeEpoch, "region", configuration.region, "content_snapshot_id", contentSnapshotID)
	var runErr error
	select {
	case <-parent.Done():
	case runErr = <-errChannel:
	}
	ready.Store(false)
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	_ = healthServer.Shutdown(shutdownCtx)
	if shutdownErr := telemetry.Shutdown(shutdownCtx); shutdownErr != nil && runErr == nil {
		runErr = shutdownErr
	}
	return runErr
}

func serveHTTP(server *http.Server, tlsConfig *tls.Config, errorsChannel chan<- error) {
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		errorsChannel <- err
		return
	}
	if tlsConfig != nil {
		err = server.ServeTLS(listener, "", "")
	} else {
		err = server.Serve(listener)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		errorsChannel <- err
	}
}

func serverTLSConfig(configuration config) (*tls.Config, error) {
	if configuration.allowInsecureDevelopment && configuration.serverCertificateFile == "" {
		return nil, nil
	}
	certificate, err := tls.LoadX509KeyPair(configuration.serverCertificateFile, configuration.serverKeyFile)
	if err != nil {
		return nil, errors.New("server TLS certificate is invalid")
	}
	contents, err := os.ReadFile(configuration.serverClientCAFile)
	if err != nil {
		return nil, errors.New("server client CA is unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(contents) {
		return nil, errors.New("server client CA is invalid")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert}, nil
}

func dependencyHTTPClient(caFile, certFile, keyFile string) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	if caFile != "" {
		contents, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(contents) {
			return nil, errors.New("invalid dependency CA")
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
	return &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func productHealth(address string, ready *atomic.Bool, pool *pgxpool.Pool, authority eventpostgres.EpochAuthority, expectedEpoch string, blobs s3store.Store, vault *vaultkeys.ClientReader, metrics http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	mux.HandleFunc("GET /live", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /ready", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
		defer cancel()
		currentEpoch, epochErr := authority.CurrentStoreEpoch(ctx)
		if !ready.Load() || pool.Ping(ctx) != nil || epochErr != nil || currentEpoch != expectedEpoch || blobs.Ready(ctx) != nil || vault.Ready(ctx) != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: 4 * time.Second, IdleTimeout: 30 * time.Second}
}

func monitorStoreEpoch(ctx context.Context, authority eventpostgres.EpochAuthority, expected string, interval time.Duration, logger *slog.Logger) error {
	if authority == nil || expected == "" || interval <= 0 {
		return errors.New("store epoch monitor is invalid")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			current, err := authority.CurrentStoreEpoch(checkCtx)
			cancel()
			if err != nil || current == "" {
				logger.Warn("store epoch authority unavailable", "error", err)
				continue
			}
			if current != expected {
				return fmt.Errorf("store epoch changed from %s to %s", expected, current)
			}
		}
	}
}
