package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/eventstore/epoch"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/anonymoussession"
	identityapi "github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/identity/password"
	identitypostgres "github.com/langshift/lites/internal/identity/postgres"
	"github.com/langshift/lites/internal/objectstore/s3store"
	"github.com/langshift/lites/internal/observability"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/payload/vaultkeys"
	platformratelimit "github.com/langshift/lites/internal/platform/ratelimit"
	"github.com/langshift/lites/internal/security/opaque"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
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
		logger.Error("identity service stopped", "error", err)
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
	keys, windows, err := trustedcontext.LoadPublicKeyring(configuration.trustedKeyringFile)
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
	epochClient, err := newEpochHTTPClient(configuration)
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
	poolConfig.MaxConns = int32(configuration.databaseMaxConnections)
	poolConfig.MinConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("connect database")
	}
	defer pool.Close()
	s3Client, err := s3store.NewClient(ctx, s3store.ClientConfig{Region: configuration.s3Region, Endpoint: configuration.s3Endpoint, UsePathStyle: configuration.s3PathStyle, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure object store")
	}
	payloadBlobs := s3store.Store{Client: s3Client, Bucket: configuration.payloadBucket, Prefix: configuration.payloadPrefix, MaxBytes: 32 << 20, ServerSideEncryption: configuration.s3Encryption, KMSKeyID: configuration.s3KMSKeyID, RequireDigestMetadata: true}
	importSources := s3store.Store{Client: s3Client, Bucket: configuration.importBucket, Prefix: configuration.importPrefix, MaxBytes: 5 << 20, ServerSideEncryption: configuration.s3Encryption, KMSKeyID: configuration.s3KMSKeyID}
	vaultReader, err := vaultkeys.NewClientReader(vaultkeys.ClientConfig{Address: configuration.vaultAddress, Namespace: configuration.vaultNamespace, Mount: configuration.vaultMount, TokenFile: configuration.vaultTokenFile, CACertificateFile: configuration.vaultCAFile, ClientCertificateFile: configuration.vaultCertFile, ClientKeyFile: configuration.vaultKeyFile, TLSServerName: configuration.vaultTLSServerName, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure Vault")
	}
	valkeyClient, err := platformratelimit.NewClient(platformratelimit.ClientConfig{Addresses: configuration.valkeyAddresses, Username: configuration.valkeyUsername, PasswordFile: configuration.valkeyPasswordFile, RootCAFile: configuration.valkeyCAFile, ClientCertificateFile: configuration.valkeyCertFile, ClientKeyFile: configuration.valkeyKeyFile, TLSServerName: configuration.valkeyTLSName, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure Valkey")
	}
	defer valkeyClient.Close()
	dependencyCtx, dependencyCancel := context.WithTimeout(ctx, 5*time.Second)
	if pool.Ping(dependencyCtx) != nil || payloadBlobs.Ready(dependencyCtx) != nil || importSources.Ready(dependencyCtx) != nil || vaultReader.Ready(dependencyCtx) != nil || platformratelimit.Ready(dependencyCtx, valkeyClient) != nil {
		dependencyCancel()
		return errors.New("identity dependency is not ready")
	}
	dependencyCancel()
	payloadStore := payload.EnvelopeStore{Keys: vaultkeys.Provider{KV: vaultReader, Prefix: configuration.vaultKeyPrefix}, Blobs: payloadBlobs}
	hasher := password.Hasher{Parameters: password.ProductionParameters(), Pepper: secrets.PasswordPepper, Random: rand.Reader}
	dummyHash, dummyParameters, err := hasher.Hash("lites timing equalizer password")
	if err != nil {
		return errors.New("prepare password timing equalizer")
	}
	rangeHTTPClient := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}, ForceAttemptHTTP2: true, MaxIdleConns: 32, MaxIdleConnsPerHost: 8, IdleConnTimeout: 30 * time.Second}, Timeout: 5 * time.Second}
	rangeChecker, err := password.NewRangeChecker(configuration.passwordRangeURL, configuration.passwordRangeAllowedHosts, rangeHTTPClient)
	if err != nil {
		return errors.New("configure password compromise screen")
	}
	service := identitypostgres.AuthService{
		Pool: pool, ImportSources: importSources, Passwords: hasher, PasswordPolicy: password.Policy{Checker: rangeChecker}, DummyPasswordHash: dummyHash, DummyPasswordParameters: dummyParameters,
		VerificationTokens: opaque.Manager{Purpose: "email-verification", Pepper: secrets.VerificationTokenPepper}, PasswordResetTokens: opaque.Manager{Purpose: "password-reset", Pepper: secrets.PasswordResetPepper}, EmailChangeTokens: opaque.Manager{Purpose: "email-change", Pepper: secrets.EmailChangePepper}, InvitationTokens: opaque.Manager{Purpose: "invitation", Pepper: secrets.InvitationPepper},
		SessionPepper: secrets.SessionPepper, CSRFPepper: secrets.CSRFPepper, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper, IdentityKey: secrets.IdentityKey, CursorKey: secrets.CursorKey,
		PublicTenantID: configuration.publicTenantID, StoreEpoch: storeEpoch, Region: configuration.region, VerificationTTL: 24 * time.Hour, PasswordResetTTL: 30 * time.Minute, EmailChangeTTL: 24 * time.Hour, ReauthenticationTTL: 5 * time.Minute, ErasureGracePeriod: 30 * 24 * time.Hour, SessionTTL: 30 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour,
		Payloads: payloadStore, Appender: eventpostgres.Appender{}, Random: rand.Reader,
	}
	anonymousBootstrap := identitypostgres.AnonymousSessionService{
		Pool: pool, SystemTenantID: configuration.publicTenantID,
		Signer:   anonymoussession.Signer{KeyID: configuration.anonymousHandleKeyID, Key: secrets.AnonymousHandleKey, DigestPepper: secrets.AnonymousHandlePepper, Random: rand.Reader},
		Payloads: payloadStore, Appender: eventpostgres.Appender{}, StoreEpoch: storeEpoch, TTL: anonymoussession.MaximumTTL, Random: rand.Reader,
	}
	onboarding := identitypostgres.OnboardingService{
		Pool: pool, Anonymous: anonymousBootstrap, Payloads: payloadStore, Appender: eventpostgres.Appender{}, StoreEpoch: storeEpoch,
		IdentityKey: secrets.IdentityKey, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper,
		IdempotencyTTL: 24 * time.Hour, OnboardingTTL: anonymoussession.MaximumTTL, Random: rand.Reader,
	}
	claimStore := identitypostgres.AnonymousClaimStore{Pool: pool, SystemTenantID: configuration.publicTenantID, IdentityKey: secrets.IdentityKey, Payloads: payloadStore, Appender: eventpostgres.Appender{}, StoreEpoch: storeEpoch}
	claimService := identitypostgres.OnboardingClaimService{Pool: pool, Store: claimStore, SystemTenantID: configuration.publicTenantID, Payloads: payloadStore, IdentityKey: secrets.IdentityKey, IdempotencyKeyPepper: secrets.IdempotencyPepper, RequestDigestPepper: secrets.RequestDigestPepper, IdempotencyTTL: 24 * time.Hour}
	telemetry, err := observability.New(ctx, observability.Config{ServiceName: "identity-service", ServiceVersion: configuration.serviceVersion, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpBearerTokenFile, TraceSampleRatio: configuration.traceSampleRatio, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure observability")
	}
	handler := identityapi.Handler{Service: service, Sessions: service, Passwords: service, Emails: service, Accounts: service, Invitations: service, Memberships: service, Onboarding: onboarding, Claims: claimService, AnonymousCSRFKey: secrets.AnonymousCSRFKey, RateLimiter: platformratelimit.Limiter{Client: valkeyClient, Namespace: "lites"}, RateLimitPepper: secrets.RateLimitPepper}
	verifier := trustedcontext.Verifier{Issuer: configuration.trustedIssuer, Audience: configuration.trustedAudience, Keys: keys, KeyWindows: windows, MaximumTTL: 5 * time.Minute, ClockSkew: 5 * time.Second}
	application := telemetry.WrapHTTP(serviceauth.Middleware{Verifier: verifier, RequireVerifiedClientCertificate: !configuration.allowInsecureDevelopment}.Wrap(handler))
	tlsConfig, err := newServerTLSConfig(configuration)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: configuration.listenAddress, Handler: application, TLSConfig: tlsConfig, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	health := identityHealth(configuration.healthAddress, pool, authority, storeEpoch, payloadBlobs, importSources, vaultReader, valkeyClient, telemetry.MetricsHandler())
	errChannel := make(chan error, 3)
	go func() {
		if serveErr := health.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errChannel <- serveErr
		}
	}()
	go func() {
		listener, listenErr := net.Listen("tcp", configuration.listenAddress)
		if listenErr != nil {
			errChannel <- listenErr
			return
		}
		var serveErr error
		if tlsConfig != nil {
			// ServeTLS enables Go's HTTP/2 integration and uses the certificate
			// already loaded into TLSConfig when file names are empty.
			serveErr = server.ServeTLS(listener, "", "")
		} else {
			serveErr = server.Serve(listener)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errChannel <- serveErr
		}
	}()
	go func() { errChannel <- monitorStoreEpoch(ctx, authority, storeEpoch, 5*time.Second, logger) }()
	logger.Info("identity service ready", "address", configuration.listenAddress, "store_epoch", storeEpoch, "region", configuration.region)
	var runErr error
	select {
	case <-parent.Done():
	case runErr = <-errChannel:
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	_ = health.Shutdown(shutdownCtx)
	if shutdownErr := telemetry.Shutdown(shutdownCtx); shutdownErr != nil && runErr == nil {
		runErr = shutdownErr
	}
	return runErr
}

func newEpochHTTPClient(configuration config) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	if configuration.epochCAFile != "" {
		contents, err := os.ReadFile(configuration.epochCAFile)
		if err != nil {
			return nil, errors.New("load epoch CA")
		}
		roots, err := x509.SystemCertPool()
		if err != nil || !roots.AppendCertsFromPEM(contents) {
			return nil, errors.New("epoch CA is invalid")
		}
		tlsConfig.RootCAs = roots
	}
	if configuration.epochCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(configuration.epochCertFile, configuration.epochKeyFile)
		if err != nil {
			return nil, errors.New("epoch client certificate is invalid")
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func newServerTLSConfig(configuration config) (*tls.Config, error) {
	if configuration.allowInsecureDevelopment && configuration.serverCertificateFile == "" {
		return nil, nil
	}
	certificate, err := tls.LoadX509KeyPair(configuration.serverCertificateFile, configuration.serverKeyFile)
	if err != nil {
		return nil, errors.New("server TLS certificate is invalid")
	}
	ca, err := os.ReadFile(configuration.serverClientCAFile)
	if err != nil {
		return nil, errors.New("server client CA is unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("server client CA is invalid")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert}, nil
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

func identityHealth(address string, pool *pgxpool.Pool, authority eventpostgres.EpochAuthority, expectedEpoch string, payloads, imports s3store.Store, vault *vaultkeys.ClientReader, cache valkey.Client, metrics http.Handler) *http.Server {
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
		if pool.Ping(ctx) != nil || epochErr != nil || currentEpoch != expectedEpoch || payloads.Ready(ctx) != nil || imports.Ready(ctx) != nil || vault.Ready(ctx) != nil || platformratelimit.Ready(ctx, cache) != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 4 * time.Second, WriteTimeout: 4 * time.Second, IdleTimeout: 30 * time.Second}
}
