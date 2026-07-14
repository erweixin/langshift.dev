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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/identity/password"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestInvitationLifecycleIsTenantAdminScopedCapabilityBoundAndIdempotent(t *testing.T) {
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
	now := time.Unix(1_800_001_100, 0).UTC()
	const publicTenant = "20000000-0000-0000-0000-000000001101"
	const adminUser = "10000000-0000-0000-0000-000000001101"
	const inviteeUser = "10000000-0000-0000-0000-000000001102"
	const adminPersonal = "20000000-0000-0000-0000-000000001102"
	const inviteePersonal = "20000000-0000-0000-0000-000000001103"
	const enterprise = "20000000-0000-0000-0000-000000001104"
	const adminPersonalMembership = "30000000-0000-0000-0000-000000001101"
	const inviteePersonalMembership = "30000000-0000-0000-0000-000000001102"
	const adminEnterpriseMembership = "30000000-0000-0000-0000-000000001103"
	const adminEmail = "invite-admin@example.com"
	const inviteeEmail = "invitee@example.com"
	const passwordValue = "invitation lifecycle password"
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users (id,normalized_email,email_verified_at,locale,status) VALUES ($1,$2,$3,'en','active'),($4,$5,$3,'en','active')`, []any{adminUser, adminEmail, now, inviteeUser, inviteeEmail}},
		{`INSERT INTO identity.tenants (id,kind,name,status,region) VALUES ($1,'anonymous_system','Invite Public','active','US')`, []any{publicTenant}},
		{`INSERT INTO identity.tenants (id,kind,name,status,region,owner_user_id) VALUES ($1,'personal','Admin Personal','active','US',$2),($3,'personal','Invitee Personal','active','US',$4)`, []any{adminPersonal, adminUser, inviteePersonal, inviteeUser}},
		{`INSERT INTO identity.tenants (id,kind,name,status,region) VALUES ($1,'enterprise','Enterprise','active','US')`, []any{enterprise}},
		{`INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ($1,$2,$3,'owner','active',$7),($4,$5,$6,'owner','active',$7),($8,$9,$3,'owner','active',$7)`, []any{adminPersonalMembership, adminPersonal, adminUser, inviteePersonalMembership, inviteePersonal, inviteeUser, now, adminEnterpriseMembership, enterprise}},
	}
	for _, s := range statements {
		if _, err = admin.Exec(ctx, s.query, s.args...); err != nil {
			t.Fatal(err)
		}
	}
	hasher := password.Hasher{Parameters: password.Parameters{Version: password.CurrentVersion, Time: 1, MemoryKiB: 8 * 1024, Threads: 1, KeyLength: 32}, Pepper: bytes.Repeat([]byte{0xe1}, 32)}
	hash, parameters, err := hasher.Hash(passwordValue)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(parameters)
	if _, err = admin.Exec(ctx, `INSERT INTO identity.password_credentials (id,user_id,password_hash,algorithm,parameters,password_changed_at) VALUES ('31000000-0000-0000-0000-000000001101',$1,$3,'argon2id',$4,$5),('31000000-0000-0000-0000-000000001102',$2,$3,'argon2id',$4,$5)`, adminUser, inviteeUser, hash, encoded, now); err != nil {
		t.Fatal(err)
	}
	dummyHash, dummyParams, _ := hasher.Hash("invitation dummy password")
	blobs := &authMemoryBlobs{values: map[string][]byte{}}
	store := payload.EnvelopeStore{Keys: authKeyProvider{key: payload.Key{ID: "invite-vault-v1", Material: bytes.Repeat([]byte{0xe2}, 32)}}, Blobs: blobs}
	importCSV := []byte("email,role\nbulk-one@example.com,member\nbad-email,member\ninvitee@example.com,member\nbulk-two@example.com,reviewer\nbulk-one@example.com,admin\n")
	importDigest := sha256.Sum256(importCSV)
	importRef := "s3://imports/invitations.csv"
	service := AuthService{Pool: pool, ImportSources: invitationImportSource{objects: map[string][]byte{importRef: importCSV}}, Passwords: hasher, PasswordPolicy: password.Policy{Checker: password.NewDigestSet([]string{"known compromised password value"})}, DummyPasswordHash: dummyHash, DummyPasswordParameters: dummyParams, VerificationTokens: opaque.Manager{Purpose: "email-verification", Pepper: bytes.Repeat([]byte{0xe3}, 32)}, PasswordResetTokens: opaque.Manager{Purpose: "password-reset", Pepper: bytes.Repeat([]byte{0xe4}, 32)}, EmailChangeTokens: opaque.Manager{Purpose: "email-change", Pepper: bytes.Repeat([]byte{0xe5}, 32)}, InvitationTokens: opaque.Manager{Purpose: "invitation", Pepper: bytes.Repeat([]byte{0xe6}, 32)}, SessionPepper: bytes.Repeat([]byte{0xe7}, 32), CSRFPepper: bytes.Repeat([]byte{0xe8}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0xe9}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xea}, 32), IdentityKey: bytes.Repeat([]byte{0xeb}, 32), CursorKey: bytes.Repeat([]byte{0xec}, 32), PublicTenantID: publicTenant, StoreEpoch: "40000000-0000-0000-0000-000000001101", Region: "US", VerificationTTL: 24 * time.Hour, PasswordResetTTL: 30 * time.Minute, EmailChangeTTL: 24 * time.Hour, ReauthenticationTTL: 15 * time.Minute, ErasureGracePeriod: 7 * 24 * time.Hour, SessionTTL: 30 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour, Payloads: store, Random: rand.Reader, Now: func() time.Time { return now }}
	requestMetadata := func(id, key string) api.RequestMetadata {
		return api.RequestMetadata{RequestID: "server-" + id, ClientRequestID: id, IdempotencyKey: key, ClientIPHash: bytes.Repeat([]byte{0xf1}, 32), UserAgentHash: bytes.Repeat([]byte{0xf2}, 32)}
	}
	login := func(email, key string) api.LoginResult {
		result, loginErr := service.Login(ctx, api.LoginCommand{RequestMetadata: requestMetadata("login-"+key, key), NormalizedEmail: email, Password: passwordValue})
		if loginErr != nil {
			t.Fatal(loginErr)
		}
		return result
	}
	adminLogin := login(adminEmail, "invite-admin-login-key")
	inviteeLogin := login(inviteeEmail, "invitee-login-key-01")
	if _, err = (SessionStore{Pool: pool, Now: service.Now}).SwitchActiveTenant(ctx, SwitchTenantInput{SessionID: adminLogin.SessionID, UserID: adminUser, TargetTenantID: enterprise, ExpectedVersion: 1, SecurityEventID: "50000000-0000-0000-0000-000000001101", RequestID: "switch-admin-enterprise", IPHash: bytes.Repeat([]byte{0xf1}, 32), UserAgentHash: bytes.Repeat([]byte{0xf2}, 32)}); err != nil {
		t.Fatal(err)
	}
	adminAuth := func(id, key string) api.AuthenticatedRequestMetadata {
		return api.AuthenticatedRequestMetadata{RequestMetadata: requestMetadata(id, key), UserID: adminUser, TenantID: enterprise, MembershipID: adminEnterpriseMembership, SessionID: adminLogin.SessionID}
	}
	inviteeAuth := func(id, key string) api.AuthenticatedRequestMetadata {
		return api.AuthenticatedRequestMetadata{RequestMetadata: requestMetadata(id, key), UserID: inviteeUser, TenantID: inviteePersonal, MembershipID: inviteePersonalMembership, SessionID: inviteeLogin.SessionID}
	}
	create := api.InvitationCreateCommand{AuthenticatedRequestMetadata: adminAuth("invite-create-001", "invite-create-key-0001"), NormalizedEmail: inviteeEmail, Role: "reviewer", ExpiresInDays: 7}
	created := concurrentCalls(t, 12, func() (api.InvitationMutationResult, error) { return service.CreateInvitation(ctx, create) })
	for _, r := range created {
		if r != created[0] || r.Status != "pending" || r.Version != 1 {
			t.Fatalf("create=%#v", r)
		}
	}
	token := latestInvitationToken(t, ctx, admin, store, enterprise, created[0].ID)
	raceStart := make(chan struct{})
	raceErrors := make(chan error, 12)
	raceSuccesses := make(chan api.InvitationMutationResult, 12)
	var raceWait sync.WaitGroup
	for index := range 12 {
		raceWait.Add(1)
		go func() {
			defer raceWait.Done()
			<-raceStart
			raceCommand := api.InvitationCreateCommand{
				AuthenticatedRequestMetadata: adminAuth(fmt.Sprintf("invite-race-%03d", index), fmt.Sprintf("invite-race-key-%04d", index)),
				NormalizedEmail:              "concurrent@example.com",
				Role:                         "member",
				ExpiresInDays:                7,
			}
			result, raceErr := service.CreateInvitation(ctx, raceCommand)
			if raceErr != nil {
				raceErrors <- raceErr
				return
			}
			raceSuccesses <- result
		}()
	}
	close(raceStart)
	raceWait.Wait()
	close(raceErrors)
	close(raceSuccesses)
	conflicts := 0
	for raceErr := range raceErrors {
		if !errors.Is(raceErr, api.ErrStateConflict) {
			t.Fatalf("concurrent distinct-key invitation: %v", raceErr)
		}
		conflicts++
	}
	successes := 0
	var raceInvitation api.InvitationMutationResult
	for result := range raceSuccesses {
		raceInvitation = result
		successes++
	}
	if successes != 1 || conflicts != 11 {
		t.Fatalf("distinct-key invitation successes=%d conflicts=%d", successes, conflicts)
	}
	raceToken := latestInvitationToken(t, ctx, admin, store, enterprise, raceInvitation.ID)
	wrongToken := api.InvitationAcceptCommand{AuthenticatedRequestMetadata: inviteeAuth("invite-wrong-token", "invite-wrong-token-key"), InvitationID: created[0].ID, Token: token + "x", ExpectedVersion: 1}
	if _, err = service.AcceptInvitation(ctx, wrongToken); !errors.Is(err, api.ErrInvalidCredentials) {
		t.Fatalf("wrong token=%v", err)
	}
	accept := api.InvitationAcceptCommand{AuthenticatedRequestMetadata: inviteeAuth("invite-accept-001", "invite-accept-key-0001"), InvitationID: created[0].ID, Token: token, ExpectedVersion: 1}
	accepted := concurrentCalls(t, 12, func() (api.InvitationMutationResult, error) { return service.AcceptInvitation(ctx, accept) })
	for _, r := range accepted {
		if r != accepted[0] || r.Status != "accepted" || r.Version != 2 {
			t.Fatalf("accept=%#v", r)
		}
	}
	used := accept
	used.IdempotencyKey = "invite-accept-key-0002"
	used.RequestID = "server-invite-used"
	used.ClientRequestID = "invite-used"
	if _, err = service.AcceptInvitation(ctx, used); !errors.Is(err, api.ErrVersionConflict) {
		t.Fatalf("used token=%v", err)
	}
	var inviteeEnterpriseMembership string
	if err = admin.QueryRow(ctx, `SELECT id::text FROM identity.memberships WHERE tenant_id=$1 AND user_id=$2 AND role='reviewer' AND status='active'`, enterprise, inviteeUser).Scan(&inviteeEnterpriseMembership); err != nil {
		t.Fatal(err)
	}
	if _, err = (SessionStore{Pool: pool, Now: service.Now}).SwitchActiveTenant(ctx, SwitchTenantInput{SessionID: inviteeLogin.SessionID, UserID: inviteeUser, TargetTenantID: enterprise, ExpectedVersion: 1, SecurityEventID: "50000000-0000-0000-0000-000000001102", RequestID: "switch-invitee-enterprise", IPHash: bytes.Repeat([]byte{0xf1}, 32), UserAgentHash: bytes.Repeat([]byte{0xf2}, 32)}); err != nil {
		t.Fatal(err)
	}
	reviewerMetadata := api.AuthenticatedRequestMetadata{RequestMetadata: requestMetadata("reviewer-invite", "reviewer-invite-key-1"), UserID: inviteeUser, TenantID: enterprise, MembershipID: inviteeEnterpriseMembership, SessionID: inviteeLogin.SessionID}
	if _, err = service.CreateInvitation(ctx, api.InvitationCreateCommand{AuthenticatedRequestMetadata: reviewerMetadata, NormalizedEmail: "another@example.com", Role: "member", ExpiresInDays: 7}); !errors.Is(err, api.ErrPermissionDenied) {
		t.Fatalf("reviewer invite=%v", err)
	}
	rejectCreate := create
	rejectCreate.NormalizedEmail = "reject@example.com"
	rejectCreate.IdempotencyKey = "invite-create-reject-key"
	rejectCreate.RequestID = "server-invite-create-reject"
	rejectCreate.ClientRequestID = "invite-create-reject"
	rejectInvitation, err := service.CreateInvitation(ctx, rejectCreate)
	if err != nil {
		t.Fatal(err)
	}
	rejectToken := latestInvitationToken(t, ctx, admin, store, enterprise, rejectInvitation.ID)
	if _, err = admin.Exec(ctx, `UPDATE identity.users SET normalized_email='reject@example.com' WHERE id=$1`, inviteeUser); err != nil {
		t.Fatal(err)
	}
	rejectMetadata := reviewerMetadata
	rejectMetadata.RequestMetadata = requestMetadata("invite-reject", "invite-reject-key-0001")
	rejectCommand := api.InvitationRejectCommand{AuthenticatedRequestMetadata: rejectMetadata, InvitationID: rejectInvitation.ID, Token: rejectToken, ExpectedVersion: 1}
	rejected := concurrentCalls(t, 12, func() (api.InvitationMutationResult, error) { return service.RejectInvitation(ctx, rejectCommand) })
	for _, result := range rejected {
		if result != rejected[0] || result.Version != 2 || result.Status != "rejected" {
			t.Fatalf("reject=%#v", result)
		}
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.users SET normalized_email=$1 WHERE id=$2`, inviteeEmail, inviteeUser); err != nil {
		t.Fatal(err)
	}
	usedReject := rejectCommand
	usedReject.IdempotencyKey = "invite-reject-key-0002"
	usedReject.RequestID = "server-invite-reject-used"
	usedReject.ClientRequestID = "invite-reject-used"
	if _, err = service.RejectInvitation(ctx, usedReject); !errors.Is(err, api.ErrVersionConflict) {
		t.Fatalf("used reject token=%v", err)
	}
	reviewerRevokeMetadata := reviewerMetadata
	reviewerRevokeMetadata.RequestMetadata = requestMetadata("reviewer-revoke", "reviewer-revoke-key-01")
	if _, err = service.RevokeInvitation(ctx, api.InvitationRevokeCommand{AuthenticatedRequestMetadata: reviewerRevokeMetadata, InvitationID: raceInvitation.ID, Reason: "unauthorized revoke", ExpectedVersion: 1}); !errors.Is(err, api.ErrPermissionDenied) {
		t.Fatalf("reviewer revoke=%v", err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, now.Add(-16*time.Minute), adminLogin.SessionID); err != nil {
		t.Fatal(err)
	}
	revokeCommand := api.InvitationRevokeCommand{AuthenticatedRequestMetadata: adminAuth("invite-revoke", "invite-revoke-key-0001"), InvitationID: raceInvitation.ID, Reason: "recipient was invited in error", ExpectedVersion: 1}
	if _, err = service.RevokeInvitation(ctx, revokeCommand); !errors.Is(err, api.ErrReauthenticationRequired) {
		t.Fatalf("stale revoke=%v", err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, now, adminLogin.SessionID); err != nil {
		t.Fatal(err)
	}
	revoked := concurrentCalls(t, 12, func() (api.InvitationMutationResult, error) { return service.RevokeInvitation(ctx, revokeCommand) })
	for _, result := range revoked {
		if result != revoked[0] || result.Version != 2 || result.Status != "revoked" {
			t.Fatalf("revoke=%#v", result)
		}
	}
	revokedAcceptMetadata := reviewerMetadata
	revokedAcceptMetadata.RequestMetadata = requestMetadata("invite-revoked-accept", "invite-revoked-accept-key")
	revokedAccept := api.InvitationAcceptCommand{AuthenticatedRequestMetadata: revokedAcceptMetadata, InvitationID: raceInvitation.ID, Token: raceToken, ExpectedVersion: 2}
	if _, err = service.AcceptInvitation(ctx, revokedAccept); !errors.Is(err, api.ErrStateConflict) {
		t.Fatalf("revoked token accept=%v", err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, now.Add(-16*time.Minute), adminLogin.SessionID); err != nil {
		t.Fatal(err)
	}
	importCommand := api.InvitationImportCommand{AuthenticatedRequestMetadata: adminAuth("invite-import-001", "invite-import-key-0001"), ObjectRef: importRef, ContentHash: "sha256:" + hex.EncodeToString(importDigest[:]), ImportKey: "enterprise-import-0001", DefaultRole: "member"}
	if _, err = service.ImportInvitations(ctx, importCommand); !errors.Is(err, api.ErrReauthenticationRequired) {
		t.Fatalf("stale import=%v", err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, now, adminLogin.SessionID); err != nil {
		t.Fatal(err)
	}
	imports := concurrentCalls(t, 12, func() (api.InvitationMutationResult, error) { return service.ImportInvitations(ctx, importCommand) })
	for _, r := range imports {
		if r != imports[0] || r.Status != "queued" || r.Version != 1 {
			t.Fatalf("import=%#v", r)
		}
	}
	duplicateImport := importCommand
	duplicateImport.IdempotencyKey = "invite-import-key-0002"
	duplicateImport.RequestID = "server-import-duplicate"
	duplicateImport.ClientRequestID = "import-duplicate"
	if _, err = service.ImportInvitations(ctx, duplicateImport); !errors.Is(err, api.ErrStateConflict) {
		t.Fatalf("duplicate import=%v", err)
	}
	workCommand := deliveredImportCommand(t, ctx, admin, enterprise, imports[0].ID, "identity.invitation_import.process")
	dispatcher := IdentityImportDispatcher{Service: service, Inbox: eventpostgres.InboxStore{Pool: pool, Epochs: identityEpochAuthority{epoch: service.StoreEpoch}, Tokens: opaque.Manager{Purpose: "identity-import-inbox", Pepper: bytes.Repeat([]byte{0xed}, 32)}, LeaseTTL: 5 * time.Minute, Now: service.Now}}
	processed := make([]ImportDispatchResult, 0, 12)
	for range 12 {
		result, dispatchErr := dispatcher.Dispatch(ctx, workCommand)
		if dispatchErr != nil {
			t.Fatal(dispatchErr)
		}
		processed = append(processed, result)
	}
	claimed, replayed := 0, 0
	for _, result := range processed {
		if !result.Completed || result.TerminalFailure {
			t.Fatalf("processed import=%#v", result)
		}
		if result.Claimed {
			claimed++
			if result.Import.Status != "completed" || result.Import.Version != 2 || result.Import.AcceptedRows != 2 || result.Import.RejectedRows != 3 {
				t.Fatalf("claimed import=%#v", result)
			}
		}
		if result.Replayed {
			replayed++
		}
	}
	if claimed != 1 || replayed != 11 {
		t.Fatalf("dispatcher claimed=%d replayed=%d", claimed, replayed)
	}
	secondCreate := create
	secondCreate.NormalizedEmail = "expiring@example.com"
	secondCreate.IdempotencyKey = "invite-create-key-0002"
	secondCreate.RequestID = "server-invite-expiring"
	secondCreate.ClientRequestID = "invite-expiring"
	second, err := service.CreateInvitation(ctx, secondCreate)
	if err != nil {
		t.Fatal(err)
	}
	secondToken := latestInvitationToken(t, ctx, admin, store, enterprise, second.ID)
	later := now.Add(7*24*time.Hour + time.Second)
	service.Now = func() time.Time { return later }
	expiredMetadata := reviewerMetadata
	expiredMetadata.RequestMetadata = requestMetadata("invite-expired", "invite-expired-key-01")
	expired := api.InvitationAcceptCommand{AuthenticatedRequestMetadata: expiredMetadata, InvitationID: second.ID, Token: secondToken, ExpectedVersion: 1}
	if _, err = service.AcceptInvitation(ctx, expired); !errors.Is(err, api.ErrStateConflict) {
		t.Fatalf("expired=%v", err)
	}
	service.Now = func() time.Time { return now }
	var invitations, memberships, createdEvents, acceptedEvents, rejectedEvents, revokedEvents, membershipEvents, importsCount, importEvents, importCompletedEvents, inboxCompleted int
	checks := []struct {
		q   string
		out *int
	}{{`SELECT count(*) FROM identity.invitations WHERE tenant_id='20000000-0000-0000-0000-000000001104'`, &invitations}, {`SELECT count(*) FROM identity.memberships WHERE tenant_id='20000000-0000-0000-0000-000000001104' AND user_id='10000000-0000-0000-0000-000000001102'`, &memberships}, {`SELECT count(*) FROM agent.events WHERE tenant_id='20000000-0000-0000-0000-000000001104' AND event_type='InvitationCreated'`, &createdEvents}, {`SELECT count(*) FROM agent.events WHERE tenant_id='20000000-0000-0000-0000-000000001104' AND event_type='InvitationAccepted'`, &acceptedEvents}, {`SELECT count(*) FROM agent.events WHERE tenant_id='20000000-0000-0000-0000-000000001104' AND event_type='InvitationRejected'`, &rejectedEvents}, {`SELECT count(*) FROM agent.events WHERE tenant_id='20000000-0000-0000-0000-000000001104' AND event_type='InvitationRevoked'`, &revokedEvents}, {`SELECT count(*) FROM agent.events WHERE tenant_id='20000000-0000-0000-0000-000000001104' AND event_type='MembershipCreated'`, &membershipEvents}, {`SELECT count(*) FROM identity.invitation_imports WHERE tenant_id='20000000-0000-0000-0000-000000001104'`, &importsCount}, {`SELECT count(*) FROM agent.events WHERE tenant_id='20000000-0000-0000-0000-000000001104' AND event_type='InvitationImportQueued'`, &importEvents}, {`SELECT count(*) FROM agent.events WHERE tenant_id='20000000-0000-0000-0000-000000001104' AND event_type='InvitationImportCompleted'`, &importCompletedEvents}, {`SELECT count(*) FROM agent.inbox WHERE tenant_id='20000000-0000-0000-0000-000000001104' AND consumer_name='identity-import-worker' AND status='completed'`, &inboxCompleted}}
	for _, check := range checks {
		if err = admin.QueryRow(ctx, check.q).Scan(check.out); err != nil {
			t.Fatal(err)
		}
	}
	if invitations != 6 || memberships != 1 || createdEvents != 6 || acceptedEvents != 1 || rejectedEvents != 1 || revokedEvents != 1 || membershipEvents != 1 || importsCount != 1 || importEvents != 1 || importCompletedEvents != 1 || inboxCompleted != 1 {
		t.Fatalf("invitations=%d memberships=%d created=%d accepted=%d rejected=%d revoked=%d membership-events=%d imports=%d queued=%d completed=%d inbox=%d", invitations, memberships, createdEvents, acceptedEvents, rejectedEvents, revokedEvents, membershipEvents, importsCount, importEvents, importCompletedEvents, inboxCompleted)
	}
	for _, stored := range blobs.values {
		for _, secret := range []string{token, raceToken, rejectToken, secondToken, inviteeEmail, "recipient was invited in error"} {
			if bytes.Contains(stored, []byte(secret)) {
				t.Fatalf("plaintext invite secret reached object storage: %q", secret)
			}
		}
	}
	digest, _ := service.InvitationTokens.Digest(token)
	var storedDigest []byte
	if err = admin.QueryRow(ctx, `SELECT token_hash FROM identity.invitations WHERE id=$1`, created[0].ID).Scan(&storedDigest); err != nil || !bytes.Equal(storedDigest, digest[:]) {
		t.Fatalf("token digest mismatch error=%v", err)
	}
}

type invitationImportSource struct{ objects map[string][]byte }

func (source invitationImportSource) Get(_ context.Context, ref string) ([]byte, error) {
	value, ok := source.objects[ref]
	if !ok {
		return nil, errors.New("import object not found")
	}
	return append([]byte(nil), value...), nil
}

type identityEpochAuthority struct{ epoch string }

func (authority identityEpochAuthority) CurrentStoreEpoch(context.Context) (string, error) {
	return authority.epoch, nil
}

func deliveredImportCommand(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenantID, aggregateID, commandType string) eventpostgres.DeliveredCommand {
	t.Helper()
	command := eventpostgres.DeliveredCommand{TenantID: tenantID, CommandType: commandType}
	if err := admin.QueryRow(ctx, `SELECT command_id::text,store_epoch::text,aggregate_kind,aggregate_id::text,payload_ref,payload_hash FROM agent.outbox WHERE tenant_id=$1 AND aggregate_id=$2 AND command_type=$3`, tenantID, aggregateID, commandType).Scan(&command.CommandID, &command.StoreEpoch, &command.AggregateKind, &command.AggregateID, &command.PayloadRef, &command.PayloadHash); err != nil {
		t.Fatal(err)
	}
	return command
}

func latestInvitationToken(t *testing.T, ctx context.Context, admin *pgxpool.Pool, store payload.Store, tenantID, invitationID string) string {
	t.Helper()
	var commandID, ref, hash string
	if err := admin.QueryRow(ctx, `SELECT command_id::text,payload_ref,payload_hash FROM agent.outbox WHERE tenant_id=$1 AND aggregate_id=$2 AND command_type='identity.email.invitation' ORDER BY created_at DESC,id DESC LIMIT 1`, tenantID, invitationID).Scan(&commandID, &ref, &hash); err != nil {
		t.Fatal(err)
	}
	encoded, err := store.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: commandID, Class: "mail-command", ContentType: "application/json"}, payload.Manifest{Ref: ref, Hash: hash})
	if err != nil {
		t.Fatal(err)
	}
	var command struct {
		Token string `json:"token"`
	}
	if err = json.Unmarshal(encoded, &command); err != nil || command.Token == "" {
		t.Fatalf("mail=%s error=%v", encoded, err)
	}
	return command.Token
}
