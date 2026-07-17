package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/platform/ids"
	productapi "github.com/langshift/lites/internal/product/api"
	"github.com/langshift/lites/internal/product/contentcatalog"
)

const contentRoleIDDomain = "product-content-role.v1"
const contentRubricIDDomain = "product-content-rubric.v1"

type RoleProfileProjector interface {
	EnsureRoleProfiles(context.Context, pgx.Tx, string, ...string) error
}

// ContentCatalog projects the signed immutable content release into a tenant's
// RLS boundary. IDs include both tenant and release identity so rollback and
// concurrent release revisions never alias one another.
type ContentCatalog struct {
	Pool    *pgxpool.Pool
	Release contentcatalog.Release
	IDKey   []byte
}

type projectedRole struct {
	ID             string
	Slug           string
	Revision       int
	Status         string
	NameEN         string
	NameZH         string
	Spec           []byte
	SourceManifest []byte
}

type projectedRubric struct {
	ID, Slug, Status, PracticeKind string
	Revision                       int
	Spec, Dimensions, ScoringRules []byte
}

func (catalog ContentCatalog) ListRoles(ctx context.Context, query productapi.RoleCatalogQuery) (productapi.RoleCatalogResult, error) {
	if catalog.Pool == nil || len(catalog.IDKey) < 32 || query.TenantID == "" || query.Locale != "en" && query.Locale != "zh-CN" || catalog.Release.Identity() == ":" {
		return productapi.RoleCatalogResult{}, productapi.ErrValidation
	}
	roles, err := catalog.roles(query.TenantID)
	if err != nil {
		return productapi.RoleCatalogResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	tx, err := catalog.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return productapi.RoleCatalogResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, query.TenantID); err != nil {
		return productapi.RoleCatalogResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	requested := make([]string, 0, len(roles))
	for _, role := range roles {
		if role.Status == "active" {
			requested = append(requested, role.ID)
		}
	}
	if err = catalog.ensure(ctx, tx, query.TenantID, roles, requested); err != nil {
		return productapi.RoleCatalogResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	rubrics, err := catalog.rubrics(query.TenantID)
	if err != nil {
		return productapi.RoleCatalogResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	if err = catalog.ensureRubrics(ctx, tx, query.TenantID, rubrics); err != nil {
		return productapi.RoleCatalogResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return productapi.RoleCatalogResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	items := make([]productapi.RoleCatalogItem, 0, len(requested))
	for _, role := range roles {
		if role.Status != "active" {
			continue
		}
		name := role.NameEN
		if query.Locale == "zh-CN" {
			name = role.NameZH
		}
		items = append(items, productapi.RoleCatalogItem{ID: role.ID, Slug: role.Slug, Revision: role.Revision, Status: role.Status, Name: name})
	}
	rubricItems := make([]productapi.RubricCatalogItem, 0, len(rubrics))
	for _, rubric := range rubrics {
		if rubric.Status == "active" {
			rubricItems = append(rubricItems, productapi.RubricCatalogItem{ID: rubric.ID, Slug: rubric.Slug, Revision: rubric.Revision, Status: rubric.Status, PracticeKind: rubric.PracticeKind})
		}
	}
	return productapi.RoleCatalogResult{ReleaseVersion: catalog.Release.Manifest.ReleaseVersion, ContentRootSHA256: catalog.Release.Manifest.ContentRootSHA256, Locale: query.Locale, Items: items, Rubrics: rubricItems}, nil
}

func (catalog ContentCatalog) EnsureRoleProfiles(ctx context.Context, tx pgx.Tx, tenantID string, roleIDs ...string) error {
	if tx == nil || tenantID == "" || len(catalog.IDKey) < 32 || len(roleIDs) == 0 {
		if len(roleIDs) == 0 {
			return nil
		}
		return productapi.ErrValidation
	}
	roles, err := catalog.roles(tenantID)
	if err != nil {
		return err
	}
	if err = catalog.ensure(ctx, tx, tenantID, roles, roleIDs); err != nil {
		return err
	}
	rubrics, err := catalog.rubrics(tenantID)
	if err != nil {
		return err
	}
	return catalog.ensureRubrics(ctx, tx, tenantID, rubrics)
}

func (catalog ContentCatalog) rubrics(tenantID string) ([]projectedRubric, error) {
	result := make([]projectedRubric, 0, len(catalog.Release.Rubrics.Rubrics))
	for _, source := range catalog.Release.Rubrics.Rubrics {
		id, err := ids.DeterministicUUID(catalog.IDKey, contentRubricIDDomain, tenantID+"\x00"+catalog.Release.Identity()+"\x00"+source.ID)
		if err != nil {
			return nil, err
		}
		spec, err := json.Marshal(source)
		if err != nil {
			return nil, err
		}
		dimensions, err := json.Marshal(source.Dimensions)
		if err != nil {
			return nil, err
		}
		scoringRules, err := json.Marshal(map[string]any{"scoring_rule": source.ScoringRule, "scale": source.Scale})
		if err != nil {
			return nil, err
		}
		result = append(result, projectedRubric{ID: id, Slug: source.ID, Revision: source.Revision, Status: source.Status, PracticeKind: source.PracticeKind, Spec: spec, Dimensions: dimensions, ScoringRules: scoringRules})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Slug < result[right].Slug })
	return result, nil
}

func (catalog ContentCatalog) ensureRubrics(ctx context.Context, tx pgx.Tx, tenantID string, rubrics []projectedRubric) error {
	for _, rubric := range rubrics {
		if rubric.Status != "active" {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO product.rubric_versions(id,tenant_id,slug,revision,status,spec,dimensions,scoring_rules) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8::jsonb) ON CONFLICT DO NOTHING`, rubric.ID, tenantID, rubric.Slug, rubric.Revision, rubric.Status, rubric.Spec, rubric.Dimensions, rubric.ScoringRules); err != nil {
			return err
		}
		var slug, status string
		var revision int
		var spec, dimensions, scoringRules []byte
		if err := tx.QueryRow(ctx, `SELECT slug,revision,status,spec::text,dimensions::text,scoring_rules::text FROM product.rubric_versions WHERE tenant_id=$1 AND id=$2`, tenantID, rubric.ID).Scan(&slug, &revision, &status, &spec, &dimensions, &scoringRules); err != nil {
			return err
		}
		if slug != rubric.Slug || revision != rubric.Revision || status != rubric.Status || !equalJSON(spec, rubric.Spec) || !equalJSON(dimensions, rubric.Dimensions) || !equalJSON(scoringRules, rubric.ScoringRules) {
			return errors.New("projected rubric does not match immutable content release")
		}
	}
	return nil
}

func (catalog ContentCatalog) roles(tenantID string) ([]projectedRole, error) {
	manifest, err := json.Marshal(map[string]any{
		"manifest_version":    catalog.Release.Manifest.ManifestVersion,
		"release_version":     catalog.Release.Manifest.ReleaseVersion,
		"content_root_sha256": catalog.Release.Manifest.ContentRootSHA256,
		"release_identity":    catalog.Release.Identity(),
	})
	if err != nil {
		return nil, err
	}
	roles := make([]projectedRole, 0, len(catalog.Release.Ontology.Roles))
	for _, source := range catalog.Release.Ontology.Roles {
		id, idErr := ids.DeterministicUUID(catalog.IDKey, contentRoleIDDomain, tenantID+"\x00"+catalog.Release.Identity()+"\x00"+source.ID)
		if idErr != nil {
			return nil, idErr
		}
		spec, marshalErr := json.Marshal(source)
		if marshalErr != nil {
			return nil, marshalErr
		}
		roles = append(roles, projectedRole{ID: id, Slug: source.ID, Revision: source.Revision, Status: source.Status, NameEN: source.Name.EN, NameZH: source.Name.ZH, Spec: spec, SourceManifest: manifest})
	}
	sort.Slice(roles, func(left, right int) bool { return roles[left].Slug < roles[right].Slug })
	return roles, nil
}

func (catalog ContentCatalog) ensure(ctx context.Context, tx pgx.Tx, tenantID string, roles []projectedRole, requested []string) error {
	byID := make(map[string]projectedRole, len(roles))
	for _, role := range roles {
		byID[role.ID] = role
	}
	seen := make(map[string]struct{}, len(requested))
	for _, roleID := range requested {
		if _, duplicate := seen[roleID]; duplicate {
			continue
		}
		seen[roleID] = struct{}{}
		role, exists := byID[roleID]
		if !exists || role.Status != "active" {
			return ErrMissionNotFound
		}
		if _, err := tx.Exec(ctx, `INSERT INTO product.role_profiles(id,tenant_id,slug,revision,status,spec,locale,source_manifest) VALUES($1,$2,$3,$4,$5,$6::jsonb,'bilingual',$7::jsonb) ON CONFLICT DO NOTHING`, role.ID, tenantID, role.Slug, role.Revision, role.Status, role.Spec, role.SourceManifest); err != nil {
			return err
		}
		var slug, status, locale string
		var revision int
		var spec, sourceManifest []byte
		if err := tx.QueryRow(ctx, `SELECT slug,revision,status,spec::text,locale,source_manifest::text FROM product.role_profiles WHERE tenant_id=$1 AND id=$2`, tenantID, role.ID).Scan(&slug, &revision, &status, &spec, &locale, &sourceManifest); err != nil {
			return err
		}
		if slug != role.Slug || revision != role.Revision || status != role.Status || locale != "bilingual" || !equalJSON(spec, role.Spec) || !equalJSON(sourceManifest, role.SourceManifest) {
			return errors.New("projected role profile does not match immutable content release")
		}
	}
	return nil
}

func equalJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}
