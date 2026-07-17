//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/langshift/lites/internal/objectstore/s3store"
	productapi "github.com/langshift/lites/internal/product/api"
)

type artifactObjectMemory struct {
	mu       sync.Mutex
	objects  map[string][]byte
	media    map[string]string
	metadata map[string]map[string]string
}

func newArtifactObjectMemory() *artifactObjectMemory {
	return &artifactObjectMemory{objects: map[string][]byte{}, media: map[string]string{}, metadata: map[string]map[string]string{}}
}

func (store *artifactObjectMemory) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, nil
}

func (store *artifactObjectMemory) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := aws.ToString(input.Key)
	if _, found := store.objects[key]; found {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "immutable object exists"}
	}
	value, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	store.objects[key] = append([]byte(nil), value...)
	store.media[key] = aws.ToString(input.ContentType)
	store.metadata[key] = input.Metadata
	return &s3.PutObjectOutput{VersionId: aws.String("version-1")}, nil
}

func (store *artifactObjectMemory) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := aws.ToString(input.Key)
	value, found := store.objects[key]
	if !found {
		return nil, &smithy.GenericAPIError{Code: "NotFound", Message: "missing"}
	}
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(value))), ContentType: aws.String(store.media[key]), Metadata: store.metadata[key], VersionId: aws.String("version-1")}, nil
}

func (store *artifactObjectMemory) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, found := store.objects[aws.ToString(input.Key)]
	if !found {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey", Message: "missing"}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(value)), ContentLength: aws.Int64(int64(len(value))), Metadata: store.metadata[aws.ToString(input.Key)]}, nil
}

func (store *artifactObjectMemory) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.objects, aws.ToString(input.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func TestArtifactApplicationServicePersistsScannedExactRevisionAndReplays(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	const (
		userID           = "e4000000-0000-4000-8000-000000000001"
		tenantID         = "e4000000-0000-4000-8000-000000000002"
		roleID           = "e4000000-0000-4000-8000-000000000003"
		missionID        = "e4000000-0000-4000-8000-000000000004"
		routeID          = "e4000000-0000-4000-8000-000000000005"
		projectID        = "e4000000-0000-4000-8000-000000000006"
		evidenceID       = "e4000000-0000-4000-8000-000000000007"
		workspaceID      = "e4000000-0000-4000-8000-000000000008"
		bindingID        = "e4000000-0000-4000-8000-000000000009"
		projectEventID   = "e4000000-0000-4000-8000-000000000010"
		workspaceEventID = "e4000000-0000-4000-8000-000000000011"
		epoch            = "e4000000-0000-4000-8000-000000000012"
		sessionID        = "e4000000-0000-4000-8000-000000000013"
		requestID        = "e4000000-0000-4000-8000-000000000014"
	)
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'artifact-app-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Artifact App Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'artifact-app-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,1,'claims-e4')`, []any{missionID, tenantID, userID, roleID}},
		{`INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,accepted_at) VALUES($1,$2,$3,$4,1,'accepted','claims-e4',0,'{}','encrypted://route','route@e4','ontology@e4','content@e4',$5)`, []any{routeID, tenantID, userID, missionID, now}},
		{`UPDATE product.missions SET current_route_revision_id=$1 WHERE id=$2`, []any{routeID, missionID}},
		{`INSERT INTO product.evidence(id,tenant_id,user_id,mission_id,evidence_type,status,source_kind,payload_ref,content_hash,recorded_at) VALUES($1,$2,$3,$4,'project','verified','project','encrypted://evidence','evidence-hash-e4',$5)`, []any{evidenceID, tenantID, userID, missionID, now}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	seedActiveProject(t, ctx, admin, activeProjectFixture{ProjectID: projectID, TenantID: tenantID, UserID: userID, MissionID: missionID, RouteID: routeID, ProjectEventID: projectEventID, StoreEpoch: epoch, CorrelationID: routeID, WorkspaceID: workspaceID, BindingID: bindingID, WorkspaceEventID: workspaceEventID, At: now})

	key := bytes.Repeat([]byte{0xe4}, 32)
	payloads := &missionPayloadStore{values: map[string][]byte{}}
	objects := newArtifactObjectMemory()
	store := ArtifactStore{Pool: pool, Appender: eventAppender(now), IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return now }}
	service := ArtifactApplicationService{Pool: pool, Store: store, Payloads: payloads, Objects: s3store.Store{Client: objects, Bucket: "artifact-test-bucket", Prefix: "commercial", MaxBytes: 4 << 20, ServerSideEncryption: types.ServerSideEncryptionAes256, RequireDigestMetadata: true}, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xe5}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xe6}, 32), IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	metadata := productapi.CommandMetadata{RequestID: requestID, ClientRequestID: "artifact-create-client-e4", IdempotencyKey: "artifact-create-idempotency-e4", TenantID: tenantID, UserID: userID, SessionID: sessionID}
	created, err := service.Create(ctx, productapi.CreateArtifactCommand{CommandMetadata: metadata, ProjectID: projectID, ArtifactKind: "writing", Title: "Exact production artifact"})
	if err != nil || created.Version != 1 || created.Status != "draft" || created.Replayed {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	revisionMetadata := metadata
	revisionMetadata.ClientRequestID = "artifact-revision-client-e4"
	revisionMetadata.IdempotencyKey = "artifact-revision-idempotency-e4"
	revisionCommand := productapi.CreateArtifactRevisionCommand{CommandMetadata: revisionMetadata, ArtifactID: created.ID, Content: "# Exact revision\n\nObserved evidence and failure boundary.", MediaType: "text/markdown", WorkspaceRevision: "git:head", EvidenceIDs: []string{evidenceID}, ExpectedArtifactVersion: 1}
	revision, err := service.CreateRevision(ctx, revisionCommand)
	if err != nil || revision.ArtifactVersion != 2 || revision.Revision != 1 || revision.Status != "ready" || len(revision.ContentHash) != 64 || len(revision.EvidenceManifestHash) != 64 || revision.Replayed {
		t.Fatalf("revision=%#v err=%v", revision, err)
	}
	revisionCommand.RequestID = "e4000000-0000-4000-8000-000000000015"
	replayed, err := service.CreateRevision(ctx, revisionCommand)
	if err != nil || !replayed.Replayed || replayed.ID != revision.ID || replayed.ContentHash != revision.ContentHash {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	substitution := revisionCommand
	substitution.Content += " substituted"
	if _, err = service.CreateRevision(ctx, substitution); !errors.Is(err, productapi.ErrIdempotencyConflict) {
		t.Fatalf("substitution err=%v", err)
	}
	var artifacts, revisions, evidenceLinks, completedResponses int
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM product.artifacts WHERE tenant_id=$1 AND user_id=$2),(SELECT count(*) FROM product.artifact_revisions WHERE tenant_id=$1),(SELECT count(*) FROM product.artifact_revision_evidence WHERE tenant_id=$1),(SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id IN ('artifacts.create.v2','artifacts.revise.v2') AND status='completed')`, tenantID, userID).Scan(&artifacts, &revisions, &evidenceLinks, &completedResponses); err != nil || artifacts != 1 || revisions != 1 || evidenceLinks != 1 || completedResponses != 2 {
		t.Fatalf("rows artifacts=%d revisions=%d links=%d responses=%d err=%v", artifacts, revisions, evidenceLinks, completedResponses, err)
	}
	objects.mu.Lock()
	objectCount := len(objects.objects)
	objects.mu.Unlock()
	if objectCount != 1 {
		t.Fatalf("immutable objects=%d", objectCount)
	}
}
