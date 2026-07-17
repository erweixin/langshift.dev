package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type portfolioServiceStub struct {
	request func(CreatePortfolioExportCommand) (PortfolioExportResult, error)
	get     func(string, string, string) (PortfolioExportResult, error)
}

func (stub portfolioServiceStub) Request(_ context.Context, command CreatePortfolioExportCommand) (PortfolioExportResult, error) {
	return stub.request(command)
}

func (stub portfolioServiceStub) Get(_ context.Context, tenantID, userID, exportID string) (PortfolioExportResult, error) {
	return stub.get(tenantID, userID, exportID)
}

func TestPortfolioExportAcceptsOnlyRevisionSelectionAndIsAsync(t *testing.T) {
	projectID := "f3000000-0000-4000-8000-000000000001"
	revisionID := "f3000000-0000-4000-8000-000000000002"
	exportID := "f3000000-0000-4000-8000-000000000003"
	runID := "f3000000-0000-4000-8000-000000000004"
	service := portfolioServiceStub{request: func(command CreatePortfolioExportCommand) (PortfolioExportResult, error) {
		if command.ProjectID != projectID || command.ExpectedProjectVersion != 12 || command.ExpectedWorkspaceBindingVersion != 3 || command.WorkspaceRevision != "git:portfolio" || command.Format != "pdf" || len(command.ArtifactRevisionIDs) != 1 || command.ArtifactRevisionIDs[0] != revisionID || command.IdempotencyKey != "portfolio-key-0001" {
			t.Fatalf("command=%#v", command)
		}
		return PortfolioExportResult{ID: exportID, ProjectID: projectID, RunID: runID, Status: "requested", Version: 1, Format: "pdf", RevisionManifestHash: "manifest-hash", CreatedAt: missionAPINow}, nil
	}}
	body := `{"request_id":"portfolio-client-0001","project_id":"` + projectID + `","expected_project_version":12,"expected_workspace_binding_version":3,"workspace_revision":"git:portfolio","artifact_revision_ids":["` + revisionID + `"],"format":"pdf"}`
	request := authenticatedMissionRequest(t, http.MethodPost, "/v1/portfolio-exports", portfolioExportMediaType, body, "portfolio-key-0001", `"12"`, true)
	recorder := httptest.NewRecorder()
	request.handler(PortfolioHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusAccepted || recorder.Header().Get("ETag") != `"1"` || recorder.Header().Get("Content-Type") != portfolioExportMediaType {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}

	unsafe := `{"request_id":"portfolio-client-0002","project_id":"` + projectID + `","expected_project_version":12,"expected_workspace_binding_version":3,"workspace_revision":"git:portfolio","artifact_revision_ids":["` + revisionID + `"],"format":"pdf","content_hash":"caller-hash","scan_status":"passed"}`
	request = authenticatedMissionRequest(t, http.MethodPost, "/v1/portfolio-exports", portfolioExportMediaType, unsafe, "portfolio-key-0002", `"12"`, true)
	recorder = httptest.NewRecorder()
	request.handler(PortfolioHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("caller provenance accepted status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPortfolioExportGetIsOwnerScopedAndReturnsLifecycleVersion(t *testing.T) {
	exportID := "f3000000-0000-4000-8000-000000000010"
	service := portfolioServiceStub{get: func(tenantID, userID, requestedID string) (PortfolioExportResult, error) {
		if tenantID == "" || userID == "" || requestedID != exportID {
			t.Fatalf("scope tenant=%q user=%q export=%q", tenantID, userID, requestedID)
		}
		return PortfolioExportResult{ID: exportID, ProjectID: "f3000000-0000-4000-8000-000000000011", RunID: "f3000000-0000-4000-8000-000000000012", Status: "building", Version: 2, Format: "html", RevisionManifestHash: "manifest", CreatedAt: missionAPINow}, nil
	}}
	request := authenticatedMissionRequest(t, http.MethodGet, "/v1/portfolio-exports/"+exportID, "", "", "", "", false)
	recorder := httptest.NewRecorder()
	request.handler(PortfolioHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"2"` || recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}
