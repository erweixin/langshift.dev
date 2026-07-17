package postgres

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
	projectdomain "github.com/langshift/lites/internal/product/project"
)

const (
	projectResponseClass   = "project-idempotency"
	projectEventClass      = "event-payload"
	projectBriefClass      = "project-brief"
	projectResultClass     = "project-milestone-result"
	projectReflectionClass = "project-reflection"
	projectPageSize        = 50
)

type ProjectApplicationService struct {
	Pool                 *pgxpool.Pool
	Store                ProjectStore
	Payloads             payload.Store
	IDKey                []byte
	IdempotencyKeyPepper []byte
	RequestDigestPepper  []byte
	CursorKey            []byte
	IdempotencyTTL       time.Duration
	Now                  func() time.Time
}

type projectCursor struct {
	TenantID string    `json:"tenant_id"`
	UserID   string    `json:"user_id"`
	Updated  time.Time `json:"updated_at"`
	ID       string    `json:"id"`
}

type observedProject struct {
	Status, MissionID, WorkspaceRevision, CompletionManifestHash string
	Version                                                      uint64
}

type observedMilestone struct {
	Status, VerificationID string
	Version                uint64
}

func (service ProjectApplicationService) List(ctx context.Context, query productapi.ProjectListQuery) (productapi.ProjectListResult, error) {
	if !service.valid() || query.TenantID == "" || query.UserID == "" {
		return productapi.ProjectListResult{}, productapi.ErrValidation
	}
	var cursor *projectCursor
	if query.Cursor != "" {
		decoded, err := service.decodeProjectCursor(query.Cursor)
		if err != nil || decoded.TenantID != query.TenantID || decoded.UserID != query.UserID {
			return productapi.ProjectListResult{}, productapi.ErrValidation
		}
		cursor = &decoded
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return productapi.ProjectListResult{}, service.mapProjectError(err)
	}
	defer tx.Rollback(ctx)
	if err = setProjectTenant(ctx, tx, query.TenantID); err != nil {
		return productapi.ProjectListResult{}, service.mapProjectError(err)
	}
	args := []any{query.TenantID, query.UserID, projectPageSize + 1}
	statement := `SELECT id::text,mission_id::text,accepted_route_revision_id::text,version,status,project_kind,title,created_at,updated_at,completed_at FROM product.projects WHERE tenant_id=$1 AND user_id=$2`
	if cursor != nil {
		statement += ` AND (updated_at < $4 OR (updated_at=$4 AND id < $5::uuid))`
		args = append(args, cursor.Updated, cursor.ID)
	}
	statement += ` ORDER BY updated_at DESC,id DESC LIMIT $3`
	rows, err := tx.Query(ctx, statement, args...)
	if err != nil {
		return productapi.ProjectListResult{}, service.mapProjectError(err)
	}
	defer rows.Close()
	items := make([]productapi.ProjectResource, 0, projectPageSize+1)
	for rows.Next() {
		var item productapi.ProjectResource
		if err = rows.Scan(&item.ID, &item.MissionID, &item.RouteRevisionID, &item.Version, &item.Status, &item.ProjectKind, &item.Title, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt); err != nil {
			return productapi.ProjectListResult{}, service.mapProjectError(err)
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return productapi.ProjectListResult{}, service.mapProjectError(err)
	}
	var next *string
	if len(items) > projectPageSize {
		items = items[:projectPageSize]
		last := items[len(items)-1]
		value, encodeErr := service.encodeProjectCursor(projectCursor{TenantID: query.TenantID, UserID: query.UserID, Updated: last.UpdatedAt, ID: last.ID})
		if encodeErr != nil {
			return productapi.ProjectListResult{}, service.mapProjectError(encodeErr)
		}
		next = &value
	}
	if err = tx.Commit(ctx); err != nil {
		return productapi.ProjectListResult{}, service.mapProjectError(err)
	}
	return productapi.ProjectListResult{Items: items, NextCursor: next}, nil
}

func (service ProjectApplicationService) Create(ctx context.Context, command productapi.CreateProjectCommand) (productapi.ProjectMutationResult, error) {
	if !service.valid() || !validProjectMetadata(command.CommandMetadata) || command.MissionID == "" || command.RouteRevisionID == "" || !validProjectKind(command.ProjectKind) || !validProjectText(command.Title, 200) || !validProjectText(command.Brief, 10000) {
		return productapi.ProjectMutationResult{}, productapi.ErrValidation
	}
	canonical, err := canonicalProjectRequest(command)
	if err != nil {
		return productapi.ProjectMutationResult{}, service.mapProjectError(err)
	}
	input, descriptor, err := service.projectIdempotencyInput(command.CommandMetadata, "projects.create.v2", canonical)
	if err != nil {
		return productapi.ProjectMutationResult{}, service.mapProjectError(err)
	}
	if encoded, found, loadErr := service.loadProjectCompleted(ctx, input, descriptor); found || loadErr != nil {
		var result productapi.ProjectMutationResult
		if loadErr == nil {
			loadErr = decodeProjectResponse(encoded, &result)
			result.Replayed = loadErr == nil
		}
		return result, service.mapProjectError(loadErr)
	}
	projectID, _ := ids.DeterministicUUID(service.IDKey, "projects.create.v2:project", input.RecordID)
	mutationID, _ := ids.DeterministicUUID(service.IDKey, "projects.create.v2:mutation", input.RecordID)
	brief, err := service.putProjectJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: projectID, Class: projectBriefClass, ContentType: "application/json"}, map[string]any{"schema_version": 1, "brief": command.Brief})
	if err != nil {
		return productapi.ProjectMutationResult{}, service.mapProjectError(err)
	}
	_, briefManifestHash, _ := canonicalObject(map[string]any{"schema_version": 1, "payload_ref": brief.Ref, "payload_hash": brief.Hash})
	eventIDs, _ := service.Store.projectEventIDs("project-created", mutationID)
	eventPayload, err := service.putProjectJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventIDs.event, Class: projectEventClass, ContentType: "application/json"}, map[string]any{"subject_id": projectID, "subject_version": 1})
	if err != nil {
		return productapi.ProjectMutationResult{}, service.mapProjectError(err)
	}
	encoded, replayed, err := service.executeProjectMutation(ctx, input, descriptor, func(store ProjectStore) (any, error) {
		stored, mutationErr := store.Create(ctx, CreateProjectCommand{MutationID: mutationID, ProjectID: projectID, TenantID: command.TenantID, UserID: command.UserID, MissionID: command.MissionID, RouteRevisionID: command.RouteRevisionID, ProjectKind: command.ProjectKind, Title: command.Title, CorrelationID: command.RequestID, Brief: PayloadPointer{Ref: brief.Ref, Hash: brief.Hash}, BriefManifestHash: briefManifestHash, Actor: missionActor(command.UserID, command.SessionID), CreatedEvent: PayloadPointer{Ref: eventPayload.Ref, Hash: eventPayload.Hash}})
		return apiProjectMutation(stored), mutationErr
	})
	if err != nil {
		return productapi.ProjectMutationResult{}, service.mapProjectError(err)
	}
	var result productapi.ProjectMutationResult
	if err = decodeProjectResponse(encoded, &result); err != nil {
		return result, service.mapProjectError(err)
	}
	result.Replayed = replayed
	return result, nil
}

func (service ProjectApplicationService) ChangeStatus(ctx context.Context, command productapi.ChangeProjectStatusCommand) (productapi.ProjectMutationResult, error) {
	if !service.valid() || !validProjectMetadata(command.CommandMetadata) || command.ProjectID == "" || command.ExpectedProjectVersion < 1 || command.Status != "active" && command.Status != "blocked" && command.Status != "archived" || !validOptionalProjectText(command.Reason, 2000) {
		return productapi.ProjectMutationResult{}, productapi.ErrValidation
	}
	return service.changeProjectStatus(ctx, command)
}

func (service ProjectApplicationService) changeProjectStatus(ctx context.Context, command productapi.ChangeProjectStatusCommand) (productapi.ProjectMutationResult, error) {
	canonical, err := canonicalProjectRequest(command)
	if err != nil {
		return productapi.ProjectMutationResult{}, service.mapProjectError(err)
	}
	input, descriptor, err := service.projectIdempotencyInput(command.CommandMetadata, "projects.status.v2", canonical)
	if err != nil {
		return productapi.ProjectMutationResult{}, service.mapProjectError(err)
	}
	if encoded, found, loadErr := service.loadProjectCompleted(ctx, input, descriptor); found || loadErr != nil {
		var result productapi.ProjectMutationResult
		if loadErr == nil {
			loadErr = decodeProjectResponse(encoded, &result)
			result.Replayed = loadErr == nil
		}
		return result, service.mapProjectError(loadErr)
	}
	observed, err := service.observeProject(ctx, command.TenantID, command.UserID, command.ProjectID)
	if err != nil || observed.Version != command.ExpectedProjectVersion {
		if err == nil {
			err = ErrProjectConflict
		}
		return productapi.ProjectMutationResult{}, service.mapProjectError(err)
	}
	mutationID, _ := ids.DeterministicUUID(service.IDKey, "projects.status.v2:mutation", input.RecordID)
	eventIDs, _ := service.Store.projectEventIDs("project-status-changed", mutationID)
	var workspace, completion any
	if observed.WorkspaceRevision != "" {
		workspace = observed.WorkspaceRevision
	}
	if observed.CompletionManifestHash != "" {
		completion = observed.CompletionManifestHash
	}
	eventPayload, err := service.putProjectJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventIDs.event, Class: projectEventClass, ContentType: "application/json"}, map[string]any{"subject_id": command.ProjectID, "subject_version": observed.Version + 1, "previous_state": observed.Status, "new_state": command.Status, "reason_code": "user_status_change", "project_id": command.ProjectID, "previous_status": observed.Status, "status": command.Status, "project_version": observed.Version + 1, "workspace_revision": workspace, "completion_manifest_hash": completion})
	if err != nil {
		return productapi.ProjectMutationResult{}, service.mapProjectError(err)
	}
	encoded, replayed, err := service.executeProjectMutation(ctx, input, descriptor, func(store ProjectStore) (any, error) {
		stored, mutationErr := store.ChangeStatus(ctx, ChangeProjectStatusCommand{MutationID: mutationID, ProjectID: command.ProjectID, TenantID: command.TenantID, UserID: command.UserID, ExpectedProjectVersion: command.ExpectedProjectVersion, NextStatus: projectdomain.Status(command.Status), CorrelationID: command.RequestID, Actor: missionActor(command.UserID, command.SessionID), ChangedEvent: PayloadPointer{Ref: eventPayload.Ref, Hash: eventPayload.Hash}})
		return apiProjectMutation(stored), mutationErr
	})
	if err != nil {
		return productapi.ProjectMutationResult{}, service.mapProjectError(err)
	}
	var result productapi.ProjectMutationResult
	err = decodeProjectResponse(encoded, &result)
	result.Replayed = replayed
	return result, service.mapProjectError(err)
}

func (service ProjectApplicationService) CreateMilestone(ctx context.Context, command productapi.CreateMilestoneCommand) (productapi.MilestoneMutationResult, error) {
	if !service.valid() || !validProjectMetadata(command.CommandMetadata) || command.ProjectID == "" || command.ExpectedProjectVersion < 1 || command.Sequence < 1 || !validProjectText(command.Title, 200) || !validJSONObject(command.AcceptanceSpec) {
		return productapi.MilestoneMutationResult{}, productapi.ErrValidation
	}
	canonical, err := canonicalProjectRequest(command)
	if err != nil {
		return productapi.MilestoneMutationResult{}, service.mapProjectError(err)
	}
	input, descriptor, err := service.projectIdempotencyInput(command.CommandMetadata, "milestones.create.v2", canonical)
	if err != nil {
		return productapi.MilestoneMutationResult{}, service.mapProjectError(err)
	}
	if encoded, found, loadErr := service.loadProjectCompleted(ctx, input, descriptor); found || loadErr != nil {
		var result productapi.MilestoneMutationResult
		if loadErr == nil {
			loadErr = decodeProjectResponse(encoded, &result)
			result.Replayed = loadErr == nil
		}
		return result, service.mapProjectError(loadErr)
	}
	milestoneID, _ := ids.DeterministicUUID(service.IDKey, "milestones.create.v2:milestone", input.RecordID)
	mutationID, _ := ids.DeterministicUUID(service.IDKey, "milestones.create.v2:mutation", input.RecordID)
	eventIDs, _ := service.Store.projectEventIDs("project-milestone-created", mutationID)
	eventPayload, err := service.putProjectJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventIDs.event, Class: projectEventClass, ContentType: "application/json"}, map[string]any{"subject_id": milestoneID, "subject_version": 1})
	if err != nil {
		return productapi.MilestoneMutationResult{}, service.mapProjectError(err)
	}
	encoded, replayed, err := service.executeProjectMutation(ctx, input, descriptor, func(store ProjectStore) (any, error) {
		stored, mutationErr := store.CreateMilestone(ctx, CreateMilestoneCommand{MutationID: mutationID, MilestoneID: milestoneID, ProjectID: command.ProjectID, TenantID: command.TenantID, UserID: command.UserID, ExpectedProjectVersion: command.ExpectedProjectVersion, Sequence: command.Sequence, Required: command.Required, Title: command.Title, AcceptanceSpec: command.AcceptanceSpec, CorrelationID: command.RequestID, Actor: missionActor(command.UserID, command.SessionID), CreatedEvent: PayloadPointer{Ref: eventPayload.Ref, Hash: eventPayload.Hash}})
		return apiMilestoneMutation(stored), mutationErr
	})
	if err != nil {
		return productapi.MilestoneMutationResult{}, service.mapProjectError(err)
	}
	var result productapi.MilestoneMutationResult
	err = decodeProjectResponse(encoded, &result)
	result.Replayed = replayed
	return result, service.mapProjectError(err)
}

func (service ProjectApplicationService) TransitionMilestone(ctx context.Context, command productapi.TransitionMilestoneCommand) (productapi.MilestoneMutationResult, error) {
	validAction := command.Action == "start" || command.Action == "submit" || command.Action == "verify" || command.Action == "request_rework" || command.Action == "complete"
	if !service.valid() || !validProjectMetadata(command.CommandMetadata) || command.ProjectID == "" || command.MilestoneID == "" || command.ExpectedProjectVersion < 1 || command.ExpectedMilestoneVersion < 1 || !validAction || command.Action == "submit" && !validProjectText(command.Result, 20000) {
		return productapi.MilestoneMutationResult{}, productapi.ErrValidation
	}
	canonical, err := canonicalProjectRequest(command)
	if err != nil {
		return productapi.MilestoneMutationResult{}, service.mapProjectError(err)
	}
	input, descriptor, err := service.projectIdempotencyInput(command.CommandMetadata, "milestones.transition.v2", canonical)
	if err != nil {
		return productapi.MilestoneMutationResult{}, service.mapProjectError(err)
	}
	if encoded, found, loadErr := service.loadProjectCompleted(ctx, input, descriptor); found || loadErr != nil {
		var result productapi.MilestoneMutationResult
		if loadErr == nil {
			loadErr = decodeProjectResponse(encoded, &result)
			result.Replayed = loadErr == nil
		}
		return result, service.mapProjectError(loadErr)
	}
	observed, err := service.observeMilestone(ctx, command.TenantID, command.UserID, command.ProjectID, command.MilestoneID)
	if err != nil || observed.Version != command.ExpectedMilestoneVersion {
		if err == nil {
			err = ErrProjectConflict
		}
		return productapi.MilestoneMutationResult{}, service.mapProjectError(err)
	}
	mutationID, _ := ids.DeterministicUUID(service.IDKey, "milestones.transition.v2:mutation", input.RecordID)
	resultPointer := PayloadPointer{}
	if command.Action == "submit" {
		manifest, putErr := service.putProjectJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.MilestoneID, Class: projectResultClass, ContentType: "application/json"}, map[string]any{"schema_version": 1, "result": command.Result})
		if putErr != nil {
			return productapi.MilestoneMutationResult{}, service.mapProjectError(putErr)
		}
		resultPointer = PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}
	}
	nextStatus := map[string]string{"start": "in_progress", "submit": "submitted", "verify": "verified", "request_rework": "rework", "complete": "completed"}[command.Action]
	validationID := any(nil)
	if command.Action == "verify" {
		validationID, err = service.latestPassingTest(ctx, command.TenantID, command.UserID, command.ProjectID, command.MilestoneID)
		if err != nil {
			return productapi.MilestoneMutationResult{}, service.mapProjectError(err)
		}
	} else if observed.VerificationID != "" {
		validationID = observed.VerificationID
	}
	eventIDs, _ := service.Store.projectEventIDs("project-milestone-status-changed", mutationID)
	eventPayload, err := service.putProjectJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventIDs.event, Class: projectEventClass, ContentType: "application/json"}, map[string]any{"subject_id": command.MilestoneID, "subject_version": observed.Version + 1, "previous_state": observed.Status, "new_state": nextStatus, "reason_code": "user_" + command.Action, "project_id": command.ProjectID, "milestone_id": command.MilestoneID, "previous_status": observed.Status, "status": nextStatus, "milestone_version": observed.Version + 1, "validation_result_id": validationID})
	if err != nil {
		return productapi.MilestoneMutationResult{}, service.mapProjectError(err)
	}
	encoded, replayed, err := service.executeProjectMutation(ctx, input, descriptor, func(store ProjectStore) (any, error) {
		stored, mutationErr := store.TransitionMilestone(ctx, TransitionMilestoneCommand{MutationID: mutationID, MilestoneID: command.MilestoneID, ProjectID: command.ProjectID, TenantID: command.TenantID, UserID: command.UserID, Action: command.Action, ExpectedProjectVersion: command.ExpectedProjectVersion, ExpectedMilestoneVersion: command.ExpectedMilestoneVersion, Result: resultPointer, CorrelationID: command.RequestID, Actor: missionActor(command.UserID, command.SessionID), ChangedEvent: PayloadPointer{Ref: eventPayload.Ref, Hash: eventPayload.Hash}})
		return apiMilestoneMutation(stored), mutationErr
	})
	if err != nil {
		return productapi.MilestoneMutationResult{}, service.mapProjectError(err)
	}
	var result productapi.MilestoneMutationResult
	err = decodeProjectResponse(encoded, &result)
	result.Replayed = replayed
	return result, service.mapProjectError(err)
}

func (service ProjectApplicationService) GetWorkspace(ctx context.Context, tenantID, userID, projectID string) (productapi.WorkspaceResource, error) {
	if !service.valid() || tenantID == "" || userID == "" || projectID == "" {
		return productapi.WorkspaceResource{}, productapi.ErrValidation
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return productapi.WorkspaceResource{}, service.mapProjectError(err)
	}
	defer tx.Rollback(ctx)
	if err = setProjectTenant(ctx, tx, tenantID); err != nil {
		return productapi.WorkspaceResource{}, service.mapProjectError(err)
	}
	var resource productapi.WorkspaceResource
	err = tx.QueryRow(ctx, `SELECT id::text,project_id::text,workspace_id::text,branch_name,base_revision,head_revision,binding_manifest_hash,version,updated_at FROM product.project_workspace_bindings WHERE tenant_id=$1 AND user_id=$2 AND project_id=$3`, tenantID, userID, projectID).Scan(&resource.ID, &resource.ProjectID, &resource.WorkspaceID, &resource.BranchName, &resource.BaseRevision, &resource.HeadRevision, &resource.BindingManifestHash, &resource.Version, &resource.UpdatedAt)
	if err != nil {
		return productapi.WorkspaceResource{}, service.mapProjectError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return productapi.WorkspaceResource{}, service.mapProjectError(err)
	}
	return resource, nil
}

func (service ProjectApplicationService) BindWorkspace(ctx context.Context, command productapi.BindWorkspaceCommand) (productapi.WorkspaceMutationResult, error) {
	if !service.valid() || !validProjectMetadata(command.CommandMetadata) || command.ProjectID == "" || command.WorkspaceID == "" || command.BranchName == "" || len(command.BranchName) > 240 || strings.Contains(command.BranchName, "..") || command.BaseRevision == "" || command.ExpectedProjectVersion < 1 {
		return productapi.WorkspaceMutationResult{}, productapi.ErrValidation
	}
	canonical, err := canonicalProjectRequest(command)
	if err != nil {
		return productapi.WorkspaceMutationResult{}, service.mapProjectError(err)
	}
	input, descriptor, err := service.projectIdempotencyInput(command.CommandMetadata, "projects.workspace.bind.v2", canonical)
	if err != nil {
		return productapi.WorkspaceMutationResult{}, service.mapProjectError(err)
	}
	if encoded, found, loadErr := service.loadProjectCompleted(ctx, input, descriptor); found || loadErr != nil {
		var result productapi.WorkspaceMutationResult
		if loadErr == nil {
			loadErr = decodeProjectResponse(encoded, &result)
			result.Replayed = loadErr == nil
		}
		return result, service.mapProjectError(loadErr)
	}
	bindingID, _ := ids.DeterministicUUID(service.IDKey, "projects.workspace.bind.v2:binding", input.RecordID)
	mutationID, _ := ids.DeterministicUUID(service.IDKey, "projects.workspace.bind.v2:mutation", input.RecordID)
	eventIDs, _ := service.Store.projectEventIDs("project-workspace-bound", mutationID)
	eventPayload, err := service.putProjectJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventIDs.event, Class: projectEventClass, ContentType: "application/json"}, map[string]any{"subject_id": command.ProjectID, "subject_version": command.ExpectedProjectVersion + 1, "command_id": mutationID})
	if err != nil {
		return productapi.WorkspaceMutationResult{}, service.mapProjectError(err)
	}
	encoded, replayed, err := service.executeProjectMutation(ctx, input, descriptor, func(store ProjectStore) (any, error) {
		stored, mutationErr := store.BindWorkspace(ctx, BindProjectWorkspaceCommand{MutationID: mutationID, BindingID: bindingID, ProjectID: command.ProjectID, TenantID: command.TenantID, UserID: command.UserID, WorkspaceID: command.WorkspaceID, BranchName: command.BranchName, BaseRevision: command.BaseRevision, ExpectedProjectVersion: command.ExpectedProjectVersion, CorrelationID: command.RequestID, Actor: missionActor(command.UserID, command.SessionID), BoundEvent: PayloadPointer{Ref: eventPayload.Ref, Hash: eventPayload.Hash}})
		return apiWorkspaceMutation(stored, command.ProjectID, command.BranchName, command.BaseRevision), mutationErr
	})
	if err != nil {
		return productapi.WorkspaceMutationResult{}, service.mapProjectError(err)
	}
	var result productapi.WorkspaceMutationResult
	err = decodeProjectResponse(encoded, &result)
	result.Replayed = replayed
	return result, service.mapProjectError(err)
}

func (service ProjectApplicationService) AdvanceWorkspace(ctx context.Context, command productapi.AdvanceWorkspaceCommand) (productapi.WorkspaceMutationResult, error) {
	if !service.valid() || !validProjectMetadata(command.CommandMetadata) || command.ProjectID == "" || command.BindingID == "" || command.ExpectedProjectVersion < 1 || command.ExpectedBindingVersion < 1 || command.ExpectedHeadRevision == "" || command.HeadRevision == "" || command.ExpectedHeadRevision == command.HeadRevision {
		return productapi.WorkspaceMutationResult{}, productapi.ErrValidation
	}
	canonical, err := canonicalProjectRequest(command)
	if err != nil {
		return productapi.WorkspaceMutationResult{}, service.mapProjectError(err)
	}
	input, descriptor, err := service.projectIdempotencyInput(command.CommandMetadata, "projects.workspace.advance.v2", canonical)
	if err != nil {
		return productapi.WorkspaceMutationResult{}, service.mapProjectError(err)
	}
	if encoded, found, loadErr := service.loadProjectCompleted(ctx, input, descriptor); found || loadErr != nil {
		var result productapi.WorkspaceMutationResult
		if loadErr == nil {
			loadErr = decodeProjectResponse(encoded, &result)
			result.Replayed = loadErr == nil
		}
		return result, service.mapProjectError(loadErr)
	}
	workspace, err := service.GetWorkspace(ctx, command.TenantID, command.UserID, command.ProjectID)
	if err != nil || workspace.ID != command.BindingID || workspace.Version != command.ExpectedBindingVersion || workspace.HeadRevision != command.ExpectedHeadRevision {
		if err == nil {
			err = ErrProjectConflict
		}
		return productapi.WorkspaceMutationResult{}, service.mapProjectError(err)
	}
	mutationID, _ := ids.DeterministicUUID(service.IDKey, "projects.workspace.advance.v2:mutation", input.RecordID)
	_, manifestHash, _ := canonicalObject(map[string]any{"schema_version": 1, "workspace_id": workspace.WorkspaceID, "branch_name": workspace.BranchName, "base_revision": workspace.BaseRevision, "head_revision": command.HeadRevision, "previous_head_revision": command.ExpectedHeadRevision})
	eventIDs, _ := service.Store.projectEventIDs("project-workspace-head-advanced", mutationID)
	eventPayload, err := service.putProjectJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventIDs.event, Class: projectEventClass, ContentType: "application/json"}, map[string]any{"subject_id": command.ProjectID, "subject_version": command.ExpectedProjectVersion + 1, "project_id": command.ProjectID, "binding_id": command.BindingID, "binding_version": command.ExpectedBindingVersion + 1, "previous_head_revision": command.ExpectedHeadRevision, "head_revision": command.HeadRevision, "binding_manifest_hash": manifestHash})
	if err != nil {
		return productapi.WorkspaceMutationResult{}, service.mapProjectError(err)
	}
	encoded, replayed, err := service.executeProjectMutation(ctx, input, descriptor, func(store ProjectStore) (any, error) {
		stored, mutationErr := store.AdvanceWorkspace(ctx, AdvanceProjectWorkspaceCommand{MutationID: mutationID, BindingID: command.BindingID, ProjectID: command.ProjectID, TenantID: command.TenantID, UserID: command.UserID, ExpectedProjectVersion: command.ExpectedProjectVersion, ExpectedBindingVersion: command.ExpectedBindingVersion, ExpectedHeadRevision: command.ExpectedHeadRevision, HeadRevision: command.HeadRevision, CorrelationID: command.RequestID, Actor: missionActor(command.UserID, command.SessionID), AdvancedEvent: PayloadPointer{Ref: eventPayload.Ref, Hash: eventPayload.Hash}})
		return apiWorkspaceMutation(stored, command.ProjectID, workspace.BranchName, workspace.BaseRevision), mutationErr
	})
	if err != nil {
		return productapi.WorkspaceMutationResult{}, service.mapProjectError(err)
	}
	var result productapi.WorkspaceMutationResult
	err = decodeProjectResponse(encoded, &result)
	result.Replayed = replayed
	return result, service.mapProjectError(err)
}

func (service ProjectApplicationService) Complete(ctx context.Context, command productapi.CompleteProjectCommand) (productapi.ProjectMutationResult, error) {
	if !service.valid() || !validProjectMetadata(command.CommandMetadata) || command.ProjectID == "" || command.ExpectedProjectVersion < 1 || command.WorkspaceRevision == "" || !validProjectText(command.Reflection, 20000) {
		return productapi.ProjectMutationResult{}, productapi.ErrValidation
	}
	canonical, err := canonicalProjectRequest(command)
	if err != nil {
		return productapi.ProjectMutationResult{}, service.mapProjectError(err)
	}
	input, descriptor, err := service.projectIdempotencyInput(command.CommandMetadata, "projects.complete.v2", canonical)
	if err != nil {
		return productapi.ProjectMutationResult{}, service.mapProjectError(err)
	}
	if encoded, found, loadErr := service.loadProjectCompleted(ctx, input, descriptor); found || loadErr != nil {
		var result productapi.ProjectMutationResult
		if loadErr == nil {
			loadErr = decodeProjectResponse(encoded, &result)
			result.Replayed = loadErr == nil
		}
		return result, service.mapProjectError(loadErr)
	}
	mutationID, _ := ids.DeterministicUUID(service.IDKey, "projects.complete.v2:mutation", input.RecordID)
	reflection, err := service.putProjectJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: mutationID, Class: projectReflectionClass, ContentType: "application/json"}, map[string]any{"schema_version": 1, "reflection": command.Reflection})
	if err != nil {
		return productapi.ProjectMutationResult{}, service.mapProjectError(err)
	}
	_, reflectionManifestHash, _ := canonicalObject(map[string]any{"schema_version": 1, "payload_ref": reflection.Ref, "payload_hash": reflection.Hash})
	eventIDs, _ := service.Store.projectEventIDs("project-completed", mutationID)
	encoded, replayed, err := service.executeProjectMutation(ctx, input, descriptor, func(store ProjectStore) (any, error) {
		stored, mutationErr := store.Complete(ctx, CompleteProjectCommand{MutationID: mutationID, ProjectID: command.ProjectID, TenantID: command.TenantID, UserID: command.UserID, ExpectedProjectVersion: command.ExpectedProjectVersion, WorkspaceRevision: command.WorkspaceRevision, CorrelationID: command.RequestID, Reflection: PayloadPointer{Ref: reflection.Ref, Hash: reflection.Hash}, ReflectionManifestHash: reflectionManifestHash, Actor: missionActor(command.UserID, command.SessionID), PrepareCompletedEvent: func(ctx context.Context, manifestHash string) (PayloadPointer, error) {
			manifest, putErr := service.putProjectJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventIDs.event, Class: projectEventClass, ContentType: "application/json"}, map[string]any{"subject_id": command.ProjectID, "subject_version": command.ExpectedProjectVersion + 1, "project_id": command.ProjectID, "workspace_revision": command.WorkspaceRevision, "completion_manifest_hash": manifestHash, "reflection_hash": reflection.Hash, "completed_at": service.projectNow()})
			return PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, putErr
		}})
		return apiProjectMutation(stored), mutationErr
	})
	if err != nil {
		return productapi.ProjectMutationResult{}, service.mapProjectError(err)
	}
	var result productapi.ProjectMutationResult
	err = decodeProjectResponse(encoded, &result)
	result.Replayed = replayed
	return result, service.mapProjectError(err)
}

func (service ProjectApplicationService) executeProjectMutation(ctx context.Context, input idempotencypostgres.Input, descriptor payload.Descriptor, mutation func(ProjectStore) (any, error)) ([]byte, bool, error) {
	if err := service.Store.requireProjectEpoch(ctx); err != nil {
		return nil, false, err
	}
	response, replayed, err := service.projectExecutor().Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		store := service.Store
		store.Tx, store.EpochVerified = tx, true
		result, mutationErr := mutation(store)
		if mutationErr != nil {
			return idempotency.Response{}, mutationErr
		}
		encoded, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			return idempotency.Response{}, encodeErr
		}
		manifest, putErr := service.Payloads.Put(ctx, descriptor, encoded)
		if putErr != nil {
			return idempotency.Response{}, putErr
		}
		version := uint64(1)
		switch value := result.(type) {
		case productapi.ProjectMutationResult:
			version = value.Version
		case productapi.MilestoneMutationResult:
			version = value.Version
		case productapi.WorkspaceMutationResult:
			version = value.Version
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: "application/vnd.lites.project.v2+json", PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: version}, nil
	})
	if err != nil {
		return nil, replayed, err
	}
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	return encoded, replayed, err
}

func (service ProjectApplicationService) loadProjectCompleted(ctx context.Context, input idempotencypostgres.Input, descriptor payload.Descriptor) ([]byte, bool, error) {
	response, found, err := service.projectExecutor().LoadCompleted(ctx, input)
	if err != nil || !found {
		return nil, found, err
	}
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	return encoded, true, err
}

func (service ProjectApplicationService) projectIdempotencyInput(metadata productapi.CommandMetadata, operation string, canonical []byte) (idempotencypostgres.Input, payload.Descriptor, error) {
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return idempotencypostgres.Input{}, payload.Descriptor{}, err
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+operation, metadata.TenantID+"\x00"+metadata.UserID+"\x00"+metadata.IdempotencyKey)
	if err != nil {
		return idempotencypostgres.Input{}, payload.Descriptor{}, err
	}
	return idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: metadata.TenantID, UserID: metadata.UserID, OperationID: operation}, RawKey: metadata.IdempotencyKey, RequestHash: requestHash, RequestID: metadata.RequestID}, payload.Descriptor{TenantID: metadata.TenantID, ObjectID: recordID, Class: projectResponseClass, ContentType: "application/json"}, nil
}

func (service ProjectApplicationService) observeProject(ctx context.Context, tenantID, userID, projectID string) (observedProject, error) {
	var value observedProject
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return value, err
	}
	defer tx.Rollback(ctx)
	if err = setProjectTenant(ctx, tx, tenantID); err != nil {
		return value, err
	}
	err = tx.QueryRow(ctx, `SELECT p.status,p.version,p.mission_id::text,COALESCE(w.head_revision,''),COALESCE(p.completion_manifest_hash,'') FROM product.projects p LEFT JOIN product.project_workspace_bindings w ON w.tenant_id=p.tenant_id AND w.project_id=p.id WHERE p.tenant_id=$1 AND p.user_id=$2 AND p.id=$3`, tenantID, userID, projectID).Scan(&value.Status, &value.Version, &value.MissionID, &value.WorkspaceRevision, &value.CompletionManifestHash)
	if err == nil {
		err = tx.Commit(ctx)
	}
	return value, err
}

func (service ProjectApplicationService) observeMilestone(ctx context.Context, tenantID, userID, projectID, milestoneID string) (observedMilestone, error) {
	var value observedMilestone
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return value, err
	}
	defer tx.Rollback(ctx)
	if err = setProjectTenant(ctx, tx, tenantID); err != nil {
		return value, err
	}
	err = tx.QueryRow(ctx, `SELECT status,version,COALESCE(verification_test_run_id::text,'') FROM product.project_milestones WHERE tenant_id=$1 AND user_id=$2 AND project_id=$3 AND id=$4`, tenantID, userID, projectID, milestoneID).Scan(&value.Status, &value.Version, &value.VerificationID)
	if err == nil {
		err = tx.Commit(ctx)
	}
	return value, err
}

func (service ProjectApplicationService) latestPassingTest(ctx context.Context, tenantID, userID, projectID, milestoneID string) (any, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = setProjectTenant(ctx, tx, tenantID); err != nil {
		return nil, err
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT t.id::text FROM product.project_test_runs t JOIN product.project_workspace_bindings w ON w.tenant_id=t.tenant_id AND w.project_id=t.project_id AND w.head_revision=t.workspace_revision WHERE t.tenant_id=$1 AND t.user_id=$2 AND t.project_id=$3 AND t.milestone_id=$4 AND t.result='passed' ORDER BY t.recorded_at DESC,t.id DESC LIMIT 1`, tenantID, userID, projectID, milestoneID).Scan(&id)
	if err == nil {
		err = tx.Commit(ctx)
	}
	return id, err
}

func (service ProjectApplicationService) projectExecutor() idempotencypostgres.Executor {
	return idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
}

func (service ProjectApplicationService) putProjectJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func (service ProjectApplicationService) encodeProjectCursor(cursor projectCursor) (string, error) {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, service.CursorKey)
	_, _ = mac.Write(encoded)
	return base64.RawURLEncoding.EncodeToString(append(encoded, mac.Sum(nil)...)), nil
}

func (service ProjectApplicationService) decodeProjectCursor(value string) (projectCursor, error) {
	if len(value) > 2048 {
		return projectCursor{}, productapi.ErrValidation
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) <= sha256.Size {
		return projectCursor{}, productapi.ErrValidation
	}
	body, signature := decoded[:len(decoded)-sha256.Size], decoded[len(decoded)-sha256.Size:]
	mac := hmac.New(sha256.New, service.CursorKey)
	_, _ = mac.Write(body)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return projectCursor{}, productapi.ErrValidation
	}
	var cursor projectCursor
	if decodeProjectResponse(body, &cursor) != nil || cursor.TenantID == "" || cursor.UserID == "" || cursor.ID == "" || cursor.Updated.IsZero() {
		return projectCursor{}, productapi.ErrValidation
	}
	return cursor, nil
}

func decodeProjectResponse(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return payload.ErrIntegrity
	}
	return nil
}

func canonicalProjectRequest(command any) ([]byte, error) {
	encoded, err := json.Marshal(command)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if json.Unmarshal(encoded, &value) != nil || value == nil {
		return nil, ErrInvalidProjectCommand
	}
	for _, field := range []string{"RequestID", "IdempotencyKey", "TenantID", "UserID", "SessionID"} {
		delete(value, field)
	}
	return json.Marshal(value)
}

func apiProjectMutation(value ProjectMutationResult) productapi.ProjectMutationResult {
	return productapi.ProjectMutationResult{ID: value.ProjectID, Version: value.Version, Status: value.Status, UpdatedAt: value.UpdatedAt, EventID: value.EventID, CompletionManifestHash: value.CompletionManifestHash, Replayed: value.Replayed}
}

func apiMilestoneMutation(value MilestoneMutationResult) productapi.MilestoneMutationResult {
	return productapi.MilestoneMutationResult{ProjectMutationResult: apiProjectMutation(value.ProjectMutationResult), MilestoneID: value.MilestoneID, MilestoneVersion: value.MilestoneVersion, MilestoneStatus: value.MilestoneStatus}
}

func apiWorkspaceMutation(value WorkspaceMutationResult, projectID, branchName, baseRevision string) productapi.WorkspaceMutationResult {
	return productapi.WorkspaceMutationResult{ProjectMutationResult: apiProjectMutation(value.ProjectMutationResult), Workspace: productapi.WorkspaceResource{ID: value.BindingID, ProjectID: projectID, WorkspaceID: value.WorkspaceID, BranchName: branchName, BaseRevision: baseRevision, HeadRevision: value.HeadRevision, BindingManifestHash: value.ManifestHash, Version: value.BindingVersion, UpdatedAt: value.UpdatedAt}}
}

func (service ProjectApplicationService) valid() bool {
	return service.Pool != nil && service.Store.Pool == service.Pool && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && len(service.CursorKey) >= 32 && service.IdempotencyTTL > 0
}

func validProjectMetadata(value productapi.CommandMetadata) bool {
	return value.RequestID != "" && value.ClientRequestID != "" && value.IdempotencyKey != "" && value.TenantID != "" && value.UserID != "" && value.SessionID != ""
}

func validProjectKind(value string) bool {
	return value == "code" || value == "writing" || value == "design"
}

func validProjectText(value string, maximum int) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) == value && value != "" && utf8.RuneCountInString(value) <= maximum
}

func validOptionalProjectText(value string, maximum int) bool {
	return value == "" || validProjectText(value, maximum)
}

func (service ProjectApplicationService) projectNow() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (service ProjectApplicationService) mapProjectError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, ErrProjectNotFound):
		return productapi.ErrResourceNotFound
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(err, ErrInvalidProjectCommand):
		return productapi.ErrValidation
	case errors.Is(err, ErrProjectConflict), errors.Is(err, ErrProjectNotMutable), errors.Is(err, ErrProjectIncomplete), errors.Is(err, ErrProjectBinding):
		return productapi.ErrStateConflict
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, err)
	}
}

var _ productapi.ProjectService = ProjectApplicationService{}
