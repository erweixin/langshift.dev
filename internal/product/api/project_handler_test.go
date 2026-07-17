package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type projectServiceStub struct {
	create    func(CreateProjectCommand) (ProjectMutationResult, error)
	milestone func(TransitionMilestoneCommand) (MilestoneMutationResult, error)
	workspace func(BindWorkspaceCommand) (WorkspaceMutationResult, error)
}

type projectTestServiceStub struct {
	generate func(GenerateProjectTestCommand) (ProjectTestGenerationResult, error)
}

func (stub projectTestServiceStub) Generate(_ context.Context, command GenerateProjectTestCommand) (ProjectTestGenerationResult, error) {
	return stub.generate(command)
}

func (stub projectServiceStub) List(context.Context, ProjectListQuery) (ProjectListResult, error) {
	return ProjectListResult{}, nil
}
func (stub projectServiceStub) Create(_ context.Context, command CreateProjectCommand) (ProjectMutationResult, error) {
	return stub.create(command)
}
func (stub projectServiceStub) ChangeStatus(context.Context, ChangeProjectStatusCommand) (ProjectMutationResult, error) {
	return ProjectMutationResult{}, nil
}
func (stub projectServiceStub) CreateMilestone(context.Context, CreateMilestoneCommand) (MilestoneMutationResult, error) {
	return MilestoneMutationResult{}, nil
}
func (stub projectServiceStub) TransitionMilestone(_ context.Context, command TransitionMilestoneCommand) (MilestoneMutationResult, error) {
	return stub.milestone(command)
}
func (stub projectServiceStub) GetWorkspace(context.Context, string, string, string) (WorkspaceResource, error) {
	return WorkspaceResource{}, nil
}
func (stub projectServiceStub) BindWorkspace(_ context.Context, command BindWorkspaceCommand) (WorkspaceMutationResult, error) {
	return stub.workspace(command)
}
func (stub projectServiceStub) AdvanceWorkspace(context.Context, AdvanceWorkspaceCommand) (WorkspaceMutationResult, error) {
	return WorkspaceMutationResult{}, nil
}
func (stub projectServiceStub) Complete(context.Context, CompleteProjectCommand) (ProjectMutationResult, error) {
	return ProjectMutationResult{}, nil
}

func TestProjectCreateBindsAcceptedRouteAndReturnsProjectETag(t *testing.T) {
	missionID := "e6000000-0000-4000-8000-000000000001"
	routeID := "e6000000-0000-4000-8000-000000000002"
	projectID := "e6000000-0000-4000-8000-000000000003"
	service := projectServiceStub{create: func(command CreateProjectCommand) (ProjectMutationResult, error) {
		if command.MissionID != missionID || command.RouteRevisionID != routeID || command.ProjectKind != "writing" || command.Title != "Production project" || command.Brief != "Complete brief" || command.IdempotencyKey != "project-create-key-0001" {
			t.Fatalf("command=%#v", command)
		}
		return ProjectMutationResult{ID: projectID, Version: 1, Status: "active", UpdatedAt: missionAPINow, EventID: "e6000000-0000-4000-8000-000000000004"}, nil
	}}
	request := authenticatedMissionRequest(t, http.MethodPost, "/v1/projects", "application/vnd.lites.project-create.v2+json", `{"request_id":"project-create-client-0001","mission_id":"`+missionID+`","accepted_route_revision_id":"`+routeID+`","project_kind":"writing","title":"Production project","brief":"Complete brief"}`, "project-create-key-0001", "", true)
	recorder := httptest.NewRecorder()
	request.handler(ProjectHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"1"` || recorder.Header().Get("Content-Type") != "application/vnd.lites.project.v2+json" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestMilestoneTransitionRequiresParentAndMilestoneCAS(t *testing.T) {
	projectID := "e6000000-0000-4000-8000-000000000010"
	milestoneID := "e6000000-0000-4000-8000-000000000011"
	service := projectServiceStub{milestone: func(command TransitionMilestoneCommand) (MilestoneMutationResult, error) {
		if command.ProjectID != projectID || command.MilestoneID != milestoneID || command.Action != "submit" || command.Result != "reviewable result" || command.ExpectedProjectVersion != 7 || command.ExpectedMilestoneVersion != 2 {
			t.Fatalf("command=%#v", command)
		}
		return MilestoneMutationResult{ProjectMutationResult: ProjectMutationResult{ID: projectID, Version: 8, Status: "active", UpdatedAt: time.Now()}, MilestoneID: milestoneID, MilestoneVersion: 3, MilestoneStatus: "submitted"}, nil
	}}
	target := "/v1/projects/" + projectID + "/milestones/" + milestoneID
	request := authenticatedMissionRequest(t, http.MethodPatch, target, "application/vnd.lites.milestone-transition.v2+json", `{"request_id":"milestone-submit-client-0001","action":"submit","result":"reviewable result","expected_project_version":7,"expected_milestone_version":2}`, "milestone-submit-key-0001", `"7"`, true)
	recorder := httptest.NewRecorder()
	request.handler(ProjectHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"8"` {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request = authenticatedMissionRequest(t, http.MethodPatch, target, "application/vnd.lites.milestone-transition.v2+json", `{"request_id":"milestone-submit-client-0001","action":"submit","result":"reviewable result","expected_project_version":7,"expected_milestone_version":2}`, "milestone-submit-key-0001", `"6"`, true)
	recorder = httptest.NewRecorder()
	request.handler(ProjectHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("stale status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWorkspaceBindDecodesClosedSnakeCaseContract(t *testing.T) {
	projectID := "e6000000-0000-4000-8000-000000000020"
	workspaceID := "e6000000-0000-4000-8000-000000000021"
	service := projectServiceStub{workspace: func(command BindWorkspaceCommand) (WorkspaceMutationResult, error) {
		if command.WorkspaceID != workspaceID || command.BranchName != "project/e6" || command.BaseRevision != "git:e6" || command.ExpectedProjectVersion != 3 {
			t.Fatalf("command=%#v", command)
		}
		return WorkspaceMutationResult{ProjectMutationResult: ProjectMutationResult{ID: projectID, Version: 4, Status: "active", UpdatedAt: missionAPINow}, Workspace: WorkspaceResource{ID: "e6000000-0000-4000-8000-000000000022", Version: 1, WorkspaceID: workspaceID}}, nil
	}}
	target := "/v1/projects/" + projectID + "/workspace"
	request := authenticatedMissionRequest(t, http.MethodPost, target, "application/vnd.lites.project-workspace-bind.v2+json", `{"request_id":"workspace-bind-client-0001","workspace_id":"`+workspaceID+`","branch_name":"project/e6","base_revision":"git:e6","expected_project_version":3}`, "workspace-bind-key-0001", `"3"`, true)
	recorder := httptest.NewRecorder()
	request.handler(ProjectHandler{Service: service}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("ETag") != `"4"` {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestProjectTestGenerationIsAsyncAndCannotAcceptCallerVerdict(t *testing.T) {
	projectID := "e6000000-0000-4000-8000-000000000030"
	milestoneID := "e6000000-0000-4000-8000-000000000031"
	tests := projectTestServiceStub{generate: func(command GenerateProjectTestCommand) (ProjectTestGenerationResult, error) {
		if command.ProjectID != projectID || command.MilestoneID != milestoneID || command.ValidationKind != "rubric_review" || command.WorkspaceRevision != "git:exact" || command.ExpectedProjectVersion != 9 || command.ExpectedMilestoneVersion != 3 || command.ExpectedWorkspaceBindingVersion != 2 || string(command.ValidationSpec) != `{"rubric":"production"}` {
			t.Fatalf("command=%#v", command)
		}
		return ProjectTestGenerationResult{GenerationID: "e6000000-0000-4000-8000-000000000032", RunID: "e6000000-0000-4000-8000-000000000033", Status: "queued", AcceptedAt: missionAPINow, ProjectID: projectID, MilestoneID: milestoneID, ProjectVersion: 9, WorkspaceRevision: "git:exact"}, nil
	}}
	target := "/v1/projects/" + projectID + "/test-runs"
	body := `{"request_id":"project-test-client-0001","milestone_id":"` + milestoneID + `","validation_kind":"rubric_review","validation_spec":{"rubric":"production"},"workspace_revision":"git:exact","expected_project_version":9,"expected_milestone_version":3,"expected_workspace_binding_version":2}`
	request := authenticatedMissionRequest(t, http.MethodPost, target, "application/vnd.lites.project-test-generation.v2+json", body, "project-test-key-0001", `"9"`, true)
	recorder := httptest.NewRecorder()
	request.handler(ProjectHandler{Service: projectServiceStub{}, Tests: tests}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusAccepted || recorder.Header().Get("ETag") != `"9"` || recorder.Header().Get("Content-Type") != "application/vnd.lites.project-test-generation.v2+json" {
		t.Fatalf("status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	unsafe := `{"request_id":"project-test-client-0002","milestone_id":"` + milestoneID + `","validation_kind":"rubric_review","validation_spec":{"rubric":"production"},"workspace_revision":"git:exact","expected_project_version":9,"expected_milestone_version":3,"expected_workspace_binding_version":2,"result":"passed"}`
	request = authenticatedMissionRequest(t, http.MethodPost, target, "application/vnd.lites.project-test-generation.v2+json", unsafe, "project-test-key-0002", `"9"`, true)
	recorder = httptest.NewRecorder()
	request.handler(ProjectHandler{Service: projectServiceStub{}, Tests: tests}).ServeHTTP(recorder, request.request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("caller verdict accepted status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
