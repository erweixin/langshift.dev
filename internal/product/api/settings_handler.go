package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const maximumSettingsBodyBytes int64 = 20 * 1024

type BYOKCredentialResource struct {
	ID              string     `json:"id"`
	Version         uint64     `json:"version"`
	Status          string     `json:"status"`
	ProviderID      string     `json:"provider_id"`
	BoundHost       string     `json:"bound_host"`
	SecretHint      string     `json:"secret_hint"`
	LastValidatedAt *time.Time `json:"last_validated_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	Replayed        bool       `json:"replayed,omitempty"`
}

type BYOKListQuery struct{ RequestID, TenantID, UserID, SessionID, Cursor string }
type BYOKListResult struct {
	Items      []BYOKCredentialResource `json:"items"`
	NextCursor *string                  `json:"next_cursor"`
}
type BYOKCreateCommand struct {
	CommandMetadata
	ProviderID, Endpoint, APIKey string
}
type BYOKDeleteCommand struct {
	CommandMetadata
	CredentialID    string
	ExpectedVersion uint64
}
type BYOKService interface {
	List(context.Context, BYOKListQuery) (BYOKListResult, error)
	Create(context.Context, BYOKCreateCommand) (BYOKCredentialResource, error)
	Delete(context.Context, BYOKDeleteCommand) (BYOKCredentialResource, error)
}

type MemoryPolicyResource struct {
	ID            string    `json:"id"`
	Version       uint64    `json:"version"`
	Status        string    `json:"status"`
	Enabled       bool      `json:"enabled"`
	RetentionDays *int      `json:"retention_days"`
	AllowedKinds  []string  `json:"allowed_kinds"`
	UpdatedAt     time.Time `json:"updated_at"`
	Replayed      bool      `json:"replayed,omitempty"`
}
type MemoryPolicyQuery struct{ RequestID, TenantID, UserID, SessionID string }
type MemoryPolicyUpdateCommand struct {
	CommandMetadata
	Enabled         bool
	RetentionDays   *int
	AllowedKinds    []string
	ExpectedVersion uint64
}
type MemoryPolicyService interface {
	Get(context.Context, MemoryPolicyQuery) (MemoryPolicyResource, error)
	Update(context.Context, MemoryPolicyUpdateCommand) (MemoryPolicyResource, error)
}

type SettingsHandler struct {
	BYOK   BYOKService
	Memory MemoryPolicyService
}

func (handler SettingsHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch {
	case request.URL.Path == "/v1/byok-credentials":
		handler.byokCollection(writer, request)
	case strings.HasPrefix(request.URL.Path, "/v1/byok-credentials/"):
		handler.byokResource(writer, request)
	case request.URL.Path == "/v1/memory-policy":
		handler.memoryPolicy(writer, request)
	default:
		(RouteHandler{}).writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
	}
}

func (handler SettingsHandler) byokCollection(writer http.ResponseWriter, request *http.Request) {
	if handler.BYOK == nil {
		(RouteHandler{}).writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	switch request.Method {
	case http.MethodGet:
		claims, ok := settingsClaims(writer, request, false)
		if !ok {
			return
		}
		for name, values := range request.URL.Query() {
			if name != "cursor" || len(values) != 1 || values[0] == "" {
				(RouteHandler{}).validationFailed(writer, request)
				return
			}
		}
		result, err := handler.BYOK.List(request.Context(), BYOKListQuery{RequestID: claims.RequestID, TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID, Cursor: request.URL.Query().Get("cursor")})
		if err != nil {
			settingsError(writer, request, err)
			return
		}
		if result.Items == nil {
			result.Items = []BYOKCredentialResource{}
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Cache-Control", "private, no-store")
		_ = json.NewEncoder(writer).Encode(result)
	case http.MethodPost:
		metadata, ok := settingsMetadata(writer, request)
		if !ok {
			return
		}
		var body struct {
			RequestID  string  `json:"request_id"`
			ProviderID string  `json:"provider_id"`
			Endpoint   *string `json:"endpoint"`
			APIKey     string  `json:"api_key"`
		}
		if !decodeSettings(writer, request, &body) || !validClientRequestID(body.RequestID) || !validProvider(body.ProviderID) || !validAPIKey(body.APIKey) {
			(RouteHandler{}).validationFailed(writer, request)
			return
		}
		endpoint := ""
		if body.Endpoint != nil {
			endpoint = *body.Endpoint
		}
		if _, ok = boundProviderHost(body.ProviderID, endpoint); !ok {
			(RouteHandler{}).validationFailed(writer, request)
			return
		}
		metadata.ClientRequestID = body.RequestID
		result, err := handler.BYOK.Create(request.Context(), BYOKCreateCommand{CommandMetadata: metadata, ProviderID: body.ProviderID, Endpoint: endpoint, APIKey: body.APIKey})
		writeSettingsMutation(writer, request, result, err)
	default:
		writer.Header().Set("Allow", "GET, POST")
		(RouteHandler{}).writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
	}
}

func (handler SettingsHandler) byokResource(writer http.ResponseWriter, request *http.Request) {
	id := strings.TrimPrefix(request.URL.Path, "/v1/byok-credentials/")
	if handler.BYOK == nil || strings.Contains(id, "/") || !uuidPattern.MatchString(id) {
		(RouteHandler{}).writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	if request.Method != http.MethodDelete {
		writer.Header().Set("Allow", http.MethodDelete)
		(RouteHandler{}).writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
		return
	}
	metadata, ok := settingsMetadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID       string `json:"request_id"`
		ExpectedVersion uint64 `json:"expected_credential_version"`
	}
	if !decodeSettings(writer, request, &body) || !validClientRequestID(body.RequestID) || body.ExpectedVersion < 1 {
		(RouteHandler{}).validationFailed(writer, request)
		return
	}
	if expected, valid := ifMatch(request); !valid || expected != body.ExpectedVersion {
		(RouteHandler{}).writeProblem(writer, request, http.StatusConflict, "version_conflict", "Version conflict", true)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.BYOK.Delete(request.Context(), BYOKDeleteCommand{CommandMetadata: metadata, CredentialID: id, ExpectedVersion: body.ExpectedVersion})
	writeSettingsMutation(writer, request, result, err)
}

func (handler SettingsHandler) memoryPolicy(writer http.ResponseWriter, request *http.Request) {
	if handler.Memory == nil {
		(RouteHandler{}).writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	if len(request.URL.Query()) != 0 {
		(RouteHandler{}).validationFailed(writer, request)
		return
	}
	switch request.Method {
	case http.MethodGet:
		claims, ok := settingsClaims(writer, request, false)
		if !ok {
			return
		}
		result, err := handler.Memory.Get(request.Context(), MemoryPolicyQuery{RequestID: claims.RequestID, TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID})
		if err != nil {
			settingsError(writer, request, err)
			return
		}
		writeMemoryPolicy(writer, result)
	case http.MethodPut:
		metadata, ok := settingsMetadata(writer, request)
		if !ok {
			return
		}
		var body struct {
			RequestID       string   `json:"request_id"`
			Enabled         bool     `json:"enabled"`
			RetentionDays   *int     `json:"retention_days"`
			AllowedKinds    []string `json:"allowed_kinds"`
			ExpectedVersion uint64   `json:"expected_policy_version"`
		}
		if !decodeSettings(writer, request, &body) || !validClientRequestID(body.RequestID) || body.ExpectedVersion < 1 || !validMemoryPolicy(body.Enabled, body.RetentionDays, body.AllowedKinds) {
			(RouteHandler{}).validationFailed(writer, request)
			return
		}
		if expected, valid := ifMatch(request); !valid || expected != body.ExpectedVersion {
			(RouteHandler{}).writeProblem(writer, request, http.StatusConflict, "version_conflict", "Version conflict", true)
			return
		}
		metadata.ClientRequestID = body.RequestID
		result, err := handler.Memory.Update(request.Context(), MemoryPolicyUpdateCommand{CommandMetadata: metadata, Enabled: body.Enabled, RetentionDays: body.RetentionDays, AllowedKinds: append([]string(nil), body.AllowedKinds...), ExpectedVersion: body.ExpectedVersion})
		if err != nil {
			settingsError(writer, request, err)
			return
		}
		writeMemoryPolicy(writer, result)
	default:
		writer.Header().Set("Allow", "GET, PUT")
		(RouteHandler{}).writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
	}
}

func settingsClaims(writer http.ResponseWriter, request *http.Request, mutation bool) (trustedcontext.Claims, bool) {
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.SessionID == "" || mutation && !claims.CSRFVerified {
		(RouteHandler{}).writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return trustedcontext.Claims{}, false
	}
	return claims, true
}

func settingsMetadata(writer http.ResponseWriter, request *http.Request) (CommandMetadata, bool) {
	claims, ok := settingsClaims(writer, request, true)
	if !ok {
		return CommandMetadata{}, false
	}
	keys := request.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		(RouteHandler{}).validationFailed(writer, request)
		return CommandMetadata{}, false
	}
	return CommandMetadata{RequestID: claims.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, true
}

func decodeSettings(writer http.ResponseWriter, request *http.Request, target any) bool {
	if request.Header.Get("Content-Type") != "application/json" {
		(RouteHandler{}).writeProblem(writer, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Unsupported media type", false)
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maximumSettingsBodyBytes))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

func validProvider(value string) bool {
	return value == "openai" || value == "anthropic" || value == "openai_compatible"
}
func validAPIKey(value string) bool {
	return utf8.ValidString(value) && len(value) >= 8 && len(value) <= 16*1024 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

func boundProviderHost(provider, endpoint string) (string, bool) {
	managed := map[string]string{"openai": "api.openai.com", "anthropic": "api.anthropic.com"}
	if host := managed[provider]; host != "" {
		return host, endpoint == ""
	}
	if provider != "openai_compatible" || endpoint == "" {
		return "", false
	}
	parsed, err := url.Parse(endpoint)
	host := strings.ToLower(parsed.Hostname())
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Port() != "" || host == "" || net.ParseIP(host) != nil || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || host == "metadata.google.internal" {
		return "", false
	}
	return host, true
}

func validMemoryPolicy(enabled bool, retention *int, kinds []string) bool {
	if !enabled {
		return retention == nil && len(kinds) == 0
	}
	if retention == nil || *retention < 1 || *retention > 3650 || len(kinds) == 0 || len(kinds) > 4 {
		return false
	}
	allowed := map[string]bool{"preference": true, "goal": true, "capability_context": true, "learning_history": true}
	seen := map[string]bool{}
	for _, kind := range kinds {
		if !allowed[kind] || seen[kind] {
			return false
		}
		seen[kind] = true
	}
	return true
}

func writeSettingsMutation(writer http.ResponseWriter, request *http.Request, result BYOKCredentialResource, err error) {
	if err != nil {
		settingsError(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatUint(result.Version, 10)))
	_ = json.NewEncoder(writer).Encode(result)
}
func writeMemoryPolicy(writer http.ResponseWriter, result MemoryPolicyResource) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatUint(result.Version, 10)))
	_ = json.NewEncoder(writer).Encode(result)
}
func settingsError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		(RouteHandler{}).validationFailed(writer, request)
	case errors.Is(err, ErrReauthentication):
		(RouteHandler{}).writeProblem(writer, request, http.StatusUnauthorized, "reauthentication_required", "Recent reauthentication required", false)
	case errors.Is(err, ErrPermissionDenied):
		(RouteHandler{}).writeProblem(writer, request, http.StatusForbidden, "permission_denied", "Permission denied", false)
	case errors.Is(err, ErrResourceNotFound):
		(RouteHandler{}).writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
	case errors.Is(err, ErrStateConflict), errors.Is(err, ErrIdempotencyConflict):
		(RouteHandler{}).writeProblem(writer, request, http.StatusConflict, "state_conflict", "State conflict", true)
	default:
		(RouteHandler{}).writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
	}
}
