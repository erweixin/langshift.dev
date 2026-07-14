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
	"github.com/langshift/lites/internal/identity/api"
	"github.com/langshift/lites/internal/identity/password"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/security/opaque"
)

func TestAccountExportAndErasureAreReauthenticatedIdempotentAndEvented(t *testing.T) {
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
	now := time.Unix(1_800_001_000, 0).UTC()
	const publicTenantID = "20000000-0000-0000-0000-000000001001"
	const userID = "10000000-0000-0000-0000-000000001001"
	const tenantID = "20000000-0000-0000-0000-000000001002"
	const membershipID = "30000000-0000-0000-0000-000000001001"
	const credentialID = "31000000-0000-0000-0000-000000001001"
	const email = "account-lifecycle@example.com"
	const passwordValue = "account lifecycle password"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users (id,normalized_email,email_verified_at,locale,status) VALUES ($1,$2,$3,'en','active')`, []any{userID, email, now}},
		{`INSERT INTO identity.tenants (id,kind,name,status,region) VALUES ($1,'anonymous_system','Account Public','active','US')`, []any{publicTenantID}},
		{`INSERT INTO identity.tenants (id,kind,name,status,region,owner_user_id) VALUES ($1,'personal','Account Lifecycle','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ($1,$2,$3,'owner','active',$4)`, []any{membershipID, tenantID, userID, now}},
	} {
		if _, err = admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	hasher := password.Hasher{Parameters: password.Parameters{Version: password.CurrentVersion, Time: 1, MemoryKiB: 8 * 1024, Threads: 1, KeyLength: 32}, Pepper: bytes.Repeat([]byte{0xc1}, 32)}
	passwordHash, parameters, err := hasher.Hash(passwordValue)
	if err != nil {
		t.Fatal(err)
	}
	encodedParameters, _ := json.Marshal(parameters)
	if _, err = admin.Exec(ctx, `INSERT INTO identity.password_credentials (id,user_id,password_hash,algorithm,parameters,password_changed_at) VALUES ($1,$2,$3,'argon2id',$4,$5)`, credentialID, userID, passwordHash, encodedParameters, now); err != nil {
		t.Fatal(err)
	}
	dummyHash, dummyParameters, _ := hasher.Hash("account lifecycle dummy password")
	blobs := &authMemoryBlobs{values: map[string][]byte{}}
	store := payload.EnvelopeStore{Keys: authKeyProvider{key: payload.Key{ID: "account-vault-key-v1", Material: bytes.Repeat([]byte{0xc2}, 32)}}, Blobs: blobs}
	service := AuthService{
		Pool: pool, Passwords: hasher, PasswordPolicy: password.Policy{Checker: password.NewDigestSet([]string{"known compromised password value"})}, DummyPasswordHash: dummyHash, DummyPasswordParameters: dummyParameters,
		VerificationTokens: opaque.Manager{Purpose: "email-verification", Pepper: bytes.Repeat([]byte{0xc3}, 32)}, PasswordResetTokens: opaque.Manager{Purpose: "password-reset", Pepper: bytes.Repeat([]byte{0xc4}, 32)}, EmailChangeTokens: opaque.Manager{Purpose: "email-change", Pepper: bytes.Repeat([]byte{0xc5}, 32)},
		SessionPepper: bytes.Repeat([]byte{0xc6}, 32), CSRFPepper: bytes.Repeat([]byte{0xc7}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0xc8}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xc9}, 32), IdentityKey: bytes.Repeat([]byte{0xca}, 32), CursorKey: bytes.Repeat([]byte{0xcb}, 32),
		PublicTenantID: publicTenantID, StoreEpoch: "40000000-0000-0000-0000-000000001001", Region: "US", VerificationTTL: 24 * time.Hour, PasswordResetTTL: 30 * time.Minute, EmailChangeTTL: 24 * time.Hour, ReauthenticationTTL: 15 * time.Minute, ErasureGracePeriod: 7 * 24 * time.Hour, SessionTTL: 30 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour,
		Payloads: store, Random: rand.Reader, Now: func() time.Time { return now },
	}
	metadata := func(requestID, key string) api.RequestMetadata {
		return api.RequestMetadata{RequestID: "server-" + requestID, ClientRequestID: requestID, IdempotencyKey: key, ClientIPHash: bytes.Repeat([]byte{0xd1}, 32), UserAgentHash: bytes.Repeat([]byte{0xd2}, 32)}
	}
	login, err := service.Login(ctx, api.LoginCommand{RequestMetadata: metadata("account-login-001", "account-login-key-0001"), NormalizedEmail: email, Password: passwordValue})
	if err != nil {
		t.Fatal(err)
	}
	auth := func(requestID, key string) api.AuthenticatedRequestMetadata {
		return api.AuthenticatedRequestMetadata{RequestMetadata: metadata(requestID, key), UserID: userID, TenantID: tenantID, MembershipID: membershipID, SessionID: login.SessionID}
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, now.Add(-16*time.Minute), login.SessionID); err != nil {
		t.Fatal(err)
	}
	stale := api.AccountExportCreateCommand{AuthenticatedRequestMetadata: auth("stale-export-001", "stale-export-key-0001"), Scope: []string{"account"}, Format: "json"}
	if _, err = service.CreateAccountExport(ctx, stale); !errors.Is(err, api.ErrReauthenticationRequired) {
		t.Fatalf("stale reauthentication error=%v", err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, now, login.SessionID); err != nil {
		t.Fatal(err)
	}
	export := api.AccountExportCreateCommand{AuthenticatedRequestMetadata: auth("account-export-001", "account-export-key-0001"), Scope: []string{"account", "evidence", "projects"}, Format: "zip"}
	exports := concurrentCalls(t, 12, func() (api.AccountMutationResult, error) { return service.CreateAccountExport(ctx, export) })
	for _, result := range exports {
		if result != exports[0] || result.Version != 1 || result.Status != "requested" {
			t.Fatalf("export=%#v first=%#v", result, exports[0])
		}
	}
	conflictingExport := export
	conflictingExport.Format = "json"
	if _, err = service.CreateAccountExport(ctx, conflictingExport); !errors.Is(err, api.ErrIdempotencyConflict) {
		t.Fatalf("export conflict=%v", err)
	}
	reason := "I want all private data removed"
	erasure := api.AccountErasureCreateCommand{AuthenticatedRequestMetadata: auth("account-erasure-001", "account-erasure-key-0001"), Reason: &reason}
	erasures := concurrentCalls(t, 12, func() (api.AccountMutationResult, error) { return service.CreateAccountErasure(ctx, erasure) })
	firstErasure := erasures[0]
	for _, result := range erasures {
		if result != firstErasure || result.Version != 1 || result.Status != "requested" {
			t.Fatalf("erasure=%#v first=%#v", result, firstErasure)
		}
	}
	secondActive := api.AccountErasureCreateCommand{AuthenticatedRequestMetadata: auth("account-erasure-active", "account-erasure-key-0002")}
	if _, err = service.CreateAccountErasure(ctx, secondActive); !errors.Is(err, api.ErrStateConflict) {
		t.Fatalf("second active erasure=%v", err)
	}
	staleCancel := api.AccountErasureCancelCommand{AuthenticatedRequestMetadata: auth("account-cancel-stale", "account-cancel-key-0001"), ErasureID: firstErasure.ID, ExpectedVersion: 2}
	if _, err = service.CancelAccountErasure(ctx, staleCancel); !errors.Is(err, api.ErrVersionConflict) {
		t.Fatalf("stale cancel=%v", err)
	}
	cancel := api.AccountErasureCancelCommand{AuthenticatedRequestMetadata: auth("account-cancel-001", "account-cancel-key-0002"), ErasureID: firstErasure.ID, ExpectedVersion: 1}
	cancellations := concurrentCalls(t, 12, func() (api.AccountMutationResult, error) { return service.CancelAccountErasure(ctx, cancel) })
	for _, result := range cancellations {
		if result != cancellations[0] || result.Version != 2 || result.Status != "cancelled" {
			t.Fatalf("cancel=%#v", result)
		}
	}
	secondErasure := api.AccountErasureCreateCommand{AuthenticatedRequestMetadata: auth("account-erasure-002", "account-erasure-key-0003")}
	secondResult, err := service.CreateAccountErasure(ctx, secondErasure)
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(service.ErasureGracePeriod + time.Second)
	service.Now = func() time.Time { return later }
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, later, login.SessionID); err != nil {
		t.Fatal(err)
	}
	overdue := api.AccountErasureCancelCommand{AuthenticatedRequestMetadata: auth("account-cancel-overdue", "account-cancel-key-0003"), ErasureID: secondResult.ID, ExpectedVersion: 1}
	if _, err = service.CancelAccountErasure(ctx, overdue); !errors.Is(err, api.ErrStateConflict) {
		t.Fatalf("overdue cancel=%v", err)
	}
	service.Now = func() time.Time { return now }
	var exportsCount, erasuresCount, exportEvents, requestedEvents, cancelledEvents, exportWork, erasureWork int
	var deletionRequestedAt *time.Time
	if err = admin.QueryRow(ctx, `SELECT deletion_requested_at FROM identity.users WHERE id=$1`, userID).Scan(&deletionRequestedAt); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		query string
		out   *int
	}{
		{`SELECT count(*) FROM product.data_export_requests WHERE user_id='10000000-0000-0000-0000-000000001001'`, &exportsCount},
		{`SELECT count(*) FROM identity.account_erasure_requests WHERE user_id='10000000-0000-0000-0000-000000001001'`, &erasuresCount},
		{`SELECT count(*) FROM agent.events WHERE user_id='10000000-0000-0000-0000-000000001001' AND event_type='AccountExportRequested'`, &exportEvents},
		{`SELECT count(*) FROM agent.events WHERE user_id='10000000-0000-0000-0000-000000001001' AND event_type='AccountErasureRequested'`, &requestedEvents},
		{`SELECT count(*) FROM agent.events WHERE user_id='10000000-0000-0000-0000-000000001001' AND event_type='AccountErasureCancelled'`, &cancelledEvents},
		{`SELECT count(*) FROM agent.outbox WHERE command_type='account.export.prepare'`, &exportWork},
		{`SELECT count(*) FROM agent.outbox WHERE command_type='account.erasure.schedule'`, &erasureWork},
	}
	for _, check := range checks {
		if err = admin.QueryRow(ctx, check.query).Scan(check.out); err != nil {
			t.Fatal(err)
		}
	}
	if exportsCount != 1 || erasuresCount != 2 || exportEvents != 1 || requestedEvents != 2 || cancelledEvents != 1 || exportWork != 1 || erasureWork != 2 || deletionRequestedAt == nil {
		t.Fatalf("exports=%d erasures=%d export-events=%d requested=%d cancelled=%d export-work=%d erasure-work=%d deletion=%v", exportsCount, erasuresCount, exportEvents, requestedEvents, cancelledEvents, exportWork, erasureWork, deletionRequestedAt)
	}
	var storedScope []byte
	if err = admin.QueryRow(ctx, `SELECT scope FROM product.data_export_requests WHERE id=$1`, exports[0].ID).Scan(&storedScope); err != nil {
		t.Fatalf("stored scope=%s error=%v", storedScope, err)
	}
	var scopeDocument struct {
		Categories []string `json:"categories"`
		Format     string   `json:"format"`
	}
	if err = json.Unmarshal(storedScope, &scopeDocument); err != nil || scopeDocument.Format != "zip" || len(scopeDocument.Categories) != 3 {
		t.Fatalf("stored scope=%s error=%v", storedScope, err)
	}
	var reasonRef, reasonHash string
	if err = admin.QueryRow(ctx, `SELECT details->>'reason_ref',details->>'reason_hash' FROM identity.security_events WHERE event_type='account_erasure_requested' AND request_id='server-account-erasure-001'`).Scan(&reasonRef, &reasonHash); err != nil {
		t.Fatal(err)
	}
	encodedReason, err := store.Get(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: firstErasure.ID, Class: "account-erasure-reason", ContentType: "application/json"}, payload.Manifest{Ref: reasonRef, Hash: reasonHash})
	if err != nil || !bytes.Contains(encodedReason, []byte(reason)) {
		t.Fatalf("reason payload=%s error=%v", encodedReason, err)
	}
	for _, stored := range blobs.values {
		if bytes.Contains(stored, []byte(reason)) {
			t.Fatal("plaintext erasure reason reached object storage")
		}
	}
	var piiAudit int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.security_events WHERE subject_user_id=$1 AND details::text LIKE '%I want all private data removed%'`, userID).Scan(&piiAudit); err != nil || piiAudit != 0 {
		t.Fatalf("PII audit rows=%d err=%v", piiAudit, err)
	}
}
