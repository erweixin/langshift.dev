//go:build integration

package postgres

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/behavior"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
)

type fixedEpoch string

func (epoch fixedEpoch) CurrentStoreEpoch(context.Context) (string, error) { return string(epoch), nil }

func TestBehaviorStorePersistsEvaluatesAndPromotesExactSnapshot(t *testing.T) {
	ctx := context.Background()
	admin := behaviorPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	service := behaviorPool(t, ctx, "LITES_TEST_BEHAVIOR_DATABASE_URL")
	defer service.Close()
	const (
		userID       = "10000000-0000-4000-8000-000000009801"
		tenantID     = "20000000-0000-4000-8000-000000009801"
		baselineID   = "30000000-0000-4000-8000-000000009801"
		candidateID  = "30000000-0000-4000-8000-000000009802"
		evaluationID = "30000000-0000-4000-8000-000000009803"
		deploymentID = "30000000-0000-4000-8000-000000009804"
		channelID    = "30000000-0000-4000-8000-000000009805"
		epoch        = "90000000-0000-4000-8000-000000009801"
	)
	now := time.Date(2026, 7, 15, 22, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,status) VALUES($1,'behavior@lites.invalid',$2,'en','active')`, userID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Behavior','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	publicKeys := map[string]ed25519.PublicKey{}
	privateKeys := map[string]ed25519.PrivateKey{}
	for _, keyID := range []string{"risk-key", "release-key", "rollback-key"} {
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		publicKeys[keyID], privateKeys[keyID] = publicKey, privateKey
	}
	store := Store{Pool: service, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, Epochs: fixedEpoch(epoch), StoreEpoch: epoch, PublicKeys: publicKeys, Now: func() time.Time { return now }}
	baseline := behaviorManifest(now.Add(-time.Hour), '1')
	candidate := behaviorManifest(now.Add(-30*time.Minute), '2')
	baselineSnapshot, _ := baseline.SnapshotID()
	candidateSnapshot, _ := candidate.SnapshotID()
	actor := json.RawMessage(`{"kind":"system","component":"behavior-control-plane"}`)
	baselineCommand := CreateSnapshotCommand{ID: baselineID, EventID: "40000000-0000-4000-8000-000000009801", OutboxID: "50000000-0000-4000-8000-000000009801", PublishCommandID: "60000000-0000-4000-8000-000000009801", TenantID: tenantID, UserID: userID, CorrelationID: "70000000-0000-4000-8000-000000009801", Manifest: baseline, Actor: actor, EventPayload: PayloadPointer{Ref: "encrypted://behavior/baseline", Hash: "baseline-event"}}
	if result, err := store.CreateSnapshot(ctx, baselineCommand); err != nil || result.Hash == "" || result.Replayed {
		t.Fatalf("CreateSnapshot(baseline)=%#v err=%v", result, err)
	}
	candidateCommand := CreateSnapshotCommand{ID: candidateID, EventID: "40000000-0000-4000-8000-000000009802", OutboxID: "50000000-0000-4000-8000-000000009802", PublishCommandID: "60000000-0000-4000-8000-000000009802", TenantID: tenantID, UserID: userID, CorrelationID: "70000000-0000-4000-8000-000000009802", Manifest: candidate, Actor: actor, EventPayload: PayloadPointer{Ref: "encrypted://behavior/candidate", Hash: "candidate-event"}}
	if _, err := store.CreateSnapshot(ctx, candidateCommand); err != nil {
		t.Fatal(err)
	}
	report := behaviorReport(now.Add(-10*time.Minute), baselineSnapshot, candidateSnapshot)
	evaluationCommand := RecordEvaluationCommand{ID: evaluationID, EventID: "40000000-0000-4000-8000-000000009803", OutboxID: "50000000-0000-4000-8000-000000009803", PublishCommandID: "60000000-0000-4000-8000-000000009803", TenantID: tenantID, UserID: userID, CorrelationID: "70000000-0000-4000-8000-000000009803", Report: report, Actor: actor, EventPayload: PayloadPointer{Ref: "encrypted://behavior/evaluation", Hash: "evaluation-event"}}
	evaluation, err := store.RecordEvaluation(ctx, evaluationCommand)
	if err != nil || evaluation.Hash == "" {
		t.Fatalf("RecordEvaluation()=%#v err=%v", evaluation, err)
	}
	manifestHash, _ := candidate.Hash()
	request := behavior.PromotionRequest{
		SchemaVersion: 1, TenantID: tenantID, Environment: "production", Profile: behavior.RoutePlanner, Sequence: 1,
		CandidateSnapshotID: candidateSnapshot, PreviousSnapshotID: baselineSnapshot, ManifestHash: manifestHash, EvaluationReportHash: evaluation.Hash,
		Rollout:      behavior.RolloutPolicy{Percentages: []int{1, 10, 50, 100}, ObservationSecond: 600},
		AutoRollback: behavior.AutoRollbackPolicy{MaximumErrorRate: 0.01, MaximumLatencyRatio: 1.2, MaximumCostRatio: 1.1, MinimumQualityRatio: 0.98, ZeroToleranceEnabled: true}, RequestedAt: now.Add(-time.Minute),
	}
	_, promotionHash, _ := request.SigningPayload()
	for _, approval := range []behavior.Approval{{Role: "risk_owner", ApproverID: "risk-owner", KeyID: "risk-key", SignedAt: now.Add(-30 * time.Second), ExpiresAt: now.Add(time.Hour)}, {Role: "release_owner", ApproverID: "release-owner", KeyID: "release-key", SignedAt: now.Add(-20 * time.Second), ExpiresAt: now.Add(time.Hour)}} {
		payload, _ := approval.SigningPayload(promotionHash)
		approval.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKeys[approval.KeyID], payload))
		request.Approvals = append(request.Approvals, approval)
	}
	promote := PromoteCommand{ID: deploymentID, ChannelID: channelID, EventID: "40000000-0000-4000-8000-000000009804", OutboxID: "50000000-0000-4000-8000-000000009804", PublishCommandID: "60000000-0000-4000-8000-000000009804", EvaluationID: evaluationID, TenantID: tenantID, UserID: userID, CorrelationID: "70000000-0000-4000-8000-000000009804", Manifest: candidate, Report: report, Request: request, Actor: actor, EventPayload: PayloadPointer{Ref: "encrypted://behavior/promotion", Hash: "promotion-event"}}
	first, err := store.Promote(ctx, promote)
	if err != nil || first.Sequence != 1 || first.Replayed {
		t.Fatalf("Promote()=%#v err=%v", first, err)
	}
	replay, err := store.Promote(ctx, promote)
	if err != nil || !replay.Replayed || replay.Hash != first.Hash {
		t.Fatalf("replayed Promote()=%#v err=%v", replay, err)
	}
	reordered := promote
	reordered.Request.Approvals = append([]behavior.Approval(nil), promote.Request.Approvals...)
	reordered.Request.Approvals[0], reordered.Request.Approvals[1] = reordered.Request.Approvals[1], reordered.Request.Approvals[0]
	if _, err = store.Promote(ctx, reordered); err == nil {
		t.Fatal("same deployment ID with different approval evidence was accepted as an exact replay")
	}
	stale := promote
	stale.ID = "30000000-0000-4000-8000-000000009899"
	stale.EventID = "40000000-0000-4000-8000-000000009899"
	stale.OutboxID = "50000000-0000-4000-8000-000000009899"
	stale.PublishCommandID = "60000000-0000-4000-8000-000000009899"
	if _, err = store.Promote(ctx, stale); err == nil {
		t.Fatal("stale channel sequence was accepted")
	}
	next := behaviorManifest(now.Add(-5*time.Minute), '3')
	nextSnapshot, _ := next.SnapshotID()
	nextSnapshotCommand := CreateSnapshotCommand{ID: "30000000-0000-4000-8000-000000009806", EventID: "40000000-0000-4000-8000-000000009806", OutboxID: "50000000-0000-4000-8000-000000009806", PublishCommandID: "60000000-0000-4000-8000-000000009806", TenantID: tenantID, UserID: userID, CorrelationID: "70000000-0000-4000-8000-000000009806", Manifest: next, Actor: actor, EventPayload: PayloadPointer{Ref: "encrypted://behavior/next", Hash: "next-event"}}
	if _, err = store.CreateSnapshot(ctx, nextSnapshotCommand); err != nil {
		t.Fatal(err)
	}
	nextReport := behaviorReport(now.Add(-4*time.Minute), candidateSnapshot, nextSnapshot)
	nextReport.ReportID = "route-planner-2026-07-15-next"
	nextEvaluationCommand := RecordEvaluationCommand{ID: "30000000-0000-4000-8000-000000009807", EventID: "40000000-0000-4000-8000-000000009807", OutboxID: "50000000-0000-4000-8000-000000009807", PublishCommandID: "60000000-0000-4000-8000-000000009807", TenantID: tenantID, UserID: userID, CorrelationID: "70000000-0000-4000-8000-000000009807", Report: nextReport, Actor: actor, EventPayload: PayloadPointer{Ref: "encrypted://behavior/evaluation-next", Hash: "evaluation-next-event"}}
	nextEvaluation, err := store.RecordEvaluation(ctx, nextEvaluationCommand)
	if err != nil {
		t.Fatal(err)
	}
	nextManifestHash, _ := next.Hash()
	nextRequest := request
	nextRequest.Sequence = 2
	nextRequest.CandidateSnapshotID = nextSnapshot
	nextRequest.PreviousSnapshotID = candidateSnapshot
	nextRequest.ManifestHash = nextManifestHash
	nextRequest.EvaluationReportHash = nextEvaluation.Hash
	nextRequest.Approvals = signedApprovals(t, nextRequest, now, privateKeys)
	nextPromotion := PromoteCommand{ID: "30000000-0000-4000-8000-000000009808", ChannelID: channelID, EventID: "40000000-0000-4000-8000-000000009808", OutboxID: "50000000-0000-4000-8000-000000009808", PublishCommandID: "60000000-0000-4000-8000-000000009808", EvaluationID: nextEvaluationCommand.ID, TenantID: tenantID, UserID: userID, CorrelationID: "70000000-0000-4000-8000-000000009808", Manifest: next, Report: nextReport, Request: nextRequest, Actor: actor, EventPayload: PayloadPointer{Ref: "encrypted://behavior/promotion-next", Hash: "promotion-next-event"}}
	if result, promoteErr := store.Promote(ctx, nextPromotion); promoteErr != nil || result.Sequence != 2 {
		t.Fatalf("second Promote()=%#v err=%v", result, promoteErr)
	}
	rollbackRequest := behavior.RollbackRequest{SchemaVersion: 1, TenantID: tenantID, Environment: "production", Profile: behavior.RoutePlanner, Sequence: 3, FromSnapshotID: nextSnapshot, ToSnapshotID: candidateSnapshot, Trigger: "zero_tolerance", ObservedValue: 1, Threshold: 0, IncidentEvidenceHash: strings.Repeat("9", 64), AutomationKeyID: "rollback-key", OccurredAt: now.Add(-time.Second)}
	rollbackPayload, _, _ := rollbackRequest.SigningPayload()
	rollbackRequest.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKeys["rollback-key"], rollbackPayload))
	rollback := RollbackCommand{ID: "30000000-0000-4000-8000-000000009809", ChannelID: channelID, EventID: "40000000-0000-4000-8000-000000009809", OutboxID: "50000000-0000-4000-8000-000000009809", PublishCommandID: "60000000-0000-4000-8000-000000009809", TenantID: tenantID, UserID: userID, CorrelationID: "70000000-0000-4000-8000-000000009809", Request: rollbackRequest, Actor: actor, EventPayload: PayloadPointer{Ref: "encrypted://behavior/rollback", Hash: "rollback-event"}}
	if result, rollbackErr := store.Rollback(ctx, rollback); rollbackErr != nil || result.Sequence != 3 {
		t.Fatalf("Rollback()=%#v err=%v", result, rollbackErr)
	}
	var current, persistedRollbackHash, persistedTrigger, persistedAutomationKey, persistedSignature string
	var persistedObserved, persistedThreshold float64
	tx, err := service.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, tenantID); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `SELECT snapshot_id FROM agent.behavior_channel_deployments WHERE tenant_id=$1 AND channel_id=$2 ORDER BY sequence DESC LIMIT 1`, tenantID, channelID).Scan(&current); err != nil || current != candidateSnapshot {
		t.Fatalf("current snapshot=%q err=%v", current, err)
	}
	if err = tx.QueryRow(ctx, `SELECT rollback_hash,rollback_trigger,rollback_observed_value,rollback_threshold,automation_key_id,automation_signature FROM agent.behavior_channel_deployments WHERE tenant_id=$1 AND id=$2`, tenantID, rollback.ID).Scan(&persistedRollbackHash, &persistedTrigger, &persistedObserved, &persistedThreshold, &persistedAutomationKey, &persistedSignature); err != nil {
		t.Fatal(err)
	}
	_, expectedRollbackHash, _ := rollbackRequest.SigningPayload()
	if persistedRollbackHash != expectedRollbackHash || persistedTrigger != rollbackRequest.Trigger || persistedObserved != rollbackRequest.ObservedValue || persistedThreshold != rollbackRequest.Threshold || persistedAutomationKey != rollbackRequest.AutomationKeyID || persistedSignature != rollbackRequest.Signature {
		t.Fatal("rollback cryptographic evidence was not persisted exactly")
	}
}

func signedApprovals(t *testing.T, request behavior.PromotionRequest, now time.Time, privateKeys map[string]ed25519.PrivateKey) []behavior.Approval {
	t.Helper()
	_, promotionHash, err := request.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	result := []behavior.Approval{}
	for _, approval := range []behavior.Approval{{Role: "risk_owner", ApproverID: "risk-owner", KeyID: "risk-key", SignedAt: now.Add(-30 * time.Second), ExpiresAt: now.Add(time.Hour)}, {Role: "release_owner", ApproverID: "release-owner", KeyID: "release-key", SignedAt: now.Add(-20 * time.Second), ExpiresAt: now.Add(time.Hour)}} {
		payload, payloadErr := approval.SigningPayload(promotionHash)
		if payloadErr != nil {
			t.Fatal(payloadErr)
		}
		approval.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKeys[approval.KeyID], payload))
		result = append(result, approval)
	}
	return result
}

func behaviorManifest(createdAt time.Time, promptFill byte) behavior.Manifest {
	binding := func(id, version string, fill byte) behavior.Binding {
		return behavior.Binding{ID: id, Version: version, Hash: strings.Repeat(string(fill), 64)}
	}
	return behavior.Manifest{SchemaVersion: 1, Profile: behavior.RoutePlanner, Model: binding("model", "v1", 'a'), Prompt: binding("prompt", "v1", promptFill), ProfileDefinition: binding("route_planner", "v1", 'c'), GuardrailPolicy: binding("guardrail", "v1", 'd'), RouterPolicy: binding("router", "v1", 'e'), SourceCommit: strings.Repeat("f", 40), CreatedAt: createdAt.UTC()}
}

func behaviorReport(evaluatedAt time.Time, baseline, candidate string) behavior.EvaluationReport {
	dimensions := []behavior.DimensionResult{{ID: "gap_accuracy", AverageScore: 4.3, AcceptableRate: 0.90}, {ID: "goal_understanding", AverageScore: 4.3, AcceptableRate: 0.90}, {ID: "traceability", AverageScore: 4.3, AcceptableRate: 0.90}, {ID: "transfer_bridge_credibility", AverageScore: 4.3, AcceptableRate: 0.90}}
	slice := func(language string) behavior.SliceResult {
		return behavior.SliceResult{Language: language, DatasetHash: strings.Repeat("a", 64), SampleCount: 100, Dimensions: dimensions, MinimumGroupAcceptable: 0.82, DeterministicAccuracy: 1, ExpertAgreement: 0.90, CausalReferenceRate: 1, ManifestCompleteRate: 1}
	}
	return behavior.EvaluationReport{SchemaVersion: 1, ReportID: "route-planner-2026-07-15", CandidateSnapshotID: candidate, BaselineSnapshotID: baseline, Profile: behavior.RoutePlanner, Slices: []behavior.SliceResult{slice("en"), slice("zh-CN")}, ReviewerAgreement: 0.90, DimensionRegression: map[string]float64{"gap_accuracy": 0.01, "goal_understanding": 0.01, "traceability": 0.01, "transfer_bridge_credibility": 0.01}, CostMicrounitsP95: 80, CostBudget: 100, LatencyMillisP95: 1800, LatencyBudget: 2000, EvaluatedAt: evaluatedAt.UTC()}
}

func behaviorPool(t *testing.T, ctx context.Context, variable string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(variable)
	if url == "" {
		t.Skip(variable + " is not configured")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
