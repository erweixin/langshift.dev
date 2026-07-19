//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	executionapi "github.com/langshift/lites/internal/execution/api"
	"github.com/langshift/lites/internal/identity/anonymousclaim"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
	"github.com/langshift/lites/internal/product/contentcatalog"
	productpostgres "github.com/langshift/lites/internal/product/postgres"
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
	const initiatorSession = "79000000-0000-4000-8000-000000000004"
	const approverOne = "79000000-0000-4000-8000-000000000005"
	const approverOneMembership = "79000000-0000-4000-8000-000000000006"
	const approverOneSession = "79000000-0000-4000-8000-000000000007"
	const approverTwo = "79000000-0000-4000-8000-000000000008"
	const approverTwoMembership = "79000000-0000-4000-8000-000000000009"
	const approverTwoSession = "79000000-0000-4000-8000-00000000000a"
	const subjectID = "71000000-0000-4000-8000-000000000001"
	const ephemeralUser = "72000000-0000-4000-8000-000000000001"
	const sessionID = "73000000-0000-4000-8000-000000000001"
	const claimID = "74000000-0000-4000-8000-000000000001"
	const sourceMissionID = "75000000-0000-4000-8000-000000000010"
	const sourceRouteID = "78000000-0000-4000-8000-000000000001"
	const sourceRunID = "78000000-0000-4000-8000-000000000010"
	const sourceConversationID = "78000000-0000-4000-8000-000000000011"
	const sourceMessageID = "78000000-0000-4000-8000-000000000012"
	const sourceMessageEventID = "78000000-0000-4000-8000-000000000013"
	const roleProfileID = "7a000000-0000-4000-8000-000000000001"
	const capabilityID = "7a000000-0000-4000-8000-000000000002"
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
	routeManifest, err := payloadStore.Put(ctx, payload.Descriptor{TenantID: systemTenant, ObjectID: sourceRouteID, Class: "route-revision", ContentType: "application/json"}, []byte(`{"schema_version":1,"summary":"A grounded route imported from anonymous onboarding.","transferable_experience":[{"statement":"Existing delivery practice transfers.","capability_ids":["7a000000-0000-4000-8000-000000000002"],"evidence_ids":[],"confidence":"inferred"}],"gaps":[{"statement":"Practice the target workflow.","capability_ids":["7a000000-0000-4000-8000-000000000002"],"evidence_ids":[],"confidence":"inferred"}],"bridge":[{"id":"bridge_one","title":"Build the bridge","rationale":"Practice with a bounded artifact.","from_capability_ids":[],"to_capability_ids":["7a000000-0000-4000-8000-000000000002"]}],"stages":[{"id":"stage_one","title":"Understand","outcome":"Explain the workflow.","capability_ids":["7a000000-0000-4000-8000-000000000002"],"evidence_required":["written explanation"]},{"id":"stage_two","title":"Apply","outcome":"Ship a small workflow.","capability_ids":["7a000000-0000-4000-8000-000000000002"],"evidence_required":["working artifact"]}],"first_task":{"title":"Sketch the workflow","objective":"Create a bounded workflow plan.","estimated_minutes":45,"difficulty":"standard","capability_ids":["7a000000-0000-4000-8000-000000000002"],"success_criteria":["The plan has inputs, outputs, and recovery steps."]}}`))
	if err != nil {
		t.Fatal(err)
	}
	goalManifest, err := payloadStore.Put(ctx, payload.Descriptor{TenantID: systemTenant, ObjectID: sourceMissionID, Class: "mission-goal", ContentType: "application/json"}, []byte(`{"goal":"Become an AI product lead"}`))
	if err != nil {
		t.Fatal(err)
	}
	messageManifest, err := payloadStore.Put(ctx, payload.Descriptor{TenantID: systemTenant, ObjectID: sourceMessageID, Class: "run-message", ContentType: "application/json"}, []byte(`{"schema_version":1,"role":"user","content":[{"type":"text","text":"Built B2B products and led a migration."}]}`))
	if err != nil {
		t.Fatal(err)
	}
	messageEventManifest, err := payloadStore.Put(ctx, payload.Descriptor{TenantID: systemTenant, ObjectID: sourceMessageEventID, Class: "event-payload", ContentType: "application/json"}, []byte(`{"subject_id":"78000000-0000-4000-8000-000000000011","subject_version":2,"message_id":"78000000-0000-4000-8000-000000000012"}`))
	if err != nil {
		t.Fatal(err)
	}
	bodyManifestJSON, _ := json.Marshal(bodyManifest)
	inputManifest, _ := json.Marshal(map[string]any{
		"schema_version":  4,
		"mission":         map[string]any{"id": sourceMissionID, "version": 1, "route_version": 1, "claim_set_hash": "claim-set-hash-1", "source_role_profile_id": nil, "target_role_profile_id": roleProfileID},
		"onboarding":      map[string]any{"session_id": sessionID, "version": 1, "current_role_input": map[string]any{"text": "Frontend Developer"}, "target_role_input": map[string]any{"text": "AI Application Engineer"}, "experience_payload": bodyManifest},
		"claim_revisions": []any{}, "evidence_revisions": []any{},
		"target_requirements":       []any{map[string]any{"id": "7a000000-0000-4000-8000-000000000003", "version": 1, "role_profile_id": roleProfileID, "capability_id": capabilityID, "requirement_level": "practiced", "rationale": "fixture", "revision": 1}},
		"agent_profile":             map[string]any{"profile": "route_planner", "environment": "production", "channel_id": "7a000000-0000-4000-8000-000000000004", "sequence": 1, "snapshot_id": "behavior-fixture", "activated_at": now},
		"agent_profile_snapshot_id": "behavior-fixture", "ontology_snapshot_id": "ontology-v1", "content_snapshot_id": "content-v1",
	})
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,status) VALUES($1,'claim-owner@example.invalid',$2,'en','active')`, []any{targetUser, now}},
		{`INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,status) VALUES($1,'claim-approver-one@example.invalid',$3,'en','active'),($2,'claim-approver-two@example.invalid',$3,'en','active')`, []any{approverOne, approverTwo, now}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region) VALUES($1,'anonymous_system','Anonymous','active','US'),($2,'anonymous_system','Other','active','US')`, []any{systemTenant, otherTenant}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Claim Target','active','US',$2)`, []any{targetTenant, targetUser}},
		{`INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,'owner','active',$4)`, []any{targetMembership, targetTenant, targetUser, now}},
		{`INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$3,$4,'admin','active',$6),($2,$3,$5,'admin','active',$6)`, []any{approverOneMembership, approverTwoMembership, targetTenant, approverOne, approverTwo, now}},
		{`INSERT INTO identity.sessions(id,user_id,active_tenant_id,token_hash,csrf_secret_hash,ip_hash,user_agent_hash,last_seen_at,expires_at,reauthenticated_at) VALUES($1,$2,$3,decode(repeat('11',32),'hex'),decode(repeat('12',32),'hex'),decode(repeat('13',32),'hex'),decode(repeat('14',32),'hex'),$7,$8,$7),($4,$5,$3,decode(repeat('21',32),'hex'),decode(repeat('22',32),'hex'),decode(repeat('23',32),'hex'),decode(repeat('24',32),'hex'),$7,$8,$7),($6,$9,$3,decode(repeat('31',32),'hex'),decode(repeat('32',32),'hex'),decode(repeat('33',32),'hex'),decode(repeat('34',32),'hex'),$7,$8,$7)`, []any{initiatorSession, targetUser, targetTenant, approverOneSession, approverOne, approverTwoSession, now, now.Add(time.Hour), approverTwo}},
		{`INSERT INTO identity.anonymous_subjects(id,anonymous_subject_hash,ephemeral_user_id,system_tenant_id,expires_at) VALUES($1,decode(repeat('ab',32),'hex'),$2,$3,$4)`, []any{subjectID, ephemeralUser, systemTenant, now.Add(time.Hour)}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'role_ai_application_engineer',1,'active','{}','en','{}')`, []any{roleProfileID, systemTenant}},
		{`INSERT INTO product.capabilities(id,tenant_id,slug,revision,status,spec,evidence_guidance) VALUES($1,$2,'cap_component_abstraction',1,'active','{}','{}')`, []any{capabilityID, systemTenant}},
		{`INSERT INTO product.missions(id,tenant_id,user_id,status,target_role_profile_id,goal_payload_ref,goal_payload_hash,route_version,claim_set_hash) VALUES($1,$2,$3,'draft',$4,$5,$6,1,'claim-set-hash-1')`, []any{sourceMissionID, systemTenant, ephemeralUser, roleProfileID, goalManifest.Ref, goalManifest.Hash}},
		{`INSERT INTO agent.runs(id,tenant_id,user_id,conversation_id,status,run_version,due_at,profile_snapshot_id,budget_snapshot) VALUES($1,$2,$3,$4,'succeeded',3,$5,'agent-v1','{}')`, []any{sourceRunID, systemTenant, ephemeralUser, sourceConversationID, now.Add(time.Hour)}},
		{`INSERT INTO agent.conversations(id,tenant_id,user_id,mission_id,title,mode,status,last_run_id,created_at,updated_at) VALUES($1,$2,$3,$4,'Anonymous route preview','project','archived',$5,$6,$6)`, []any{sourceConversationID, systemTenant, ephemeralUser, sourceMissionID, sourceRunID, now}},
		{`INSERT INTO agent.events(id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,correlation_id,payload_ref,payload_hash) VALUES($1,$2,$3,1,'ConversationMessageFinalized',1,'conversation',$4,1,$5,$6,$6,'{"kind":"system"}',$4,$7,$8)`, []any{sourceMessageEventID, systemTenant, ephemeralUser, sourceConversationID, storeEpoch, now, messageEventManifest.Ref, messageEventManifest.Hash}},
		{`INSERT INTO agent.event_cursors(tenant_id,user_id,last_seq) VALUES($1,$2,1)`, []any{systemTenant, ephemeralUser}},
		{`INSERT INTO agent.run_messages(id,tenant_id,user_id,run_id,role,message_index,payload_ref,payload_hash,content_hash,content_type,source_kind,trust_label,finalized_event_id,finalized_at,created_at,updated_at) VALUES($1,$2,$3,$4,'user',0,$5,$6,$6,'application/json','conversation_user','user_asserted',$7,$8,$8,$8)`, []any{sourceMessageID, systemTenant, ephemeralUser, sourceRunID, messageManifest.Ref, messageManifest.Hash, sourceMessageEventID, now}},
		{`INSERT INTO product.route_revisions(id,tenant_id,user_id,mission_id,route_version,status,claim_set_hash,base_route_version,input_manifest,route_payload_ref,route_payload_hash,agent_profile_snapshot_id,ontology_snapshot_id,content_snapshot_id,planner_run_id) VALUES($1,$2,$3,$4,1,'proposed','claim-set-hash-1',0,$5,$6,$7,'agent-v1','ontology-v1','content-v1',$8)`, []any{sourceRouteID, systemTenant, ephemeralUser, sourceMissionID, inputManifest, routeManifest.Ref, routeManifest.Hash, sourceRunID}},
		{`INSERT INTO identity.onboarding_sessions(id,tenant_id,user_id,anonymous_subject_id,status,locale,current_role_input,target_role_input,experience_payload_ref,confirmed_claim_ids,route_revision_id,mission_id,expires_at) VALUES($1,$2,$3,$4,'route_ready','en','{}','{}',$5,'[]',$6,$7,$8)`, []any{sessionID, systemTenant, ephemeralUser, subjectID, string(bodyManifestJSON), sourceRouteID, sourceMissionID, now.Add(time.Hour)}},
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
	_, sourceFile, _, _ := runtime.Caller(0)
	contentRelease, err := contentcatalog.Load(filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "product-content", "releases", "1.0.0")))
	if err != nil {
		t.Fatal(err)
	}
	missionID, err := ids.DeterministicUUID(identityKey, "anonymous-claim-target-mission", "claim-key-1\x00"+targetTenant+"\x00"+targetUser)
	if err != nil {
		t.Fatal(err)
	}
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
	requestService := OnboardingClaimService{Pool: pool, Store: store, SystemTenantID: systemTenant, Payloads: payloadStore, IdentityKey: identityKey, IdempotencyKeyPepper: bytes.Repeat([]byte{0x74}, 32), RequestDigestPepper: bytes.Repeat([]byte{0x75}, 32), IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	claimCommand := api.OnboardingClaimCommand{AuthenticatedRequestMetadata: api.AuthenticatedRequestMetadata{RequestMetadata: api.RequestMetadata{RequestID: "server-claim-request", ClientRequestID: "client-claim-request", IdempotencyKey: "claim-request-idempotency-0001", ClientIPHash: bytes.Repeat([]byte{0x76}, 32), UserAgentHash: bytes.Repeat([]byte{0x77}, 32)}, UserID: targetUser, TenantID: targetTenant, MembershipID: targetMembership, SessionID: "79000000-0000-4000-8000-000000000004"}, OnboardingSessionID: sessionID, AnonymousSubjectID: subjectID, ExpectedClaimVersion: 1}
	claimResults := concurrentCalls(t, 64, func() (api.OnboardingClaimResult, error) { return requestService.ClaimOnboarding(ctx, claimCommand) })
	for _, result := range claimResults {
		if result.ID != claimID || result.Version != 2 || result.Status != string(anonymousclaim.Reserved) {
			t.Fatalf("claim result=%#v", result)
		}
	}
	conflictingCommand := claimCommand
	conflictingCommand.ExpectedClaimVersion = 2
	if _, err = requestService.ClaimOnboarding(ctx, conflictingCommand); !errors.Is(err, api.ErrIdempotencyConflict) {
		t.Fatalf("claim idempotency conflict=%v", err)
	}
	var wait sync.WaitGroup
	var reservedAt *time.Time
	if err = admin.QueryRow(ctx, `SELECT reserved_at FROM identity.anonymous_subjects WHERE id=$1`, subjectID).Scan(&reservedAt); err != nil || reservedAt == nil {
		t.Fatalf("subject reservation=%v error=%v", reservedAt, err)
	}
	destination := AnonymousClaimDestination{Pool: pool, SystemTenantID: systemTenant, IdentityKey: identityKey, Payloads: payloadStore, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, Content: productpostgres.ContentCatalog{Pool: pool, Release: contentRelease}, StoreEpoch: storeEpoch, Now: func() time.Time { return now }}
	reservedSaga, err := store.Load(ctx, claimID)
	if err != nil || reservedSaga.Status != anonymousclaim.Reserved {
		t.Fatalf("reserved saga=%#v error=%v", reservedSaga, err)
	}
	destinationEventID, err := destination.CommitDestination(ctx, reservedSaga)
	if err != nil || destinationEventID == "" {
		t.Fatalf("destination commit event=%s error=%v", destinationEventID, err)
	}
	destinationReplays := make(chan error, 16)
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			eventID, replayErr := destination.CommitDestination(ctx, reservedSaga)
			if replayErr == nil && eventID != destinationEventID {
				replayErr = errors.New("destination replay returned a different event")
			}
			destinationReplays <- replayErr
		}()
	}
	wait.Wait()
	close(destinationReplays)
	for replayErr := range destinationReplays {
		if replayErr != nil {
			t.Fatalf("destination replay: %v", replayErr)
		}
	}
	var targetRouteID, targetRouteRef, targetRouteHash string
	var targetInput json.RawMessage
	if err = admin.QueryRow(ctx, `SELECT r.id::text,r.route_payload_ref,r.route_payload_hash,r.input_manifest FROM product.route_revisions r WHERE r.tenant_id=$1 AND r.mission_id=$2`, targetTenant, missionID).Scan(&targetRouteID, &targetRouteRef, &targetRouteHash, &targetInput); err != nil {
		t.Fatal(err)
	}
	remappedRoute, err := payloadStore.Get(ctx, payload.Descriptor{TenantID: targetTenant, ObjectID: targetRouteID, Class: "route-revision", ContentType: "application/json"}, payload.Manifest{Ref: targetRouteRef, Hash: targetRouteHash})
	if err != nil || bytes.Contains(remappedRoute, []byte(roleProfileID)) || bytes.Contains(remappedRoute, []byte(capabilityID)) || bytes.Contains(targetInput, []byte(roleProfileID)) || bytes.Contains(targetInput, []byte(capabilityID)) {
		t.Fatalf("claim content was not remapped: route=%s input=%s error=%v", remappedRoute, targetInput, err)
	}
	var remappedManifest struct {
		Onboarding struct {
			SessionID         string           `json:"session_id"`
			ExperiencePayload payload.Manifest `json:"experience_payload"`
		} `json:"onboarding"`
	}
	if json.Unmarshal(targetInput, &remappedManifest) != nil || remappedManifest.Onboarding.SessionID != sessionID || remappedManifest.Onboarding.ExperiencePayload.Ref == "" || remappedManifest.Onboarding.ExperiencePayload.Ref == bodyManifest.Ref {
		t.Fatalf("remapped onboarding manifest=%s", targetInput)
	}
	if importedBody, bodyErr := payloadStore.Get(ctx, payload.Descriptor{TenantID: targetTenant, ObjectID: sessionID, Class: "onboarding-body", ContentType: "application/json"}, remappedManifest.Onboarding.ExperiencePayload); bodyErr != nil || !bytes.Contains(importedBody, []byte("Built B2B products")) {
		t.Fatalf("target onboarding body=%s error=%v", importedBody, bodyErr)
	}
	manualReview, err := anonymousclaim.Advance(reservedSaga, anonymousclaim.Input{ExpectedVersion: reservedSaga.Version, Command: anonymousclaim.Escalate})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CompareAndSwap(ctx, reservedSaga, manualReview); err != nil {
		t.Fatal(err)
	}
	inspected, err := (AnonymousClaimRepairInspector{Pool: pool, IdentityKey: identityKey}).InspectManualReview(ctx, manualReview)
	if err != nil || inspected.TargetStatus != string(anonymousclaim.DestinationCommitted) || inspected.DestinationCommitEventID != destinationEventID || inspected.ClaimVersion != manualReview.Version+1 || inspected.EvidenceHash == "" {
		t.Fatalf("inspected=%#v error=%v", inspected, err)
	}
	authority := identityEpochAuthority{epoch: storeEpoch}
	repairService := AnonymousClaimRepairControlService{
		Pool: pool, ClaimStore: store, Inspector: AnonymousClaimRepairInspector{Pool: pool, IdentityKey: identityKey}, Payloads: payloadStore,
		Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, Epochs: authority, SystemTenantID: systemTenant, StoreEpoch: storeEpoch,
		IDKey: bytes.Repeat([]byte{0x6a}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0x6b}, 32), RequestDigestPepper: bytes.Repeat([]byte{0x6c}, 32),
		IdempotencyTTL: 24 * time.Hour, ProposalTTL: 30 * time.Minute, Now: func() time.Time { return now },
	}
	proposalCommand := executionapi.ProposeRepairCommand{RequestID: "7d000000-0000-4000-8000-000000000001", ClientRequestID: "client-claim-repair", IdempotencyKey: "claim-repair-proposal-key-0001", TenantID: targetTenant, UserID: targetUser, SessionID: initiatorSession, TargetID: claimID, TargetVersion: manualReview.Version, Resolution: "reconcile_destination_committed", EvidenceHash: inspected.EvidenceHash, Reason: "destination commit succeeded after the source worker lost its acknowledgement"}
	proposals := concurrentCalls(t, 12, func() (executionapi.RepairResult, error) { return repairService.ProposeRepair(ctx, proposalCommand) })
	for _, proposal := range proposals {
		if proposal.ID == "" || proposal.Version != 1 || proposal.Status != "proposed" {
			t.Fatalf("claim repair proposal=%#v", proposal)
		}
	}
	repairID := proposals[0].ID
	var proposalHash string
	if err = admin.QueryRow(ctx, `SELECT proposal_hash FROM agent.repair_commands WHERE tenant_id=$1 AND id=$2`, targetTenant, repairID).Scan(&proposalHash); err != nil {
		t.Fatal(err)
	}
	decisions := []executionapi.DecideRepairCommand{
		{RequestID: "7d000000-0000-4000-8000-000000000002", ClientRequestID: "client-claim-approval-one", IdempotencyKey: "claim-repair-approval-key-0001", TenantID: targetTenant, UserID: approverOne, SessionID: approverOneSession, RepairID: repairID, Decision: "approve", ProposalHash: proposalHash, ExpectedRepairVersion: 1, TargetVersion: manualReview.Version, PermissionSnapshot: "owner-or-admin@membership-v1"},
		{RequestID: "7d000000-0000-4000-8000-000000000003", ClientRequestID: "client-claim-approval-two", IdempotencyKey: "claim-repair-approval-key-0002", TenantID: targetTenant, UserID: approverTwo, SessionID: approverTwoSession, RepairID: repairID, Decision: "approve", ProposalHash: proposalHash, ExpectedRepairVersion: 1, TargetVersion: manualReview.Version, PermissionSnapshot: "owner-or-admin@membership-v1"},
	}
	decisionErrors := make(chan error, len(decisions))
	for _, decision := range decisions {
		decision := decision
		go func() {
			_, decisionErr := repairService.DecideRepair(ctx, decision)
			decisionErrors <- decisionErr
		}()
	}
	for range decisions {
		if decisionErr := <-decisionErrors; decisionErr != nil {
			t.Fatalf("claim repair decision: %v", decisionErr)
		}
	}
	var repairStatus string
	var repairVersion uint64
	if err = admin.QueryRow(ctx, `SELECT status,version FROM agent.repair_commands WHERE tenant_id=$1 AND id=$2`, targetTenant, repairID).Scan(&repairStatus, &repairVersion); err != nil || repairStatus != "executed" || repairVersion != 3 {
		t.Fatalf("claim repair status=%s version=%d error=%v", repairStatus, repairVersion, err)
	}
	reconciled, err := store.Load(ctx, claimID)
	if err != nil || reconciled.Status != anonymousclaim.DestinationCommitted || reconciled.Version != manualReview.Version+1 {
		t.Fatalf("reconciled claim=%#v error=%v", reconciled, err)
	}
	eraser := AnonymousClaimEraser{Pool: pool, SystemTenantID: systemTenant, IdentityKey: identityKey, Objects: blobs, Now: func() time.Time { return now.Add(123456789 * time.Nanosecond) }}
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
	outboxStore := eventpostgres.OutboxStore{Pool: pool, Epochs: authority, Tokens: opaque.Manager{Purpose: "claim-outbox-lease", Pepper: bytes.Repeat([]byte{0x7e}, 32)}, LeaseTTL: time.Minute, RetryBase: time.Second, RetryLimit: time.Minute, Now: func() time.Time { return now }}
	outboxClaims, err := outboxStore.ClaimBatch(ctx, systemTenant, storeEpoch, 10)
	if err != nil || len(outboxClaims) != 4 {
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
	if _, goalErr := blobs.Get(ctx, goalManifest.Ref); goalErr == nil {
		t.Fatal("anonymous mission goal blob survived erasure")
	}
	if _, messageErr := blobs.Get(ctx, messageManifest.Ref); messageErr == nil {
		t.Fatal("anonymous route-planner message blob survived erasure")
	}
	if _, eventErr := blobs.Get(ctx, messageEventManifest.Ref); eventErr == nil {
		t.Fatal("anonymous route-planner event blob survived erasure")
	}
	if importedBody, bodyErr := payloadStore.Get(ctx, payload.Descriptor{TenantID: targetTenant, ObjectID: sessionID, Class: "onboarding-body", ContentType: "application/json"}, remappedManifest.Onboarding.ExperiencePayload); bodyErr != nil || !bytes.Contains(importedBody, []byte("Built B2B products")) {
		t.Fatalf("claimed onboarding body was erased with anonymous source: %s error=%v", importedBody, bodyErr)
	}
	var receiptCount, sourceMissions, sourceRoutes, sourceConversations, targetMissions, targetRoutes, missionImports, destinationEvents, destinationOutbox, dailyCommands, focusedMissions, sourcePublishedOutbox, completedInbox int
	var deletedAt *time.Time
	var experiencePayload *string
	var sourceSessionMission, sourceSessionRoute *string
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM identity.anonymous_erasure_receipts WHERE claim_id=$1),(SELECT count(*) FROM product.missions WHERE id=$2),(SELECT count(*) FROM product.route_revisions WHERE id=$3),(SELECT count(*) FROM agent.conversations WHERE id=$11),(SELECT experience_payload_ref FROM identity.onboarding_sessions WHERE id=$4),(SELECT mission_id::text FROM identity.onboarding_sessions WHERE id=$4),(SELECT route_revision_id::text FROM identity.onboarding_sessions WHERE id=$4),(SELECT count(*) FROM product.missions WHERE id=$5 AND tenant_id=$6 AND user_id=$7),(SELECT count(*) FROM product.route_revisions WHERE tenant_id=$6 AND mission_id=$5 AND status='accepted'),(SELECT count(*) FROM product.mission_imports WHERE tenant_id=$6 AND claim_key='claim-key-1'),(SELECT count(*) FROM agent.events WHERE tenant_id=$6 AND id=$8),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$6 AND aggregate_id=$1),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$6 AND aggregate_id=$5 AND command_type='GenerateDailyTask'),(SELECT count(*) FROM product.mission_focuses WHERE tenant_id=$6 AND user_id=$7 AND mission_id=$5 AND focus_version=1),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$9 AND aggregate_id=$1 AND status='published'),(SELECT count(*) FROM agent.inbox WHERE tenant_id=$9 AND command_id=$10 AND status='completed')`, claimID, sourceMissionID, sourceRouteID, sessionID, missionID, targetTenant, targetUser, final.DestinationCommitEventID, systemTenant, reconcileCommand.CommandID, sourceConversationID).Scan(&receiptCount, &sourceMissions, &sourceRoutes, &sourceConversations, &experiencePayload, &sourceSessionMission, &sourceSessionRoute, &targetMissions, &targetRoutes, &missionImports, &destinationEvents, &destinationOutbox, &dailyCommands, &focusedMissions, &sourcePublishedOutbox, &completedInbox); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT deleted_at FROM identity.anonymous_subjects WHERE id=$1`, subjectID).Scan(&deletedAt); err != nil || deletedAt == nil || receiptCount != 3 {
		t.Fatalf("deleted_at=%v receipts=%d error=%v", deletedAt, receiptCount, err)
	}
	if sourceMissions != 0 || sourceRoutes != 0 || sourceConversations != 0 || experiencePayload != nil || sourceSessionMission != nil || sourceSessionRoute != nil {
		t.Fatalf("source_missions=%d source_routes=%d source_conversations=%d experience_payload=%v session_mission=%v session_route=%v", sourceMissions, sourceRoutes, sourceConversations, experiencePayload, sourceSessionMission, sourceSessionRoute)
	}
	if targetMissions != 1 || targetRoutes != 1 || missionImports != 1 || destinationEvents != 1 || destinationOutbox != 1 || dailyCommands != 1 || focusedMissions != 1 {
		t.Fatalf("target_missions=%d target_routes=%d imports=%d events=%d outbox=%d daily=%d focus=%d", targetMissions, targetRoutes, missionImports, destinationEvents, destinationOutbox, dailyCommands, focusedMissions)
	}
	if sourcePublishedOutbox != 4 || completedInbox != 1 {
		t.Fatalf("source_published_outbox=%d completed_inbox=%d", sourcePublishedOutbox, completedInbox)
	}
	if _, err = (AnonymousClaimStore{Pool: pool, SystemTenantID: otherTenant}).Load(ctx, claimID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant load error=%v", err)
	}
}
