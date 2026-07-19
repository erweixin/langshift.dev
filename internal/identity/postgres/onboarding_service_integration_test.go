//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/anonymoussession"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/security/trustedcontext"
)

func TestOnboardingCreateIsDurableIdempotentAcrossPublicAnonymousAndAuthenticatedPrincipals(t *testing.T) {
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
	now := time.Unix(1_800_400_000, 0).UTC()
	const anonymousTenant = "6b000000-0000-4000-8000-000000000001"
	const personalTenant = "6b000000-0000-4000-8000-000000000002"
	const formalUser = "6b000000-0000-4000-8000-000000000003"
	const membershipID = "6b000000-0000-4000-8000-000000000004"
	const sourceRoleID = "6b000000-0000-4000-8000-000000000006"
	const targetRoleID = "6b000000-0000-4000-8000-000000000007"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.tenants(id,kind,name,status,region) VALUES($1,'anonymous_system','Anonymous Onboarding','active','US')`, []any{anonymousTenant}},
		{`INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,status) VALUES($1,'onboarding-formal@example.invalid',$2,'en','active')`, []any{formalUser, now}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Onboarding Personal','active','US',$2)`, []any{personalTenant, formalUser}},
		{`INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,'owner','active',$4)`, []any{membershipID, personalTenant, formalUser, now}},
		{`INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,'frontend-engineer',1,'active','{}','en','{}'),($3,$2,'ai-product-lead',1,'active','{}','en','{}')`, []any{sourceRoleID, anonymousTenant, targetRoleID}},
	} {
		if _, err = admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	blobs := &authMemoryBlobs{values: map[string][]byte{}}
	payloadStore := payload.EnvelopeStore{Keys: authKeyProvider{key: payload.Key{ID: "onboarding-vault-key-v1", Material: bytes.Repeat([]byte{0x91}, 32)}}, Blobs: blobs}
	handleKey := bytes.Repeat([]byte{0x92}, 32)
	handlePepper := bytes.Repeat([]byte{0x93}, 32)
	const storeEpoch = "6b000000-0000-4000-8000-000000000005"
	anonymousService := AnonymousSessionService{Pool: pool, SystemTenantID: anonymousTenant, Signer: anonymoussession.Signer{KeyID: "anonymous-onboarding", Key: handleKey, DigestPepper: handlePepper}, Payloads: payloadStore, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, StoreEpoch: storeEpoch, TTL: anonymoussession.MaximumTTL, Random: rand.Reader, Now: func() time.Time { return now }}
	service := OnboardingService{Pool: pool, Anonymous: anonymousService, Payloads: payloadStore, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, StoreEpoch: storeEpoch, IdentityKey: bytes.Repeat([]byte{0x94}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0x95}, 32), RequestDigestPepper: bytes.Repeat([]byte{0x96}, 32), IdempotencyTTL: 24 * time.Hour, OnboardingTTL: 24 * time.Hour, Random: rand.Reader, Now: func() time.Time { return now }}
	publicCommand := api.OnboardingCreateCommand{RequestMetadata: api.RequestMetadata{RequestID: "server-onboarding-public", ClientRequestID: "client-onboarding-public", IdempotencyKey: "onboarding-public-key-0001", ClientIPHash: bytes.Repeat([]byte{0x97}, 32), UserAgentHash: bytes.Repeat([]byte{0x98}, 32)}, PrincipalKind: trustedcontext.PublicRequest, CurrentRole: "Product manager", TargetRole: "AI product lead", ExperienceSummary: "Built B2B workflow products", WeeklyMinutes: 300}
	results := concurrentCalls(t, 32, func() (api.OnboardingCreateResult, error) { return service.CreateOnboarding(ctx, publicCommand) })
	first := results[0]
	if first.ID == "" || first.AnonymousHandle == "" || first.Version != 1 || first.Status != "collecting" {
		t.Fatalf("first=%#v", first)
	}
	for _, result := range results {
		if result.ID != first.ID || result.AnonymousHandle != first.AnonymousHandle || !result.HandleExpiresAt.Equal(first.HandleExpiresAt) || result.Status != first.Status || result.Version != first.Version {
			t.Fatalf("non-idempotent result=%#v first=%#v", result, first)
		}
	}
	resolver := AnonymousSessionResolver{Pool: pool, Verifier: anonymoussession.Verifier{Keys: map[string][]byte{"anonymous-onboarding": handleKey}, DigestPepper: handlePepper}, Now: func() time.Time { return now }}
	principal, err := resolver.Resolve(ctx, first.AnonymousHandle)
	if err != nil {
		t.Fatal(err)
	}
	var subjects, sessions, events, outbox, idempotencyRows int
	var lastSequence uint64
	var experienceManifest string
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM identity.anonymous_subjects WHERE system_tenant_id=$1),(SELECT count(*) FROM identity.onboarding_sessions WHERE tenant_id=$1),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND user_id=$2),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1),(SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND operation_id='onboarding.create'),(SELECT last_seq FROM agent.event_cursors WHERE tenant_id=$1 AND user_id=$2),(SELECT experience_payload_ref FROM identity.onboarding_sessions WHERE id=$3)`, anonymousTenant, principal.UserID, first.ID).Scan(&subjects, &sessions, &events, &outbox, &idempotencyRows, &lastSequence, &experienceManifest); err != nil {
		t.Fatal(err)
	}
	if subjects != 1 || sessions != 1 || events != 2 || outbox != 2 || idempotencyRows != 1 || lastSequence != 2 {
		t.Fatalf("subjects=%d sessions=%d events=%d outbox=%d idempotency=%d seq=%d", subjects, sessions, events, outbox, idempotencyRows, lastSequence)
	}
	var manifest payload.Manifest
	if err = json.Unmarshal([]byte(experienceManifest), &manifest); err != nil {
		t.Fatal(err)
	}
	body, err := payloadStore.Get(ctx, payload.Descriptor{TenantID: anonymousTenant, ObjectID: first.ID, Class: "onboarding-body", ContentType: "application/json"}, manifest)
	if err != nil || !bytes.Contains(body, []byte(publicCommand.ExperienceSummary)) {
		t.Fatalf("body=%s error=%v", body, err)
	}
	updatedRole := "Senior frontend engineer"
	updatedStory := "Led a migration across three teams and handled production incidents"
	updatedMinutes := 240
	updateCommand := api.OnboardingUpdateCommand{RequestMetadata: api.RequestMetadata{RequestID: "40000000-0000-4000-8000-000000000099", ClientRequestID: "client-onboarding-update", IdempotencyKey: "onboarding-update-key-0001"}, PrincipalKind: trustedcontext.AnonymousUser, OnboardingSessionID: first.ID, UserID: principal.UserID, TenantID: principal.TenantID, AnonymousSubjectID: principal.AnonymousSubjectID, CurrentRole: &updatedRole, ExperienceSummary: &updatedStory, WeeklyMinutes: &updatedMinutes, ExpectedOnboardingVersion: 1}
	updated, err := service.UpdateOnboarding(ctx, updateCommand)
	if err != nil || updated.ID != first.ID || updated.Version != 2 || updated.Status != "collecting" {
		t.Fatalf("updated=%#v error=%v", updated, err)
	}
	replayed, err := service.UpdateOnboarding(ctx, updateCommand)
	if err != nil || replayed != updated {
		t.Fatalf("replayed=%#v updated=%#v error=%v", replayed, updated, err)
	}
	stale := updateCommand
	stale.IdempotencyKey = "onboarding-update-key-0002"
	stale.ClientRequestID = "client-onboarding-update-stale"
	if _, err = service.UpdateOnboarding(ctx, stale); !errors.Is(err, api.ErrVersionConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	var updatedVersion uint64
	var updatedManifest string
	var updatedEvents int
	if err = admin.QueryRow(ctx, `SELECT s.version,s.experience_payload_ref,(SELECT count(*) FROM agent.events e WHERE e.tenant_id=s.tenant_id AND e.aggregate_kind='onboarding_session' AND e.aggregate_id=s.id AND e.aggregate_version=2) FROM identity.onboarding_sessions s WHERE s.id=$1`, first.ID).Scan(&updatedVersion, &updatedManifest, &updatedEvents); err != nil {
		t.Fatal(err)
	}
	if updatedVersion != 2 || updatedEvents != 1 {
		t.Fatalf("version=%d update events=%d", updatedVersion, updatedEvents)
	}
	if err = json.Unmarshal([]byte(updatedManifest), &manifest); err != nil {
		t.Fatal(err)
	}
	body, err = payloadStore.Get(ctx, payload.Descriptor{TenantID: anonymousTenant, ObjectID: first.ID, Class: "onboarding-body", ContentType: "application/json"}, manifest)
	if err != nil || !bytes.Contains(body, []byte(updatedRole)) || !bytes.Contains(body, []byte(updatedStory)) || !bytes.Contains(body, []byte(`"weekly_minutes":240`)) {
		t.Fatalf("updated body=%s error=%v", body, err)
	}
	routeService := OnboardingRouteService{Pool: pool, Payloads: payloadStore, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, StoreEpoch: storeEpoch, IdentityKey: bytes.Repeat([]byte{0x99}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0x9a}, 32), RequestDigestPepper: bytes.Repeat([]byte{0x9b}, 32), IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	routeCommand := api.OnboardingRouteCommand{RequestMetadata: api.RequestMetadata{RequestID: "40000000-0000-4000-8000-000000000100", ClientRequestID: "client-onboarding-route", IdempotencyKey: "onboarding-route-key-0001"}, OnboardingSessionID: first.ID, TenantID: principal.TenantID, UserID: principal.UserID, AnonymousSubjectID: principal.AnonymousSubjectID, SourceRoleProfileID: sourceRoleID, TargetRoleProfileID: targetRoleID, ConfirmedClaimIDs: []string{}, ExpectedOnboardingVersion: 2}
	routeResults := concurrentCalls(t, 32, func() (api.OnboardingRouteRequestResult, error) { return routeService.RequestRoute(ctx, routeCommand) })
	firstRoute := routeResults[0]
	if firstRoute.RunID == "" || firstRoute.Status != "accepted" || firstRoute.Version != 3 || !firstRoute.AcceptedAt.Equal(now) {
		t.Fatalf("first route=%#v", firstRoute)
	}
	for _, result := range routeResults {
		if result.RunID != firstRoute.RunID || result.Status != firstRoute.Status || result.Version != firstRoute.Version || !result.AcceptedAt.Equal(firstRoute.AcceptedAt) {
			t.Fatalf("non-idempotent route=%#v first=%#v", result, firstRoute)
		}
	}
	route, err := routeService.GetRoute(ctx, first.ID, principal.TenantID, principal.UserID, principal.AnonymousSubjectID)
	if err != nil || route.ID != first.ID || route.Version != 3 || route.Status != "route_generating" || route.MissionID == nil || *route.MissionID != firstRoute.RunID || route.RouteRevisionID != nil || string(route.Route) != "null" {
		t.Fatalf("route=%#v error=%v", route, err)
	}
	if _, err = routeService.GetRoute(ctx, first.ID, principal.TenantID, principal.UserID, "6b000000-0000-4000-8000-000000000099"); !errors.Is(err, api.ErrPermissionDenied) {
		t.Fatalf("cross-subject route read error=%v", err)
	}
	staleRoute := routeCommand
	staleRoute.IdempotencyKey = "onboarding-route-key-0002"
	staleRoute.ClientRequestID = "client-onboarding-route-stale"
	if _, err = routeService.RequestRoute(ctx, staleRoute); !errors.Is(err, api.ErrVersionConflict) {
		t.Fatalf("stale route error=%v", err)
	}
	var routeMissions, routeEvents, routeOutbox, routeIdempotency int
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM product.missions WHERE tenant_id=$1 AND id=$2),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND aggregate_kind='onboarding_session' AND aggregate_id=$3 AND event_type='RoutePreviewRequested'),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND aggregate_kind='mission' AND aggregate_id=$2),(SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND operation_id='onboarding.route-preview')`, anonymousTenant, firstRoute.RunID, first.ID).Scan(&routeMissions, &routeEvents, &routeOutbox, &routeIdempotency); err != nil {
		t.Fatal(err)
	}
	if routeMissions != 1 || routeEvents != 1 || routeOutbox != 1 || routeIdempotency != 1 {
		t.Fatalf("route missions=%d events=%d outbox=%d idempotency=%d", routeMissions, routeEvents, routeOutbox, routeIdempotency)
	}
	conflicting := publicCommand
	conflicting.WeeklyMinutes++
	if _, err = service.CreateOnboarding(ctx, conflicting); !errors.Is(err, api.ErrIdempotencyConflict) {
		t.Fatalf("conflict error=%v", err)
	}
	anonymousCommand := publicCommand
	anonymousCommand.RequestID = "server-onboarding-anonymous"
	anonymousCommand.ClientRequestID = "client-onboarding-anonymous"
	anonymousCommand.IdempotencyKey = "onboarding-anonymous-key-0001"
	anonymousCommand.PrincipalKind = trustedcontext.AnonymousUser
	anonymousCommand.UserID = principal.UserID
	anonymousCommand.TenantID = principal.TenantID
	anonymousCommand.AnonymousSubjectID = principal.AnonymousSubjectID
	anonymousResult, err := service.CreateOnboarding(ctx, anonymousCommand)
	if err != nil || anonymousResult.ID == first.ID || anonymousResult.AnonymousHandle != "" {
		t.Fatalf("anonymous result=%#v error=%v", anonymousResult, err)
	}
	authenticatedCommand := publicCommand
	authenticatedCommand.RequestID = "server-onboarding-authenticated"
	authenticatedCommand.ClientRequestID = "client-onboarding-authenticated"
	authenticatedCommand.IdempotencyKey = "onboarding-authenticated-key-0001"
	authenticatedCommand.PrincipalKind = trustedcontext.AuthenticatedUser
	authenticatedCommand.UserID = formalUser
	authenticatedCommand.TenantID = personalTenant
	authenticatedResult, err := service.CreateOnboarding(ctx, authenticatedCommand)
	if err != nil || authenticatedResult.ID == "" || authenticatedResult.AnonymousHandle != "" {
		t.Fatalf("authenticated result=%#v error=%v", authenticatedResult, err)
	}
	var anonymousSessionCount, formalSessionCount int
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM identity.onboarding_sessions WHERE tenant_id=$1 AND anonymous_subject_id=$2),(SELECT count(*) FROM identity.onboarding_sessions WHERE tenant_id=$3 AND user_id=$4 AND anonymous_subject_id IS NULL)`, anonymousTenant, principal.AnonymousSubjectID, personalTenant, formalUser).Scan(&anonymousSessionCount, &formalSessionCount); err != nil || anonymousSessionCount != 2 || formalSessionCount != 1 {
		t.Fatalf("anonymous sessions=%d formal sessions=%d error=%v", anonymousSessionCount, formalSessionCount, err)
	}
}
