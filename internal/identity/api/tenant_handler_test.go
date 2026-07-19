package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

type tenantServiceStub func(TenantsQuery) (TenantsPage, error)

func (stub tenantServiceStub) ListTenants(_ context.Context, query TenantsQuery) (TenantsPage, error) {
	return stub(query)
}

func TestTenantListReturnsMembershipContextAndActiveTenant(t *testing.T) {
	service := tenantServiceStub(func(query TenantsQuery) (TenantsPage, error) {
		if query.UserID == "" || query.TenantID == "" || query.Cursor != "signed-cursor" || query.Limit != 50 {
			t.Fatalf("query=%#v", query)
		}
		return TenantsPage{Items: []TenantItem{{ID: query.TenantID, Version: 1, Kind: "personal", Name: "Personal", Status: "active", Region: "CN", MembershipID: query.MembershipID, MembershipVersion: 1, Role: "owner", Active: true, JoinedAt: apiTestNow, UpdatedAt: apiTestNow}}}, nil
	})
	recorder := serveAuthenticatedHandler(t, Handler{Tenants: service}, http.MethodGet, "/v1/tenants?cursor=signed-cursor", "", "", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"active":true`) || !strings.Contains(recorder.Body.String(), `"role":"owner"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
