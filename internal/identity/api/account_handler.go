package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

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
	CreateAccountExport(context.Context, AccountExportCreateCommand) (AccountMutationResult, error)
	CreateAccountErasure(context.Context, AccountErasureCreateCommand) (AccountMutationResult, error)
	CancelAccountErasure(context.Context, AccountErasureCancelCommand) (AccountMutationResult, error)
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
