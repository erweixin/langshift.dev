package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/langshift/lites/internal/capacity/reference"
	"github.com/langshift/lites/internal/observability"
	"go.opentelemetry.io/otel/metric"
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
		logger.Error("reference capacity controller stopped", "error", err)
		os.Exit(1)
	}
}

func run(parent context.Context, configuration config, logger *slog.Logger) error {
	profile, _, profileHash, err := reference.LoadProfile(configuration.profileFile)
	if err != nil {
		return errors.New("load immutable capacity profile")
	}
	dataset, err := reference.LoadDataset(configuration.datasetFile, configuration.sourceCommit, profile)
	if err != nil {
		return errors.New("load immutable capacity dataset")
	}
	bearerToken, err := readSecret(configuration.bearerTokenFile)
	if err != nil {
		return errors.New("load capacity controller bearer credential")
	}
	gatewayClient, err := newGatewayClient(configuration)
	if err != nil {
		return err
	}
	telemetry, err := observability.New(parent, observability.Config{ServiceName: "reference-capacity-controller", ServiceVersion: configuration.version, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpTokenFile, TraceSampleRatio: configuration.traceRatio})
	if err != nil {
		return errors.New("configure capacity controller observability")
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = telemetry.Shutdown(shutdown)
	}()
	loadMetrics, registration, err := newLoadMetrics(telemetry.Meter("github.com/langshift/lites/cmd/reference-capacity-controller"))
	if err != nil {
		return errors.New("configure capacity controller metrics")
	}
	defer registration.Unregister()
	manager := &reference.Manager{Profile: profile, ProfileSHA256: profileHash, SourceCommit: configuration.sourceCommit, DatasetSHA256: dataset.SHA256, Context: parent}
	manager.Factory = func(runID string) (reference.ManagedWorkload, error) {
		return reference.NewWorkload(reference.WorkloadConfig{GatewayURL: configuration.gatewayURL, PublicOrigin: configuration.publicOrigin, Client: gatewayClient, Profile: profile, Dataset: dataset, RunID: runID, WarmupDuration: configuration.warmupDuration, RequestTimeout: configuration.requestTimeout, MaximumInflight: configuration.maximumInflight, MaximumErrorRatio: configuration.maximumErrorRatio, Metrics: loadMetrics})
	}
	application := telemetry.WrapHTTP(reference.Handler{Manager: manager, BearerToken: bearerToken})
	serverTLS, err := newServerTLS(configuration)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: configuration.listenAddress, Handler: application, TLSConfig: serverTLS, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	healthMux := http.NewServeMux()
	healthMux.Handle("GET /metrics", telemetry.MetricsHandler())
	healthMux.HandleFunc("GET /live", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	healthMux.HandleFunc("GET /ready", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	health := &http.Server{Addr: configuration.healthAddress, Handler: healthMux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
	errs := make(chan error, 2)
	go func() {
		listener, listenErr := net.Listen("tcp", configuration.listenAddress)
		if listenErr != nil {
			errs <- listenErr
			return
		}
		errs <- server.ServeTLS(listener, "", "")
	}()
	go func() { errs <- health.ListenAndServe() }()
	logger.Info("reference capacity controller ready", "address", configuration.listenAddress, "profile", profile.ProfileID, "profile_hash", profileHash, "dataset_hash", dataset.SHA256, "source_commit", configuration.sourceCommit)
	var runErr error
	select {
	case <-parent.Done():
	case runErr = <-errs:
		if errors.Is(runErr, http.ErrServerClosed) {
			runErr = nil
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	_ = health.Shutdown(shutdown)
	gatewayClient.CloseIdleConnections()
	return runErr
}

type capacityMetrics struct {
	hotPercent atomic.Uint64
	hotAppends metric.Int64Counter
}

func newLoadMetrics(meter metric.Meter) (*capacityMetrics, metric.Registration, error) {
	hotPercent, err := meter.Float64ObservableGauge("capacity.hot_tenant_percent", metric.WithUnit("%"), metric.WithDescription("Maximum accepted public API request share of a fixed authenticated tenant bucket"))
	if err != nil {
		return nil, nil, err
	}
	hotAppends, err := meter.Int64Counter("capacity.hot_tenant_user_event_appends", metric.WithUnit("{event}"), metric.WithDescription("Proven minimum atomic event appends accepted for the fixed hot tenant-user"))
	if err != nil {
		return nil, nil, err
	}
	result := &capacityMetrics{hotAppends: hotAppends}
	registration, err := meter.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
		observer.ObserveFloat64(hotPercent, math.Float64frombits(result.hotPercent.Load()))
		return nil
	}, hotPercent)
	if err != nil {
		return nil, nil, err
	}
	return result, registration, nil
}

func (metrics *capacityMetrics) SetHotTenantPercent(value float64) {
	metrics.hotPercent.Store(math.Float64bits(value))
}

func (metrics *capacityMetrics) AddHotUserEventAppends(ctx context.Context, count int64) {
	metrics.hotAppends.Add(ctx, count)
}

func newGatewayClient(configuration config) (*http.Client, error) {
	tlsConfig, err := clientTLS(configuration.gatewayCAFile, configuration.gatewayCertFile, configuration.gatewayKeyFile, configuration.gatewayTLSServerName)
	if err != nil {
		return nil, errors.New("configure gateway mutual TLS")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	transport.MaxIdleConns = 30000
	transport.MaxIdleConnsPerHost = 30000
	transport.MaxConnsPerHost = 30000
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 20 * time.Second
	transport.ForceAttemptHTTP2 = true
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func newServerTLS(configuration config) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(configuration.serverCertificateFile, configuration.serverKeyFile)
	if err != nil {
		return nil, errors.New("load controller server certificate")
	}
	clientCA, err := os.ReadFile(configuration.serverClientCAFile)
	if err != nil {
		return nil, errors.New("load controller client CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(clientCA) {
		return nil, errors.New("controller client CA is invalid")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}, nil
}

func clientTLS(caFile, certFile, keyFile, serverName string) (*tls.Config, error) {
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("CA file contains no certificate")
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}, ServerName: serverName}, nil
}

func readSecret(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 8193))
	value := strings.TrimSpace(string(raw))
	if err != nil || len(raw) > 8192 || value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("secret file is invalid")
	}
	return value, nil
}
