//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/identity/password"
	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestEmailChangeIsVersionedConfirmedRotatingAndSecretSafe(t *testing.T) {
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

	now := time.Unix(1_800_000_900, 0).UTC()
	const publicTenantID = "20000000-0000-0000-0000-000000000901"
	const userID = "10000000-0000-0000-0000-000000000901"
	const tenantID = "20000000-0000-0000-0000-000000000902"
	const membershipID = "30000000-0000-0000-0000-000000000901"
	const credentialID = "31000000-0000-0000-0000-000000000901"
	const storeEpoch = "40000000-0000-0000-0000-000000000901"
	const oldEmail = "email-change-old@example.com"
	const firstNewEmail = "email-change-first@example.com"
	const finalEmail = "email-change-final@example.com"
	const currentPassword = "email change current password"

	if _, err = admin.Exec(ctx, `INSERT INTO identity.users (id,version,normalized_email,email_verified_at,locale,status) VALUES ($1,2,$2,$3,'en','active')`, userID, oldEmail, now); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.tenants (id,kind,name,status,region) VALUES ($1,'anonymous_system','Email Public','active','US')`, publicTenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.tenants (id,kind,name,status,region,owner_user_id) VALUES ($1,'personal','Email Lifecycle','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ($1,$2,$3,'owner','active',$4)`, membershipID, tenantID, userID, now); err != nil {
		t.Fatal(err)
	}
	hasher := password.Hasher{Parameters: password.Parameters{Version: password.CurrentVersion, Time: 1, MemoryKiB: 8 * 1024, Threads: 1, KeyLength: 32}, Pepper: bytes.Repeat([]byte{0xa1}, 32)}
	passwordHash, passwordParameters, err := hasher.Hash(currentPassword)
	if err != nil {
		t.Fatal(err)
	}
	encodedParameters, _ := json.Marshal(passwordParameters)
	if _, err = admin.Exec(ctx, `INSERT INTO identity.password_credentials (id,user_id,password_hash,algorithm,parameters,password_changed_at) VALUES ($1,$2,$3,'argon2id',$4,$5)`, credentialID, userID, passwordHash, encodedParameters, now); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO agent.event_cursors (tenant_id,user_id,last_seq) VALUES ($1,$2,2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO agent.events (id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,correlation_id,payload_ref,payload_hash) VALUES
		('50000000-0000-0000-0000-000000000901',$1,$2,1,'UserRegistered',1,'user',$2,1,$3,$4,$4,'{"kind":"user","id":"10000000-0000-0000-0000-000000000901"}','50000000-0000-0000-0000-000000000901','seed://email-user-registered','seed-hash-1'),
		('50000000-0000-0000-0000-000000000902',$1,$2,2,'EmailVerified',1,'user',$2,2,$3,$4,$4,'{"kind":"user","id":"10000000-0000-0000-0000-000000000901"}','50000000-0000-0000-0000-000000000902','seed://email-verified','seed-hash-2')`, tenantID, userID, storeEpoch, now); err != nil {
		t.Fatal(err)
	}

	dummyHash, dummyParameters, err := hasher.Hash("constant email lifecycle dummy password")
	if err != nil {
		t.Fatal(err)
	}
	blobs := &authMemoryBlobs{values: map[string][]byte{}}
	payloadStore := payload.EnvelopeStore{Keys: authKeyProvider{key: payload.Key{ID: "email-vault-key-v1", Material: bytes.Repeat([]byte{0xa2}, 32)}}, Blobs: blobs}
	service := AuthService{
		Pool: pool, Passwords: hasher, PasswordPolicy: password.Policy{Checker: password.NewDigestSet([]string{"known compromised password value"})}, DummyPasswordHash: dummyHash, DummyPasswordParameters: dummyParameters,
		VerificationTokens: opaque.Manager{Purpose: "email-verification", Pepper: bytes.Repeat([]byte{0xa3}, 32)}, PasswordResetTokens: opaque.Manager{Purpose: "password-reset", Pepper: bytes.Repeat([]byte{0xa4}, 32)}, EmailChangeTokens: opaque.Manager{Purpose: "email-change", Pepper: bytes.Repeat([]byte{0xa5}, 32)}, InvitationTokens: opaque.Manager{Purpose: "invitation", Pepper: bytes.Repeat([]byte{0xac}, 32)},
		SessionPepper: bytes.Repeat([]byte{0xa6}, 32), CSRFPepper: bytes.Repeat([]byte{0xa7}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0xa8}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xa9}, 32), IdentityKey: bytes.Repeat([]byte{0xaa}, 32), CursorKey: bytes.Repeat([]byte{0xab}, 32),
		PublicTenantID: publicTenantID, StoreEpoch: storeEpoch, Region: "US", VerificationTTL: 24 * time.Hour, PasswordResetTTL: 30 * time.Minute, EmailChangeTTL: 24 * time.Hour, ReauthenticationTTL: 15 * time.Minute, ErasureGracePeriod: 7 * 24 * time.Hour, SessionTTL: 30 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour,
		Payloads: payloadStore, Random: rand.Reader, Now: func() time.Time { return now },
	}
	requestMetadata := func(requestID, key string) api.RequestMetadata {
		return api.RequestMetadata{RequestID: "server-" + requestID, ClientRequestID: requestID, IdempotencyKey: key, ClientIPHash: bytes.Repeat([]byte{0xb1}, 32), UserAgentHash: bytes.Repeat([]byte{0xb2}, 32)}
	}
	login := func(email, key string) api.LoginResult {
		result, loginErr := service.Login(ctx, api.LoginCommand{RequestMetadata: requestMetadata("login-"+key, key), NormalizedEmail: email, Password: currentPassword})
		if loginErr != nil {
			t.Fatal(loginErr)
		}
		return result
	}
	sessions := []api.LoginResult{login(oldEmail, "email-login-key-01"), login(oldEmail, "email-login-key-02"), login(oldEmail, "email-login-key-03")}
	current := sessions[0]
	authenticated := func(requestID, key string) api.AuthenticatedRequestMetadata {
		return api.AuthenticatedRequestMetadata{RequestMetadata: requestMetadata(requestID, key), UserID: userID, TenantID: tenantID, MembershipID: membershipID, SessionID: current.SessionID}
	}

	staleVersion := api.EmailChangeCommand{AuthenticatedRequestMetadata: authenticated("email-change-stale", "email-change-stale-key"), NewNormalizedEmail: firstNewEmail, CurrentPassword: currentPassword, ExpectedUserVersion: 1}
	if _, err = service.ChangeEmail(ctx, staleVersion); !errors.Is(err, api.ErrVersionConflict) {
		t.Fatalf("stale version error=%v", err)
	}
	wrongPassword := api.EmailChangeCommand{AuthenticatedRequestMetadata: authenticated("email-change-wrong-password", "email-change-wrong-password-key"), NewNormalizedEmail: firstNewEmail, CurrentPassword: "incorrect current password", ExpectedUserVersion: 2}
	if _, err = service.ChangeEmail(ctx, wrongPassword); !errors.Is(err, api.ErrReauthenticationRequired) {
		t.Fatalf("wrong password error=%v", err)
	}

	firstChange := api.EmailChangeCommand{AuthenticatedRequestMetadata: authenticated("email-change-first", "email-change-key-0001"), NewNormalizedEmail: firstNewEmail, CurrentPassword: currentPassword, ExpectedUserVersion: 2}
	firstResults := concurrentCalls(t, 12, func() (api.EmailChangeResult, error) { return service.ChangeEmail(ctx, firstChange) })
	for _, result := range firstResults {
		if result.ID != userID || result.Version != 3 || result.Status != "pending_confirmation" || result.UpdatedAt != now {
			t.Fatalf("first change result=%#v", result)
		}
	}
	firstToken := latestEmailChangeToken(t, ctx, admin, payloadStore, tenantID)
	secondChange := api.EmailChangeCommand{AuthenticatedRequestMetadata: authenticated("email-change-second", "email-change-key-0002"), NewNormalizedEmail: finalEmail, CurrentPassword: currentPassword, ExpectedUserVersion: 3}
	if result, changeErr := service.ChangeEmail(ctx, secondChange); changeErr != nil || result.Version != 4 {
		t.Fatalf("second change result=%#v error=%v", result, changeErr)
	}
	secondToken := latestEmailChangeToken(t, ctx, admin, payloadStore, tenantID)
	if firstToken == secondToken {
		t.Fatal("email change token was reused")
	}

	invalidated := api.EmailConfirmChangeCommand{AuthenticatedRequestMetadata: authenticated("email-confirm-invalidated", "email-confirm-invalidated-key"), Token: firstToken}
	if _, err = service.ConfirmEmailChange(ctx, invalidated); !errors.Is(err, api.ErrInvalidCredentials) {
		t.Fatalf("invalidated token error=%v", err)
	}
	confirm := api.EmailConfirmChangeCommand{AuthenticatedRequestMetadata: authenticated("email-confirm-final", "email-confirm-key-0001"), Token: secondToken}
	confirmationResults := concurrentCalls(t, 12, func() (api.EmailConfirmChangeResult, error) { return service.ConfirmEmailChange(ctx, confirm) })
	firstConfirmation := confirmationResults[0]
	for _, result := range confirmationResults {
		if result.ID != userID || result.Version != 5 || result.Status != "changed" || result.UpdatedAt != now || result.SessionToken != firstConfirmation.SessionToken || result.CSRFToken != firstConfirmation.CSRFToken || result.ExpiresAt != now.Add(service.SessionTTL) {
			t.Fatalf("confirmation result=%#v first=%#v", result, firstConfirmation)
		}
	}

	resolver := SessionResolver{Pool: pool, Pepper: service.SessionPepper, Now: service.Now}
	if _, err = resolver.Resolve(ctx, current.SessionToken); !errors.Is(err, session.ErrUnauthenticated) {
		t.Fatalf("old current token error=%v", err)
	}
	principal, err := resolver.Resolve(ctx, firstConfirmation.SessionToken)
	if err != nil || principal.UserID != userID || principal.SessionID != current.SessionID {
		t.Fatalf("rotated principal=%#v error=%v", principal, err)
	}
	for _, revoked := range sessions[1:] {
		if _, err = resolver.Resolve(ctx, revoked.SessionToken); !errors.Is(err, session.ErrUnauthenticated) {
			t.Fatalf("other session %s error=%v", revoked.SessionID, err)
		}
	}
	if _, err = service.Login(ctx, api.LoginCommand{RequestMetadata: requestMetadata("old-email-login", "old-email-login-key"), NormalizedEmail: oldEmail, Password: currentPassword}); !errors.Is(err, api.ErrInvalidCredentials) {
		t.Fatalf("old email login error=%v", err)
	}
	_ = login(finalEmail, "final-email-login-key")
	usedWithDifferentKey := confirm
	usedWithDifferentKey.IdempotencyKey = "email-confirm-key-0002"
	usedWithDifferentKey.RequestID = "server-email-confirm-used"
	usedWithDifferentKey.ClientRequestID = "email-confirm-used"
	if _, err = service.ConfirmEmailChange(ctx, usedWithDifferentKey); !errors.Is(err, api.ErrInvalidCredentials) {
		t.Fatalf("used token error=%v", err)
	}

	var storedEmail string
	var userVersion, totalVerifications, liveVerifications, revokedSessions, requestedEvents, changedEvents, verifyMails, securityMails int
	if err = admin.QueryRow(ctx, `SELECT normalized_email,version FROM identity.users WHERE id=$1`, userID).Scan(&storedEmail, &userVersion); err != nil {
		t.Fatal(err)
	}
	queries := []struct {
		query string
		out   *int
	}{
		{`SELECT count(*) FROM identity.email_verifications WHERE user_id='10000000-0000-0000-0000-000000000901'`, &totalVerifications},
		{`SELECT count(*) FROM identity.email_verifications WHERE user_id='10000000-0000-0000-0000-000000000901' AND used_at IS NULL AND revoked_at IS NULL`, &liveVerifications},
		{`SELECT count(*) FROM identity.sessions WHERE user_id='10000000-0000-0000-0000-000000000901' AND revoked_at IS NOT NULL`, &revokedSessions},
		{`SELECT count(*) FROM agent.events WHERE user_id='10000000-0000-0000-0000-000000000901' AND event_type='EmailChangeRequested'`, &requestedEvents},
		{`SELECT count(*) FROM agent.events WHERE user_id='10000000-0000-0000-0000-000000000901' AND event_type='EmailChanged'`, &changedEvents},
		{`SELECT count(*) FROM agent.outbox WHERE tenant_id='20000000-0000-0000-0000-000000000902' AND command_type='identity.email.change.verify'`, &verifyMails},
		{`SELECT count(*) FROM agent.outbox WHERE tenant_id='20000000-0000-0000-0000-000000000902' AND command_type='identity.email.security'`, &securityMails},
	}
	for _, query := range queries {
		if err = admin.QueryRow(ctx, query.query).Scan(query.out); err != nil {
			t.Fatal(err)
		}
	}
	if storedEmail != finalEmail || userVersion != 5 || totalVerifications != 2 || liveVerifications != 0 || revokedSessions != 2 || requestedEvents != 2 || changedEvents != 1 || verifyMails != 2 || securityMails != 4 {
		t.Fatalf("email=%s version=%d verifications=%d live=%d revoked-sessions=%d requested-events=%d changed-events=%d verify-mails=%d security-mails=%d", storedEmail, userVersion, totalVerifications, liveVerifications, revokedSessions, requestedEvents, changedEvents, verifyMails, securityMails)
	}
	var securityPII int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.security_events WHERE subject_user_id=$1 AND (details::text LIKE '%email-change-old@example.com%' OR details::text LIKE '%email-change-first@example.com%' OR details::text LIKE '%email-change-final@example.com%')`, userID).Scan(&securityPII); err != nil || securityPII != 0 {
		t.Fatalf("security PII rows=%d error=%v", securityPII, err)
	}
	eventRows, err := admin.Query(ctx, `SELECT id::text,payload_ref,payload_hash FROM agent.events WHERE user_id=$1 AND event_type IN ('EmailChangeRequested','EmailChanged') ORDER BY seq`, userID)
	if err != nil {
		t.Fatal(err)
	}
	for eventRows.Next() {
		var eventID, ref, hash string
		if err = eventRows.Scan(&eventID, &ref, &hash); err != nil {
			t.Fatal(err)
		}
		encoded, readErr := payloadStore.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: eventID, Class: "event-payload", ContentType: "application/json"}, payload.Manifest{Ref: ref, Hash: hash})
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, secret := range []string{oldEmail, firstNewEmail, finalEmail, firstToken, secondToken} {
			if bytes.Contains(encoded, []byte(secret)) {
				t.Fatalf("event payload contains email or token: %q", secret)
			}
		}
	}
	if err = eventRows.Err(); err != nil {
		t.Fatal(err)
	}
	eventRows.Close()
	for _, stored := range blobs.values {
		for _, secret := range []string{oldEmail, firstNewEmail, finalEmail, currentPassword, firstToken, secondToken, current.SessionToken, firstConfirmation.SessionToken, firstConfirmation.CSRFToken} {
			if bytes.Contains(stored, []byte(secret)) {
				t.Fatalf("plaintext secret reached object storage: %q", secret)
			}
		}
	}
	var firstDigest, secondDigest, sessionDigest, csrfDigest []byte
	rows, err := admin.Query(ctx, `SELECT token_hash FROM identity.email_verifications WHERE user_id=$1 ORDER BY created_at,id`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() || rows.Scan(&firstDigest) != nil || !rows.Next() || rows.Scan(&secondDigest) != nil {
		t.Fatal("email change token digests are missing")
	}
	if err = admin.QueryRow(ctx, `SELECT token_hash,csrf_secret_hash FROM identity.sessions WHERE id=$1`, current.SessionID).Scan(&sessionDigest, &csrfDigest); err != nil {
		t.Fatal(err)
	}
	expectedFirstDigest, _ := service.EmailChangeTokens.Digest(firstToken)
	expectedSecondDigest, _ := service.EmailChangeTokens.Digest(secondToken)
	expectedSessionDigest, _ := session.Digest(firstConfirmation.SessionToken, service.SessionPepper)
	expectedCSRFDigest, _ := session.Digest(firstConfirmation.CSRFToken, service.CSRFPepper)
	if !bytes.Equal(firstDigest, expectedFirstDigest[:]) || !bytes.Equal(secondDigest, expectedSecondDigest[:]) || !bytes.Equal(sessionDigest, expectedSessionDigest[:]) || !bytes.Equal(csrfDigest, expectedCSRFDigest[:]) {
		t.Fatal("stored credential digests do not match their purpose-separated credentials")
	}
	for _, pair := range []struct {
		stored []byte
		raw    string
	}{{firstDigest, firstToken}, {secondDigest, secondToken}, {sessionDigest, firstConfirmation.SessionToken}, {csrfDigest, firstConfirmation.CSRFToken}} {
		if strings.Contains(string(pair.stored), pair.raw) {
			t.Fatal("raw email-change or session credential reached PostgreSQL")
		}
	}
}

func latestEmailChangeToken(t *testing.T, ctx context.Context, admin *pgxpool.Pool, store payload.Store, tenantID string) string {
	t.Helper()
	var commandID, ref, hash string
	if err := admin.QueryRow(ctx, `SELECT command_id::text,payload_ref,payload_hash FROM agent.outbox WHERE tenant_id=$1 AND command_type='identity.email.change.verify' ORDER BY created_at DESC,id DESC LIMIT 1`, tenantID).Scan(&commandID, &ref, &hash); err != nil {
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
		t.Fatalf("mail command=%s error=%v", encoded, err)
	}
	return command.Token
}
