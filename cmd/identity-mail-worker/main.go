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
	identitymail "github.com/langshift/lites/internal/identity/mail"
	"github.com/langshift/lites/internal/objectstore/s3store"
	"github.com/langshift/lites/internal/observability"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/payload/vaultkeys"
	"github.com/langshift/lites/internal/security/opaque"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
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
		logger.Error("identity mail worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, configuration config, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	inboxPepper, err := readBase64Secret(configuration.inboxPepperFile)
	if err != nil {
		return errors.New("load inbox lease pepper")
	}
	epochToken := ""
	if configuration.epochTokenFile != "" {
		epochToken, err = readSecret(configuration.epochTokenFile, 8192)
		if err != nil {
			return errors.New("load epoch authority credential")
		}
	}
	epochClient, err := configuration.epochClient()
	if err != nil {
		return err
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
	poolConfig.MaxConns = int32(configuration.concurrency + 4)
	poolConfig.MinConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("connect database")
	}
	defer pool.Close()
	connection, js, err := natsjs.Connect(natsjs.ConnectionConfig{URLs: configuration.natsURLs, Name: "lites-identity-mail-worker", CredentialsFile: configuration.natsCredentialsFile, RootCAFile: configuration.natsCAFile, ClientCertificateFile: configuration.natsCertFile, ClientKeyFile: configuration.natsKeyFile, ConnectTimeout: 5 * time.Second, ReconnectWait: 2 * time.Second, AllowInsecureDevelopment: configuration.allowInsecureDevelopment, OnDisconnect: func(err error) { logger.Warn("nats disconnected", "error", err) }, OnReconnect: func(url string) { logger.Info("nats reconnected", "url", url) }, OnClosed: func(err error) { logger.Error("nats closed", "error", err) }})
	if err != nil {
		return errors.New("connect nats")
	}
	defer connection.Close()
	s3Client, err := s3store.NewClient(ctx, s3store.ClientConfig{Region: configuration.s3Region, Endpoint: configuration.s3Endpoint, UsePathStyle: configuration.s3PathStyle, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure object store")
	}
	payloadBlobs := s3store.Store{Client: s3Client, Bucket: configuration.payloadBucket, Prefix: configuration.payloadPrefix, MaxBytes: 32 << 20, ServerSideEncryption: configuration.s3Encryption, KMSKeyID: configuration.s3KMSKeyID, RequireDigestMetadata: true}
	vaultReader, err := vaultkeys.NewClientReader(vaultkeys.ClientConfig{Address: configuration.vaultAddress, Namespace: configuration.vaultNamespace, Mount: configuration.vaultMount, TokenFile: configuration.vaultTokenFile, CACertificateFile: configuration.vaultCAFile, ClientCertificateFile: configuration.vaultCertFile, ClientKeyFile: configuration.vaultKeyFile, TLSServerName: configuration.vaultTLSServerName, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure Vault")
	}
	sender, appURL, err := configuration.smtp()
	if err != nil {
		return err
	}
	dependencyCtx, dependencyCancel := context.WithTimeout(ctx, 5*time.Second)
	if pool.Ping(dependencyCtx) != nil || payloadBlobs.Ready(dependencyCtx) != nil || vaultReader.Ready(dependencyCtx) != nil || sender.Ready(dependencyCtx) != nil {
		dependencyCancel()
		return errors.New("mail worker dependency is not ready")
	}
	dependencyCancel()
	payloadStore := payload.EnvelopeStore{Keys: vaultkeys.Provider{KV: vaultReader, Prefix: configuration.vaultKeyPrefix}, Blobs: payloadBlobs}
	mailTypes := []string{"identity.email.verify", "identity.email.password_reset", "identity.email.change.verify", "identity.email.invitation", "identity.email.security"}
	filterSubjects := make([]string, 0, len(mailTypes))
	for _, commandType := range mailTypes {
		subject, subjectErr := natsjs.SubjectFor(commandType)
		if subjectErr != nil {
			return errors.New("mail command subject is not registered")
		}
		filterSubjects = append(filterSubjects, subject)
	}
	provisionCtx, provisionCancel := context.WithTimeout(ctx, 15*time.Second)
	source, err := (natsjs.DurableConsumer{Stream: configuration.streamName, Name: configuration.consumerName, FilterSubjects: filterSubjects, Replicas: configuration.streamReplicas, AckWait: 2 * time.Minute, Backoff: []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute}, MaxDeliver: 20, MaxAckPending: configuration.concurrency * 2, MaxRequestBatch: configuration.concurrency, MaxRequestMaxBytes: configuration.concurrency * (64 << 10)}).Provision(provisionCtx, js)
	provisionCancel()
	if err != nil {
		return errors.New("provision identity mail consumer")
	}
	telemetry, err := observability.New(ctx, observability.Config{ServiceName: "identity-mail-worker", ServiceVersion: configuration.serviceVersion, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpBearerTokenFile, TraceSampleRatio: configuration.traceSampleRatio, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure observability")
	}
	deliveryErrors, _ := telemetry.Meter("github.com/langshift/lites/cmd/identity-mail-worker").Int64Counter("identity.mail.delivery.errors")
	inbox := eventpostgres.InboxStore{Pool: pool, Epochs: authority, Tokens: opaque.Manager{Purpose: "identity-mail-inbox-lease", Pepper: inboxPepper}, LeaseTTL: 5 * time.Minute}
	dispatcher := identitymail.Dispatcher{Payloads: payloadStore, Inbox: inbox, Sender: sender, AppURL: appURL, StoreEpoch: storeEpoch, ConsumerName: configuration.consumerName}
	consumer := natsjs.Consumer{Source: source, Handle: func(ctx context.Context, command eventpostgres.DeliveredCommand) error {
		_, dispatchErr := dispatcher.Dispatch(ctx, command)
		return dispatchErr
	}, OnError: func(deliveryContext context.Context, deliveryErr error) {
		deliveryErrors.Add(deliveryContext, 1)
		logger.Error("identity mail delivery", "error", deliveryErr)
	}, Concurrency: configuration.concurrency, PullExpires: 30 * time.Second, HeartbeatInterval: 20 * time.Second, BusyDelay: 15 * time.Second, RetryDelay: 30 * time.Second, AckTimeout: 5 * time.Minute}
	health := healthServer(configuration.healthAddress, pool, connection, js, authority, storeEpoch, payloadBlobs, vaultReader, sender, telemetry.MetricsHandler())
	errChannel := make(chan error, 3)
	go func() {
		if serveErr := health.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errChannel <- serveErr
		}
	}()
	go func() { errChannel <- consumer.Run(ctx) }()
	go func() { errChannel <- monitorStoreEpoch(ctx, authority, storeEpoch, 5*time.Second, logger) }()
	logger.Info("identity mail worker ready", "consumer", configuration.consumerName, "store_epoch", storeEpoch, "concurrency", configuration.concurrency)
	var runErr error
	select {
	case <-parent.Done():
	case runErr = <-errChannel:
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

func healthServer(address string, pool *pgxpool.Pool, connection *nats.Conn, js jetstream.JetStream, authority eventpostgres.EpochAuthority, expectedEpoch string, payloads s3store.Store, vault *vaultkeys.ClientReader, sender identitymail.SMTPSender, metrics http.Handler) *http.Server {
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
		if pool.Ping(ctx) != nil || natsjs.Ready(ctx, connection, js) != nil || epochErr != nil || currentEpoch != expectedEpoch || payloads.Ready(ctx) != nil || vault.Ready(ctx) != nil || sender.Ready(ctx) != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: 4 * time.Second, IdleTimeout: 30 * time.Second}
}
