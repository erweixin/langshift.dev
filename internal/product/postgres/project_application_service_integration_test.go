//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/langshift/lites/internal/payload"
	productapi "github.com/langshift/lites/internal/product/api"
)

type projectPayloadFailureStore struct {
	inner *missionPayloadStore
	fail  bool
}

func (store projectPayloadFailureStore) Put(ctx context.Context, descriptor payload.Descriptor, value []byte) (payload.Manifest, error) {
	if store.fail && descriptor.Class == projectResponseClass {
		return payload.Manifest{}, errors.New("injected project response failure")
	}
	return store.inner.Put(ctx, descriptor, value)
}

func (store projectPayloadFailureStore) Get(ctx context.Context, descriptor payload.Descriptor, manifest payload.Manifest) ([]byte, error) {
	return store.inner.Get(ctx, descriptor, manifest)
}

func TestProjectApplicationServiceAtomicallyCommitsIdempotencyProjectAndEvent(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 16, 18, 0, 0, 0, time.UTC)
	const (
		userID    = "d6000000-0000-4000-8000-000000000001"
		tenantID  = "d6000000-0000-4000-8000-000000000002"
		roleID    = "d6000000-0000-4000-8000-000000000003"
		missionID = "d6000000-0000-4000-8000-000000000004"
		routeID   = "d6000000-0000-4000-8000-000000000005"
		epoch     = "d6000000-0000-4000-8000-000000000006"
		sessionID = "d6000000-0000-4000-8000-000000000007"
		requestID = "d6000000-0000-4000-8000-000000000008"
	)
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'project-service-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Project Service Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'project-service-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'active',$4,1,'claims-d6')`, []any{missionID, tenantID, userID, roleID}},
		{`INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,accepted_at) VALUES($1,$2,$3,$4,1,'accepted','claims-d6',0,'{}','encrypted://route','route@d6','ontology@d6','content@d6',$5)`, []any{routeID, tenantID, userID, missionID, now}},
		{`UPDATE product.missions SET current_route_revision_id=$1 WHERE id=$2`, []any{routeID, missionID}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	key := bytes.Repeat([]byte{0xd6}, 32)
	values := &missionPayloadStore{values: map[string][]byte{}}
	store := ProjectStore{Pool: pool, Appender: eventAppender(now), IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return now }}
	service := func(payloads payload.Store) ProjectApplicationService {
		return ProjectApplicationService{Pool: pool, Store: store, Payloads: payloads, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xd7}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xd8}, 32), CursorKey: bytes.Repeat([]byte{0xd9}, 32), IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	}
	command := productapi.CreateProjectCommand{CommandMetadata: productapi.CommandMetadata{RequestID: requestID, ClientRequestID: "project-create-client-d6", IdempotencyKey: "project-create-idempotency-key-d6", TenantID: tenantID, UserID: userID, SessionID: sessionID}, MissionID: missionID, RouteRevisionID: routeID, ProjectKind: "writing", Title: "Atomic project", Brief: "Build and verify the complete production journey."}
	if _, err := service(projectPayloadFailureStore{inner: values, fail: true}).Create(ctx, command); !errors.Is(err, productapi.ErrDependencyUnavailable) {
		t.Fatalf("injected failure=%v", err)
	}
	var projects, events, responses int
	if err := admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM product.projects WHERE tenant_id=$1 AND user_id=$2),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND user_id=$2 AND aggregate_kind='project'),(SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id='projects.create.v2')`, tenantID, userID).Scan(&projects, &events, &responses); err != nil || projects != 0 || events != 0 || responses != 0 {
		t.Fatalf("failed transaction projects=%d events=%d responses=%d err=%v", projects, events, responses, err)
	}
	created, err := service(projectPayloadFailureStore{inner: values}).Create(ctx, command)
	if err != nil || created.Version != 1 || created.Status != "active" || created.Replayed {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	snapshot, err := service(projectPayloadFailureStore{inner: values}).Get(ctx, productapi.ProjectGetQuery{TenantID: tenantID, UserID: userID, ProjectID: created.ID})
	if err != nil || snapshot.Project.ID != created.ID || snapshot.Project.Version != created.Version || snapshot.Brief != command.Brief || snapshot.Workspace != nil || len(snapshot.Milestones) != 0 || len(snapshot.Artifacts) != 0 || snapshot.LatestExport != nil {
		t.Fatalf("partial recovery snapshot=%#v err=%v", snapshot, err)
	}
	if _, err = service(projectPayloadFailureStore{inner: values}).Get(ctx, productapi.ProjectGetQuery{TenantID: tenantID, UserID: "d6000000-0000-4000-8000-000000000099", ProjectID: created.ID}); !errors.Is(err, productapi.ErrResourceNotFound) {
		t.Fatalf("cross-owner recovery read=%v", err)
	}
	retry := command
	retry.RequestID = "d6000000-0000-4000-8000-000000000009"
	retry.SessionID = "d6000000-0000-4000-8000-000000000010"
	replayed, err := service(projectPayloadFailureStore{inner: values}).Create(ctx, retry)
	if err != nil || !replayed.Replayed || replayed.ID != created.ID || replayed.EventID != created.EventID {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	substitution := command
	substitution.Title = "Substituted title"
	if _, err = service(projectPayloadFailureStore{inner: values}).Create(ctx, substitution); !errors.Is(err, productapi.ErrIdempotencyConflict) {
		t.Fatalf("substitution error=%v", err)
	}
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM product.projects WHERE tenant_id=$1 AND user_id=$2),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND user_id=$2 AND aggregate_kind='project'),(SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id='projects.create.v2' AND status='completed')`, tenantID, userID).Scan(&projects, &events, &responses); err != nil || projects != 1 || events != 1 || responses != 1 {
		t.Fatalf("committed projects=%d events=%d responses=%d err=%v", projects, events, responses, err)
	}
}
