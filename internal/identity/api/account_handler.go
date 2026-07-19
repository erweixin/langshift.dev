package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type AccountResource struct {
	UserID          string    `json:"user_id"`
	NormalizedEmail string    `json:"normalized_email"`
	EmailVerified   bool      `json:"email_verified"`
	Locale          string    `json:"locale"`
	Timezone        string    `json:"timezone"`
	DisplayName     *string   `json:"display_name"`
	Version         uint64    `json:"version"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type AccountQuery struct{ AuthenticatedRequestMetadata }

type AccountChanges struct {
	Locale         *string `json:"locale,omitempty"`
	Timezone       *string `json:"timezone,omitempty"`
	DisplayName    *string `json:"display_name,omitempty"`
	DisplayNameSet bool    `json:"display_name_set"`
}

type AccountUpdateCommand struct {
	AuthenticatedRequestMetadata
	ExpectedVersion uint64
	Changes         AccountChanges
}

type AccountMutationResult struct {
	ID        string
	Version   uint64
	Status    string
	UpdatedAt time.Time
}

type AccountExportCreateCommand struct {
	AuthenticatedRequestMetadata
	Scope  []string
	Format string
}

type AccountExportQuery struct {
	AuthenticatedRequestMetadata
	ExportID string
}

type AccountExportResource struct {
	ID          string     `json:"id"`
	Version     uint64     `json:"version"`
	Status      string     `json:"status"`
	Scope       []string   `json:"scope"`
	Format      string     `json:"format"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	FailureCode *string    `json:"failure_code"`
}

type AccountExportDownload struct {
	Filename    string
	MediaType   string
	ContentHash string
	Body        []byte
}

type AccountErasureCreateCommand struct {
	AuthenticatedRequestMetadata
	Reason *string
}

type AccountErasureCancelCommand struct {
	AuthenticatedRequestMetadata
	ErasureID       string
	ExpectedVersion uint64
}

type AccountService interface {
	GetAccount(context.Context, AccountQuery) (AccountResource, error)
	UpdateAccount(context.Context, AccountUpdateCommand) (AccountMutationResult, error)
	CreateAccountExport(context.Context, AccountExportCreateCommand) (AccountMutationResult, error)
	GetAccountExport(context.Context, AccountExportQuery) (AccountExportResource, error)
	DownloadAccountExport(context.Context, AccountExportQuery) (AccountExportDownload, error)
	CreateAccountErasure(context.Context, AccountErasureCreateCommand) (AccountMutationResult, error)
	CancelAccountErasure(context.Context, AccountErasureCancelCommand) (AccountMutationResult, error)
}

func (handler Handler) account(writer http.ResponseWriter, request *http.Request) {
	if handler.Accounts == nil {
		handler.internalError(writer, request)
		return
	}
	switch request.Method {
	case http.MethodGet:
		handler.accountGet(writer, request)
	case http.MethodPatch:
		handler.accountUpdate(writer, request)
	default:
		handler.methodNotAllowed(writer, request, "GET, PATCH")
	}
}

func (handler Handler) accountGet(writer http.ResponseWriter, request *http.Request) {
	if len(request.URL.Query()) != 0 {
		handler.validationFailed(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, false)
	if !ok {
		return
	}
	result, err := handler.Accounts.GetAccount(request.Context(), AccountQuery{AuthenticatedRequestMetadata: metadata})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.UserID != metadata.UserID || result.NormalizedEmail == "" || result.Version < 1 || result.UpdatedAt.IsZero() || result.Locale != "en" && result.Locale != "zh-CN" || !validTimezone(result.Timezone) {
		handler.internalError(writer, request)
		return
	}
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatUint(result.Version, 10)))
	writer.Header().Set("Cache-Control", "private, no-store")
	handler.writeJSON(writer, http.StatusOK, result)
}

func (handler Handler) accountUpdate(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	expected, ok := handler.ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		Changes   struct {
			Locale      *string                `json:"locale"`
			Timezone    *string                `json:"timezone"`
			DisplayName optionalNullableString `json:"display_name"`
		} `json:"changes"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	changes := AccountChanges{Locale: body.Changes.Locale, Timezone: body.Changes.Timezone, DisplayName: body.Changes.DisplayName.Value, DisplayNameSet: body.Changes.DisplayName.Set}
	if !validClientRequestID(body.RequestID) || !validAccountChanges(changes) {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Accounts.UpdateAccount(request.Context(), AccountUpdateCommand{AuthenticatedRequestMetadata: metadata, ExpectedVersion: expected, Changes: changes})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeAccountMutation(writer, request, result)
}

type optionalNullableString struct {
	Set   bool
	Value *string
}

func (value *optionalNullableString) UnmarshalJSON(encoded []byte) error {
	value.Set = true
	if string(encoded) == "null" {
		value.Value = nil
		return nil
	}
	var decoded string
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return err
	}
	value.Value = &decoded
	return nil
}

func validAccountChanges(changes AccountChanges) bool {
	if changes.Locale == nil && changes.Timezone == nil && !changes.DisplayNameSet {
		return false
	}
	if changes.Locale != nil && *changes.Locale != "en" && *changes.Locale != "zh-CN" {
		return false
	}
	if changes.Timezone != nil && !validTimezone(*changes.Timezone) {
		return false
	}
	if changes.DisplayNameSet && changes.DisplayName != nil {
		name := *changes.DisplayName
		if name == "" || strings.TrimSpace(name) != name || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 200 || strings.ContainsFunc(name, unicode.IsControl) {
			return false
		}
	}
	return true
}

func validTimezone(value string) bool {
	if value == "" || len(value) > 128 || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || strings.Contains(value, "..") {
		return false
	}
	_, err := time.LoadLocation(value)
	return err == nil
}

var accountExportScopes = map[string]struct{}{
	"account": {}, "missions": {}, "evidence": {}, "projects": {}, "conversations": {}, "memory": {}, "audit": {},
}

func (handler Handler) accountExportCreate(writer http.ResponseWriter, request *http.Request) {
	if handler.Accounts == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	var body struct {
		RequestID string   `json:"request_id"`
		Scope     []string `json:"scope"`
		Format    string   `json:"format"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || (body.Format != "json" && body.Format != "zip") || len(body.Scope) == 0 || len(body.Scope) > len(accountExportScopes) {
		handler.validationFailed(writer, request)
		return
	}
	seen := make(map[string]struct{}, len(body.Scope))
	for _, value := range body.Scope {
		if _, allowed := accountExportScopes[value]; !allowed {
			handler.validationFailed(writer, request)
			return
		}
		if _, duplicate := seen[value]; duplicate {
			handler.validationFailed(writer, request)
			return
		}
		seen[value] = struct{}{}
	}
	sort.Strings(body.Scope)
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Accounts.CreateAccountExport(request.Context(), AccountExportCreateCommand{AuthenticatedRequestMetadata: metadata, Scope: body.Scope, Format: body.Format})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeAccountMutation(writer, request, result)
}

func (handler Handler) accountExportGet(writer http.ResponseWriter, request *http.Request, exportID string) {
	if handler.Accounts == nil || len(request.URL.Query()) != 0 || exportID == "" {
		if handler.Accounts == nil {
			handler.internalError(writer, request)
		} else {
			handler.validationFailed(writer, request)
		}
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, false)
	if !ok {
		return
	}
	result, err := handler.Accounts.GetAccountExport(request.Context(), AccountExportQuery{AuthenticatedRequestMetadata: metadata, ExportID: exportID})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatUint(result.Version, 10)))
	writer.Header().Set("Cache-Control", "private, no-store")
	handler.writeJSON(writer, http.StatusOK, result)
}

func (handler Handler) accountExportDownload(writer http.ResponseWriter, request *http.Request, exportID string) {
	if handler.Accounts == nil || len(request.URL.Query()) != 0 || exportID == "" {
		if handler.Accounts == nil {
			handler.internalError(writer, request)
		} else {
			handler.validationFailed(writer, request)
		}
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, false)
	if !ok {
		return
	}
	result, err := handler.Accounts.DownloadAccountExport(request.Context(), AccountExportQuery{AuthenticatedRequestMetadata: metadata, ExportID: exportID})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if result.Filename == "" || (result.MediaType != "application/json" && result.MediaType != "application/zip") || len(result.Body) == 0 {
		handler.internalError(writer, request)
		return
	}
	digest, decodeErr := hex.DecodeString(result.ContentHash)
	if decodeErr != nil || len(digest) != sha256.Size {
		handler.internalError(writer, request)
		return
	}
	writer.Header().Set("Content-Type", result.MediaType)
	writer.Header().Set("Content-Disposition", `attachment; filename="`+result.Filename+`"`)
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Digest", "SHA-256="+base64.StdEncoding.EncodeToString(digest))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(result.Body)
}

func (handler Handler) accountErasureCreate(writer http.ResponseWriter, request *http.Request) {
	if handler.Accounts == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	var body struct {
		RequestID    string  `json:"request_id"`
		Confirmation string  `json:"confirmation"`
		Reason       *string `json:"reason"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || body.Confirmation != "DELETE MY ACCOUNT" || (body.Reason != nil && utf8.RuneCountInString(*body.Reason) > 1000) {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Accounts.CreateAccountErasure(request.Context(), AccountErasureCreateCommand{AuthenticatedRequestMetadata: metadata, Reason: body.Reason})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeAccountMutation(writer, request, result)
}

func (handler Handler) accountErasureCancel(writer http.ResponseWriter, request *http.Request) {
	if handler.Accounts == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	erasureID := strings.TrimPrefix(request.URL.Path, "/v1/account/erasure-requests/")
	if erasureID == "" || strings.Contains(erasureID, "/") || len(erasureID) > 200 {
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	expected, ok := handler.ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID              string `json:"request_id"`
		ExpectedErasureVersion uint64 `json:"expected_erasure_version"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || body.ExpectedErasureVersion == 0 || body.ExpectedErasureVersion != expected {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Accounts.CancelAccountErasure(request.Context(), AccountErasureCancelCommand{AuthenticatedRequestMetadata: metadata, ErasureID: erasureID, ExpectedVersion: expected})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeAccountMutation(writer, request, result)
}

func (handler Handler) writeAccountMutation(writer http.ResponseWriter, request *http.Request, result AccountMutationResult) {
	if result.ID == "" || result.Version == 0 || result.Status == "" || result.UpdatedAt.IsZero() {
		handler.internalError(writer, request)
		return
	}
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatUint(result.Version, 10)))
	handler.writeJSON(writer, http.StatusOK, struct {
		ID        string    `json:"id"`
		Version   uint64    `json:"version"`
		Status    string    `json:"status"`
		UpdatedAt time.Time `json:"updated_at"`
	}{result.ID, result.Version, result.Status, result.UpdatedAt.UTC()})
}
