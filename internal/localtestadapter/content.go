package localtestadapter

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	productapi "github.com/langshift/lites/internal/product/api"
	"github.com/langshift/lites/internal/product/contentcatalog"
	productpostgres "github.com/langshift/lites/internal/product/postgres"
)

// EnsurePublicContent projects the immutable product release into the public
// anonymous tenant before a browser starts onboarding. The projection uses the
// same ContentCatalog implementation and IDs as product-service and Claim.
func EnsurePublicContent(ctx context.Context, pool *pgxpool.Pool, release contentcatalog.Release, tenantID string) error {
	if pool == nil || tenantID == "" || release.Identity() == ":" {
		return ErrInvalid
	}
	_, err := (productpostgres.ContentCatalog{Pool: pool, Release: release}).ListRoles(ctx, productapi.RoleCatalogQuery{TenantID: tenantID, Locale: "en"})
	return err
}
