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

func TestPasswordRecoveryAndChangeAreUniformSingleUseRotatingAndSecretSafe(t *testing.T) {
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
	now := time.Unix(1_800_000_800, 0).UTC()
	const publicTenantID = "20000000-0000-0000-0000-000000000801"
	const userID = "10000000-0000-0000-0000-000000000801"
	const tenantID = "20000000-0000-0000-0000-000000000802"
	const membershipID = "30000000-0000-0000-0000-000000000801"
	const credentialID = "31000000-0000-0000-0000-000000000801"
	const email = "password-lifecycle@example.com"
	if _, err = admin.Exec(ctx, `INSERT INTO identity.users (id,normalized_email,email_verified_at,locale,status) VALUES ($1,$2,$3,'en','active')`, userID, email, now); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.tenants (id,kind,name,status,region) VALUES ($1,'anonymous_system','Password Public','active','US')`, publicTenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.tenants (id,kind,name,status,region,owner_user_id) VALUES ($1,'personal','Password Lifecycle','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ($1,$2,$3,'owner','active',$4)`, membershipID, tenantID, userID, now); err != nil {
		t.Fatal(err)
	}
	hasher := password.Hasher{Parameters: password.Parameters{Version: password.CurrentVersion, Time: 1, MemoryKiB: 8 * 1024, Threads: 1, KeyLength: 32}, Pepper: bytes.Repeat([]byte{0x81}, 32)}
	initialPassword := "initial password value"
	resetPassword := "password after reset"
	changedPassword := "password after authenticated change"
	initialHash, initialParameters, err := hasher.Hash(initialPassword)
	if err != nil {
		t.Fatal(err)
	}
	encodedParameters, _ := json.Marshal(initialParameters)
	if _, err = admin.Exec(ctx, `INSERT INTO identity.password_credentials (id,user_id,password_hash,algorithm,parameters,password_changed_at) VALUES ($1,$2,$3,'argon2id',$4,$5)`, credentialID, userID, initialHash, encodedParameters, now); err != nil {
		t.Fatal(err)
	}
	dummyHash, dummyParameters, err := hasher.Hash("constant password dummy value")
	if err != nil {
		t.Fatal(err)
	}
	blobs := &authMemoryBlobs{values: map[string][]byte{}}
	payloadStore := payload.EnvelopeStore{Keys: authKeyProvider{key: payload.Key{ID: "password-vault-key-v1", Material: bytes.Repeat([]byte{0x82}, 32)}}, Blobs: blobs}
	service := AuthService{
		Pool: pool, Passwords: hasher, PasswordPolicy: password.Policy{Checker: password.NewDigestSet([]string{"known compromised password value"})}, DummyPasswordHash: dummyHash, DummyPasswordParameters: dummyParameters,
		VerificationTokens: opaque.Manager{Purpose: "email-verification", Pepper: bytes.Repeat([]byte{0x83}, 32)}, PasswordResetTokens: opaque.Manager{Purpose: "password-reset", Pepper: bytes.Repeat([]byte{0x84}, 32)},
		EmailChangeTokens: opaque.Manager{Purpose: "email-change", Pepper: bytes.Repeat([]byte{0x8b}, 32)},
		SessionPepper:     bytes.Repeat([]byte{0x85}, 32), CSRFPepper: bytes.Repeat([]byte{0x86}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0x87}, 32), RequestDigestPepper: bytes.Repeat([]byte{0x88}, 32), IdentityKey: bytes.Repeat([]byte{0x89}, 32), CursorKey: bytes.Repeat([]byte{0x8a}, 32),
		PublicTenantID: publicTenantID, StoreEpoch: "40000000-0000-0000-0000-000000000801", Region: "US", VerificationTTL: 24 * time.Hour, PasswordResetTTL: 30 * time.Minute, EmailChangeTTL: 24 * time.Hour, SessionTTL: 30 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour,
		Payloads: payloadStore, Random: rand.Reader, Now: func() time.Time { return now },
	}
	metadata := func(requestID, key string) api.RequestMetadata {
		return api.RequestMetadata{RequestID: "server-" + requestID, ClientRequestID: requestID, IdempotencyKey: key, ClientIPHash: bytes.Repeat([]byte{0x91}, 32), UserAgentHash: bytes.Repeat([]byte{0x92}, 32)}
	}
	login := func(passwordValue, key string) api.LoginResult {
		result, loginErr := service.Login(ctx, api.LoginCommand{RequestMetadata: metadata("login-request-"+key, key), NormalizedEmail: email, Password: passwordValue})
		if loginErr != nil {
			t.Fatal(loginErr)
		}
		return result
	}
	originalSessions := []api.LoginResult{login(initialPassword, "password-login-key-01"), login(initialPassword, "password-login-key-02"), login(initialPassword, "password-login-key-03")}
	missing, err := service.ForgotPassword(ctx, api.PasswordForgotCommand{RequestMetadata: metadata("forgot-missing-001", "forgot-missing-key-01"), NormalizedEmail: "missing-password@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	forgot := api.PasswordForgotCommand{RequestMetadata: metadata("forgot-found-0001", "forgot-found-key-0001"), NormalizedEmail: email}
	foundResults := concurrentCalls(t, 12, func() (api.PasswordForgotResult, error) { return service.ForgotPassword(ctx, forgot) })
	if missing != foundResults[0] || missing.Status != "accepted" || missing.Message != api.PasswordResetAcceptedMessage {
		t.Fatalf("missing=%#v found=%#v", missing, foundResults[0])
	}
	firstToken := latestPasswordResetToken(t, ctx, admin, payloadStore, tenantID)
	secondForgot := forgot
	secondForgot.IdempotencyKey, secondForgot.RequestID, secondForgot.ClientRequestID = "forgot-found-key-0002", "server-forgot-found-0002", "forgot-found-0002"
	if _, err = service.ForgotPassword(ctx, secondForgot); err != nil {
		t.Fatal(err)
	}
	secondToken := latestPasswordResetToken(t, ctx, admin, payloadStore, tenantID)
	invalidated := api.PasswordResetCommand{RequestMetadata: metadata("reset-invalidated-01", "reset-invalidated-key"), Token: firstToken, NewPassword: resetPassword}
	if _, err = service.ResetPassword(ctx, invalidated); !errors.Is(err, api.ErrInvalidCredentials) {
		t.Fatalf("revoked reset token error=%v", err)
	}
	compromisedReset := api.PasswordResetCommand{RequestMetadata: metadata("reset-compromised-01", "reset-compromised-key"), Token: secondToken, NewPassword: "KNOWN COMPROMISED PASSWORD VALUE"}
	if _, err = service.ResetPassword(ctx, compromisedReset); !errors.Is(err, api.ErrValidation) {
		t.Fatalf("compromised reset error=%v", err)
	}
	reset := api.PasswordResetCommand{RequestMetadata: metadata("reset-request-0001", "reset-key-000000001"), Token: secondToken, NewPassword: resetPassword}
	resetResults := concurrentCalls(t, 12, func() (api.PasswordResetResult, error) { return service.ResetPassword(ctx, reset) })
	for _, result := range resetResults {
		if result.RevokedSessionCount != 3 {
			t.Fatalf("reset result=%#v", result)
		}
	}
	otherResetKey := reset
	otherResetKey.IdempotencyKey = "reset-key-000000002"
	if _, err = service.ResetPassword(ctx, otherResetKey); !errors.Is(err, api.ErrInvalidCredentials) {
		t.Fatalf("used reset token error=%v", err)
	}
	if _, err = service.Login(ctx, api.LoginCommand{RequestMetadata: metadata("old-password-login", "old-password-key-01"), NormalizedEmail: email, Password: initialPassword}); !errors.Is(err, api.ErrInvalidCredentials) {
		t.Fatalf("old password login error=%v", err)
	}
	current := login(resetPassword, "post-reset-login-key")
	thirdForgot := forgot
	thirdForgot.IdempotencyKey, thirdForgot.RequestID, thirdForgot.ClientRequestID = "forgot-found-key-0003", "server-forgot-found-0003", "forgot-found-0003"
	if _, err = service.ForgotPassword(ctx, thirdForgot); err != nil {
		t.Fatal(err)
	}
	thirdToken := latestPasswordResetToken(t, ctx, admin, payloadStore, tenantID)
	service.Now = func() time.Time { return now.Add(service.PasswordResetTTL + time.Second) }
	expired := api.PasswordResetCommand{RequestMetadata: metadata("reset-expired-0001", "reset-expired-key-01"), Token: thirdToken, NewPassword: changedPassword}
	if _, err = service.ResetPassword(ctx, expired); !errors.Is(err, api.ErrInvalidCredentials) {
		t.Fatalf("expired reset token error=%v", err)
	}
	service.Now = func() time.Time { return now }
	changeMetadata := api.AuthenticatedRequestMetadata{RequestMetadata: metadata("change-password-01", "change-password-key-01"), UserID: userID, TenantID: tenantID, MembershipID: membershipID, SessionID: current.SessionID}
	compromisedChange := api.PasswordChangeCommand{AuthenticatedRequestMetadata: api.AuthenticatedRequestMetadata{RequestMetadata: metadata("change-compromised", "change-compromised-key"), UserID: userID, TenantID: tenantID, MembershipID: membershipID, SessionID: current.SessionID}, CurrentPassword: resetPassword, NewPassword: "KNOWN COMPROMISED PASSWORD VALUE"}
	if _, err = service.ChangePassword(ctx, compromisedChange); !errors.Is(err, api.ErrValidation) {
		t.Fatalf("compromised change error=%v", err)
	}
	change := api.PasswordChangeCommand{AuthenticatedRequestMetadata: changeMetadata, CurrentPassword: resetPassword, NewPassword: changedPassword}
	changeResults := concurrentCalls(t, 12, func() (api.PasswordChangeResult, error) { return service.ChangePassword(ctx, change) })
	firstChange := changeResults[0]
	for _, result := range changeResults {
		if result.ID != credentialID || result.Version != 3 || result.Status != "changed" || result.SessionToken != firstChange.SessionToken || result.CSRFToken != firstChange.CSRFToken {
			t.Fatalf("change result=%#v first=%#v", result, firstChange)
		}
	}
	resolver := SessionResolver{Pool: pool, Pepper: service.SessionPepper, Now: service.Now}
	if _, err = resolver.Resolve(ctx, current.SessionToken); !errors.Is(err, session.ErrUnauthenticated) {
		t.Fatalf("old rotated session token error=%v", err)
	}
	principal, err := resolver.Resolve(ctx, firstChange.SessionToken)
	if err != nil || principal.UserID != userID || principal.SessionID != current.SessionID {
		t.Fatalf("rotated principal=%#v err=%v", principal, err)
	}
	if _, err = service.Login(ctx, api.LoginCommand{RequestMetadata: metadata("reset-password-login", "reset-password-old-key"), NormalizedEmail: email, Password: resetPassword}); !errors.Is(err, api.ErrInvalidCredentials) {
		t.Fatalf("pre-change password login error=%v", err)
	}
	_ = login(changedPassword, "changed-password-login-key")
	var resetRequests, activeResetRequests, revokedSessions, credentialVersion, resetRequestedEvents, resetCompletedEvents, changedEvents, securityMailCommands int
	queries := []struct {
		query string
		args  []any
		out   *int
	}{
		{`SELECT count(*) FROM identity.password_reset_requests WHERE user_id=$1`, []any{userID}, &resetRequests},
		{`SELECT count(*) FROM identity.password_reset_requests WHERE user_id=$1 AND used_at IS NULL AND revoked_at IS NULL`, []any{userID}, &activeResetRequests},
		{`SELECT count(*) FROM identity.sessions WHERE user_id=$1 AND revoked_at IS NOT NULL`, []any{userID}, &revokedSessions},
		{`SELECT version FROM identity.password_credentials WHERE id=$1`, []any{credentialID}, &credentialVersion},
		{`SELECT count(*) FROM agent.events WHERE user_id=$1 AND event_type='PasswordResetRequested'`, []any{userID}, &resetRequestedEvents},
		{`SELECT count(*) FROM agent.events WHERE user_id=$1 AND event_type='PasswordResetCompleted'`, []any{userID}, &resetCompletedEvents},
		{`SELECT count(*) FROM agent.events WHERE user_id=$1 AND event_type='PasswordChanged'`, []any{userID}, &changedEvents},
		{`SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND command_type='identity.email.security'`, []any{tenantID}, &securityMailCommands},
	}
	for _, query := range queries {
		if err = admin.QueryRow(ctx, query.query, query.args...).Scan(query.out); err != nil {
			t.Fatal(err)
		}
	}
	if resetRequests != 3 || activeResetRequests != 0 || revokedSessions != len(originalSessions) || credentialVersion != 3 || resetRequestedEvents != 3 || resetCompletedEvents != 1 || changedEvents != 1 || securityMailCommands != 2 {
		t.Fatalf("reset-requests=%d active-reset=%d revoked-sessions=%d credential-version=%d requested-events=%d completed-events=%d changed-events=%d security-mails=%d", resetRequests, activeResetRequests, revokedSessions, credentialVersion, resetRequestedEvents, resetCompletedEvents, changedEvents, securityMailCommands)
	}
	for _, stored := range blobs.values {
		for _, secret := range []string{initialPassword, resetPassword, changedPassword, firstToken, secondToken, current.SessionToken, firstChange.SessionToken, email} {
			if bytes.Contains(stored, []byte(secret)) {
				t.Fatalf("plaintext secret reached object storage: %q", secret)
			}
		}
	}
	var resetDigest, currentSessionDigest, currentCSRFDigest []byte
	if err = admin.QueryRow(ctx, `SELECT token_hash FROM identity.password_reset_requests WHERE user_id=$1 ORDER BY created_at DESC LIMIT 1`, userID).Scan(&resetDigest); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT token_hash,csrf_secret_hash FROM identity.sessions WHERE id=$1`, current.SessionID).Scan(&currentSessionDigest, &currentCSRFDigest); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(resetDigest), thirdToken) || strings.Contains(string(currentSessionDigest), firstChange.SessionToken) || strings.Contains(string(currentCSRFDigest), firstChange.CSRFToken) {
		t.Fatal("raw reset, session, or CSRF token reached PostgreSQL")
	}
}

func latestPasswordResetToken(t *testing.T, ctx context.Context, admin *pgxpool.Pool, store payload.Store, tenantID string) string {
	t.Helper()
	var commandID, ref, hash string
	if err := admin.QueryRow(ctx, `SELECT command_id::text,payload_ref,payload_hash FROM agent.outbox WHERE tenant_id=$1 AND command_type='identity.email.password_reset' ORDER BY created_at DESC,id DESC LIMIT 1`, tenantID).Scan(&commandID, &ref, &hash); err != nil {
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
		t.Fatalf("mail command=%s err=%v", encoded, err)
	}
	return command.Token
}
