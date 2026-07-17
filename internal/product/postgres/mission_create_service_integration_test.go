//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	productapi "github.com/langshift/lites/internal/product/api"
	"github.com/langshift/lites/internal/product/mission"
)

func TestMissionCreateServiceCommitsEncryptedManifestsEventAndIdempotency(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()
	now := time.Date(2026, time.July, 16, 17, 0, 0, 0, time.UTC)
	userID := "f5000000-0000-4000-8000-000000000001"
	tenantID := "f5000000-0000-4000-8000-000000000002"
	roleID := "f5000000-0000-4000-8000-000000000003"
	epoch := "f5000000-0000-4000-8000-000000000004"
	if _, err := admin.Exec(ctx, `DELETE FROM identity.tenants WHERE id=$1`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `DELETE FROM identity.users WHERE id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'mission-create-owner@example.invalid','en','active')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Mission Create Tenant','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'mission-create-role',1,'active','{}','en','{}')`, roleID, tenantID); err != nil {
		t.Fatal(err)
	}
	payloads := &missionPayloadStore{values: map[string][]byte{}}
	key := bytes.Repeat([]byte{0xf5}, 32)
	store := MissionFocusStore{Pool: pool, Appender: eventAppender(now), IDKey: key, StoreEpoch: epoch, Epochs: artifactEpochStub{epoch}, Now: func() time.Time { return now }}
	service := MissionMutationService{Pool: pool, Store: store, Payloads: payloads, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xf6}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xf7}, 32), IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	command := productapi.CreateMissionCommand{CommandMetadata: productapi.CommandMetadata{RequestID: "f5000000-0000-4000-8000-000000000005", ClientRequestID: "client-request-create-f5", IdempotencyKey: "mission-create-idempotency-key-f5", TenantID: tenantID, UserID: userID, SessionID: "f5000000-0000-4000-8000-000000000006"}, TargetRoleProfileID: roleID, Goal: "Build crash-safe cloud agents"}
	created, err := service.Create(ctx, command)
	if err != nil || created.Replayed || created.Mission.Status != mission.Draft || created.Mission.Version != 1 || created.Mission.ID == "" || created.Focus.MissionID != nil || created.Focus.Version != 0 || len(created.EventIDs) != 1 {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	replayed, err := service.Create(ctx, command)
	if err != nil || !replayed.Replayed || replayed.Mission.ID != created.Mission.ID || replayed.EventIDs[0] != created.EventIDs[0] {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	conflict := command
	conflict.Goal = "Substituted goal"
	if _, err = service.Create(ctx, conflict); !errors.Is(err, productapi.ErrIdempotencyConflict) {
		t.Fatalf("idempotency substitution error=%v", err)
	}
	var goalRef, goalHash, eventRef, eventHash string
	var missions, events, commands, responses int
	err = admin.QueryRow(ctx, `SELECT m.goal_payload_ref,m.goal_payload_hash,e.payload_ref,e.payload_hash,(SELECT count(*) FROM product.missions WHERE tenant_id=$1 AND user_id=$2),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND user_id=$2 AND event_type='MissionCreated'),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='GenerateMissionRoute'),(SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND user_id=$2 AND operation_id='missions.create.v2' AND status='completed') FROM product.missions m JOIN agent.events e ON e.tenant_id=m.tenant_id AND e.aggregate_id=m.id WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.id=$3`, tenantID, userID, created.Mission.ID).Scan(&goalRef, &goalHash, &eventRef, &eventHash, &missions, &events, &commands, &responses)
	if err != nil || goalRef == "" || len(goalHash) != 64 || eventRef == "" || len(eventHash) != 64 || missions != 1 || events != 1 || commands != 1 || responses != 1 {
		t.Fatalf("goal=%s/%s event=%s/%s missions=%d events=%d commands=%d responses=%d err=%v", goalRef, goalHash, eventRef, eventHash, missions, events, commands, responses, err)
	}
}
