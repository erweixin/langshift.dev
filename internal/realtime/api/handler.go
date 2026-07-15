// Package api exposes the authenticated EventStore cursor and SSE boundaries.
// Event payloads remain behind their canonical, separately authorized APIs.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/langshift/lites/internal/platform/problem"
	"github.com/langshift/lites/internal/realtime"
	realtimepostgres "github.com/langshift/lites/internal/realtime/postgres"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const defaultPageSize = 200

type StreamRunner interface {
	Run(context.Context, realtime.Session, realtime.Sink) error
}

type Handler struct {
	Store    realtime.EventStore
	Stream   StreamRunner
	PageSize int
}

type eventDTO struct {
	ID               string    `json:"id"`
	Sequence         uint64    `json:"seq"`
	EventType        string    `json:"event_type"`
	SchemaVersion    int       `json:"event_schema_version"`
	AggregateKind    string    `json:"aggregate_kind"`
	AggregateID      string    `json:"aggregate_id"`
	AggregateVersion uint64    `json:"aggregate_version"`
	StoreEpoch       string    `json:"store_epoch"`
	OccurredAt       time.Time `json:"occurred_at"`
	CommittedAt      time.Time `json:"committed_at"`
	CausationID      *string   `json:"causation_id"`
	CorrelationID    string    `json:"correlation_id"`
}

type listResponse struct {
	Events        []eventDTO `json:"events"`
	HighWatermark uint64     `json:"high_watermark"`
	NextAfterSeq  *uint64    `json:"next_after_seq"`
}

func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		handler.writeProblem(writer, request, http.StatusMethodNotAllowed, "validation_failed", "Method not allowed", false)
		return
	}
	claims, ok := authenticatedClaims(request)
	if !ok {
		handler.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return
	}
	switch request.URL.Path {
	case "/v1/events":
		handler.list(writer, request, claims)
	case "/v1/realtime":
		handler.connect(writer, request, claims)
	default:
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
	}
}

func (handler Handler) list(writer http.ResponseWriter, request *http.Request, claims trustedcontext.Claims) {
	if handler.Store == nil {
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	after, err := parseCursor(request.URL.Query().Get("after_seq"))
	if err != nil {
		handler.writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Invalid cursor", false)
		return
	}
	currentHigh, err := handler.Store.HighWatermark(request.Context(), claims.TenantID, claims.SubjectID)
	if err != nil {
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	high := currentHigh
	throughValue := strings.TrimSpace(request.URL.Query().Get("through_seq"))
	if throughValue != "" {
		high, err = parseCursor(throughValue)
		if err != nil || high < after {
			handler.writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Invalid snapshot boundary", false)
			return
		}
	}
	if after > currentHigh || high > currentHigh {
		handler.writeProblem(writer, request, http.StatusConflict, "state_conflict", "Cursor is ahead of the event stream", false)
		return
	}
	events, err := handler.Store.List(request.Context(), claims.TenantID, claims.SubjectID, after, high, handler.pageSize())
	if err != nil {
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	response := listResponse{Events: make([]eventDTO, 0, len(events)), HighWatermark: high}
	last := after
	for _, event := range events {
		if event.Sequence != last+1 {
			handler.writeProblem(writer, request, http.StatusServiceUnavailable, "event_stream_inconsistent", "Event stream is inconsistent", false)
			return
		}
		response.Events = append(response.Events, publicEvent(event))
		last = event.Sequence
	}
	if last < high {
		if len(events) == 0 {
			handler.writeProblem(writer, request, http.StatusServiceUnavailable, "event_stream_inconsistent", "Event stream is inconsistent", false)
			return
		}
		response.NextAfterSeq = &last
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(writer).Encode(response)
}

func (handler Handler) connect(writer http.ResponseWriter, request *http.Request, claims trustedcontext.Claims) {
	if handler.Stream == nil {
		handler.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	after, err := reconnectCursor(request)
	if err != nil {
		handler.writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Invalid or ambiguous cursor", false)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	sink := &sseSink{writer: writer, controller: http.NewResponseController(writer)}
	err = handler.Stream.Run(request.Context(), realtime.Session{TenantID: claims.TenantID, UserID: claims.SubjectID, AfterSequence: after, ExpiresAt: time.Unix(claims.ExpiresAt, 0).UTC()}, sink)
	if err != nil && !errors.Is(err, context.Canceled) {
		code := "stream_unavailable"
		if errors.Is(err, realtime.ErrCursorAhead) {
			code = "cursor_ahead"
		} else if errors.Is(err, realtime.ErrEventGap) {
			code = "event_stream_inconsistent"
		} else if errors.Is(err, realtime.ErrAuthExpired) {
			code = "authentication_expired"
		}
		_ = sink.named(request.Context(), "stream_error", realtime.Control{Kind: code})
	}
}

type sseSink struct {
	writer     http.ResponseWriter
	controller *http.ResponseController
}

func (sink *sseSink) Event(ctx context.Context, event realtimepostgres.Event) error {
	return sink.write(ctx, strconv.FormatUint(event.Sequence, 10), "event", publicEvent(event))
}

func (sink *sseSink) Control(ctx context.Context, control realtime.Control) error {
	return sink.named(ctx, "control", control)
}

func (sink *sseSink) named(ctx context.Context, name string, value any) error {
	return sink.write(ctx, "", name, value)
}

func (sink *sseSink) write(ctx context.Context, id, name string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = sink.controller.SetWriteDeadline(deadline)
		defer func() { _ = sink.controller.SetWriteDeadline(time.Time{}) }()
	}
	if id != "" {
		if _, err = fmt.Fprintf(sink.writer, "id: %s\n", id); err != nil {
			return err
		}
	}
	if _, err = fmt.Fprintf(sink.writer, "event: %s\ndata: %s\n\n", name, encoded); err != nil {
		return err
	}
	return sink.controller.Flush()
}

func authenticatedClaims(request *http.Request) (trustedcontext.Claims, bool) {
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	return claims, ok && claims.PrincipalKind == trustedcontext.AuthenticatedUser && claims.TenantID != "" && claims.SubjectID != "" && claims.SessionID != "" && claims.ExpiresAt > 0
}

func reconnectCursor(request *http.Request) (uint64, error) {
	header := strings.TrimSpace(request.Header.Get("Last-Event-ID"))
	query := strings.TrimSpace(request.URL.Query().Get("after_seq"))
	if header != "" && query != "" && header != query {
		return 0, errors.New("cursor sources disagree")
	}
	if header != "" {
		return parseCursor(header)
	}
	return parseCursor(query)
}

func parseCursor(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	if strings.HasPrefix(value, "+") {
		return 0, errors.New("cursor is not canonical")
	}
	return strconv.ParseUint(value, 10, 64)
}

func publicEvent(event realtimepostgres.Event) eventDTO {
	return eventDTO{ID: event.ID, Sequence: event.Sequence, EventType: event.EventType, SchemaVersion: event.SchemaVersion, AggregateKind: event.AggregateKind, AggregateID: event.AggregateID, AggregateVersion: event.AggregateVersion, StoreEpoch: event.StoreEpoch, OccurredAt: event.OccurredAt, CommittedAt: event.CommittedAt, CausationID: event.CausationID, CorrelationID: event.CorrelationID}
}

func (handler Handler) pageSize() int {
	if handler.PageSize >= 1 && handler.PageSize <= 1000 {
		return handler.PageSize
	}
	return defaultPageSize
}

func (handler Handler) writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, title string, retryable bool) {
	problem.Write(writer, problem.Value{Type: "https://errors.lites.dev/" + code, Title: title, Status: status, Code: code, RequestID: request.Header.Get(transport.RequestIDHeader), Retryable: retryable})
}
