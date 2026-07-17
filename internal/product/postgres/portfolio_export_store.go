package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/platform/ids"
	"github.com/langshift/lites/internal/security/opaque"
)

var (
	ErrInvalidPortfolioExport  = errors.New("portfolio export command is invalid")
	ErrPortfolioExportConflict = errors.New("portfolio export conflicts with durable state")
	ErrPortfolioNotExportable  = errors.New("project is not exportable at the requested version")
	ErrPortfolioBinding        = errors.New("portfolio export revision binding is invalid")
)

type PortfolioExportStore struct {
	Pool       *pgxpool.Pool
	Appender   eventpostgres.Appender
	IDKey      []byte
	StoreEpoch string
	Epochs     EpochAuthority
	RunTokens  opaque.Manager
	Behavior   executionpostgres.BehaviorResolver
	Now        func() time.Time
}

type WorkspaceExportBinding struct {
	BindingID    string `json:"binding_id"`
	Version      uint64 `json:"version"`
	Revision     string `json:"revision"`
	ManifestHash string `json:"manifest_hash"`
}

type PortfolioArtifactBinding struct {
	RevisionID           string `json:"revision_id"`
	ArtifactID           string `json:"artifact_id"`
	Revision             int    `json:"revision"`
	ContentHash          string `json:"content_hash"`
	ObjectVersion        string `json:"object_version"`
	WorkspaceRevision    string `json:"workspace_revision"`
	MediaType            string `json:"media_type"`
	ByteSize             int64  `json:"byte_size"`
	ScanResultHash       string `json:"scan_result_hash"`
	EvidenceManifestHash string `json:"evidence_manifest_hash"`
}

type RequestPortfolioExportCommand struct {
	ExportID, RequestID, TenantID, UserID string
	ProjectID, CorrelationID              string
	ProjectVersion                        uint64
	Workspace                             WorkspaceExportBinding
	Format                                string
	Artifacts                             []PortfolioArtifactBinding
	Evidence                              []EvidenceBinding
	Actor                                 json.RawMessage
	RequestedEvent                        PayloadPointer
	Run                                   executionpostgres.AcceptRunCommand
	// Conversation and MessageRun are populated by the public application
	// service. Keeping them optional preserves the lower-level recovery tests
	// that seed an already-admitted conversation, while the public path commits
	// the immutable builder input and Run in the same transaction as the export.
	Conversation *executionpostgres.CreateConversationCommand
	MessageRun   *executionpostgres.AcceptMessageRunCommand
}

type PortfolioExport struct {
	ExportID, RunID, StartCommandID string
	Status                          string
	Version                         uint64
	RevisionManifestHash            string
	CreatedAt                       time.Time
	Replayed                        bool
}

type portfolioProjectBinding struct {
	ProjectID string `json:"project_id"`
	Version   uint64 `json:"version"`
}

type portfolioManifest struct {
	SchemaVersion int                        `json:"schema_version"`
	Project       portfolioProjectBinding    `json:"project"`
	Workspace     WorkspaceExportBinding     `json:"workspace"`
	ExportFormat  string                     `json:"export_format"`
	Artifacts     []PortfolioArtifactBinding `json:"artifacts"`
	Evidence      []EvidenceBinding          `json:"evidence"`
}

type portfolioEventIDs struct{ event, outbox, publish string }

// Request creates the immutable portfolio revision manifest and queues its
// artifact_builder Run in the same PostgreSQL transaction.
func (store PortfolioExportStore) Request(ctx context.Context, command RequestPortfolioExportCommand) (PortfolioExport, error) {
	if !store.valid() {
		return PortfolioExport{}, ErrConfiguration
	}
	if !validPortfolioExport(command) {
		return PortfolioExport{}, ErrInvalidPortfolioExport
	}
	if err := store.requireEpoch(ctx); err != nil {
		return PortfolioExport{}, err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return PortfolioExport{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := store.RequestInTx(ctx, tx, command)
	if err != nil {
		return PortfolioExport{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PortfolioExport{}, err
	}
	return result, nil
}

// RequestInTx lets the public idempotency record, encrypted builder input,
// Conversation, Run, immutable revision manifest, events, and start command
// share one commit. The caller owns commit or rollback and must perform the
// store-epoch check before entering its transaction.
func (store PortfolioExportStore) RequestInTx(ctx context.Context, tx pgx.Tx, command RequestPortfolioExportCommand) (PortfolioExport, error) {
	if tx == nil || !store.valid() || !validPortfolioExport(command) {
		return PortfolioExport{}, ErrInvalidPortfolioExport
	}
	manifest, manifestHash, artifacts, evidence, err := canonicalPortfolioManifest(command)
	if err != nil {
		return PortfolioExport{}, err
	}
	eventIDs, err := store.portfolioEventIDs(command.ExportID)
	if err != nil {
		return PortfolioExport{}, ErrConfiguration
	}
	now := store.now()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return PortfolioExport{}, err
	}
	// Idempotency keys are serialized before either the Run or export aggregate
	// is written, so concurrent retries cannot create an orphan losing Run.
	lockKey := command.TenantID + ":" + command.UserID + ":" + command.RequestID
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return PortfolioExport{}, err
	}
	if replay, found, replayErr := store.loadPortfolioReplay(ctx, tx, command, manifest, manifestHash, artifacts, evidence, eventIDs.event); replayErr != nil {
		return PortfolioExport{}, replayErr
	} else if found {
		return replay, nil
	}

	var projectUser, projectMission, projectStatus string
	var projectVersion uint64
	var reflection *string
	err = tx.QueryRow(ctx, `SELECT user_id::text,mission_id::text,status,version,reflection_ref FROM product.projects WHERE tenant_id=$1 AND id=$2 FOR SHARE`, command.TenantID, command.ProjectID).Scan(&projectUser, &projectMission, &projectStatus, &projectVersion, &reflection)
	if err != nil || projectUser != command.UserID || projectStatus != "completed" || projectVersion != command.ProjectVersion || reflection == nil || *reflection == "" {
		return PortfolioExport{}, ErrPortfolioNotExportable
	}
	var workspaceUser, workspaceProject, workspaceRevision, workspaceHash string
	var workspaceVersion uint64
	// The binding's exact composite foreign key is the race boundary. Taking a
	// row lock here would require UPDATE privilege on an otherwise read-only
	// table; a concurrent head change instead makes the later insert fail its
	// exact FK, while an accepted export prevents that exact parent from moving.
	err = tx.QueryRow(ctx, `SELECT user_id::text,project_id::text,version,head_revision,binding_manifest_hash FROM product.project_workspace_bindings WHERE tenant_id=$1 AND id=$2`, command.TenantID, command.Workspace.BindingID).Scan(&workspaceUser, &workspaceProject, &workspaceVersion, &workspaceRevision, &workspaceHash)
	if err != nil || workspaceUser != command.UserID || workspaceProject != command.ProjectID || workspaceVersion != command.Workspace.Version || workspaceRevision != command.Workspace.Revision || workspaceHash != command.Workspace.ManifestHash {
		return PortfolioExport{}, ErrPortfolioBinding
	}
	evidenceSet := make(map[string]EvidenceBinding, len(evidence))
	for _, binding := range evidence {
		evidenceSet[binding.EvidenceID] = binding
	}
	for _, binding := range artifacts {
		var artifactID, projectID, userID, status, contentHash, objectVersion, workspaceRevision, mediaType, scanStatus, scanResultHash, evidenceManifestHash string
		var revision int
		var byteSize int64
		// Revisions are append-only and later protected by an exact composite FK.
		// Lock only the mutable artifact row whose ready status is being checked.
		err = tx.QueryRow(ctx, `SELECT r.artifact_id::text,r.project_id::text,r.user_id::text,a.status,r.revision,r.content_hash,r.object_version,r.workspace_revision,r.media_type,r.byte_size,r.scan_status,r.scan_result_hash,r.evidence_manifest_hash FROM product.artifact_revisions r JOIN product.artifacts a ON a.tenant_id=r.tenant_id AND a.id=r.artifact_id WHERE r.tenant_id=$1 AND r.id=$2 FOR SHARE OF a`, command.TenantID, binding.RevisionID).Scan(&artifactID, &projectID, &userID, &status, &revision, &contentHash, &objectVersion, &workspaceRevision, &mediaType, &byteSize, &scanStatus, &scanResultHash, &evidenceManifestHash)
		if err != nil || artifactID != binding.ArtifactID || projectID != command.ProjectID || userID != command.UserID || status != "ready" || revision != binding.Revision || contentHash != binding.ContentHash || objectVersion != binding.ObjectVersion || workspaceRevision != binding.WorkspaceRevision || mediaType != binding.MediaType || byteSize != binding.ByteSize || scanStatus != "passed" || scanResultHash != binding.ScanResultHash || evidenceManifestHash != binding.EvidenceManifestHash {
			return PortfolioExport{}, ErrPortfolioBinding
		}
		rows, queryErr := tx.Query(ctx, `SELECT evidence_id::text,evidence_version,evidence_content_hash FROM product.artifact_revision_evidence WHERE tenant_id=$1 AND artifact_revision_id=$2`, command.TenantID, binding.RevisionID)
		if queryErr != nil {
			return PortfolioExport{}, ErrPortfolioBinding
		}
		artifactEvidenceCount := 0
		for rows.Next() {
			var artifactEvidence EvidenceBinding
			if rows.Scan(&artifactEvidence.EvidenceID, &artifactEvidence.Version, &artifactEvidence.ContentHash) != nil || evidenceSet[artifactEvidence.EvidenceID] != artifactEvidence {
				rows.Close()
				return PortfolioExport{}, ErrPortfolioBinding
			}
			artifactEvidenceCount++
		}
		if rows.Err() != nil || artifactEvidenceCount == 0 {
			rows.Close()
			return PortfolioExport{}, ErrPortfolioBinding
		}
		rows.Close()
	}
	for _, binding := range evidence {
		var missionID, userID, status, contentHash string
		var version uint64
		err = tx.QueryRow(ctx, `SELECT mission_id::text,user_id::text,status,version,content_hash FROM product.evidence WHERE tenant_id=$1 AND id=$2 FOR SHARE`, command.TenantID, binding.EvidenceID).Scan(&missionID, &userID, &status, &version, &contentHash)
		if err != nil || missionID != projectMission || userID != command.UserID || status != "recorded" && status != "verified" || version != binding.Version || contentHash != binding.ContentHash {
			return PortfolioExport{}, ErrPortfolioBinding
		}
	}

	runStore := executionpostgres.RunStore{Appender: store.Appender, IDKey: store.IDKey, StoreEpoch: store.StoreEpoch, Now: store.Now, Behavior: store.Behavior}
	if command.Conversation != nil {
		if _, err = runStore.CreatePortfolioBuilderConversationInTx(ctx, tx, *command.Conversation); err != nil {
			return PortfolioExport{}, err
		}
	}
	var acceptedRun executionpostgres.AcceptedRun
	if command.MessageRun != nil {
		acceptedMessage, acceptErr := runStore.AcceptMessageRunInTx(ctx, tx, *command.MessageRun)
		if acceptErr != nil {
			return PortfolioExport{}, acceptErr
		}
		acceptedRun = acceptedMessage.AcceptedRun
	} else {
		acceptedRun, err = runStore.AcceptInTx(ctx, tx, command.Run)
		if err != nil {
			return PortfolioExport{}, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO product.portfolio_exports(id,tenant_id,user_id,project_id,status,revision_manifest,request_id,project_version,workspace_binding_id,workspace_binding_version,workspace_revision,workspace_manifest_hash,export_format,revision_manifest_hash,run_id,start_command_id,request_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,'requested',$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$17)`, command.ExportID, command.TenantID, command.UserID, command.ProjectID, manifest, command.RequestID, command.ProjectVersion, command.Workspace.BindingID, command.Workspace.Version, command.Workspace.Revision, command.Workspace.ManifestHash, command.Format, manifestHash, command.Run.RunID, acceptedRun.StartCommandID, eventIDs.event, now)
	if err != nil {
		if constraintViolation(err) {
			return PortfolioExport{}, ErrPortfolioExportConflict
		}
		return PortfolioExport{}, err
	}
	for ordinal, binding := range artifacts {
		if _, err = tx.Exec(ctx, `INSERT INTO product.portfolio_export_artifacts(tenant_id,portfolio_export_id,artifact_revision_id,artifact_id,artifact_revision,content_hash,ordinal,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, command.TenantID, command.ExportID, binding.RevisionID, binding.ArtifactID, binding.Revision, binding.ContentHash, ordinal, now); err != nil {
			return PortfolioExport{}, ErrPortfolioBinding
		}
	}
	for ordinal, binding := range evidence {
		if _, err = tx.Exec(ctx, `INSERT INTO product.portfolio_export_evidence(tenant_id,portfolio_export_id,evidence_id,evidence_version,evidence_content_hash,ordinal,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, command.TenantID, command.ExportID, binding.EvidenceID, binding.Version, binding.ContentHash, ordinal, now); err != nil {
			return PortfolioExport{}, ErrPortfolioBinding
		}
	}
	requested := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.event, TenantID: command.TenantID, UserID: command.UserID, EventType: "PortfolioExportRequested", SchemaVersion: 2, AggregateKind: "portfolio_export", AggregateID: command.ExportID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.RequestedEvent.Ref, PayloadHash: command.RequestedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.outbox, CommandID: eventIDs.publish, CommandType: "events.publish", PayloadRef: command.RequestedEvent.Ref, PayloadHash: command.RequestedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, requested); err != nil {
		return PortfolioExport{}, err
	}
	return PortfolioExport{ExportID: command.ExportID, RunID: command.Run.RunID, StartCommandID: acceptedRun.StartCommandID, Status: "requested", Version: 1, RevisionManifestHash: manifestHash, CreatedAt: now}, nil
}

func (store PortfolioExportStore) loadPortfolioReplay(ctx context.Context, tx pgx.Tx, command RequestPortfolioExportCommand, expectedManifest json.RawMessage, expectedHash string, artifacts []PortfolioArtifactBinding, evidence []EvidenceBinding, requestEventID string) (PortfolioExport, bool, error) {
	var actual PortfolioExport
	var exportID, userID, projectID, requestID, workspaceID, workspaceRevision, workspaceHash, format, runID, startCommandID, durableEventID string
	var projectVersion, workspaceVersion uint64
	var manifest json.RawMessage
	err := tx.QueryRow(ctx, `SELECT id::text,user_id::text,project_id::text,request_id,project_version,workspace_binding_id::text,workspace_binding_version,workspace_revision,workspace_manifest_hash,export_format,revision_manifest,revision_manifest_hash,run_id::text,start_command_id::text,request_event_id::text,status,version,created_at FROM product.portfolio_exports WHERE tenant_id=$1 AND (id=$2 OR (user_id=$3 AND request_id=$4)) FOR UPDATE`, command.TenantID, command.ExportID, command.UserID, command.RequestID).Scan(&exportID, &userID, &projectID, &requestID, &projectVersion, &workspaceID, &workspaceVersion, &workspaceRevision, &workspaceHash, &format, &manifest, &actual.RevisionManifestHash, &runID, &startCommandID, &durableEventID, &actual.Status, &actual.Version, &actual.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return PortfolioExport{}, false, nil
	}
	if err != nil || exportID != command.ExportID || userID != command.UserID || projectID != command.ProjectID || requestID != command.RequestID || projectVersion != command.ProjectVersion || workspaceID != command.Workspace.BindingID || workspaceVersion != command.Workspace.Version || workspaceRevision != command.Workspace.Revision || workspaceHash != command.Workspace.ManifestHash || format != command.Format || actual.RevisionManifestHash != expectedHash || runID != command.Run.RunID || durableEventID != requestEventID {
		return PortfolioExport{}, false, ErrPortfolioExportConflict
	}
	var actualValue, expectedValue portfolioManifest
	if json.Unmarshal(manifest, &actualValue) != nil || json.Unmarshal(expectedManifest, &expectedValue) != nil || !reflect.DeepEqual(actualValue, expectedValue) {
		return PortfolioExport{}, false, ErrPortfolioExportConflict
	}
	var artifactCount, evidenceCount int
	if err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM product.portfolio_export_artifacts WHERE tenant_id=$1 AND portfolio_export_id=$2),(SELECT count(*) FROM product.portfolio_export_evidence WHERE tenant_id=$1 AND portfolio_export_id=$2)`, command.TenantID, command.ExportID).Scan(&artifactCount, &evidenceCount); err != nil || artifactCount != len(artifacts) || evidenceCount != len(evidence) {
		return PortfolioExport{}, false, ErrPortfolioExportConflict
	}
	for ordinal, binding := range artifacts {
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.portfolio_export_artifacts WHERE tenant_id=$1 AND portfolio_export_id=$2 AND artifact_revision_id=$3 AND artifact_id=$4 AND artifact_revision=$5 AND content_hash=$6 AND ordinal=$7)`, command.TenantID, command.ExportID, binding.RevisionID, binding.ArtifactID, binding.Revision, binding.ContentHash, ordinal).Scan(&exists)
		if err != nil || !exists {
			return PortfolioExport{}, false, ErrPortfolioExportConflict
		}
	}
	for ordinal, binding := range evidence {
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.portfolio_export_evidence WHERE tenant_id=$1 AND portfolio_export_id=$2 AND evidence_id=$3 AND evidence_version=$4 AND evidence_content_hash=$5 AND ordinal=$6)`, command.TenantID, command.ExportID, binding.EvidenceID, binding.Version, binding.ContentHash, ordinal).Scan(&exists)
		if err != nil || !exists {
			return PortfolioExport{}, false, ErrPortfolioExportConflict
		}
	}
	var requestEvent, acceptedEvent, queuedEvent, startCommand bool
	err = tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='PortfolioExportRequested' AND event_schema_version=2 AND aggregate_kind='portfolio_export' AND aggregate_id=$3 AND aggregate_version=1 AND store_epoch=$4 AND payload_ref=$5 AND payload_hash=$6),
		EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$7 AND aggregate_version=1 AND event_type='RunAccepted' AND store_epoch=$4 AND payload_ref=$8 AND payload_hash=$9),
		EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='run' AND aggregate_id=$7 AND aggregate_version=2 AND event_type='RunQueued' AND store_epoch=$4 AND payload_ref=$10 AND payload_hash=$11),
		EXISTS(SELECT 1 FROM agent.outbox WHERE tenant_id=$1 AND command_id=$12 AND command_type='StartAgentRun' AND aggregate_kind='run' AND aggregate_id=$7 AND store_epoch=$4 AND payload_ref=$13 AND payload_hash=$14)`, command.TenantID, requestEventID, command.ExportID, store.StoreEpoch, command.RequestedEvent.Ref, command.RequestedEvent.Hash, command.Run.RunID, command.Run.AcceptedEvent.Ref, command.Run.AcceptedEvent.Hash, command.Run.QueuedEvent.Ref, command.Run.QueuedEvent.Hash, startCommandID, command.Run.StartCommand.Ref, command.Run.StartCommand.Hash).Scan(&requestEvent, &acceptedEvent, &queuedEvent, &startCommand)
	if err != nil || !requestEvent || !acceptedEvent || !queuedEvent || !startCommand {
		return PortfolioExport{}, false, ErrPortfolioExportConflict
	}
	actual.ExportID, actual.RunID, actual.StartCommandID, actual.Replayed = exportID, runID, startCommandID, true
	return actual, true, nil
}

func canonicalPortfolioManifest(command RequestPortfolioExportCommand) (json.RawMessage, string, []PortfolioArtifactBinding, []EvidenceBinding, error) {
	artifacts := append([]PortfolioArtifactBinding(nil), command.Artifacts...)
	evidence := append([]EvidenceBinding(nil), command.Evidence...)
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].ArtifactID == artifacts[j].ArtifactID {
			return artifacts[i].RevisionID < artifacts[j].RevisionID
		}
		return artifacts[i].ArtifactID < artifacts[j].ArtifactID
	})
	sort.Slice(evidence, func(i, j int) bool { return evidence[i].EvidenceID < evidence[j].EvidenceID })
	for index := range artifacts {
		if index > 0 && artifacts[index-1].ArtifactID == artifacts[index].ArtifactID {
			return nil, "", nil, nil, ErrPortfolioBinding
		}
	}
	for index := range evidence {
		if index > 0 && evidence[index-1].EvidenceID == evidence[index].EvidenceID {
			return nil, "", nil, nil, ErrPortfolioBinding
		}
	}
	manifest := portfolioManifest{SchemaVersion: 1, Project: portfolioProjectBinding{ProjectID: command.ProjectID, Version: command.ProjectVersion}, Workspace: command.Workspace, ExportFormat: command.Format, Artifacts: artifacts, Evidence: evidence}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nil, "", nil, nil, err
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), artifacts, evidence, nil
}

func validPortfolioExport(command RequestPortfolioExportCommand) bool {
	if command.ExportID == "" || command.RequestID == "" || len(command.RequestID) > 256 || command.TenantID == "" || command.UserID == "" || command.ProjectID == "" || command.ProjectVersion < 1 || command.CorrelationID == "" || command.Workspace.BindingID == "" || command.Workspace.Version < 1 || command.Workspace.Revision == "" || command.Workspace.ManifestHash == "" || command.Format != "html" && command.Format != "pdf" && command.Format != "zip" || len(command.Artifacts) < 1 || len(command.Artifacts) > 100 || len(command.Evidence) < 1 || len(command.Evidence) > 500 || !validJSONObject(command.Actor) || !validPointer(command.RequestedEvent) {
		return false
	}
	for _, binding := range command.Artifacts {
		if binding.RevisionID == "" || binding.ArtifactID == "" || binding.Revision < 1 || binding.ContentHash == "" || binding.ObjectVersion == "" || binding.WorkspaceRevision == "" || binding.MediaType == "" || binding.ByteSize < 1 || binding.ByteSize > 1<<30 || binding.ScanResultHash == "" || binding.EvidenceManifestHash == "" {
			return false
		}
	}
	for _, binding := range command.Evidence {
		if binding.EvidenceID == "" || binding.Version < 1 || binding.ContentHash == "" {
			return false
		}
	}
	run := command.Run
	if run.RunID == "" || run.TenantID != command.TenantID || run.UserID != command.UserID || run.CorrelationID != command.CorrelationID || run.QueueClass != "background" || run.BehaviorProfile != "artifact_builder" || run.BehaviorEnvironment != "production" && run.BehaviorEnvironment != "staging" {
		return false
	}
	if (command.Conversation == nil) != (command.MessageRun == nil) {
		return false
	}
	if command.Conversation == nil {
		return true
	}
	conversation, message := *command.Conversation, *command.MessageRun
	return conversation.ConversationID == run.ConversationID && conversation.TenantID == command.TenantID && conversation.UserID == command.UserID && conversation.MissionID != "" && conversation.ProjectID == command.ProjectID && conversation.ProjectVersion == command.ProjectVersion && conversation.MilestoneID == "" && conversation.MilestoneVersion == 0 && conversation.WorkspaceRevision == command.Workspace.Revision && conversation.Mode == "project" && conversation.CorrelationID == command.CorrelationID &&
		reflect.DeepEqual(message.Run, run) && message.ExpectedConversationVersion == 1 && message.ExpectedConversationMode == "project" && message.ExpectedConversationProfile == "artifact_builder" && message.CoachContext == nil
}

func (store PortfolioExportStore) portfolioEventIDs(exportID string) (portfolioEventIDs, error) {
	values := make([]string, 3)
	for index, part := range []string{"event", "outbox", "publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, "portfolio-export-requested:"+part, exportID)
		if err != nil {
			return portfolioEventIDs{}, err
		}
		values[index] = value
	}
	return portfolioEventIDs{values[0], values[1], values[2]}, nil
}

func (store PortfolioExportStore) valid() bool {
	return store.Pool != nil && store.Epochs != nil && store.StoreEpoch != "" && len(store.IDKey) >= 32 && store.Behavior != nil
}

func (store PortfolioExportStore) requireEpoch(ctx context.Context) error {
	current, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || current == "" {
		return ErrConfiguration
	}
	if current != store.StoreEpoch {
		return ErrStaleEpoch
	}
	return nil
}

func (store PortfolioExportStore) now() time.Time {
	now := time.Now().UTC()
	if store.Now != nil {
		now = store.Now().UTC()
	}
	return now.Truncate(time.Microsecond)
}
