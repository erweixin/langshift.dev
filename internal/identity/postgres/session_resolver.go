// Package postgres implements identity persistence on PostgreSQL. Every
// tenant-scoped query sets a transaction-local tenant context before access.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/identity/session"
)

type SessionResolver struct {
	Pool   *pgxpool.Pool
	Pepper []byte
	Now    func() time.Time
}

func (resolver SessionResolver) Resolve(ctx context.Context, rawToken string) (session.Principal, error) {
	if resolver.Pool == nil {
		return session.Principal{}, session.ErrUnauthenticated
	}
	digest, err := session.Digest(rawToken, resolver.Pepper)
	if err != nil {
		return session.Principal{}, session.ErrUnauthenticated
	}
	now := time.Now().UTC()
	if resolver.Now != nil {
		now = resolver.Now().UTC()
	}
	tx, err := resolver.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
	if err != nil {
		return session.Principal{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var principal session.Principal
	err = tx.QueryRow(ctx, `
		SELECT s.id::text, s.user_id::text, s.active_tenant_id::text, s.expires_at
		FROM identity.sessions AS s
		JOIN identity.users AS u ON u.id = s.user_id
		WHERE s.token_hash = $1
		  AND s.revoked_at IS NULL
		  AND s.expires_at > $2
		  AND u.status = 'active'
		  AND u.email_verified_at IS NOT NULL`, digest[:], now).
		Scan(&principal.SessionID, &principal.UserID, &principal.TenantID, &principal.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return session.Principal{}, session.ErrUnauthenticated
		}
		return session.Principal{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id', $1, true)`, principal.TenantID); err != nil {
		return session.Principal{}, err
	}
	var role string
	err = tx.QueryRow(ctx, `
		SELECT id::text, role
		FROM identity.memberships
		WHERE tenant_id = $1 AND user_id = $2 AND status = 'active'`, principal.TenantID, principal.UserID).
		Scan(&principal.MembershipID, &role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return session.Principal{}, session.ErrUnauthenticated
		}
		return session.Principal{}, err
	}
	principal.Roles = []string{role}
	if err = tx.Commit(ctx); err != nil {
		return session.Principal{}, err
	}
	return principal, nil
}
