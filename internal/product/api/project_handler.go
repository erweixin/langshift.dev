package api

import (
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

type ProjectHandler struct {
	Service ProjectService
	Tests   ProjectTestService
}

func (handler ProjectHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	projectID, child, childID, ok := projectPath(request.URL.Path)
	if !ok {
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
		return
	}
	if projectID == "" {
		switch request.Method {
		case http.MethodGet:
			handler.list(writer, request)
		case http.MethodPost:
			handler.create(writer, request)
		default:
			handler.methodNotAllowed(writer, request, "GET, POST")
		}
		return
	}
	switch child {
	case "":
		if request.Method != http.MethodPatch {
			handler.methodNotAllowed(writer, request, http.MethodPatch)
			return
		}
		handler.changeStatus(writer, request, projectID)
	case "milestones":
		if childID == "" && request.Method == http.MethodPost {
			handler.createMilestone(writer, request, projectID)
			return
		}
		if childID != "" && request.Method == http.MethodPatch {
			handler.transitionMilestone(writer, request, projectID, childID)
			return
		}
		handler.methodNotAllowed(writer, request, map[bool]string{true: http.MethodPost, false: http.MethodPatch}[childID == ""])
	case "workspace":
		switch request.Method {
		case http.MethodGet:
			handler.getWorkspace(writer, request, projectID)
		case http.MethodPost:
			handler.bindWorkspace(writer, request, projectID)
		case http.MethodPatch:
			handler.advanceWorkspace(writer, request, projectID)
		default:
			handler.methodNotAllowed(writer, request, "GET, POST, PATCH")
		}
	case "completion":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.complete(writer, request, projectID)
	case "test-runs":
		if request.Method != http.MethodPost {
			handler.methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		handler.generateTest(writer, request, projectID)
	default:
		handler.problem(writer, request, http.StatusNotFound, "resource_not_found", false)
	}
}

func (handler ProjectHandler) generateTest(writer http.ResponseWriter, request *http.Request, projectID string) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	if handler.Tests == nil {
		handler.problem(writer, request, http.StatusServiceUnavailable, "dependency_unavailable", true)
		return
	}
	var body struct {
		RequestID                       string          `json:"request_id"`
		MilestoneID                     string          `json:"milestone_id"`
		ValidationKind                  string          `json:"validation_kind"`
		ValidationSpec                  json.RawMessage `json:"validation_spec"`
		WorkspaceRevision               string          `json:"workspace_revision"`
		ExpectedProjectVersion          uint64          `json:"expected_project_version"`
		ExpectedMilestoneVersion        uint64          `json:"expected_milestone_version"`
		ExpectedWorkspaceBindingVersion uint64          `json:"expected_workspace_binding_version"`
	}
	if !handler.decode(writer, request, "application/vnd.lites.project-test-generation.v2+json", &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.MilestoneID) || body.ValidationKind != "deterministic_test" && body.ValidationKind != "rubric_review" || !validProjectJSONObject(body.ValidationSpec) || body.WorkspaceRevision == "" || len(body.WorkspaceRevision) > 500 || body.ExpectedProjectVersion < 1 || body.ExpectedMilestoneVersion < 1 || body.ExpectedWorkspaceBindingVersion < 1 {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	if !handler.matchVersion(writer, request, body.ExpectedProjectVersion) {
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Tests.Generate(request.Context(), GenerateProjectTestCommand{CommandMetadata: metadata, ProjectID: projectID, MilestoneID: body.MilestoneID, WorkspaceRevision: body.WorkspaceRevision, ValidationKind: body.ValidationKind, ExpectedProjectVersion: body.ExpectedProjectVersion, ExpectedMilestoneVersion: body.ExpectedMilestoneVersion, ExpectedWorkspaceBindingVersion: body.ExpectedWorkspaceBindingVersion, ValidationSpec: body.ValidationSpec})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, http.StatusAccepted, "application/vnd.lites.project-test-generation.v2+json", result.ProjectVersion, result)
}

func (handler ProjectHandler) list(writer http.ResponseWriter, request *http.Request) {
	claims, ok := handler.claims(writer, request, false)
	if !ok {
		return
	}
	values := request.URL.Query()
	for key, entries := range values {
		if key != "cursor" || len(entries) != 1 || entries[0] == "" {
			handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
			return
		}
	}
	result, err := handler.Service.List(request.Context(), ProjectListQuery{TenantID: claims.TenantID, UserID: claims.SubjectID, Cursor: values.Get("cursor")})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	if result.Items == nil {
		result.Items = []ProjectResource{}
	}
	handler.write(writer, http.StatusOK, "application/vnd.lites.projects.v2+json", 0, result)
}

func (handler ProjectHandler) create(writer http.ResponseWriter, request *http.Request) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID               string `json:"request_id"`
		MissionID               string `json:"mission_id"`
		AcceptedRouteRevisionID string `json:"accepted_route_revision_id"`
		ProjectKind             string `json:"project_kind"`
		Title                   string `json:"title"`
		Brief                   string `json:"brief"`
	}
	if !handler.decode(writer, request, "application/vnd.lites.project-create.v2+json", &body) || !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.MissionID) || !uuidPattern.MatchString(body.AcceptedRouteRevisionID) || body.ProjectKind != "code" && body.ProjectKind != "writing" && body.ProjectKind != "design" || len(body.Title) < 1 || len(body.Title) > 200 || len(body.Brief) < 1 || len(body.Brief) > 10000 {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.Create(request.Context(), CreateProjectCommand{CommandMetadata: metadata, MissionID: body.MissionID, RouteRevisionID: body.AcceptedRouteRevisionID, ProjectKind: body.ProjectKind, Title: body.Title, Brief: body.Brief})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, http.StatusOK, "application/vnd.lites.project.v2+json", result.Version, result)
}

func (handler ProjectHandler) changeStatus(writer http.ResponseWriter, request *http.Request, projectID string) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID              string `json:"request_id"`
		Status                 string `json:"status"`
		Reason                 string `json:"reason"`
		ExpectedProjectVersion uint64 `json:"expected_project_version"`
	}
	if !handler.decode(writer, request, "application/vnd.lites.project-status.v2+json", &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || body.ExpectedProjectVersion < 1 || body.Status != "active" && body.Status != "blocked" && body.Status != "archived" || len(body.Reason) > 2000 {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	if !handler.matchVersion(writer, request, body.ExpectedProjectVersion) {
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.ChangeStatus(request.Context(), ChangeProjectStatusCommand{CommandMetadata: metadata, ProjectID: projectID, Status: body.Status, Reason: body.Reason, ExpectedProjectVersion: body.ExpectedProjectVersion})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, http.StatusOK, "application/vnd.lites.project.v2+json", result.Version, result)
}

func (handler ProjectHandler) createMilestone(writer http.ResponseWriter, request *http.Request, projectID string) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID              string          `json:"request_id"`
		Title                  string          `json:"title"`
		Required               bool            `json:"required"`
		Sequence               int             `json:"sequence"`
		AcceptanceSpec         json.RawMessage `json:"acceptance_spec"`
		ExpectedProjectVersion uint64          `json:"expected_project_version"`
	}
	if !handler.decode(writer, request, "application/vnd.lites.milestone-create.v2+json", &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || len(body.Title) < 1 || len(body.Title) > 200 || body.Sequence < 1 || body.ExpectedProjectVersion < 1 || !validProjectJSONObject(body.AcceptanceSpec) {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	if !handler.matchVersion(writer, request, body.ExpectedProjectVersion) {
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.CreateMilestone(request.Context(), CreateMilestoneCommand{CommandMetadata: metadata, ProjectID: projectID, Title: body.Title, Required: body.Required, Sequence: body.Sequence, AcceptanceSpec: body.AcceptanceSpec, ExpectedProjectVersion: body.ExpectedProjectVersion})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, http.StatusOK, "application/vnd.lites.milestone.v2+json", result.Version, result)
}

func (handler ProjectHandler) transitionMilestone(writer http.ResponseWriter, request *http.Request, projectID, milestoneID string) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID                string  `json:"request_id"`
		Action                   string  `json:"action"`
		Result                   *string `json:"result"`
		ExpectedProjectVersion   uint64  `json:"expected_project_version"`
		ExpectedMilestoneVersion uint64  `json:"expected_milestone_version"`
	}
	if !handler.decode(writer, request, "application/vnd.lites.milestone-transition.v2+json", &body) {
		return
	}
	validAction := body.Action == "start" || body.Action == "submit" || body.Action == "verify" || body.Action == "request_rework" || body.Action == "complete"
	if !validClientRequestID(body.RequestID) || !validAction || body.ExpectedProjectVersion < 1 || body.ExpectedMilestoneVersion < 1 || body.Action == "submit" && (body.Result == nil || len(*body.Result) < 1 || len(*body.Result) > 20000) || body.Action != "submit" && body.Result != nil {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	if !handler.matchVersion(writer, request, body.ExpectedProjectVersion) {
		return
	}
	resultText := ""
	if body.Result != nil {
		resultText = *body.Result
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.TransitionMilestone(request.Context(), TransitionMilestoneCommand{CommandMetadata: metadata, ProjectID: projectID, MilestoneID: milestoneID, Action: body.Action, Result: resultText, ExpectedProjectVersion: body.ExpectedProjectVersion, ExpectedMilestoneVersion: body.ExpectedMilestoneVersion})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, http.StatusOK, "application/vnd.lites.milestone.v2+json", result.Version, result)
}

func (handler ProjectHandler) getWorkspace(writer http.ResponseWriter, request *http.Request, projectID string) {
	claims, ok := handler.claims(writer, request, false)
	if !ok {
		return
	}
	result, err := handler.Service.GetWorkspace(request.Context(), claims.TenantID, claims.SubjectID, projectID)
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, http.StatusOK, "application/vnd.lites.project-workspace.v2+json", result.Version, result)
}

func (handler ProjectHandler) bindWorkspace(writer http.ResponseWriter, request *http.Request, projectID string) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID              string `json:"request_id"`
		WorkspaceID            string `json:"workspace_id"`
		BranchName             string `json:"branch_name"`
		BaseRevision           string `json:"base_revision"`
		ExpectedProjectVersion uint64 `json:"expected_project_version"`
	}
	if !handler.decode(writer, request, "application/vnd.lites.project-workspace-bind.v2+json", &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.WorkspaceID) || body.BranchName == "" || len(body.BranchName) > 240 || body.BaseRevision == "" || body.ExpectedProjectVersion < 1 {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	if !handler.matchVersion(writer, request, body.ExpectedProjectVersion) {
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.BindWorkspace(request.Context(), BindWorkspaceCommand{CommandMetadata: metadata, ProjectID: projectID, WorkspaceID: body.WorkspaceID, BranchName: body.BranchName, BaseRevision: body.BaseRevision, ExpectedProjectVersion: body.ExpectedProjectVersion})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, http.StatusOK, "application/vnd.lites.project-workspace.v2+json", result.Version, result)
}

func (handler ProjectHandler) advanceWorkspace(writer http.ResponseWriter, request *http.Request, projectID string) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID              string `json:"request_id"`
		BindingID              string `json:"binding_id"`
		ExpectedHeadRevision   string `json:"expected_head_revision"`
		HeadRevision           string `json:"head_revision"`
		ExpectedProjectVersion uint64 `json:"expected_project_version"`
		ExpectedBindingVersion uint64 `json:"expected_binding_version"`
	}
	if !handler.decode(writer, request, "application/vnd.lites.project-workspace-advance.v2+json", &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || !uuidPattern.MatchString(body.BindingID) || body.ExpectedProjectVersion < 1 || body.ExpectedBindingVersion < 1 || body.ExpectedHeadRevision == "" || body.HeadRevision == "" || body.ExpectedHeadRevision == body.HeadRevision {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	if !handler.matchVersion(writer, request, body.ExpectedProjectVersion) {
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.AdvanceWorkspace(request.Context(), AdvanceWorkspaceCommand{CommandMetadata: metadata, ProjectID: projectID, BindingID: body.BindingID, ExpectedProjectVersion: body.ExpectedProjectVersion, ExpectedBindingVersion: body.ExpectedBindingVersion, ExpectedHeadRevision: body.ExpectedHeadRevision, HeadRevision: body.HeadRevision})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, http.StatusOK, "application/vnd.lites.project-workspace.v2+json", result.Version, result)
}

func (handler ProjectHandler) complete(writer http.ResponseWriter, request *http.Request, projectID string) {
	metadata, ok := handler.metadata(writer, request)
	if !ok {
		return
	}
	var body struct {
		RequestID              string `json:"request_id"`
		Reflection             string `json:"reflection"`
		WorkspaceRevision      string `json:"workspace_revision"`
		ExpectedProjectVersion uint64 `json:"expected_project_version"`
	}
	if !handler.decode(writer, request, "application/vnd.lites.project-complete.v2+json", &body) {
		return
	}
	if !validClientRequestID(body.RequestID) || len(body.Reflection) < 1 || len(body.Reflection) > 20000 || body.WorkspaceRevision == "" || body.ExpectedProjectVersion < 1 {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return
	}
	if !handler.matchVersion(writer, request, body.ExpectedProjectVersion) {
		return
	}
	metadata.ClientRequestID = body.RequestID
	result, err := handler.Service.Complete(request.Context(), CompleteProjectCommand{CommandMetadata: metadata, ProjectID: projectID, Reflection: body.Reflection, WorkspaceRevision: body.WorkspaceRevision, ExpectedProjectVersion: body.ExpectedProjectVersion})
	if err != nil {
		handler.finish(writer, request, err)
		return
	}
	handler.write(writer, http.StatusOK, "application/vnd.lites.project.v2+json", result.Version, result)
}

func (handler ProjectHandler) metadata(writer http.ResponseWriter, request *http.Request) (CommandMetadata, bool) {
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

func (handler ProjectHandler) claims(writer http.ResponseWriter, request *http.Request, csrf bool) (trustedcontext.Claims, bool) {
	claims, ok := serviceauth.ClaimsFromContext(request.Context())
	if handler.Service == nil || !ok || claims.PrincipalKind != trustedcontext.AuthenticatedUser || claims.SubjectID == "" || claims.TenantID == "" || claims.MembershipID == "" || claims.SessionID == "" || csrf && !claims.CSRFVerified {
		handler.problem(writer, request, http.StatusUnauthorized, "authentication_required", false)
		return trustedcontext.Claims{}, false
	}
	return claims, true
}

func (handler ProjectHandler) decode(writer http.ResponseWriter, request *http.Request, contentType string, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 512<<10))
	decoder.DisallowUnknownFields()
	if request.Header.Get("Content-Type") != contentType || decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) == nil {
		handler.problem(writer, request, http.StatusBadRequest, "validation_failed", false)
		return false
	}
	return true
}

func (handler ProjectHandler) matchVersion(writer http.ResponseWriter, request *http.Request, expected uint64) bool {
	actual, ok := ifMatch(request)
	if !ok || actual != expected {
		handler.problem(writer, request, http.StatusConflict, "version_conflict", true)
		return false
	}
	return true
}

func (handler ProjectHandler) write(writer http.ResponseWriter, status int, contentType string, version uint64, value any) {
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", "private, no-store")
	if version > 0 {
		writer.Header().Set("ETag", `"`+strconv.FormatUint(version, 10)+`"`)
	}
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func (handler ProjectHandler) finish(writer http.ResponseWriter, request *http.Request, err error) {
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

func (handler ProjectHandler) methodNotAllowed(writer http.ResponseWriter, request *http.Request, allow string) {
	writer.Header().Set("Allow", allow)
	handler.problem(writer, request, http.StatusMethodNotAllowed, "method_not_allowed", false)
}

func (handler ProjectHandler) problem(writer http.ResponseWriter, request *http.Request, status int, code string, retryable bool) {
	(RouteHandler{}).writeProblem(writer, request, status, code, strings.ReplaceAll(code, "_", " "), retryable)
}

func projectPath(path string) (projectID, child, childID string, ok bool) {
	if path == "/v1/projects" {
		return "", "", "", true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/v1/projects/"), "/")
	if len(parts) < 1 || len(parts) > 3 || !uuidPattern.MatchString(parts[0]) {
		return "", "", "", false
	}
	projectID = parts[0]
	if len(parts) == 1 {
		return projectID, "", "", true
	}
	child = parts[1]
	if child != "milestones" && child != "workspace" && child != "completion" && child != "test-runs" {
		return "", "", "", false
	}
	if len(parts) == 3 {
		if child != "milestones" || !uuidPattern.MatchString(parts[2]) {
			return "", "", "", false
		}
		childID = parts[2]
	}
	return projectID, child, childID, true
}

func validProjectJSONObject(value json.RawMessage) bool {
	if !json.Valid(value) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}
