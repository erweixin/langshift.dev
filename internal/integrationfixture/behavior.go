//go:build integration

// Package integrationfixture creates production-shaped records used across
// PostgreSQL integration suites. It is excluded from all production builds.
package integrationfixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/behavior"
)

func SeedBehaviorChannel(ctx context.Context, admin *pgxpool.Pool, tenantID, userID string, profile behavior.Profile, environment string, at time.Time) (behavior.ChannelBinding, error) {
	if admin == nil || tenantID == "" || userID == "" || !profile.Valid() || environment != "staging" && environment != "production" {
		return behavior.ChannelBinding{}, fmt.Errorf("invalid behavior fixture")
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		return behavior.ChannelBinding{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		return behavior.ChannelBinding{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1||':'||$2||':'||$3,0))`, tenantID, profile, environment); err != nil {
		return behavior.ChannelBinding{}, err
	}
	var existing behavior.ChannelBinding
	existing.Profile, existing.Environment = profile, environment
	err = tx.QueryRow(ctx, `SELECT channel_id::text,sequence,snapshot_id,activated_at FROM agent.behavior_channel_deployments WHERE tenant_id=$1 AND profile_name=$2 AND environment=$3 ORDER BY sequence DESC LIMIT 1`, tenantID, profile, environment).Scan(&existing.ChannelID, &existing.Sequence, &existing.SnapshotID, &existing.ActivatedAt)
	if err == nil {
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return behavior.ChannelBinding{}, err
	}

	at = at.UTC().Truncate(time.Microsecond)
	baselineHash := fixtureHash(tenantID, string(profile), environment, "baseline")
	candidateHash := fixtureHash(tenantID, string(profile), environment, "candidate")
	baselineSnapshot := "behavior-" + baselineHash
	candidateSnapshot := "behavior-" + candidateHash
	channelID := fixtureUUID(tenantID, string(profile), environment, "channel")
	evaluationID := fixtureUUID(tenantID, string(profile), environment, "evaluation")
	baselineID := fixtureUUID(tenantID, string(profile), environment, "baseline-row")
	candidateID := fixtureUUID(tenantID, string(profile), environment, "candidate-row")
	deploymentID := fixtureUUID(tenantID, string(profile), environment, "deployment")
	epoch := fixtureUUID(tenantID, "fixture-epoch")
	correlation := fixtureUUID(tenantID, string(profile), environment, "correlation")
	eventIDs := []string{
		fixtureUUID(tenantID, string(profile), environment, "baseline-event"),
		fixtureUUID(tenantID, string(profile), environment, "candidate-event"),
		fixtureUUID(tenantID, string(profile), environment, "evaluation-event"),
		fixtureUUID(tenantID, string(profile), environment, "deployment-event"),
	}
	// Preserve the EventStore cursor-before-event lock order even in fixtures so
	// concurrent integration work cannot introduce a fixture-only deadlock.
	var firstSequence int64
	if err = tx.QueryRow(ctx, `INSERT INTO agent.event_cursors(tenant_id,user_id,last_seq)
VALUES($1,$2,(SELECT COALESCE(max(seq),0)+$3 FROM agent.events WHERE tenant_id=$1 AND user_id=$2))
ON CONFLICT (tenant_id,user_id) DO UPDATE SET last_seq=agent.event_cursors.last_seq+$3
RETURNING last_seq-$3+1`, tenantID, userID, len(eventIDs)).Scan(&firstSequence); err != nil {
		return behavior.ChannelBinding{}, err
	}
	for index, eventID := range eventIDs {
		aggregateID := []string{baselineID, candidateID, evaluationID, channelID}[index]
		eventType := []string{"BehaviorSnapshotCreated", "BehaviorSnapshotCreated", "BehaviorEvaluationRecorded", "BehaviorSnapshotPromoted"}[index]
		if _, err = tx.Exec(ctx, `INSERT INTO agent.events(id,tenant_id,user_id,seq,event_type,event_schema_version,aggregate_kind,aggregate_id,aggregate_version,store_epoch,occurred_at,committed_at,actor,correlation_id,payload_ref,payload_hash) VALUES($1,$2,$3,$4,$5,1,'integration_fixture',$6,1,$7,$8,$8,'{"kind":"system","component":"integration-fixture"}',$9,$10,$11)`, eventID, tenantID, userID, firstSequence+int64(index), eventType, aggregateID, epoch, at, correlation, "encrypted://integration/behavior/"+eventID, fixtureHash(eventID)); err != nil {
			return behavior.ChannelBinding{}, err
		}
	}
	manifest := func(hash string) string {
		return fmt.Sprintf(`{"schema_version":1,"profile":%q,"source_commit":"integration-fixture","fixture_hash":%q}`, profile, hash)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent.behavior_snapshots(id,tenant_id,snapshot_id,profile_name,manifest,manifest_hash,source_commit,created_by,created_event_id,created_at) VALUES($1,$2,$3,$4,$5,$6,'integration-fixture',$7,$8,$9),($10,$2,$11,$4,$12,$13,'integration-fixture',$7,$14,$9)`, baselineID, tenantID, baselineSnapshot, profile, manifest(baselineHash), baselineHash, userID, eventIDs[0], at, candidateID, candidateSnapshot, manifest(candidateHash), candidateHash, eventIDs[1]); err != nil {
		return behavior.ChannelBinding{}, err
	}
	reportHash := fixtureHash(tenantID, string(profile), environment, "report")
	reportID := "integration-" + string(profile) + "-" + environment
	report := fmt.Sprintf(`{"schema_version":1,"report_id":%q,"profile":%q,"candidate_snapshot_id":%q,"baseline_snapshot_id":%q}`, reportID, profile, candidateSnapshot, baselineSnapshot)
	if _, err = tx.Exec(ctx, `INSERT INTO agent.behavior_evaluation_reports(id,tenant_id,report_id,profile_name,candidate_snapshot_id,baseline_snapshot_id,report,report_hash,passed,recorded_by,recorded_event_id,evaluated_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,true,$9,$10,$11,$11)`, evaluationID, tenantID, reportID, profile, candidateSnapshot, baselineSnapshot, report, reportHash, userID, eventIDs[2], at); err != nil {
		return behavior.ChannelBinding{}, err
	}
	promotionHash := fixtureHash(tenantID, string(profile), environment, "promotion")
	approvals := `[{"role":"risk_owner","approver_id":"integration-risk","signature":"risk-signature"},{"role":"release_owner","approver_id":"integration-release","signature":"release-signature"}]`
	if _, err = tx.Exec(ctx, `INSERT INTO agent.behavior_channel_deployments(id,tenant_id,channel_id,profile_name,environment,sequence,action,snapshot_id,previous_snapshot_id,evaluation_report_id,evaluation_report_hash,promotion_hash,rollout_policy,auto_rollback_policy,approvals,activation_event_id,activated_by,activated_at) VALUES($1,$2,$3,$4,$5,1,'promote',$6,$7,$8,$9,$10,'{"percentages":[100],"observation_seconds":60}','{"zero_tolerance_enabled":true}',$11,$12,$13,$14)`, deploymentID, tenantID, channelID, profile, environment, candidateSnapshot, baselineSnapshot, evaluationID, reportHash, promotionHash, approvals, eventIDs[3], userID, at); err != nil {
		return behavior.ChannelBinding{}, err
	}
	result := behavior.ChannelBinding{ChannelID: channelID, Sequence: 1, SnapshotID: candidateSnapshot, Profile: profile, Environment: environment, ActivatedAt: at}
	return result, tx.Commit(ctx)
}

func fixtureHash(parts ...string) string {
	digest := sha256.Sum256([]byte(fmt.Sprint(parts)))
	return hex.EncodeToString(digest[:])
}

func fixtureUUID(parts ...string) string {
	digest := sha256.Sum256([]byte(fmt.Sprint(parts)))
	value := digest[:16]
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
