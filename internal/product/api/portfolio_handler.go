package api

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const portfolioExportMediaType = "application/vnd.lites.portfolio-export.v2+json"

type PortfolioHandler struct {
	Service   PortfolioExportService
	Downloads PortfolioDownloadService
}

func (handler PortfolioHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	id, download, ok := portfolioExportPath(request.URL.Path)
	if !ok {
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
		return
	}
	if id == "" {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			handler.problem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", false)
			return
		}
		handler.create(writer, request)
		return
	}
	if download {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			handler.problem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", false)
			return
		}
		handler.download(writer, request, id)
		return
	}
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		handler.problem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", false)
		return
	}
	handler.get(writer, request, id)
}

func (handler PortfolioHandler) download(writer http.ResponseWriter, request *http.Request, id string) {
	claims, ok := handler.claims(writer, request, false)
	if !ok {
		return
	}
	if handler.Downloads == nil || len(request.URL.Query()) != 0 {
		if handler.Downloads == nil {
			handler.problem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", true)
		} else {
			handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		}
		return
	}
	result, err := handler.Downloads.Download(request.Context(), claims.TenantID, claims.SubjectID, id)
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	digest, err := hex.DecodeString(result.ContentHash)
	if err != nil || len(result.Body) == 0 || len(digest) != 32 || result.Filename == "" || result.MediaType == "" {
		handler.problem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", true)
		return
	}
	writer.Header().Set("Content-Type", result.MediaType)
	writer.Header().Set("Content-Disposition", `attachment; filename="`+result.Filename+`"`)
	writer.Header().Set("Content-Length", strconv.Itoa(len(result.Body)))
	writer.Header().Set("Digest", "sha-256="+base64.StdEncoding.EncodeToString(digest))
	writer.Header().Set("ETag", `"`+result.ContentHash+`"`)
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(result.Body)
}

func (handler PortfolioHandler) create(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID                       string   `json:"request_id"`
		ProjectID                       string   `json:"project_id"`
		ExpectedProjectVersion          uint64   `json:"expected_project_version"`
		ExpectedWorkspaceBindingVersion uint64   `json:"expected_workspace_binding_version"`
		WorkspaceRevision               string   `json:"workspace_revision"`
		ArtifactRevisionIDs             []string `json:"artifact_revision_ids"`
		Format                          string   `json:"format"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 256<<10))
	decoder.DisallowUnknownFields()
	if request.Header.Get("Content-Type") != portfolioExportMediaType || decoder.Decode(&body) != nil || decoder.Decode(&struct{}{}) == nil || !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.ProjectID) || body.ExpectedProjectVersion < 1 || body.ExpectedWorkspaceBindingVersion < 1 || body.WorkspaceRevision == "" || len(body.WorkspaceRevision) > 500 || body.Format != "html" && body.Format != "pdf" && body.Format != "zip" || !validPortfolioRevisionIDs(body.ArtifactRevisionIDs) {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	if actual, matched := ifMatch(request); !matched || actual != body.ExpectedProjectVersion {
		handler.problem(writer, request, http.StatusConflict, "version_conflict", true)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.Request(request.Context(), CreatePortfolioExportCommand{CommandMetadata: metadata, ProjectID: body.ProjectID, ExpectedProjectVersion: body.ExpectedProjectVersion, ExpectedWorkspaceBindingVersion: body.ExpectedWorkspaceBindingVersion, WorkspaceRevision: body.WorkspaceRevision, Format: body.Format, ArtifactRevisionIDs: body.ArtifactRevisionIDs})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, http.StatusAccepted, result)
}

func (handler PortfolioHandler) get(writer http.ResponseWriter, request *http.Request, id string) {
	claims, ok := handler.claims(writer, request, false)
	if !ok {
		return
	}
	if len(request.URL.Query()) != 0 {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	result, err := handler.Service.Get(request.Context(), claims.TenantID, claims.SubjectID, id)
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, http.StatusOK, result)
}

func (handler PortfolioHandler) metadata(writer http.ResponseWriter, request *http.Request) (CommandMetadata, bool) {
	claims, ok := handler.claims(writer, request, true)
	if !ok {
		return CommandMetadata{}, false
	}
	keys := request.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return CommandMetadata{}, false
	}
	return CommandMetadata{RequestID: claims.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, true
}

func (handler PortfolioHandler) claims(writer http.ResponseWriter, request *http.Request, csrf bool) (trustedcontext.Claims, bool) {
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if handler.Service == nil || !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || csrf && !claims.CSRFVerified {
		handler.problem(writer, request, http.StatusUnauthorized, "authentication_required", false)
		return trustedcontext.Claims{}, false
	}
	return claims, true
}

func (handler PortfolioHandler) write(writer http.ResponseWriter, status int, result PortfolioExportResult) {
	writer.Header().Set("Content-Type", portfolioExportMediaType)
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(result.Version, 10)+`"`)
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(result)
}

func (handler PortfolioHandler) finish(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
	case errors.Is(err, ErrResourceNotFound):
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
	case errors.Is(err, ErrStateConflict), errors.Is(err, ErrIdempotencyConflict):
		handler.problem(writer, request, http.StatusConflict, "state_conflict", true)
	default:
		handler.problem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", true)
	}
}

func (handler PortfolioHandler) problem(writer http.ResponseWriter, request *http.Request, status int, code string, retryable bool) {
	(RouteHandler{}).writeProblem(writer, request, status, code, strings.ReplaceAll(code, "_", " "), retryable)
}

func validPortfolioRevisionIDs(values []string) bool {
	if len(values) < 1 || len(values) > 100 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !uuidPattern.MatchString(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func portfolioExportPath(path string) (id string, download, ok bool) {
	if path == "/v1/portfolio-exports" {
		return "", false, true
	}
	value := strings.TrimPrefix(path, "/v1/portfolio-exports/")
	if value == path {
		return "", false, false
	}
	parts := strings.Split(value, "/")
	if len(parts) == 1 && uuidPattern.MatchString(parts[0]) {
		return parts[0], false, true
	}
	if len(parts) == 2 && uuidPattern.MatchString(parts[0]) && parts[1] == "download" {
		return parts[0], true, true
	}
	return "", false, false
}
