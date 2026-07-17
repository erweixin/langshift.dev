package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const (
	supportCaseCreateMediaType = "application/vnd.lites.support-case-create.v2+json"
	supportReplyMediaType      = "application/vnd.lites.support-reply.v2+json"
	supportCaseMediaType       = "application/vnd.lites.support-case.v2+json"
	supportCasePageMediaType   = "application/vnd.lites.support-case-page.v2+json"
)

type SupportCase struct {
	ID, Reference, RequesterUserID, Category, Priority, Status, Subject, SupportTier string
	Version                                                                          uint64
	ResponseDueAt, ResolutionDueAt                                                   *time.Time
	FirstRespondedAt, ResolvedAt                                                     *time.Time
	CreatedAt, UpdatedAt                                                             time.Time
	Messages                                                                         []SupportMessage
	Replayed                                                                         bool
}

type SupportMessage struct {
	ID, AuthorUserID, AuthorKind, Body string
	CreatedAt                          time.Time
}

type SupportPage struct {
	Items      []SupportCase
	NextCursor string
}

type SupportQuery struct {
	TenantID, UserID, MembershipID, Cursor string
	TenantWide                             bool
	Limit                                  int
}

type CreateSupportCaseCommand struct {
	CommandMetadata
	MembershipID                      string
	Category, Priority, Subject, Body string
}

type ReplySupportCaseCommand struct {
	CommandMetadata
	MembershipID        string
	CaseID, Body        string
	TenantWide          bool
	ExpectedCaseVersion uint64
}

type SupportService interface {
	Create(context.Context, CreateSupportCaseCommand) (SupportCase, error)
	List(context.Context, SupportQuery) (SupportPage, error)
	Get(context.Context, SupportQuery, string) (SupportCase, error)
	Reply(context.Context, ReplySupportCaseCommand) (SupportCase, error)
}

type SupportHandler struct{ Service SupportService }

func (handler SupportHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	caseID, reply, valid := supportPath(request.URL.Path)
	if !valid || handler.Service == nil {
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
		return
	}
	claims, ok := handler.authorize(writer, request, request.Method != http.MethodGet)
	if !ok {
		return
	}
	switch {
	case caseID == "" && !reply && request.Method == http.MethodPost:
		handler.create(writer, request, claims)
	case caseID == "" && !reply && request.Method == http.MethodGet:
		handler.list(writer, request, claims)
	case caseID != "" && !reply && request.Method == http.MethodGet:
		handler.get(writer, request, claims, caseID)
	case caseID != "" && reply && request.Method == http.MethodPost:
		handler.reply(writer, request, claims, caseID)
	default:
		writer.Header().Set("Allow", map[bool]string{true: "GET, POST", false: map[bool]string{true: http.MethodPost, false: http.MethodGet}[reply]}[caseID == ""])
		handler.problem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", false)
	}
}

func (handler SupportHandler) create(writer http.ResponseWriter, request *http.Request, claims trustedcontext.Claims) {
	metadata, ok := handler.metadata(writer, request, claims)
	if !ok {
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		Category  string `json:"category"`
		Priority  string `json:"priority"`
		Subject   string `json:"subject"`
		Body      string `json:"body"`
	}
	if !decodeSupport(writer, request, supportCaseCreateMediaType, &body) || !validClientRequestID(body.RequestID) || !validSupportCategory(body.Category) || !validSupportPriority(body.Priority) || !validSupportText(body.Subject, 1, 200) || !validSupportText(body.Body, 1, 65536) {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.Create(request.Context(), CreateSupportCaseCommand{CommandMetadata: metadata, MembershipID: claims.MembershipID, Category: body.Category, Priority: body.Priority, Subject: strings.TrimSpace(body.Subject), Body: strings.TrimSpace(body.Body)})
	if err != nil {
		handler.finishError(writer, request, err)
		return
	}
	handler.write(writer, http.StatusCreated, result)
}

func (handler SupportHandler) list(writer http.ResponseWriter, request *http.Request, claims trustedcontext.Claims) {
	query, ok := handler.query(writer, request, claims)
	if !ok {
		return
	}
	result, err := handler.Service.List(request.Context(), query)
	if err != nil {
		handler.finishError(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", supportCasePageMediaType)
	writer.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(writer).Encode(struct {
		Items      []supportCaseResource `json:"items"`
		NextCursor string                `json:"next_cursor,omitempty"`
	}{supportResources(result.Items, false), result.NextCursor})
}

func (handler SupportHandler) get(writer http.ResponseWriter, request *http.Request, claims trustedcontext.Claims, caseID string) {
	if len(request.URL.Query()) != 0 {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	query := handler.queryForClaims(claims)
	result, err := handler.Service.Get(request.Context(), query, caseID)
	if err != nil {
		handler.finishError(writer, request, err)
		return
	}
	handler.write(writer, http.StatusOK, result)
}

func (handler SupportHandler) reply(writer http.ResponseWriter, request *http.Request, claims trustedcontext.Claims, caseID string) {
	metadata, ok := handler.metadata(writer, request, claims)
	if !ok {
		return
	}
	expected, ok := ifMatch(request)
	if !ok || expected < 1 {
		handler.problem(writer, request, http.StatusPreconditionRequired, "precondition_required", false)
		return
	}
	var body struct {
		RequestID           string `json:"request_id"`
		Body                string `json:"body"`
		ExpectedCaseVersion uint64 `json:"expected_case_version"`
	}
	if !decodeSupport(writer, request, supportReplyMediaType, &body) || !validClientRequestID(body.RequestID) || !validSupportText(body.Body, 1, 65536) || body.ExpectedCaseVersion != expected {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.Reply(request.Context(), ReplySupportCaseCommand{CommandMetadata: metadata, MembershipID: claims.MembershipID, CaseID: caseID, Body: strings.TrimSpace(body.Body), TenantWide: supportTenantWide(claims.Roles), ExpectedCaseVersion: expected})
	if err != nil {
		handler.finishError(writer, request, err)
		return
	}
	handler.write(writer, http.StatusOK, result)
}

func (handler SupportHandler) query(writer http.ResponseWriter, request *http.Request, claims trustedcontext.Claims) (SupportQuery, bool) {
	values := request.URL.Query()
	for key, entries := range values {
		if key != "cursor" || len(entries) != 1 || entries[0] == "" || len(entries[0]) > 4096 {
			handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
			return SupportQuery{}, false
		}
	}
	query := handler.queryForClaims(claims)
	query.Cursor = values.Get("cursor")
	return query, true
}

func (handler SupportHandler) queryForClaims(claims trustedcontext.Claims) SupportQuery {
	return SupportQuery{TenantID: claims.TenantID, UserID: claims.SubjectID, MembershipID: claims.MembershipID, TenantWide: supportTenantWide(claims.Roles), Limit: 50}
}

func (handler SupportHandler) authorize(writer http.ResponseWriter, request *http.Request, mutation bool) (trustedcontext.Claims, bool) {
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || mutation && !claims.CSRFVerified {
		handler.problem(writer, request, http.StatusUnauthorized, "authentication_required", false)
		return trustedcontext.Claims{}, false
	}
	return claims, true
}

func (handler SupportHandler) metadata(writer http.ResponseWriter, request *http.Request, claims trustedcontext.Claims) (CommandMetadata, bool) {
	keys := request.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return CommandMetadata{}, false
	}
	return CommandMetadata{RequestID: claims.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, true
}

func (handler SupportHandler) write(writer http.ResponseWriter, status int, result SupportCase) {
	writer.Header().Set("Content-Type", supportCaseMediaType)
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(toSupportResource(result, true))
}

func (handler SupportHandler) finishError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
	case errors.Is(err, ErrPermissionDenied):
		handler.problem(writer, request, http.StatusForbidden, "permission_denied", false)
	case errors.Is(err, ErrResourceNotFound):
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
	case errors.Is(err, ErrStateConflict), errors.Is(err, ErrIdempotencyConflict):
		handler.problem(writer, request, http.StatusConflict, "state_conflict", true)
	default:
		handler.problem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", true)
	}
}

func (handler SupportHandler) problem(writer http.ResponseWriter, request *http.Request, status int, code string, retryable bool) {
	(RouteHandler{}).writeProblem(writer, request, status, code, strings.ReplaceAll(code, "_", " "), retryable)
}

type supportCaseResource struct {
	ID               string                   `json:"id"`
	Reference        string                   `json:"reference"`
	RequesterUserID  string                   `json:"requester_user_id"`
	Category         string                   `json:"category"`
	Priority         string                   `json:"priority"`
	Status           string                   `json:"status"`
	Subject          string                   `json:"subject"`
	SupportTier      string                   `json:"support_tier"`
	Version          uint64                   `json:"version"`
	ResponseDueAt    *time.Time               `json:"response_due_at"`
	ResolutionDueAt  *time.Time               `json:"resolution_due_at"`
	FirstRespondedAt *time.Time               `json:"first_responded_at"`
	ResolvedAt       *time.Time               `json:"resolved_at"`
	CreatedAt        time.Time                `json:"created_at"`
	UpdatedAt        time.Time                `json:"updated_at"`
	Messages         []supportMessageResource `json:"messages,omitempty"`
	Replayed         bool                     `json:"replayed,omitempty"`
}

type supportMessageResource struct {
	ID           string    `json:"id"`
	AuthorUserID string    `json:"author_user_id"`
	AuthorKind   string    `json:"author_kind"`
	Body         string    `json:"body"`
	CreatedAt    time.Time `json:"created_at"`
}

func toSupportResource(value SupportCase, includeMessages bool) supportCaseResource {
	resource := supportCaseResource{ID: value.ID, Reference: value.Reference, RequesterUserID: value.RequesterUserID, Category: value.Category, Priority: value.Priority, Status: value.Status, Subject: value.Subject, SupportTier: value.SupportTier, Version: value.Version, ResponseDueAt: value.ResponseDueAt, ResolutionDueAt: value.ResolutionDueAt, FirstRespondedAt: value.FirstRespondedAt, ResolvedAt: value.ResolvedAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, Replayed: value.Replayed}
	if includeMessages {
		resource.Messages = make([]supportMessageResource, len(value.Messages))
		for index, message := range value.Messages {
			resource.Messages[index] = supportMessageResource{ID: message.ID, AuthorUserID: message.AuthorUserID, AuthorKind: message.AuthorKind, Body: message.Body, CreatedAt: message.CreatedAt}
		}
	}
	return resource
}

func supportResources(values []SupportCase, includeMessages bool) []supportCaseResource {
	result := make([]supportCaseResource, len(values))
	for index, value := range values {
		result[index] = toSupportResource(value, includeMessages)
	}
	return result
}

func supportPath(path string) (caseID string, reply, valid bool) {
	if path == "/v1/support/cases" {
		return "", false, true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/v1/support/cases/"), "/")
	if len(parts) == 1 && uuidPattern.MatchString(parts[0]) {
		return parts[0], false, true
	}
	if len(parts) == 2 && uuidPattern.MatchString(parts[0]) && parts[1] == "messages" {
		return parts[0], true, true
	}
	return "", false, false
}

func decodeSupport(writer http.ResponseWriter, request *http.Request, mediaType string, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 128<<10))
	decoder.DisallowUnknownFields()
	return request.Header.Get("Content-Type") == mediaType && decoder.Decode(target) == nil && errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

func validSupportCategory(value string) bool {
	return value == "product" || value == "security" || value == "privacy" || value == "contract" || value == "availability"
}

func validSupportPriority(value string) bool {
	return value == "low" || value == "normal" || value == "high" || value == "urgent"
}

func validSupportText(value string, minimum, maximum int) bool {
	trimmed := strings.TrimSpace(value)
	return len(trimmed) >= minimum && len(trimmed) <= maximum
}

func supportTenantWide(roles []string) bool {
	for _, role := range roles {
		if role == "owner" || role == "admin" || role == "contract_admin" {
			return true
		}
	}
	return false
}
