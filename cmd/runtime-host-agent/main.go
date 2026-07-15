package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
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
	runtimecontract "github.com/langshift/lites/internal/runtime"
	runtimeapi "github.com/langshift/lites/internal/runtime/api"
	"github.com/langshift/lites/internal/runtime/controller"
	"github.com/langshift/lites/internal/runtime/firecracker"
	runtimepostgres "github.com/langshift/lites/internal/runtime/postgres"
	"github.com/langshift/lites/internal/security/trustedcontext"
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
		logger.Error("runtime host agent stopped", "error", err)
		os.Exit(1)
	}
}

type versionFence struct {
	mu      sync.Mutex
	version uint64
}

func run(parent context.Context, configuration config, logger *slog.Logger) error {
	if syscall.Geteuid() != 0 && !configuration.allowInsecureDevelopment {
		return errors.New("runtime host agent must run as root on a dedicated node")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	idKey, err := readBase64Key(configuration.idKeyFile)
	if err != nil {
		return errors.New("load runtime id key")
	}
	tokenPepper, err := readBase64Key(configuration.tokenPepperFile)
	if err != nil {
		return errors.New("load runtime token pepper")
	}
	ownershipKey, err := readBase64Key(configuration.ownershipKeyFile)
	if err != nil {
		return errors.New("load runtime ownership key")
	}
	hostControl, err := readSecret(configuration.hostControlTokenFile, 8192)
	if err != nil || len(hostControl) < 32 {
		return errors.New("load runtime host control token")
	}
	hostControlHash := sha256.Sum256([]byte(hostControl))
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
	poolConfig.MaxConns, poolConfig.MinConns = 32, 4
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("connect database")
	}
	defer pool.Close()
	keys, keyWindows, err := trustedcontext.LoadPublicKeyring(configuration.capabilityKeyringFile)
	if err != nil {
		return errors.New("load runtime capability keyring")
	}
	windows := make(map[string]runtimecontract.CapabilityKeyWindow, len(keyWindows))
	for keyID, window := range keyWindows {
		windows[keyID] = runtimecontract.CapabilityKeyWindow{NotBefore: window.NotBefore, NotAfter: window.NotAfter}
	}
	s3Client, err := s3store.NewClient(ctx, s3store.ClientConfig{Region: configuration.s3Region, Endpoint: configuration.s3Endpoint, UsePathStyle: configuration.s3PathStyle, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure object store")
	}
	payloadBlobs := s3store.Store{Client: s3Client, Bucket: configuration.payloadBucket, Prefix: configuration.payloadPrefix, MaxBytes: 32 << 20, ServerSideEncryption: configuration.s3Encryption, KMSKeyID: configuration.s3KMSKeyID, RequireDigestMetadata: true}
	vaultReader, err := vaultkeys.NewClientReader(vaultkeys.ClientConfig{Address: configuration.vaultAddress, Namespace: configuration.vaultNamespace, Mount: configuration.vaultMount, TokenFile: configuration.vaultTokenFile, CACertificateFile: configuration.vaultCAFile, ClientCertificateFile: configuration.vaultCertFile, ClientKeyFile: configuration.vaultKeyFile, TLSServerName: configuration.vaultTLSName, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure Vault")
	}
	payloadStore := payload.EnvelopeStore{Keys: vaultkeys.Provider{KV: vaultReader, Prefix: configuration.vaultKeyPrefix}, Blobs: payloadBlobs}
	dependencyCtx, dependencyCancel := context.WithTimeout(ctx, 5*time.Second)
	dependencyErr := pool.Ping(dependencyCtx)
	if dependencyErr == nil {
		dependencyErr = payloadBlobs.Ready(dependencyCtx)
	}
	if dependencyErr == nil {
		dependencyErr = vaultReader.Ready(dependencyCtx)
	}
	dependencyCancel()
	if dependencyErr != nil {
		return errors.New("runtime dependency is not ready")
	}
	store := runtimepostgres.Store{Pool: pool, Appender: eventpostgres.Appender{}, Epochs: authority, StoreEpoch: storeEpoch, IDKey: idKey, TokenPepper: tokenPepper, Verifier: runtimecontract.CapabilityVerifier{Issuer: configuration.capabilityIssuer, Audience: configuration.capabilityAudience, Keys: keys, KeyWindows: windows, MaximumTTL: 5 * time.Minute, ClockSkew: 5 * time.Second}}
	jailer := firecracker.JailerConfig{JailerPath: configuration.jailerPath, FirecrackerPath: configuration.firecrackerPath, ChrootBaseDir: configuration.chrootBaseDir, UID: configuration.jailerUID, GID: configuration.jailerGID, ParentCgroup: configuration.parentCgroup, CPUQuotaMicros: configuration.cpuQuotaMicros, CPUPeriodMicros: configuration.cpuPeriodMicros, MemoryMaxBytes: configuration.memoryMaxBytes, PidsMax: configuration.pidsMax, FileSizeMaxBytes: configuration.fileSizeMaxBytes, NoFileMax: configuration.noFileMax, NetworkNamespace: configuration.networkNamespace}
	if _, err = jailer.Arguments("configuration-probe"); err != nil {
		return errors.New("invalid Firecracker jailer configuration")
	}
	for _, asset := range []firecracker.Asset{{Path: configuration.jailerPath, Digest: configuration.jailerDigest}, {Path: configuration.firecrackerPath, Digest: configuration.firecrackerDigest}, {Path: configuration.kernelPath, Digest: configuration.kernelDigest}, {Path: configuration.rootfsPath, Digest: configuration.rootfsDigest}, {Path: configuration.scratchPath, Digest: configuration.scratchDigest}} {
		actual, digestErr := firecracker.AssetDigest(asset.Path)
		if digestErr != nil || actual != asset.Digest {
			return errors.New("Firecracker asset catalog integrity check failed")
		}
	}
	ownership := firecracker.OwnershipStore{Jailer: jailer, HostID: configuration.hostID, Key: ownershipKey, HostOwnerUID: 0}
	runner := firecracker.Runner{Jailer: jailer, StartupTimeout: 10 * time.Second, PollInterval: 10 * time.Millisecond, APITimeout: 2 * time.Second, StopGrace: 5 * time.Second}
	stager := firecracker.Stager{Jailer: jailer, Kernel: firecracker.Asset{Path: configuration.kernelPath, Digest: configuration.kernelDigest}, RootFS: firecracker.Asset{Path: configuration.rootfsPath, Digest: configuration.rootfsDigest}, Scratch: firecracker.Asset{Path: configuration.scratchPath, Digest: configuration.scratchDigest}, HostOwnerUID: 0, SourceOwnerUID: 0, MaximumKernelBytes: configuration.maximumKernelBytes, MaximumRootFSBytes: configuration.maximumRootFSBytes, MaximumScratchBytes: configuration.maximumScratchBytes}
	controllerErrors := make(chan error, 1)
	runtimeController := &controller.Controller{Store: store, Recovery: store, Payloads: payloadStore, Stager: stager, Runner: runner, Ownership: ownership, OnError: func(value error) {
		select {
		case controllerErrors <- value:
		default:
		}
	}}
	now := time.Now().UTC().Truncate(time.Microsecond)
	hostVersion, err := store.RegisterHost(ctx, runtimepostgres.HostRegistration{HostID: configuration.hostID, PoolKey: configuration.poolKey, Architecture: configuration.architecture, AvailabilityZone: configuration.availabilityZone, KernelCatalogHash: configuration.kernelCatalogHash, RootFSCatalogHash: configuration.rootfsCatalogHash, ScratchDigest: configuration.scratchDigest, ControlTokenHash: hostControlHash[:], CapacityVCPU: configuration.capacityVCPU, CapacityMemoryMiB: configuration.capacityMemoryMiB, CapacityDiskMiB: configuration.capacityDiskMiB, CapacitySessions: configuration.capacitySessions, ObservedAt: now, HeartbeatDeadline: now.Add(configuration.heartbeatTTL)})
	if err != nil {
		return errors.New("register runtime host")
	}
	fence := &versionFence{version: hostVersion}
	recovered, err := runtimeController.Recover(ctx, controller.RecoverRequest{HostID: configuration.hostID, HostControlHash: hostControlHash[:], AllowedExecutables: []string{configuration.jailerPath, configuration.firecrackerPath}, StopGrace: 5 * time.Second})
	if err != nil {
		return errors.New("recover runtime host ownership")
	}
	if err = fence.update(ctx, func(version uint64) (uint64, error) {
		at := time.Now().UTC()
		return store.SetHostStatus(ctx, configuration.hostID, version, hostControlHash[:], "active", at, at.Add(configuration.heartbeatTTL))
	}); err != nil {
		return errors.New("activate runtime host")
	}
	telemetry, err := observability.New(ctx, observability.Config{ServiceName: "runtime-host-agent", ServiceVersion: configuration.serviceVersion, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpBearerTokenFile, TraceSampleRatio: configuration.traceSampleRatio, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure observability")
	}
	ready := &atomic.Bool{}
	ready.Store(true)
	serverTLS, err := serverTLSConfig(configuration.serverCertFile, configuration.serverKeyFile, configuration.serverClientCAFile, configuration.allowInsecureDevelopment)
	if err != nil {
		return err
	}
	controlServer := &http.Server{Addr: configuration.listenAddress, Handler: telemetry.WrapHTTP(runtimeapi.Handler{Controller: runtimeController, HostID: configuration.hostID, RequireVerifiedClientCertificate: !configuration.allowInsecureDevelopment, AllowedClientSPIFFEID: configuration.serverAllowedClientSPIFFEID, RequestTimeout: 30 * time.Second}), TLSConfig: serverTLS, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 35 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10}
	healthServer := runtimeHealth(configuration.healthAddress, ready, pool, authority, storeEpoch, payloadBlobs, vaultReader, telemetry.MetricsHandler())
	errorsChannel := make(chan error, 5)
	go serve(controlServer, serverTLS != nil, errorsChannel)
	go func() {
		if serveErr := healthServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errorsChannel <- serveErr
		}
	}()
	go heartbeatLoop(ctx, store, fence, configuration, hostControlHash[:], errorsChannel)
	go reconcileLoop(ctx, runtimeController, configuration, hostControlHash[:], errorsChannel)
	go epochLoop(ctx, authority, storeEpoch, errorsChannel)
	logger.Info("runtime host agent ready", "host_id", configuration.hostID, "store_epoch", storeEpoch, "adopted", recovered.Adopted, "terminated", recovered.Terminated, "cleaned", recovered.Cleaned)
	var runErr error
	select {
	case <-parent.Done():
	case runErr = <-errorsChannel:
	case runErr = <-controllerErrors:
	}
	ready.Store(false)
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), configuration.shutdownTimeout)
	defer shutdownCancel()
	if err = fence.update(shutdownCtx, func(version uint64) (uint64, error) {
		at := time.Now().UTC()
		return store.SetHostStatus(shutdownCtx, configuration.hostID, version, hostControlHash[:], "draining", at, at.Add(configuration.heartbeatTTL))
	}); err == nil {
		_, err = runtimeController.Drain(shutdownCtx, "runtime_host_shutdown")
	}
	if err == nil {
		err = fence.update(shutdownCtx, func(version uint64) (uint64, error) {
			at := time.Now().UTC()
			return store.SetHostStatus(shutdownCtx, configuration.hostID, version, hostControlHash[:], "retired", at, at.Add(configuration.heartbeatTTL))
		})
	}
	_ = controlServer.Shutdown(shutdownCtx)
	_ = healthServer.Shutdown(shutdownCtx)
	if telemetryErr := telemetry.Shutdown(shutdownCtx); telemetryErr != nil && runErr == nil {
		runErr = telemetryErr
	}
	if err != nil && runErr == nil {
		runErr = err
	}
	return runErr
}

func (fence *versionFence) update(ctx context.Context, operation func(uint64) (uint64, error)) error {
	fence.mu.Lock()
	defer fence.mu.Unlock()
	next, err := operation(fence.version)
	if err == nil {
		fence.version = next
	}
	return err
}
func heartbeatLoop(ctx context.Context, store runtimepostgres.Store, fence *versionFence, configuration config, control []byte, output chan<- error) {
	ticker := time.NewTicker(configuration.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := fence.update(ctx, func(version uint64) (uint64, error) {
				at := time.Now().UTC()
				return store.HeartbeatHost(ctx, configuration.hostID, version, control, at, at.Add(configuration.heartbeatTTL))
			})
			if err != nil {
				output <- err
				return
			}
		}
	}
}
func reconcileLoop(ctx context.Context, runtimeController *controller.Controller, configuration config, control []byte, output chan<- error) {
	ticker := time.NewTicker(configuration.recoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := runtimeController.ReconcileTerminations(ctx, controller.ReconcileRequest{HostID: configuration.hostID, HostControlHash: control}); err != nil {
				output <- err
				return
			}
		}
	}
}
func epochLoop(ctx context.Context, authority epoch.HTTPAuthority, expected string, output chan<- error) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check, cancel := context.WithTimeout(ctx, 3*time.Second)
			current, err := authority.CurrentStoreEpoch(check)
			cancel()
			if err == nil && current != expected {
				output <- runtimepostgres.ErrStaleEpoch
				return
			}
		}
	}
}
func serve(server *http.Server, secure bool, output chan<- error) {
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		output <- err
		return
	}
	if secure {
		err = server.ServeTLS(listener, "", "")
	} else {
		err = server.Serve(listener)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		output <- err
	}
}
func runtimeHealth(address string, ready *atomic.Bool, pool *pgxpool.Pool, authority epoch.HTTPAuthority, expected string, blobs s3store.Store, vault *vaultkeys.ClientReader, metrics http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	mux.HandleFunc("GET /live", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		current, err := authority.CurrentStoreEpoch(ctx)
		if !ready.Load() || err != nil || current != expected || pool.Ping(ctx) != nil || blobs.Ready(ctx) != nil || vault.Ready(ctx) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
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
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig, Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true}
	if allow && caFile == "" && certFile == "" {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}, nil
}
func serverTLSConfig(certFile, keyFile, caFile string, allow bool) (*tls.Config, error) {
	if allow && certFile == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(contents) {
		return nil, errors.New("invalid client CA")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert}, nil
}
