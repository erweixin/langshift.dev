//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
	productapi "github.com/langshift/lites/internal/product/api"
	"github.com/langshift/lites/internal/product/mission"
)

type missionPayloadStore struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (store *missionPayloadStore) Put(_ context.Context, descriptor payload.Descriptor, value []byte) (payload.Manifest, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	digest := sha256.Sum256(value)
	hash := hex.EncodeToString(digest[:])
	ref := descriptor.TenantID + "/" + descriptor.Class + "/" + descriptor.ObjectID + "/" + hash
	if existing, found := store.values[ref]; found && !bytes.Equal(existing, value) {
		return payload.Manifest{}, errors.New("immutable payload conflict")
	}
	store.values[ref] = append([]byte(nil), value...)
	return payload.Manifest{Ref: ref, Hash: hash}, nil
}

func (store *missionPayloadStore) Get(_ context.Context, _ payload.Descriptor, manifest payload.Manifest) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, found := store.values[manifest.Ref]
	if !found {
		return nil, errors.New("payload not found")
	}
	digest := sha256.Sum256(value)
	if hex.EncodeToString(digest[:]) != manifest.Hash {
		return nil, payload.ErrIntegrity
	}
	return append([]byte(nil), value...), nil
}

func TestMissionMutationServiceCommitsIdempotencyWithDomainEvents(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 16, 14, 0, 0, 0, time.UTC)
	userID := "d4000000-0000-4000-8000-000000000001"
	tenantID := "d4000000-0000-4000-8000-000000000002"
	roleID := "d4000000-0000-4000-8000-000000000003"
	missionID := "d4000000-0000-4000-8000-000000000004"
	epoch := "d4000000-0000-4000-8000-000000000005"
	routeID := "d4000000-0000-4000-8000-000000000008"
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'mission-service-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Mission Service Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'mission-service-role',1,'active','{}','en','{}')`, []any{roleID, tenantID}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'draft',$4,1,'claims-d4')`, []any{missionID, tenantID, userID, roleID}},
		{`INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,route_payload_hash,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,accepted_at) VALUES($1,$2,$3,$4,1,'accepted','claims-d4',0,'{}','encrypted://route-d4',$5,'route@1','ontology@1','content@1',$6)`, []any{routeID, tenantID, userID, missionID, strings.Repeat("d", 64), now}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(ctx, `UPDATE product.missions SET current_route_revision_id=$1 WHERE tenant_id=$2 AND id=$3`, routeID, tenantID, missionID); err != nil {
		t.Fatal(err)
	}
	payloads := &missionPayloadStore{values: map[string][]byte{}}
	key := bytes.Repeat([]byte{0xd4}, 32)
	store := MissionFocusStore{Pool: pool, Appender: eventAppender(now), IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return now }}
	service := MissionMutationService{Pool: pool, Store: store, Payloads: payloads, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xd5}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xd6}, 32), IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	command := productapi.ChangeMissionStatusCommand{CommandMetadata: productapi.CommandMetadata{RequestID: "d4000000-0000-4000-8000-000000000007", ClientRequestID: "client-request-d4", IdempotencyKey: "mission-service-idempotency-key-d4", TenantID: tenantID, UserID: userID, SessionID: "d4000000-0000-4000-8000-000000000006"}, MissionID: missionID, Status: mission.Active, ExpectedMissionVersion: 1, ExpectedFocusVersion: 0}
	created, err := service.ChangeStatus(ctx, command)
	if err != nil || created.Replayed || created.Mission.Version != 2 || created.Mission.Status != mission.Active || created.Focus.MissionID == nil || *created.Focus.MissionID != missionID || created.Focus.Version != 1 || len(created.EventIDs) != 2 {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	replayed, err := service.ChangeStatus(ctx, command)
	if err != nil || !replayed.Replayed || replayed.Mission.Version != 2 || replayed.Focus.Version != 1 {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	conflict := command
	conflict.Status = mission.Archived
	if _, err = service.ChangeStatus(ctx, conflict); !errors.Is(err, productapi.ErrIdempotencyConflict) {
		t.Fatalf("idempotency substitution error=%v", err)
	}
	var events, responses, dailyCommands int
	err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND user_id=$2),(SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id='missions.update.v2' AND status='completed'),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='GenerateDailyTask' AND aggregate_id=$3)`, tenantID, userID, missionID).Scan(&events, &responses, &dailyCommands)
	if err != nil || events != 2 || responses != 1 || dailyCommands != 1 {
		t.Fatalf("events=%d responses=%d daily=%d err=%v", events, responses, dailyCommands, err)
	}
}

func eventAppender(now time.Time) eventpostgres.Appender {
	return eventpostgres.Appender{Now: func() time.Time { return now }}
}
