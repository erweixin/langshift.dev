package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/gateway"
	"github.com/langshift/lites/internal/identity/anonymoussession"
	identitypostgres "github.com/langshift/lites/internal/identity/postgres"
	"github.com/langshift/lites/internal/observability"
	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/security/transport"
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
		logger.Error("api gateway stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, configuration config, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	now := time.Now().UTC()
	secrets, err := loadGatewaySecrets(configuration.secretBundleFile, now, configuration.trustedContextTTL)
	if err != nil {
		return err
	}
	if err = verifySigningKeyring(configuration.trustedKeyringFile, secrets, now, configuration.trustedContextTTL); err != nil {
		return err
	}
	proxyNetworks, err := gateway.ParseTrustedProxyCIDRs(configuration.trustedProxyCIDRs)
	if err != nil {
		return errors.New("parse trusted proxy networks")
	}
	poolConfig, err := pgxpool.ParseConfig(configuration.databaseURL)
	if err != nil {
		return errors.New("parse database configuration")
	}
	poolConfig.MaxConns = int32(configuration.databaseMaxConnections)
	poolConfig.MinConns = 4
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("connect database")
	}
	defer pool.Close()
	dependencyCtx, dependencyCancel := context.WithTimeout(ctx, 5*time.Second)
	if err = pool.Ping(dependencyCtx); err != nil {
		dependencyCancel()
		return errors.New("gateway database is not ready")
	}
	dependencyCancel()
	upstreamClient, upstream, err := configuration.upstreamClient()
	if err != nil {
		return err
	}
	proxy := &httputil.ReverseProxy{
		Transport: upstreamClient.Transport,
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(upstream)
			request.Out.Host = upstream.Host
			for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP"} {
				request.Out.Header.Del(name)
			}
		},
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, proxyErr error) {
			logger.Error("identity upstream request", "error", proxyErr, "request_id", request.Header.Get(transport.RequestIDHeader))
			problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/dependency_unavailable", Title: "Dependency unavailable", Status: http.StatusServiceUnavailable, Code: "dependency_unavailable", RequestID: request.Header.Get(transport.RequestIDHeader), Retryable: true})
		},
		ModifyResponse: func(response *http.Response) error {
			for _, name := range []string{"Server", "Strict-Transport-Security", "X-Content-Type-Options", "Referrer-Policy", "Permissions-Policy"} {
				response.Header.Del(name)
			}
			return nil
		},
	}
	boundary := gateway.TrustBoundary{
		Resolver:          identitypostgres.SessionResolver{Pool: pool, Pepper: secrets.SessionPepper},
		AnonymousResolver: identitypostgres.AnonymousSessionResolver{Pool: pool, Verifier: anonymoussession.Verifier{Keys: map[string][]byte{secrets.AnonymousHandleKeyID: secrets.AnonymousHandleKey}, DigestPepper: secrets.AnonymousHandlePepper}},
		SigningKey:        ed25519.PrivateKey(secrets.SigningKey),
		SigningKeyID:      secrets.SigningKeyID,
		Issuer:            configuration.trustedIssuer,
		Audience:          configuration.trustedAudience,
		TTL:               configuration.trustedContextTTL,
		CSRFPepper:        secrets.CSRFPepper,
		AnonymousCSRFKey:  secrets.AnonymousCSRFKey,
		FingerprintPepper: secrets.FingerprintPepper,
		PublicOrigins:     configuration.publicOrigins,
		RoutePolicy:       gateway.IdentityRoutePolicy,
	}
	telemetry, err := observability.New(ctx, observability.Config{ServiceName: "api-gateway", ServiceVersion: configuration.serviceVersion, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpBearerTokenFile, TraceSampleRatio: configuration.traceSampleRatio, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure observability")
	}
	boundaryHandler := boundary.Wrap(proxy)
	application := gateway.CORS{Origins: configuration.publicOrigins}.Wrap(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(writer, request.Body, 8<<20)
		boundaryHandler.ServeHTTP(writer, request)
	}))
	application = telemetry.WrapHTTP(application)
	application = gateway.TrustedProxy{Networks: proxyNetworks}.Wrap(application)
	application = gateway.SecurityHeaders(application)
	tlsConfig, err := serverTLS(configuration)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: configuration.listenAddress, Handler: application, TLSConfig: tlsConfig, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	health := gatewayHealth(configuration.healthAddress, pool, secrets.SigningNotAfter, configuration.trustedContextTTL, telemetry.MetricsHandler())
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
			serveErr = server.ServeTLS(listener, "", "")
		} else {
			serveErr = server.Serve(listener)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errChannel <- serveErr
		}
	}()
	go func() {
		errChannel <- monitorSigningWindow(ctx, secrets.SigningNotAfter, configuration.trustedContextTTL)
	}()
	logger.Info("api gateway ready", "address", configuration.listenAddress, "upstream", upstream.Host, "signing_key_id", secrets.SigningKeyID)
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
	upstreamClient.CloseIdleConnections()
	if telemetryErr := telemetry.Shutdown(shutdownCtx); telemetryErr != nil && runErr == nil {
		runErr = telemetryErr
	}
	return runErr
}

func gatewayHealth(address string, pool *pgxpool.Pool, keyNotAfter time.Time, ttl time.Duration, metrics http.Handler) *http.Server {
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
		if pool.Ping(ctx) != nil || !keyNotAfter.After(time.Now().UTC().Add(ttl)) {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
}

func monitorSigningWindow(ctx context.Context, notAfter time.Time, ttl time.Duration) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if !notAfter.After(time.Now().UTC().Add(ttl)) {
			return errors.New("trusted context signing key window expired")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
