// Package observability provides privacy-bounded OpenTelemetry traces and
// Prometheus metrics. Tenant, user, email, token, request target, and raw URL
// values are intentionally absent from its attribute vocabulary.
package observability

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/credentials"
)

var (
	ErrConfiguration = errors.New("observability configuration is invalid")
	ErrExporter      = errors.New("observability exporter is unavailable")
)

var tokenPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)

type Config struct {
	ServiceName               string
	ServiceVersion            string
	Environment               string
	Region                    string
	OTLPEndpoint              string
	OTLPRootCAFile            string
	OTLPClientCertificateFile string
	OTLPClientKeyFile         string
	OTLPTLSServerName         string
	OTLPBearerTokenFile       string
	TraceSampleRatio          float64
	AllowInsecureDevelopment  bool
}

type Runtime struct {
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
	metrics        http.Handler
	propagator     propagation.TextMapPropagator
	tracer         trace.Tracer
	requests       metric.Int64Counter
	duration       metric.Float64Histogram
	inflight       metric.Int64UpDownCounter
	agent          *AgentMetrics
}

func New(ctx context.Context, configuration Config) (*Runtime, error) {
	if !tokenPattern.MatchString(configuration.ServiceName) || configuration.ServiceVersion == "" || !tokenPattern.MatchString(configuration.Environment) || configuration.Region == "" || configuration.TraceSampleRatio < 0 || configuration.TraceSampleRatio > 1 || (configuration.OTLPClientCertificateFile == "") != (configuration.OTLPClientKeyFile == "") {
		return nil, ErrConfiguration
	}
	if !configuration.AllowInsecureDevelopment && (configuration.OTLPEndpoint == "" || (configuration.OTLPBearerTokenFile == "" && configuration.OTLPClientCertificateFile == "")) {
		return nil, ErrConfiguration
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	metricExporter, err := otelprom.New(
		otelprom.WithRegisterer(registry),
		otelprom.WithNamespace("lites"),
		otelprom.WithResourceAsConstantLabels(attribute.NewAllowKeysFilter(
			attribute.Key("service.name"),
			attribute.Key("deployment.environment.name"),
			attribute.Key("cloud.region"),
		)),
	)
	if err != nil {
		return nil, ErrExporter
	}
	res := resource.NewSchemaless(attribute.String("service.name", configuration.ServiceName), attribute.String("service.version", configuration.ServiceVersion), attribute.String("deployment.environment.name", configuration.Environment), attribute.String("cloud.region", configuration.Region))
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricExporter), sdkmetric.WithResource(res))
	traceOptions := []sdktrace.TracerProviderOption{sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(configuration.TraceSampleRatio)))}
	if configuration.OTLPEndpoint != "" {
		exporter, exportErr := newTraceExporter(ctx, configuration)
		if exportErr != nil {
			_ = meterProvider.Shutdown(context.Background())
			return nil, exportErr
		}
		traceOptions = append(traceOptions, sdktrace.WithBatcher(exporter, sdktrace.WithMaxQueueSize(4096), sdktrace.WithMaxExportBatchSize(512), sdktrace.WithBatchTimeout(2*time.Second), sdktrace.WithExportTimeout(5*time.Second)))
	}
	tracerProvider := sdktrace.NewTracerProvider(traceOptions...)
	meter := meterProvider.Meter("github.com/langshift/lites/internal/observability")
	requests, err := meter.Int64Counter("http.server.requests", metric.WithDescription("Completed HTTP requests"), metric.WithUnit("{request}"))
	if err != nil {
		return nil, ErrExporter
	}
	duration, err := meter.Float64Histogram("http.server.duration", metric.WithDescription("HTTP request duration"), metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30))
	if err != nil {
		return nil, ErrExporter
	}
	inflight, err := meter.Int64UpDownCounter("http.server.active_requests", metric.WithDescription("Active HTTP requests"), metric.WithUnit("{request}"))
	if err != nil {
		return nil, ErrExporter
	}
	agentMetrics, err := newAgentMetrics(meter)
	if err != nil {
		_ = tracerProvider.Shutdown(context.Background())
		_ = meterProvider.Shutdown(context.Background())
		return nil, ErrExporter
	}
	return &Runtime{tracerProvider: tracerProvider, meterProvider: meterProvider, metrics: promhttp.HandlerFor(registry, promhttp.HandlerOpts{ErrorHandling: promhttp.HTTPErrorOnError}), propagator: propagation.TraceContext{}, tracer: tracerProvider.Tracer("github.com/langshift/lites/internal/observability"), requests: requests, duration: duration, inflight: inflight, agent: agentMetrics}, nil
}

func (runtime *Runtime) MetricsHandler() http.Handler {
	if runtime == nil || runtime.metrics == nil {
		return http.NotFoundHandler()
	}
	return runtime.metrics
}

func (runtime *Runtime) Meter(name string) metric.Meter {
	return runtime.meterProvider.Meter(name)
}

func (runtime *Runtime) AgentMetrics() *AgentMetrics {
	if runtime == nil {
		return nil
	}
	return runtime.agent
}

func (runtime *Runtime) Shutdown(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	var result error
	if runtime.tracerProvider != nil {
		result = errors.Join(result, runtime.tracerProvider.Shutdown(ctx))
	}
	if runtime.meterProvider != nil {
		result = errors.Join(result, runtime.meterProvider.Shutdown(ctx))
	}
	return result
}

func newTraceExporter(ctx context.Context, configuration Config) (sdktrace.SpanExporter, error) {
	host, port, err := net.SplitHostPort(configuration.OTLPEndpoint)
	if err != nil || host == "" || port == "" {
		return nil, ErrConfiguration
	}
	options := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(configuration.OTLPEndpoint), otlptracegrpc.WithCompressor("gzip"), otlptracegrpc.WithMaxRequestSize(4 << 20)}
	if configuration.AllowInsecureDevelopment {
		if !isLoopback(host) || configuration.OTLPRootCAFile != "" || configuration.OTLPClientCertificateFile != "" {
			return nil, ErrConfiguration
		}
		options = append(options, otlptracegrpc.WithInsecure())
	} else {
		tlsConfig, tlsErr := loadClientTLS(configuration)
		if tlsErr != nil {
			return nil, tlsErr
		}
		options = append(options, otlptracegrpc.WithTLSCredentials(credentials.NewTLS(tlsConfig)))
	}
	if configuration.OTLPBearerTokenFile != "" {
		token, readErr := readToken(configuration.OTLPBearerTokenFile)
		if readErr != nil {
			return nil, readErr
		}
		options = append(options, otlptracegrpc.WithHeaders(map[string]string{"authorization": "Bearer " + token}))
	}
	exporter, err := otlptracegrpc.New(ctx, options...)
	if err != nil {
		return nil, ErrExporter
	}
	return exporter, nil
}

func loadClientTLS(configuration Config) (*tls.Config, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: configuration.OTLPTLSServerName}
	if configuration.OTLPRootCAFile != "" {
		contents, err := os.ReadFile(configuration.OTLPRootCAFile)
		if err != nil {
			return nil, ErrConfiguration
		}
		roots, err := x509.SystemCertPool()
		if err != nil || !roots.AppendCertsFromPEM(contents) {
			return nil, ErrConfiguration
		}
		tlsConfig.RootCAs = roots
	}
	if configuration.OTLPClientCertificateFile != "" {
		certificate, err := tls.LoadX509KeyPair(configuration.OTLPClientCertificateFile, configuration.OTLPClientKeyFile)
		if err != nil {
			return nil, ErrConfiguration
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	return tlsConfig, nil
}

func readToken(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", ErrConfiguration
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, 8193))
	token := strings.TrimSpace(string(contents))
	if err != nil || len(contents) > 8192 || token == "" || strings.ContainsAny(token, "\x00\r\n") {
		return "", ErrConfiguration
	}
	return token, nil
}

func isLoopback(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
