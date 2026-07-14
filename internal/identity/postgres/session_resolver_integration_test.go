//go:build integration

package postgres

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/identity/session"
)

func TestSessionResolverUsesPersistedTenantAndActiveMembership(t *testing.T) {
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, requiredEnv(t, "LITES_TEST_ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	servicePool, err := pgxpool.New(ctx, requiredEnv(t, "LITES_TEST_IDENTITY_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer servicePool.Close()
	var visibleWithoutTenant int
	if err = servicePool.QueryRow(ctx, `SELECT count(*) FROM identity.memberships`).Scan(&visibleWithoutTenant); err != nil {
		t.Fatal(err)
	}
	if visibleWithoutTenant != 0 {
		t.Fatalf("memberships visible without tenant context=%d", visibleWithoutTenant)
	}

	const userID = "10000000-0000-0000-0000-000000000101"
	const tenantA = "20000000-0000-0000-0000-000000000101"
	const tenantB = "20000000-0000-0000-0000-000000000102"
	const membershipID = "30000000-0000-0000-0000-000000000101"
	const sessionID = "40000000-0000-0000-0000-000000000101"
	pepper := bytes.Repeat([]byte{0x42}, 32)
	credential, err := session.NewFrom(bytes.NewReader(bytes.Repeat([]byte{0x24}, 32)), pepper)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	if _, err = admin.Exec(ctx, `INSERT INTO identity.users (id, normalized_email, email_verified_at, locale, status) VALUES ($1,'resolver@lites.invalid',$2,'en','active')`, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.tenants (id,kind,name,status,region,owner_user_id) VALUES ($1,'personal','A','active','US',$2),($3,'enterprise','B','active','US',NULL)`, tenantA, userID, tenantB); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ($1,$2,$3,'member','active',$4)`, membershipID, tenantA, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.sessions (id,user_id,active_tenant_id,token_hash,csrf_secret_hash,ip_hash,user_agent_hash,last_seen_at,expires_at) VALUES ($1,$2,$3,$4,$5,$5,$5,$6,$7)`, sessionID, userID, tenantA, credential.Digest[:], bytes.Repeat([]byte{1}, 32), now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	resolver := SessionResolver{Pool: servicePool, Pepper: pepper, Now: func() time.Time { return now }}
	principal, err := resolver.Resolve(ctx, credential.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if principal.UserID != userID || principal.TenantID != tenantA || principal.MembershipID != membershipID || principal.Roles[0] != "member" {
		t.Fatalf("principal=%#v", principal)
	}

	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET active_tenant_id=$1, version=version+1 WHERE id=$2`, tenantB, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.Resolve(ctx, credential.Raw); !errors.Is(err, session.ErrUnauthenticated) {
		t.Fatalf("tenant without membership error=%v", err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET active_tenant_id=$1, version=version+1 WHERE id=$2`, tenantA, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.memberships SET status='suspended', version=version+1 WHERE id=$1`, membershipID); err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.Resolve(ctx, credential.Raw); !errors.Is(err, session.ErrUnauthenticated) {
		t.Fatalf("suspended membership error=%v", err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.memberships SET status='active', version=version+1 WHERE id=$1`, membershipID); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$1, version=version+1 WHERE id=$2`, now, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = resolver.Resolve(ctx, credential.Raw); !errors.Is(err, session.ErrUnauthenticated) {
		t.Fatalf("revoked session error=%v", err)
	}
}

func TestSwitchActiveTenantRequiresMembershipCASAndAudit(t *testing.T) {
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, requiredEnv(t, "LITES_TEST_ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	servicePool, err := pgxpool.New(ctx, requiredEnv(t, "LITES_TEST_IDENTITY_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer servicePool.Close()
	const userID = "10000000-0000-0000-0000-000000000201"
	const tenantA = "20000000-0000-0000-0000-000000000201"
	const tenantB = "20000000-0000-0000-0000-000000000202"
	const membershipA = "30000000-0000-0000-0000-000000000201"
	const membershipB = "30000000-0000-0000-0000-000000000202"
	const sessionID = "40000000-0000-0000-0000-000000000201"
	pepper := bytes.Repeat([]byte{0x52}, 32)
	credential, err := session.NewFrom(bytes.NewReader(bytes.Repeat([]byte{0x34}, 32)), pepper)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_100, 0).UTC()
	hash := bytes.Repeat([]byte{2}, 32)
	if _, err = admin.Exec(ctx, `INSERT INTO identity.users (id,normalized_email,email_verified_at,locale,status) VALUES ($1,'switch@lites.invalid',$2,'en','active')`, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.tenants (id,kind,name,status,region,owner_user_id) VALUES ($1,'personal','A2','active','US',$2),($3,'enterprise','B2','active','US',NULL)`, tenantA, userID, tenantB); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ($1,$2,$3,'member','active',$4)`, membershipA, tenantA, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.sessions (id,user_id,active_tenant_id,token_hash,csrf_secret_hash,ip_hash,user_agent_hash,last_seen_at,expires_at) VALUES ($1,$2,$3,$4,$5,$5,$5,$6,$7)`, sessionID, userID, tenantA, credential.Digest[:], hash, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	store := SessionStore{Pool: servicePool, Now: func() time.Time { return now }}
	base := SwitchTenantInput{SessionID: sessionID, UserID: userID, TargetTenantID: tenantB, ExpectedVersion: 1, SecurityEventID: "50000000-0000-0000-0000-000000000201", RequestID: "request-switch-1", IPHash: hash, UserAgentHash: hash}
	if _, err = store.SwitchActiveTenant(ctx, base); !errors.Is(err, session.ErrTenantUnavailable) {
		t.Fatalf("missing membership error=%v", err)
	}
	if _, err = admin.Exec(ctx, `INSERT INTO identity.memberships (id,tenant_id,user_id,role,status,joined_at) VALUES ($1,$2,$3,'reviewer','active',$4)`, membershipB, tenantB, userID, now); err != nil {
		t.Fatal(err)
	}
	version, err := store.SwitchActiveTenant(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("version=%d", version)
	}
	resolver := SessionResolver{Pool: servicePool, Pepper: pepper, Now: func() time.Time { return now }}
	principal, err := resolver.Resolve(ctx, credential.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if principal.TenantID != tenantB || principal.MembershipID != membershipB || principal.Roles[0] != "reviewer" {
		t.Fatalf("principal=%#v", principal)
	}
	if _, err = store.SwitchActiveTenant(ctx, base); !errors.Is(err, session.ErrVersionConflict) {
		t.Fatalf("stale CAS error=%v", err)
	}
	var events int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM identity.security_events WHERE id=$1 AND tenant_id=$2 AND actor_user_id=$3 AND event_type='session_active_tenant_changed'`, base.SecurityEventID, tenantB, userID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("audit events=%d", events)
	}
}

func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("missing %s", name)
	}
	return value
}
