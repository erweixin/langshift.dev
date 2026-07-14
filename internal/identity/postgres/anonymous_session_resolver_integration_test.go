//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/identity/anonymoussession"
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
	idEntropy := append(bytes.Repeat([]byte{0x74}, 16), bytes.Repeat([]byte{0x75}, 16)...)
	bootstrapService := AnonymousSessionService{Pool: pool, SystemTenantID: tenantID, Signer: anonymoussession.Signer{KeyID: "anonymous-test", Key: key, DigestPepper: pepper, Random: bytes.NewReader(bytes.Repeat([]byte{0x73}, 32))}, TTL: anonymoussession.MaximumTTL, Random: bytes.NewReader(idEntropy), Now: func() time.Time { return now }}
	bootstrap, err := bootstrapService.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolver := AnonymousSessionResolver{Pool: pool, Verifier: anonymoussession.Verifier{Keys: map[string][]byte{"anonymous-test": key}, DigestPepper: pepper}, Now: func() time.Time { return now }}
	principal, err := resolver.Resolve(ctx, bootstrap.Credential.Raw)
	if err != nil || principal.AnonymousSubjectID != bootstrap.Principal.AnonymousSubjectID || principal.UserID != bootstrap.Principal.UserID || principal.TenantID != bootstrap.Principal.TenantID || !principal.ExpiresAt.Equal(bootstrap.Principal.ExpiresAt) || principal.AnonymousSubjectID == principal.UserID {
		t.Fatalf("principal=%#v error=%v", principal, err)
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
