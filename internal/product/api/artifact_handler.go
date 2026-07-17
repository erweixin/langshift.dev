package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/langshift/lites/internal/idempotency"
	"github.com/langshift/lites/internal/security/transport"
	"github.com/langshift/lites/internal/security/trustedcontext"
	"github.com/langshift/lites/internal/serviceauth"
)

const (
	artifactCreateMediaType   = "application/vnd.lites.artifact-create.v2+json"
	artifactRevisionMediaType = "application/vnd.lites.artifact-revision-create.v2+json"
	maximumArtifactBodyBytes  = 4<<20 + 64<<10
)

type ArtifactMutationResult struct {
	ID              string    `json:"id"`
	Version         uint64    `json:"version"`
	Status          string    `json:"status"`
	CurrentRevision int       `json:"current_revision"`
	UpdatedAt       time.Time `json:"updated_at"`
	Replayed        bool      `json:"replayed"`
}

type ArtifactRevisionResult struct {
	ID                   string    `json:"id"`
	ArtifactID           string    `json:"artifact_id"`
	ArtifactVersion      uint64    `json:"artifact_version"`
	Revision             int       `json:"revision"`
	Status               string    `json:"status"`
	ContentHash          string    `json:"content_hash"`
	EvidenceManifestHash string    `json:"evidence_manifest_hash"`
	UpdatedAt            time.Time `json:"updated_at"`
	Replayed             bool      `json:"replayed"`
}

type CreateArtifactCommand struct {
	CommandMetadata
	ProjectID, ArtifactKind, Title string
}

type CreateArtifactRevisionCommand struct {
	CommandMetadata
	ArtifactID, Content, MediaType, WorkspaceRevision string
	EvidenceIDs                                       []string
	ExpectedArtifactVersion                           uint64
}

type ArtifactService interface {
	Create(context.Context, CreateArtifactCommand) (ArtifactMutationResult, error)
	CreateRevision(context.Context, CreateArtifactRevisionCommand) (ArtifactRevisionResult, error)
}

type ArtifactHandler struct{ Service ArtifactService }

func (handler ArtifactHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	artifactID, revisions, ok := artifactPath(request.URL.Path)
	if !ok {
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
		return
	}
	if artifactID == "" && !revisions && request.Method == http.MethodPost {
		handler.create(writer, request)
		return
	}
	if artifactID != "" && revisions && request.Method == http.MethodPost {
		handler.revise(writer, request, artifactID)
		return
	}
	writer.Header().Set("Allow", http.MethodPost)
	handler.problem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", false)
}

func (handler ArtifactHandler) create(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID    string `json:"request_id"`
		ProjectID    string `json:"project_id"`
		ArtifactKind string `json:"artifact_kind"`
		Title        string `json:"title"`
	}
	if !handler.decode(writer, request, artifactCreateMediaType, &body) || !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.ProjectID) || body.ArtifactKind != "code" && body.ArtifactKind != "writing" && body.ArtifactKind != "design" || !validArtifactText(body.Title, 200) {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.Create(request.Context(), CreateArtifactCommand{CommandMetadata: metadata, ProjectID: body.ProjectID, ArtifactKind: body.ArtifactKind, Title: body.Title})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, http.StatusOK, artifactCreateMediaType, result.Version, result)
}

func (handler ArtifactHandler) revise(writer http.ResponseWriter, request *http.Request, artifactID string) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	expected, matched := ifMatch(request)
	if !matched {
		handler.problem(writer, request, http.StatusPreconditionRequired, "precondition_required", false)
		return
	}
	var body struct {
		RequestID               string   `json:"request_id"`
		Content                 string   `json:"content"`
		MediaType               string   `json:"media_type"`
		WorkspaceRevision       string   `json:"workspace_revision"`
		EvidenceIDs             []string `json:"evidence_ids"`
		ExpectedArtifactVersion uint64   `json:"expected_artifact_version"`
	}
	if !handler.decode(writer, request, artifactRevisionMediaType, &body) {
		return
	}
	validMedia := body.MediaType == "text/plain" || body.MediaType == "text/markdown" || body.MediaType == "application/json"
	if !validClientRequestID(body.RequestID) || body.ExpectedArtifactVersion < 1 || !utf8.ValidString(body.Content) || len(body.Content) < 1 || len(body.Content) > 4<<20 || !validMedia || body.MediaType == "application/json" && !json.Valid([]byte(body.Content)) || body.WorkspaceRevision == "" || len(body.WorkspaceRevision) > 500 || !validArtifactIDs(body.EvidenceIDs) {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	if body.ExpectedArtifactVersion != expected {
		handler.problem(writer, request, http.StatusConflict, "version_conflict", true)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.CreateRevision(request.Context(), CreateArtifactRevisionCommand{CommandMetadata: metadata, ArtifactID: artifactID, Content: body.Content, MediaType: body.MediaType, WorkspaceRevision: body.WorkspaceRevision, EvidenceIDs: body.EvidenceIDs, ExpectedArtifactVersion: body.ExpectedArtifactVersion})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, http.StatusOK, artifactRevisionMediaType, result.ArtifactVersion, result)
}

func (handler ArtifactHandler) metadata(writer http.ResponseWriter, request *http.Request) (CommandMetadata, bool) {
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if handler.Service == nil || !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || !claims.CSRFVerified {
		handler.problem(writer, request, http.StatusUnauthorized, "authentication_required", false)
		return CommandMetadata{}, false
	}
	keys := request.Header.Values(transport.IdempotencyHeader)
	if len(keys) != 1 || idempotency.ValidateRawKey(keys[0]) != nil {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return CommandMetadata{}, false
	}
	return CommandMetadata{RequestID: claims.RequestID, IdempotencyKey: keys[0], TenantID: claims.TenantID, UserID: claims.SubjectID, SessionID: claims.SessionID}, true
}

func (handler ArtifactHandler) decode(writer http.ResponseWriter, request *http.Request, expected string, target any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != expected {
		handler.problem(writer, request, http.StatusUnsupportedMediaType, "unsupported_media_type", false)
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maximumArtifactBodyBytes))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			handler.problem(writer, request, http.StatusRequestEntityTooLarge, "request_too_large", false)
			return false
		}
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return false
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return false
	}
	return true
}

func (handler ArtifactHandler) write(writer http.ResponseWriter, status int, contentType string, version uint64, value any) {
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("ETag", `"`+strconv.FormatUint(version, 10)+`"`)
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func (handler ArtifactHandler) finish(writer http.ResponseWriter, request *http.Request, err error) {
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

func (handler ArtifactHandler) problem(writer http.ResponseWriter, request *http.Request, status int, code string, retryable bool) {
	(RouteHandler{}).writeProblem(writer, request, status, code, strings.ReplaceAll(code, "_", " "), retryable)
}

func artifactPath(path string) (artifactID string, revisions, ok bool) {
	if path == "/v1/artifacts" {
		return "", false, true
	}
	value := strings.TrimPrefix(path, "/v1/artifacts/")
	if value == path || !strings.HasSuffix(value, "/revisions") {
		return "", false, false
	}
	id := strings.TrimSuffix(value, "/revisions")
	return id, true, uuidPattern.MatchString(id)
}

func validArtifactText(value string, maximum int) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func validArtifactIDs(values []string) bool {
	if len(values) < 1 || len(values) > 100 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !uuidPattern.MatchString(value) {
			return false
		}
		if _, found := seen[value]; found {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}
