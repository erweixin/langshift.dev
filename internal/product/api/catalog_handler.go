package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"

	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const roleCatalogMediaType = "application/vnd.lites.role-catalog.v1+json"

type RoleCatalogQuery struct {
	TenantID string
	Locale   string
}

type RoleCatalogItem struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Revision int    `json:"revision"`
	Status   string `json:"status"`
	Name     string `json:"name"`
}

type RoleCatalogResult struct {
	ReleaseVersion    string              `json:"release_version"`
	ContentRootSHA256 string              `json:"content_root_sha256"`
	Locale            string              `json:"locale"`
	Items             []RoleCatalogItem   `json:"items"`
	Rubrics           []RubricCatalogItem `json:"rubrics"`
}

type RubricCatalogItem struct {
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	Revision     int    `json:"revision"`
	Status       string `json:"status"`
	PracticeKind string `json:"practice_kind"`
}

type RoleCatalogService interface {
	ListRoles(context.Context, RoleCatalogQuery) (RoleCatalogResult, error)
}

type RoleCatalogHandler struct {
	Service        RoleCatalogService
	PublicTenantID string
}

func (handler RoleCatalogHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || request.URL.Path != "/v1/catalog/roles" {
		writer.Header().Set("Allow", http.MethodGet)
		(RouteHandler{}).writeProblem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", false)
		return
	}
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if handler.Service == nil || !ok {
		(RouteHandler{}).writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return
	}
	tenantID := claims.TenantID
	if claims.PrincipalKind == trustedcontext.PublicRequest {
		tenantID = handler.PublicTenantID
	}
	if tenantID == "" || claims.PrincipalKind != trustedcontext.PublicRequest && claims.PrincipalKind != trustedcontext.AnonymousUser && claims.PrincipalKind != trustedcontext.AuthenticatedUser {
		(RouteHandler{}).writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "Authentication required", false)
		return
	}
	values := request.URL.Query()
	if len(values) > 1 || len(values["locale"]) > 1 {
		(RouteHandler{}).writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Validation failed", false)
		return
	}
	for name := range values {
		if name != "locale" {
			(RouteHandler{}).writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Validation failed", false)
			return
		}
	}
	locale := values.Get("locale")
	if locale == "" {
		locale = "en"
	}
	if locale != "en" && locale != "zh-CN" {
		(RouteHandler{}).writeProblem(writer, request, http.StatusBadRequest, "validation_failed", "Validation failed", false)
		return
	}
	result, err := handler.Service.ListRoles(request.Context(), RoleCatalogQuery{TenantID: tenantID, Locale: locale})
	if err != nil || result.ReleaseVersion == "" || len(result.ContentRootSHA256) != 64 || result.Locale != locale || result.Items == nil || result.Rubrics == nil {
		(RouteHandler{}).writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	sort.Slice(result.Items, func(left, right int) bool { return result.Items[left].Slug < result.Items[right].Slug })
	sort.Slice(result.Rubrics, func(left, right int) bool { return result.Rubrics[left].Slug < result.Rubrics[right].Slug })
	body, err := json.Marshal(result)
	if err != nil {
		(RouteHandler{}).writeProblem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", "Dependency unavailable", true)
		return
	}
	digest := sha256.Sum256(body)
	writer.Header().Set("Content-Type", roleCatalogMediaType)
	writer.Header().Set("ETag", `"`+hex.EncodeToString(digest[:])+`"`)
	if claims.PrincipalKind == trustedcontext.PublicRequest {
		writer.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
	} else {
		writer.Header().Set("Cache-Control", "private, no-store")
	}
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(append(body, '\n'))
}
