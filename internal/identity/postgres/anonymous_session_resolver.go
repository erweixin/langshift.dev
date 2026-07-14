package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/identity/anonymoussession"
)

type AnonymousSessionResolver struct {
	Pool     *pgxpool.Pool
	Verifier anonymoussession.Verifier
	Now      func() time.Time
}

func (resolver AnonymousSessionResolver) Resolve(ctx context.Context, rawHandle string) (anonymoussession.Principal, error) {
	if resolver.Pool == nil {
		return anonymoussession.Principal{}, anonymoussession.ErrUnauthenticated
	}
	now := time.Now().UTC()
	if resolver.Now != nil {
		now = resolver.Now().UTC()
	}
	credential, err := resolver.Verifier.Verify(rawHandle, now)
	if err != nil {
		return anonymoussession.Principal{}, anonymoussession.ErrUnauthenticated
	}
	var principal anonymoussession.Principal
	err = resolver.Pool.QueryRow(ctx, `
		SELECT subject.id::text,subject.ephemeral_user_id::text,subject.system_tenant_id::text,subject.expires_at
		FROM identity.anonymous_subjects AS subject
		JOIN identity.tenants AS tenant ON tenant.id=subject.system_tenant_id
		WHERE subject.anonymous_subject_hash=$1
		  AND subject.deleted_at IS NULL
		  AND subject.expires_at>$2
		  AND tenant.kind='anonymous_system'
		  AND tenant.status='active'`, credential.Digest[:], now).
		Scan(&principal.AnonymousSubjectID, &principal.UserID, &principal.TenantID, &principal.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return anonymoussession.Principal{}, anonymoussession.ErrUnauthenticated
		}
		return anonymoussession.Principal{}, err
	}
	if credential.ExpiresAt.Before(principal.ExpiresAt) {
		principal.ExpiresAt = credential.ExpiresAt
	}
	if principal.AnonymousSubjectID == "" || principal.UserID == "" || principal.TenantID == "" || !principal.ExpiresAt.After(now) {
		return anonymoussession.Principal{}, anonymoussession.ErrUnauthenticated
	}
	return principal, nil
}

var _ anonymoussession.Resolver = AnonymousSessionResolver{}
