// Package localtestadapter contains deterministic macOS engineering adapters.
// It must only be linked by cmd/lites-macos-test-adapter. Records created here
// are test evidence and never constitute a production behavior promotion.
package localtestadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/behavior"
	"github.com/langshift/lites/internal/localengineeringassets"
)

var ErrInvalid = errors.New("local engineering behavior adapter is invalid")

type TenantPrincipal struct {
	TenantID string
	UserID   string
}

// ListTenantPrincipals returns active tenants only after they have an active
// user that can own immutable test evidence. The macOS initializer gives the
// public anonymous tenant a dedicated engineering projection owner; personal
// and enterprise tenants continue to use their real owner or membership.
func ListTenantPrincipals(ctx context.Context, pool *pgxpool.Pool, after string, limit int) ([]TenantPrincipal, error) {
	if pool == nil || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := pool.Query(ctx, `
SELECT t.id::text,principal.user_id::text
FROM identity.tenants t
CROSS JOIN LATERAL (
  SELECT candidate.user_id
  FROM (
    SELECT t.owner_user_id AS user_id,0 AS priority
    UNION ALL
    SELECT m.user_id,1 FROM identity.memberships m
      WHERE m.tenant_id=t.id AND m.status='active'
    UNION ALL
    SELECT a.ephemeral_user_id,2 FROM identity.anonymous_subjects a
      WHERE a.system_tenant_id=t.id AND a.deleted_at IS NULL AND a.expires_at>CURRENT_TIMESTAMP
  ) candidate
  JOIN identity.users u ON u.id=candidate.user_id AND u.status='active'
  WHERE candidate.user_id IS NOT NULL
  ORDER BY candidate.priority,candidate.user_id
  LIMIT 1
) principal
WHERE t.status='active' AND ($1='' OR t.id>$1::uuid)
ORDER BY t.id
LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TenantPrincipal, 0, limit)
	for rows.Next() {
		var item TenantPrincipal
		if err = rows.Scan(&item.TenantID, &item.UserID); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func EnsureAllBehaviorChannels(ctx context.Context, pool *pgxpool.Pool, principal TenantPrincipal, environment, storeEpoch string, at time.Time) error {
	if pool == nil || principal.TenantID == "" || principal.UserID == "" || environment != "staging" && environment != "production" || storeEpoch == "" {
		return ErrInvalid
	}
	for _, profile := range []behavior.Profile{behavior.RoutePlanner, behavior.DailyPlanner, behavior.Coach, behavior.Evaluator, behavior.ArtifactBuilder} {
		if err := EnsureBehaviorChannel(ctx, pool, principal, profile, environment, storeEpoch, at); err != nil {
			return err
		}
	}
	return nil
}

func EnsureBehaviorChannel(ctx context.Context, pool *pgxpool.Pool, principal TenantPrincipal, profile behavior.Profile, environment, storeEpoch string, at time.Time) error {
	if pool == nil || principal.TenantID == "" || principal.UserID == "" || !profile.Valid() || environment != "staging" && environment != "production" || storeEpoch == "" {
		return ErrInvalid
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, principal.TenantID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1||':'||$2||':'||$3,0))`, principal.TenantID, profile, environment); err != nil {
		return err
	}
	creditBucketID := uuid(principal.TenantID, "engineering-test-credit-bucket")
	if _, err = tx.Exec(ctx, `INSERT INTO contracts.credit_buckets(id,tenant_id,bucket_kind,granted_units,starts_at,expires_at,created_at,updated_at) VALUES($1,$2,'llm',1000000000,$3,$4,$5,$5) ON CONFLICT (id) DO NOTHING`, creditBucketID, principal.TenantID, at.Add(-time.Hour), at.Add(48*time.Hour), at); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent.behavior_channel_deployments WHERE tenant_id=$1 AND profile_name=$2 AND environment=$3)`, principal.TenantID, profile, environment).Scan(&exists); err != nil || exists {
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	at = at.UTC().Truncate(time.Microsecond)
	baselineManifest, err := localengineeringassets.BehaviorManifest(profile, at.Add(-time.Microsecond), "baseline")
	if err != nil {
		return err
	}
	candidateManifest, err := localengineeringassets.BehaviorManifest(profile, at, "candidate")
	if err != nil {
		return err
	}
	baselineHash, err := baselineManifest.Hash()
	if err != nil {
		return err
	}
	candidateHash, err := candidateManifest.Hash()
	if err != nil {
		return err
	}
	baselineJSON, err := json.Marshal(baselineManifest)
	if err != nil {
		return err
	}
	candidateJSON, err := json.Marshal(candidateManifest)
	if err != nil {
		return err
	}
	baselineSnapshot := "behavior-" + baselineHash
	candidateSnapshot := "behavior-" + candidateHash
	channelID := uuid(principal.TenantID, string(profile), environment, "channel")
	evaluationID := uuid(principal.TenantID, string(profile), environment, "evaluation")
	baselineID := uuid(principal.TenantID, string(profile), environment, "baseline-row")
	candidateID := uuid(principal.TenantID, string(profile), environment, "candidate-row")
	deploymentID := uuid(principal.TenantID, string(profile), environment, "deployment")
	correlation := uuid(principal.TenantID, string(profile), environment, "correlation")
	eventIDs := []string{
		uuid(principal.TenantID, string(profile), environment, "baseline-event"),
		uuid(principal.TenantID, string(profile), environment, "candidate-event"),
		uuid(principal.TenantID, string(profile), environment, "evaluation-event"),
		uuid(principal.TenantID, string(profile), environment, "deployment-event"),
	}
	// Match the production EventStore lock order: reserve the per-user cursor
	// range before inserting event rows. Reversing these operations can deadlock
	// with login or Claim while this adapter discovers a newly created tenant.
	var firstSequence int64
	if err = tx.QueryRow(ctx, `INSERT INTO agent.event_cursors(tenant_id,user_id,last_seq)
VALUES($1,$2,(SELECT COALESCE(max(seq),0)+$3 FROM agent.events WHERE tenant_id=$1 AND user_id=$2))
ON CONFLICT (tenant_id,user_id) DO UPDATE SET last_seq=agent.event_cursors.last_seq+$3
RETURNING last_seq-$3+1`, principal.TenantID, principal.UserID, len(eventIDs)).Scan(&firstSequence); err != nil {
		return err
	}
	for index, eventID := range eventIDs {
		aggregateID := []string{baselineID, candidateID, evaluationID, channelID}[index]
		eventType := []string{"BehaviorSnapshotCreated", "BehaviorSnapshotCreated", "BehaviorEvaluationRecorded", "BehaviorSnapshotPromoted"}[index]
		if _, err = tx.Exec(ctx, `INSERT INTO agent.events(id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,correlation_id,payload_ref,payload_hash) VALUES($1,$2,$3,$4,$5,1,'engineering_test_adapter',$6,1,$7,$8,$8,'{"kind":"system","component":"lites-macos-test-adapter"}',$9,$10,$11)`, eventID, principal.TenantID, principal.UserID, firstSequence+int64(index), eventType, aggregateID, storeEpoch, at, correlation, "encrypted://engineering-test/behavior/"+eventID, hash(eventID)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.behavior_snapshots(id,tenant_id,snapshot_id,profile_name,manifest,manifest_hash,source_commit,created_by,created_event_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10),($11,$2,$12,$4,$13,$14,$7,$8,$15,$10)`, baselineID, principal.TenantID, baselineSnapshot, profile, baselineJSON, baselineHash, localengineeringassets.SourceCommit, principal.UserID, eventIDs[0], at, candidateID, candidateSnapshot, candidateJSON, candidateHash, eventIDs[1]); err != nil {
		return err
	}
	reportHash := hash(principal.TenantID, string(profile), environment, "report")
	reportID := "engineering-test-" + string(profile) + "-" + environment
	report := fmt.Sprintf(`{"schema_version":1,"report_id":%q,"profile":%q,"candidate_snapshot_id":%q,"baseline_snapshot_id":%q,"fixture_validated":true,"production_claim":false}`, reportID, profile, candidateSnapshot, baselineSnapshot)
	if _, err = tx.Exec(ctx, `INSERT INTO agent.behavior_evaluation_reports(id,tenant_id,report_id,profile_name,candidate_snapshot_id,baseline_snapshot_id,report,report_hash,passed,recorded_by,recorded_event_id,evaluated_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,true,$9,$10,$11,$11)`, evaluationID, principal.TenantID, reportID, profile, candidateSnapshot, baselineSnapshot, report, reportHash, principal.UserID, eventIDs[2], at); err != nil {
		return err
	}
	promotionHash := hash(principal.TenantID, string(profile), environment, "promotion")
	approvals := `[{"role":"risk_owner","approver_id":"engineering-test-risk","signature":"fixture-only"},{"role":"release_owner","approver_id":"engineering-test-release","signature":"fixture-only"}]`
	if _, err = tx.Exec(ctx, `INSERT INTO agent.behavior_channel_deployments(id,tenant_id,channel_id,profile_name,environment,sequence,action,snapshot_id,previous_snapshot_id,evaluation_report_id,evaluation_report_hash,promotion_hash,rollout_policy,auto_rollback_policy,approvals,activation_event_id,activated_by,activated_at) VALUES($1,$2,$3,$4,$5,1,'promote',$6,$7,$8,$9,$10,'{"percentages":[100],"observation_seconds":60,"engineering_test":true}','{"zero_tolerance_enabled":true}',$11,$12,$13,$14)`, deploymentID, principal.TenantID, channelID, profile, environment, candidateSnapshot, baselineSnapshot, evaluationID, reportHash, promotionHash, approvals, eventIDs[3], principal.UserID, at); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func hash(parts ...string) string {
	digest := sha256.Sum256([]byte(fmt.Sprint(parts)))
	return hex.EncodeToString(digest[:])
}

func uuid(parts ...string) string {
	digest := sha256.Sum256([]byte(fmt.Sprint(parts)))
	value := digest[:16]
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
