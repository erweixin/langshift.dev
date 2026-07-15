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
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/eventbus/natsjs"
	"github.com/langshift/lites/internal/observability"
	"github.com/langshift/lites/internal/realtime"
	realtimeapi "github.com/langshift/lites/internal/realtime/api"
	"github.com/langshift/lites/internal/realtime/natswake"
	realtimepostgres "github.com/langshift/lites/internal/realtime/postgres"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
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
		logger.Error("realtime gateway stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, configuration config, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	keys, windows, err := trustedcontext.LoadPublicKeyring(configuration.trustedKeyringFile)
	if err != nil {
		return errors.New("load trusted context keyring")
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
	connection, js, err := natsjs.Connect(natsjs.ConnectionConfig{URLs: configuration.natsURLs, Name: "realtime-gateway", CredentialsFile: configuration.natsCredentialsFile, RootCAFile: configuration.natsCAFile, ClientCertificateFile: configuration.natsCertFile, ClientKeyFile: configuration.natsKeyFile, ConnectTimeout: 5 * time.Second, ReconnectWait: time.Second, AllowInsecureDevelopment: configuration.allowInsecureDevelopment, OnDisconnect: func(err error) { logger.Warn("NATS disconnected", "error", err) }, OnReconnect: func(endpoint string) { logger.Info("NATS reconnected", "endpoint", endpoint) }})
	if err != nil {
		return errors.New("connect NATS")
	}
	defer connection.Close()
	hub, err := natswake.New(natswake.NATSSubscriber{Connection: connection})
	if err != nil {
		return errors.New("subscribe realtime wake source")
	}
	defer hub.Close()
	dependencyCtx, dependencyCancel := context.WithTimeout(ctx, 5*time.Second)
	err = pool.Ping(dependencyCtx)
	if err == nil {
		err = natsjs.Ready(dependencyCtx, connection, js)
	}
	dependencyCancel()
	if err != nil {
		return errors.New("realtime dependency is not ready")
	}
	telemetry, err := observability.New(ctx, observability.Config{ServiceName: "realtime-gateway", ServiceVersion: configuration.serviceVersion, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpBearerTokenFile, TraceSampleRatio: configuration.traceSampleRatio, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure observability")
	}
	reader := realtimepostgres.Reader{Pool: pool}
	stream := realtime.Stream{Store: reader, Wakes: hub, PageSize: configuration.pageSize, Heartbeat: configuration.heartbeat, CatchUpInterval: configuration.catchUpInterval, WakeRetry: configuration.wakeRetry, SendTimeout: configuration.sendTimeout, ReauthLead: configuration.reauthLead}
	handler := realtimeapi.Handler{Store: reader, Stream: stream, PageSize: configuration.pageSize}
	verifier := trustedcontext.Verifier{Issuer: configuration.trustedIssuer, Audience: configuration.trustedAudience, Keys: keys, KeyWindows: windows, MaximumTTL: 5 * time.Minute, ClockSkew: 5 * time.Second}
	application := telemetry.WrapHTTP(serviceauth.Middleware{Verifier: verifier, RequireVerifiedClientCertificate: !configuration.allowInsecureDevelopment}.Wrap(handler))
	tlsConfig, err := realtimeServerTLS(configuration)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: configuration.listenAddress, Handler: application, TLSConfig: tlsConfig, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 0, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 32 << 10}
	health := realtimeHealth(configuration.healthAddress, pool, connection, js, telemetry.MetricsHandler())
	errChannel := make(chan error, 2)
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
	logger.Info("realtime gateway ready", "address", configuration.listenAddress, "region", configuration.region)
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
	_ = hub.Close()
	_ = connection.Drain()
	if telemetryErr := telemetry.Shutdown(shutdownCtx); telemetryErr != nil && runErr == nil {
		runErr = telemetryErr
	}
	return runErr
}

func realtimeServerTLS(configuration config) (*tls.Config, error) {
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

func realtimeHealth(address string, pool *pgxpool.Pool, connection *nats.Conn, js jetstream.JetStream, metrics http.Handler) *http.Server {
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
		if pool.Ping(ctx) != nil || natsjs.Ready(ctx, connection, js) != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
}
