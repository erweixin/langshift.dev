//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/identity/password"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestSessionLifecycleIsPaginatedOwnedCASIdempotentAndEvented(t *testing.T) {
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
	now := time.Unix(1_800_000_700, 0).UTC()
	const publicTenantID = "20000000-0000-0000-0000-000000000701"
	const userID = "10000000-0000-0000-0000-000000000701"
	const tenantID = "20000000-0000-0000-0000-000000000702"
	const otherUserID = "10000000-0000-0000-0000-000000000702"
	const otherTenantID = "20000000-0000-0000-0000-000000000703"
	if _, err = admin.Exec(ctx, `INSERT INTO identity.users (id,normalized_email,email_verified_at,locale,status) VALUES ($1,'sessions-one@example.com',$3,'en','active'),($2,'sessions-two@example.com',$3,'en','active')`, userID, otherUserID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.tenants (id,kind,name,status,region) VALUES ($1,'anonymous_system','Session Public','active','US')`, publicTenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.tenants (id,kind,name,status,region,owner_user_id) VALUES ($1,'personal','Session One','active','US',$2),($3,'personal','Session Two','active','US',$4)`, tenantID, userID, otherTenantID, otherUserID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ('30000000-0000-0000-0000-000000000701',$1,$2,'owner','active',$5),('30000000-0000-0000-0000-000000000702',$3,$4,'owner','active',$5)`, tenantID, userID, otherTenantID, otherUserID, now); err != nil {
		t.Fatal(err)
	}
	hasher := password.Hasher{Parameters: password.Parameters{Version: password.CurrentVersion, Time: 1, MemoryKiB: 8 * 1024, Threads: 1, KeyLength: 32}, Pepper: bytes.Repeat([]byte{0x51}, 32)}
	userPassword := "session lifecycle password"
	otherPassword := "other session password"
	for index, account := range []struct {
		id, password string
	}{{userID, userPassword}, {otherUserID, otherPassword}} {
		digest, parameters, hashErr := hasher.Hash(account.password)
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		credentialID := fmt.Sprintf("31000000-0000-0000-0000-%012d", 701+index)
		encodedParameters, marshalErr := json.Marshal(parameters)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err = admin.Exec(ctx, `INSERT INTO identity.password_credentials (id,user_id,password_hash,algorithm,parameters,password_changed_at) VALUES ($1,$2,$3,'argon2id',$4,$5)`, credentialID, account.id, digest, encodedParameters, now); err != nil {
			t.Fatal(err)
		}
	}
	dummyHash, dummyParameters, err := hasher.Hash("constant session dummy password")
	if err != nil {
		t.Fatal(err)
	}
	blobs := &authMemoryBlobs{values: map[string][]byte{}}
	service := AuthService{
		Pool: pool, Passwords: hasher, PasswordPolicy: password.Policy{Checker: password.NewDigestSet([]string{"known compromised password value"})}, DummyPasswordHash: dummyHash, DummyPasswordParameters: dummyParameters,
		VerificationTokens:  opaque.Manager{Purpose: "email-verification", Pepper: bytes.Repeat([]byte{0x52}, 32)},
		PasswordResetTokens: opaque.Manager{Purpose: "password-reset", Pepper: bytes.Repeat([]byte{0x5a}, 32)},
		SessionPepper:       bytes.Repeat([]byte{0x53}, 32), CSRFPepper: bytes.Repeat([]byte{0x54}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0x55}, 32), RequestDigestPepper: bytes.Repeat([]byte{0x56}, 32), IdentityKey: bytes.Repeat([]byte{0x57}, 32), CursorKey: bytes.Repeat([]byte{0x58}, 32),
		PublicTenantID: publicTenantID, StoreEpoch: "40000000-0000-0000-0000-000000000701", Region: "US", VerificationTTL: 24 * time.Hour, PasswordResetTTL: 30 * time.Minute, SessionTTL: 30 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour,
		Payloads: payload.EnvelopeStore{Keys: authKeyProvider{key: payload.Key{ID: "session-vault-key-v1", Material: bytes.Repeat([]byte{0x59}, 32)}}, Blobs: blobs}, Random: rand.Reader, Now: func() time.Time { return now },
	}
	login := func(email, passwordValue, key string) api.LoginResult {
		result, loginErr := service.Login(ctx, api.LoginCommand{RequestMetadata: api.RequestMetadata{RequestID: "server-" + key, ClientRequestID: "client-" + key, IdempotencyKey: key, ClientIPHash: bytes.Repeat([]byte{0x61}, 32), UserAgentHash: bytes.Repeat([]byte{0x62}, 32)}, NormalizedEmail: email, Password: passwordValue})
		if loginErr != nil {
			t.Fatal(loginErr)
		}
		return result
	}
	userSessions := make([]api.LoginResult, 4)
	for index := range userSessions {
		userSessions[index] = login("sessions-one@example.com", userPassword, fmt.Sprintf("session-login-key-%04d", index))
	}
	otherSession := login("sessions-two@example.com", otherPassword, "other-login-key-0001")
	metadata := api.AuthenticatedRequestMetadata{RequestMetadata: api.RequestMetadata{RequestID: "server-session-list", ClientRequestID: "client-session-list", ClientIPHash: bytes.Repeat([]byte{0x61}, 32), UserAgentHash: bytes.Repeat([]byte{0x62}, 32)}, UserID: userID, TenantID: tenantID, MembershipID: "30000000-0000-0000-0000-000000000701", SessionID: userSessions[0].SessionID}
	firstPage, err := service.ListSessions(ctx, api.SessionsQuery{AuthenticatedRequestMetadata: metadata, Limit: 2})
	if err != nil || len(firstPage.Items) != 2 || firstPage.NextCursor == nil {
		t.Fatalf("first page=%#v err=%v", firstPage, err)
	}
	secondPage, err := service.ListSessions(ctx, api.SessionsQuery{AuthenticatedRequestMetadata: metadata, Limit: 2, Cursor: *firstPage.NextCursor})
	if err != nil || len(secondPage.Items) != 2 {
		t.Fatalf("second page=%#v err=%v", secondPage, err)
	}
	seen := map[string]bool{}
	for _, item := range append(firstPage.Items, secondPage.Items...) {
		if seen[item.ID] || item.ActiveTenantID != tenantID {
			t.Fatalf("invalid paginated item=%#v", item)
		}
		seen[item.ID] = true
	}
	otherMetadata := metadata
	otherMetadata.UserID, otherMetadata.TenantID, otherMetadata.MembershipID, otherMetadata.SessionID = otherUserID, otherTenantID, "30000000-0000-0000-0000-000000000702", otherSession.SessionID
	if _, err = service.ListSessions(ctx, api.SessionsQuery{AuthenticatedRequestMetadata: otherMetadata, Limit: 2, Cursor: *firstPage.NextCursor}); !errors.Is(err, api.ErrValidation) {
		t.Fatalf("cross-principal cursor error=%v", err)
	}
	revoke := api.RevokeSessionCommand{AuthenticatedRequestMetadata: metadata, TargetSessionID: userSessions[1].SessionID, ExpectedVersion: 1, ReasonCode: "user_revoked"}
	revoke.IdempotencyKey, revoke.RequestID, revoke.ClientRequestID = "session-revoke-key-0001", "server-revoke-one", "client-revoke-one"
	revocations := concurrentCalls(t, 12, func() (api.SessionMutationResult, error) { return service.RevokeSession(ctx, revoke) })
	for _, result := range revocations {
		if result.ID != userSessions[1].SessionID || result.Version != 2 || result.Status != "revoked" {
			t.Fatalf("revoke result=%#v", result)
		}
	}
	crossUser := revoke
	crossUser.TargetSessionID, crossUser.IdempotencyKey = otherSession.SessionID, "session-revoke-cross-user"
	if _, err = service.RevokeSession(ctx, crossUser); !errors.Is(err, api.ErrResourceNotFound) {
		t.Fatalf("cross-user revoke error=%v", err)
	}
	others := api.RevokeOtherSessionsCommand{AuthenticatedRequestMetadata: metadata}
	others.IdempotencyKey, others.RequestID, others.ClientRequestID = "session-others-key-0001", "server-revoke-others", "client-revoke-others"
	otherResults := concurrentCalls(t, 12, func() (api.SessionMutationResult, error) { return service.RevokeOtherSessions(ctx, others) })
	for _, result := range otherResults {
		if result.ID != userSessions[0].SessionID || result.Version != 1 || result.Status != "active" {
			t.Fatalf("revoke others result=%#v", result)
		}
	}
	logout := api.LogoutCommand{AuthenticatedRequestMetadata: metadata, AllDevices: true}
	logout.IdempotencyKey, logout.RequestID, logout.ClientRequestID = "session-logout-key-0001", "server-logout-all", "client-logout-all"
	logoutResult, err := service.Logout(ctx, logout)
	if err != nil || logoutResult.RevokedSessionCount != 1 {
		t.Fatalf("logout=%#v err=%v", logoutResult, err)
	}
	replay, err := service.Logout(ctx, logout)
	if err != nil || replay != logoutResult {
		t.Fatalf("logout replay=%#v err=%v", replay, err)
	}
	var revoked, revokeEvents, audits, otherRevoked int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.sessions WHERE user_id=$1 AND revoked_at IS NOT NULL`, userID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.events WHERE user_id=$1 AND event_type='SessionRevoked'`, userID).Scan(&revokeEvents); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.security_events WHERE subject_user_id=$1 AND event_type='sessions_revoked'`, userID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.sessions WHERE id=$1 AND revoked_at IS NOT NULL`, otherSession.SessionID).Scan(&otherRevoked); err != nil {
		t.Fatal(err)
	}
	if revoked != 4 || revokeEvents != 4 || audits != 3 || otherRevoked != 0 {
		t.Fatalf("revoked=%d events=%d audits=%d other-revoked=%d", revoked, revokeEvents, audits, otherRevoked)
	}
}
