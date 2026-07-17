package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
	projectdomain "github.com/langshift/lites/internal/product/project"
)

var (
	ErrInvalidProjectCommand = errors.New("project command is invalid")
	ErrProjectConflict       = errors.New("project command conflicts with durable state")
	ErrProjectNotFound       = errors.New("project does not exist in owner scope")
	ErrProjectNotMutable     = errors.New("project is not mutable")
	ErrProjectIncomplete     = errors.New("project completion requirements are not satisfied")
	ErrProjectBinding        = errors.New("project binding is invalid")
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ProjectStore is the transactional Create aggregate boundary. Every child
// mutation advances the parent Project version so If-Match fences cover the
// exact milestone, workspace and validation set used at completion/export.
type ProjectStore struct {
	Pool *pgxpool.Pool
	Tx   pgx.Tx
	// EpochVerified is set only by an application service after it validates
	// the authority immediately before entering an external idempotency tx.
	EpochVerified bool
	Appender      eventpostgres.Appender
	IDKey         []byte
	StoreEpoch    string
	Epochs        EpochAuthority
	Now           func() time.Time
}

type ProjectMutationResult struct {
	ProjectID              string    `json:"id"`
	Status                 string    `json:"status"`
	Version                uint64    `json:"version"`
	UpdatedAt              time.Time `json:"updated_at"`
	EventID                string    `json:"event_id"`
	CompletionManifestHash string    `json:"completion_manifest_hash,omitempty"`
	Replayed               bool      `json:"replayed"`
}

type CreateProjectCommand struct {
	MutationID, ProjectID, TenantID, UserID string
	MissionID, RouteRevisionID              string
	ProjectKind, Title, CorrelationID       string
	Brief                                   PayloadPointer
	BriefManifestHash                       string
	Actor                                   json.RawMessage
	CreatedEvent                            PayloadPointer
}

type ChangeProjectStatusCommand struct {
	MutationID, ProjectID, TenantID, UserID string
	ExpectedProjectVersion                  uint64
	NextStatus                              projectdomain.Status
	CorrelationID                           string
	Actor                                   json.RawMessage
	ChangedEvent                            PayloadPointer
}

type MilestoneMutationResult struct {
	ProjectMutationResult
	MilestoneID      string    `json:"milestone_id"`
	MilestoneStatus  string    `json:"milestone_status"`
	MilestoneVersion uint64    `json:"milestone_version"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type CreateMilestoneCommand struct {
	MutationID, MilestoneID, ProjectID     string
	TenantID, UserID, Title, CorrelationID string
	ExpectedProjectVersion                 uint64
	Sequence                               int
	Required                               bool
	AcceptanceSpec                         json.RawMessage
	Actor                                  json.RawMessage
	CreatedEvent                           PayloadPointer
}

type TransitionMilestoneCommand struct {
	MutationID, MilestoneID, ProjectID      string
	TenantID, UserID, Action, CorrelationID string
	ExpectedProjectVersion                  uint64
	ExpectedMilestoneVersion                uint64
	Result                                  PayloadPointer
	Actor                                   json.RawMessage
	ChangedEvent                            PayloadPointer
}

type WorkspaceMutationResult struct {
	ProjectMutationResult
	BindingID, WorkspaceID, HeadRevision, ManifestHash string
	BindingVersion                                     uint64
}

type BindProjectWorkspaceCommand struct {
	MutationID, BindingID, ProjectID, TenantID, UserID string
	WorkspaceID, BranchName, BaseRevision              string
	ExpectedProjectVersion                             uint64
	CorrelationID                                      string
	Actor                                              json.RawMessage
	BoundEvent                                         PayloadPointer
}

type AdvanceProjectWorkspaceCommand struct {
	MutationID, BindingID, ProjectID, TenantID, UserID string
	ExpectedProjectVersion, ExpectedBindingVersion     uint64
	ExpectedHeadRevision, HeadRevision, CorrelationID  string
	Actor                                              json.RawMessage
	AdvancedEvent                                      PayloadPointer
}

type ProjectTestRunResult struct {
	ProjectMutationResult
	TestRunID, Result, ResultManifestHash, WorkspaceRevision string
}

type RecordProjectTestRunCommand struct {
	MutationID, TestRunID, ProjectID, MilestoneID     string
	TenantID, UserID, WorkspaceRevision               string
	ValidationKind, Result, EvidenceID, CorrelationID string
	ExpectedProjectVersion                            uint64
	ResultManifest                                    json.RawMessage
	Actor                                             json.RawMessage
	RecordedEvent                                     PayloadPointer
}

type CompleteProjectCommand struct {
	MutationID, ProjectID, TenantID, UserID string
	ExpectedProjectVersion                  uint64
	WorkspaceRevision, CorrelationID        string
	Reflection                              PayloadPointer
	ReflectionManifestHash                  string
	Actor                                   json.RawMessage
	CompletedEvent                          PayloadPointer
	PrepareCompletedEvent                   func(context.Context, string) (PayloadPointer, error)
}

type projectEventIDs struct{ event, outbox, publish string }

func (store ProjectStore) Create(ctx context.Context, command CreateProjectCommand) (ProjectMutationResult, error) {
	if !store.validProjectStore() || !validCreateProject(command) {
		return ProjectMutationResult{}, ErrInvalidProjectCommand
	}
	if err := store.requireProjectEpoch(ctx); err != nil {
		return ProjectMutationResult{}, err
	}
	eventIDs, err := store.projectEventIDs("project-created", command.MutationID)
	if err != nil {
		return ProjectMutationResult{}, ErrConfiguration
	}
	now := store.projectNow()
	tx, owned, err := store.beginProjectTx(ctx)
	if err != nil {
		return ProjectMutationResult{}, err
	}
	defer store.rollbackProjectTx(ctx, tx, owned)
	if err = setProjectTenant(ctx, tx, command.TenantID); err != nil {
		return ProjectMutationResult{}, err
	}
	if replay, found, replayErr := loadProjectCreateReplay(ctx, tx, command, eventIDs.event); found || replayErr != nil {
		if replayErr == nil {
			replay.Replayed = true
			replayErr = store.commitProjectTx(ctx, tx, owned)
		}
		return replay, replayErr
	}
	var missionUser, missionStatus, currentRoute, routeStatus string
	err = tx.QueryRow(ctx, `SELECT m.user_id::text,m.status,m.current_route_revision_id::text,r.status FROM product.missions m JOIN product.route_revisions r ON r.tenant_id=m.tenant_id AND r.id=$4 AND r.mission_id=m.id AND r.user_id=m.user_id WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.id=$3 FOR SHARE OF m,r`, command.TenantID, command.UserID, command.MissionID, command.RouteRevisionID).Scan(&missionUser, &missionStatus, &currentRoute, &routeStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectMutationResult{}, ErrProjectBinding
	}
	if err != nil || missionUser != command.UserID || missionStatus != "active" || routeStatus != "accepted" || currentRoute != command.RouteRevisionID {
		return ProjectMutationResult{}, ErrProjectBinding
	}
	_, err = tx.Exec(ctx, `INSERT INTO product.projects(id,tenant_id,user_id,mission_id,accepted_route_revision_id,status,project_kind,title,brief_ref,brief_hash,brief_manifest_hash,created_event_id,last_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'active',$6,$7,$8,$9,$10,$11,$11,$12,$12)`, command.ProjectID, command.TenantID, command.UserID, command.MissionID, command.RouteRevisionID, command.ProjectKind, command.Title, command.Brief.Ref, command.Brief.Hash, command.BriefManifestHash, eventIDs.event, now)
	if err != nil {
		if constraintViolation(err) {
			return ProjectMutationResult{}, ErrProjectConflict
		}
		return ProjectMutationResult{}, err
	}
	if err = store.appendProjectEvent(ctx, tx, eventIDs, command.TenantID, command.UserID, command.ProjectID, 1, "ProjectCreated", command.CorrelationID, command.Actor, command.CreatedEvent, now); err != nil {
		return ProjectMutationResult{}, err
	}
	if err = store.commitProjectTx(ctx, tx, owned); err != nil {
		return ProjectMutationResult{}, err
	}
	return ProjectMutationResult{ProjectID: command.ProjectID, Status: "active", Version: 1, UpdatedAt: now, EventID: eventIDs.event}, nil
}

func (store ProjectStore) ChangeStatus(ctx context.Context, command ChangeProjectStatusCommand) (ProjectMutationResult, error) {
	if !store.validProjectStore() || !validChangeProjectStatus(command) {
		return ProjectMutationResult{}, ErrInvalidProjectCommand
	}
	if err := store.requireProjectEpoch(ctx); err != nil {
		return ProjectMutationResult{}, err
	}
	ids, err := store.projectEventIDs("project-status-changed", command.MutationID)
	if err != nil {
		return ProjectMutationResult{}, ErrConfiguration
	}
	return store.mutateProject(ctx, command.TenantID, command.UserID, command.ProjectID, command.ExpectedProjectVersion, ids, "ProjectStatusChanged", command.CorrelationID, command.Actor, command.ChangedEvent, func(ctx context.Context, tx pgx.Tx, status string, version uint64, now time.Time) (string, error) {
		next, transitionErr := (projectdomain.Project{ID: command.ProjectID, Version: version, Status: projectdomain.Status(status)}).Transition(version, command.NextStatus)
		if transitionErr != nil || next.Status == projectdomain.Completed {
			return "", ErrProjectNotMutable
		}
		tag, updateErr := tx.Exec(ctx, `UPDATE product.projects SET status=$1,version=$2,last_event_id=$3,updated_at=$4 WHERE tenant_id=$5 AND user_id=$6 AND id=$7 AND version=$8 AND status=$9`, next.Status, next.Version, ids.event, now, command.TenantID, command.UserID, command.ProjectID, version, status)
		if updateErr != nil || tag.RowsAffected() != 1 {
			return "", ErrProjectConflict
		}
		return string(next.Status), nil
	})
}

func (store ProjectStore) CreateMilestone(ctx context.Context, command CreateMilestoneCommand) (MilestoneMutationResult, error) {
	canonical, acceptanceHash, err := canonicalObject(command.AcceptanceSpec)
	if !store.validProjectStore() || !validCreateMilestone(command) || err != nil {
		return MilestoneMutationResult{}, ErrInvalidProjectCommand
	}
	if err = store.requireProjectEpoch(ctx); err != nil {
		return MilestoneMutationResult{}, err
	}
	eventIDs, err := store.projectEventIDs("project-milestone-created", command.MutationID)
	if err != nil {
		return MilestoneMutationResult{}, ErrConfiguration
	}
	now := store.projectNow()
	tx, owned, err := store.beginProjectTx(ctx)
	if err != nil {
		return MilestoneMutationResult{}, err
	}
	defer store.rollbackProjectTx(ctx, tx, owned)
	status, version, err := lockMutableProject(ctx, tx, command.TenantID, command.UserID, command.ProjectID, command.ExpectedProjectVersion)
	if err != nil {
		return MilestoneMutationResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO product.project_milestones(id,tenant_id,user_id,project_id,sequence,required,status,title,acceptance_spec,acceptance_spec_hash,created_event_id,last_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'planned',$7,$8,$9,$10,$10,$11,$11)`, command.MilestoneID, command.TenantID, command.UserID, command.ProjectID, command.Sequence, command.Required, command.Title, canonical, acceptanceHash, eventIDs.event, now); err != nil {
		if constraintViolation(err) {
			return MilestoneMutationResult{}, ErrProjectConflict
		}
		return MilestoneMutationResult{}, err
	}
	projectVersion := version + 1
	if err = bumpProject(ctx, tx, command.TenantID, command.UserID, command.ProjectID, version, status, eventIDs.event, now); err != nil {
		return MilestoneMutationResult{}, err
	}
	if err = store.appendProjectEvent(ctx, tx, eventIDs, command.TenantID, command.UserID, command.ProjectID, projectVersion, "MilestoneCreated", command.CorrelationID, command.Actor, command.CreatedEvent, now); err != nil {
		return MilestoneMutationResult{}, err
	}
	if err = store.commitProjectTx(ctx, tx, owned); err != nil {
		return MilestoneMutationResult{}, err
	}
	return MilestoneMutationResult{ProjectMutationResult: ProjectMutationResult{ProjectID: command.ProjectID, Status: status, Version: projectVersion, UpdatedAt: now, EventID: eventIDs.event}, MilestoneID: command.MilestoneID, MilestoneStatus: "planned", MilestoneVersion: 1, UpdatedAt: now}, nil
}

func (store ProjectStore) TransitionMilestone(ctx context.Context, command TransitionMilestoneCommand) (MilestoneMutationResult, error) {
	if !store.validProjectStore() || !validTransitionMilestone(command) {
		return MilestoneMutationResult{}, ErrInvalidProjectCommand
	}
	if err := store.requireProjectEpoch(ctx); err != nil {
		return MilestoneMutationResult{}, err
	}
	eventIDs, err := store.projectEventIDs("project-milestone-status-changed", command.MutationID)
	if err != nil {
		return MilestoneMutationResult{}, ErrConfiguration
	}
	now := store.projectNow()
	tx, owned, err := store.beginProjectTx(ctx)
	if err != nil {
		return MilestoneMutationResult{}, err
	}
	defer store.rollbackProjectTx(ctx, tx, owned)
	projectStatus, projectVersion, err := lockMutableProject(ctx, tx, command.TenantID, command.UserID, command.ProjectID, command.ExpectedProjectVersion)
	if err != nil {
		return MilestoneMutationResult{}, err
	}
	var milestoneStatus, resultRef, resultHash string
	var verificationID *string
	var milestoneVersion uint64
	err = tx.QueryRow(ctx, `SELECT status,version,COALESCE(result_ref,''),COALESCE(result_hash,''),verification_test_run_id::text FROM product.project_milestones WHERE tenant_id=$1 AND user_id=$2 AND project_id=$3 AND id=$4 FOR UPDATE`, command.TenantID, command.UserID, command.ProjectID, command.MilestoneID).Scan(&milestoneStatus, &milestoneVersion, &resultRef, &resultHash, &verificationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return MilestoneMutationResult{}, ErrProjectNotFound
	}
	if err != nil || milestoneVersion != command.ExpectedMilestoneVersion {
		return MilestoneMutationResult{}, ErrProjectConflict
	}
	verificationBound := verificationID != nil
	if command.Action == "verify" {
		var testRunID string
		err = tx.QueryRow(ctx, `SELECT t.id::text FROM product.project_test_runs t JOIN product.project_workspace_bindings w ON w.tenant_id=t.tenant_id AND w.project_id=t.project_id AND w.head_revision=t.workspace_revision WHERE t.tenant_id=$1 AND t.user_id=$2 AND t.project_id=$3 AND t.milestone_id=$4 AND t.result='passed' ORDER BY t.recorded_at DESC,t.id DESC LIMIT 1`, command.TenantID, command.UserID, command.ProjectID, command.MilestoneID).Scan(&testRunID)
		if errors.Is(err, pgx.ErrNoRows) {
			return MilestoneMutationResult{}, ErrProjectIncomplete
		}
		if err != nil {
			return MilestoneMutationResult{}, err
		}
		verificationID = &testRunID
		verificationBound = true
	}
	resultBound := resultRef != "" && resultHash != ""
	if command.Action == "submit" {
		resultRef, resultHash = command.Result.Ref, command.Result.Hash
		resultBound = true
	}
	next, transitionErr := (projectdomain.Milestone{ID: command.MilestoneID, ProjectID: command.ProjectID, Version: milestoneVersion, Status: projectdomain.MilestoneStatus(milestoneStatus)}).Transition(projectdomain.MilestoneCommand{ExpectedVersion: milestoneVersion, Action: command.Action, ResultBound: resultBound, VerificationBound: verificationBound})
	if transitionErr != nil {
		return MilestoneMutationResult{}, ErrProjectNotMutable
	}
	completedAt := any(nil)
	if next.Status == projectdomain.Done {
		completedAt = now
	}
	if next.Status == projectdomain.Rework {
		verificationID = nil
	}
	tag, err := tx.Exec(ctx, `UPDATE product.project_milestones SET status=$1,version=$2,result_ref=NULLIF($3,''),result_hash=NULLIF($4,''),verification_test_run_id=$5,completed_at=$6,last_event_id=$7,updated_at=$8 WHERE tenant_id=$9 AND user_id=$10 AND project_id=$11 AND id=$12 AND version=$13 AND status=$14`, next.Status, next.Version, resultRef, resultHash, verificationID, completedAt, eventIDs.event, now, command.TenantID, command.UserID, command.ProjectID, command.MilestoneID, milestoneVersion, milestoneStatus)
	if err != nil || tag.RowsAffected() != 1 {
		return MilestoneMutationResult{}, ErrProjectConflict
	}
	if err = bumpProject(ctx, tx, command.TenantID, command.UserID, command.ProjectID, projectVersion, projectStatus, eventIDs.event, now); err != nil {
		return MilestoneMutationResult{}, err
	}
	if err = store.appendProjectEvent(ctx, tx, eventIDs, command.TenantID, command.UserID, command.ProjectID, projectVersion+1, "MilestoneStatusChanged", command.CorrelationID, command.Actor, command.ChangedEvent, now); err != nil {
		return MilestoneMutationResult{}, err
	}
	if err = store.commitProjectTx(ctx, tx, owned); err != nil {
		return MilestoneMutationResult{}, err
	}
	return MilestoneMutationResult{ProjectMutationResult: ProjectMutationResult{ProjectID: command.ProjectID, Status: projectStatus, Version: projectVersion + 1, UpdatedAt: now, EventID: eventIDs.event}, MilestoneID: command.MilestoneID, MilestoneStatus: string(next.Status), MilestoneVersion: next.Version, UpdatedAt: now}, nil
}

func (store ProjectStore) BindWorkspace(ctx context.Context, command BindProjectWorkspaceCommand) (WorkspaceMutationResult, error) {
	manifest, manifestHash, err := canonicalObject(map[string]any{"schema_version": 1, "workspace_id": command.WorkspaceID, "branch_name": command.BranchName, "base_revision": command.BaseRevision, "head_revision": command.BaseRevision})
	if !store.validProjectStore() || !validBindWorkspace(command) || err != nil {
		return WorkspaceMutationResult{}, ErrInvalidProjectCommand
	}
	if err = store.requireProjectEpoch(ctx); err != nil {
		return WorkspaceMutationResult{}, err
	}
	eventIDs, err := store.projectEventIDs("project-workspace-bound", command.MutationID)
	if err != nil {
		return WorkspaceMutationResult{}, ErrConfiguration
	}
	now := store.projectNow()
	tx, owned, err := store.beginProjectTx(ctx)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}
	defer store.rollbackProjectTx(ctx, tx, owned)
	status, version, err := lockMutableProject(ctx, tx, command.TenantID, command.UserID, command.ProjectID, command.ExpectedProjectVersion)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO product.project_workspace_bindings(id,tenant_id,user_id,project_id,workspace_id,branch_name,base_revision,head_revision,binding_manifest_hash,binding_manifest,created_event_id,last_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$7,$8,$9,$10,$10,$11,$11)`, command.BindingID, command.TenantID, command.UserID, command.ProjectID, command.WorkspaceID, command.BranchName, command.BaseRevision, manifestHash, manifest, eventIDs.event, now)
	if err != nil {
		if constraintViolation(err) {
			return WorkspaceMutationResult{}, ErrProjectConflict
		}
		return WorkspaceMutationResult{}, err
	}
	if err = bumpProject(ctx, tx, command.TenantID, command.UserID, command.ProjectID, version, status, eventIDs.event, now); err != nil {
		return WorkspaceMutationResult{}, err
	}
	if err = store.appendProjectEvent(ctx, tx, eventIDs, command.TenantID, command.UserID, command.ProjectID, version+1, "ProjectWorkspaceBound", command.CorrelationID, command.Actor, command.BoundEvent, now); err != nil {
		return WorkspaceMutationResult{}, err
	}
	if err = store.commitProjectTx(ctx, tx, owned); err != nil {
		return WorkspaceMutationResult{}, err
	}
	return WorkspaceMutationResult{ProjectMutationResult: ProjectMutationResult{ProjectID: command.ProjectID, Status: status, Version: version + 1, UpdatedAt: now, EventID: eventIDs.event}, BindingID: command.BindingID, WorkspaceID: command.WorkspaceID, HeadRevision: command.BaseRevision, ManifestHash: manifestHash, BindingVersion: 1}, nil
}

func (store ProjectStore) AdvanceWorkspace(ctx context.Context, command AdvanceProjectWorkspaceCommand) (WorkspaceMutationResult, error) {
	if !store.validProjectStore() || !validAdvanceWorkspace(command) {
		return WorkspaceMutationResult{}, ErrInvalidProjectCommand
	}
	if err := store.requireProjectEpoch(ctx); err != nil {
		return WorkspaceMutationResult{}, err
	}
	eventIDs, err := store.projectEventIDs("project-workspace-head-advanced", command.MutationID)
	if err != nil {
		return WorkspaceMutationResult{}, ErrConfiguration
	}
	now := store.projectNow()
	tx, owned, err := store.beginProjectTx(ctx)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}
	defer store.rollbackProjectTx(ctx, tx, owned)
	status, projectVersion, err := lockMutableProject(ctx, tx, command.TenantID, command.UserID, command.ProjectID, command.ExpectedProjectVersion)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}
	var workspaceID, branchName, baseRevision, oldHead string
	var bindingVersion uint64
	err = tx.QueryRow(ctx, `SELECT workspace_id::text,branch_name,base_revision,head_revision,version FROM product.project_workspace_bindings WHERE tenant_id=$1 AND user_id=$2 AND project_id=$3 AND id=$4 FOR UPDATE`, command.TenantID, command.UserID, command.ProjectID, command.BindingID).Scan(&workspaceID, &branchName, &baseRevision, &oldHead, &bindingVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkspaceMutationResult{}, ErrProjectNotFound
	}
	if err != nil || bindingVersion != command.ExpectedBindingVersion || oldHead != command.ExpectedHeadRevision {
		return WorkspaceMutationResult{}, ErrProjectConflict
	}
	manifest, manifestHash, err := canonicalObject(map[string]any{"schema_version": 1, "workspace_id": workspaceID, "branch_name": branchName, "base_revision": baseRevision, "head_revision": command.HeadRevision, "previous_head_revision": oldHead})
	if err != nil {
		return WorkspaceMutationResult{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE product.project_workspace_bindings SET version=version+1,head_revision=$1,binding_manifest_hash=$2,binding_manifest=$3,last_event_id=$4,updated_at=$5 WHERE tenant_id=$6 AND user_id=$7 AND project_id=$8 AND id=$9 AND version=$10 AND head_revision=$11`, command.HeadRevision, manifestHash, manifest, eventIDs.event, now, command.TenantID, command.UserID, command.ProjectID, command.BindingID, bindingVersion, oldHead)
	if err != nil || tag.RowsAffected() != 1 {
		return WorkspaceMutationResult{}, ErrProjectConflict
	}
	if err = bumpProject(ctx, tx, command.TenantID, command.UserID, command.ProjectID, projectVersion, status, eventIDs.event, now); err != nil {
		return WorkspaceMutationResult{}, err
	}
	if err = store.appendProjectEvent(ctx, tx, eventIDs, command.TenantID, command.UserID, command.ProjectID, projectVersion+1, "ProjectWorkspaceHeadAdvanced", command.CorrelationID, command.Actor, command.AdvancedEvent, now); err != nil {
		return WorkspaceMutationResult{}, err
	}
	if err = store.commitProjectTx(ctx, tx, owned); err != nil {
		return WorkspaceMutationResult{}, err
	}
	return WorkspaceMutationResult{ProjectMutationResult: ProjectMutationResult{ProjectID: command.ProjectID, Status: status, Version: projectVersion + 1, UpdatedAt: now, EventID: eventIDs.event}, BindingID: command.BindingID, WorkspaceID: workspaceID, HeadRevision: command.HeadRevision, ManifestHash: manifestHash, BindingVersion: bindingVersion + 1}, nil
}

func (store ProjectStore) RecordTestRun(ctx context.Context, command RecordProjectTestRunCommand) (ProjectTestRunResult, error) {
	manifest, manifestHash, err := canonicalObject(command.ResultManifest)
	if !store.validProjectStore() || !validRecordTestRun(command) || err != nil {
		return ProjectTestRunResult{}, ErrInvalidProjectCommand
	}
	if err = store.requireProjectEpoch(ctx); err != nil {
		return ProjectTestRunResult{}, err
	}
	eventIDs, err := store.projectEventIDs("project-test-run-recorded", command.MutationID)
	if err != nil {
		return ProjectTestRunResult{}, ErrConfiguration
	}
	now := store.projectNow()
	tx, owned, err := store.beginProjectTx(ctx)
	if err != nil {
		return ProjectTestRunResult{}, err
	}
	defer store.rollbackProjectTx(ctx, tx, owned)
	status, version, err := lockMutableProject(ctx, tx, command.TenantID, command.UserID, command.ProjectID, command.ExpectedProjectVersion)
	if err != nil {
		return ProjectTestRunResult{}, err
	}
	var missionID, headRevision string
	err = tx.QueryRow(ctx, `SELECT p.mission_id::text,w.head_revision FROM product.projects p JOIN product.project_workspace_bindings w ON w.tenant_id=p.tenant_id AND w.project_id=p.id WHERE p.tenant_id=$1 AND p.user_id=$2 AND p.id=$3`, command.TenantID, command.UserID, command.ProjectID).Scan(&missionID, &headRevision)
	if err != nil || headRevision != command.WorkspaceRevision {
		return ProjectTestRunResult{}, fmt.Errorf("%w: workspace revision", ErrProjectBinding)
	}
	var evidenceUser, evidenceMission, evidenceStatus string
	err = tx.QueryRow(ctx, `SELECT user_id::text,mission_id::text,status FROM product.evidence WHERE tenant_id=$1 AND id=$2 FOR SHARE`, command.TenantID, command.EvidenceID).Scan(&evidenceUser, &evidenceMission, &evidenceStatus)
	if err != nil {
		return ProjectTestRunResult{}, fmt.Errorf("%w: evidence unavailable: %v", ErrProjectBinding, err)
	}
	if evidenceUser != command.UserID {
		return ProjectTestRunResult{}, fmt.Errorf("%w: evidence owner", ErrProjectBinding)
	}
	if evidenceMission != missionID {
		return ProjectTestRunResult{}, fmt.Errorf("%w: evidence mission", ErrProjectBinding)
	}
	if evidenceStatus != "recorded" && evidenceStatus != "verified" {
		return ProjectTestRunResult{}, fmt.Errorf("%w: evidence status", ErrProjectBinding)
	}
	var milestone any
	if command.MilestoneID != "" {
		var milestoneStatus string
		if err = tx.QueryRow(ctx, `SELECT status FROM product.project_milestones WHERE tenant_id=$1 AND user_id=$2 AND project_id=$3 AND id=$4 FOR SHARE`, command.TenantID, command.UserID, command.ProjectID, command.MilestoneID).Scan(&milestoneStatus); err != nil || milestoneStatus != "submitted" && milestoneStatus != "rework" && milestoneStatus != "verified" {
			return ProjectTestRunResult{}, fmt.Errorf("%w: milestone scope", ErrProjectBinding)
		}
		milestone = command.MilestoneID
	}
	_, err = tx.Exec(ctx, `INSERT INTO product.project_test_runs(id,tenant_id,user_id,project_id,milestone_id,workspace_revision,validation_kind,result,result_manifest,result_manifest_hash,evidence_id,recorded_event_id,recorded_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13,$13)`, command.TestRunID, command.TenantID, command.UserID, command.ProjectID, milestone, command.WorkspaceRevision, command.ValidationKind, command.Result, manifest, manifestHash, command.EvidenceID, eventIDs.event, now)
	if err != nil {
		if constraintViolation(err) {
			return ProjectTestRunResult{}, ErrProjectConflict
		}
		return ProjectTestRunResult{}, err
	}
	if err = bumpProject(ctx, tx, command.TenantID, command.UserID, command.ProjectID, version, status, eventIDs.event, now); err != nil {
		return ProjectTestRunResult{}, err
	}
	if err = store.appendProjectEvent(ctx, tx, eventIDs, command.TenantID, command.UserID, command.ProjectID, version+1, "ProjectTestRunRecorded", command.CorrelationID, command.Actor, command.RecordedEvent, now); err != nil {
		return ProjectTestRunResult{}, err
	}
	if err = store.commitProjectTx(ctx, tx, owned); err != nil {
		return ProjectTestRunResult{}, err
	}
	return ProjectTestRunResult{ProjectMutationResult: ProjectMutationResult{ProjectID: command.ProjectID, Status: status, Version: version + 1, UpdatedAt: now, EventID: eventIDs.event}, TestRunID: command.TestRunID, Result: command.Result, ResultManifestHash: manifestHash, WorkspaceRevision: command.WorkspaceRevision}, nil
}

func (store ProjectStore) Complete(ctx context.Context, command CompleteProjectCommand) (ProjectMutationResult, error) {
	if !store.validProjectStore() || !validCompleteProject(command) {
		return ProjectMutationResult{}, ErrInvalidProjectCommand
	}
	if err := store.requireProjectEpoch(ctx); err != nil {
		return ProjectMutationResult{}, err
	}
	eventIDs, err := store.projectEventIDs("project-completed", command.MutationID)
	if err != nil {
		return ProjectMutationResult{}, ErrConfiguration
	}
	now := store.projectNow()
	tx, owned, err := store.beginProjectTx(ctx)
	if err != nil {
		return ProjectMutationResult{}, err
	}
	defer store.rollbackProjectTx(ctx, tx, owned)
	if err = setProjectTenant(ctx, tx, command.TenantID); err != nil {
		return ProjectMutationResult{}, err
	}
	var manifest json.RawMessage
	err = tx.QueryRow(ctx, `SELECT product.lock_project_completion_manifest($1,$2,$3,$4,$5)`, command.TenantID, command.UserID, command.ProjectID, command.ExpectedProjectVersion, command.WorkspaceRevision).Scan(&manifest)
	if err != nil {
		return ProjectMutationResult{}, err
	}
	if len(manifest) == 0 || string(manifest) == "null" {
		return ProjectMutationResult{}, ErrProjectIncomplete
	}
	canonical, manifestHash, err := canonicalObject(manifest)
	if err != nil {
		return ProjectMutationResult{}, ErrProjectConflict
	}
	completedEvent := command.CompletedEvent
	if command.PrepareCompletedEvent != nil {
		completedEvent, err = command.PrepareCompletedEvent(ctx, manifestHash)
		if err != nil || !validPointer(completedEvent) {
			return ProjectMutationResult{}, ErrConfiguration
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE product.projects SET status='completed',version=version+1,reflection_ref=$1,reflection_hash=$2,reflection_manifest_hash=$3,completion_manifest=$4,completion_manifest_hash=$5,completion_event_id=$6,last_event_id=$6,completed_at=$7,updated_at=$7 WHERE tenant_id=$8 AND user_id=$9 AND id=$10 AND version=$11 AND status IN ('active','blocked')`, command.Reflection.Ref, command.Reflection.Hash, command.ReflectionManifestHash, canonical, manifestHash, eventIDs.event, now, command.TenantID, command.UserID, command.ProjectID, command.ExpectedProjectVersion)
	if err != nil || tag.RowsAffected() != 1 {
		return ProjectMutationResult{}, ErrProjectConflict
	}
	if err = store.appendProjectEvent(ctx, tx, eventIDs, command.TenantID, command.UserID, command.ProjectID, command.ExpectedProjectVersion+1, "ProjectCompleted", command.CorrelationID, command.Actor, completedEvent, now); err != nil {
		return ProjectMutationResult{}, err
	}
	if err = store.commitProjectTx(ctx, tx, owned); err != nil {
		return ProjectMutationResult{}, err
	}
	return ProjectMutationResult{ProjectID: command.ProjectID, Status: "completed", Version: command.ExpectedProjectVersion + 1, UpdatedAt: now, EventID: eventIDs.event, CompletionManifestHash: manifestHash}, nil
}

func (store ProjectStore) mutateProject(ctx context.Context, tenantID, userID, projectID string, expectedVersion uint64, eventIDs projectEventIDs, eventType, correlationID string, actor json.RawMessage, pointer PayloadPointer, mutation func(context.Context, pgx.Tx, string, uint64, time.Time) (string, error)) (ProjectMutationResult, error) {
	now := store.projectNow()
	tx, owned, err := store.beginProjectTx(ctx)
	if err != nil {
		return ProjectMutationResult{}, err
	}
	defer store.rollbackProjectTx(ctx, tx, owned)
	if err = setProjectTenant(ctx, tx, tenantID); err != nil {
		return ProjectMutationResult{}, err
	}
	var status string
	var version uint64
	err = tx.QueryRow(ctx, `SELECT status,version FROM product.projects WHERE tenant_id=$1 AND user_id=$2 AND id=$3 FOR UPDATE`, tenantID, userID, projectID).Scan(&status, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectMutationResult{}, ErrProjectNotFound
	}
	if err != nil || version != expectedVersion {
		return ProjectMutationResult{}, ErrProjectConflict
	}
	nextStatus, err := mutation(ctx, tx, status, version, now)
	if err != nil {
		return ProjectMutationResult{}, err
	}
	if err = store.appendProjectEvent(ctx, tx, eventIDs, tenantID, userID, projectID, version+1, eventType, correlationID, actor, pointer, now); err != nil {
		return ProjectMutationResult{}, err
	}
	if err = store.commitProjectTx(ctx, tx, owned); err != nil {
		return ProjectMutationResult{}, err
	}
	return ProjectMutationResult{ProjectID: projectID, Status: nextStatus, Version: version + 1, UpdatedAt: now, EventID: eventIDs.event}, nil
}

func (store ProjectStore) appendProjectEvent(ctx context.Context, tx pgx.Tx, identifiers projectEventIDs, tenantID, userID, projectID string, version uint64, eventType, correlationID string, actor json.RawMessage, pointer PayloadPointer, at time.Time) error {
	_, err := store.Appender.Append(ctx, tx, eventpostgres.Input{Event: eventpostgres.Event{ID: identifiers.event, TenantID: tenantID, UserID: userID, EventType: eventType, SchemaVersion: 1, AggregateKind: "project", AggregateID: projectID, AggregateVersion: version, StoreEpoch: store.StoreEpoch, OccurredAt: at, Actor: actor, CorrelationID: correlationID, PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: identifiers.outbox, CommandID: identifiers.publish, CommandType: "events.publish", PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}}})
	return err
}

func lockMutableProject(ctx context.Context, tx pgx.Tx, tenantID, userID, projectID string, expectedVersion uint64) (string, uint64, error) {
	if err := setProjectTenant(ctx, tx, tenantID); err != nil {
		return "", 0, err
	}
	var status string
	var version uint64
	err := tx.QueryRow(ctx, `SELECT status,version FROM product.projects WHERE tenant_id=$1 AND user_id=$2 AND id=$3 FOR UPDATE`, tenantID, userID, projectID).Scan(&status, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, ErrProjectNotFound
	}
	if err != nil {
		return "", 0, err
	}
	if version != expectedVersion {
		return "", 0, ErrProjectConflict
	}
	if status != "active" && status != "blocked" {
		return "", 0, ErrProjectNotMutable
	}
	return status, version, nil
}

func bumpProject(ctx context.Context, tx pgx.Tx, tenantID, userID, projectID string, version uint64, status, eventID string, now time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE product.projects SET version=version+1,last_event_id=$1,updated_at=$2 WHERE tenant_id=$3 AND user_id=$4 AND id=$5 AND version=$6 AND status=$7`, eventID, now, tenantID, userID, projectID, version, status)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrProjectConflict
	}
	return nil
}

func setProjectTenant(ctx context.Context, tx pgx.Tx, tenantID string) error {
	_, err := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID)
	return err
}

func (store ProjectStore) projectEventIDs(domain, seed string) (projectEventIDs, error) {
	values := make([]string, 3)
	for index, part := range []string{"event", "outbox", "publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain+":"+part, seed)
		if err != nil {
			return projectEventIDs{}, err
		}
		values[index] = value
	}
	return projectEventIDs{values[0], values[1], values[2]}, nil
}

func (store ProjectStore) beginProjectTx(ctx context.Context) (pgx.Tx, bool, error) {
	if store.Tx != nil {
		return store.Tx, false, nil
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	return tx, true, err
}

func (store ProjectStore) commitProjectTx(ctx context.Context, tx pgx.Tx, owned bool) error {
	if !owned {
		return nil
	}
	return tx.Commit(ctx)
}

func (store ProjectStore) rollbackProjectTx(ctx context.Context, tx pgx.Tx, owned bool) {
	if owned {
		_ = tx.Rollback(ctx)
	}
}

func (store ProjectStore) validProjectStore() bool {
	return store.Pool != nil && store.Epochs != nil && store.StoreEpoch != "" && len(store.IDKey) >= 32
}

func (store ProjectStore) requireProjectEpoch(ctx context.Context) error {
	if store.Tx != nil {
		if store.EpochVerified {
			return nil
		}
		return ErrConfiguration
	}
	current, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || current == "" {
		return ErrConfiguration
	}
	if current != store.StoreEpoch {
		return ErrStaleEpoch
	}
	return nil
}

func (store ProjectStore) projectNow() time.Time {
	now := time.Now().UTC()
	if store.Now != nil {
		now = store.Now().UTC()
	}
	return now.Truncate(time.Microsecond)
}

func canonicalObject(value any) (json.RawMessage, string, error) {
	encoded, err := json.Marshal(value)
	if raw, ok := value.(json.RawMessage); ok {
		var object map[string]any
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return nil, "", ErrInvalidProjectCommand
		}
		encoded, err = json.Marshal(object)
	}
	if err != nil || !validJSONObject(encoded) {
		return nil, "", ErrInvalidProjectCommand
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}

func validCreateProject(c CreateProjectCommand) bool {
	return c.MutationID != "" && c.ProjectID != "" && c.TenantID != "" && c.UserID != "" && c.MissionID != "" && c.RouteRevisionID != "" && (c.ProjectKind == "code" || c.ProjectKind == "writing" || c.ProjectKind == "design") && len(c.Title) >= 1 && len(c.Title) <= 200 && c.CorrelationID != "" && validJSONObject(c.Actor) && validHashPointer(c.Brief) && sha256Pattern.MatchString(c.BriefManifestHash) && validPointer(c.CreatedEvent)
}

func validChangeProjectStatus(c ChangeProjectStatusCommand) bool {
	return c.MutationID != "" && c.ProjectID != "" && c.TenantID != "" && c.UserID != "" && c.ExpectedProjectVersion > 0 && c.NextStatus != projectdomain.Completed && c.CorrelationID != "" && validJSONObject(c.Actor) && validPointer(c.ChangedEvent)
}

func validCreateMilestone(c CreateMilestoneCommand) bool {
	return c.MutationID != "" && c.MilestoneID != "" && c.ProjectID != "" && c.TenantID != "" && c.UserID != "" && c.ExpectedProjectVersion > 0 && c.Sequence > 0 && len(c.Title) >= 1 && len(c.Title) <= 200 && c.CorrelationID != "" && validJSONObject(c.Actor) && validPointer(c.CreatedEvent)
}

func validTransitionMilestone(c TransitionMilestoneCommand) bool {
	validAction := c.Action == "start" || c.Action == "submit" || c.Action == "verify" || c.Action == "request_rework" || c.Action == "complete"
	return c.MutationID != "" && c.MilestoneID != "" && c.ProjectID != "" && c.TenantID != "" && c.UserID != "" && c.ExpectedProjectVersion > 0 && c.ExpectedMilestoneVersion > 0 && validAction && (c.Action != "submit" || validHashPointer(c.Result)) && c.CorrelationID != "" && validJSONObject(c.Actor) && validPointer(c.ChangedEvent)
}

func validBindWorkspace(c BindProjectWorkspaceCommand) bool {
	return c.MutationID != "" && c.BindingID != "" && c.ProjectID != "" && c.TenantID != "" && c.UserID != "" && c.WorkspaceID != "" && c.BranchName != "" && len(c.BranchName) <= 240 && c.BaseRevision != "" && c.ExpectedProjectVersion > 0 && c.CorrelationID != "" && validJSONObject(c.Actor) && validPointer(c.BoundEvent)
}

func validAdvanceWorkspace(c AdvanceProjectWorkspaceCommand) bool {
	return c.MutationID != "" && c.BindingID != "" && c.ProjectID != "" && c.TenantID != "" && c.UserID != "" && c.ExpectedProjectVersion > 0 && c.ExpectedBindingVersion > 0 && c.ExpectedHeadRevision != "" && c.HeadRevision != "" && c.HeadRevision != c.ExpectedHeadRevision && c.CorrelationID != "" && validJSONObject(c.Actor) && validPointer(c.AdvancedEvent)
}

func validRecordTestRun(c RecordProjectTestRunCommand) bool {
	return c.MutationID != "" && c.TestRunID != "" && c.ProjectID != "" && c.TenantID != "" && c.UserID != "" && c.WorkspaceRevision != "" && (c.ValidationKind == "deterministic_test" || c.ValidationKind == "rubric_review") && (c.Result == "passed" || c.Result == "failed") && c.EvidenceID != "" && c.ExpectedProjectVersion > 0 && c.CorrelationID != "" && validJSONObject(c.Actor) && validPointer(c.RecordedEvent)
}

func validCompleteProject(c CompleteProjectCommand) bool {
	return c.MutationID != "" && c.ProjectID != "" && c.TenantID != "" && c.UserID != "" && c.ExpectedProjectVersion > 0 && c.WorkspaceRevision != "" && c.CorrelationID != "" && validHashPointer(c.Reflection) && sha256Pattern.MatchString(c.ReflectionManifestHash) && validJSONObject(c.Actor) && (validPointer(c.CompletedEvent) || c.PrepareCompletedEvent != nil)
}

func validHashPointer(pointer PayloadPointer) bool {
	return pointer.Ref != "" && sha256Pattern.MatchString(pointer.Hash)
}

func loadProjectCreateReplay(ctx context.Context, tx pgx.Tx, command CreateProjectCommand, eventID string) (ProjectMutationResult, bool, error) {
	var result ProjectMutationResult
	var userID, missionID, routeID, kind, title, briefRef, briefHash, briefManifestHash, createdEventID string
	err := tx.QueryRow(ctx, `SELECT user_id::text,mission_id::text,accepted_route_revision_id::text,project_kind,title,brief_ref,brief_hash,brief_manifest_hash,created_event_id::text,status,version,updated_at FROM product.projects WHERE tenant_id=$1 AND id=$2`, command.TenantID, command.ProjectID).Scan(&userID, &missionID, &routeID, &kind, &title, &briefRef, &briefHash, &briefManifestHash, &createdEventID, &result.Status, &result.Version, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectMutationResult{}, false, nil
	}
	if err != nil {
		return ProjectMutationResult{}, false, err
	}
	if userID != command.UserID || missionID != command.MissionID || routeID != command.RouteRevisionID || kind != command.ProjectKind || title != command.Title || briefRef != command.Brief.Ref || briefHash != command.Brief.Hash || briefManifestHash != command.BriefManifestHash || createdEventID != eventID {
		return ProjectMutationResult{}, true, ErrProjectConflict
	}
	result.ProjectID, result.EventID = command.ProjectID, eventID
	return result, true, nil
}
