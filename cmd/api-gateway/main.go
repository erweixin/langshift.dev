package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
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
	realtimeClient, realtimeUpstream, err := configuration.realtimeUpstreamClient()
	if err != nil {
		return err
	}
	behaviorClient, behaviorUpstream, err := configuration.behaviorUpstreamClient()
	if err != nil {
		return err
	}
	agentClient, agentUpstream, err := configuration.agentUpstreamClient()
	if err != nil {
		return err
	}
	productClient, productUpstream, err := configuration.productUpstreamClient()
	if err != nil {
		return err
	}
	contractClient, contractUpstream, err := configuration.contractUpstreamClient()
	if err != nil {
		return err
	}
	identityProxy := newUpstreamProxy(upstreamClient, upstream, "identity", 0, logger)
	realtimeProxy := newUpstreamProxy(realtimeClient, realtimeUpstream, "realtime", -1, logger)
	behaviorProxy := newUpstreamProxy(behaviorClient, behaviorUpstream, "behavior", 0, logger)
	agentProxy := newUpstreamProxy(agentClient, agentUpstream, "agent-control", 0, logger)
	productProxy := newUpstreamProxy(productClient, productUpstream, "product", 0, logger)
	contractProxy := newUpstreamProxy(contractClient, contractUpstream, "contract", 0, logger)
	upstreamRouter := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if isRealtimeRoute(request.URL.Path) {
			realtimeProxy.ServeHTTP(writer, request)
			return
		}
		if isBehaviorRoute(request.URL.Path) {
			behaviorProxy.ServeHTTP(writer, request)
			return
		}
		if isAgentRoute(request.URL.Path) {
			agentProxy.ServeHTTP(writer, request)
			return
		}
		if isContractRoute(request.URL.Path) {
			contractProxy.ServeHTTP(writer, request)
			return
		}
		if isProductRoute(request.URL.Path) {
			productProxy.ServeHTTP(writer, request)
			return
		}
		identityProxy.ServeHTTP(writer, request)
	})
	boundary := gateway.TrustBoundary{
		Resolver:          identitypostgres.SessionResolver{Pool: pool, Pepper: secrets.SessionPepper},
		AnonymousResolver: identitypostgres.AnonymousSessionResolver{Pool: pool, Verifier: anonymoussession.Verifier{Keys: map[string][]byte{secrets.AnonymousHandleKeyID: secrets.AnonymousHandleKey}, DigestPepper: secrets.AnonymousHandlePepper}},
		SigningKey:        ed25519.PrivateKey(secrets.SigningKey),
		SigningKeyID:      secrets.SigningKeyID,
		Issuer:            configuration.trustedIssuer,
		Audience:          configuration.trustedAudience,
		AudienceForRequest: func(request *http.Request) string {
			if isRealtimeRoute(request.URL.Path) {
				return configuration.realtimeTrustedAudience
			}
			if isBehaviorRoute(request.URL.Path) {
				return configuration.behaviorTrustedAudience
			}
			if isAgentRoute(request.URL.Path) {
				return configuration.agentTrustedAudience
			}
			if isContractRoute(request.URL.Path) {
				return configuration.contractTrustedAudience
			}
			if isProductRoute(request.URL.Path) {
				return configuration.productTrustedAudience
			}
			return configuration.trustedAudience
		},
		TTL:                          configuration.trustedContextTTL,
		CSRFPepper:                   secrets.CSRFPepper,
		AnonymousCSRFKey:             secrets.AnonymousCSRFKey,
		FingerprintPepper:            secrets.FingerprintPepper,
		PublicOrigins:                configuration.publicOrigins,
		AllowInsecureLoopbackOrigins: configuration.allowInsecureDevelopment && configuration.environment == "engineering-test",
		RoutePolicy:                  gateway.IdentityRoutePolicy,
	}
	telemetry, err := observability.New(ctx, observability.Config{ServiceName: "api-gateway", ServiceVersion: configuration.serviceVersion, Environment: configuration.environment, Region: configuration.region, OTLPEndpoint: configuration.otlpEndpoint, OTLPRootCAFile: configuration.otlpCAFile, OTLPClientCertificateFile: configuration.otlpCertFile, OTLPClientKeyFile: configuration.otlpKeyFile, OTLPTLSServerName: configuration.otlpTLSName, OTLPBearerTokenFile: configuration.otlpBearerTokenFile, TraceSampleRatio: configuration.traceSampleRatio, AllowInsecureDevelopment: configuration.allowInsecureDevelopment})
	if err != nil {
		return errors.New("configure observability")
	}
	boundaryHandler := boundary.Wrap(upstreamRouter)
	application := gateway.CORS{Origins: configuration.publicOrigins}.Wrap(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(writer, request.Body, 8<<20)
		boundaryHandler.ServeHTTP(writer, request)
	}))
	application = telemetry.WrapHTTP(application)
	timedApplication := application
	application = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !isRealtimeRoute(request.URL.Path) {
			controller := http.NewResponseController(writer)
			_ = controller.SetWriteDeadline(time.Now().Add(40 * time.Second))
			defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
		}
		timedApplication.ServeHTTP(writer, request)
	})
	application = gateway.TrustedProxy{Networks: proxyNetworks}.Wrap(application)
	application = gateway.SecurityHeaders(application)
	tlsConfig, err := serverTLS(configuration)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: configuration.listenAddress, Handler: application, TLSConfig: tlsConfig, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 0, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
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
	logger.Info("api gateway ready", "address", configuration.listenAddress, "identity_upstream", upstream.Host, "realtime_upstream", realtimeUpstream.Host, "behavior_upstream", behaviorUpstream.Host, "agent_upstream", agentUpstream.Host, "product_upstream", productUpstream.Host, "contract_upstream", contractUpstream.Host, "signing_key_id", secrets.SigningKeyID)
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
	realtimeClient.CloseIdleConnections()
	behaviorClient.CloseIdleConnections()
	agentClient.CloseIdleConnections()
	productClient.CloseIdleConnections()
	contractClient.CloseIdleConnections()
	if telemetryErr := telemetry.Shutdown(shutdownCtx); telemetryErr != nil && runErr == nil {
		runErr = telemetryErr
	}
	return runErr
}

func isRealtimeRoute(path string) bool { return path == "/v1/events" || path == "/v1/realtime" }

func isBehaviorRoute(path string) bool {
	switch path {
	case "/v1/admin/behavior/snapshots", "/v1/admin/behavior/evaluations", "/v1/admin/behavior/promotions":
		return true
	}
	const prefix = "/v1/admin/behavior/channels/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) != 2 {
		return false
	}
	profile, environment := parts[0], parts[1]
	validProfile := profile == "route_planner" || profile == "daily_planner" || profile == "coach" || profile == "evaluator" || profile == "artifact_builder"
	return validProfile && (environment == "staging" || environment == "production")
}

func isAgentRoute(path string) bool {
	if path == "/v1/conversations" || path == "/v1/messages" || path == "/v1/admin/repair-commands" {
		return true
	}
	if strings.HasPrefix(path, "/v1/conversations/") {
		value := strings.TrimPrefix(path, "/v1/conversations/")
		return value != "" && !strings.Contains(value, "/")
	}
	for _, prefix := range []string{"/v1/runs/", "/v1/approvals/", "/v1/admin/approval-requests/", "/v1/admin/repair-commands/"} {
		if strings.HasPrefix(path, prefix) {
			value := strings.TrimPrefix(path, prefix)
			if value == "" {
				return false
			}
			parts := strings.Split(value, "/")
			switch prefix {
			case "/v1/runs/":
				return len(parts) == 1 || len(parts) == 2 && parts[1] == "cancel"
			default:
				return len(parts) == 2 && parts[1] == "decisions"
			}
		}
	}
	return false
}

func isContractRoute(path string) bool {
	if path == "/v1/admin/contracts" || path == "/v1/admin/entitlements" || path == "/v1/admin/usage" || path == "/v1/admin/usage/adjustments" || path == "/v1/admin/audit" || path == "/v1/admin/audit-exports" {
		return true
	}
	if strings.HasPrefix(path, "/v1/admin/audit-exports/") {
		return gatewayUUIDPattern.MatchString(strings.TrimPrefix(path, "/v1/admin/audit-exports/"))
	}
	for _, prefix := range []string{"/v1/admin/contracts/", "/v1/admin/entitlements/", "/v1/admin/usage/adjustments/"} {
		if strings.HasPrefix(path, prefix) {
			parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
			return len(parts) == 2 && gatewayUUIDPattern.MatchString(parts[0]) && parts[1] == "approval-decisions"
		}
	}
	return false
}

var gatewayUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func isProductRoute(path string) bool {
	if path == "/v1/public/status" || path == "/v1/catalog/roles" || path == "/v1/missions" || path == "/v1/route-revisions" || path == "/v1/daily-tasks" || path == "/v1/submissions" || path == "/v1/reviews" || path == "/v1/capability-evidence" || path == "/v1/capability-claims" || path == "/v1/preferences" || path == "/v1/byok-credentials" || path == "/v1/memory-policy" || path == "/v1/reminder-schedules" || path == "/v1/projects" || path == "/v1/artifacts" || path == "/v1/portfolio-exports" || path == "/v1/share-grants" || path == "/v1/support/cases" || path == "/v1/admin/aggregate-snapshots" || path == "/v1/admin/aggregate-queries" || path == "/v1/admin/programs" || path == "/v1/admin/cohorts" || path == "/v1/admin/role-packs" || path == "/v1/admin/task-packs" {
		return true
	}
	if strings.HasPrefix(path, "/v1/missions/") {
		parts := strings.Split(strings.TrimPrefix(path, "/v1/missions/"), "/")
		return len(parts) == 1 && gatewayUUIDPattern.MatchString(parts[0]) || len(parts) == 2 && gatewayUUIDPattern.MatchString(parts[0]) && parts[1] == "focus"
	}
	if strings.HasPrefix(path, "/v1/route-revisions/") {
		parts := strings.Split(strings.TrimPrefix(path, "/v1/route-revisions/"), "/")
		return len(parts) == 2 && gatewayUUIDPattern.MatchString(parts[0]) && parts[1] == "accept"
	}
	if strings.HasPrefix(path, "/v1/capability-claims/") {
		parts := strings.Split(strings.TrimPrefix(path, "/v1/capability-claims/"), "/")
		return len(parts) == 2 && gatewayUUIDPattern.MatchString(parts[0]) && parts[1] == "revisions"
	}
	if strings.HasPrefix(path, "/v1/byok-credentials/") {
		return gatewayUUIDPattern.MatchString(strings.TrimPrefix(path, "/v1/byok-credentials/"))
	}
	if strings.HasPrefix(path, "/v1/daily-tasks/") {
		id := strings.TrimPrefix(path, "/v1/daily-tasks/")
		return gatewayUUIDPattern.MatchString(id)
	}
	if strings.HasPrefix(path, "/v1/reviews/") {
		id := strings.TrimPrefix(path, "/v1/reviews/")
		return gatewayUUIDPattern.MatchString(id)
	}
	if strings.HasPrefix(path, "/v1/reminder-schedules/") {
		id := strings.TrimPrefix(path, "/v1/reminder-schedules/")
		return gatewayUUIDPattern.MatchString(id)
	}
	if strings.HasPrefix(path, "/v1/projects/") {
		parts := strings.Split(strings.TrimPrefix(path, "/v1/projects/"), "/")
		if len(parts) == 1 {
			return gatewayUUIDPattern.MatchString(parts[0])
		}
		if len(parts) == 2 && gatewayUUIDPattern.MatchString(parts[0]) {
			return parts[1] == "milestones" || parts[1] == "workspace" || parts[1] == "completion" || parts[1] == "test-runs"
		}
		return len(parts) == 3 && gatewayUUIDPattern.MatchString(parts[0]) && parts[1] == "milestones" && gatewayUUIDPattern.MatchString(parts[2])
	}
	if strings.HasPrefix(path, "/v1/artifacts/") {
		parts := strings.Split(strings.TrimPrefix(path, "/v1/artifacts/"), "/")
		return len(parts) == 2 && gatewayUUIDPattern.MatchString(parts[0]) && parts[1] == "revisions"
	}
	if strings.HasPrefix(path, "/v1/portfolio-exports/") {
		parts := strings.Split(strings.TrimPrefix(path, "/v1/portfolio-exports/"), "/")
		return len(parts) == 1 && gatewayUUIDPattern.MatchString(parts[0]) || len(parts) == 2 && gatewayUUIDPattern.MatchString(parts[0]) && parts[1] == "download"
	}
	for _, prefix := range []string{"/v1/share-grants/", "/v1/admin/programs/"} {
		if strings.HasPrefix(path, prefix) {
			return gatewayUUIDPattern.MatchString(strings.TrimPrefix(path, prefix))
		}
	}
	if strings.HasPrefix(path, "/v1/admin/cohorts/") {
		parts := strings.Split(strings.TrimPrefix(path, "/v1/admin/cohorts/"), "/")
		return len(parts) == 2 && gatewayUUIDPattern.MatchString(parts[0]) && parts[1] == "enrollments" || len(parts) == 3 && gatewayUUIDPattern.MatchString(parts[0]) && parts[1] == "enrollments" && gatewayUUIDPattern.MatchString(parts[2])
	}
	if strings.HasPrefix(path, "/v1/support/cases/") {
		parts := strings.Split(strings.TrimPrefix(path, "/v1/support/cases/"), "/")
		return len(parts) == 1 && gatewayUUIDPattern.MatchString(parts[0]) || len(parts) == 2 && gatewayUUIDPattern.MatchString(parts[0]) && parts[1] == "messages"
	}
	return false
}

func newUpstreamProxy(client *http.Client, upstream *url.URL, name string, flushInterval time.Duration, logger *slog.Logger) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport:     client.Transport,
		FlushInterval: flushInterval,
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(upstream)
			request.Out.Host = upstream.Host
			for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP"} {
				request.Out.Header.Del(header)
			}
		},
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, proxyErr error) {
			logger.Error(name+" upstream request", "error", proxyErr, "request_id", request.Header.Get(transport.RequestIDHeader))
			problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/dependency_unavailable", Title: "Dependency unavailable", Status: http.StatusServiceUnavailable, Code: "dependency_unavailable", RequestID: request.Header.Get(transport.RequestIDHeader), Retryable: true})
		},
		ModifyResponse: func(response *http.Response) error {
			for _, header := range []string{"Server", "Strict-Transport-Security", "X-Content-Type-Options", "Referrer-Policy", "Permissions-Policy"} {
				response.Header.Del(header)
			}
			return nil
		},
	}
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
