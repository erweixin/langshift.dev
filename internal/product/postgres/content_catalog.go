package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
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
const contentCapabilityIDDomain = "product-content-capability.v1"
const contentRequirementIDDomain = "product-content-role-requirement.v1"

type RoleProfileProjector interface {
	EnsureRoleProfiles(context.Context, pgx.Tx, string, ...string) error
}

type CapabilityProjector interface {
	EnsureCapabilities(context.Context, pgx.Tx, string, ...string) error
}

// ContentCatalog projects the signed immutable content release into a tenant's
// RLS boundary. IDs include both tenant and release identity so rollback and
// concurrent release revisions never alias one another.
type ContentCatalog struct {
	Pool    *pgxpool.Pool
	Release contentcatalog.Release
	// IDKey is retained for source compatibility with pre-RC callers. Content
	// projection IDs intentionally use a public release-derived namespace:
	// identifiers are tenant-scoped references, not authorization secrets, and
	// every service that imports an anonymous route must be able to reproduce
	// the exact target IDs without sharing a product-service authority key.
	IDKey []byte
}

// ContentProjection contains the tenant-local IDs for immutable catalog slugs.
// Maps contain only the slugs explicitly requested by the caller.
type ContentProjection struct {
	RoleIDs       map[string]string
	CapabilityIDs map[string]string
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

type projectedCapability struct {
	ID, Slug, Status       string
	Revision               int
	Spec, EvidenceGuidance []byte
}

type projectedRequirement struct {
	ID, RoleProfileID, CapabilityID, RequirementLevel, Rationale string
	Revision                                                     int
}

func (catalog ContentCatalog) ListRoles(ctx context.Context, query productapi.RoleCatalogQuery) (productapi.RoleCatalogResult, error) {
	if catalog.Pool == nil || query.TenantID == "" || query.Locale != "en" && query.Locale != "zh-CN" || catalog.Release.Identity() == ":" {
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
	capabilities, err := catalog.capabilities(query.TenantID)
	if err != nil {
		return productapi.RoleCatalogResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	capabilityIDs := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		if capability.Status == "active" {
			capabilityIDs = append(capabilityIDs, capability.ID)
		}
	}
	if err = catalog.ensureCapabilities(ctx, tx, query.TenantID, capabilities, capabilityIDs); err != nil {
		return productapi.RoleCatalogResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	requirements, err := catalog.requirements(query.TenantID, roles, capabilities)
	if err != nil {
		return productapi.RoleCatalogResult{}, errors.Join(productapi.ErrDependencyUnavailable, err)
	}
	if err = catalog.ensureRequirements(ctx, tx, query.TenantID, requirements, requested); err != nil {
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
	if tx == nil || tenantID == "" || catalog.Release.Identity() == ":" || len(roleIDs) == 0 {
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
	capabilities, err := catalog.capabilities(tenantID)
	if err != nil {
		return err
	}
	requirements, err := catalog.requirements(tenantID, roles, capabilities)
	if err != nil {
		return err
	}
	capabilityIDs := requirementCapabilityIDs(requirements, roleIDs)
	if err = catalog.ensureCapabilities(ctx, tx, tenantID, capabilities, capabilityIDs); err != nil {
		return err
	}
	if err = catalog.ensureRequirements(ctx, tx, tenantID, requirements, roleIDs); err != nil {
		return err
	}
	rubrics, err := catalog.rubrics(tenantID)
	if err != nil {
		return err
	}
	return catalog.ensureRubrics(ctx, tx, tenantID, rubrics)
}

func (catalog ContentCatalog) EnsureCapabilities(ctx context.Context, tx pgx.Tx, tenantID string, capabilityIDs ...string) error {
	if tx == nil || tenantID == "" || catalog.Release.Identity() == ":" || len(capabilityIDs) == 0 {
		if len(capabilityIDs) == 0 {
			return nil
		}
		return productapi.ErrValidation
	}
	capabilities, err := catalog.capabilities(tenantID)
	if err != nil {
		return err
	}
	return catalog.ensureCapabilities(ctx, tx, tenantID, capabilities, capabilityIDs)
}

// ProjectSlugs materializes immutable release records inside one tenant and
// returns their tenant-local IDs. It is used by the anonymous Claim saga to
// replace every source-tenant content reference before the source is erased.
func (catalog ContentCatalog) ProjectSlugs(ctx context.Context, tx pgx.Tx, tenantID string, roleSlugs, capabilitySlugs []string) (ContentProjection, error) {
	result := ContentProjection{RoleIDs: map[string]string{}, CapabilityIDs: map[string]string{}}
	if tx == nil || tenantID == "" || catalog.Release.Identity() == ":" || len(roleSlugs) == 0 {
		return ContentProjection{}, productapi.ErrValidation
	}
	roles, err := catalog.roles(tenantID)
	if err != nil {
		return ContentProjection{}, err
	}
	rolesBySlug := make(map[string]projectedRole, len(roles))
	requestedRoles := make([]string, 0, len(roleSlugs))
	for _, role := range roles {
		rolesBySlug[role.Slug] = role
	}
	seenRoles := map[string]struct{}{}
	for _, slug := range roleSlugs {
		if _, duplicate := seenRoles[slug]; duplicate {
			continue
		}
		seenRoles[slug] = struct{}{}
		role, exists := rolesBySlug[slug]
		if !exists || role.Status != "active" {
			return ContentProjection{}, ErrMissionNotFound
		}
		requestedRoles = append(requestedRoles, role.ID)
		result.RoleIDs[slug] = role.ID
	}
	if err = catalog.ensure(ctx, tx, tenantID, roles, requestedRoles); err != nil {
		return ContentProjection{}, err
	}
	capabilities, err := catalog.capabilities(tenantID)
	if err != nil {
		return ContentProjection{}, err
	}
	capabilitiesBySlug := make(map[string]projectedCapability, len(capabilities))
	requestedCapabilities := make([]string, 0, len(capabilitySlugs))
	for _, capability := range capabilities {
		capabilitiesBySlug[capability.Slug] = capability
	}
	seenCapabilities := map[string]struct{}{}
	for _, slug := range capabilitySlugs {
		if _, duplicate := seenCapabilities[slug]; duplicate {
			continue
		}
		seenCapabilities[slug] = struct{}{}
		capability, exists := capabilitiesBySlug[slug]
		if !exists || capability.Status != "active" {
			return ContentProjection{}, ErrMissionNotFound
		}
		requestedCapabilities = append(requestedCapabilities, capability.ID)
		result.CapabilityIDs[slug] = capability.ID
	}
	requirements, err := catalog.requirements(tenantID, roles, capabilities)
	if err != nil {
		return ContentProjection{}, err
	}
	for _, requirementID := range requirementCapabilityIDs(requirements, requestedRoles) {
		if _, exists := seenCapabilities[requirementID]; !exists {
			requestedCapabilities = append(requestedCapabilities, requirementID)
		}
	}
	if err = catalog.ensureCapabilities(ctx, tx, tenantID, capabilities, requestedCapabilities); err != nil {
		return ContentProjection{}, err
	}
	if err = catalog.ensureRequirements(ctx, tx, tenantID, requirements, requestedRoles); err != nil {
		return ContentProjection{}, err
	}
	rubrics, err := catalog.rubrics(tenantID)
	if err != nil {
		return ContentProjection{}, err
	}
	if err = catalog.ensureRubrics(ctx, tx, tenantID, rubrics); err != nil {
		return ContentProjection{}, err
	}
	return result, nil
}

func (catalog ContentCatalog) capabilities(tenantID string) ([]projectedCapability, error) {
	result := make([]projectedCapability, 0, len(catalog.Release.Ontology.Capabilities))
	for _, source := range catalog.Release.Ontology.Capabilities {
		id, err := ids.DeterministicUUID(catalog.projectionKey(), contentCapabilityIDDomain, tenantID+"\x00"+catalog.Release.Identity()+"\x00"+source.ID)
		if err != nil {
			return nil, err
		}
		spec, err := json.Marshal(source)
		if err != nil {
			return nil, err
		}
		guidance, err := json.Marshal(map[string]any{"evidence_levels": source.EvidenceLevels, "usage": source.Usage, "source_ids": source.SourceIDs})
		if err != nil {
			return nil, err
		}
		result = append(result, projectedCapability{ID: id, Slug: source.ID, Revision: source.Revision, Status: source.Status, Spec: spec, EvidenceGuidance: guidance})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Slug < result[right].Slug })
	return result, nil
}

func (catalog ContentCatalog) ensureCapabilities(ctx context.Context, tx pgx.Tx, tenantID string, capabilities []projectedCapability, requested []string) error {
	byID := make(map[string]projectedCapability, len(capabilities))
	for _, capability := range capabilities {
		byID[capability.ID] = capability
	}
	seen := make(map[string]struct{}, len(requested))
	for _, capabilityID := range requested {
		if _, duplicate := seen[capabilityID]; duplicate {
			continue
		}
		seen[capabilityID] = struct{}{}
		capability, exists := byID[capabilityID]
		if !exists || capability.Status != "active" {
			return ErrMissionNotFound
		}
		if _, err := tx.Exec(ctx, `INSERT INTO product.capabilities(id,tenant_id,slug,revision,status,spec,evidence_guidance) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb) ON CONFLICT DO NOTHING`, capability.ID, tenantID, capability.Slug, capability.Revision, capability.Status, capability.Spec, capability.EvidenceGuidance); err != nil {
			return err
		}
		var slug, status string
		var revision int
		var spec, guidance []byte
		if err := tx.QueryRow(ctx, `SELECT slug,revision,status,spec::text,evidence_guidance::text FROM product.capabilities WHERE tenant_id=$1 AND id=$2`, tenantID, capability.ID).Scan(&slug, &revision, &status, &spec, &guidance); err != nil {
			return err
		}
		if slug != capability.Slug || revision != capability.Revision || status != capability.Status || !equalJSON(spec, capability.Spec) || !equalJSON(guidance, capability.EvidenceGuidance) {
			return errors.New("projected capability does not match immutable content release")
		}
	}
	return nil
}

func (catalog ContentCatalog) requirements(tenantID string, roles []projectedRole, capabilities []projectedCapability) ([]projectedRequirement, error) {
	roleIDs := make(map[string]string, len(roles))
	for _, role := range roles {
		roleIDs[role.Slug] = role.ID
	}
	capabilityIDs := make(map[string]string, len(capabilities))
	for _, capability := range capabilities {
		capabilityIDs[capability.Slug] = capability.ID
	}
	result := make([]projectedRequirement, 0)
	for _, role := range catalog.Release.Ontology.Roles {
		roleID, roleExists := roleIDs[role.ID]
		if !roleExists {
			return nil, errors.New("content role projection is incomplete")
		}
		for _, requirement := range role.RequirementIDs {
			capabilityID, capabilityExists := capabilityIDs[requirement.CapabilityID]
			if !capabilityExists {
				return nil, errors.New("content capability projection is incomplete")
			}
			id, err := ids.DeterministicUUID(catalog.projectionKey(), contentRequirementIDDomain, tenantID+"\x00"+catalog.Release.Identity()+"\x00"+role.ID+"\x00"+requirement.CapabilityID)
			if err != nil {
				return nil, err
			}
			rationale := "immutable_content_release=" + catalog.Release.Identity() + ";role=" + role.ID + ";capability=" + requirement.CapabilityID
			result = append(result, projectedRequirement{ID: id, RoleProfileID: roleID, CapabilityID: capabilityID, RequirementLevel: requirement.MinimumEvidenceLevel, Rationale: rationale, Revision: role.Revision})
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].RoleProfileID == result[right].RoleProfileID {
			return result[left].CapabilityID < result[right].CapabilityID
		}
		return result[left].RoleProfileID < result[right].RoleProfileID
	})
	return result, nil
}

func (catalog ContentCatalog) ensureRequirements(ctx context.Context, tx pgx.Tx, tenantID string, requirements []projectedRequirement, requestedRoleIDs []string) error {
	requested := make(map[string]struct{}, len(requestedRoleIDs))
	for _, roleID := range requestedRoleIDs {
		requested[roleID] = struct{}{}
	}
	for _, requirement := range requirements {
		if _, ok := requested[requirement.RoleProfileID]; !ok {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO product.role_capability_requirements(id,tenant_id,role_profile_id,capability_id,requirement_level,rationale,revision) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, requirement.ID, tenantID, requirement.RoleProfileID, requirement.CapabilityID, requirement.RequirementLevel, requirement.Rationale, requirement.Revision); err != nil {
			return err
		}
		var roleProfileID, capabilityID, level, rationale string
		var revision int
		if err := tx.QueryRow(ctx, `SELECT role_profile_id::text,capability_id::text,requirement_level,rationale,revision FROM product.role_capability_requirements WHERE tenant_id=$1 AND id=$2`, tenantID, requirement.ID).Scan(&roleProfileID, &capabilityID, &level, &rationale, &revision); err != nil {
			return err
		}
		if roleProfileID != requirement.RoleProfileID || capabilityID != requirement.CapabilityID || level != requirement.RequirementLevel || rationale != requirement.Rationale || revision != requirement.Revision {
			return errors.New("projected role requirement does not match immutable content release")
		}
	}
	return nil
}

func requirementCapabilityIDs(requirements []projectedRequirement, roleIDs []string) []string {
	requested := make(map[string]struct{}, len(roleIDs))
	for _, roleID := range roleIDs {
		requested[roleID] = struct{}{}
	}
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, requirement := range requirements {
		if _, ok := requested[requirement.RoleProfileID]; !ok {
			continue
		}
		if _, duplicate := seen[requirement.CapabilityID]; duplicate {
			continue
		}
		seen[requirement.CapabilityID] = struct{}{}
		result = append(result, requirement.CapabilityID)
	}
	sort.Strings(result)
	return result
}

func (catalog ContentCatalog) rubrics(tenantID string) ([]projectedRubric, error) {
	result := make([]projectedRubric, 0, len(catalog.Release.Rubrics.Rubrics))
	for _, source := range catalog.Release.Rubrics.Rubrics {
		id, err := ids.DeterministicUUID(catalog.projectionKey(), contentRubricIDDomain, tenantID+"\x00"+catalog.Release.Identity()+"\x00"+source.ID)
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
		id, idErr := ids.DeterministicUUID(catalog.projectionKey(), contentRoleIDDomain, tenantID+"\x00"+catalog.Release.Identity()+"\x00"+source.ID)
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

func (catalog ContentCatalog) projectionKey() []byte {
	digest := sha256.Sum256([]byte("lites-content-projection.v1\x00" + catalog.Release.Identity()))
	return digest[:]
}
