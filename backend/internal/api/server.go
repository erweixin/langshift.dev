package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"lites/backend/internal/content"
	"lites/backend/internal/event"
	"lites/backend/internal/outline"
)

type ServerConfig struct {
	Addr              string
	SingleUser        bool
	ContentGeneration contentGenerationStarter
	ContentReader     contentReader
	OutlineGeneration outlineGenerationStarter
	OutlineReader     outlineReader
}

type Server struct {
	addr              string
	mux               *http.ServeMux
	singleUser        bool
	contentGeneration contentGenerationStarter
	contentReader     contentReader
	outlineGeneration outlineGenerationStarter
	outlineReader     outlineReader
}

type contentGenerationStarter interface {
	Start(ctx context.Context, request content.StartRequest) (content.StartResult, error)
}

type contentReader interface {
	GetRun(ctx context.Context, userID string, runID string) (content.RunResult, error)
	GetArtifact(ctx context.Context, contentKey string) (content.ArtifactRecord, error)
}

type outlineGenerationStarter interface {
	Start(ctx context.Context, request outline.StartRequest) (outline.StartResult, error)
}

type outlineReader interface {
	GetRun(ctx context.Context, userID string, runID string) (outline.RunResult, error)
	GetOutline(ctx context.Context, userID string, outlineID string) (outline.OutlineRecord, error)
}

func NewServer(config ServerConfig) *Server {
	addr := config.Addr
	if addr == "" {
		addr = ":8080"
	}

	server := &Server{
		addr:              addr,
		mux:               http.NewServeMux(),
		singleUser:        config.SingleUser,
		contentGeneration: config.ContentGeneration,
		contentReader:     config.ContentReader,
		outlineGeneration: config.OutlineGeneration,
		outlineReader:     config.OutlineReader,
	}
	server.routes()
	return server
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	httpServer := &http.Server{
		Addr:              s.addr,
		Handler:           requestLogger(s.mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("lites backend listening", "addr", s.addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", s.health)
	s.mux.HandleFunc("POST /api/content-generation-runs", s.createContentGenerationRun)
	s.mux.HandleFunc("GET /api/content-generation-runs/{run_id}", s.getContentGenerationRun)
	s.mux.HandleFunc("GET /api/content-artifacts/{content_key}", s.getContentArtifact)
	s.mux.HandleFunc("POST /api/outline-generation-runs", s.createOutlineGenerationRun)
	s.mux.HandleFunc("GET /api/outline-generation-runs/{run_id}", s.getOutlineGenerationRun)
	s.mux.HandleFunc("GET /api/learning-outlines/{outline_id}", s.getLearningOutline)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": "lites",
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func (s *Server) createContentGenerationRun(w http.ResponseWriter, r *http.Request) {
	if s.contentGeneration == nil {
		writeError(w, http.StatusServiceUnavailable, "content generation is not configured")
		return
	}

	userID, ok := s.userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
		return
	}

	var input content.GenerationInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid json body: %v", err))
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	result, err := s.contentGeneration.Start(r.Context(), content.StartRequest{
		UserID:         userID,
		IdempotencyKey: idempotencyKey,
		Input:          input,
	})
	if err != nil {
		switch {
		case errors.Is(err, content.ErrInvalidRequest):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, event.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, err.Error())
		default:
			slog.Error("create content generation run failed", "error", err)
			writeError(w, http.StatusInternalServerError, "create content generation run failed")
		}
		return
	}

	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) getContentGenerationRun(w http.ResponseWriter, r *http.Request) {
	if s.contentReader == nil {
		writeError(w, http.StatusServiceUnavailable, "content reader is not configured")
		return
	}

	userID, ok := s.userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	runID := strings.TrimSpace(r.PathValue("run_id"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "missing run_id")
		return
	}

	result, err := s.contentReader.GetRun(r.Context(), userID, runID)
	if err != nil {
		switch {
		case errors.Is(err, content.ErrNotFound):
			writeError(w, http.StatusNotFound, "content generation run not found")
		case errors.Is(err, content.ErrInvalidRequest):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			slog.Error("get content generation run failed", "error", err)
			writeError(w, http.StatusInternalServerError, "get content generation run failed")
		}
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) getContentArtifact(w http.ResponseWriter, r *http.Request) {
	if s.contentReader == nil {
		writeError(w, http.StatusServiceUnavailable, "content reader is not configured")
		return
	}

	if _, ok := s.userID(r); !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	contentKey := strings.TrimSpace(r.PathValue("content_key"))
	if contentKey == "" {
		writeError(w, http.StatusBadRequest, "missing content_key")
		return
	}

	record, err := s.contentReader.GetArtifact(r.Context(), contentKey)
	if err != nil {
		switch {
		case errors.Is(err, content.ErrNotFound):
			writeError(w, http.StatusNotFound, "content artifact not found")
		case errors.Is(err, content.ErrInvalidRequest):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			slog.Error("get content artifact failed", "error", err)
			writeError(w, http.StatusInternalServerError, "get content artifact failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, content.PublicArtifact(record))
}

func (s *Server) createOutlineGenerationRun(w http.ResponseWriter, r *http.Request) {
	if s.outlineGeneration == nil {
		writeError(w, http.StatusServiceUnavailable, "outline generation is not configured")
		return
	}

	userID, ok := s.userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "missing Idempotency-Key")
		return
	}

	var input outline.GenerationInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid json body: %v", err))
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	result, err := s.outlineGeneration.Start(r.Context(), outline.StartRequest{
		UserID:         userID,
		IdempotencyKey: idempotencyKey,
		Input:          input,
	})
	if err != nil {
		switch {
		case errors.Is(err, outline.ErrInvalidRequest):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, event.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, err.Error())
		default:
			slog.Error("create outline generation run failed", "error", err)
			writeError(w, http.StatusInternalServerError, "create outline generation run failed")
		}
		return
	}

	writeJSON(w, http.StatusAccepted, result)
}

func (s *Server) getOutlineGenerationRun(w http.ResponseWriter, r *http.Request) {
	if s.outlineReader == nil {
		writeError(w, http.StatusServiceUnavailable, "outline reader is not configured")
		return
	}

	userID, ok := s.userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	runID := strings.TrimSpace(r.PathValue("run_id"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "missing run_id")
		return
	}

	result, err := s.outlineReader.GetRun(r.Context(), userID, runID)
	if err != nil {
		switch {
		case errors.Is(err, outline.ErrNotFound):
			writeError(w, http.StatusNotFound, "outline generation run not found")
		case errors.Is(err, outline.ErrInvalidRequest):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			slog.Error("get outline generation run failed", "error", err)
			writeError(w, http.StatusInternalServerError, "get outline generation run failed")
		}
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) getLearningOutline(w http.ResponseWriter, r *http.Request) {
	if s.outlineReader == nil {
		writeError(w, http.StatusServiceUnavailable, "outline reader is not configured")
		return
	}

	userID, ok := s.userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	outlineID := strings.TrimSpace(r.PathValue("outline_id"))
	if outlineID == "" {
		writeError(w, http.StatusBadRequest, "missing outline_id")
		return
	}

	record, err := s.outlineReader.GetOutline(r.Context(), userID, outlineID)
	if err != nil {
		switch {
		case errors.Is(err, outline.ErrNotFound):
			writeError(w, http.StatusNotFound, "learning outline not found")
		case errors.Is(err, outline.ErrInvalidRequest):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			slog.Error("get learning outline failed", "error", err)
			writeError(w, http.StatusInternalServerError, "get learning outline failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, outline.PublicOutline(record))
}

func (s *Server) userID(r *http.Request) (string, bool) {
	if s.singleUser {
		return "local-user", true
	}
	return "", false
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		slog.Info("http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", remoteAddr(r),
			"run_id", r.Header.Get("X-Run-ID"),
			"claimed_user_id", r.Header.Get("X-User-ID"),
			"surface", r.Header.Get("X-Lites-Surface"),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func remoteAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
