package api

import (
	"context"
	"encoding/json"
	"time"
)

type ProjectResource struct {
	ID              string     `json:"id"`
	MissionID       string     `json:"mission_id"`
	RouteRevisionID string     `json:"accepted_route_revision_id"`
	Version         uint64     `json:"version"`
	Status          string     `json:"status"`
	ProjectKind     string     `json:"project_kind"`
	Title           string     `json:"title"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at"`
}

type ProjectListQuery struct{ TenantID, UserID, Cursor string }
type ProjectListResult struct {
	Items      []ProjectResource `json:"items"`
	NextCursor *string           `json:"next_cursor"`
}

type ProjectMutationResult struct {
	ID                     string    `json:"id"`
	Version                uint64    `json:"version"`
	Status                 string    `json:"status"`
	UpdatedAt              time.Time `json:"updated_at"`
	EventID                string    `json:"event_id"`
	CompletionManifestHash string    `json:"completion_manifest_hash,omitempty"`
	Replayed               bool      `json:"replayed"`
}

type MilestoneMutationResult struct {
	ProjectMutationResult
	MilestoneID      string `json:"milestone_id"`
	MilestoneVersion uint64 `json:"milestone_version"`
	MilestoneStatus  string `json:"milestone_status"`
}

type WorkspaceResource struct {
	ID                  string    `json:"id"`
	ProjectID           string    `json:"project_id"`
	WorkspaceID         string    `json:"workspace_id"`
	BranchName          string    `json:"branch_name"`
	BaseRevision        string    `json:"base_revision"`
	HeadRevision        string    `json:"head_revision"`
	BindingManifestHash string    `json:"binding_manifest_hash"`
	Version             uint64    `json:"version"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type WorkspaceMutationResult struct {
	ProjectMutationResult
	Workspace WorkspaceResource `json:"workspace"`
}

type CreateProjectCommand struct {
	CommandMetadata
	MissionID, RouteRevisionID, ProjectKind, Title, Brief string
}

type ChangeProjectStatusCommand struct {
	CommandMetadata
	ProjectID, Status, Reason string
	ExpectedProjectVersion    uint64
}

type CreateMilestoneCommand struct {
	CommandMetadata
	ProjectID, Title       string
	ExpectedProjectVersion uint64
	Sequence               int
	Required               bool
	AcceptanceSpec         json.RawMessage
}

type TransitionMilestoneCommand struct {
	CommandMetadata
	ProjectID, MilestoneID, Action, Result string
	ExpectedProjectVersion                 uint64
	ExpectedMilestoneVersion               uint64
}

type BindWorkspaceCommand struct {
	CommandMetadata
	ProjectID, WorkspaceID, BranchName, BaseRevision string
	ExpectedProjectVersion                           uint64
}

type AdvanceWorkspaceCommand struct {
	CommandMetadata
	ProjectID, BindingID, ExpectedHeadRevision, HeadRevision string
	ExpectedProjectVersion, ExpectedBindingVersion           uint64
}

type CompleteProjectCommand struct {
	CommandMetadata
	ProjectID, Reflection, WorkspaceRevision string
	ExpectedProjectVersion                   uint64
}

type GenerateProjectTestCommand struct {
	CommandMetadata
	ProjectID, MilestoneID, WorkspaceRevision, ValidationKind string
	ExpectedProjectVersion, ExpectedMilestoneVersion          uint64
	ExpectedWorkspaceBindingVersion                           uint64
	ValidationSpec                                            json.RawMessage
}

type ProjectTestGenerationResult struct {
	GenerationID      string    `json:"generation_id"`
	RunID             string    `json:"run_id"`
	Status            string    `json:"status"`
	AcceptedAt        time.Time `json:"accepted_at"`
	ProjectID         string    `json:"project_id"`
	MilestoneID       string    `json:"milestone_id"`
	ProjectVersion    uint64    `json:"project_version"`
	WorkspaceRevision string    `json:"workspace_revision"`
	Replayed          bool      `json:"replayed"`
}

type ProjectTestService interface {
	Generate(context.Context, GenerateProjectTestCommand) (ProjectTestGenerationResult, error)
}

type ProjectService interface {
	List(context.Context, ProjectListQuery) (ProjectListResult, error)
	Create(context.Context, CreateProjectCommand) (ProjectMutationResult, error)
	ChangeStatus(context.Context, ChangeProjectStatusCommand) (ProjectMutationResult, error)
	CreateMilestone(context.Context, CreateMilestoneCommand) (MilestoneMutationResult, error)
	TransitionMilestone(context.Context, TransitionMilestoneCommand) (MilestoneMutationResult, error)
	GetWorkspace(context.Context, string, string, string) (WorkspaceResource, error)
	BindWorkspace(context.Context, BindWorkspaceCommand) (WorkspaceMutationResult, error)
	AdvanceWorkspace(context.Context, AdvanceWorkspaceCommand) (WorkspaceMutationResult, error)
	Complete(context.Context, CompleteProjectCommand) (ProjectMutationResult, error)
}
