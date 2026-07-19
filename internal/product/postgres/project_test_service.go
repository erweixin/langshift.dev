package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/behavior"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
)

const (
	projectTestGenerateOperation = "projects.test.generate.v2"
	projectTestResponseClass     = "project-test-idempotency"
	projectTestOutputContract    = `Return exactly one JSON object and no markdown fences. Closed shape: {"schema_version":1,"result":"passed|failed","summary":"string","checks":[{"name":"string","status":"pass|fail","evidence":"string"}],"uncertainty":"string"}. A passed result requires every check to pass using observed evidence from the exact pinned workspace revision. Never treat user assertions, missing tool output, or inability to inspect/execute as a pass; use failed and explain the uncertainty.`
)

type ProjectTestGenerationService struct {
	Pool                                             *pgxpool.Pool
	Runs                                             executionpostgres.RunStore
	Payloads                                         payload.Store
	IDKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	BehaviorEnvironment                              string
	RunTimeout                                       time.Duration
	RunMaxSteps                                      int
	RunMaxCostMicrounits                             int64
	RunMaxAttempts                                   int
	IdempotencyTTL                                   time.Duration
	Now                                              func() time.Time
}

type projectTestSnapshot struct {
	MissionID, BriefRef, BriefHash, MilestoneTitle, ResultRef, ResultHash string
	BindingID, WorkspaceID, WorkspaceRevision, WorkspaceManifestHash      string
	ProjectVersion, MilestoneVersion, WorkspaceBindingVersion             uint64
	AcceptanceSpec, ValidationSpec                                        json.RawMessage
	AcceptanceSpecHash, ValidationSpecHash                                string
	Behavior                                                              routeBehaviorBinding
	Brief, MilestoneResult                                                []byte
}

type projectTestGenerationIDs struct {
	generation, conversation, message, run, startCommand string
}

type projectTestPrepared struct {
	conversationEvent, message, messageEvent, acceptedEvent, queuedEvent, startCommand executionpostgres.PayloadPointer
	messageHash                                                                        string
}

func (service ProjectTestGenerationService) Generate(ctx context.Context, command productapi.GenerateProjectTestCommand) (productapi.ProjectTestGenerationResult, error) {
	if !service.valid() || !validProjectMetadata(command.CommandMetadata) || command.ProjectID == "" || command.MilestoneID == "" || command.WorkspaceRevision == "" || command.ExpectedProjectVersion < 1 || command.ExpectedMilestoneVersion < 1 || command.ExpectedWorkspaceBindingVersion < 1 || command.ValidationKind != "deterministic_test" && command.ValidationKind != "rubric_review" || len(command.ValidationSpec) == 0 || len(command.ValidationSpec) > 64<<10 || !validJSONObject(command.ValidationSpec) {
		return productapi.ProjectTestGenerationResult{}, productapi.ErrValidation
	}
	canonical, err := canonicalProjectRequest(command)
	if err != nil {
		return productapi.ProjectTestGenerationResult{}, service.mapError(err)
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.ProjectTestGenerationResult{}, service.mapError(err)
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+projectTestGenerateOperation, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return productapi.ProjectTestGenerationResult{}, service.mapError(err)
	}
	identifiers, err := service.identifiers(recordID)
	if err != nil {
		return productapi.ProjectTestGenerationResult{}, service.mapError(err)
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: projectTestResponseClass, ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: projectTestGenerateOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.ProjectTestGenerationResult{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.read(ctx, descriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	snapshot, manifest, err := service.snapshot(ctx, command)
	if err != nil {
		return productapi.ProjectTestGenerationResult{}, service.mapError(err)
	}
	prepared, err := service.prepare(ctx, command, identifiers, snapshot, manifest)
	if err != nil {
		return productapi.ProjectTestGenerationResult{}, service.mapError(err)
	}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		if _, innerErr := tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		var admissible bool
		if innerErr := tx.QueryRow(ctx, `SELECT agent.lock_owned_project_evaluation($1,$2,$3,$4,$5,$6,$7)`, command.TenantID, command.UserID, command.ProjectID, command.ExpectedProjectVersion, command.MilestoneID, command.ExpectedMilestoneVersion, command.WorkspaceRevision).Scan(&admissible); innerErr != nil || !admissible {
			if innerErr != nil {
				return idempotency.Response{}, innerErr
			}
			return idempotency.Response{}, ErrProjectConflict
		}
		var bindingVersion uint64
		var manifestHash string
		if innerErr := tx.QueryRow(ctx, `SELECT version,binding_manifest_hash FROM product.project_workspace_bindings WHERE tenant_id=$1 AND user_id=$2 AND project_id=$3 AND id=$4`, command.TenantID, command.UserID, command.ProjectID, snapshot.BindingID).Scan(&bindingVersion, &manifestHash); innerErr != nil || bindingVersion != command.ExpectedWorkspaceBindingVersion || manifestHash != snapshot.WorkspaceManifestHash {
			if innerErr != nil {
				return idempotency.Response{}, innerErr
			}
			return idempotency.Response{}, ErrProjectConflict
		}
		title := "Project milestone evaluator"
		conversation, innerErr := service.Runs.CreateProjectEvaluatorConversationInTx(ctx, tx, executionpostgres.CreateConversationCommand{ConversationID: identifiers.conversation, TenantID: command.TenantID, UserID: command.UserID, MissionID: snapshot.MissionID, ProjectID: command.ProjectID, ProjectVersion: command.ExpectedProjectVersion, MilestoneID: command.MilestoneID, MilestoneVersion: command.ExpectedMilestoneVersion, WorkspaceRevision: command.WorkspaceRevision, Title: &title, Mode: "project", CorrelationID: command.RequestID, Actor: missionActor(command.UserID, command.SessionID), CreatedEvent: prepared.conversationEvent})
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		now := service.now()
		budget := json.RawMessage(fmt.Sprintf(`{"max_steps":%d,"max_cost_microunits":%d}`, service.RunMaxSteps, service.RunMaxCostMicrounits))
		accepted, innerErr := service.Runs.AcceptMessageRunInTx(ctx, tx, executionpostgres.AcceptMessageRunCommand{Run: executionpostgres.AcceptRunCommand{RunID: identifiers.run, TenantID: command.TenantID, UserID: command.UserID, ConversationID: identifiers.conversation, CorrelationID: command.RequestID, DueAt: now.Add(service.RunTimeout), BehaviorProfile: behavior.Evaluator, BehaviorEnvironment: snapshot.Behavior.Environment, ExpectedProfileSnapshotID: snapshot.Behavior.SnapshotID, ExpectedBehaviorChannelID: snapshot.Behavior.ChannelID, ExpectedBehaviorSequence: snapshot.Behavior.Sequence, PinnedBehaviorBinding: true, BudgetSnapshot: budget, Actor: missionActor(command.UserID, command.SessionID), AcceptedEvent: prepared.acceptedEvent, QueuedEvent: prepared.queuedEvent, StartCommand: prepared.startCommand, QueueClass: "background", ResourceClass: "llm", Priority: 100, CostUnits: int64(service.RunMaxSteps), MaxAttempts: service.RunMaxAttempts}, MessageID: identifiers.message, ExpectedConversationVersion: conversation.Version, ExpectedConversationMode: "project", ExpectedConversationProfile: behavior.Evaluator, Message: prepared.message, ContentHash: prepared.messageHash, AppendedEvent: prepared.messageEvent, Actor: missionActor(command.UserID, command.SessionID)})
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		if accepted.RunID != identifiers.run || accepted.ProfileSnapshotID != snapshot.Behavior.SnapshotID || accepted.BehaviorChannelID != snapshot.Behavior.ChannelID || accepted.BehaviorChannelSequence != snapshot.Behavior.Sequence {
			return idempotency.Response{}, ErrProjectConflict
		}
		inputHash := sha256.Sum256(manifest)
		_, innerErr = tx.Exec(ctx, `INSERT INTO product.project_test_generations(id,tenant_id,user_id,version,request_id,project_id,project_version,milestone_id,milestone_version,workspace_binding_id,workspace_binding_version,workspace_revision,workspace_manifest_hash,validation_kind,validation_spec,validation_spec_hash,status,input_manifest,input_manifest_hash,behavior_profile,behavior_environment,behavior_channel_id,behavior_channel_sequence,behavior_snapshot_id,behavior_activated_at,evaluator_run_id,created_at,updated_at) VALUES($1,$2,$3,1,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,'generating',$16,$17,'evaluator',$18,$19,$20,$21,$22,$23,$24,$24)`, identifiers.generation, command.TenantID, command.UserID, command.ClientRequestID, command.ProjectID, command.ExpectedProjectVersion, command.MilestoneID, command.ExpectedMilestoneVersion, snapshot.BindingID, command.ExpectedWorkspaceBindingVersion, command.WorkspaceRevision, snapshot.WorkspaceManifestHash, command.ValidationKind, snapshot.ValidationSpec, snapshot.ValidationSpecHash, manifest, hex.EncodeToString(inputHash[:]), snapshot.Behavior.Environment, snapshot.Behavior.ChannelID, snapshot.Behavior.Sequence, snapshot.Behavior.SnapshotID, snapshot.Behavior.ActivatedAt, identifiers.run, now)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		result := productapi.ProjectTestGenerationResult{GenerationID: identifiers.generation, RunID: identifiers.run, Status: "queued", AcceptedAt: now, ProjectID: command.ProjectID, MilestoneID: command.MilestoneID, ProjectVersion: command.ExpectedProjectVersion, WorkspaceRevision: command.WorkspaceRevision}
		stored, innerErr := service.putJSON(ctx, descriptor, result)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		return idempotency.Response{Status: http.StatusAccepted, ContentType: "application/vnd.lites.project-test-generation.v2+json", PayloadRef: stored.Ref, Hash: stored.Hash, ResourceVersion: command.ExpectedProjectVersion}, nil
	})
	if err != nil {
		return productapi.ProjectTestGenerationResult{}, service.mapError(err)
	}
	result, err := service.read(ctx, descriptor, response)
	result.Replayed = replayed
	return result, service.mapError(err)
}

func (service ProjectTestGenerationService) snapshot(ctx context.Context, command productapi.GenerateProjectTestCommand) (projectTestSnapshot, []byte, error) {
	var snapshot projectTestSnapshot
	canonicalValidation, validationHash, err := canonicalObject(command.ValidationSpec)
	if err != nil {
		return snapshot, nil, err
	}
	snapshot.ValidationSpec, snapshot.ValidationSpecHash = canonicalValidation, validationHash
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return snapshot, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return snapshot, nil, err
	}
	var projectStatus, milestoneStatus string
	err = tx.QueryRow(ctx, `SELECT p.mission_id::text,p.version,p.status,p.brief_ref,p.brief_hash,pm.version,pm.status,pm.title,pm.acceptance_spec,pm.acceptance_spec_hash,COALESCE(pm.result_ref,''),COALESCE(pm.result_hash,''),w.id::text,w.version,w.workspace_id::text,w.head_revision,w.binding_manifest_hash FROM product.projects p JOIN product.project_milestones pm ON pm.tenant_id=p.tenant_id AND pm.project_id=p.id JOIN product.project_workspace_bindings w ON w.tenant_id=p.tenant_id AND w.project_id=p.id WHERE p.tenant_id=$1 AND p.user_id=$2 AND p.id=$3 AND pm.user_id=$2 AND pm.id=$4 AND w.user_id=$2`, command.TenantID, command.UserID, command.ProjectID, command.MilestoneID).Scan(&snapshot.MissionID, &snapshot.ProjectVersion, &projectStatus, &snapshot.BriefRef, &snapshot.BriefHash, &snapshot.MilestoneVersion, &milestoneStatus, &snapshot.MilestoneTitle, &snapshot.AcceptanceSpec, &snapshot.AcceptanceSpecHash, &snapshot.ResultRef, &snapshot.ResultHash, &snapshot.BindingID, &snapshot.WorkspaceBindingVersion, &snapshot.WorkspaceID, &snapshot.WorkspaceRevision, &snapshot.WorkspaceManifestHash)
	if err != nil {
		return snapshot, nil, err
	}
	if snapshot.ProjectVersion != command.ExpectedProjectVersion || snapshot.MilestoneVersion != command.ExpectedMilestoneVersion || snapshot.WorkspaceBindingVersion != command.ExpectedWorkspaceBindingVersion || snapshot.WorkspaceRevision != command.WorkspaceRevision || projectStatus != "active" && projectStatus != "blocked" || milestoneStatus != "submitted" && milestoneStatus != "rework" || snapshot.ResultRef == "" || snapshot.ResultHash == "" {
		return snapshot, nil, ErrProjectConflict
	}
	snapshot.Behavior.Profile = string(behavior.Evaluator)
	snapshot.Behavior.Environment = service.BehaviorEnvironment
	err = tx.QueryRow(ctx, `SELECT channel_id::text,sequence,snapshot_id,activated_at FROM agent.behavior_channel_deployments WHERE tenant_id=$1 AND profile_name=$2 AND environment=$3 ORDER BY sequence DESC LIMIT 1`, command.TenantID, behavior.Evaluator, service.BehaviorEnvironment).Scan(&snapshot.Behavior.ChannelID, &snapshot.Behavior.Sequence, &snapshot.Behavior.SnapshotID, &snapshot.Behavior.ActivatedAt)
	if err != nil {
		return snapshot, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return snapshot, nil, err
	}
	snapshot.Brief, err = service.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.ProjectID, Class: projectBriefClass, ContentType: "application/json"}, payload.Manifest{Ref: snapshot.BriefRef, Hash: snapshot.BriefHash})
	if err != nil {
		return snapshot, nil, err
	}
	snapshot.MilestoneResult, err = service.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.MilestoneID, Class: projectResultClass, ContentType: "application/json"}, payload.Manifest{Ref: snapshot.ResultRef, Hash: snapshot.ResultHash})
	if err != nil {
		return snapshot, nil, err
	}
	manifestValue := map[string]any{"schema_version": 1, "project_id": command.ProjectID, "project_version": snapshot.ProjectVersion, "milestone_id": command.MilestoneID, "milestone_version": snapshot.MilestoneVersion, "workspace_binding_id": snapshot.BindingID, "workspace_binding_version": snapshot.WorkspaceBindingVersion, "workspace_id": snapshot.WorkspaceID, "workspace_revision": snapshot.WorkspaceRevision, "workspace_manifest_hash": snapshot.WorkspaceManifestHash, "acceptance_spec_hash": snapshot.AcceptanceSpecHash, "milestone_result_hash": snapshot.ResultHash, "validation_kind": command.ValidationKind, "validation_spec_hash": snapshot.ValidationSpecHash, "agent_profile": snapshot.Behavior}
	manifest, err := json.Marshal(manifestValue)
	return snapshot, manifest, err
}

func (service ProjectTestGenerationService) prepare(ctx context.Context, command productapi.GenerateProjectTestCommand, identifiers projectTestGenerationIDs, snapshot projectTestSnapshot, manifest []byte) (projectTestPrepared, error) {
	prompt := map[string]any{"schema_version": 1, "role": "user", "content": []map[string]any{{"type": "text", "text": "Evaluate this exact Project milestone. Use available read/execute tools only against the pinned workspace identity and revision. " + projectTestOutputContract + "\nINPUT_MANIFEST=" + string(manifest) + "\nPROJECT_BRIEF=" + string(snapshot.Brief) + "\nMILESTONE_TITLE=" + snapshot.MilestoneTitle + "\nACCEPTANCE_SPEC=" + string(snapshot.AcceptanceSpec) + "\nMILESTONE_RESULT=" + string(snapshot.MilestoneResult) + "\nVALIDATION_SPEC=" + string(snapshot.ValidationSpec)}}}
	messageJSON, err := json.Marshal(prompt)
	if err != nil {
		return projectTestPrepared{}, err
	}
	digest := sha256.Sum256(messageJSON)
	put := func(objectID, class string, value any) (executionpostgres.PayloadPointer, error) {
		encoded, innerErr := json.Marshal(value)
		if innerErr != nil {
			return executionpostgres.PayloadPointer{}, innerErr
		}
		manifest, innerErr := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, encoded)
		return executionpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, innerErr
	}
	putRaw := func(objectID, class string, encoded []byte) (executionpostgres.PayloadPointer, error) {
		manifest, innerErr := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, encoded)
		return executionpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, innerErr
	}
	var prepared projectTestPrepared
	if prepared.conversationEvent, err = put(identifiers.conversation, "event-payload", map[string]any{"subject_id": identifiers.conversation, "subject_version": 1, "mission_id": snapshot.MissionID, "project_id": command.ProjectID, "purpose": "project_evaluator"}); err != nil {
		return prepared, err
	}
	if prepared.message, err = putRaw(identifiers.message, "run-message", messageJSON); err != nil {
		return prepared, err
	}
	prepared.messageHash = hex.EncodeToString(digest[:])
	if prepared.messageEvent, err = put(identifiers.message, "event-payload", map[string]any{"subject_id": identifiers.conversation, "subject_version": 2, "message_id": identifiers.message, "generation_id": identifiers.generation}); err != nil {
		return prepared, err
	}
	binding := map[string]any{"profile": behavior.Evaluator, "environment": snapshot.Behavior.Environment, "snapshot_id": snapshot.Behavior.SnapshotID, "channel_id": snapshot.Behavior.ChannelID, "sequence": snapshot.Behavior.Sequence}
	if prepared.acceptedEvent, err = put(identifiers.run, "event-payload", map[string]any{"subject_id": identifiers.run, "subject_version": 1, "run_id": identifiers.run, "generation_id": identifiers.generation, "behavior": binding}); err != nil {
		return prepared, err
	}
	if prepared.queuedEvent, err = put(identifiers.startCommand, "event-payload", map[string]any{"subject_id": identifiers.run, "subject_version": 2, "run_id": identifiers.run, "pending_command_id": identifiers.startCommand}); err != nil {
		return prepared, err
	}
	prepared.startCommand, err = put(identifiers.startCommand, "agent-run-command", map[string]any{"schema_version": 1, "run_id": identifiers.run, "correlation_id": command.RequestID})
	return prepared, err
}

func (service ProjectTestGenerationService) identifiers(seed string) (projectTestGenerationIDs, error) {
	domains := []string{"project-test-generation", "project-test-conversation", "project-test-message", "project-test-run"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(service.IDKey, domain, seed)
		if err != nil {
			return projectTestGenerationIDs{}, err
		}
		values[index] = value
	}
	startCommand, err := executionpostgres.RunStartCommandID(service.Runs.IDKey, values[3])
	if err != nil {
		return projectTestGenerationIDs{}, err
	}
	return projectTestGenerationIDs{values[0], values[1], values[2], values[3], startCommand}, nil
}

func (service ProjectTestGenerationService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func (service ProjectTestGenerationService) read(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (productapi.ProjectTestGenerationResult, error) {
	var result productapi.ProjectTestGenerationResult
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return result, err
	}
	err = decodeProjectResponse(encoded, &result)
	return result, err
}

func (service ProjectTestGenerationService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (service ProjectTestGenerationService) valid() bool {
	return service.Pool != nil && service.Runs.Pool == service.Pool && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.BehaviorEnvironment != "" && service.RunTimeout > 0 && service.RunMaxSteps > 0 && service.RunMaxCostMicrounits > 0 && service.RunMaxAttempts > 0 && service.IdempotencyTTL > 0
}

func (service ProjectTestGenerationService) mapError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return productapi.ErrResourceNotFound
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(err, ErrInvalidProjectCommand), errors.Is(err, executionpostgres.ErrInvalidCommand):
		return productapi.ErrValidation
	case errors.Is(err, ErrProjectConflict), errors.Is(err, executionpostgres.ErrRunConflict), errors.Is(err, executionpostgres.ErrRunMessageConflict):
		return productapi.ErrStateConflict
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, err)
	}
}

var _ productapi.ProjectTestService = ProjectTestGenerationService{}
