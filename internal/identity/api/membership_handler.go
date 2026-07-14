package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type MembershipsQuery struct {
	AuthenticatedRequestMetadata
	Cursor string
	Limit  int
}

type MembershipItem struct {
	ID, UserID, Email, Role, Status string
	Version                         uint64
	JoinedAt, UpdatedAt             time.Time
	DeactivatedAt                   *time.Time
}

type MembershipsPage struct {
	Items      []MembershipItem
	NextCursor *string
}

type MembershipDeactivateCommand struct {
	AuthenticatedRequestMetadata
	MembershipID, Action, Reason string
	ExpectedVersion              uint64
}

type MembershipImportCommand struct {
	AuthenticatedRequestMetadata
	ObjectRef, ContentHash, ImportKey, Mode string
}

type MembershipMutationResult struct {
	ID        string
	Version   uint64
	Status    string
	UpdatedAt time.Time
}

type MembershipService interface {
	ListMemberships(context.Context, MembershipsQuery) (MembershipsPage, error)
	DeactivateMembership(context.Context, MembershipDeactivateCommand) (MembershipMutationResult, error)
	ImportMemberships(context.Context, MembershipImportCommand) (MembershipMutationResult, error)
}

func (handler Handler) membershipList(writer http.ResponseWriter, request *http.Request) {
	if handler.Memberships == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, false)
	if !ok {
		return
	}
	values, exists := request.URL.Query()["cursor"]
	if len(request.URL.Query()) > 1 || (!exists && len(request.URL.Query()) != 0) || (exists && len(values) != 1) || (len(values) == 1 && (values[0] == "" || len(values[0]) > 4096)) {
		handler.validationFailed(writer, request)
		return
	}
	var cursor string
	if len(values) == 1 {
		cursor = values[0]
	}
	page, err := handler.Memberships.ListMemberships(request.Context(), MembershipsQuery{AuthenticatedRequestMetadata: metadata, Cursor: cursor, Limit: 50})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	items := make([]membershipResource, len(page.Items))
	for index, item := range page.Items {
		items[index] = membershipResource{item.ID, item.UserID, item.Email, item.Role, item.Status, item.Version, item.JoinedAt.UTC(), item.UpdatedAt.UTC(), item.DeactivatedAt}
	}
	handler.writeJSON(writer, http.StatusOK, struct {
		Items      []membershipResource `json:"items"`
		NextCursor *string              `json:"next_cursor"`
	}{items, page.NextCursor})
}

type membershipResource struct {
	ID            string     `json:"id"`
	UserID        string     `json:"user_id"`
	Email         string     `json:"email"`
	Role          string     `json:"role"`
	Status        string     `json:"status"`
	Version       uint64     `json:"version"`
	JoinedAt      time.Time  `json:"joined_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	DeactivatedAt *time.Time `json:"deactivated_at"`
}

func (handler Handler) membershipDeactivate(writer http.ResponseWriter, request *http.Request) {
	if handler.Memberships == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, true)
	if !ok {
		return
	}
	membershipID := strings.TrimPrefix(request.URL.Path, "/v1/memberships/")
	if membershipID == "" || strings.Contains(membershipID, "/") || len(membershipID) > 200 {
		handler.writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "Resource not found", false)
		return
	}
	expected, ok := handler.ifMatch(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID                 string `json:"request_id"`
		Action                    string `json:"action"`
		Reason                    string `json:"reason"`
		ExpectedMembershipVersion uint64 `json:"expected_membership_version"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || (body.Action != "deactivate" && body.Action != "leave") || !validReason(body.Reason) || body.ExpectedMembershipVersion != expected {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Memberships.DeactivateMembership(request.Context(), MembershipDeactivateCommand{metadata, membershipID, body.Action, body.Reason, expected})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	if body.Action == "leave" && membershipID == metadata.MembershipID {
		handler.clearSessionCookies(writer)
	}
	handler.writeMembershipMutation(writer, request, result)
}

func (handler Handler) membershipImport(writer http.ResponseWriter, request *http.Request) {
	if handler.Memberships == nil {
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
		Mode        string `json:"mode"`
	}
	if !handler.decode(writer, request, &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || !validOpaqueField(body.ObjectRef, 1, 2000) || !validSHA256ContentHash(body.ContentHash) || !validOpaqueField(body.ImportKey, 16, 200) || (body.Mode != "upsert" && body.Mode != "deactivate_missing") {
		handler.validationFailed(writer, request)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Memberships.ImportMemberships(request.Context(), MembershipImportCommand{metadata, body.ObjectRef, body.ContentHash, body.ImportKey, body.Mode})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	handler.writeMembershipMutation(writer, request, result)
}

func validReason(value string) bool {
	return len(value) >= 1 && len(value) <= 1000 && utf8.ValidString(value) && strings.TrimSpace(value) != "" && !strings.ContainsFunc(value, unicode.IsControl)
}

func (handler Handler) writeMembershipMutation(writer http.ResponseWriter, request *http.Request, result MembershipMutationResult) {
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
