package observability

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func (runtime *Runtime) WrapHTTP(next http.Handler) http.Handler {
	if runtime == nil || next == nil {
		return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "service unavailable", http.StatusServiceUnavailable)
		})
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		route := routeTemplate(request.URL.Path)
		method := request.Method
		ctx := runtime.propagator.Extract(request.Context(), propagation.HeaderCarrier(request.Header))
		ctx, span := runtime.tracer.Start(ctx, method+" "+route, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attribute.String("http.request.method", method), attribute.String("http.route", route)))
		defer span.End()
		attributes := []attribute.KeyValue{attribute.String("http.request.method", method), attribute.String("http.route", route)}
		runtime.inflight.Add(ctx, 1, metric.WithAttributes(attributes...))
		defer runtime.inflight.Add(ctx, -1, metric.WithAttributes(attributes...))
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: writer, status: http.StatusOK}
		next.ServeHTTP(recorder, request.WithContext(ctx))
		statusClass := strconv.Itoa(recorder.status/100) + "xx"
		measurementAttributes := append(attributes, attribute.String("http.response.status_class", statusClass), attribute.Bool("http.response.throttled", recorder.status == http.StatusTooManyRequests))
		runtime.requests.Add(ctx, 1, metric.WithAttributes(measurementAttributes...))
		runtime.duration.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(measurementAttributes...))
		span.SetAttributes(attribute.Int("http.response.status_code", recorder.status))
		if recorder.status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, "server error")
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (recorder *statusRecorder) WriteHeader(status int) {
	if recorder.wroteHeader || status < 100 || status > 999 {
		return
	}
	recorder.status = status
	recorder.wroteHeader = true
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *statusRecorder) Write(body []byte) (int, error) {
	if !recorder.wroteHeader {
		recorder.wroteHeader = true
	}
	return recorder.ResponseWriter.Write(body)
}

func (recorder *statusRecorder) Unwrap() http.ResponseWriter { return recorder.ResponseWriter }

func routeTemplate(path string) string {
	switch path {
	case "/v1/auth/register", "/v1/auth/verify-email", "/v1/auth/login", "/v1/auth/password/forgot", "/v1/auth/password/reset", "/v1/auth/password/change", "/v1/auth/email/change", "/v1/auth/email/confirm-change", "/v1/auth/logout", "/v1/account/export-requests", "/v1/account/erasure-requests", "/v1/invitations", "/v1/invitation-imports", "/v1/membership-imports", "/v1/memberships/deactivate", "/v1/memberships/leave", "/v1/sessions", "/v1/sessions/revoke-others":
		return path
	}
	if strings.HasPrefix(path, "/v1/sessions/") && !strings.Contains(strings.TrimPrefix(path, "/v1/sessions/"), "/") {
		return "/v1/sessions/{session_id}"
	}
	if strings.HasPrefix(path, "/v1/invitations/") {
		remainder := strings.TrimPrefix(path, "/v1/invitations/")
		parts := strings.Split(remainder, "/")
		if len(parts) == 1 && parts[0] != "" {
			return "/v1/invitations/{invitation_id}"
		}
		if len(parts) == 2 && parts[0] != "" && (parts[1] == "accept" || parts[1] == "reject") {
			return "/v1/invitations/{invitation_id}/" + parts[1]
		}
	}
	return "/unmatched"
}
