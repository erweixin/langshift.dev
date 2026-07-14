//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/anonymousclaim"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestAnonymousClaimStoreConvergesWithRLSAndProtectsReservationFromExpiry(t *testing.T) {
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, requiredEnv(t, "LITES_TEST_ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	pool, err := pgxpool.New(ctx, requiredEnv(t, "LITES_TEST_IDENTITY_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	now := time.Unix(1_800_002_900, 0).UTC()
	const systemTenant = "70000000-0000-4000-8000-000000000001"
	const otherTenant = "70000000-0000-4000-8000-000000000002"
	const targetTenant = "79000000-0000-4000-8000-000000000001"
	const targetUser = "79000000-0000-4000-8000-000000000002"
	const targetMembership = "79000000-0000-4000-8000-000000000003"
	const subjectID = "71000000-0000-4000-8000-000000000001"
	const ephemeralUser = "72000000-0000-4000-8000-000000000001"
	const sessionID = "73000000-0000-4000-8000-000000000001"
	const claimID = "74000000-0000-4000-8000-000000000001"
	const missionID = "75000000-0000-4000-8000-000000000001"
	const sourceMissionID = "75000000-0000-4000-8000-000000000010"
	const sourceRouteID = "78000000-0000-4000-8000-000000000001"
	const roleProfileID = "7a000000-0000-4000-8000-000000000001"
	const expiredSubjectID = "71000000-0000-4000-8000-000000000002"
	const expiredUserID = "72000000-0000-4000-8000-000000000002"
	const expiredSessionID = "73000000-0000-4000-8000-000000000002"
	const expiredClaimID = "74000000-0000-4000-8000-000000000002"
	const storeEpoch = "7c000000-0000-4000-8000-000000000001"
	blobs := &authMemoryBlobs{values: map[string][]byte{}}
	payloadStore := payload.EnvelopeStore{Keys: authKeyProvider{key: payload.Key{ID: "claim-vault-key-v1", Material: bytes.Repeat([]byte{0x7d}, 32)}}, Blobs: blobs}
	bodyManifest, err := payloadStore.Put(ctx, payload.Descriptor{TenantID: systemTenant, ObjectID: sessionID, Class: "onboarding-body", ContentType: "application/json"}, []byte(`{"experience_summary":"Built B2B products"}`))
	if err != nil {
		t.Fatal(err)
	}
	routeManifest, err := payloadStore.Put(ctx, payload.Descriptor{TenantID: systemTenant, ObjectID: sourceRouteID, Class: "route-revision", ContentType: "application/json"}, []byte(`{"steps":[{"title":"Build an AI workflow"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	bodyManifestJSON, _ := json.Marshal(bodyManifest)
	routeManifestJSON, _ := json.Marshal(routeManifest)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,status) VALUES($1,'claim-owner@example.invalid',$2,'en','active')`, []any{targetUser, now}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region) VALUES($1,'anonymous_system','Anonymous','active','US'),($2,'anonymous_system','Other','active','US')`, []any{systemTenant, otherTenant}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Claim Target','active','US',$2)`, []any{targetTenant, targetUser}},
		{`INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,'owner','active',$4)`, []any{targetMembership, targetTenant, targetUser, now}},
		{`INSERT INTO identity.anonymous_subjects(id,anonymous_subject_hash,ephemeral_user_id,system_tenant_id,expires_at) VALUES($1,decode(repeat('ab',32),'hex'),$2,$3,$4)`, []any{subjectID, ephemeralUser, systemTenant, now.Add(time.Hour)}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'ai-product-lead',1,'active','{}','en','{}')`, []any{roleProfileID, systemTenant}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,route_version,claim_set_hash) VALUES($1,$2,$3,'draft',$4,1,'claim-set-hash-1')`, []any{sourceMissionID, systemTenant, ephemeralUser, roleProfileID}},
		{`INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id) VALUES($1,$2,$3,$4,1,'proposed','claim-set-hash-1',0,'{}',$5,'agent-v1','ontology-v1','content-v1')`, []any{sourceRouteID, systemTenant, ephemeralUser, sourceMissionID, string(routeManifestJSON)}},
		{`INSERT INTO identity.onboarding_sessions(id,tenant_id,user_id,anonymous_subject_id,status,locale,current_role_input,target_role_input,experience_payload_ref,confirmed_claim_ids,route_revision_id,expires_at) VALUES($1,$2,$3,$4,'route_ready','en','{}','{}',$5,'[]',$6,$7)`, []any{sessionID, systemTenant, ephemeralUser, subjectID, string(bodyManifestJSON), sourceRouteID, now.Add(time.Hour)}},
		{`INSERT INTO identity.onboarding_claims(id,tenant_id,user_id,anonymous_subject_id,onboarding_session_id,claim_key,status,source_route_revision_id,expires_at) VALUES($1,$2,$3,$4,$5,'claim-key-1','available',$6,$7)`, []any{claimID, systemTenant, ephemeralUser, subjectID, sessionID, sourceRouteID, now.Add(time.Hour)}},
		{`INSERT INTO identity.anonymous_subjects(id,anonymous_subject_hash,ephemeral_user_id,system_tenant_id,expires_at) VALUES($1,decode(repeat('cd',32),'hex'),$2,$3,$4)`, []any{expiredSubjectID, expiredUserID, systemTenant, now.Add(-time.Second)}},
		{`INSERT INTO identity.onboarding_sessions(id,tenant_id,user_id,anonymous_subject_id,status,locale,current_role_input,target_role_input,confirmed_claim_ids,expires_at) VALUES($1,$2,$3,$4,'route_ready','en','{}','{}','[]',$5)`, []any{expiredSessionID, systemTenant, expiredUserID, expiredSubjectID, now.Add(-time.Second)}},
		{`INSERT INTO identity.onboarding_claims(id,tenant_id,user_id,anonymous_subject_id,onboarding_session_id,claim_key,status,source_route_revision_id,expires_at) VALUES($1,$2,$3,$4,$5,'claim-key-expired','available',$6,$7)`, []any{expiredClaimID, systemTenant, expiredUserID, expiredSubjectID, expiredSessionID, "78000000-0000-4000-8000-000000000002", now.Add(-time.Second)}},
	} {
		if _, err = admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	identityKey := bytes.Repeat([]byte{0x7b}, 32)
	store := AnonymousClaimStore{Pool: pool, SystemTenantID: systemTenant, IdentityKey: identityKey, Payloads: payloadStore, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, StoreEpoch: storeEpoch, Now: func() time.Time { return now }}
	reservation := anonymousclaim.Reservation{ClaimID: claimID, ClaimKey: "claim-key-1", TargetTenantID: targetTenant, TargetUserID: targetUser, MissionID: missionID}
	claimService := anonymousclaim.Service{Store: store, Now: func() time.Time { return now }}
	if _, err = claimService.Reserve(ctx, anonymousclaim.Reservation{ClaimID: expiredClaimID, ClaimKey: "claim-key-expired", TargetTenantID: reservation.TargetTenantID, TargetUserID: reservation.TargetUserID, MissionID: "75000000-0000-4000-8000-000000000002"}); !errors.Is(err, anonymousclaim.ErrInvalidTransition) {
		t.Fatalf("expired reservation error=%v", err)
	}
	if expired, expireErr := claimService.ExpireAvailable(ctx, expiredClaimID); expireErr != nil || expired.Status != anonymousclaim.Expired {
		t.Fatalf("expired cleanup=%#v error=%v", expired, expireErr)
	}
	var expiredStatus string
	var expiredReservedAt *time.Time
	if err = admin.QueryRow(ctx, `SELECT status,reserved_at FROM identity.onboarding_claims WHERE id=$1`, expiredClaimID).Scan(&expiredStatus, &expiredReservedAt); err != nil || expiredStatus != "expired" || expiredReservedAt != nil {
		t.Fatalf("expired status=%s reserved_at=%v error=%v", expiredStatus, expiredReservedAt, err)
	}
	const contenders = 64
	results := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, reserveErr := claimService.Reserve(ctx, reservation)
			results <- reserveErr
		}()
	}
	wait.Wait()
	close(results)
	for reserveErr := range results {
		if reserveErr != nil {
			t.Fatalf("reserve: %v", reserveErr)
		}
	}
	var reservedAt *time.Time
	if err = admin.QueryRow(ctx, `SELECT reserved_at FROM identity.anonymous_subjects WHERE id=$1`, subjectID).Scan(&reservedAt); err != nil || reservedAt == nil {
		t.Fatalf("subject reservation=%v error=%v", reservedAt, err)
	}
	destination := AnonymousClaimDestination{Pool: pool, SystemTenantID: systemTenant, IdentityKey: identityKey, Payloads: payloadStore, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, StoreEpoch: storeEpoch, Now: func() time.Time { return now }}
	eraser := AnonymousClaimEraser{Pool: pool, SystemTenantID: systemTenant, IdentityKey: identityKey, Objects: blobs, Now: func() time.Time { return now }}
	claimService.Destination = destination
	claimService.Eraser = eraser
	const reconcilers = 16
	reconcileResults := make(chan error, reconcilers)
	for range reconcilers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, reconcileErr := claimService.Reconcile(ctx, claimID)
			reconcileResults <- reconcileErr
		}()
	}
	wait.Wait()
	close(reconcileResults)
	for reconcileErr := range reconcileResults {
		if reconcileErr != nil {
			t.Fatalf("reconcile: %v", reconcileErr)
		}
	}
	final, err := store.Load(ctx, claimID)
	if err != nil || final.Status != anonymousclaim.Claimed || len(final.DeletionReceipts) != 3 {
		t.Fatalf("final=%#v error=%v", final, err)
	}
	authority := identityEpochAuthority{epoch: storeEpoch}
	outboxStore := eventpostgres.OutboxStore{Pool: pool, Epochs: authority, Tokens: opaque.Manager{Purpose: "claim-outbox-lease", Pepper: bytes.Repeat([]byte{0x7e}, 32)}, LeaseTTL: time.Minute, RetryBase: time.Second, RetryLimit: time.Minute, Now: func() time.Time { return now }}
	outboxClaims, err := outboxStore.ClaimBatch(ctx, systemTenant, storeEpoch, 10)
	if err != nil || len(outboxClaims) != 2 {
		t.Fatalf("outbox claims=%d error=%v", len(outboxClaims), err)
	}
	var reconcileCommand eventpostgres.DeliveredCommand
	for _, outboxClaim := range outboxClaims {
		if outboxClaim.CommandType == AnonymousClaimReconcileCommand {
			reconcileCommand = outboxClaim.PublishedCommand.Delivered()
		}
		if err = outboxStore.MarkPublished(ctx, outboxClaim); err != nil {
			t.Fatal(err)
		}
	}
	if reconcileCommand.CommandID == "" {
		t.Fatal("reservation did not enqueue reconcile command")
	}
	dispatcher := AnonymousClaimDispatcher{Reconciler: claimService, Payloads: payloadStore, Inbox: eventpostgres.InboxStore{Pool: pool, Epochs: authority, Tokens: opaque.Manager{Purpose: "claim-inbox-lease", Pepper: bytes.Repeat([]byte{0x7f}, 32)}, LeaseTTL: time.Minute, Now: func() time.Time { return now }}, StoreEpoch: storeEpoch}
	dispatched, err := dispatcher.Dispatch(ctx, reconcileCommand)
	if err != nil || !dispatched.Completed || !dispatched.Claimed || dispatched.Saga.Status != anonymousclaim.Claimed {
		t.Fatalf("dispatch=%#v error=%v", dispatched, err)
	}
	replayed, err := dispatcher.Dispatch(ctx, reconcileCommand)
	if err != nil || !replayed.Completed || !replayed.Replayed || replayed.Claimed {
		t.Fatalf("replay=%#v error=%v", replayed, err)
	}
	if _, bodyErr := blobs.Get(ctx, bodyManifest.Ref); bodyErr == nil {
		t.Fatal("anonymous onboarding body blob survived erasure")
	}
	if _, routeErr := blobs.Get(ctx, routeManifest.Ref); routeErr == nil {
		t.Fatal("anonymous route blob survived erasure")
	}
	var receiptCount, sourceMissions, sourceRoutes, targetMissions, targetRoutes, missionImports, destinationEvents, destinationOutbox, sourcePublishedOutbox, completedInbox int
	var deletedAt *time.Time
	var experiencePayload *string
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM identity.anonymous_erasure_receipts WHERE claim_id=$1),(SELECT count(*) FROM product.missions WHERE id=$2),(SELECT count(*) FROM product.route_revisions WHERE id=$3),(SELECT experience_payload_ref FROM identity.onboarding_sessions WHERE id=$4),(SELECT count(*) FROM product.missions WHERE id=$5 AND tenant_id=$6 AND user_id=$7),(SELECT count(*) FROM product.route_revisions WHERE tenant_id=$6 AND mission_id=$5 AND status='accepted'),(SELECT count(*) FROM product.mission_imports WHERE tenant_id=$6 AND claim_key='claim-key-1'),(SELECT count(*) FROM agent.events WHERE tenant_id=$6 AND id=$8),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$6 AND aggregate_id=$1),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$9 AND aggregate_id=$1 AND status='published'),(SELECT count(*) FROM agent.inbox WHERE tenant_id=$9 AND command_id=$10 AND status='completed')`, claimID, sourceMissionID, sourceRouteID, sessionID, missionID, targetTenant, targetUser, final.DestinationCommitEventID, systemTenant, reconcileCommand.CommandID).Scan(&receiptCount, &sourceMissions, &sourceRoutes, &experiencePayload, &targetMissions, &targetRoutes, &missionImports, &destinationEvents, &destinationOutbox, &sourcePublishedOutbox, &completedInbox); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT deleted_at FROM identity.anonymous_subjects WHERE id=$1`, subjectID).Scan(&deletedAt); err != nil || deletedAt == nil || receiptCount != 3 {
		t.Fatalf("deleted_at=%v receipts=%d error=%v", deletedAt, receiptCount, err)
	}
	if sourceMissions != 0 || sourceRoutes != 0 || experiencePayload != nil {
		t.Fatalf("source_missions=%d source_routes=%d experience_payload=%v", sourceMissions, sourceRoutes, experiencePayload)
	}
	if targetMissions != 1 || targetRoutes != 1 || missionImports != 1 || destinationEvents != 1 || destinationOutbox != 1 {
		t.Fatalf("target_missions=%d target_routes=%d imports=%d events=%d outbox=%d", targetMissions, targetRoutes, missionImports, destinationEvents, destinationOutbox)
	}
	if sourcePublishedOutbox != 2 || completedInbox != 1 {
		t.Fatalf("source_published_outbox=%d completed_inbox=%d", sourcePublishedOutbox, completedInbox)
	}
	if _, err = (AnonymousClaimStore{Pool: pool, SystemTenantID: otherTenant}).Load(ctx, claimID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant load error=%v", err)
	}
}
