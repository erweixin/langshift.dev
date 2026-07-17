package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
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
	portfolioRequestOperation = "portfolio-exports.request.v2"
	portfolioResponseClass    = "portfolio-export-idempotency"
	portfolioResponseType     = "application/vnd.lites.portfolio-export.v2+json"
)

type PortfolioExportService struct {
	Pool                                             *pgxpool.Pool
	Store                                            PortfolioExportStore
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

type portfolioExportSnapshot struct {
	MissionID, BriefRef, BriefHash, ReflectionRef, ReflectionHash string
	Workspace                                                     WorkspaceExportBinding
	Artifacts                                                     []PortfolioArtifactBinding
	Evidence                                                      []EvidenceBinding
	Behavior                                                      routeBehaviorBinding
	Brief, Reflection                                             []byte
}

type portfolioExportIDs struct {
	export, conversation, message, run, startPayload string
}

type portfolioExportPrepared struct {
	conversationEvent, message, messageEvent executionpostgres.PayloadPointer
	acceptedEvent, queuedEvent, startCommand executionpostgres.PayloadPointer
	requestedEvent                           PayloadPointer
	messageHash                              string
}

func (service PortfolioExportService) Request(ctx context.Context, command productapi.CreatePortfolioExportCommand) (productapi.PortfolioExportResult, error) {
	if !service.valid() || !validProjectMetadata(command.CommandMetadata) || command.ProjectID == "" || command.ExpectedProjectVersion < 1 || command.ExpectedWorkspaceBindingVersion < 1 || command.WorkspaceRevision == "" || command.Format != "html" && command.Format != "pdf" && command.Format != "zip" || !validPortfolioSelection(command.ArtifactRevisionIDs) {
		return productapi.PortfolioExportResult{}, productapi.ErrValidation
	}
	canonicalCommand := command
	canonicalCommand.ArtifactRevisionIDs = append([]string(nil), command.ArtifactRevisionIDs...)
	sort.Strings(canonicalCommand.ArtifactRevisionIDs)
	canonical, err := canonicalProjectRequest(canonicalCommand)
	if err != nil {
		return productapi.PortfolioExportResult{}, service.mapError(err)
	}
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return productapi.PortfolioExportResult{}, service.mapError(err)
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+portfolioRequestOperation, command.TenantID+"\x00"+command.UserID+"\x00"+command.IdempotencyKey)
	if err != nil {
		return productapi.PortfolioExportResult{}, service.mapError(err)
	}
	identifiers, err := service.identifiers(recordID)
	if err != nil {
		return productapi.PortfolioExportResult{}, service.mapError(err)
	}
	descriptor := payload.Descriptor{TenantID: command.TenantID, ObjectID: recordID, Class: portfolioResponseClass, ContentType: "application/json"}
	input := idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: command.TenantID, UserID: command.UserID, OperationID: portfolioRequestOperation}, RawKey: command.IdempotencyKey, RequestHash: requestHash, RequestID: command.RequestID}
	executor := idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
	if response, found, loadErr := executor.LoadCompleted(ctx, input); loadErr != nil {
		return productapi.PortfolioExportResult{}, service.mapError(loadErr)
	} else if found {
		result, readErr := service.read(ctx, descriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	if err = service.Store.requireEpoch(ctx); err != nil {
		return productapi.PortfolioExportResult{}, service.mapError(err)
	}
	snapshot, manifest, err := service.snapshot(ctx, canonicalCommand)
	if err != nil {
		return productapi.PortfolioExportResult{}, service.mapError(err)
	}
	prepared, err := service.prepare(ctx, command, identifiers, snapshot, manifest)
	if err != nil {
		return productapi.PortfolioExportResult{}, service.mapError(err)
	}
	response, replayed, err := executor.Execute(ctx, input, func(ctx context.Context, tx pgx.Tx) (idempotency.Response, error) {
		now := service.now()
		actor := missionActor(command.UserID, command.SessionID)
		title := "Portfolio export builder"
		budget := json.RawMessage(fmt.Sprintf(`{"max_steps":%d,"max_cost_microunits":%d}`, service.RunMaxSteps, service.RunMaxCostMicrounits))
		run := executionpostgres.AcceptRunCommand{RunID: identifiers.run, TenantID: command.TenantID, UserID: command.UserID, ConversationID: identifiers.conversation, CorrelationID: command.RequestID, DueAt: now.Add(service.RunTimeout), BehaviorProfile: behavior.ArtifactBuilder, BehaviorEnvironment: snapshot.Behavior.Environment, ExpectedProfileSnapshotID: snapshot.Behavior.SnapshotID, ExpectedBehaviorChannelID: snapshot.Behavior.ChannelID, ExpectedBehaviorSequence: snapshot.Behavior.Sequence, PinnedBehaviorBinding: true, BudgetSnapshot: budget, Actor: actor, AcceptedEvent: prepared.acceptedEvent, QueuedEvent: prepared.queuedEvent, StartCommand: prepared.startCommand, QueueClass: "background", ResourceClass: "llm", Priority: 50, CostUnits: int64(service.RunMaxSteps), MaxAttempts: service.RunMaxAttempts}
		conversation := executionpostgres.CreateConversationCommand{ConversationID: identifiers.conversation, TenantID: command.TenantID, UserID: command.UserID, MissionID: snapshot.MissionID, ProjectID: command.ProjectID, ProjectVersion: command.ExpectedProjectVersion, WorkspaceRevision: command.WorkspaceRevision, Title: &title, Mode: "project", CorrelationID: command.RequestID, Actor: actor, CreatedEvent: prepared.conversationEvent}
		messageRun := executionpostgres.AcceptMessageRunCommand{Run: run, MessageID: identifiers.message, ExpectedConversationVersion: 1, ExpectedConversationMode: "project", ExpectedConversationProfile: behavior.ArtifactBuilder, Message: prepared.message, ContentHash: prepared.messageHash, AppendedEvent: prepared.messageEvent, Actor: actor}
		stored, innerErr := service.Store.RequestInTx(ctx, tx, RequestPortfolioExportCommand{ExportID: identifiers.export, RequestID: command.ClientRequestID, TenantID: command.TenantID, UserID: command.UserID, ProjectID: command.ProjectID, CorrelationID: command.RequestID, ProjectVersion: command.ExpectedProjectVersion, Workspace: snapshot.Workspace, Format: command.Format, Artifacts: snapshot.Artifacts, Evidence: snapshot.Evidence, Actor: actor, RequestedEvent: prepared.requestedEvent, Run: run, Conversation: &conversation, MessageRun: &messageRun})
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		result := productapi.PortfolioExportResult{ID: stored.ExportID, ProjectID: command.ProjectID, RunID: stored.RunID, Status: stored.Status, Version: stored.Version, Format: command.Format, RevisionManifestHash: stored.RevisionManifestHash, CreatedAt: stored.CreatedAt}
		responseManifest, innerErr := service.putJSON(ctx, descriptor, result)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		return idempotency.Response{Status: http.StatusAccepted, ContentType: portfolioResponseType, PayloadRef: responseManifest.Ref, Hash: responseManifest.Hash, ResourceVersion: stored.Version}, nil
	})
	if err != nil {
		return productapi.PortfolioExportResult{}, service.mapError(err)
	}
	result, err := service.read(ctx, descriptor, response)
	result.Replayed = replayed
	return result, service.mapError(err)
}

func (service PortfolioExportService) Get(ctx context.Context, tenantID, userID, exportID string) (productapi.PortfolioExportResult, error) {
	if !service.valid() || tenantID == "" || userID == "" || exportID == "" {
		return productapi.PortfolioExportResult{}, productapi.ErrValidation
	}
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return productapi.PortfolioExportResult{}, service.mapError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return productapi.PortfolioExportResult{}, service.mapError(err)
	}
	var result productapi.PortfolioExportResult
	err = tx.QueryRow(ctx, `SELECT id::text,project_id::text,run_id::text,status,version,export_format,revision_manifest_hash,content_hash,media_type,byte_size,created_at,started_at,completed_at,expires_at,failure_code FROM product.portfolio_exports WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenantID, userID, exportID).Scan(&result.ID, &result.ProjectID, &result.RunID, &result.Status, &result.Version, &result.Format, &result.RevisionManifestHash, &result.ContentHash, &result.MediaType, &result.ByteSize, &result.CreatedAt, &result.StartedAt, &result.CompletedAt, &result.ExpiresAt, &result.FailureCode)
	if err != nil {
		return productapi.PortfolioExportResult{}, service.mapError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return productapi.PortfolioExportResult{}, service.mapError(err)
	}
	return result, nil
}

func (service PortfolioExportService) snapshot(ctx context.Context, command productapi.CreatePortfolioExportCommand) (portfolioExportSnapshot, []byte, error) {
	var snapshot portfolioExportSnapshot
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return snapshot, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return snapshot, nil, err
	}
	var projectStatus string
	var projectVersion uint64
	err = tx.QueryRow(ctx, `SELECT mission_id::text,status,version,brief_ref,brief_hash,reflection_ref,reflection_hash FROM product.projects WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, command.TenantID, command.UserID, command.ProjectID).Scan(&snapshot.MissionID, &projectStatus, &projectVersion, &snapshot.BriefRef, &snapshot.BriefHash, &snapshot.ReflectionRef, &snapshot.ReflectionHash)
	if err != nil || projectStatus != "completed" || projectVersion != command.ExpectedProjectVersion || snapshot.ReflectionRef == "" || snapshot.ReflectionHash == "" {
		if err != nil {
			return snapshot, nil, err
		}
		return snapshot, nil, ErrPortfolioNotExportable
	}
	err = tx.QueryRow(ctx, `SELECT id::text,version,head_revision,binding_manifest_hash FROM product.project_workspace_bindings WHERE tenant_id=$1 AND user_id=$2 AND project_id=$3`, command.TenantID, command.UserID, command.ProjectID).Scan(&snapshot.Workspace.BindingID, &snapshot.Workspace.Version, &snapshot.Workspace.Revision, &snapshot.Workspace.ManifestHash)
	if err != nil || snapshot.Workspace.Version != command.ExpectedWorkspaceBindingVersion || snapshot.Workspace.Revision != command.WorkspaceRevision {
		if err != nil {
			return snapshot, nil, err
		}
		return snapshot, nil, ErrPortfolioBinding
	}
	evidence := make(map[string]EvidenceBinding)
	for _, revisionID := range command.ArtifactRevisionIDs {
		var binding PortfolioArtifactBinding
		var projectID, userID, artifactStatus, scanStatus string
		err = tx.QueryRow(ctx, `SELECT r.id::text,r.artifact_id::text,r.project_id::text,r.user_id::text,a.status,r.revision,r.content_hash,r.object_version,r.workspace_revision,r.media_type,r.byte_size,r.scan_status,r.scan_result_hash,r.evidence_manifest_hash FROM product.artifact_revisions r JOIN product.artifacts a ON a.tenant_id=r.tenant_id AND a.id=r.artifact_id WHERE r.tenant_id=$1 AND r.id=$2`, command.TenantID, revisionID).Scan(&binding.RevisionID, &binding.ArtifactID, &projectID, &userID, &artifactStatus, &binding.Revision, &binding.ContentHash, &binding.ObjectVersion, &binding.WorkspaceRevision, &binding.MediaType, &binding.ByteSize, &scanStatus, &binding.ScanResultHash, &binding.EvidenceManifestHash)
		if err != nil || projectID != command.ProjectID || userID != command.UserID || artifactStatus != "ready" || scanStatus != "passed" || binding.WorkspaceRevision != command.WorkspaceRevision {
			if err != nil {
				return snapshot, nil, err
			}
			return snapshot, nil, ErrPortfolioBinding
		}
		snapshot.Artifacts = append(snapshot.Artifacts, binding)
		rows, queryErr := tx.Query(ctx, `SELECT evidence_id::text,evidence_version,evidence_content_hash FROM product.artifact_revision_evidence WHERE tenant_id=$1 AND artifact_revision_id=$2 ORDER BY ordinal`, command.TenantID, revisionID)
		if queryErr != nil {
			return snapshot, nil, queryErr
		}
		count := 0
		for rows.Next() {
			var item EvidenceBinding
			if scanErr := rows.Scan(&item.EvidenceID, &item.Version, &item.ContentHash); scanErr != nil {
				rows.Close()
				return snapshot, nil, scanErr
			}
			if existing, found := evidence[item.EvidenceID]; found && existing != item {
				rows.Close()
				return snapshot, nil, ErrPortfolioBinding
			}
			evidence[item.EvidenceID] = item
			count++
		}
		if rows.Err() != nil || count == 0 {
			rowErr := rows.Err()
			rows.Close()
			if rowErr != nil {
				return snapshot, nil, rowErr
			}
			return snapshot, nil, ErrPortfolioBinding
		}
		rows.Close()
	}
	for _, item := range evidence {
		snapshot.Evidence = append(snapshot.Evidence, item)
	}
	sort.Slice(snapshot.Artifacts, func(i, j int) bool { return snapshot.Artifacts[i].ArtifactID < snapshot.Artifacts[j].ArtifactID })
	sort.Slice(snapshot.Evidence, func(i, j int) bool { return snapshot.Evidence[i].EvidenceID < snapshot.Evidence[j].EvidenceID })
	snapshot.Behavior.Profile = string(behavior.ArtifactBuilder)
	snapshot.Behavior.Environment = service.BehaviorEnvironment
	err = tx.QueryRow(ctx, `SELECT channel_id::text,sequence,snapshot_id,activated_at FROM agent.behavior_channel_deployments WHERE tenant_id=$1 AND profile_name=$2 AND environment=$3 ORDER BY sequence DESC LIMIT 1`, command.TenantID, behavior.ArtifactBuilder, service.BehaviorEnvironment).Scan(&snapshot.Behavior.ChannelID, &snapshot.Behavior.Sequence, &snapshot.Behavior.SnapshotID, &snapshot.Behavior.ActivatedAt)
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
	snapshot.Reflection, err = service.Payloads.Get(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: command.ProjectID, Class: projectReflectionClass, ContentType: "application/json"}, payload.Manifest{Ref: snapshot.ReflectionRef, Hash: snapshot.ReflectionHash})
	if err != nil {
		return snapshot, nil, err
	}
	manifest := map[string]any{"schema_version": 1, "project_id": command.ProjectID, "project_version": command.ExpectedProjectVersion, "workspace": snapshot.Workspace, "export_format": command.Format, "artifacts": snapshot.Artifacts, "evidence": snapshot.Evidence, "project_brief_hash": snapshot.BriefHash, "reflection_hash": snapshot.ReflectionHash, "agent_profile": snapshot.Behavior}
	encoded, err := json.Marshal(manifest)
	return snapshot, encoded, err
}

func (service PortfolioExportService) prepare(ctx context.Context, command productapi.CreatePortfolioExportCommand, identifiers portfolioExportIDs, snapshot portfolioExportSnapshot, manifest []byte) (portfolioExportPrepared, error) {
	prompt := map[string]any{"schema_version": 1, "role": "user", "content": []map[string]any{{"type": "text", "text": "Build the requested portfolio export from only the exact immutable manifest below. Read each Artifact revision through platform tools, preserve its provenance, and use the approved artifact_export tool so the platform—not the model—writes, hashes, scans, and versions the output. Never claim success from prose or an unverified object. Return exactly one closed JSON object: {\"schema_version\":1,\"result\":\"built|failed\",\"export_id\":\"uuid\",\"tool_call_id\":\"uuid|null\",\"summary\":\"string\",\"uncertainty\":\"string\"}. INPUT_MANIFEST=" + string(manifest) + " PROJECT_BRIEF=" + string(snapshot.Brief) + " PROJECT_REFLECTION=" + string(snapshot.Reflection)}}}
	messageJSON, err := json.Marshal(prompt)
	if err != nil {
		return portfolioExportPrepared{}, err
	}
	digest := sha256.Sum256(messageJSON)
	put := func(objectID, class string, value any) (executionpostgres.PayloadPointer, error) {
		encoded, innerErr := json.Marshal(value)
		if innerErr != nil {
			return executionpostgres.PayloadPointer{}, innerErr
		}
		stored, innerErr := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, encoded)
		return executionpostgres.PayloadPointer{Ref: stored.Ref, Hash: stored.Hash}, innerErr
	}
	putRaw := func(objectID, class string, encoded []byte) (executionpostgres.PayloadPointer, error) {
		stored, innerErr := service.Payloads.Put(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, encoded)
		return executionpostgres.PayloadPointer{Ref: stored.Ref, Hash: stored.Hash}, innerErr
	}
	var prepared portfolioExportPrepared
	if prepared.conversationEvent, err = put(identifiers.conversation, "event-payload", map[string]any{"subject_id": identifiers.conversation, "subject_version": 1, "mission_id": snapshot.MissionID, "project_id": command.ProjectID, "export_id": identifiers.export, "purpose": "portfolio_builder"}); err != nil {
		return prepared, err
	}
	if prepared.message, err = putRaw(identifiers.message, "run-message", messageJSON); err != nil {
		return prepared, err
	}
	prepared.messageHash = hex.EncodeToString(digest[:])
	if prepared.messageEvent, err = put(identifiers.message, "event-payload", map[string]any{"subject_id": identifiers.conversation, "subject_version": 2, "message_id": identifiers.message, "export_id": identifiers.export}); err != nil {
		return prepared, err
	}
	binding := map[string]any{"profile": behavior.ArtifactBuilder, "environment": snapshot.Behavior.Environment, "snapshot_id": snapshot.Behavior.SnapshotID, "channel_id": snapshot.Behavior.ChannelID, "sequence": snapshot.Behavior.Sequence}
	if prepared.acceptedEvent, err = put(identifiers.run, "event-payload", map[string]any{"subject_id": identifiers.run, "subject_version": 1, "run_id": identifiers.run, "export_id": identifiers.export, "behavior": binding}); err != nil {
		return prepared, err
	}
	if prepared.queuedEvent, err = put(identifiers.startPayload, "event-payload", map[string]any{"subject_id": identifiers.run, "subject_version": 2, "run_id": identifiers.run}); err != nil {
		return prepared, err
	}
	if prepared.startCommand, err = put(identifiers.startPayload, "agent-run-command", map[string]any{"schema_version": 1, "run_id": identifiers.run, "export_id": identifiers.export, "correlation_id": command.RequestID}); err != nil {
		return prepared, err
	}
	requested, err := put(identifiers.export, "event-payload", map[string]any{"subject_id": identifiers.export, "subject_version": 1, "project_id": command.ProjectID, "project_version": command.ExpectedProjectVersion, "workspace_revision": command.WorkspaceRevision, "format": command.Format})
	prepared.requestedEvent = PayloadPointer{Ref: requested.Ref, Hash: requested.Hash}
	return prepared, err
}

func (service PortfolioExportService) identifiers(seed string) (portfolioExportIDs, error) {
	domains := []string{"portfolio-export", "portfolio-conversation", "portfolio-message", "portfolio-run", "portfolio-start-payload"}
	values := make([]string, len(domains))
	for index, domain := range domains {
		value, err := ids.DeterministicUUID(service.IDKey, domain, seed)
		if err != nil {
			return portfolioExportIDs{}, err
		}
		values[index] = value
	}
	return portfolioExportIDs{values[0], values[1], values[2], values[3], values[4]}, nil
}

func (service PortfolioExportService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func (service PortfolioExportService) read(ctx context.Context, descriptor payload.Descriptor, response idempotency.Response) (productapi.PortfolioExportResult, error) {
	var result productapi.PortfolioExportResult
	encoded, err := service.Payloads.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil {
		return result, err
	}
	err = decodeProjectResponse(encoded, &result)
	return result, err
}

func (service PortfolioExportService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (service PortfolioExportService) valid() bool {
	return service.Pool != nil && service.Store.Pool == service.Pool && service.Payloads != nil && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && (service.BehaviorEnvironment == "production" || service.BehaviorEnvironment == "staging") && service.RunTimeout > 0 && service.RunMaxSteps > 0 && service.RunMaxCostMicrounits > 0 && service.RunMaxAttempts > 0 && service.IdempotencyTTL > 0
}

func (service PortfolioExportService) mapError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return productapi.ErrResourceNotFound
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(err, ErrInvalidPortfolioExport), errors.Is(err, executionpostgres.ErrInvalidCommand):
		return productapi.ErrValidation
	case errors.Is(err, ErrPortfolioExportConflict), errors.Is(err, ErrPortfolioNotExportable), errors.Is(err, ErrPortfolioBinding), errors.Is(err, executionpostgres.ErrRunConflict), errors.Is(err, executionpostgres.ErrRunMessageConflict):
		return productapi.ErrStateConflict
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, err)
	}
}

func validPortfolioSelection(values []string) bool {
	if len(values) < 1 || len(values) > 100 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return false
		}
		if _, found := seen[value]; found {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

var _ productapi.PortfolioExportService = PortfolioExportService{}
