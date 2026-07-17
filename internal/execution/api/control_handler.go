package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const maximumControlBodyBytes int64 = 128 * 1024

type ControlMetadata struct {
	RequestID, ClientRequestID, IdempotencyKey string
	TenantID, UserID, SessionID                string
}

type CreateConversationCommand struct {
	ControlMetadata
	MissionID string
	Title     *string
	Mode      string
}

type CreateMessageCommand struct {
	ControlMetadata
	ConversationID              string
	Content, Mode               string
	ExpectedConversationVersion uint64
}

type GetRunCommand struct {
	RequestID, TenantID, UserID string
	RunID                       string
}

type GetConversationCommand struct {
	RequestID, TenantID, UserID string
	ConversationID              string
	Limit                       int
	BeforeCreatedAt             *time.Time
	BeforeMessageID             string
}

type CancelRunCommand struct {
	ControlMetadata
	RunID              string
	Reason             string
	ExpectedRunVersion uint64
}

type ConversationResult struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ConversationMessageResult struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type ConversationDetailResult struct {
	ID         string                      `json:"id"`
	MissionID  string                      `json:"mission_id"`
	Version    uint64                      `json:"version"`
	Title      *string                     `json:"title"`
	Mode       string                      `json:"mode"`
	Status     string                      `json:"status"`
	Messages   []ConversationMessageResult `json:"messages"`
	NextCursor *string                     `json:"next_cursor"`
	UpdatedAt  time.Time                   `json:"updated_at"`
}

type MessageResult struct {
	RunID               string    `json:"run_id"`
	Status              string    `json:"status"`
	AcceptedAt          time.Time `json:"accepted_at"`
	ConversationVersion uint64    `json:"-"`
}

type RunResult struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ControlService interface {
	CreateConversation(context.Context, CreateConversationCommand) (ConversationResult, error)
	GetConversation(context.Context, GetConversationCommand) (ConversationDetailResult, error)
	CreateMessage(context.Context, CreateMessageCommand) (MessageResult, error)
	GetRun(context.Context, GetRunCommand) (RunResult, error)
	CancelRun(context.Context, CancelRunCommand) (RunResult, error)
}

type ControlHandler struct {
	Service ControlService
}

func (handler ControlHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/v1/conversations":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.createConversation(writer, request)
	case "/v1/messages":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.createMessage(writer, request)
	default:
		if conversationID, ok := controlConversationPath(request.URL.Path); ok {
			if request.Method != http.MethodGet {
				handler.methodNotAllowed(writer, request, http.MethodGet)
				return
			}
			handler.getConversation(writer, request, conversationID)
			return
		}
		runID, cancel, ok := controlRunPath(request.URL.Path)
		if !ok {
			RepairHandler{}.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
			return
		}
		if cancel {
			if request.Method != http.MethodPost {
				handler.methodNotAllowed(writer, request, http.MethodPost)
				return
			}
			handler.cancelRun(writer, request, runID)
			return
		}
		if request.Method != http.MethodGet {
			handler.methodNotAllowed(writer, request, http.MethodGet)
			return
		}
		handler.getRun(writer, request, runID)
	}
}

func (handler ControlHandler) getConversation(writer http.ResponseWriter, request *http.Request, conversationID string) {
	metadata, ok := handler.metadata(writer, request, false, false)
	if !ok {
		return
	}
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			RepairHandler{}.validationFailed(writer, request)
			return
		}
		limit = parsed
	}
	var beforeAt *time.Time
	var beforeID string
	if raw := request.URL.Query().Get("before"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		var cursor struct {
			CreatedAt time.Time `json:"created_at"`
			ID        string    `json:"id"`
		}
		if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.CreatedAt.IsZero() || cursor.ID == "" {
			RepairHandler{}.validationFailed(writer, request)
			return
		}
		value := cursor.CreatedAt.UTC()
		beforeAt, beforeID = &value, cursor.ID
	}
	if len(request.URL.Query()) > 0 {
		for key := range request.URL.Query() {
			if key != "limit" && key != "before" {
				RepairHandler{}.validationFailed(writer, request)
				return
			}
		}
	}
	result, err := handler.Service.GetConversation(request.Context(), GetConversationCommand{RequestID: metadata.RequestID, TenantID: metadata.TenantID, UserID: metadata.UserID, ConversationID: conversationID, Limit: limit, BeforeCreatedAt: beforeAt, BeforeMessageID: beforeID})
	if err != nil {
		RepairHandler{}.serviceError(writer, request, err)
		return
	}
	if result.ID == "" || result.MissionID == "" || result.Version == 0 || result.Mode == "" || result.Status == "" || result.Messages == nil || result.UpdatedAt.IsZero() {
		RepairHandler{}.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	writeControlJSON(writer, http.StatusOK, result)
}

func (handler ControlHandler) createConversation(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.metadata(writer, request, true, true)
	if !ok {
		return
	}
	var body struct {
		RequestID string          `json:"request_id"`
		MissionID string          `json:"mission_id"`
		Title     json.RawMessage `json:"title"`
		Mode      string          `json:"mode"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	title, validTitle := decodeNullableTitle(body.Title)
	if !validClientRequestID(body.RequestID) || body.MissionID == "" || !validTitle || body.Mode != "coach" && body.Mode != "task" && body.Mode != "project" {
		RepairHandler{}.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.CreateConversation(request.Context(), CreateConversationCommand{ControlMetadata: metadata, MissionID: body.MissionID, Title: title, Mode: body.Mode})
	if err != nil {
		RepairHandler{}.serviceError(writer, request, err)
		return
	}
	handler.writeConversation(writer, request, result)
}

func (handler ControlHandler) createMessage(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.metadata(writer, request, true, true)
	if !ok {
		return
	}
	expected, ok := (RepairHandler{}).ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID                   string `json:"request_id"`
		ConversationID              string `json:"conversation_id"`
		Content                     string `json:"content"`
		Mode                        string `json:"mode"`
		ExpectedConversationVersion uint64 `json:"expected_conversation_version"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	validMode := body.Mode == "enqueue" || body.Mode == "interrupt" || body.Mode == "feedback" || body.Mode == "parallel_background"
	if !validClientRequestID(body.RequestID) || body.ConversationID == "" || !utf8.ValidString(body.Content) || utf8.RuneCountInString(body.Content) < 1 || utf8.RuneCountInString(body.Content) > 100000 || !validMode || body.ExpectedConversationVersion == 0 {
		RepairHandler{}.validationFailed(writer, request)
		return
	}
	if body.ExpectedConversationVersion != expected {
		RepairHandler{}.writeProblem(writer, request, http.StatusConflict, "version_conflict", "Version conflict", true)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.CreateMessage(request.Context(), CreateMessageCommand{ControlMetadata: metadata, ConversationID: body.ConversationID, Content: body.Content, Mode: body.Mode, ExpectedConversationVersion: expected})
	if err != nil {
		RepairHandler{}.serviceError(writer, request, err)
		return
	}
	if result.RunID == "" || result.Status == "" || result.AcceptedAt.IsZero() || result.ConversationVersion == 0 {
		RepairHandler{}.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.ConversationVersion, 10)+`"`)
	writeControlJSON(writer, http.StatusAccepted, result)
}

func (handler ControlHandler) getRun(writer http.ResponseWriter, request *http.Request, runID string) {
	metadata, ok := handler.metadata(writer, request, false, false)
	if !ok {
		return
	}
	result, err := handler.Service.GetRun(request.Context(), GetRunCommand{RequestID: metadata.RequestID, TenantID: metadata.TenantID, UserID: metadata.UserID, RunID: runID})
	if err != nil {
		RepairHandler{}.serviceError(writer, request, err)
		return
	}
	handler.writeRun(writer, request, result)
}

func (handler ControlHandler) cancelRun(writer http.ResponseWriter, request *http.Request, runID string) {
	metadata, ok := handler.metadata(writer, request, true, true)
	if !ok {
		return
	}
	expected, ok := (RepairHandler{}).ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID          string `json:"request_id"`
		Reason             string `json:"reason"`
		ExpectedRunVersion uint64 `json:"expected_run_version"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || strings.TrimSpace(body.Reason) == "" || !utf8.ValidString(body.Reason) || utf8.RuneCountInString(body.Reason) > 1000 || body.ExpectedRunVersion == 0 {
		RepairHandler{}.validationFailed(writer, request)
		return
	}
	if body.ExpectedRunVersion != expected {
		RepairHandler{}.writeProblem(writer, request, http.StatusConflict, "version_conflict", "Version conflict", true)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.CancelRun(request.Context(), CancelRunCommand{ControlMetadata: metadata, RunID: runID, Reason: body.Reason, ExpectedRunVersion: expected})
	if err != nil {
		RepairHandler{}.serviceError(writer, request, err)
		return
	}
	handler.writeRun(writer, request, result)
}

func (handler ControlHandler) metadata(writer http.ResponseWriter, request *http.Request, mutation, requireKey bool) (ControlMetadata, bool) {
	if handler.Service == nil {
		RepairHandler{}.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return ControlMetadata{}, false
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || mutation && !claims.CSRFVerified {
		RepairHandler{}.writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return ControlMetadata{}, false
	}
	metadata := ControlMetadata{RequestID: claims.RequestID, TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}
	if requireKey {
		values := request.Header.Values(transport.IdempotencyHeader)
		if len(values) != 1 || idempotency.ValidateRawKey(values[0]) != nil {
			RepairHandler{}.validationFailed(writer, request)
			return ControlMetadata{}, false
		}
		metadata.IdempotencyKey = values[0]
	}
	return metadata, true
}

func (handler ControlHandler) decode(writer http.ResponseWriter, request *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		RepairHandler{}.writeProblem(writer, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Unsupported media type", false)
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maximumControlBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			RepairHandler{}.writeProblem(writer, request, http.StatusRequestEntityTooLarge, "payload_too_large", "Payload too large", false)
		} else {
			RepairHandler{}.validationFailed(writer, request)
		}
		return false
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		RepairHandler{}.validationFailed(writer, request)
		return false
	}
	return true
}

func (handler ControlHandler) writeConversation(writer http.ResponseWriter, request *http.Request, result ConversationResult) {
	if result.ID == "" || result.Version == 0 || result.Status == "" || result.UpdatedAt.IsZero() {
		RepairHandler{}.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	writeControlJSON(writer, http.StatusOK, result)
}

func (handler ControlHandler) writeRun(writer http.ResponseWriter, request *http.Request, result RunResult) {
	if result.ID == "" || result.Version == 0 || result.Status == "" || result.UpdatedAt.IsZero() {
		RepairHandler{}.writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	writeControlJSON(writer, http.StatusOK, result)
}

func (handler ControlHandler) methodNotAllowed(writer http.ResponseWriter, request *http.Request, allowed string) {
	writer.Header().Set("Allow", allowed)
	RepairHandler{}.writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
}

func writeControlJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func decodeNullableTitle(raw json.RawMessage) (*string, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	if string(raw) == "null" {
		return nil, true
	}
	var title string
	if json.Unmarshal(raw, &title) != nil || !utf8.ValidString(title) || utf8.RuneCountInString(title) < 1 || utf8.RuneCountInString(title) > 200 {
		return nil, false
	}
	return &title, true
}

func controlRunPath(path string) (runID string, cancel, ok bool) {
	if !strings.HasPrefix(path, "/v1/runs/") {
		return "", false, false
	}
	value := strings.TrimPrefix(path, "/v1/runs/")
	if strings.HasSuffix(value, "/cancel") {
		value = strings.TrimSuffix(value, "/cancel")
		cancel = true
	}
	if value == "" || strings.Contains(value, "/") {
		return "", false, false
	}
	return value, cancel, true
}

func controlConversationPath(path string) (string, bool) {
	if !strings.HasPrefix(path, "/v1/conversations/") {
		return "", false
	}
	value := strings.TrimPrefix(path, "/v1/conversations/")
	return value, value != "" && !strings.Contains(value, "/")
}
