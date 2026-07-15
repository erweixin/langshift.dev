package toolworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/toolregistry"
)

var (
	ErrPolicyConfiguration = errors.New("tool execution overlay store configuration is invalid")
	ErrPolicyBinding       = errors.New("tool execution overlay descriptor binding is invalid")
	ErrPolicySnapshot      = errors.New("tool execution overlay snapshot is invalid")
)

type PostgresPolicyEvaluator struct {
	Pool     *pgxpool.Pool
	Registry *toolregistry.Registry
	Now      func() time.Time
}

type overlayRecord struct {
	Scope         string     `json:"scope"`
	Revision      uint64     `json:"revision"`
	ID            string     `json:"id,omitempty"`
	PolicyVersion uint64     `json:"policy_version,omitempty"`
	Decision      string     `json:"decision,omitempty"`
	ReasonCode    string     `json:"reason_code,omitempty"`
	EffectiveAt   *time.Time `json:"effective_at,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

type evaluatedOverlay struct {
	SchemaVersion        int           `json:"schema_version"`
	TenantID             string        `json:"tenant_id"`
	ToolName             string        `json:"tool_name"`
	DescriptorSnapshotID string        `json:"descriptor_snapshot_id"`
	DescriptorHash       string        `json:"descriptor_hash"`
	Platform             overlayRecord `json:"platform"`
	Tenant               overlayRecord `json:"tenant"`
}

func (evaluator PostgresPolicyEvaluator) Evaluate(ctx context.Context, execution ProposedExecution) (PolicyDecision, error) {
	if evaluator.Pool == nil || evaluator.Registry == nil || evaluator.Registry.Hash() == "" || execution.Command.TenantID == "" {
		return PolicyDecision{}, ErrPolicyConfiguration
	}
	snapshot, err := evaluator.Registry.Resolve(execution.Payload.DescriptorSnapshotID, execution.Payload.DescriptorHash)
	if err != nil || snapshot.Descriptor.Name != execution.Payload.ToolName || snapshot.Descriptor.EffectClass != execution.Payload.EffectClass {
		return PolicyDecision{}, errors.Join(ErrPolicyBinding, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if evaluator.Now != nil {
		now = evaluator.Now().UTC().Truncate(time.Microsecond)
	}
	tx, err := evaluator.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PolicyDecision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, execution.Command.TenantID); err != nil {
		return PolicyDecision{}, err
	}
	platform, err := loadOverlay(ctx, tx, "platform", execution.Command.TenantID, snapshot, now)
	if err != nil {
		return PolicyDecision{}, err
	}
	tenant, err := loadOverlay(ctx, tx, "tenant", execution.Command.TenantID, snapshot, now)
	if err != nil {
		return PolicyDecision{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PolicyDecision{}, err
	}
	return decideOverlay(execution.Command.TenantID, snapshot, platform, tenant)
}

func loadOverlay(ctx context.Context, tx pgx.Tx, scope, tenantID string, snapshot toolregistry.Snapshot, now time.Time) (overlayRecord, error) {
	query := `WITH revision AS (
		SELECT COALESCE(max(policy_version),0)::bigint AS value
		FROM agent.platform_tool_execution_overlays
		WHERE tool_name=$1 AND descriptor_snapshot_id=$2 AND descriptor_hash=$3 AND effective_at<=$4
	), active AS (
		SELECT id::text,policy_version,decision,reason_code,effective_at,expires_at
		FROM agent.platform_tool_execution_overlays
		WHERE tool_name=$1 AND descriptor_snapshot_id=$2 AND descriptor_hash=$3
		  AND effective_at<=$4 AND (expires_at IS NULL OR expires_at>$4)
		ORDER BY policy_version DESC,effective_at DESC,id DESC LIMIT 1
	)
	SELECT revision.value,COALESCE(active.id,''),COALESCE(active.policy_version,0),
	       COALESCE(active.decision,''),COALESCE(active.reason_code,''),active.effective_at,active.expires_at
	FROM revision LEFT JOIN active ON true`
	arguments := []any{snapshot.Descriptor.Name, snapshot.SnapshotID, snapshot.Hash, now}
	if scope == "tenant" {
		query = `WITH revision AS (
			SELECT COALESCE(max(policy_version),0)::bigint AS value
			FROM agent.tenant_tool_execution_overlays
			WHERE tenant_id=$1 AND tool_name=$2 AND descriptor_snapshot_id=$3 AND descriptor_hash=$4 AND effective_at<=$5
		), active AS (
			SELECT id::text,policy_version,decision,reason_code,effective_at,expires_at
			FROM agent.tenant_tool_execution_overlays
			WHERE tenant_id=$1 AND tool_name=$2 AND descriptor_snapshot_id=$3 AND descriptor_hash=$4
			  AND effective_at<=$5 AND (expires_at IS NULL OR expires_at>$5)
			ORDER BY policy_version DESC,effective_at DESC,id DESC LIMIT 1
		)
		SELECT revision.value,COALESCE(active.id,''),COALESCE(active.policy_version,0),
		       COALESCE(active.decision,''),COALESCE(active.reason_code,''),active.effective_at,active.expires_at
		FROM revision LEFT JOIN active ON true`
		arguments = []any{tenantID, snapshot.Descriptor.Name, snapshot.SnapshotID, snapshot.Hash, now}
	}
	var record overlayRecord
	var revision, version int64
	var effective, expires pgtype.Timestamptz
	if err := tx.QueryRow(ctx, query, arguments...).Scan(&revision, &record.ID, &version, &record.Decision, &record.ReasonCode, &effective, &expires); err != nil {
		return overlayRecord{}, err
	}
	if revision < 0 || version < 0 {
		return overlayRecord{}, ErrPolicySnapshot
	}
	record.Scope, record.Revision, record.PolicyVersion = scope, uint64(revision), uint64(version)
	if effective.Valid {
		value := effective.Time.UTC().Truncate(time.Microsecond)
		record.EffectiveAt = &value
	}
	if expires.Valid {
		value := expires.Time.UTC().Truncate(time.Microsecond)
		record.ExpiresAt = &value
	}
	if !validOverlayRecord(record) {
		return overlayRecord{}, ErrPolicySnapshot
	}
	return record, nil
}

func decideOverlay(tenantID string, snapshot toolregistry.Snapshot, platform, tenant overlayRecord) (PolicyDecision, error) {
	if tenantID == "" || snapshot.SnapshotID == "" || snapshot.Hash == "" || platform.Scope != "platform" || tenant.Scope != "tenant" || !validOverlayRecord(platform) || !validOverlayRecord(tenant) {
		return PolicyDecision{}, ErrPolicySnapshot
	}
	value := evaluatedOverlay{SchemaVersion: 1, TenantID: tenantID, ToolName: snapshot.Descriptor.Name, DescriptorSnapshotID: snapshot.SnapshotID, DescriptorHash: snapshot.Hash, Platform: platform, Tenant: tenant}
	encoded, err := json.Marshal(value)
	if err != nil {
		return PolicyDecision{}, err
	}
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	version := max(platform.Revision, tenant.Revision, uint64(1))
	decision := PolicyDecision{Allowed: true, SnapshotID: "tool-overlay-" + hash, SnapshotHash: hash, OverlayVersion: version}
	if platform.Decision == "deny" {
		decision.Allowed, decision.ReasonCode = false, "platform:"+platform.ReasonCode
	} else if tenant.Decision == "deny" {
		decision.Allowed, decision.ReasonCode = false, "tenant:"+tenant.ReasonCode
	}
	return decision, nil
}

func validOverlayRecord(record overlayRecord) bool {
	if record.Scope != "platform" && record.Scope != "tenant" {
		return false
	}
	if record.ID == "" {
		return record.PolicyVersion == 0 && record.Decision == "" && record.ReasonCode == "" && record.EffectiveAt == nil && record.ExpiresAt == nil
	}
	return record.PolicyVersion > 0 && record.PolicyVersion <= record.Revision && (record.Decision == "allow" || record.Decision == "deny") && resultCodePattern.MatchString(record.ReasonCode) && record.EffectiveAt != nil && (record.ExpiresAt == nil || record.ExpiresAt.After(*record.EffectiveAt))
}
