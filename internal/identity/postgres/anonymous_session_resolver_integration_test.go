//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/identity/anonymoussession"
	"github.com/langshift/lites/internal/payload"
)

func TestAnonymousSessionResolverMapsOpaqueHandleToIsolatedEphemeralPrincipal(t *testing.T) {
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
	now := time.Unix(1_800_300_000, 0).UTC()
	key := bytes.Repeat([]byte{0x71}, 32)
	pepper := bytes.Repeat([]byte{0x72}, 32)
	const tenantID = "6a000000-0000-4000-8000-000000000001"
	if _, err = admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region) VALUES($1,'anonymous_system','Anonymous Resolver','active','US')`, tenantID); err != nil {
		t.Fatal(err)
	}
	idEntropy := make([]byte, 0, 80)
	for value := byte(0x74); value < 0x79; value++ {
		idEntropy = append(idEntropy, bytes.Repeat([]byte{value}, 16)...)
	}
	blobs := &authMemoryBlobs{values: map[string][]byte{}}
	payloadStore := payload.EnvelopeStore{Keys: authKeyProvider{key: payload.Key{ID: "anonymous-vault-key-v1", Material: bytes.Repeat([]byte{0x79}, 32)}}, Blobs: blobs, Random: bytes.NewReader(bytes.Repeat([]byte{0x7a}, 12))}
	bootstrapService := AnonymousSessionService{Pool: pool, SystemTenantID: tenantID, Signer: anonymoussession.Signer{KeyID: "anonymous-test", Key: key, DigestPepper: pepper, Random: bytes.NewReader(bytes.Repeat([]byte{0x73}, 32))}, Payloads: payloadStore, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, StoreEpoch: "6a000000-0000-4000-8000-000000000004", TTL: anonymoussession.MaximumTTL, Random: bytes.NewReader(idEntropy), Now: func() time.Time { return now }}
	bootstrap, err := bootstrapService.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolver := AnonymousSessionResolver{Pool: pool, Verifier: anonymoussession.Verifier{Keys: map[string][]byte{"anonymous-test": key}, DigestPepper: pepper}, Now: func() time.Time { return now }}
	principal, err := resolver.Resolve(ctx, bootstrap.Credential.Raw)
	if err != nil || principal.AnonymousSubjectID != bootstrap.Principal.AnonymousSubjectID || principal.UserID != bootstrap.Principal.UserID || principal.TenantID != bootstrap.Principal.TenantID || !principal.ExpiresAt.Equal(bootstrap.Principal.ExpiresAt) || principal.AnonymousSubjectID == principal.UserID {
		t.Fatalf("principal=%#v error=%v", principal, err)
	}
	var eventCount, outboxCount int
	var lastSequence uint64
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent.events WHERE tenant_id=$1 AND user_id=$2 AND event_type='AnonymousSubjectCreated'),(SELECT count(*) FROM agent.outbox WHERE tenant_id=$1 AND aggregate_id=$3),(SELECT last_seq FROM agent.event_cursors WHERE tenant_id=$1 AND user_id=$2)`, tenantID, principal.UserID, principal.AnonymousSubjectID).Scan(&eventCount, &outboxCount, &lastSequence); err != nil || eventCount != 1 || outboxCount != 1 || lastSequence != 1 {
		t.Fatalf("events=%d outbox=%d seq=%d error=%v", eventCount, outboxCount, lastSequence, err)
	}
	var users, memberships int
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM identity.users WHERE id=$1),(SELECT count(*) FROM identity.memberships WHERE user_id=$1)`, principal.UserID).Scan(&users, &memberships); err != nil || users != 0 || memberships != 0 {
		t.Fatalf("users=%d memberships=%d error=%v", users, memberships, err)
	}
	if _, err = resolver.Resolve(ctx, bootstrap.Credential.Raw+"tampered"); !errors.Is(err, anonymoussession.ErrUnauthenticated) {
		t.Fatalf("tamper error=%v", err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.anonymous_subjects SET deleted_at=$1 WHERE id=$2`, now, principal.AnonymousSubjectID); err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.Resolve(ctx, bootstrap.Credential.Raw); !errors.Is(err, anonymoussession.ErrUnauthenticated) {
		t.Fatalf("deleted subject error=%v", err)
	}
}
