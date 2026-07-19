package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/agentworker"
	billingpostgres "github.com/langshift/lites/internal/billing/postgres"
	"github.com/langshift/lites/internal/eventbus/natsjs"
	"github.com/langshift/lites/internal/eventstore/epoch"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/llmgateway"
	"github.com/langshift/lites/internal/llmgateway/egress"
	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/llmgateway/vaultsecrets"
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
	if err == nil {
		err = run(ctx, configuration, logger)
	}
	if err != nil {
		logger.Error("agent worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, configuration config, logger *slog.Logger) error {
	localCompose := configuration.environment == "engineering-test" && os.Getenv("LITES_LOCAL_COMPOSE") == "true"
	executionIDKey, err := readBase64(configuration.executionIDKeyFile, 32)
	if err != nil {
		return errors.New("load execution ID key")
	}
	executionPepper, err := readBase64(configuration.executionLeasePepperFile, 32)
	if err != nil {
		return errors.New("load execution lease pepper")
	}
	llmIDKey, err := readBase64(configuration.llmIDKeyFile, 32)
	if err != nil {
		return errors.New("load LLM ID key")
	}
	llmPepper, err := readBase64(configuration.llmTokenPepperFile, 32)
	if err != nil {
		return errors.New("load LLM token pepper")
	}
	billingIDKey, err := readBase64(configuration.billingIDKeyFile, 32)
	if err != nil {
		return errors.New("load billing ID key")
	}
	agentIDKey, err := readBase64(configuration.agentIDKeyFile, 32)
	if err != nil {
		return errors.New("load Agent ID key")
	}
	keys := [][]byte{executionIDKey, executionPepper, llmIDKey, llmPepper, billingIDKey, agentIDKey}
	for left := range keys {
		for right := left + 1; right < len(keys); right++ {
			if bytes.Equal(keys[left], keys[right]) {
				return errors.New("AgentWorker cryptographic purposes must use distinct keys")
			}
		}
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
	pool, err := openPool(parent, configuration.databaseURL, 64, 8)
	if err != nil {
		return errors.New("connect Agent database")
	}
	defer pool.Close()
	s3Client, err := s3store.NewClient(parent, s3store.ClientConfig{Region: configuration.s3Region, Endpoint: configuration.s3Endpoint, UsePathStyle: configuration.s3PathStyle, AllowInsecureDevelopment: configuration.allowInsecure, AllowLocalCompose: localCompose})
	if err != nil {
		return errors.New("configure object store")
	}
	blobs := s3store.Store{Client: s3Client, Bucket: configuration.payloadBucket, Prefix: configuration.payloadPrefix, MaxBytes: 32 << 20, ServerSideEncryption: configuration.s3Encryption, KMSKeyID: configuration.s3KMSKeyID, RequireDigestMetadata: true}
	vaultReader, err := vaultkeys.NewClientReader(vaultkeys.ClientConfig{Address: configuration.vaultAddress, Namespace: configuration.vaultNamespace, Mount: configuration.vaultMount, TokenFile: configuration.vaultTokenFile, CACertificateFile: configuration.vaultCAFile, ClientCertificateFile: configuration.vaultCertFile, ClientKeyFile: configuration.vaultKeyFile, TLSServerName: configuration.vaultTLSName, AllowInsecureDevelopment: configuration.allowInsecure, AllowLocalCompose: localCompose})
	if err != nil {
		return errors.New("configure Vault")
	}
	payloadKeys, err := vaultkeys.ProviderForEnvironment(configuration.environment, localCompose, os.Getenv("LITES_LOCAL_PAYLOAD_KEY_SEED_FILE"), vaultReader, configuration.vaultKeyPrefix)
	if err != nil {
		return errors.New("configure payload keys")
	}
	payloads := payload.EnvelopeStore{Keys: payloadKeys, Blobs: blobs}
	providers, err := provider.LoadRegistryFile(configuration.providerPath, configuration.providerHash)
	if err != nil {
		return errors.New("load provider registry")
	}
	providerRoots, err := providerRootCAs(configuration.providerRootCAFile)
	if err != nil {
		return errors.New("load provider CA")
	}
	connection, js, err := natsjs.Connect(natsjs.ConnectionConfig{URLs: configuration.natsURLs, Name: configuration.natsName, CredentialsFile: configuration.natsCredentialsFile, RootCAFile: configuration.natsCAFile, ClientCertificateFile: configuration.natsCertFile, ClientKeyFile: configuration.natsKeyFile, ConnectTimeout: 5 * time.Second, ReconnectWait: 2 * time.Second, AllowInsecureDevelopment: configuration.allowInsecure, OnDisconnect: func(err error) { logger.Warn("nats disconnected", "error", err) }, OnReconnect: func(url string) { logger.Info("nats reconnected", "url", url) }})
	if err != nil {
		return errors.New("connect NATS")
	}
	defer connection.Close()
	startSubject, _ := natsjs.DispatchSubjectFor("StartAgentRun")
	resumeSubject, _ := natsjs.DispatchSubjectFor("ResumeAgentRun")
	backoff := []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}
	consumerSource, err := (natsjs.DurableConsumer{Stream: configuration.streamName, Name: configuration.consumerName, FilterSubjects: []string{startSubject, resumeSubject}, Replicas: configuration.streamReplicas, AckWait: configuration.ackWait, Backoff: backoff, MaxDeliver: configuration.maxDeliver, MaxAckPending: configuration.maxAckPending, MaxRequestBatch: configuration.concurrency, MaxRequestMaxBytes: 64 << 10}).Provision(parent, js)
	if err != nil {
		return errors.New("provision AgentWorker durable consumer")
	}
	telemetry, err := observability.New(parent, observability.Config{ServiceName: "agent-worker", ServiceVersion: configuration.version, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpTokenFile, TraceSampleRatio: configuration.traceRatio, AllowInsecureDevelopment: configuration.allowInsecure})
	if err != nil {
		return errors.New("configure observability")
	}
	payloads.Metrics = telemetry.AgentMetrics()
	actor := json.RawMessage(`{"kind":"service","name":"agent-worker"}`)
	appender := eventpostgres.Appender{Observer: telemetry.AgentMetrics()}
	billing := billingpostgres.Store{Pool: pool, Appender: appender, Epochs: authority, StoreEpoch: storeEpoch, IDKey: billingIDKey}
	attempts := llmpostgres.Store{Pool: pool, Appender: appender, Epochs: authority, StoreEpoch: storeEpoch, IDKey: llmIDKey, TokenPepper: llmPepper, Billing: billing}
	runs := executionpostgres.RunStore{Pool: pool, Appender: appender, IDKey: executionIDKey, StoreEpoch: storeEpoch, Epochs: authority, Tokens: opaque.Manager{Purpose: "agent-worker-execution-lease-v1", Pepper: executionPepper}, LeaseTTL: configuration.executionLeaseTTL, RequireDispatchFence: true}
	secretSource := vaultsecrets.MultiSource{Sources: []vaultsecrets.Source{
		{KV: vaultReader, Prefix: configuration.providerSecretPrefix, Field: "api_key"},
		{KV: vaultReader, Prefix: configuration.byokSecretPrefix, Field: "api_key"},
	}}
	gateway := llmgateway.Executor{Store: attempts, Registry: providers, Clients: llmgateway.ClientFactory{Policy: egress.EndpointPolicy{Resolver: egress.NetResolver{}, PrivateEngineeringTestHost: configuration.privateEngineeringProviderHost}, Secrets: secretSource, RootCAs: providerRoots}, Evidence: llmgateway.PayloadEvidence{Payloads: payloads}}
	production, err := agentworker.NewProductionRuntime(agentworker.ProductionConfig{Pool: pool, Payloads: payloads, Runs: runs, Attempts: attempts, Billing: billing, Gateway: gateway, Providers: providers, PromptArtifactPath: configuration.promptPath, PromptArtifactHash: configuration.promptHash, RouteArtifactPath: configuration.routePath, RouteArtifactHash: configuration.routeHash, ToolArtifactPath: configuration.toolPath, ToolArtifactHash: configuration.toolHash, ConsumerName: configuration.consumerName, WorkerID: configuration.workerID, Actor: actor, IDKey: agentIDKey, MaximumOutputTokens: configuration.maximumOutputTokens, MaximumCommand: configuration.maximumCommand, MaximumMessageBytes: configuration.maximumMessageBytes, MaximumMessages: configuration.maximumMessages, MaximumCalls: configuration.maximumCalls, HeartbeatInterval: configuration.agentHeartbeat, PrepareTTL: configuration.prepareTTL, CompletionTTL: configuration.completionTTL, ReconciliationDelay: configuration.reconciliationDelay, ApprovalTTL: configuration.approvalTTL, BucketTTL: configuration.bucketTTL, Metrics: telemetry.AgentMetrics(), Notification: agentworker.PlanSchedule{QueueClass: "interactive", ResourceClass: "notification", Priority: 50, CostUnits: 1, MaxAttempts: 10}})
	if err != nil {
		return errors.New("compose production AgentWorker")
	}
	dependencyCtx, dependencyCancel := context.WithTimeout(parent, 5*time.Second)
	dependencyErr := dependenciesReady(dependencyCtx, pool, connection, js, authority, storeEpoch, blobs.Ready, vaultReader.Ready)
	dependencyCancel()
	if dependencyErr != nil {
		return errors.New("AgentWorker dependency is not ready")
	}
	meter := telemetry.Meter("github.com/langshift/lites/cmd/agent-worker")
	failed, _ := meter.Int64Counter("agent_worker.deliveries.failed")
	consumer := natsjs.Consumer{Source: consumerSource, Handle: production.Handler.Handle, Dispatch: true, Concurrency: configuration.concurrency, PullExpires: configuration.pullExpires, HeartbeatInterval: configuration.messageHeartbeat, BusyDelay: configuration.busyDelay, RetryDelay: configuration.retryDelay, AckTimeout: configuration.ackTimeout, OnError: func(_ context.Context, failure error) {
		failed.Add(parent, 1)
		logger.Error("agent delivery", "error", failure)
	}}
	ready := &atomic.Bool{}
	ready.Store(true)
	health := healthServer(configuration.healthAddress, ready, pool, connection, js, authority, storeEpoch, blobs.Ready, vaultReader.Ready, telemetry.MetricsHandler())
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	errs := make(chan error, 2)
	go func() {
		if serveErr := health.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errs <- serveErr
		}
	}()
	go func() { errs <- consumer.Run(ctx) }()
	providerID, providerVersion, providerHash := providers.Snapshot()
	logger.Info("agent worker ready", "consumer", configuration.consumerName, "worker_id", configuration.workerID, "provider_registry", providerID, "provider_version", providerVersion, "provider_hash", providerHash, "prompt_artifact", production.PromptArtifact.ArtifactID, "route_artifact", production.RouteArtifact.ArtifactID, "tool_registry", production.ToolArtifact.RegistryID, "store_epoch", storeEpoch)
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

func providerRootCAs(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, err
	}
	if !roots.AppendCertsFromPEM(contents) {
		return nil, errors.New("provider CA contains no certificates")
	}
	return roots, nil
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
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: serverName}
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
		tlsConfig.RootCAs = roots
	}
	if certFile != "" {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns, transport.MaxIdleConnsPerHost, transport.ResponseHeaderTimeout = 256, 64, timeout
	if allowInsecure && caFile == "" && certFile == "" {
		transport.TLSClientConfig = nil
	} else {
		transport.TLSClientConfig = tlsConfig
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
