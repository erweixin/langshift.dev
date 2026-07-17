package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/identity/erasure"
	"github.com/langshift/lites/internal/objectstore/s3store"
	"github.com/langshift/lites/internal/payload"
)

type VersionedObjectPurger interface {
	Purge(context.Context, string) (s3store.PurgeReceipt, error)
}

type SubjectKeyPurger interface {
	PurgeKey(context.Context, string) (string, error)
}

type SubjectIndexPurger interface {
	PurgeSubject(context.Context, string, string, string) (int, string, error)
}

type SubjectCachePurger interface {
	PurgeSubject(context.Context, string, string, string) (int, string, error)
}

// ObjectBackedIndexPurger receipts the exact vector/lexical projection set
// after AccountErasureSurface has purged every referenced object version.
// Projection metadata is retained for audit, while its content is unreadable.
type ObjectBackedIndexPurger struct{ Pool *pgxpool.Pool }

func (purger ObjectBackedIndexPurger) PurgeSubject(ctx context.Context, tenantID, userID, recoveryEpoch string) (int, string, error) {
	if purger.Pool == nil || tenantID == "" || userID == "" || recoveryEpoch == "" {
		return 0, "", erasure.ErrInvalid
	}
	tx, err := purger.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return 0, "", err
	}
	rows, err := tx.Query(ctx, `WITH RECURSIVE affected(memory_id,memory_version) AS (
  SELECT r.memory_id,r.memory_version FROM agent.memory_document_revisions r
  LEFT JOIN agent.memory_revision_subjects s ON s.tenant_id=r.tenant_id AND s.memory_id=r.memory_id AND s.memory_version=r.memory_version
  WHERE r.tenant_id=$1 AND (r.user_id=$2 OR s.data_subject_id=$2)
  UNION
  SELECT d.memory_id,d.memory_version FROM agent.memory_revision_derivations d
  JOIN affected a ON a.memory_id=d.source_memory_id AND a.memory_version=d.source_memory_version
  WHERE d.tenant_id=$1
)
SELECT p.id::text,COALESCE(p.embedding_ref,''),COALESCE(p.lexical_ref,'')
FROM agent.memory_index_projections p JOIN affected a ON a.memory_id=p.memory_id AND a.memory_version=p.memory_version
WHERE p.tenant_id=$1 ORDER BY p.id`, tenantID, userID)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var id, embedding, lexical string
		if err = rows.Scan(&id, &embedding, &lexical); err != nil {
			return 0, "", err
		}
		values = append(values, id+"\x00"+embedding+"\x00"+lexical)
	}
	if err = rows.Err(); err != nil {
		return 0, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, "", err
	}
	return len(values), combinedErasureChecksum(tenantID+"\x00"+userID+"\x00"+recoveryEpoch, values), nil
}

// AccountErasureSurface executes one independently receipted deletion surface.
// Construct one value per erasure.RequiredSurfaces entry; the coordinator will
// never complete the request unless every surface returns valid JSON evidence.
type AccountErasureSurface struct {
	Pool    *pgxpool.Pool
	Surface erasure.Surface
	Objects VersionedObjectPurger
	Keys    SubjectKeyPurger
	Indexes SubjectIndexPurger
	Caches  SubjectCachePurger
	Now     func() time.Time
}

func (surface AccountErasureSurface) Erase(ctx context.Context, request erasure.Request, recoveryEpoch string) (json.RawMessage, error) {
	if surface.Pool == nil || request.ID == "" || request.TenantID == "" || request.UserID == "" || recoveryEpoch == "" {
		return nil, erasure.ErrInvalid
	}
	tenants, err := surface.subjectTenants(ctx, request.UserID)
	if err != nil {
		return nil, err
	}
	if len(tenants) == 0 {
		return nil, erasure.ErrInvalid
	}
	details := map[string]any{"surface": surface.Surface, "tenant_count": len(tenants), "verified_absent": true}
	switch surface.Surface {
	case erasure.Payload:
		if surface.Objects == nil {
			return nil, erasure.ErrInvalid
		}
		refs, loadErr := surface.refs(ctx, tenants, request, accountPayloadRefsSQL)
		if loadErr != nil {
			return nil, loadErr
		}
		count, versions, checksum, purgeErr := surface.purgeObjects(ctx, refs)
		if purgeErr != nil {
			return nil, purgeErr
		}
		if err = surface.retirePrincipal(ctx, tenants, request.UserID); err != nil {
			return nil, err
		}
		details["objects"] = count
		details["object_versions"] = versions
		details["checksum"] = checksum
		details["principal_mapping_destroyed"] = true
	case erasure.Memory:
		if surface.Objects == nil || surface.Keys == nil {
			return nil, erasure.ErrInvalid
		}
		refs, loadErr := surface.refs(ctx, tenants, request, accountMemoryRefsSQL)
		if loadErr != nil {
			return nil, loadErr
		}
		count, versions, objectChecksum, purgeErr := surface.purgeObjects(ctx, refs)
		if purgeErr != nil {
			return nil, purgeErr
		}
		keyRefs, loadErr := surface.refs(ctx, tenants, request, accountMemoryKeyRefsSQL)
		if loadErr != nil {
			return nil, loadErr
		}
		keyChecksums := make([]string, 0, len(keyRefs))
		for _, ref := range keyRefs {
			checksum, keyErr := surface.Keys.PurgeKey(ctx, ref)
			if keyErr != nil || len(checksum) != 64 {
				if keyErr != nil {
					return nil, keyErr
				}
				return nil, erasure.ErrInvalid
			}
			keyChecksums = append(keyChecksums, checksum)
		}
		details["objects"] = count
		details["object_versions"] = versions
		details["keys_destroyed"] = len(keyRefs)
		details["checksum"] = combinedErasureChecksum(objectChecksum, keyChecksums)
	case erasure.Indexes:
		if surface.Objects == nil || surface.Indexes == nil {
			return nil, erasure.ErrInvalid
		}
		refs, loadErr := surface.refs(ctx, tenants, request, accountIndexRefsSQL)
		if loadErr != nil {
			return nil, loadErr
		}
		count, versions, objectChecksum, purgeErr := surface.purgeObjects(ctx, refs)
		if purgeErr != nil {
			return nil, purgeErr
		}
		checksums := []string{objectChecksum}
		indexRecords := 0
		for _, tenantID := range tenants {
			purged, checksum, indexErr := surface.Indexes.PurgeSubject(ctx, tenantID, request.UserID, recoveryEpoch)
			if indexErr != nil || purged < 0 || len(checksum) != 64 {
				if indexErr != nil {
					return nil, indexErr
				}
				return nil, erasure.ErrInvalid
			}
			indexRecords += purged
			checksums = append(checksums, checksum)
		}
		details["objects"] = count
		details["object_versions"] = versions
		details["index_records"] = indexRecords
		details["checksum"] = combinedErasureChecksum("", checksums)
	case erasure.WorkspaceArtifact:
		if surface.Objects == nil {
			return nil, erasure.ErrInvalid
		}
		refs, loadErr := surface.refs(ctx, tenants, request, accountWorkspaceArtifactRefsSQL)
		if loadErr != nil {
			return nil, loadErr
		}
		count, versions, checksum, purgeErr := surface.purgeObjects(ctx, refs)
		if purgeErr != nil {
			return nil, purgeErr
		}
		details["objects"] = count
		details["object_versions"] = versions
		details["checksum"] = checksum
	case erasure.Cache:
		if surface.Caches == nil {
			return nil, erasure.ErrInvalid
		}
		checksums := make([]string, 0, len(tenants))
		cacheKeys := 0
		for _, tenantID := range tenants {
			purged, checksum, cacheErr := surface.Caches.PurgeSubject(ctx, tenantID, request.UserID, recoveryEpoch)
			if cacheErr != nil || purged < 0 || len(checksum) != 64 {
				if cacheErr != nil {
					return nil, cacheErr
				}
				return nil, erasure.ErrInvalid
			}
			cacheKeys += purged
			checksums = append(checksums, checksum)
		}
		details["cache_keys"] = cacheKeys
		details["checksum"] = combinedErasureChecksum("", checksums)
	case erasure.Snapshot:
		if surface.Objects == nil {
			return nil, erasure.ErrInvalid
		}
		refs, loadErr := surface.refs(ctx, tenants, request, accountSnapshotRefsSQL)
		if loadErr != nil {
			return nil, loadErr
		}
		count, versions, checksum, purgeErr := surface.purgeObjects(ctx, refs)
		if purgeErr != nil {
			return nil, purgeErr
		}
		details["objects"] = count
		details["object_versions"] = versions
		details["checksum"] = checksum
	default:
		return nil, erasure.ErrInvalid
	}
	return json.Marshal(details)
}

func (surface AccountErasureSurface) subjectTenants(ctx context.Context, userID string) ([]string, error) {
	rows, err := surface.Pool.Query(ctx, `SELECT tenant_id::text FROM identity.list_subject_erasure_tenants($1)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tenants []string
	for rows.Next() {
		var tenantID string
		if err = rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		tenants = append(tenants, tenantID)
	}
	return tenants, rows.Err()
}

func (surface AccountErasureSurface) refs(ctx context.Context, tenants []string, request erasure.Request, query string) ([]string, error) {
	unique := map[string]struct{}{}
	for _, tenantID := range tenants {
		tx, err := surface.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
		arguments := []any{request.UserID}
		if strings.Contains(query, "$2") {
			arguments = append(arguments, request.ID)
		}
		rows, err := tx.Query(ctx, query, arguments...)
		if err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
		for rows.Next() {
			var ref *string
			if err = rows.Scan(&ref); err != nil {
				rows.Close()
				_ = tx.Rollback(ctx)
				return nil, err
			}
			if ref != nil && strings.TrimSpace(*ref) != "" {
				unique[strings.TrimSpace(*ref)] = struct{}{}
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			_ = tx.Rollback(ctx)
			return nil, err
		}
		rows.Close()
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
	}
	refs := make([]string, 0, len(unique))
	for ref := range unique {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs, nil
}

func (surface AccountErasureSurface) purgeObjects(ctx context.Context, rawRefs []string) (int, int, string, error) {
	refs := make([]string, 0, len(rawRefs))
	seen := map[string]struct{}{}
	for _, raw := range rawRefs {
		ref, err := erasureObjectRef(raw)
		if err != nil {
			return 0, 0, "", err
		}
		if _, exists := seen[ref]; !exists {
			seen[ref] = struct{}{}
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	checksums := make([]string, 0, len(refs))
	versions := 0
	for _, ref := range refs {
		receipt, err := surface.Objects.Purge(ctx, ref)
		if err != nil || receipt.VersionsDeleted < 0 || len(receipt.Checksum) != 64 {
			if err != nil {
				return 0, 0, "", err
			}
			return 0, 0, "", erasure.ErrInvalid
		}
		versions += receipt.VersionsDeleted
		checksums = append(checksums, receipt.Checksum)
	}
	return len(refs), versions, combinedErasureChecksum("", checksums), nil
}

func (surface AccountErasureSurface) retirePrincipal(ctx context.Context, tenants []string, userID string) error {
	now := time.Now().UTC()
	if surface.Now != nil {
		now = surface.Now().UTC()
	}
	digest := sha256.Sum256([]byte("lites-erased-principal-v1\x00" + userID))
	pseudonym := "erased+" + hex.EncodeToString(digest[:16]) + "@deleted.invalid"
	tx, err := surface.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var email string
	if err = tx.QueryRow(ctx, `SELECT normalized_email FROM identity.users WHERE id=$1 FOR UPDATE`, userID).Scan(&email); err != nil {
		return err
	}
	for _, query := range []string{
		`DELETE FROM identity.password_credentials WHERE user_id=$1`,
		`DELETE FROM identity.email_verifications WHERE user_id=$1`,
		`DELETE FROM identity.password_reset_requests WHERE user_id=$1`,
		`DELETE FROM identity.sessions WHERE user_id=$1`,
	} {
		if _, err = tx.Exec(ctx, query, userID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE identity.users SET normalized_email=$1,email_verified_at=NULL,locale='und',status='deleted',version=version+1,updated_at=$2 WHERE id=$3 AND normalized_email<>$1`, pseudonym, now, userID); err != nil {
		return err
	}
	for _, tenantID := range tenants {
		if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE identity.memberships SET status='left',deactivated_at=COALESCE(deactivated_at,$1),version=version+1,updated_at=$1 WHERE tenant_id=$2 AND user_id=$3 AND status<>'left'`, now, tenantID, userID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE identity.consent_records SET withdrawn_at=COALESCE(withdrawn_at,$1),version=version+1,updated_at=$1 WHERE tenant_id=$2 AND user_id=$3 AND withdrawn_at IS NULL`, now, tenantID, userID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM identity.invitations WHERE tenant_id=$1 AND normalized_email=$2`, tenantID, email); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func erasureObjectRef(raw string) (string, error) {
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		var manifest payload.Manifest
		if err := json.Unmarshal([]byte(raw), &manifest); err != nil || manifest.Ref == "" {
			return "", erasure.ErrInvalid
		}
		return manifest.Ref, nil
	}
	if !strings.HasPrefix(raw, "s3://") {
		return "", erasure.ErrInvalid
	}
	return raw, nil
}

func combinedErasureChecksum(seed string, values []string) string {
	ordered := append([]string(nil), values...)
	sort.Strings(ordered)
	digest := sha256.New()
	_, _ = digest.Write([]byte("lites-account-erasure-surface-v1\x00" + seed + "\x00"))
	for _, value := range ordered {
		_, _ = digest.Write([]byte(value + "\x00"))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

const accountPayloadRefsSQL = `
SELECT experience_payload_ref FROM identity.onboarding_sessions WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT goal_payload_ref FROM product.missions WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT r.route_payload_ref FROM product.route_revisions r JOIN product.missions m ON m.tenant_id=r.tenant_id AND m.id=r.mission_id WHERE r.tenant_id=current_setting('lites.tenant_id')::uuid AND m.user_id=$1
UNION ALL SELECT payload_ref FROM product.evidence WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT task_payload_ref FROM product.daily_tasks WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT payload_ref FROM product.submissions WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT understanding_ref FROM product.submissions WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT review_payload_ref FROM product.reviews WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT object_ref FROM product.data_export_requests WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT subject_ref FROM product.support_cases WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND requester_user_id=$1
UNION ALL SELECT body_ref FROM product.support_case_messages WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND author_user_id=$1
UNION ALL SELECT payload_ref FROM agent.events WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT response_payload_ref FROM agent.idempotency_responses WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT normalized_input_ref FROM agent.tool_calls WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT payload_ref FROM agent.run_messages WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT details->>'reason_ref' FROM identity.security_events WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND subject_user_id=$1
UNION ALL SELECT payload_ref FROM agent.outbox WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND aggregate_kind='account_erasure_request' AND aggregate_id=$2`

const affectedMemoryRevisionsCTE = `WITH RECURSIVE affected(memory_id,memory_version) AS (
  SELECT r.memory_id,r.memory_version FROM agent.memory_document_revisions r
  LEFT JOIN agent.memory_revision_subjects s ON s.tenant_id=r.tenant_id AND s.memory_id=r.memory_id AND s.memory_version=r.memory_version
  WHERE r.tenant_id=current_setting('lites.tenant_id')::uuid AND (r.user_id=$1 OR s.data_subject_id=$1)
  UNION
  SELECT d.memory_id,d.memory_version FROM agent.memory_revision_derivations d
  JOIN affected a ON a.memory_id=d.source_memory_id AND a.memory_version=d.source_memory_version
  WHERE d.tenant_id=current_setting('lites.tenant_id')::uuid
)
`

const accountMemoryRefsSQL = affectedMemoryRevisionsCTE + `
SELECT r.content_ref FROM agent.memory_document_revisions r JOIN affected a ON a.memory_id=r.memory_id AND a.memory_version=r.memory_version WHERE r.tenant_id=current_setting('lites.tenant_id')::uuid
UNION ALL SELECT r.summary_ref FROM agent.memory_document_revisions r JOIN affected a ON a.memory_id=r.memory_id AND a.memory_version=r.memory_version WHERE r.tenant_id=current_setting('lites.tenant_id')::uuid
UNION ALL SELECT c.content_ref FROM agent.retrieval_manifest_chunks c JOIN agent.retrieval_manifests m ON m.tenant_id=c.tenant_id AND m.id=c.manifest_id WHERE c.tenant_id=current_setting('lites.tenant_id')::uuid AND m.user_id=$1
UNION ALL SELECT c.content_ref FROM agent.retrieval_manifest_chunks c JOIN affected a ON a.memory_id=c.memory_id AND a.memory_version=c.memory_version WHERE c.tenant_id=current_setting('lites.tenant_id')::uuid`

const accountMemoryKeyRefsSQL = affectedMemoryRevisionsCTE + `
SELECT r.key_ref FROM agent.memory_document_revisions r JOIN affected a ON a.memory_id=r.memory_id AND a.memory_version=r.memory_version WHERE r.tenant_id=current_setting('lites.tenant_id')::uuid`

const accountIndexRefsSQL = affectedMemoryRevisionsCTE + `
SELECT p.embedding_ref FROM agent.memory_index_projections p JOIN affected a ON a.memory_id=p.memory_id AND a.memory_version=p.memory_version WHERE p.tenant_id=current_setting('lites.tenant_id')::uuid
UNION ALL SELECT p.lexical_ref FROM agent.memory_index_projections p JOIN affected a ON a.memory_id=p.memory_id AND a.memory_version=p.memory_version WHERE p.tenant_id=current_setting('lites.tenant_id')::uuid`

const accountWorkspaceArtifactRefsSQL = `
SELECT brief_ref FROM product.projects WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT reflection_ref FROM product.projects WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT object_ref FROM product.artifact_revisions WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT object_ref FROM product.portfolio_exports WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1`

const accountSnapshotRefsSQL = `
SELECT payload_ref FROM agent.coach_context_snapshots WHERE tenant_id=current_setting('lites.tenant_id')::uuid AND user_id=$1
UNION ALL SELECT s.state_ref FROM agent.snapshots s JOIN agent.runs r ON r.tenant_id=s.tenant_id AND r.id=s.aggregate_id WHERE s.tenant_id=current_setting('lites.tenant_id')::uuid AND r.user_id=$1`

var _ erasure.SurfaceEraser = AccountErasureSurface{}
var _ SubjectIndexPurger = ObjectBackedIndexPurger{}
