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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
)

var (
	ErrConfiguration       = errors.New("artifact store is not configured")
	ErrInvalidCommand      = errors.New("artifact command is invalid")
	ErrStaleEpoch          = errors.New("artifact command store epoch is stale")
	ErrArtifactConflict    = errors.New("artifact conflicts with durable state")
	ErrArtifactNotWritable = errors.New("artifact is not writable")
	ErrArtifactVersion     = errors.New("artifact version does not match")
	ErrEvidenceBinding     = errors.New("artifact evidence binding is invalid")
)

type EpochAuthority interface {
	CurrentStoreEpoch(context.Context) (string, error)
}

type PayloadPointer struct{ Ref, Hash string }

type ArtifactStore struct {
	Pool       *pgxpool.Pool
	Appender   eventpostgres.Appender
	IDKey      []byte
	StoreEpoch string
	Epochs     EpochAuthority
	Now        func() time.Time
}

type CreateArtifactCommand struct {
	ArtifactID, TenantID, UserID, ProjectID string
	ArtifactKind, Title, CorrelationID      string
	Actor                                   json.RawMessage
	CreatedEvent                            PayloadPointer
}

type Artifact struct {
	ArtifactID, Status string
	Version            uint64
	CurrentRevision    int
	UpdatedAt          time.Time
	Replayed           bool
}

type EvidenceBinding struct {
	EvidenceID  string `json:"evidence_id"`
	Version     uint64 `json:"version"`
	ContentHash string `json:"content_hash"`
}

type AppendArtifactRevisionCommand struct {
	ArtifactID, RevisionID, TenantID, UserID string
	ExpectedArtifactVersion                  uint64
	ContentHash, ObjectRef, ObjectVersion    string
	WorkspaceRevision, MediaType             string
	ByteSize                                 int64
	ScanResultHash, CorrelationID            string
	Evidence                                 []EvidenceBinding
	Actor                                    json.RawMessage
	RevisionCreatedEvent                     PayloadPointer
}

type ArtifactRevision struct {
	ArtifactID, RevisionID, Status, ContentHash string
	ArtifactVersion                             uint64
	Revision                                    int
	EvidenceManifestHash                        string
	UpdatedAt                                   time.Time
	Replayed                                    bool
}

type artifactEventIDs struct{ event, outbox, publish string }

func (store ArtifactStore) CreateArtifact(ctx context.Context, command CreateArtifactCommand) (Artifact, error) {
	if !store.valid() {
		return Artifact{}, ErrConfiguration
	}
	if !validCreateArtifact(command) {
		return Artifact{}, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return Artifact{}, err
	}
	eventIDs, err := store.eventIDs("artifact-created", command.ArtifactID)
	if err != nil {
		return Artifact{}, err
	}
	now := store.now()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Artifact{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return Artifact{}, err
	}
	if replay, found, replayErr := store.loadArtifactReplay(ctx, tx, command, eventIDs.event); replayErr != nil {
		return Artifact{}, replayErr
	} else if found {
		if err = tx.Commit(ctx); err != nil {
			return Artifact{}, err
		}
		return replay, nil
	}
	var projectUser, projectStatus string
	err = tx.QueryRow(ctx, `SELECT user_id::text,status FROM product.projects WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.ProjectID).Scan(&projectUser, &projectStatus)
	if err != nil || projectUser != command.UserID || projectStatus != "active" && projectStatus != "blocked" {
		return Artifact{}, ErrArtifactNotWritable
	}
	if _, err = tx.Exec(ctx, `INSERT INTO product.artifacts(id,tenant_id,user_id,project_id,artifact_kind,title,status,current_revision,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'draft',0,$7,$7)`, command.ArtifactID, command.TenantID, command.UserID, command.ProjectID, command.ArtifactKind, command.Title, now); err != nil {
		if constraintViolation(err) {
			return Artifact{}, ErrArtifactConflict
		}
		return Artifact{}, err
	}
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.event, TenantID: command.TenantID, UserID: command.UserID, EventType: "ArtifactCreated", SchemaVersion: 1, AggregateKind: "artifact", AggregateID: command.ArtifactID, AggregateVersion: 1, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.CreatedEvent.Ref, PayloadHash: command.CreatedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.outbox, CommandID: eventIDs.publish, CommandType: "events.publish", PayloadRef: command.CreatedEvent.Ref, PayloadHash: command.CreatedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return Artifact{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Artifact{}, err
	}
	return Artifact{ArtifactID: command.ArtifactID, Status: "draft", Version: 1, UpdatedAt: now}, nil
}

func (store ArtifactStore) AppendArtifactRevision(ctx context.Context, command AppendArtifactRevisionCommand) (ArtifactRevision, error) {
	if !store.valid() {
		return ArtifactRevision{}, ErrConfiguration
	}
	if !validAppendRevision(command) {
		return ArtifactRevision{}, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return ArtifactRevision{}, err
	}
	eventIDs, err := store.eventIDs("artifact-revision-created", command.RevisionID)
	if err != nil {
		return ArtifactRevision{}, err
	}
	now := store.now()
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return ArtifactRevision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, command.TenantID); err != nil {
		return ArtifactRevision{}, err
	}
	if replay, found, replayErr := store.loadRevisionReplay(ctx, tx, command, eventIDs.event); replayErr != nil {
		return ArtifactRevision{}, replayErr
	} else if found {
		if err = tx.Commit(ctx); err != nil {
			return ArtifactRevision{}, err
		}
		return replay, nil
	}
	var projectID, missionID, artifactStatus string
	var artifactVersion uint64
	var currentRevision int
	err = tx.QueryRow(ctx, `SELECT a.project_id::text,p.mission_id::text,a.status,a.version,a.current_revision FROM product.artifacts a JOIN product.projects p ON p.tenant_id=a.tenant_id AND p.id=a.project_id WHERE a.tenant_id=$1 AND a.id=$2 AND a.user_id=$3 AND p.user_id=$3 AND p.status IN ('active','blocked') FOR UPDATE OF a,p`, command.TenantID, command.ArtifactID, command.UserID).Scan(&projectID, &missionID, &artifactStatus, &artifactVersion, &currentRevision)
	if err != nil || artifactStatus != "draft" && artifactStatus != "ready" {
		return ArtifactRevision{}, ErrArtifactNotWritable
	}
	if artifactVersion != command.ExpectedArtifactVersion {
		return ArtifactRevision{}, ErrArtifactVersion
	}
	bindings := append([]EvidenceBinding(nil), command.Evidence...)
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].EvidenceID < bindings[j].EvidenceID })
	for index, binding := range bindings {
		if index > 0 && bindings[index-1].EvidenceID == binding.EvidenceID {
			return ArtifactRevision{}, ErrEvidenceBinding
		}
		var evidenceMission, status, contentHash string
		var version uint64
		err = tx.QueryRow(ctx, `SELECT mission_id::text,status,version,content_hash FROM product.evidence WHERE tenant_id=$1 AND id=$2 AND user_id=$3 FOR KEY SHARE`, command.TenantID, binding.EvidenceID, command.UserID).Scan(&evidenceMission, &status, &version, &contentHash)
		if err != nil || evidenceMission != missionID || status != "recorded" && status != "verified" || version != binding.Version || contentHash != binding.ContentHash {
			return ArtifactRevision{}, ErrEvidenceBinding
		}
	}
	revision := currentRevision + 1
	manifest, manifestHash, err := artifactEvidenceManifest(command.ArtifactID, revision, command.WorkspaceRevision, bindings)
	if err != nil {
		return ArtifactRevision{}, err
	}
	nextArtifactVersion := artifactVersion + 1
	if _, err = tx.Exec(ctx, `INSERT INTO product.artifact_revisions(id,tenant_id,user_id,artifact_id,project_id,revision,content_hash,object_ref,object_version,workspace_revision,media_type,byte_size,scan_status,scan_result_hash,evidence_manifest,evidence_manifest_hash,created_event_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'passed',$13,$14,$15,$16,$17,$17)`, command.RevisionID, command.TenantID, command.UserID, command.ArtifactID, projectID, revision, command.ContentHash, command.ObjectRef, command.ObjectVersion, command.WorkspaceRevision, command.MediaType, command.ByteSize, command.ScanResultHash, manifest, manifestHash, eventIDs.event, now); err != nil {
		if constraintViolation(err) {
			return ArtifactRevision{}, ErrArtifactConflict
		}
		return ArtifactRevision{}, err
	}
	for ordinal, binding := range bindings {
		if _, err = tx.Exec(ctx, `INSERT INTO product.artifact_revision_evidence(tenant_id,artifact_revision_id,evidence_id,evidence_version,evidence_content_hash,ordinal,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, command.TenantID, command.RevisionID, binding.EvidenceID, binding.Version, binding.ContentHash, ordinal, now); err != nil {
			return ArtifactRevision{}, ErrEvidenceBinding
		}
	}
	if tag, updateErr := tx.Exec(ctx, `UPDATE product.artifacts SET version=$1,status='ready',current_revision=$2,current_revision_id=$3,current_content_hash=$4,updated_at=$5 WHERE tenant_id=$6 AND id=$7 AND user_id=$8 AND version=$9 AND status=$10 AND current_revision=$11`, nextArtifactVersion, revision, command.RevisionID, command.ContentHash, now, command.TenantID, command.ArtifactID, command.UserID, artifactVersion, artifactStatus, currentRevision); updateErr != nil || tag.RowsAffected() != 1 {
		return ArtifactRevision{}, ErrArtifactVersion
	}
	event := eventpostgres.Input{Event: eventpostgres.Event{ID: eventIDs.event, TenantID: command.TenantID, UserID: command.UserID, EventType: "ArtifactRevisionCreated", SchemaVersion: 1, AggregateKind: "artifact", AggregateID: command.ArtifactID, AggregateVersion: nextArtifactVersion, StoreEpoch: store.StoreEpoch, OccurredAt: now, Actor: command.Actor, CorrelationID: command.CorrelationID, PayloadRef: command.RevisionCreatedEvent.Ref, PayloadHash: command.RevisionCreatedEvent.Hash}, Commands: []eventpostgres.OutboxCommand{{ID: eventIDs.outbox, CommandID: eventIDs.publish, CommandType: "events.publish", PayloadRef: command.RevisionCreatedEvent.Ref, PayloadHash: command.RevisionCreatedEvent.Hash}}}
	if _, err = store.Appender.Append(ctx, tx, event); err != nil {
		return ArtifactRevision{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ArtifactRevision{}, err
	}
	return ArtifactRevision{ArtifactID: command.ArtifactID, RevisionID: command.RevisionID, Status: "ready", ContentHash: command.ContentHash, ArtifactVersion: nextArtifactVersion, Revision: revision, EvidenceManifestHash: manifestHash, UpdatedAt: now}, nil
}

func (store ArtifactStore) loadArtifactReplay(ctx context.Context, tx pgx.Tx, command CreateArtifactCommand, eventID string) (Artifact, bool, error) {
	var projectID, userID, kind, title string
	var createdAt time.Time
	err := tx.QueryRow(ctx, `SELECT project_id::text,user_id::text,artifact_kind,title,created_at FROM product.artifacts WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, command.TenantID, command.ArtifactID).Scan(&projectID, &userID, &kind, &title, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Artifact{}, false, nil
	}
	if err != nil || projectID != command.ProjectID || userID != command.UserID || kind != command.ArtifactKind || title != command.Title {
		return Artifact{}, false, ErrArtifactConflict
	}
	var exists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='ArtifactCreated' AND aggregate_kind='artifact' AND aggregate_id=$3 AND aggregate_version=1 AND store_epoch=$4 AND payload_ref=$5 AND payload_hash=$6)`, command.TenantID, eventID, command.ArtifactID, store.StoreEpoch, command.CreatedEvent.Ref, command.CreatedEvent.Hash).Scan(&exists)
	if err != nil || !exists {
		return Artifact{}, false, ErrArtifactConflict
	}
	return Artifact{ArtifactID: command.ArtifactID, Status: "draft", Version: 1, UpdatedAt: createdAt, Replayed: true}, true, nil
}

func (store ArtifactStore) loadRevisionReplay(ctx context.Context, tx pgx.Tx, command AppendArtifactRevisionCommand, eventID string) (ArtifactRevision, bool, error) {
	var actual ArtifactRevision
	var userID, objectRef, objectVersion, workspaceRevision, mediaType, scanResultHash, createdEventID string
	var byteSize int64
	var manifest json.RawMessage
	err := tx.QueryRow(ctx, `SELECT artifact_id::text,user_id::text,revision,content_hash,object_ref,object_version,workspace_revision,media_type,byte_size,scan_result_hash,evidence_manifest,evidence_manifest_hash,created_event_id::text,created_at FROM product.artifact_revisions WHERE tenant_id=$1 AND id=$2`, command.TenantID, command.RevisionID).Scan(&actual.ArtifactID, &userID, &actual.Revision, &actual.ContentHash, &objectRef, &objectVersion, &workspaceRevision, &mediaType, &byteSize, &scanResultHash, &manifest, &actual.EvidenceManifestHash, &createdEventID, &actual.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactRevision{}, false, nil
	}
	if err != nil || actual.ArtifactID != command.ArtifactID || userID != command.UserID || actual.ContentHash != command.ContentHash || objectRef != command.ObjectRef || objectVersion != command.ObjectVersion || workspaceRevision != command.WorkspaceRevision || mediaType != command.MediaType || byteSize != command.ByteSize || scanResultHash != command.ScanResultHash || createdEventID != eventID {
		return ArtifactRevision{}, false, ErrArtifactConflict
	}
	bindings := append([]EvidenceBinding(nil), command.Evidence...)
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].EvidenceID < bindings[j].EvidenceID })
	expectedManifest, expectedHash, err := artifactEvidenceManifest(command.ArtifactID, actual.Revision, command.WorkspaceRevision, bindings)
	var expectedValue, actualValue evidenceManifest
	if err != nil || json.Unmarshal(expectedManifest, &expectedValue) != nil || json.Unmarshal(manifest, &actualValue) != nil || expectedHash != actual.EvidenceManifestHash || !reflect.DeepEqual(expectedValue, actualValue) {
		return ArtifactRevision{}, false, ErrArtifactConflict
	}
	var linkCount int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM product.artifact_revision_evidence WHERE tenant_id=$1 AND artifact_revision_id=$2`, command.TenantID, command.RevisionID).Scan(&linkCount)
	if err != nil || linkCount != len(bindings) {
		return ArtifactRevision{}, false, ErrArtifactConflict
	}
	for ordinal, binding := range bindings {
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM product.artifact_revision_evidence WHERE tenant_id=$1 AND artifact_revision_id=$2 AND evidence_id=$3 AND evidence_version=$4 AND evidence_content_hash=$5 AND ordinal=$6)`, command.TenantID, command.RevisionID, binding.EvidenceID, binding.Version, binding.ContentHash, ordinal).Scan(&exists)
		if err != nil || !exists {
			return ArtifactRevision{}, false, ErrArtifactConflict
		}
	}
	actual.ArtifactVersion = command.ExpectedArtifactVersion + 1
	var eventExists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.events WHERE tenant_id=$1 AND id=$2 AND event_type='ArtifactRevisionCreated' AND aggregate_kind='artifact' AND aggregate_id=$3 AND aggregate_version=$4 AND store_epoch=$5 AND payload_ref=$6 AND payload_hash=$7)`, command.TenantID, eventID, command.ArtifactID, actual.ArtifactVersion, store.StoreEpoch, command.RevisionCreatedEvent.Ref, command.RevisionCreatedEvent.Hash).Scan(&eventExists)
	if err != nil || !eventExists {
		return ArtifactRevision{}, false, ErrArtifactConflict
	}
	actual.RevisionID, actual.Status, actual.Replayed = command.RevisionID, "ready", true
	return actual, true, nil
}

type evidenceManifest struct {
	SchemaVersion     int               `json:"schema_version"`
	ArtifactID        string            `json:"artifact_id"`
	ArtifactRevision  int               `json:"artifact_revision"`
	WorkspaceRevision string            `json:"workspace_revision"`
	Evidence          []EvidenceBinding `json:"evidence"`
}

func artifactEvidenceManifest(artifactID string, revision int, workspaceRevision string, bindings []EvidenceBinding) (json.RawMessage, string, error) {
	encoded, err := json.Marshal(evidenceManifest{SchemaVersion: 1, ArtifactID: artifactID, ArtifactRevision: revision, WorkspaceRevision: workspaceRevision, Evidence: bindings})
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}

func (store ArtifactStore) eventIDs(domain, seed string) (artifactEventIDs, error) {
	values := make([]string, 3)
	for index, part := range []string{"event", "outbox", "publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, domain+":"+part, seed)
		if err != nil {
			return artifactEventIDs{}, err
		}
		values[index] = value
	}
	return artifactEventIDs{values[0], values[1], values[2]}, nil
}

func (store ArtifactStore) valid() bool {
	return store.Pool != nil && store.Epochs != nil && store.StoreEpoch != "" && len(store.IDKey) >= 32
}

func (store ArtifactStore) requireEpoch(ctx context.Context) error {
	current, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || current == "" {
		return ErrConfiguration
	}
	if current != store.StoreEpoch {
		return ErrStaleEpoch
	}
	return nil
}

func (store ArtifactStore) now() time.Time {
	now := time.Now().UTC()
	if store.Now != nil {
		now = store.Now().UTC()
	}
	return now.Truncate(time.Microsecond)
}

func validCreateArtifact(command CreateArtifactCommand) bool {
	return command.ArtifactID != "" && command.TenantID != "" && command.UserID != "" && command.ProjectID != "" && command.ArtifactKind != "" && len(command.Title) >= 1 && len(command.Title) <= 200 && validJSONObject(command.Actor) && command.CorrelationID != "" && validPointer(command.CreatedEvent)
}

func validAppendRevision(command AppendArtifactRevisionCommand) bool {
	if command.ArtifactID == "" || command.RevisionID == "" || command.TenantID == "" || command.UserID == "" || command.ExpectedArtifactVersion < 1 || command.ContentHash == "" || command.ObjectRef == "" || command.ObjectVersion == "" || command.WorkspaceRevision == "" || command.MediaType == "" || command.ByteSize < 1 || command.ByteSize > 1<<30 || command.ScanResultHash == "" || len(command.Evidence) < 1 || len(command.Evidence) > 100 || !validJSONObject(command.Actor) || command.CorrelationID == "" || !validPointer(command.RevisionCreatedEvent) {
		return false
	}
	for _, binding := range command.Evidence {
		if binding.EvidenceID == "" || binding.Version < 1 || binding.ContentHash == "" {
			return false
		}
	}
	return true
}

func validPointer(pointer PayloadPointer) bool { return pointer.Ref != "" && pointer.Hash != "" }

func validJSONObject(value json.RawMessage) bool {
	if !json.Valid(value) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

func constraintViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && (postgresError.Code == "23505" || postgresError.Code == "23503" || postgresError.Code == "23514")
}
