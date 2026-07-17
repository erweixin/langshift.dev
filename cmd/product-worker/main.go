package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/behavior"
	behaviorpostgres "github.com/langshift/lites/internal/behavior/postgres"
	"github.com/langshift/lites/internal/eventbus/natsjs"
	"github.com/langshift/lites/internal/eventstore/epoch"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/objectstore/s3store"
	"github.com/langshift/lites/internal/observability"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/payload/vaultkeys"
	"github.com/langshift/lites/internal/product/contentcatalog"
	productpostgres "github.com/langshift/lites/internal/product/postgres"
	productreminder "github.com/langshift/lites/internal/product/reminder"
	"github.com/langshift/lites/internal/security/opaque"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cfg, err := loadConfig()
	if err == nil {
		err = run(ctx, cfg, logger)
	}
	if err != nil {
		logger.Error("product worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, cfg config, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	secrets, err := loadWorkerSecrets(cfg.secretBundleFile)
	if err != nil {
		return err
	}
	contentRelease, err := contentcatalog.Load(cfg.contentReleaseDirectory)
	if err != nil {
		return errors.New("load immutable product content release")
	}
	ontologySnapshotID := "ontology:" + contentRelease.Identity()
	contentSnapshotID := "content:" + contentRelease.Identity()
	inboxPepper, err := readBase64(cfg.inboxPepperFile)
	if err != nil {
		return errors.New("load product inbox lease pepper")
	}
	epochToken := ""
	if cfg.epochTokenFile != "" {
		epochToken, err = readSecret(cfg.epochTokenFile, 8192)
		if err != nil {
			return errors.New("load epoch credential")
		}
	}
	epochClient, err := dependencyClient(cfg.epochCAFile, cfg.epochCertFile, cfg.epochKeyFile, cfg.allowInsecure)
	if err != nil {
		return errors.New("configure epoch transport")
	}
	authority := epoch.HTTPAuthority{Endpoint: cfg.epochURL, BearerToken: epochToken, Client: epochClient, AllowInsecureLoopback: cfg.allowInsecure}
	epochCtx, epochCancel := context.WithTimeout(ctx, 3*time.Second)
	storeEpoch, err := authority.CurrentStoreEpoch(epochCtx)
	epochCancel()
	if err != nil || storeEpoch == "" {
		return errors.New("read authoritative store epoch")
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.databaseURL)
	if err != nil {
		return errors.New("parse database configuration")
	}
	poolCfg.MaxConns = int32(cfg.concurrency + 12)
	poolCfg.MinConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return errors.New("connect database")
	}
	defer pool.Close()
	s3Client, err := s3store.NewClient(ctx, s3store.ClientConfig{Region: cfg.s3Region, Endpoint: cfg.s3Endpoint, UsePathStyle: cfg.s3PathStyle, AllowInsecureDevelopment: cfg.allowInsecure})
	if err != nil {
		return errors.New("configure object store")
	}
	blobs := s3store.Store{Client: s3Client, Bucket: cfg.payloadBucket, Prefix: cfg.payloadPrefix, MaxBytes: 4 << 20, ServerSideEncryption: cfg.s3Encryption, KMSKeyID: cfg.s3KMSKeyID, RequireDigestMetadata: true}
	vault, err := vaultkeys.NewClientReader(vaultkeys.ClientConfig{Address: cfg.vaultAddress, Namespace: cfg.vaultNamespace, Mount: cfg.vaultMount, TokenFile: cfg.vaultTokenFile, CACertificateFile: cfg.vaultCAFile, ClientCertificateFile: cfg.vaultCertFile, ClientKeyFile: cfg.vaultKeyFile, TLSServerName: cfg.vaultTLSName, AllowInsecureDevelopment: cfg.allowInsecure})
	if err != nil {
		return errors.New("configure Vault")
	}
	payloads := payload.EnvelopeStore{Keys: vaultkeys.Provider{KV: vault, Prefix: cfg.vaultKeyPrefix}, Blobs: blobs}
	reminderToken, err := readSecret(cfg.reminderTokenFile, 8192)
	if err != nil {
		return errors.New("load reminder delivery credential")
	}
	reminderEndpoint, err := url.Parse(cfg.reminderDeliveryURL)
	if err != nil {
		return errors.New("parse reminder delivery URL")
	}
	reminderReadiness, err := url.Parse(cfg.reminderReadinessURL)
	if err != nil {
		return errors.New("parse reminder readiness URL")
	}
	reminderClient, err := dependencyClient(cfg.reminderCAFile, cfg.reminderCertFile, cfg.reminderKeyFile, cfg.allowInsecure)
	if err != nil {
		return errors.New("configure reminder delivery transport")
	}
	reminderClient.Timeout = cfg.reminderDeliveryTimeout
	if transport, ok := reminderClient.Transport.(*http.Transport); ok && transport.TLSClientConfig != nil && cfg.reminderTLSName != "" {
		transport.TLSClientConfig.ServerName = cfg.reminderTLSName
	}
	reminderSender := productreminder.HTTPDeliverySender{Endpoint: reminderEndpoint, Readiness: reminderReadiness, Client: reminderClient, BearerToken: reminderToken, AllowInsecure: cfg.allowInsecure}
	connection, js, err := natsjs.Connect(natsjs.ConnectionConfig{URLs: cfg.natsURLs, Name: cfg.natsName, CredentialsFile: cfg.natsCredentialsFile, RootCAFile: cfg.natsCAFile, ClientCertificateFile: cfg.natsCertFile, ClientKeyFile: cfg.natsKeyFile, ConnectTimeout: 5 * time.Second, ReconnectWait: 2 * time.Second, AllowInsecureDevelopment: cfg.allowInsecure, OnDisconnect: func(err error) { logger.Warn("nats disconnected", "error", err) }, OnReconnect: func(url string) { logger.Info("nats reconnected", "url", url) }})
	if err != nil {
		return errors.New("connect NATS")
	}
	defer connection.Close()
	generateSubject, _ := natsjs.SubjectFor("GenerateMissionRoute")
	planningSubject, _ := natsjs.SubjectFor("RoutePlanningRequested")
	dailyTaskSubject, _ := natsjs.SubjectFor("GenerateDailyTask")
	reminderDeliverySubject, _ := natsjs.SubjectFor("DeliverReminder")
	source, err := (natsjs.DurableConsumer{Stream: cfg.streamName, Name: cfg.consumerName, FilterSubjects: []string{generateSubject, planningSubject, dailyTaskSubject, reminderDeliverySubject}, Replicas: cfg.streamReplicas, AckWait: cfg.ackWait, Backoff: []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute}, MaxDeliver: cfg.maxDeliver, MaxAckPending: cfg.concurrency * 2, MaxRequestBatch: cfg.concurrency, MaxRequestMaxBytes: cfg.concurrency * (64 << 10)}).Provision(ctx, js)
	if err != nil {
		return errors.New("provision product route planner consumer")
	}
	telemetry, err := observability.New(ctx, observability.Config{ServiceName: "product-worker", ServiceVersion: cfg.version, Environment: cfg.environment, Region: cfg.region, OTLPEndpoint: cfg.otlpEndpoint, OTLPRootCAFile: cfg.otlpCAFile, OTLPClientCertificateFile: cfg.otlpCertFile, OTLPClientKeyFile: cfg.otlpKeyFile, OTLPTLSServerName: cfg.otlpTLSName, OTLPBearerTokenFile: cfg.otlpTokenFile, TraceSampleRatio: cfg.traceRatio, AllowInsecureDevelopment: cfg.allowInsecure})
	if err != nil {
		return errors.New("configure observability")
	}
	appender := eventpostgres.Appender{Observer: telemetry.AgentMetrics()}
	now := func() time.Time { return time.Now().UTC() }
	routes := productpostgres.RouteStore{Pool: pool, Appender: appender, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, Epochs: authority, Now: now}
	routeService := productpostgres.RouteService{Pool: pool, Store: routes, Payloads: payloads, IDKey: secrets.IDKey, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper, CursorKey: secrets.CursorKey, IdempotencyTTL: cfg.idempotencyTTL, BehaviorProfile: string(behavior.RoutePlanner), BehaviorEnvironment: cfg.routeBehaviorEnvironment, OntologySnapshotID: ontologySnapshotID, ContentSnapshotID: contentSnapshotID, Now: now}
	behaviorStore := behaviorpostgres.Store{Pool: pool, Appender: appender, StoreEpoch: storeEpoch, Epochs: authority, Now: now}
	runs := executionpostgres.RunStore{Pool: pool, Appender: appender, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, Epochs: authority, Behavior: behaviorStore, Now: now}
	inbox := eventpostgres.InboxStore{Pool: pool, Epochs: authority, Tokens: opaque.Manager{Purpose: "product-route-planner-inbox-v1", Pepper: inboxPepper}, LeaseTTL: cfg.inboxLeaseTTL, Now: now}
	dispatcher := productpostgres.RoutePlannerDispatcher{Service: productpostgres.RoutePlannerService{Pool: pool, Routes: routes, Runs: runs, Payloads: payloads, IDKey: secrets.IDKey, RunTimeout: cfg.runTimeout, RunMaxSteps: cfg.runMaxSteps, RunMaxCostMicrounits: cfg.runMaxCostMicrounits, RunMaxAttempts: cfg.runMaxAttempts, QueuePriority: 100, Now: now}, Inbox: inbox, ConsumerName: cfg.consumerName}
	missionDispatcher := productpostgres.MissionRouteDispatcher{Routes: routeService, Inbox: inbox, ConsumerName: cfg.consumerName}
	reconciler := productpostgres.RoutePlannerReconciler{Pool: pool, Routes: routes, Payloads: payloads, IDKey: secrets.IDKey, Now: now}
	onboardingRouteReconciler := productpostgres.OnboardingRouteReconciler{Pool: pool, Appender: appender, Payloads: payloads, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, Now: now}
	dailyDispatcher := productpostgres.DailyTaskPlannerDispatcher{Service: productpostgres.DailyTaskPlannerService{Pool: pool, Routes: routes, Runs: runs, Payloads: payloads, IDKey: secrets.IDKey, BehaviorEnvironment: cfg.routeBehaviorEnvironment, ContentSnapshotID: contentSnapshotID, RunTimeout: cfg.runTimeout, RunMaxSteps: cfg.runMaxSteps, RunMaxCostMicrounits: cfg.runMaxCostMicrounits, RunMaxAttempts: cfg.runMaxAttempts, QueuePriority: 100, Now: now}, Inbox: inbox, ConsumerName: cfg.consumerName}
	dailyReconciler := productpostgres.DailyTaskPlannerReconciler{Pool: pool, Routes: routes, Payloads: payloads, IDKey: secrets.IDKey, Now: now}
	reviewReconciler := productpostgres.ReviewReconciler{Pool: pool, Appender: appender, Payloads: payloads, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, Now: now}
	projectStore := productpostgres.ProjectStore{Pool: pool, Appender: appender, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, Epochs: authority, Now: now}
	projectTestReconciler := productpostgres.ProjectTestReconciler{Pool: pool, Store: projectStore, Appender: appender, Payloads: payloads, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, Now: now}
	portfolioStore := productpostgres.PortfolioExportStore{Pool: pool, Appender: appender, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, Epochs: authority, Behavior: behaviorStore, Now: now}
	portfolioReconciler := productpostgres.PortfolioExportReconciler{Pool: pool, Store: portfolioStore, Appender: appender, Payloads: payloads, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, Retention: cfg.portfolioRetention, Now: now}
	reminderScheduler := productpostgres.ReminderScheduler{Pool: pool, Appender: appender, Payloads: payloads, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, Now: now}
	reminderDispatcher := productpostgres.ReminderDeliveryDispatcher{Pool: pool, Appender: appender, Payloads: payloads, Inbox: inbox, Sender: reminderSender, IDKey: secrets.IDKey, StoreEpoch: storeEpoch, ConsumerName: cfg.consumerName, Now: now}
	meter := telemetry.Meter("github.com/langshift/lites/cmd/product-worker")
	deliveryErrors, _ := meter.Int64Counter("product.route_planner.delivery.errors")
	reconciled, _ := meter.Int64Counter("product.route_planner.reconciled")
	remindersScheduled, _ := meter.Int64Counter("product.reminders.scheduled")
	portfolioFinalized, _ := meter.Int64Counter("product.portfolio_exports.finalized")
	consumer := natsjs.Consumer{Source: source, Handle: func(c context.Context, command eventpostgres.DeliveredCommand) error {
		switch command.CommandType {
		case "GenerateMissionRoute":
			_, e := missionDispatcher.Dispatch(c, command)
			return e
		case "RoutePlanningRequested":
			_, e := dispatcher.Dispatch(c, command)
			return e
		case "GenerateDailyTask":
			_, e := dailyDispatcher.Dispatch(c, command)
			return e
		case "DeliverReminder":
			_, e := reminderDispatcher.Dispatch(c, command)
			return e
		default:
			return productpostgres.ErrMissionRouteCommand
		}
	}, OnError: func(c context.Context, e error) {
		deliveryErrors.Add(c, 1)
		logger.Error("route planner delivery", "error", e)
	}, Concurrency: cfg.concurrency, PullExpires: cfg.pullExpires, HeartbeatInterval: cfg.heartbeat, BusyDelay: cfg.busyDelay, RetryDelay: cfg.retryDelay, AckTimeout: cfg.ackTimeout}
	dependencyCtx, dependencyCancel := context.WithTimeout(ctx, 5*time.Second)
	readyErr := dependenciesReady(dependencyCtx, pool, connection, js, authority, storeEpoch, blobs.Ready, vault.Ready, reminderSender.Ready)
	dependencyCancel()
	if readyErr != nil {
		return readyErr
	}
	ready := &atomic.Bool{}
	ready.Store(true)
	health := healthServer(cfg.healthAddress, ready, pool, connection, js, authority, storeEpoch, telemetry.MetricsHandler(), blobs.Ready, vault.Ready, reminderSender.Ready)
	errs := make(chan error, 8)
	go func() {
		if e := health.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
			errs <- e
		}
	}()
	go func() { errs <- consumer.Run(ctx) }()
	go func() { errs <- reconcileLoop(ctx, reconciler, cfg, logger, func(n int64) { reconciled.Add(ctx, n) }) }()
	go func() {
		errs <- onboardingRouteReconcileLoop(ctx, onboardingRouteReconciler, cfg, logger, func(n int64) { reconciled.Add(ctx, n) })
	}()
	go func() {
		errs <- dailyReconcileLoop(ctx, dailyReconciler, cfg, logger, func(n int64) { reconciled.Add(ctx, n) })
	}()
	go func() {
		errs <- reviewReconcileLoop(ctx, reviewReconciler, cfg, logger, func(n int64) { reconciled.Add(ctx, n) })
	}()
	go func() {
		errs <- projectTestReconcileLoop(ctx, projectTestReconciler, cfg, logger, func(n int64) { reconciled.Add(ctx, n) })
	}()
	go func() {
		errs <- portfolioReconcileLoop(ctx, portfolioReconciler, cfg, logger, func(n int64) { portfolioFinalized.Add(ctx, n) })
	}()
	go func() {
		errs <- reminderReconcileLoop(ctx, reminderScheduler, cfg, logger, func(n int64) { remindersScheduled.Add(ctx, n) })
	}()
	logger.Info("product worker ready", "consumer", cfg.consumerName, "store_epoch", storeEpoch, "concurrency", cfg.concurrency)
	var runErr error
	select {
	case <-parent.Done():
	case runErr = <-errs:
	}
	ready.Store(false)
	cancel()
	shutdown, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()
	_ = health.Shutdown(shutdown)
	_ = connection.Drain()
	if e := telemetry.Shutdown(shutdown); runErr == nil {
		runErr = e
	}
	if errors.Is(runErr, context.Canceled) && parent.Err() != nil {
		return nil
	}
	return runErr
}

func portfolioReconcileLoop(ctx context.Context, reconciler productpostgres.PortfolioExportReconciler, cfg config, logger *slog.Logger, observe func(int64)) error {
	ticker := time.NewTicker(cfg.portfolioReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			after := ""
			for {
				tenants, err := reconciler.ListTenantIDs(ctx, after, cfg.portfolioTenantBatch)
				if err != nil {
					return err
				}
				for _, tenantID := range tenants {
					result, reconcileErr := reconciler.ReconcileTenant(ctx, tenantID, cfg.portfolioExportBatch)
					if reconcileErr != nil {
						return reconcileErr
					}
					count := int64(result.Ready + result.Failed)
					if count > 0 {
						observe(count)
						logger.Info("portfolio export reconciliation", "tenant_id", tenantID, "ready", result.Ready, "failed", result.Failed, "pending", result.Pending)
					}
				}
				if len(tenants) < cfg.portfolioTenantBatch {
					break
				}
				after = tenants[len(tenants)-1]
			}
		}
	}
}

func projectTestReconcileLoop(ctx context.Context, reconciler productpostgres.ProjectTestReconciler, cfg config, logger *slog.Logger, observe func(int64)) error {
	ticker := time.NewTicker(cfg.reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			after := ""
			for {
				tenants, err := reconciler.ListTenantIDs(ctx, after, cfg.reconcileTenantBatch)
				if err != nil {
					return err
				}
				for _, tenant := range tenants {
					result, reconcileErr := reconciler.ReconcileTenant(ctx, tenant, cfg.reconcileRouteBatch)
					if reconcileErr != nil {
						return reconcileErr
					}
					count := int64(result.Succeeded + result.Superseded + result.Failed)
					if count > 0 {
						observe(count)
						logger.Info("project test reconciliation", "tenant_id", tenant, "succeeded", result.Succeeded, "superseded", result.Superseded, "failed", result.Failed)
					}
				}
				if len(tenants) < cfg.reconcileTenantBatch {
					break
				}
				after = tenants[len(tenants)-1]
			}
		}
	}
}

func reminderReconcileLoop(ctx context.Context, scheduler productpostgres.ReminderScheduler, cfg config, logger *slog.Logger, observe func(int64)) error {
	ticker := time.NewTicker(cfg.reminderReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			after := ""
			for {
				tenants, err := scheduler.ListTenantIDs(ctx, after, cfg.reminderTenantBatch)
				if err != nil {
					return err
				}
				for _, tenantID := range tenants {
					result, scheduleErr := scheduler.ScheduleTenant(ctx, tenantID, cfg.reminderScheduleBatch)
					if scheduleErr != nil {
						return scheduleErr
					}
					if result.Scheduled > 0 {
						observe(int64(result.Scheduled))
						logger.Info("reminder scheduling", "tenant_id", tenantID, "scheduled", result.Scheduled)
					}
				}
				if len(tenants) < cfg.reminderTenantBatch {
					break
				}
				after = tenants[len(tenants)-1]
			}
		}
	}
}

func reviewReconcileLoop(ctx context.Context, reconciler productpostgres.ReviewReconciler, cfg config, logger *slog.Logger, observe func(int64)) error {
	ticker := time.NewTicker(cfg.reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			after := ""
			for {
				tenants, err := reconciler.ListTenantIDs(ctx, after, cfg.reconcileTenantBatch)
				if err != nil {
					return err
				}
				for _, tenant := range tenants {
					result, err := reconciler.ReconcileTenant(ctx, tenant, cfg.reconcileRouteBatch)
					if err != nil {
						return err
					}
					count := int64(result.Succeeded + result.Superseded + result.Failed)
					if count > 0 {
						observe(count)
						logger.Info("submission review reconciliation", "tenant_id", tenant, "succeeded", result.Succeeded, "superseded", result.Superseded, "failed", result.Failed)
					}
				}
				if len(tenants) < cfg.reconcileTenantBatch {
					break
				}
				after = tenants[len(tenants)-1]
			}
		}
	}
}

func dailyReconcileLoop(ctx context.Context, reconciler productpostgres.DailyTaskPlannerReconciler, cfg config, logger *slog.Logger, observe func(int64)) error {
	ticker := time.NewTicker(cfg.reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			after := ""
			for {
				tenants, err := reconciler.ListTenantIDs(ctx, after, cfg.reconcileTenantBatch)
				if err != nil {
					return err
				}
				for _, tenant := range tenants {
					result, reconcileErr := reconciler.ReconcileTenant(ctx, tenant, cfg.reconcileRouteBatch)
					if reconcileErr != nil {
						return reconcileErr
					}
					count := int64(result.Scheduled + result.Superseded + result.Failed)
					if count > 0 {
						observe(count)
						logger.Info("daily task planner reconciliation", "tenant_id", tenant, "scheduled", result.Scheduled, "superseded", result.Superseded, "failed", result.Failed)
					}
				}
				if len(tenants) < cfg.reconcileTenantBatch {
					break
				}
				after = tenants[len(tenants)-1]
			}
		}
	}
}

func reconcileLoop(ctx context.Context, reconciler productpostgres.RoutePlannerReconciler, cfg config, logger *slog.Logger, observe func(int64)) error {
	ticker := time.NewTicker(cfg.reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			after := ""
			for {
				tenants, err := reconciler.ListTenantIDs(ctx, after, cfg.reconcileTenantBatch)
				if err != nil {
					return err
				}
				for _, tenant := range tenants {
					result, e := reconciler.ReconcileTenant(ctx, tenant, cfg.reconcileRouteBatch)
					if e != nil {
						return e
					}
					count := int64(result.Proposed + result.Stale + result.Failed)
					if count > 0 {
						observe(count)
						logger.Info("route planner reconciliation", "tenant_id", tenant, "proposed", result.Proposed, "stale", result.Stale, "failed", result.Failed)
					}
				}
				if len(tenants) < cfg.reconcileTenantBatch {
					break
				}
				after = tenants[len(tenants)-1]
			}
		}
	}
}

func onboardingRouteReconcileLoop(ctx context.Context, reconciler productpostgres.OnboardingRouteReconciler, cfg config, logger *slog.Logger, observe func(int64)) error {
	ticker := time.NewTicker(cfg.reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			after := ""
			for {
				tenants, err := reconciler.ListTenantIDs(ctx, after, cfg.reconcileTenantBatch)
				if err != nil {
					return err
				}
				for _, tenantID := range tenants {
					result, reconcileErr := reconciler.ReconcileTenant(ctx, tenantID, cfg.reconcileRouteBatch)
					if reconcileErr != nil {
						return reconcileErr
					}
					count := int64(result.Ready + result.Failed)
					if count > 0 {
						observe(count)
						logger.Info("onboarding route reconciliation", "tenant_id", tenantID, "ready", result.Ready, "failed", result.Failed)
					}
				}
				if len(tenants) < cfg.reconcileTenantBatch {
					break
				}
				after = tenants[len(tenants)-1]
			}
		}
	}
}

type readiness func(context.Context) error

func dependenciesReady(ctx context.Context, pool *pgxpool.Pool, nc *nats.Conn, js jetstream.JetStream, authority eventpostgres.EpochAuthority, epoch string, checks ...readiness) error {
	current, err := authority.CurrentStoreEpoch(ctx)
	if pool.Ping(ctx) != nil || natsjs.Ready(ctx, nc, js) != nil || err != nil || current != epoch {
		return errors.New("product worker dependency is not ready")
	}
	for _, check := range checks {
		if check(ctx) != nil {
			return errors.New("product worker encrypted payload dependency is not ready")
		}
	}
	return nil
}
func healthServer(address string, ready *atomic.Bool, pool *pgxpool.Pool, nc *nats.Conn, js jetstream.JetStream, authority eventpostgres.EpochAuthority, epoch string, metrics http.Handler, checks ...readiness) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	mux.HandleFunc("GET /live", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if !ready.Load() || dependenciesReady(ctx, pool, nc, js, authority, epoch, checks...) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: 4 * time.Second, IdleTimeout: 30 * time.Second}
}
func dependencyClient(caFile, certFile, keyFile string, allow bool) (*http.Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("invalid dependency CA")
		}
		tlsCfg.RootCAs = roots
	}
	if certFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if allow && caFile == "" && certFile == "" {
		transport.TLSClientConfig = nil
	} else {
		transport.TLSClientConfig = tlsCfg
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}, nil
}
