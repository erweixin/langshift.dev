package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
	"github.com/langshift/lites/internal/statuspage"
)

const publicStatusMediaType = "application/vnd.lites.public-status.v1+json"

type PublicStatusHandler struct{ Reader statuspage.Reader }

func (handler PublicStatusHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if handler.Reader == nil || !ok || claims.PrincipalKind != trustedcontext.PublicRequest {
		(RouteHandler{}).writeProblem(writer, request, http.StatusUnauthorized, "authentication_required", "authentication required", false)
		return
	}
	if request.Method != http.MethodGet || request.URL.Path != "/v1/public/status" || len(request.URL.Query()) != 0 {
		(RouteHandler{}).writeProblem(writer, request, http.StatusNotFound, "resource_not_found", "resource not found", false)
		return
	}
	document, etag, err := handler.Reader.Read(request.Context())
	if err != nil {
		(RouteHandler{}).writeProblem(writer, request, http.StatusServiceUnavailable, "status_snapshot_unavailable", strings.ReplaceAll("status_snapshot_unavailable", "_", " "), true)
		return
	}
	writer.Header().Set("Content-Type", publicStatusMediaType)
	writer.Header().Set("Cache-Control", "public, max-age=15, must-revalidate")
	writer.Header().Set("ETag", etag)
	_ = json.NewEncoder(writer).Encode(document)
}
