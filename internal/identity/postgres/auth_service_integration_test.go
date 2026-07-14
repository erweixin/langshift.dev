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
		ReauthenticationTTL:     15 * time.Minute,
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
	if users != 1 || sessions != 1 || events != 4 || outbox != 5 || idempotencyRows != 3 || loginFailures != 2 {
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
