package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/eventbus/natsjs"
	"github.com/langshift/lites/internal/eventstore/epoch"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/erasure"
	identitypostgres "github.com/langshift/lites/internal/identity/postgres"
	"github.com/langshift/lites/internal/objectstore/s3store"
	"github.com/langshift/lites/internal/observability"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/payload/vaultkeys"
	platformratelimit "github.com/langshift/lites/internal/platform/ratelimit"
	"github.com/langshift/lites/internal/security/opaque"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	valkey "github.com/valkey-io/valkey-go"
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
		logger.Error("account erasure worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, configuration config, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	inboxPepper, err := readBase64Secret(configuration.inboxPepperFile)
	if err != nil {
		return errors.New("load erasure inbox pepper")
	}
	identityKey, err := readBase64Secret(configuration.identityKeyFile)
	if err != nil {
		return errors.New("load erasure identity key")
	}
	receiptKey, err := readBase64Secret(configuration.receiptKeyFile)
	if err != nil {
		return errors.New("load erasure receipt key")
	}
	epochToken := ""
	if configuration.epochTokenFile != "" {
		epochToken, err = readSecret(configuration.epochTokenFile, 8192)
		if err != nil {
			return errors.New("load epoch authority credential")
		}
	}
	epochClient, err := newEpochHTTPClient(configuration)
	if err != nil {
		return err
	}
	authority := epoch.HTTPAuthority{Endpoint: configuration.epochURL, BearerToken: epochToken, Client: epochClient, AllowInsecureLoopback: configuration.allowInsecureDevelopment}
	dependencyCtx, dependencyCancel := context.WithTimeout(ctx, 5*time.Second)
	storeEpoch, err := authority.CurrentStoreEpoch(dependencyCtx)
	dependencyCancel()
	if err != nil {
		return errors.New("read authoritative store epoch")
	}
	poolConfig, err := pgxpool.ParseConfig(configuration.databaseURL)
	if err != nil {
		return errors.New("parse database configuration")
	}
	poolConfig.MaxConns = int32(configuration.concurrency + 6)
	poolConfig.MinConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("connect database")
	}
	defer pool.Close()
	connection, js, err := natsjs.Connect(natsjs.ConnectionConfig{URLs: configuration.natsURLs, Name: "lites-account-erasure-worker", CredentialsFile: configuration.natsCredentialsFile, RootCAFile: configuration.natsCAFile, ClientCertificateFile: configuration.natsCertFile, ClientKeyFile: configuration.natsKeyFile, ConnectTimeout: 5 * time.Second, ReconnectWait: 2 * time.Second, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("connect nats")
	}
	defer connection.Close()
	s3Client, err := s3store.NewClient(ctx, s3store.ClientConfig{Region: configuration.s3Region, Endpoint: configuration.s3Endpoint, UsePathStyle: configuration.s3PathStyle, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure object store")
	}
	payloadObjects := s3store.Store{Client: s3Client, Bucket: configuration.payloadBucket, Prefix: configuration.payloadPrefix, MaxBytes: 32 << 20, ServerSideEncryption: configuration.s3Encryption, KMSKeyID: configuration.s3KMSKeyID, RequireDigestMetadata: true}
	artifactObjects := s3store.Store{Client: s3Client, Bucket: configuration.artifactBucket, Prefix: configuration.artifactPrefix, MaxBytes: 5 << 30, ServerSideEncryption: configuration.s3Encryption, KMSKeyID: configuration.s3KMSKeyID, RequireDigestMetadata: true}
	objectPurger := s3store.PurgeRouter{Stores: []s3store.Store{payloadObjects, artifactObjects}}
	payloadKeys, err := vaultkeys.NewClientReader(vaultkeys.ClientConfig{Address: configuration.vaultAddress, Namespace: configuration.vaultNamespace, Mount: configuration.vaultPayloadMount, TokenFile: configuration.vaultTokenFile, CACertificateFile: configuration.vaultCAFile, ClientCertificateFile: configuration.vaultCertFile, ClientKeyFile: configuration.vaultKeyFile, TLSServerName: configuration.vaultTLSName, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure payload Vault")
	}
	transitKeys, err := vaultkeys.NewClientReader(vaultkeys.ClientConfig{Address: configuration.vaultAddress, Namespace: configuration.vaultNamespace, Mount: configuration.vaultTransitMount, TokenFile: configuration.vaultTokenFile, CACertificateFile: configuration.vaultCAFile, ClientCertificateFile: configuration.vaultCertFile, ClientKeyFile: configuration.vaultKeyFile, TLSServerName: configuration.vaultTLSName, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure memory Transit Vault")
	}
	cacheClient, err := platformratelimit.NewClient(platformratelimit.ClientConfig{Addresses: configuration.valkeyAddresses, Username: configuration.valkeyUsername, PasswordFile: configuration.valkeyPasswordFile, RootCAFile: configuration.valkeyCAFile, ClientCertificateFile: configuration.valkeyCertFile, ClientKeyFile: configuration.valkeyKeyFile, TLSServerName: configuration.valkeyTLSName, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure Valkey")
	}
	defer cacheClient.Close()
	dependencyCtx, dependencyCancel = context.WithTimeout(ctx, 5*time.Second)
	if pool.Ping(dependencyCtx) != nil || payloadObjects.Ready(dependencyCtx) != nil || artifactObjects.Ready(dependencyCtx) != nil || payloadKeys.Ready(dependencyCtx) != nil || transitKeys.Ready(dependencyCtx) != nil || platformratelimit.Ready(dependencyCtx, cacheClient) != nil {
		dependencyCancel()
		return errors.New("erasure dependency is not ready")
	}
	dependencyCancel()
	payloadStore := payload.EnvelopeStore{Keys: vaultkeys.Provider{KV: payloadKeys, Prefix: configuration.vaultPayloadKeyPrefix}, Blobs: payloadObjects}
	telemetry, err := observability.New(ctx, observability.Config{ServiceName: "account-erasure-worker", ServiceVersion: configuration.serviceVersion, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpBearerTokenFile, TraceSampleRatio: configuration.traceSampleRatio, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure observability")
	}
	appender := eventpostgres.Appender{Observer: telemetry.AgentMetrics()}
	store := identitypostgres.AccountErasureStore{Pool: pool, IdentityKey: identityKey, Payloads: payloadStore, Appender: appender, StoreEpoch: storeEpoch}
	cachePurger := erasure.TaggedSubjectCache{Client: cacheClient, Namespace: "lites", ReceiptKey: receiptKey}
	indexPurger := identitypostgres.ObjectBackedIndexPurger{Pool: pool}
	erasors := make(map[erasure.Surface]erasure.SurfaceEraser, len(erasure.RequiredSurfaces))
	for _, erasureSurface := range erasure.RequiredSurfaces {
		erasors[erasureSurface] = identitypostgres.AccountErasureSurface{Pool: pool, Surface: erasureSurface, Objects: objectPurger, Keys: transitKeys, Indexes: indexPurger, Caches: cachePurger}
	}
	service := erasure.Service{Store: store, Erasers: erasors, RecoveryEpoch: storeEpoch, ReceiptKey: receiptKey}
	inbox := eventpostgres.InboxStore{Pool: pool, Epochs: authority, Tokens: opaque.Manager{Purpose: "account-erasure-inbox-lease", Pepper: inboxPepper}, LeaseTTL: 30 * time.Minute}
	dispatcher := identitypostgres.AccountErasureDispatcher{Service: service, Payloads: payloadStore, Inbox: inbox, StoreEpoch: storeEpoch, ConsumerName: configuration.consumerName}
	subject, _ := natsjs.SubjectFor(identitypostgres.AccountErasureScheduleCommand)
	provisionCtx, provisionCancel := context.WithTimeout(ctx, 15*time.Second)
	source, err := (natsjs.DurableConsumer{Stream: configuration.streamName, Name: configuration.consumerName, FilterSubjects: []string{subject}, Replicas: configuration.streamReplicas, AckWait: 30 * time.Minute, Backoff: []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour}, MaxDeliver: 30, MaxAckPending: configuration.concurrency, MaxRequestBatch: configuration.concurrency, MaxRequestMaxBytes: configuration.concurrency * (64 << 10)}).Provision(provisionCtx, js)
	provisionCancel()
	if err != nil {
		return errors.New("provision account erasure consumer")
	}
	meter := telemetry.Meter("github.com/langshift/lites/cmd/account-erasure-worker")
	deliveryErrors, _ := meter.Int64Counter("account.erasure.delivery.errors")
	deliveryCompleted, _ := meter.Int64Counter("account.erasure.delivery.completed")
	erasuresCompleted, _ := meter.Int64Counter("account.erasure.subjects.completed")
	receiptsVerified, _ := meter.Int64Counter("account.erasure.receipts.verified")
	deliveryReplays, _ := meter.Int64Counter("account.erasure.delivery.replays")
	terminalDeliveries, _ := meter.Int64Counter("account.erasure.delivery.terminal")
	deliveryDuration, _ := meter.Float64Histogram("account.erasure.delivery.duration.seconds")
	restoreCycles, _ := meter.Int64Counter("account.erasure.restore.cycles")
	restoreReplays, _ := meter.Int64Counter("account.erasure.restore.subjects.replayed")
	restoreErrors, _ := meter.Int64Counter("account.erasure.restore.errors")
	consumer := natsjs.Consumer{Source: source, Handle: func(deliveryContext context.Context, command eventpostgres.DeliveredCommand) error {
		started := time.Now()
		result, dispatchErr := dispatcher.Dispatch(deliveryContext, command)
		deliveryDuration.Record(deliveryContext, time.Since(started).Seconds())
		if result.Completed {
			deliveryCompleted.Add(deliveryContext, 1)
		}
		if result.Erasure.Completed {
			erasuresCompleted.Add(deliveryContext, 1)
			receiptsVerified.Add(deliveryContext, int64(len(result.Erasure.Receipts)))
		}
		if result.Replayed || result.Erasure.Replayed {
			deliveryReplays.Add(deliveryContext, 1)
		}
		if result.TerminalFailure {
			terminalDeliveries.Add(deliveryContext, 1)
		}
		return dispatchErr
	}, OnError: func(deliveryContext context.Context, deliveryErr error) {
		deliveryErrors.Add(deliveryContext, 1)
		logger.Error("account erasure delivery", "error", deliveryErr)
	}, Concurrency: configuration.concurrency, PullExpires: 30 * time.Second, HeartbeatInterval: 20 * time.Second, BusyDelay: 30 * time.Second, RetryDelay: time.Minute, AckTimeout: 30 * time.Minute}
	health := erasureHealth(configuration.healthAddress, pool, connection, js, authority, storeEpoch, payloadObjects, artifactObjects, payloadKeys, transitKeys, cacheClient, telemetry.MetricsHandler())
	errChannel := make(chan error, 4)
	go func() {
		if serveErr := health.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errChannel <- serveErr
		}
	}()
	go func() { errChannel <- consumer.Run(ctx) }()
	go func() { errChannel <- monitorEpoch(ctx, authority, storeEpoch, logger) }()
	go func() {
		errChannel <- reconcileRestores(ctx, service, configuration.restoreInterval, logger, func(reconcileContext context.Context, count int, reconcileErr error) {
			restoreCycles.Add(reconcileContext, 1)
			if reconcileErr != nil {
				restoreErrors.Add(reconcileContext, 1)
			} else if count > 0 {
				restoreReplays.Add(reconcileContext, int64(count))
				receiptsVerified.Add(reconcileContext, int64(count*len(erasure.RequiredSurfaces)))
			}
		})
	}()
	logger.Info("account erasure worker ready", "consumer", configuration.consumerName, "recovery_epoch", storeEpoch, "concurrency", configuration.concurrency)
	var runErr error
	select {
	case <-parent.Done():
	case runErr = <-errChannel:
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	_ = health.Shutdown(shutdownCtx)
	if err = connection.Drain(); err != nil && runErr == nil {
		runErr = err
	}
	if err = telemetry.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = err
	}
	return runErr
}

func reconcileRestores(ctx context.Context, service erasure.Service, interval time.Duration, logger *slog.Logger, observe func(context.Context, int, error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		results, err := service.ReconcileRestore(ctx, 100)
		if observe != nil {
			observe(ctx, len(results), err)
		}
		if err != nil {
			logger.Error("subject erasure restore reconciliation", "error", err)
		} else if len(results) > 0 {
			logger.Info("subject erasure tombstones replayed", "count", len(results), "recovery_epoch", service.RecoveryEpoch)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func monitorEpoch(ctx context.Context, authority eventpostgres.EpochAuthority, expected string, logger *slog.Logger) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			current, err := authority.CurrentStoreEpoch(checkCtx)
			cancel()
			if err != nil {
				logger.Warn("store epoch authority unavailable", "error", err)
				continue
			}
			if current != expected {
				return fmt.Errorf("store epoch changed from %s to %s", expected, current)
			}
		}
	}
}

func erasureHealth(address string, pool *pgxpool.Pool, connection *nats.Conn, js jetstream.JetStream, authority eventpostgres.EpochAuthority, expectedEpoch string, payloads, artifacts s3store.Store, payloadVault, transitVault *vaultkeys.ClientReader, cache valkey.Client, metrics http.Handler) *http.Server {
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
		current, epochErr := authority.CurrentStoreEpoch(ctx)
		if pool.Ping(ctx) != nil || natsjs.Ready(ctx, connection, js) != nil || epochErr != nil || current != expectedEpoch || payloads.Ready(ctx) != nil || artifacts.Ready(ctx) != nil || payloadVault.Ready(ctx) != nil || transitVault.Ready(ctx) != nil || platformratelimit.Ready(ctx, cache) != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: 4 * time.Second, IdleTimeout: 30 * time.Second}
}
