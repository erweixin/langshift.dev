//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	productapi "github.com/langshift/lites/internal/product/api"
)

type settingsVaultStub struct {
	mu        sync.Mutex
	values    map[string]string
	destroyed map[string]bool
}

func (vault *settingsVaultStub) PutAPIKey(_ context.Context, ref, value string) (string, bool, error) {
	vault.mu.Lock()
	defer vault.mu.Unlock()
	if existing, ok := vault.values[ref]; ok {
		if existing != value {
			return "", false, errors.New("immutable secret conflict")
		}
		return "1", false, nil
	}
	vault.values[ref] = value
	return "1", true, nil
}

func (vault *settingsVaultStub) DestroySecretVersion(_ context.Context, ref string, version int) error {
	vault.mu.Lock()
	defer vault.mu.Unlock()
	if version != 1 || vault.values[ref] == "" {
		return errors.New("secret version not found")
	}
	vault.destroyed[ref+":"+strconv.Itoa(version)] = true
	return nil
}

func TestSettingsServicesPersistReplayAuditAndReauthenticationBoundaries(t *testing.T) {
	ctx := context.Background()
	admin := artifactPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	pool := artifactPool(t, ctx, "LITES_TEST_PRODUCT_DATABASE_URL")
	defer pool.Close()

	now := time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC)
	userID := "be100000-0000-4000-8000-000000000001"
	tenantID := "be100000-0000-4000-8000-000000000002"
	sessionID := "be100000-0000-4000-8000-000000000003"
	epoch := "be100000-0000-4000-8000-000000000004"
	if _, err := admin.Exec(ctx, `DELETE FROM identity.tenants WHERE id=$1`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `DELETE FROM identity.users WHERE id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	setup := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO identity.users(id,normalized_email,locale,status) VALUES($1,'settings-owner@example.invalid','en','active')`, []any{userID}},
		{`INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Settings Tenant','active','US',$2)`, []any{tenantID, userID}},
		{`INSERT INTO identity.sessions(id,user_id,active_tenant_id,token_hash,csrf_secret_hash,ip_hash,user_agent_hash,last_seen_at,expires_at,reauthenticated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$8)`, []any{sessionID, userID, tenantID, bytes.Repeat([]byte{0xb1}, 32), bytes.Repeat([]byte{0xb2}, 32), bytes.Repeat([]byte{0xb3}, 32), bytes.Repeat([]byte{0xb4}, 32), now, now.Add(time.Hour)}},
	}
	for _, statement := range setup {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	key := bytes.Repeat([]byte{0xb5}, 32)
	payloads := &missionPayloadStore{values: map[string][]byte{}}
	vault := &settingsVaultStub{values: map[string]string{}, destroyed: map[string]bool{}}
	common := productapi.CommandMetadata{RequestID: "be100000-0000-4000-8000-000000000005", ClientRequestID: "settings-client-be1", IdempotencyKey: "settings-idempotency-key-be1", TenantID: tenantID, UserID: userID, SessionID: sessionID}
	memory := MemoryPolicyService{Pool: pool, Appender: eventAppender(now), Payloads: payloads, IDKey: key, IdempotencyKeyPepper: bytes.Repeat([]byte{0xb6}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xb7}, 32), StoreEpoch: epoch, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	byok := BYOKService{Pool: pool, Appender: eventAppender(now), Payloads: payloads, Vault: vault, SecretPrefix: "lites/byok", IDKey: key, CursorKey: bytes.Repeat([]byte{0xb8}, 32), IdempotencyKeyPepper: bytes.Repeat([]byte{0xb9}, 32), RequestDigestPepper: bytes.Repeat([]byte{0xba}, 32), StoreEpoch: epoch, IdempotencyTTL: 24 * time.Hour, Now: func() time.Time { return now }}

	defaultPolicy, err := memory.Get(ctx, productapi.MemoryPolicyQuery{RequestID: common.RequestID, TenantID: tenantID, UserID: userID, SessionID: sessionID})
	if err != nil || defaultPolicy.Version != 1 || !defaultPolicy.Enabled || defaultPolicy.RetentionDays == nil || *defaultPolicy.RetentionDays != 365 {
		t.Fatalf("default policy=%#v err=%v", defaultPolicy, err)
	}
	retention := 90
	updated, err := memory.Update(ctx, productapi.MemoryPolicyUpdateCommand{CommandMetadata: common, Enabled: true, RetentionDays: &retention, AllowedKinds: []string{"preference", "goal"}, ExpectedVersion: 1})
	if err != nil || updated.Version != 2 || updated.Replayed {
		t.Fatalf("updated policy=%#v err=%v", updated, err)
	}
	replayedPolicy, err := memory.Update(ctx, productapi.MemoryPolicyUpdateCommand{CommandMetadata: common, Enabled: true, RetentionDays: &retention, AllowedKinds: []string{"preference", "goal"}, ExpectedVersion: 1})
	if err != nil || !replayedPolicy.Replayed || replayedPolicy.Version != 2 {
		t.Fatalf("replayed policy=%#v err=%v", replayedPolicy, err)
	}

	byokCommand := common
	byokCommand.RequestID = "be100000-0000-4000-8000-000000000006"
	byokCommand.ClientRequestID = "byok-client-be1"
	byokCommand.IdempotencyKey = "byok-idempotency-key-be1"
	created, err := byok.Create(ctx, productapi.BYOKCreateCommand{CommandMetadata: byokCommand, ProviderID: "openai_compatible", Endpoint: "https://models.example.com/v1", APIKey: "provider-secret-be1"})
	if err != nil || created.Version != 1 || created.Status != "active" || created.BoundHost != "models.example.com" || created.SecretHint != "••••-be1" {
		t.Fatalf("created credential=%#v err=%v", created, err)
	}
	replayedCredential, err := byok.Create(ctx, productapi.BYOKCreateCommand{CommandMetadata: byokCommand, ProviderID: "openai_compatible", Endpoint: "https://models.example.com/v1", APIKey: "provider-secret-be1"})
	if err != nil || !replayedCredential.Replayed || replayedCredential.ID != created.ID {
		t.Fatalf("replayed credential=%#v err=%v", replayedCredential, err)
	}
	listed, err := byok.List(ctx, productapi.BYOKListQuery{RequestID: "be100000-0000-4000-8000-000000000007", TenantID: tenantID, UserID: userID, SessionID: sessionID})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].ID != created.ID {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
	deleteMetadata := byokCommand
	deleteMetadata.RequestID = "be100000-0000-4000-8000-000000000008"
	deleteMetadata.ClientRequestID = "byok-delete-client-be1"
	deleteMetadata.IdempotencyKey = "byok-delete-idempotency-be1"
	deleted, err := byok.Delete(ctx, productapi.BYOKDeleteCommand{CommandMetadata: deleteMetadata, CredentialID: created.ID, ExpectedVersion: 1})
	if err != nil || deleted.Version != 2 || deleted.Status != "revoked" {
		t.Fatalf("deleted=%#v err=%v", deleted, err)
	}

	var auditRows, byokEvents, memoryEvents int
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM product.settings_access_audit WHERE tenant_id=$1),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND event_type IN ('ByokCredentialConfigured','ByokCredentialDeleted')),(SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND event_type='MemoryPolicyChanged')`, tenantID).Scan(&auditRows, &byokEvents, &memoryEvents); err != nil || auditRows != 5 || byokEvents != 2 || memoryEvents != 2 {
		t.Fatalf("audits=%d byok_events=%d memory_events=%d err=%v", auditRows, byokEvents, memoryEvents, err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET reauthenticated_at=$1 WHERE id=$2`, now.Add(-6*time.Minute), sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = byok.List(ctx, productapi.BYOKListQuery{RequestID: "be100000-0000-4000-8000-000000000009", TenantID: tenantID, UserID: userID, SessionID: sessionID}); !errors.Is(err, productapi.ErrReauthentication) {
		t.Fatalf("stale reauthentication err=%v", err)
	}
}
