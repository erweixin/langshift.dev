package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
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
	"github.com/langshift/lites/internal/runtime/firecracker"
	"github.com/langshift/lites/internal/runtime/sessionrequest"
	"github.com/langshift/lites/internal/security/opaque"
	"github.com/langshift/lites/internal/toolworker"
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
		logger.Error("tool worker stopped", "error", err)
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
	runtimeIDKey, err := readBase64(configuration.runtimeIDKeyFile, 32)
	if err != nil {
		return errors.New("load Runtime ID key")
	}
	runtimeNonceKey, err := readBase64(configuration.runtimeNonceKeyFile, 32)
	if err != nil {
		return errors.New("load Runtime nonce key")
	}
	runtimePepper, err := readBase64(configuration.runtimeTokenPepperFile, 32)
	if err != nil {
		return errors.New("load Runtime token pepper")
	}
	provisionKey, err := readBase64(configuration.provisionDerivationKeyFile, 32)
	if err != nil {
		return errors.New("load provision derivation key")
	}
	machineKey, err := readBase64(configuration.machineIdentityKeyFile, 32)
	if err != nil {
		return errors.New("load machine identity key")
	}
	keys := [][]byte{executionIDKey, executionPepper, runtimeIDKey, runtimeNonceKey, runtimePepper, provisionKey, machineKey}
	for left := range keys {
		for right := left + 1; right < len(keys); right++ {
			if bytes.Equal(keys[left], keys[right]) {
				return errors.New("ToolWorker cryptographic purposes must use distinct keys")
			}
		}
	}
	privateKeyBytes, err := readBase64(configuration.capabilityPrivateKeyFile, ed25519.PrivateKeySize)
	if err != nil {
		return errors.New("load Runtime capability signing key")
	}
	endpoints, err := loadHostEndpoints(configuration.hostEndpointsFile, configuration.allowInsecure)
	if err != nil {
		return err
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
	agentPool, err := openPool(parent, configuration.databaseURL, 32, 4)
	if err != nil {
		return errors.New("connect execution database")
	}
	defer agentPool.Close()
	runtimePool, err := openPool(parent, configuration.runtimeDatabaseURL, 16, 3)
	if err != nil {
		return errors.New("connect Runtime database")
	}
	defer runtimePool.Close()
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
	hostHTTP, err := dependencyHTTPClient(configuration.hostCAFile, configuration.hostCertFile, configuration.hostKeyFile, configuration.hostTLSName, configuration.allowInsecure, 0)
	if err != nil {
		return errors.New("configure Runtime Host transport")
	}
	connection, js, err := natsjs.Connect(natsjs.ConnectionConfig{URLs: configuration.natsURLs, Name: configuration.natsName, CredentialsFile: configuration.natsCredentialsFile, RootCAFile: configuration.natsCAFile, ClientCertificateFile: configuration.natsCertFile, ClientKeyFile: configuration.natsKeyFile, ConnectTimeout: 5 * time.Second, ReconnectWait: 2 * time.Second, AllowInsecureDevelopment: configuration.allowInsecure, OnDisconnect: func(err error) { logger.Warn("nats disconnected", "error", err) }, OnReconnect: func(url string) { logger.Info("nats reconnected", "url", url) }})
	if err != nil {
		return errors.New("connect NATS")
	}
	defer connection.Close()
	subject, _ := natsjs.DispatchSubjectFor("ExecuteToolCall")
	backoff := []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}
	consumerSource, err := (natsjs.DurableConsumer{Stream: configuration.streamName, Name: configuration.consumerName, FilterSubjects: []string{subject}, Replicas: configuration.streamReplicas, AckWait: configuration.ackWait, Backoff: backoff, MaxDeliver: configuration.maxDeliver, MaxAckPending: configuration.maxAckPending, MaxRequestBatch: configuration.concurrency, MaxRequestMaxBytes: 64 << 10}).Provision(parent, js)
	if err != nil {
		return errors.New("provision ToolWorker durable consumer")
	}
	telemetry, err := observability.New(parent, observability.Config{ServiceName: "tool-worker", ServiceVersion: configuration.version, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpTokenFile, TraceSampleRatio: configuration.traceRatio, AllowInsecureDevelopment: configuration.allowInsecure})
	if err != nil {
		return errors.New("configure observability")
	}
	actor := json.RawMessage(`{"kind":"service","name":"tool-worker"}`)
	leaseTokens := opaque.Manager{Purpose: "tool-worker-execution-lease-v1", Pepper: executionPepper}
	runStore := executionpostgres.RunStore{Pool: agentPool, Appender: eventpostgres.Appender{}, IDKey: executionIDKey, StoreEpoch: storeEpoch, Epochs: authority, Tokens: leaseTokens, LeaseTTL: configuration.executionLeaseTTL, RequireDispatchFence: true}
	sessionStore := sessionrequest.Store{Pool: agentPool, Appender: eventpostgres.Appender{}, Epochs: authority, StoreEpoch: storeEpoch, IDKey: runtimeIDKey, NonceKey: runtimeNonceKey, ExecutionTokens: leaseTokens}
	issuer := &sessionrequest.Issuer{Store: sessionStore, Issuer: configuration.capabilityIssuer, Audience: configuration.capabilityAudience, KeyID: configuration.capabilityKeyID, PrivateKey: ed25519.PrivateKey(privateKeyBytes), TTL: configuration.capabilityTTL}
	scratch := firecracker.RateLimit{Bandwidth: firecracker.TokenBucket{Size: configuration.scratchBandwidthSize, RefillMillis: configuration.scratchRefillMillis, Burst: configuration.scratchBandwidthBurst}, Operations: firecracker.TokenBucket{Size: configuration.scratchOperationsSize, RefillMillis: configuration.scratchRefillMillis, Burst: configuration.scratchOperationsBurst}}
	broker := &toolworker.PostgresSandboxBroker{Pool: agentPool, PlacementPool: runtimePool, Issuer: issuer, Payloads: payloads, Endpoints: endpoints, HTTPClient: hostHTTP, AllowInsecureDevelopment: configuration.allowInsecure, ProvisionTokenPepper: runtimePepper, ProvisionTokenDerivationKey: provisionKey, MachineIdentityKey: machineKey, Actor: actor, ScratchRate: scratch}
	production, err := toolworker.NewProductionRuntime(toolworker.ProductionConfig{ArtifactPath: configuration.artifactPath, ArtifactFileHash: configuration.artifactHash, Pool: agentPool, Payloads: payloads, Tools: runStore, SandboxBroker: broker, SandboxCleanupTimeout: configuration.cleanupTimeout, ConsumerName: configuration.consumerName, WorkerID: configuration.workerID, Actor: actor, IDKey: executionIDKey, HeartbeatInterval: configuration.toolHeartbeat, MaximumCommand: configuration.maximumCommand, MaximumInput: configuration.maximumInput, MaximumResult: configuration.maximumResult, Resume: toolworker.Schedule{QueueClass: "interactive", ResourceClass: "llm", Priority: 50, CostUnits: 4, MaxAttempts: 5}, Reconcile: toolworker.Schedule{QueueClass: "background", ResourceClass: "tool-reconciliation", Priority: 40, CostUnits: 1, MaxAttempts: 20}, DefaultReconcileAfter: time.Minute})
	if err != nil {
		return fmt.Errorf("compose production ToolWorker: %w", err)
	}
	dependencyCtx, dependencyCancel := context.WithTimeout(parent, 5*time.Second)
	dependencyErr := dependenciesReady(dependencyCtx, agentPool, runtimePool, connection, js, authority, storeEpoch, blobs.Ready, vaultReader.Ready)
	dependencyCancel()
	if dependencyErr != nil {
		return errors.New("ToolWorker dependency is not ready")
	}
	meter := telemetry.Meter("github.com/langshift/lites/cmd/tool-worker")
	failed, _ := meter.Int64Counter("tool_worker.deliveries.failed")
	consumer := natsjs.Consumer{Source: consumerSource, Handle: production.Handler.Handle, Dispatch: true, Concurrency: configuration.concurrency, PullExpires: configuration.pullExpires, HeartbeatInterval: configuration.messageHeartbeat, BusyDelay: configuration.busyDelay, RetryDelay: configuration.retryDelay, AckTimeout: configuration.ackTimeout, OnError: func(_ context.Context, failure error) {
		failed.Add(parent, 1)
		logger.Error("tool delivery", "error", failure)
	}}
	ready := &atomic.Bool{}
	ready.Store(true)
	health := healthServer(configuration.healthAddress, ready, agentPool, runtimePool, connection, js, authority, storeEpoch, blobs.Ready, vaultReader.Ready, telemetry.MetricsHandler())
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	errs := make(chan error, 2)
	go func() {
		if serveErr := health.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errs <- serveErr
		}
	}()
	go func() { errs <- consumer.Run(ctx) }()
	logger.Info("tool worker ready", "consumer", configuration.consumerName, "worker_id", configuration.workerID, "registry_hash", production.Artifact.RegistryHash, "store_epoch", storeEpoch)
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

type endpointEnvelope struct {
	SchemaVersion int                                       `json:"schema_version"`
	Hosts         map[string]toolworker.SandboxHostEndpoint `json:"hosts"`
}

func loadHostEndpoints(path string, allowInsecure bool) (map[string]toolworker.SandboxHostEndpoint, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open Runtime Host inventory")
	}
	defer file.Close()
	var envelope endpointEnvelope
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.SchemaVersion != 1 || len(envelope.Hosts) == 0 || len(envelope.Hosts) > 4096 {
		return nil, errors.New("Runtime Host inventory is invalid")
	}
	hostPattern := regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{2,127}$`)
	digestPattern := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	for hostID, endpoint := range envelope.Hosts {
		if !hostPattern.MatchString(hostID) || !digestPattern.MatchString(endpoint.KernelDigest) || !digestPattern.MatchString(endpoint.RootFSDigest) || secureEndpoint(endpoint.URL, allowInsecure) != nil {
			return nil, errors.New("Runtime Host inventory entry is invalid")
		}
	}
	return envelope.Hosts, nil
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
	transport.MaxIdleConns = 256
	transport.MaxIdleConnsPerHost = 64
	if timeout > 0 {
		transport.ResponseHeaderTimeout = timeout
	}
	if allowInsecure && caFile == "" && certFile == "" {
		transport.TLSClientConfig = nil
	} else {
		transport.TLSClientConfig = tlsConfig
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

type readiness func(context.Context) error

func dependenciesReady(ctx context.Context, agentPool, runtimePool *pgxpool.Pool, connection *nats.Conn, js jetstream.JetStream, authority epoch.HTTPAuthority, expectedEpoch string, checks ...readiness) error {
	if agentPool.Ping(ctx) != nil || runtimePool.Ping(ctx) != nil || natsjs.Ready(ctx, connection, js) != nil {
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

func healthServer(address string, ready *atomic.Bool, agentPool, runtimePool *pgxpool.Pool, connection *nats.Conn, js jetstream.JetStream, authority epoch.HTTPAuthority, expectedEpoch string, blobReady, vaultReady readiness, metrics http.Handler) *http.Server {
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
		if !ready.Load() || dependenciesReady(ctx, agentPool, runtimePool, connection, js, authority, expectedEpoch, blobReady, vaultReady) != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
}
