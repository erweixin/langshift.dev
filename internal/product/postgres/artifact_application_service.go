package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/idempotency"
	idempotencypostgres "github.com/langshift/lites/internal/idempotency/postgres"
	"github.com/langshift/lites/internal/objectstore/s3store"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
)

const (
	artifactCreateOperation   = "artifacts.create.v2"
	artifactRevisionOperation = "artifacts.revise.v2"
	artifactResponseClass     = "artifact-idempotency"
	artifactResponseType      = "application/vnd.lites.artifact.v2+json"
)

type ArtifactApplicationService struct {
	Pool                                             *pgxpool.Pool
	Store                                            ArtifactStore
	Payloads                                         payload.Store
	Objects                                          s3store.Store
	IDKey, IdempotencyKeyPepper, RequestDigestPepper []byte
	IdempotencyTTL                                   time.Duration
	Now                                              func() time.Time
}

type artifactPrepared struct {
	input      idempotencypostgres.Input
	descriptor payload.Descriptor
	recordID   string
}

func (service ArtifactApplicationService) Create(ctx context.Context, command productapi.CreateArtifactCommand) (productapi.ArtifactMutationResult, error) {
	if !service.valid() || !validProjectMetadata(command.CommandMetadata) || command.ProjectID == "" || !validProjectKind(command.ArtifactKind) || !validProjectText(command.Title, 200) {
		return productapi.ArtifactMutationResult{}, productapi.ErrValidation
	}
	canonical, err := json.Marshal(struct {
		RequestID    string `json:"request_id"`
		ProjectID    string `json:"project_id"`
		ArtifactKind string `json:"artifact_kind"`
		Title        string `json:"title"`
	}{command.ClientRequestID, command.ProjectID, command.ArtifactKind, command.Title})
	if err != nil {
		return productapi.ArtifactMutationResult{}, service.mapError(err)
	}
	prepared, err := service.prepare(command.CommandMetadata, artifactCreateOperation, canonical)
	if err != nil {
		return productapi.ArtifactMutationResult{}, service.mapError(err)
	}
	executor := service.executor()
	if response, found, loadErr := executor.LoadCompleted(ctx, prepared.input); loadErr != nil {
		return productapi.ArtifactMutationResult{}, service.mapError(loadErr)
	} else if found {
		result, readErr := readArtifactResponse[productapi.ArtifactMutationResult](ctx, service.Payloads, prepared.descriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	artifactID, err := ids.DeterministicUUID(service.IDKey, "artifact-create", prepared.recordID)
	if err != nil {
		return productapi.ArtifactMutationResult{}, service.mapError(err)
	}
	response, replayed, err := executor.Execute(ctx, prepared.input, func(ctx context.Context, _ pgx.Tx) (idempotency.Response, error) {
		eventIDs, innerErr := service.Store.eventIDs("artifact-created", artifactID)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		event, innerErr := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventIDs.event, Class: projectEventClass, ContentType: "application/json"}, map[string]any{"subject_id": artifactID, "subject_version": 1, "project_id": command.ProjectID, "artifact_kind": command.ArtifactKind, "title": command.Title})
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		stored, innerErr := service.Store.CreateArtifact(ctx, CreateArtifactCommand{ArtifactID: artifactID, TenantID: command.TenantID, UserID: command.UserID, ProjectID: command.ProjectID, ArtifactKind: command.ArtifactKind, Title: command.Title, CorrelationID: command.RequestID, Actor: missionActor(command.UserID, command.SessionID), CreatedEvent: PayloadPointer{Ref: event.Ref, Hash: event.Hash}})
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		result := productapi.ArtifactMutationResult{ID: stored.ArtifactID, Version: stored.Version, Status: stored.Status, CurrentRevision: stored.CurrentRevision, UpdatedAt: stored.UpdatedAt, Replayed: stored.Replayed}
		manifest, innerErr := service.putJSON(ctx, prepared.descriptor, result)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: artifactResponseType, PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: result.Version}, nil
	})
	if err != nil {
		return productapi.ArtifactMutationResult{}, service.mapError(err)
	}
	result, err := readArtifactResponse[productapi.ArtifactMutationResult](ctx, service.Payloads, prepared.descriptor, response)
	result.Replayed = result.Replayed || replayed
	return result, service.mapError(err)
}

func (service ArtifactApplicationService) CreateRevision(ctx context.Context, command productapi.CreateArtifactRevisionCommand) (productapi.ArtifactRevisionResult, error) {
	if !service.valid() || !validProjectMetadata(command.CommandMetadata) || command.ArtifactID == "" || command.ExpectedArtifactVersion < 1 || command.Content == "" || len(command.Content) > 4<<20 || !utf8.ValidString(command.Content) || command.MediaType != "text/plain" && command.MediaType != "text/markdown" && command.MediaType != "application/json" || command.MediaType == "application/json" && !json.Valid([]byte(command.Content)) || command.WorkspaceRevision == "" || len(command.WorkspaceRevision) > 500 || len(command.EvidenceIDs) < 1 || len(command.EvidenceIDs) > 100 {
		return productapi.ArtifactRevisionResult{}, productapi.ErrValidation
	}
	evidenceIDs := append([]string(nil), command.EvidenceIDs...)
	sort.Strings(evidenceIDs)
	for index, id := range evidenceIDs {
		if id == "" || index > 0 && evidenceIDs[index-1] == id {
			return productapi.ArtifactRevisionResult{}, productapi.ErrValidation
		}
	}
	canonical, err := json.Marshal(struct {
		RequestID               string   `json:"request_id"`
		ArtifactID              string   `json:"artifact_id"`
		Content                 string   `json:"content"`
		MediaType               string   `json:"media_type"`
		WorkspaceRevision       string   `json:"workspace_revision"`
		EvidenceIDs             []string `json:"evidence_ids"`
		ExpectedArtifactVersion uint64   `json:"expected_artifact_version"`
	}{command.ClientRequestID, command.ArtifactID, command.Content, command.MediaType, command.WorkspaceRevision, evidenceIDs, command.ExpectedArtifactVersion})
	if err != nil {
		return productapi.ArtifactRevisionResult{}, service.mapError(err)
	}
	prepared, err := service.prepare(command.CommandMetadata, artifactRevisionOperation, canonical)
	if err != nil {
		return productapi.ArtifactRevisionResult{}, service.mapError(err)
	}
	executor := service.executor()
	if response, found, loadErr := executor.LoadCompleted(ctx, prepared.input); loadErr != nil {
		return productapi.ArtifactRevisionResult{}, service.mapError(loadErr)
	} else if found {
		result, readErr := readArtifactResponse[productapi.ArtifactRevisionResult](ctx, service.Payloads, prepared.descriptor, response)
		result.Replayed = readErr == nil
		return result, service.mapError(readErr)
	}
	bindings, err := service.resolveRevisionBindings(ctx, command.TenantID, command.UserID, command.ArtifactID, command.WorkspaceRevision, evidenceIDs)
	if err != nil {
		return productapi.ArtifactRevisionResult{}, service.mapError(err)
	}
	revisionID, err := ids.DeterministicUUID(service.IDKey, "artifact-revision", prepared.recordID)
	if err != nil {
		return productapi.ArtifactRevisionResult{}, service.mapError(err)
	}
	content := []byte(command.Content)
	contentDigest := sha256.Sum256(content)
	contentHash := hex.EncodeToString(contentDigest[:])
	scanDocument, _ := json.Marshal(map[string]any{"schema_version": 1, "scanner": "lites_inline_text_v1", "result": "passed", "content_hash": contentHash, "media_type": command.MediaType})
	scanDigest := sha256.Sum256(scanDocument)
	scanHash := hex.EncodeToString(scanDigest[:])
	response, replayed, err := executor.Execute(ctx, prepared.input, func(ctx context.Context, _ pgx.Tx) (idempotency.Response, error) {
		object, innerErr := service.Objects.PutScannedVersioned(ctx, command.TenantID+"/artifacts/"+command.ArtifactID+"/"+revisionID, command.MediaType, content, scanHash)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		eventIDs, innerErr := service.Store.eventIDs("artifact-revision-created", revisionID)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		event, innerErr := service.putJSON(ctx, payload.Descriptor{TenantID: command.TenantID, ObjectID: eventIDs.event, Class: projectEventClass, ContentType: "application/json"}, map[string]any{"subject_id": command.ArtifactID, "subject_version": command.ExpectedArtifactVersion + 1, "revision_id": revisionID, "workspace_revision": command.WorkspaceRevision, "content_hash": object.ContentHash, "object_version": object.VersionID, "media_type": object.MediaType, "byte_size": object.ByteSize, "scan_result_hash": object.ScanResultHash, "evidence": bindings})
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		stored, innerErr := service.Store.AppendArtifactRevision(ctx, AppendArtifactRevisionCommand{ArtifactID: command.ArtifactID, RevisionID: revisionID, TenantID: command.TenantID, UserID: command.UserID, ExpectedArtifactVersion: command.ExpectedArtifactVersion, ContentHash: object.ContentHash, ObjectRef: object.Reference, ObjectVersion: object.VersionID, WorkspaceRevision: command.WorkspaceRevision, MediaType: object.MediaType, ByteSize: object.ByteSize, ScanResultHash: object.ScanResultHash, CorrelationID: command.RequestID, Evidence: bindings, Actor: missionActor(command.UserID, command.SessionID), RevisionCreatedEvent: PayloadPointer{Ref: event.Ref, Hash: event.Hash}})
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		result := productapi.ArtifactRevisionResult{ID: stored.RevisionID, ArtifactID: stored.ArtifactID, ArtifactVersion: stored.ArtifactVersion, Revision: stored.Revision, Status: stored.Status, ContentHash: stored.ContentHash, EvidenceManifestHash: stored.EvidenceManifestHash, UpdatedAt: stored.UpdatedAt, Replayed: stored.Replayed}
		manifest, innerErr := service.putJSON(ctx, prepared.descriptor, result)
		if innerErr != nil {
			return idempotency.Response{}, innerErr
		}
		return idempotency.Response{Status: http.StatusOK, ContentType: artifactResponseType, PayloadRef: manifest.Ref, Hash: manifest.Hash, ResourceVersion: result.ArtifactVersion}, nil
	})
	if err != nil {
		return productapi.ArtifactRevisionResult{}, service.mapError(err)
	}
	result, err := readArtifactResponse[productapi.ArtifactRevisionResult](ctx, service.Payloads, prepared.descriptor, response)
	result.Replayed = result.Replayed || replayed
	return result, service.mapError(err)
}

func (service ArtifactApplicationService) resolveRevisionBindings(ctx context.Context, tenantID, userID, artifactID, workspaceRevision string, evidenceIDs []string) ([]EvidenceBinding, error) {
	tx, err := service.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return nil, err
	}
	var missionID, headRevision string
	err = tx.QueryRow(ctx, `SELECT p.mission_id::text,w.head_revision FROM product.artifacts a JOIN product.projects p ON p.tenant_id=a.tenant_id AND p.id=a.project_id JOIN product.project_workspace_bindings w ON w.tenant_id=p.tenant_id AND w.project_id=p.id WHERE a.tenant_id=$1 AND a.user_id=$2 AND a.id::text=$3 AND p.user_id=$2 AND p.status IN ('active','blocked')`, tenantID, userID, artifactID).Scan(&missionID, &headRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, productapi.ErrResourceNotFound
	}
	if err != nil {
		return nil, err
	}
	if headRevision != workspaceRevision {
		return nil, ErrEvidenceBinding
	}
	bindings := make([]EvidenceBinding, 0, len(evidenceIDs))
	for _, evidenceID := range evidenceIDs {
		var binding EvidenceBinding
		var evidenceMission, status string
		err = tx.QueryRow(ctx, `SELECT id::text,version,content_hash,mission_id::text,status FROM product.evidence WHERE tenant_id=$1 AND user_id=$2 AND id::text=$3`, tenantID, userID, evidenceID).Scan(&binding.EvidenceID, &binding.Version, &binding.ContentHash, &evidenceMission, &status)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (evidenceMission != missionID || status != "recorded" && status != "verified") {
			return nil, ErrEvidenceBinding
		}
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return bindings, nil
}

func (service ArtifactApplicationService) prepare(metadata productapi.CommandMetadata, operation string, canonical []byte) (artifactPrepared, error) {
	requestHash, err := idempotency.RequestDigest(canonical, service.RequestDigestPepper)
	if err != nil {
		return artifactPrepared{}, err
	}
	recordID, err := ids.DeterministicUUID(service.IDKey, "product-idempotency:"+operation, metadata.TenantID+"\x00"+metadata.UserID+"\x00"+metadata.IdempotencyKey)
	if err != nil {
		return artifactPrepared{}, err
	}
	return artifactPrepared{input: idempotencypostgres.Input{RecordID: recordID, Scope: idempotency.Scope{TenantID: metadata.TenantID, UserID: metadata.UserID, OperationID: operation}, RawKey: metadata.IdempotencyKey, RequestHash: requestHash, RequestID: metadata.RequestID}, descriptor: payload.Descriptor{TenantID: metadata.TenantID, ObjectID: recordID, Class: artifactResponseClass, ContentType: "application/json"}, recordID: recordID}, nil
}

func (service ArtifactApplicationService) executor() idempotencypostgres.Executor {
	return idempotencypostgres.Executor{Pool: service.Pool, KeyPepper: service.IdempotencyKeyPepper, TTL: service.IdempotencyTTL, Now: service.Now}
}

func (service ArtifactApplicationService) putJSON(ctx context.Context, descriptor payload.Descriptor, value any) (payload.Manifest, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return payload.Manifest{}, err
	}
	return service.Payloads.Put(ctx, descriptor, encoded)
}

func readArtifactResponse[T any](ctx context.Context, store payload.Store, descriptor payload.Descriptor, response idempotency.Response) (T, error) {
	var result T
	if response.PayloadRef == "" || response.Hash == "" {
		return result, payload.ErrIntegrity
	}
	encoded, err := store.Get(ctx, descriptor, payload.Manifest{Ref: response.PayloadRef, Hash: response.Hash})
	if err != nil || json.Unmarshal(encoded, &result) != nil {
		return result, payload.ErrIntegrity
	}
	return result, nil
}

func (service ArtifactApplicationService) valid() bool {
	return service.Pool != nil && service.Store.Pool == service.Pool && service.Payloads != nil && service.Objects.Client != nil && service.Objects.Bucket != "" && len(service.IDKey) >= 32 && len(service.IdempotencyKeyPepper) >= 32 && len(service.RequestDigestPepper) >= 32 && service.IdempotencyTTL > 0
}

func (service ArtifactApplicationService) mapError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, productapi.ErrResourceNotFound), errors.Is(err, pgx.ErrNoRows):
		return productapi.ErrResourceNotFound
	case errors.Is(err, idempotency.ErrKeyConflict), errors.Is(err, idempotency.ErrInProgress):
		return productapi.ErrIdempotencyConflict
	case errors.Is(err, ErrInvalidCommand):
		return productapi.ErrValidation
	case errors.Is(err, ErrArtifactConflict), errors.Is(err, ErrArtifactNotWritable), errors.Is(err, ErrArtifactVersion), errors.Is(err, ErrEvidenceBinding):
		return productapi.ErrStateConflict
	default:
		return errors.Join(productapi.ErrDependencyUnavailable, err)
	}
}

var _ productapi.ArtifactService = ArtifactApplicationService{}
