//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/identity/password"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/security/opaque"
)

const authPublicTenantID = "20000000-0000-0000-0000-000000000601"
const authStoreEpoch = "40000000-0000-0000-0000-000000000601"

type authKeyProvider struct{ key payload.Key }

func (provider authKeyProvider) Current(context.Context, string) (payload.Key, error) {
	return provider.key, nil
}
func (provider authKeyProvider) ByID(_ context.Context, _ string, id string) (payload.Key, error) {
	if id != provider.key.ID {
		return payload.Key{}, payload.ErrInvalidKey
	}
	return provider.key, nil
}

type authMemoryBlobs struct {
	mu     sync.RWMutex
	values map[string][]byte
}

func (store *authMemoryBlobs) Put(_ context.Context, key string, value []byte) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	ref := "memory://" + key
	if _, exists := store.values[ref]; exists {
		return "", errors.New("immutable blob already exists")
	}
	store.values[ref] = append([]byte(nil), value...)
	return ref, nil
}
func (store *authMemoryBlobs) Get(_ context.Context, ref string) ([]byte, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.values[ref]
	if !ok {
		return nil, errors.New("blob not found")
	}
	return append([]byte(nil), value...), nil
}

func (store *authMemoryBlobs) Delete(_ context.Context, ref string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.values, ref)
	return nil
}

func TestAuthRegistrationVerificationAndLoginAreDurableIdempotentAndSecretSafe(t *testing.T) {
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
	now := time.Unix(1_800_000_600, 0).UTC()
	if _, err = admin.Exec(ctx, `INSERT INTO identity.tenants (id,kind,name,status,region) VALUES ($1,'anonymous_system','Public Identity','active','US')`, authPublicTenantID); err != nil {
		t.Fatal(err)
	}
	hasher := password.Hasher{Parameters: password.Parameters{Version: password.CurrentVersion, Time: 1, MemoryKiB: 8 * 1024, Threads: 1, KeyLength: 32}, Pepper: bytes.Repeat([]byte{0x31}, 32)}
	dummyHash, dummyParameters, err := hasher.Hash("constant dummy password value")
	if err != nil {
		t.Fatal(err)
	}
	blobs := &authMemoryBlobs{values: map[string][]byte{}}
	payloadStore := payload.EnvelopeStore{Keys: authKeyProvider{key: payload.Key{ID: "test-vault-key-v1", Material: bytes.Repeat([]byte{0x71}, 32)}}, Blobs: blobs}
	service := AuthService{
		Pool:                    pool,
		Passwords:               hasher,
		PasswordPolicy:          password.Policy{Checker: password.NewDigestSet([]string{"known compromised password value"})},
		DummyPasswordHash:       dummyHash,
		DummyPasswordParameters: dummyParameters,
		VerificationTokens:      opaque.Manager{Purpose: "email-verification", Pepper: bytes.Repeat([]byte{0x32}, 32)},
		PasswordResetTokens:     opaque.Manager{Purpose: "password-reset", Pepper: bytes.Repeat([]byte{0x39}, 32)},
		EmailChangeTokens:       opaque.Manager{Purpose: "email-change", Pepper: bytes.Repeat([]byte{0x3a}, 32)},
		InvitationTokens:        opaque.Manager{Purpose: "invitation", Pepper: bytes.Repeat([]byte{0x3b}, 32)},
		SessionPepper:           bytes.Repeat([]byte{0x33}, 32),
		CSRFPepper:              bytes.Repeat([]byte{0x34}, 32),
		IdempotencyKeyPepper:    bytes.Repeat([]byte{0x35}, 32),
		RequestDigestPepper:     bytes.Repeat([]byte{0x36}, 32),
		IdentityKey:             bytes.Repeat([]byte{0x37}, 32),
		CursorKey:               bytes.Repeat([]byte{0x38}, 32),
		PublicTenantID:          authPublicTenantID,
		StoreEpoch:              authStoreEpoch,
		Region:                  "US",
		VerificationTTL:         24 * time.Hour,
		PasswordResetTTL:        30 * time.Minute,
		EmailChangeTTL:          24 * time.Hour,
		ReauthenticationTTL:     5 * time.Minute,
		ErasureGracePeriod:      7 * 24 * time.Hour,
		SessionTTL:              30 * 24 * time.Hour,
		IdempotencyTTL:          24 * time.Hour,
		Payloads:                payloadStore,
		Random:                  rand.Reader,
		Now:                     func() time.Time { return now },
	}
	metadata := api.RequestMetadata{RequestID: "server-register-001", ClientRequestID: "client-register-001", IdempotencyKey: "register-idempotency-0001", ClientIPHash: bytes.Repeat([]byte{0x41}, 32), UserAgentHash: bytes.Repeat([]byte{0x42}, 32)}
	compromisedRegistration := api.RegisterCommand{RequestMetadata: api.RequestMetadata{RequestID: "server-register-blocked", ClientRequestID: "client-register-blocked", IdempotencyKey: "register-blocked-key-0001", ClientIPHash: metadata.ClientIPHash, UserAgentHash: metadata.UserAgentHash}, NormalizedEmail: "blocked-password@example.com", Password: "KNOWN COMPROMISED PASSWORD VALUE", Locale: "en"}
	if _, err = service.Register(ctx, compromisedRegistration); !errors.Is(err, api.ErrValidation) {
		t.Fatalf("compromised registration error=%v", err)
	}
	var blockedUsers int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE normalized_email=$1`, compromisedRegistration.NormalizedEmail).Scan(&blockedUsers); err != nil || blockedUsers != 0 {
		t.Fatalf("blocked users=%d error=%v", blockedUsers, err)
	}
	register := api.RegisterCommand{RequestMetadata: metadata, NormalizedEmail: "production@example.com", Password: "correct horse battery staple", Locale: "en"}
	registrationResults := concurrentCalls(t, 12, func() (api.RegisterResult, error) { return service.Register(ctx, register) })
	userID := registrationResults[0].UserID
	for _, result := range registrationResults {
		if result.UserID != userID || result.EmailVerificationExpiresAt != now.Add(24*time.Hour) {
			t.Fatalf("registration result=%#v", result)
		}
	}
	service.PasswordPolicy = password.Policy{Checker: password.NewDigestSet([]string{register.Password})}
	replayedRegistration, err := service.Register(ctx, register)
	if err != nil || replayedRegistration != registrationResults[0] {
		t.Fatalf("registration replay=%#v error=%v", replayedRegistration, err)
	}
	conflicting := register
	conflicting.Password = "different secure password value"
	if _, err = service.Register(ctx, conflicting); !errors.Is(err, api.ErrIdempotencyConflict) {
		t.Fatalf("registration conflict error=%v", err)
	}

	var tenantID, verificationCommandID, verificationRef, verificationHash string
	if err = admin.QueryRow(ctx, `SELECT id::text FROM identity.tenants WHERE owner_user_id=$1 AND kind='personal'`, userID).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT command_id::text,payload_ref,payload_hash FROM agent.outbox WHERE tenant_id=$1 AND command_type='identity.email.verify'`, tenantID).Scan(&verificationCommandID, &verificationRef, &verificationHash); err != nil {
		t.Fatal(err)
	}
	mailPayload, err := payloadStore.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: verificationCommandID, Class: "mail-command", ContentType: "application/json"}, payload.Manifest{Ref: verificationRef, Hash: verificationHash})
	if err != nil {
		t.Fatal(err)
	}
	var mail struct {
		Recipient string `json:"recipient"`
		Token     string `json:"token"`
	}
	if err = json.Unmarshal(mailPayload, &mail); err != nil || mail.Recipient != register.NormalizedEmail || len(mail.Token) < 32 {
		t.Fatalf("mail=%#v err=%v", mail, err)
	}
	for _, stored := range blobs.values {
		if bytes.Contains(stored, []byte(register.Password)) || bytes.Contains(stored, []byte(mail.Token)) || bytes.Contains(stored, []byte(register.NormalizedEmail)) {
			t.Fatal("plaintext secret or email reached blob storage")
		}
	}

	originalVerificationToken := mail.Token
	resend := api.ResendVerificationCommand{RequestMetadata: api.RequestMetadata{RequestID: "server-resend-001", ClientRequestID: "client-resend-001", IdempotencyKey: "resend-idempotency-001", ClientIPHash: metadata.ClientIPHash, UserAgentHash: metadata.UserAgentHash}, NormalizedEmail: register.NormalizedEmail}
	resendResults := concurrentCalls(t, 8, func() (api.ResendVerificationResult, error) { return service.ResendVerification(ctx, resend) })
	for _, result := range resendResults {
		if result.Status != "accepted" || result.NextAllowedAt != now.Add(verificationResendCooldown) {
			t.Fatalf("resend result=%#v", result)
		}
	}
	var resentCommandID, resentRef, resentHash string
	if err = admin.QueryRow(ctx, `SELECT command_id::text,payload_ref,payload_hash FROM agent.outbox WHERE tenant_id=$1 AND command_type='identity.email.verify' AND command_id<>$2 ORDER BY created_at DESC,id DESC LIMIT 1`, tenantID, verificationCommandID).Scan(&resentCommandID, &resentRef, &resentHash); err != nil {
		t.Fatal(err)
	}
	resentPayload, err := payloadStore.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: resentCommandID, Class: "mail-command", ContentType: "application/json"}, payload.Manifest{Ref: resentRef, Hash: resentHash})
	if err != nil {
		t.Fatal(err)
	}
	var resentMail struct {
		Recipient string `json:"recipient"`
		Token     string `json:"token"`
	}
	if err = json.Unmarshal(resentPayload, &resentMail); err != nil || resentMail.Recipient != register.NormalizedEmail || len(resentMail.Token) < 32 || resentMail.Token == originalVerificationToken {
		t.Fatalf("resent mail=%#v err=%v", resentMail, err)
	}
	var totalVerifications, liveVerifications int
	if err = admin.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE used_at IS NULL AND revoked_at IS NULL) FROM identity.email_verifications WHERE user_id=$1`, userID).Scan(&totalVerifications, &liveVerifications); err != nil || totalVerifications != 2 || liveVerifications != 1 {
		t.Fatalf("verification rows total=%d live=%d err=%v", totalVerifications, liveVerifications, err)
	}
	oldVerify := api.VerifyEmailCommand{RequestMetadata: api.RequestMetadata{RequestID: "server-old-verify", ClientRequestID: "client-old-verify", IdempotencyKey: "old-verify-key-00001", ClientIPHash: metadata.ClientIPHash, UserAgentHash: metadata.UserAgentHash}, Token: originalVerificationToken}
	if _, err = service.VerifyEmail(ctx, oldVerify); !errors.Is(err, api.ErrInvalidCredentials) {
		t.Fatalf("revoked verification token error=%v", err)
	}
	mail.Token = resentMail.Token
	missingResend := resend
	missingResend.RequestID, missingResend.ClientRequestID, missingResend.IdempotencyKey, missingResend.NormalizedEmail = "server-resend-missing", "client-resend-missing", "resend-missing-key-01", "missing-resend@example.com"
	missingResult, err := service.ResendVerification(ctx, missingResend)
	if err != nil || missingResult.Status != "accepted" || missingResult.NextAllowedAt != now.Add(verificationResendCooldown) {
		t.Fatalf("missing resend result=%#v err=%v", missingResult, err)
	}

	verifyMetadata := metadata
	verifyMetadata.RequestID = "server-verify-001"
	verifyMetadata.ClientRequestID = "client-verify-001"
	verifyMetadata.IdempotencyKey = "verify-idempotency-00001"
	verify := api.VerifyEmailCommand{RequestMetadata: verifyMetadata, Token: mail.Token}
	verificationResults := concurrentCalls(t, 8, func() (api.VerifyEmailResult, error) { return service.VerifyEmail(ctx, verify) })
	for _, result := range verificationResults {
		if result.UserID != userID || result.VerifiedAt != now {
			t.Fatalf("verification result=%#v", result)
		}
	}
	otherVerificationKey := verify
	otherVerificationKey.IdempotencyKey = "verify-idempotency-00002"
	if _, err = service.VerifyEmail(ctx, otherVerificationKey); !errors.Is(err, api.ErrInvalidCredentials) {
		t.Fatalf("used token error=%v", err)
	}

	wrong := api.LoginCommand{RequestMetadata: api.RequestMetadata{RequestID: "server-login-fail-1", ClientRequestID: "client-login-fail-1", IdempotencyKey: "login-failure-key-0001", ClientIPHash: metadata.ClientIPHash, UserAgentHash: metadata.UserAgentHash}, NormalizedEmail: register.NormalizedEmail, Password: "wrong password"}
	missing := wrong
	missing.RequestID = "server-login-fail-2"
	missing.NormalizedEmail = "missing@example.com"
	for _, command := range []api.LoginCommand{wrong, missing} {
		if _, err = service.Login(ctx, command); !errors.Is(err, api.ErrInvalidCredentials) {
			t.Fatalf("invalid login error=%v", err)
		}
	}

	login := api.LoginCommand{RequestMetadata: api.RequestMetadata{RequestID: "server-login-001", ClientRequestID: "client-login-001", IdempotencyKey: "login-idempotency-00001", ClientIPHash: metadata.ClientIPHash, UserAgentHash: metadata.UserAgentHash}, NormalizedEmail: register.NormalizedEmail, Password: register.Password}
	loginResults := concurrentCalls(t, 12, func() (api.LoginResult, error) { return service.Login(ctx, login) })
	firstLogin := loginResults[0]
	for _, result := range loginResults {
		if result.UserID != userID || result.SessionID != firstLogin.SessionID || result.SessionToken != firstLogin.SessionToken || result.CSRFToken != firstLogin.CSRFToken || result.ExpiresAt != now.Add(30*24*time.Hour) {
			t.Fatalf("login result=%#v first=%#v", result, firstLogin)
		}
	}
	principal, err := (SessionResolver{Pool: pool, Pepper: service.SessionPepper, Now: service.Now}).Resolve(ctx, firstLogin.SessionToken)
	if err != nil || principal.UserID != userID || principal.TenantID != tenantID || principal.SessionID != firstLogin.SessionID || principal.Roles[0] != "owner" {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	reauthenticationMetadata := api.AuthenticatedRequestMetadata{RequestMetadata: api.RequestMetadata{RequestID: "server-reauth-failed", ClientRequestID: "client-reauth-failed", IdempotencyKey: "reauth-failed-key-0001", ClientIPHash: metadata.ClientIPHash, UserAgentHash: metadata.UserAgentHash}, UserID: userID, TenantID: tenantID, MembershipID: principal.MembershipID, SessionID: firstLogin.SessionID}
	if _, err = service.Reauthenticate(ctx, api.ReauthenticateCommand{AuthenticatedRequestMetadata: reauthenticationMetadata, Password: "wrong password"}); !errors.Is(err, api.ErrInvalidCredentials) {
		t.Fatalf("wrong reauthentication error=%v", err)
	}
	reauthenticationMetadata.RequestID, reauthenticationMetadata.ClientRequestID, reauthenticationMetadata.IdempotencyKey = "server-reauth-001", "client-reauth-001", "reauth-idempotency-0001"
	reauthenticate := api.ReauthenticateCommand{AuthenticatedRequestMetadata: reauthenticationMetadata, Password: register.Password}
	reauthenticationResults := concurrentCalls(t, 8, func() (api.ReauthenticationResult, error) { return service.Reauthenticate(ctx, reauthenticate) })
	for _, result := range reauthenticationResults {
		if result.SessionID != firstLogin.SessionID || result.SessionVersion != 2 || !result.ReauthenticatedAt.Equal(now) || !result.ValidUntil.Equal(now.Add(5*time.Minute)) {
			t.Fatalf("reauthentication result=%#v", result)
		}
	}
	conflictingReauthentication := reauthenticate
	conflictingReauthentication.Password = "different password under reused key"
	if _, err = service.Reauthenticate(ctx, conflictingReauthentication); !errors.Is(err, api.ErrIdempotencyConflict) {
		t.Fatalf("reauthentication idempotency conflict=%v", err)
	}
	var reauthenticationEvents, reauthenticationSuccessAudits, reauthenticationFailureAudits, sessionVersion int
	var reauthenticatedAt time.Time
	if err = admin.QueryRow(ctx, `SELECT version,reauthenticated_at FROM identity.sessions WHERE id=$1`, firstLogin.SessionID).Scan(&sessionVersion, &reauthenticatedAt); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.events WHERE aggregate_id=$1 AND event_type='SessionReauthenticated'`, firstLogin.SessionID).Scan(&reauthenticationEvents); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FILTER (WHERE event_type='session_reauthenticated'),count(*) FILTER (WHERE event_type='session_reauthentication_failed') FROM identity.security_events WHERE subject_user_id=$1`, userID).Scan(&reauthenticationSuccessAudits, &reauthenticationFailureAudits); err != nil {
		t.Fatal(err)
	}
	if sessionVersion != 2 || !reauthenticatedAt.Equal(now) || reauthenticationEvents != 1 || reauthenticationSuccessAudits != 1 || reauthenticationFailureAudits != 1 {
		t.Fatalf("session-version=%d reauthenticated-at=%s events=%d success-audits=%d failure-audits=%d", sessionVersion, reauthenticatedAt, reauthenticationEvents, reauthenticationSuccessAudits, reauthenticationFailureAudits)
	}

	var users, sessions, events, outbox, idempotencyRows, loginFailures int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE normalized_email=$1`, register.NormalizedEmail).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.sessions WHERE user_id=$1`, userID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.events WHERE user_id=$1`, userID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.outbox WHERE tenant_id=$1`, tenantID).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM agent.idempotency_responses WHERE request_id IN ('server-register-001','server-verify-001','server-login-001')`).Scan(&idempotencyRows); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.security_events WHERE event_type='login_failed' AND tenant_id=$1`, authPublicTenantID).Scan(&loginFailures); err != nil {
		t.Fatal(err)
	}
	if users != 1 || sessions != 1 || events != 6 || outbox != 8 || idempotencyRows != 3 || loginFailures != 2 {
		t.Fatalf("users=%d sessions=%d events=%d outbox=%d idempotency=%d login-failures=%d", users, sessions, events, outbox, idempotencyRows, loginFailures)
	}
	var passwordHash, verificationHashBytes, sessionHash []byte
	if err = admin.QueryRow(ctx, `SELECT password_hash FROM identity.password_credentials WHERE user_id=$1`, userID).Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT token_hash FROM identity.email_verifications WHERE user_id=$1`, userID).Scan(&verificationHashBytes); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(ctx, `SELECT token_hash FROM identity.sessions WHERE id=$1`, firstLogin.SessionID).Scan(&sessionHash); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(passwordHash, []byte(register.Password)) || bytes.Contains(verificationHashBytes, []byte(mail.Token)) || bytes.Contains(sessionHash, []byte(firstLogin.SessionToken)) || strings.Contains(string(mailPayload), register.Password) {
		t.Fatal("raw credential reached PostgreSQL or mail command")
	}
}

func concurrentCalls[T any](t *testing.T, count int, call func() (T, error)) []T {
	t.Helper()
	results := make(chan T, count)
	failures := make(chan error, count)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := call()
			if err != nil {
				failures <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Errorf("concurrent call: %v", err)
	}
	values := make([]T, 0, count)
	for result := range results {
		values = append(values, result)
	}
	if len(values) != count {
		t.Fatalf("results=%d want=%d", len(values), count)
	}
	return values
}
