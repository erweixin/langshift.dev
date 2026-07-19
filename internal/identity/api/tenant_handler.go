package api

import (
	"context"
	"net/http"
	"time"
)

type TenantsQuery struct {
	AuthenticatedRequestMetadata
	Cursor string
	Limit  int
}

type TenantItem struct {
	ID, Kind, Name, Status, Region, MembershipID, Role string
	Version, MembershipVersion                         uint64
	Active                                             bool
	JoinedAt, UpdatedAt                                time.Time
}

type TenantsPage struct {
	Items      []TenantItem
	NextCursor *string
}

type TenantService interface {
	ListTenants(context.Context, TenantsQuery) (TenantsPage, error)
}

func (handler Handler) tenantList(writer http.ResponseWriter, request *http.Request) {
	if handler.Tenants == nil {
		handler.internalError(writer, request)
		return
	}
	metadata, ok := handler.authenticatedMetadata(writer, request, false)
	if !ok {
		return
	}
	values, exists := request.URL.Query()["cursor"]
	if len(request.URL.Query()) > 1 || !exists && len(request.URL.Query()) != 0 || exists && len(values) != 1 || len(values) == 1 && (values[0] == "" || len(values[0]) > 4096) {
		handler.validationFailed(writer, request)
		return
	}
	cursor := ""
	if len(values) == 1 {
		cursor = values[0]
	}
	page, err := handler.Tenants.ListTenants(request.Context(), TenantsQuery{AuthenticatedRequestMetadata: metadata, Cursor: cursor, Limit: 50})
	if err != nil {
		handler.serviceError(writer, request, err)
		return
	}
	items := make([]tenantResource, len(page.Items))
	for index, item := range page.Items {
		items[index] = tenantResource{ID: item.ID, Version: item.Version, Kind: item.Kind, Name: item.Name, Status: item.Status, Region: item.Region, MembershipID: item.MembershipID, MembershipVersion: item.MembershipVersion, Role: item.Role, Active: item.Active, JoinedAt: item.JoinedAt.UTC(), UpdatedAt: item.UpdatedAt.UTC()}
	}
	writer.Header().Set("Cache-Control", "private, no-store")
	handler.writeJSON(writer, http.StatusOK, struct {
		Items      []tenantResource `json:"items"`
		NextCursor *string          `json:"next_cursor"`
	}{Items: items, NextCursor: page.NextCursor})
}

type tenantResource struct {
	ID                string    `json:"id"`
	Version           uint64    `json:"version"`
	Kind              string    `json:"kind"`
	Name              string    `json:"name"`
	Status            string    `json:"status"`
	Region            string    `json:"region"`
	MembershipID      string    `json:"membership_id"`
	MembershipVersion uint64    `json:"membership_version"`
	Role              string    `json:"role"`
	Active            bool      `json:"active"`
	JoinedAt          time.Time `json:"joined_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}
