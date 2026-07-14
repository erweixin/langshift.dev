//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/identity/password"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestMembershipLifecycleIsTenantScopedAuditedCASAndSessionSafe(t *testing.T) {
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
	now := time.Unix(1_800_002_200, 0).UTC()
	const publicTenant = "20000000-0000-0000-0000-000000002201"
	const ownerUser = "10000000-0000-0000-0000-000000002201"
	const memberUser = "10000000-0000-0000-0000-000000002202"
	const leaverUser = "10000000-0000-0000-0000-000000002203"
	const ownerPersonal = "20000000-0000-0000-0000-000000002202"
	const memberPersonal = "20000000-0000-0000-0000-000000002203"
	const leaverPersonal = "20000000-0000-0000-0000-000000002204"
	const enterprise = "20000000-0000-0000-0000-000000002205"
	const ownerPersonalMembership = "30000000-0000-0000-0000-000000002201"
	const memberPersonalMembership = "30000000-0000-0000-0000-000000002202"
	const leaverPersonalMembership = "30000000-0000-0000-0000-000000002203"
	const ownerEnterpriseMembership = "30000000-0000-0000-0000-000000002204"
	const memberEnterpriseMembership = "30000000-0000-0000-0000-000000002205"
	const leaverEnterpriseMembership = "30000000-0000-0000-0000-000000002206"
	const missingUser = "10000000-0000-0000-0000-000000002204"
	const missingEnterpriseMembership = "30000000-0000-0000-0000-000000002207"
	const ownerEmail = "membership-owner@example.com"
	const memberEmail = "membership-member@example.com"
	const leaverEmail = "membership-leaver@example.com"
	const passwordValue = "membership lifecycle password"
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users (id,normalized_email,email_verified_at,locale,status) VALUES ($1,$2,$7,'en','active'),($3,$4,$7,'en','active'),($5,$6,$7,'en','active')`, []any{ownerUser, ownerEmail, memberUser, memberEmail, leaverUser, leaverEmail, now}},
		{`INSERT INTO identity.tenants (id,kind,name,status,region) VALUES ($1,'anonymous_system','Membership Public','active','US')`, []any{publicTenant}},
		{`INSERT INTO identity.tenants (id,kind,name,status,region,owner_user_id) VALUES ($1,'personal','Owner Personal','active','US',$2),($3,'personal','Member Personal','active','US',$4),($5,'personal','Leaver Personal','active','US',$6)`, []any{ownerPersonal, ownerUser, memberPersonal, memberUser, leaverPersonal, leaverUser}},
		{`INSERT INTO identity.tenants (id,kind,name,status,region) VALUES ($1,'enterprise','Membership Enterprise','active','US')`, []any{enterprise}},
		{`INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ($1,$2,$3,'owner','active',$13),($4,$5,$6,'owner','active',$13),($7,$8,$9,'owner','active',$13),($10,$11,$3,'owner','active',$13),($12,$11,$6,'member','active',$13),($14,$11,$9,'reviewer','active',$13)`, []any{ownerPersonalMembership, ownerPersonal, ownerUser, memberPersonalMembership, memberPersonal, memberUser, leaverPersonalMembership, leaverPersonal, leaverUser, ownerEnterpriseMembership, enterprise, memberEnterpriseMembership, now, leaverEnterpriseMembership}},
	}
	for _, statement := range statements {
		if _, err = admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	hasher := password.Hasher{Parameters: password.Parameters{Version: password.CurrentVersion, Time: 1, MemoryKiB: 8 * 1024, Threads: 1, KeyLength: 32}, Pepper: bytes.Repeat([]byte{0xd1}, 32)}
	hash, parameters, err := hasher.Hash(passwordValue)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(parameters)
	if _, err = admin.Exec(ctx, `INSERT INTO identity.password_credentials (id,user_id,password_hash,algorithm,parameters,password_changed_at) VALUES ('31000000-0000-0000-0000-000000002201',$1,$4,'argon2id',$5,$6),('31000000-0000-0000-0000-000000002202',$2,$4,'argon2id',$5,$6),('31000000-0000-0000-0000-000000002203',$3,$4,'argon2id',$5,$6)`, ownerUser, memberUser, leaverUser, hash, encoded, now); err != nil {
		t.Fatal(err)
	}
	dummyHash, dummyParameters, _ := hasher.Hash("membership dummy password")
	blobs := &authMemoryBlobs{values: map[string][]byte{}}
	store := payload.EnvelopeStore{Keys: authKeyProvider{key: payload.Key{ID: "membership-vault-v1", Material: bytes.Repeat([]byte{0xd2}, 32)}}, Blobs: blobs}
	service := AuthService{Pool: pool, Passwords: hasher, PasswordPolicy: password.Policy{Checker: password.NewDigestSet([]string{"known compromised password value"})}, DummyPasswordHash: dummyHash, DummyPasswordParameters: dummyParameters, VerificationTokens: opaque.Manager{Purpose: "email-verification", Pepper: bytes.Repeat([]byte{0xd3}, 32)}, PasswordResetTokens: opaque.Manager{Purpose: "password-reset", Pepper: bytes.Repeat([]byte{0xd4}, 32)}, EmailChangeTokens: opaque.Manager{Purpose: "email-change", Pepper: bytes.Repeat([]byte{0xd5}, 32)}, InvitationTokens: opaque.Manager{Purpose: "invitation", Pepper: bytes.Repeat([]byte{0xd6}, 32)}, SessionPepper: bytes.Repeat([]byte{0xd7}, 32), CSRFPepper: bytes.Repeat([]byte{0xd8}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0xd9}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xda}, 32), IdentityKey: bytes.Repeat([]byte{0xdb}, 32), CursorKey: bytes.Repeat([]byte{0xdc}, 32), PublicTenantID: publicTenant, StoreEpoch: "40000000-0000-0000-0000-000000002201", Region: "US", VerificationTTL: 24 * time.Hour, PasswordResetTTL: 30 * time.Minute, EmailChangeTTL: 24 * time.Hour, ReauthenticationTTL: 15 * time.Minute, ErasureGracePeriod: 7 * 24 * time.Hour, SessionTTL: 30 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour, Payloads: store, Random: rand.Reader, Now: func() time.Time { return now }}
	requestMetadata := func(id, key string) api.RequestMetadata {
		return api.RequestMetadata{RequestID: "server-" + id, ClientRequestID: id, IdempotencyKey: key, ClientIPHash: bytes.Repeat([]byte{0xe1}, 32), UserAgentHash: bytes.Repeat([]byte{0xe2}, 32)}
	}
	login := func(email, key string) api.LoginResult {
		result, loginErr := service.Login(ctx, api.LoginCommand{RequestMetadata: requestMetadata("login-"+key, key), NormalizedEmail: email, Password: passwordValue})
		if loginErr != nil {
			t.Fatal(loginErr)
		}
		return result
	}
	ownerLogin := login(ownerEmail, "membership-owner-login-key")
	memberEnterpriseOne := login(memberEmail, "membership-member-login-1")
	memberEnterpriseTwo := login(memberEmail, "membership-member-login-2")
	memberPersonalLogin := login(memberEmail, "membership-member-login-3")
	leaverLogin := login(leaverEmail, "membership-leaver-login")
	switchTenant := func(login api.LoginResult, userID, tenantID, eventID, requestID string) {
		if _, switchErr := (SessionStore{Pool: pool, Now: service.Now}).SwitchActiveTenant(ctx, SwitchTenantInput{SessionID: login.SessionID, UserID: userID, TargetTenantID: tenantID, ExpectedVersion: 1, SecurityEventID: eventID, RequestID: requestID, IPHash: bytes.Repeat([]byte{0xe1}, 32), UserAgentHash: bytes.Repeat([]byte{0xe2}, 32)}); switchErr != nil {
			t.Fatal(switchErr)
		}
	}
	switchTenant(ownerLogin, ownerUser, enterprise, "50000000-0000-0000-0000-000000002201", "switch-membership-owner")
	switchTenant(memberEnterpriseOne, memberUser, enterprise, "50000000-0000-0000-0000-000000002202", "switch-membership-member-1")
	switchTenant(memberEnterpriseTwo, memberUser, enterprise, "50000000-0000-0000-0000-000000002203", "switch-membership-member-2")
	switchTenant(leaverLogin, leaverUser, enterprise, "50000000-0000-0000-0000-000000002204", "switch-membership-leaver")
	ownerMetadata := func(id, key string) api.AuthenticatedRequestMetadata {
		return api.AuthenticatedRequestMetadata{RequestMetadata: requestMetadata(id, key), UserID: ownerUser, TenantID: enterprise, MembershipID: ownerEnterpriseMembership, SessionID: ownerLogin.SessionID}
	}
	memberMetadata := api.AuthenticatedRequestMetadata{RequestMetadata: requestMetadata("member-list", ""), UserID: memberUser, TenantID: enterprise, MembershipID: memberEnterpriseMembership, SessionID: memberEnterpriseOne.SessionID}
	firstPage, err := service.ListMemberships(ctx, api.MembershipsQuery{AuthenticatedRequestMetadata: ownerMetadata("membership-list-1", ""), Limit: 2})
	if err != nil || len(firstPage.Items) != 2 || firstPage.NextCursor == nil {
		t.Fatalf("first page=%#v error=%v", firstPage, err)
	}
	secondPage, err := service.ListMemberships(ctx, api.MembershipsQuery{AuthenticatedRequestMetadata: ownerMetadata("membership-list-2", ""), Limit: 2, Cursor: *firstPage.NextCursor})
	if err != nil || len(secondPage.Items) != 1 {
		t.Fatalf("second page=%#v error=%v", secondPage, err)
	}
	if _, err = service.ListMemberships(ctx, api.MembershipsQuery{AuthenticatedRequestMetadata: memberMetadata, Limit: 2}); !errors.Is(err, api.ErrPermissionDenied) {
		t.Fatalf("member list error=%v", err)
	}
	personalMetadata := api.AuthenticatedRequestMetadata{RequestMetadata: requestMetadata("cross-cursor", ""), UserID: memberUser, TenantID: memberPersonal, MembershipID: memberPersonalMembership, SessionID: memberPersonalLogin.SessionID}
	if _, err = service.ListMemberships(ctx, api.MembershipsQuery{AuthenticatedRequestMetadata: personalMetadata, Limit: 2, Cursor: *firstPage.NextCursor}); !errors.Is(err, api.ErrValidation) {
		t.Fatalf("cross-scope cursor error=%v", err)
	}
	deactivate := api.MembershipDeactivateCommand{AuthenticatedRequestMetadata: ownerMetadata("membership-deactivate", "membership-deactivate-key-01"), MembershipID: memberEnterpriseMembership, Action: "deactivate", Reason: "employment relationship ended", ExpectedVersion: 1}
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, now.Add(-16*time.Minute), ownerLogin.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.DeactivateMembership(ctx, deactivate); !errors.Is(err, api.ErrReauthenticationRequired) {
		t.Fatalf("stale deactivation=%v", err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, now, ownerLogin.SessionID); err != nil {
		t.Fatal(err)
	}
	deactivated := concurrentCalls(t, 12, func() (api.MembershipMutationResult, error) { return service.DeactivateMembership(ctx, deactivate) })
	for _, result := range deactivated {
		if result != deactivated[0] || result.ID != memberEnterpriseMembership || result.Version != 2 || result.Status != "suspended" {
			t.Fatalf("deactivation=%#v", result)
		}
	}
	var revokedEnterpriseSessions, activePersonalSessions int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.sessions WHERE user_id=$1 AND active_tenant_id=$2 AND revoked_at IS NOT NULL`, memberUser, enterprise).Scan(&revokedEnterpriseSessions); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.sessions WHERE id=$1 AND active_tenant_id=$2 AND revoked_at IS NULL`, memberPersonalLogin.SessionID, memberPersonal).Scan(&activePersonalSessions); err != nil {
		t.Fatal(err)
	}
	if revokedEnterpriseSessions != 2 || activePersonalSessions != 1 {
		t.Fatalf("revoked enterprise=%d active personal=%d", revokedEnterpriseSessions, activePersonalSessions)
	}
	leaverMetadata := api.AuthenticatedRequestMetadata{RequestMetadata: requestMetadata("membership-leave", "membership-leave-key-0001"), UserID: leaverUser, TenantID: enterprise, MembershipID: leaverEnterpriseMembership, SessionID: leaverLogin.SessionID}
	left := concurrentCalls(t, 12, func() (api.MembershipMutationResult, error) {
		return service.DeactivateMembership(ctx, api.MembershipDeactivateCommand{AuthenticatedRequestMetadata: leaverMetadata, MembershipID: leaverEnterpriseMembership, Action: "leave", Reason: "voluntary departure", ExpectedVersion: 1})
	})
	for _, result := range left {
		if result != left[0] || result.Version != 2 || result.Status != "left" {
			t.Fatalf("leave=%#v", result)
		}
	}
	ownerLeave := api.MembershipDeactivateCommand{AuthenticatedRequestMetadata: ownerMetadata("owner-leave", "owner-leave-key-0001"), MembershipID: ownerEnterpriseMembership, Action: "leave", Reason: "attempt last owner exit", ExpectedVersion: 1}
	if _, err = service.DeactivateMembership(ctx, ownerLeave); !errors.Is(err, api.ErrStateConflict) {
		t.Fatalf("last owner leave=%v", err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.users (id,normalized_email,email_verified_at,locale,status) VALUES ($1,'membership-missing@example.com',$2,'en','active')`, missingUser, now); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ($1,$2,$3,'member','active',$4)`, missingEnterpriseMembership, enterprise, missingUser, now); err != nil {
		t.Fatal(err)
	}
	importCSV := []byte("email,role\nmembership-owner@example.com,owner\nmembership-member@example.com,reviewer\n")
	importDigest := sha256.Sum256(importCSV)
	importRef := "s3://imports/members.csv"
	service.ImportSources = invitationImportSource{objects: map[string][]byte{importRef: importCSV}}
	importCommand := api.MembershipImportCommand{AuthenticatedRequestMetadata: ownerMetadata("membership-import", "membership-import-key-01"), ObjectRef: importRef, ContentHash: "sha256:" + hex.EncodeToString(importDigest[:]), ImportKey: "membership-import-batch-01", Mode: "deactivate_missing"}
	imports := concurrentCalls(t, 12, func() (api.MembershipMutationResult, error) { return service.ImportMemberships(ctx, importCommand) })
	for _, result := range imports {
		if result != imports[0] || result.Version != 1 || result.Status != "queued" {
			t.Fatalf("import=%#v", result)
		}
	}
	duplicateImport := importCommand
	duplicateImport.IdempotencyKey = "membership-import-key-02"
	duplicateImport.RequestID = "server-membership-import-duplicate"
	duplicateImport.ClientRequestID = "membership-import-duplicate"
	if _, err = service.ImportMemberships(ctx, duplicateImport); !errors.Is(err, api.ErrStateConflict) {
		t.Fatalf("duplicate import=%v", err)
	}
	processed := concurrentCalls(t, 12, func() (ImportProcessResult, error) {
		return service.ProcessMembershipImport(ctx, enterprise, imports[0].ID)
	})
	for _, result := range processed {
		if result != processed[0] || result.Status != "completed" || result.Version != 2 || result.AcceptedRows != 2 || result.RejectedRows != 0 || result.DeactivatedRows != 1 {
			t.Fatalf("processed membership import=%#v", result)
		}
	}
	var reactivated, missingSuspended, leftCount, deactivatedEvents, reactivatedEvents, sessionEvents, importsCount, importEvents, importCompletedEvents, listAudits int
	checks := []struct {
		query string
		out   *int
	}{
		{`SELECT count(*) FROM identity.memberships WHERE id='30000000-0000-0000-0000-000000002205' AND status='active' AND role='reviewer' AND version=3`, &reactivated},
		{`SELECT count(*) FROM identity.memberships WHERE id='30000000-0000-0000-0000-000000002207' AND status='suspended' AND version=2`, &missingSuspended},
		{`SELECT count(*) FROM identity.memberships WHERE id='30000000-0000-0000-0000-000000002206' AND status='left' AND version=2`, &leftCount},
		{`SELECT count(*) FROM agent.events WHERE tenant_id='20000000-0000-0000-0000-000000002205' AND event_type='MembershipDeactivated'`, &deactivatedEvents},
		{`SELECT count(*) FROM agent.events WHERE tenant_id='20000000-0000-0000-0000-000000002205' AND event_type='MembershipReactivated'`, &reactivatedEvents},
		{`SELECT count(*) FROM agent.events WHERE tenant_id='20000000-0000-0000-0000-000000002205' AND event_type='SessionRevoked'`, &sessionEvents},
		{`SELECT count(*) FROM identity.membership_imports WHERE tenant_id='20000000-0000-0000-0000-000000002205'`, &importsCount},
		{`SELECT count(*) FROM agent.events WHERE tenant_id='20000000-0000-0000-0000-000000002205' AND event_type='MembershipImportQueued'`, &importEvents},
		{`SELECT count(*) FROM agent.events WHERE tenant_id='20000000-0000-0000-0000-000000002205' AND event_type='MembershipImportCompleted'`, &importCompletedEvents},
		{`SELECT count(*) FROM identity.security_events WHERE tenant_id='20000000-0000-0000-0000-000000002205' AND event_type='memberships_listed'`, &listAudits},
	}
	for _, check := range checks {
		if err = admin.QueryRow(ctx, check.query).Scan(check.out); err != nil {
			t.Fatal(err)
		}
	}
	if reactivated != 1 || missingSuspended != 1 || leftCount != 1 || deactivatedEvents != 3 || reactivatedEvents != 1 || sessionEvents != 3 || importsCount != 1 || importEvents != 1 || importCompletedEvents != 1 || listAudits != 2 {
		t.Fatalf("reactivated=%d missing-suspended=%d left=%d deactivated-events=%d reactivated-events=%d session-events=%d imports=%d queued=%d completed=%d list-audits=%d", reactivated, missingSuspended, leftCount, deactivatedEvents, reactivatedEvents, sessionEvents, importsCount, importEvents, importCompletedEvents, listAudits)
	}
	for _, stored := range blobs.values {
		for _, secret := range []string{"employment relationship ended", "voluntary departure"} {
			if bytes.Contains(stored, []byte(secret)) {
				t.Fatalf("plaintext membership reason reached object storage: %q", secret)
			}
		}
	}
}
