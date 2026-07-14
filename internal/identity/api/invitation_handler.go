package api

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	identityemail "github.com/langshift/lites/internal/identity/email"
)

type InvitationMutationResult struct {
	ID        string
	Version   uint64
	Status    string
	UpdatedAt time.Time
}

type InvitationCreateCommand struct {
	AuthenticatedRequestMetadata
	NormalizedEmail string
	Role            string
	ExpiresInDays   int
}

type InvitationImportCommand struct {
	AuthenticatedRequestMetadata
	ObjectRef, ContentHash, ImportKey, DefaultRole string
}

type InvitationAcceptCommand struct {
	AuthenticatedRequestMetadata
	InvitationID, Token string
	ExpectedVersion     uint64
}

type InvitationRejectCommand struct {
	AuthenticatedRequestMetadata
	InvitationID, Token string
	ExpectedVersion     uint64
}

type InvitationRevokeCommand struct {
	AuthenticatedRequestMetadata
	InvitationID, Reason string
	ExpectedVersion      uint64
}

type InvitationService interface {
	CreateInvitation(context.Context, InvitationCreateCommand) (InvitationMutationResult, error)
	ImportInvitations(context.Context, InvitationImportCommand) (InvitationMutationResult, error)
	AcceptInvitation(context.Context, InvitationAcceptCommand) (InvitationMutationResult, error)
	RejectInvitation(context.Context, InvitationRejectCommand) (InvitationMutationResult, error)
	RevokeInvitation(context.Context, InvitationRevokeCommand) (InvitationMutationResult, error)
}

func validInvitationRole(value string) bool {
	switch value {
	case "admin", "contract_admin", "program_manager", "reviewer", "member":
		return true
	default:
		return false
	}
}

func (handler Handler) invitationCreate(writer http.ResponseWriter, request *http.Request) {
	if handler.Invitations == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	var body struct {
		RequestID     string `json:"request_id"`
		Email         string `json:"email"`
		Role          string `json:"role"`
		ExpiresInDays int    `json:"expires_in_days"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	normalized, err := identityemail.Normalize(body.Email)
	if err != nil || !validClientRequestID(body.RequestID) || !validInvitationRole(body.Role) || body.ExpiresInDays < 1 || body.ExpiresInDays > 7 {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Invitations.CreateInvitation(request.Context(), InvitationCreateCommand{metadata, normalized, body.Role, body.ExpiresInDays})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeInvitationMutation(writer, request, result)
}

func (handler Handler) invitationImport(writer http.ResponseWriter, request *http.Request) {
	if handler.Invitations == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	var body struct {
		RequestID   string `json:"request_id"`
		ObjectRef   string `json:"object_ref"`
		ContentHash string `json:"content_hash"`
		ImportKey   string `json:"import_key"`
		DefaultRole string `json:"default_role"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || !validOpaqueField(body.ObjectRef, 1, 2000) || !validSHA256ContentHash(body.ContentHash) || !validOpaqueField(body.ImportKey, 16, 200) || !validInvitationRole(body.DefaultRole) {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Invitations.ImportInvitations(request.Context(), InvitationImportCommand{metadata, body.ObjectRef, body.ContentHash, body.ImportKey, body.DefaultRole})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeInvitationMutation(writer, request, result)
}

func (handler Handler) invitationAccept(writer http.ResponseWriter, request *http.Request) {
	if handler.Invitations == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	segment := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/v1/invitations/"), "/accept")
	if segment == "" || strings.Contains(segment, "/") || len(segment) > 200 {
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	expected, ok := handler.ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		Token     string `json:"token"`
		Decision  string `json:"decision"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || body.Decision != "accept" || !validOpaqueToken(body.Token, 32, 512) {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Invitations.AcceptInvitation(request.Context(), InvitationAcceptCommand{metadata, segment, body.Token, expected})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeInvitationMutation(writer, request, result)
}

func (handler Handler) invitationReject(writer http.ResponseWriter, request *http.Request) {
	if handler.Invitations == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	segment := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/v1/invitations/"), "/reject")
	if segment == "" || strings.Contains(segment, "/") || len(segment) > 200 {
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	expected, ok := handler.ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
		Token     string `json:"token"`
		Decision  string `json:"decision"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || body.Decision != "reject" || !validOpaqueToken(body.Token, 32, 512) {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Invitations.RejectInvitation(request.Context(), InvitationRejectCommand{metadata, segment, body.Token, expected})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeInvitationMutation(writer, request, result)
}

func (handler Handler) invitationRevoke(writer http.ResponseWriter, request *http.Request) {
	if handler.Invitations == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	segment := strings.TrimPrefix(request.URL.Path, "/v1/invitations/")
	if segment == "" || strings.Contains(segment, "/") || len(segment) > 200 {
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	expected, ok := handler.ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID                 string `json:"request_id"`
		Reason                    string `json:"reason"`
		ExpectedInvitationVersion uint64 `json:"expected_invitation_version"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || !validReason(body.Reason) || body.ExpectedInvitationVersion != expected {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Invitations.RevokeInvitation(request.Context(), InvitationRevokeCommand{metadata, segment, body.Reason, expected})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeInvitationMutation(writer, request, result)
}

func validOpaqueField(value string, min, max int) bool {
	return len(value) >= min && len(value) <= max && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}
func validSHA256ContentHash(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && "sha256:"+hex.EncodeToString(decoded) == value
}
func validOpaqueToken(value string, min, max int) bool {
	return len(value) >= min && len(value) <= max && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsSpace)
}

func (handler Handler) writeInvitationMutation(writer http.ResponseWriter, request *http.Request, result InvitationMutationResult) {
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
