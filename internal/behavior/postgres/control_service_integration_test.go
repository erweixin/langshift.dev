//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/langshift/lites/internal/behavior"
	behaviorapi "github.com/langshift/lites/internal/behavior/api"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/platform/ids"
)

type behaviorControlKeys struct{ key payload.Key }

func (provider behaviorControlKeys) Current(context.Context, string) (payload.Key, error) {
	return provider.key, nil
}

func (provider behaviorControlKeys) ByID(_ context.Context, _ string, keyID string) (payload.Key, error) {
	if keyID != provider.key.ID {
		return payload.Key{}, errors.New("unknown key")
	}
	return provider.key, nil
}

type behaviorControlBlobs struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (store *behaviorControlBlobs) Put(_ context.Context, key string, value []byte) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.values[key]; ok && !bytes.Equal(existing, value) {
		return "", errors.New("immutable blob conflict")
	}
	store.values[key] = append([]byte(nil), value...)
	return "memory://" + key, nil
}

func (store *behaviorControlBlobs) Get(_ context.Context, ref string) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.values[ref[len("memory://"):]]
	if !ok {
		return nil, errors.New("blob missing")
	}
	return append([]byte(nil), value...), nil
}

func TestControlServicePersistsEncryptedIdempotentPromotionAndRollback(t *testing.T) {
	ctx := context.Background()
	admin := behaviorPool(t, ctx, "LITES_TEST_ADMIN_DATABASE_URL")
	defer admin.Close()
	database := behaviorPool(t, ctx, "LITES_TEST_BEHAVIOR_DATABASE_URL")
	defer database.Close()
	const (
		userID         = "10000000-0000-4000-8000-000000009901"
		automationUser = "10000000-0000-4000-8000-000000009902"
		tenantID       = "20000000-0000-4000-8000-000000009901"
		membershipID   = "30000000-0000-4000-8000-000000009901"
		sessionID      = "40000000-0000-4000-8000-000000009901"
		epoch          = "90000000-0000-4000-8000-000000009901"
	)
	now := time.Date(2026, time.July, 15, 11, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO identity.users(id,normalized_email,email_verified_at,locale,status) VALUES($1,'behavior-control@lites.invalid',$3,'en','active'),($2,'behavior-automation@lites.invalid',$3,'en','active')`, userID, automationUser, now); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.tenants(id,kind,name,status,region,owner_user_id) VALUES($1,'personal','Behavior Control','active','US',$2)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO identity.memberships(id,tenant_id,user_id,role,status,joined_at) VALUES($1,$2,$3,'owner','active',$4)`, membershipID, tenantID, userID, now); err != nil {
		t.Fatal(err)
	}
	tokenHash := sha256.Sum256([]byte("behavior-control-token"))
	csrfHash := sha256.Sum256([]byte("behavior-control-csrf"))
	ipHash := sha256.Sum256([]byte("behavior-control-ip"))
	userAgentHash := sha256.Sum256([]byte("behavior-control-agent"))
	if _, err := admin.Exec(ctx, `INSERT INTO identity.sessions(id,user_id,active_tenant_id,token_hash,csrf_secret_hash,ip_hash,user_agent_hash,last_seen_at,expires_at,reauthenticated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$8)`, sessionID, userID, tenantID, tokenHash[:], csrfHash[:], ipHash[:], userAgentHash[:], now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	publicKeys := map[string]ed25519.PublicKey{}
	privateKeys := map[string]ed25519.PrivateKey{}
	for _, keyID := range []string{"risk-control", "release-control", "rollback-control"} {
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		publicKeys[keyID], privateKeys[keyID] = publicKey, privateKey
	}
	blobs := &behaviorControlBlobs{values: map[string][]byte{}}
	payloads := payload.EnvelopeStore{Keys: behaviorControlKeys{key: payload.Key{ID: "behavior-control-v1", Material: bytes.Repeat([]byte{0x91}, 32)}}, Blobs: blobs}
	store := Store{Pool: database, Appender: eventpostgres.Appender{Now: func() time.Time { return now }}, Epochs: fixedEpoch(epoch), StoreEpoch: epoch, PublicKeys: publicKeys, Now: func() time.Time { return now }}
	service := ControlService{
		Pool: database, Store: store, Payloads: payloads, IDKey: bytes.Repeat([]byte{0x81}, 32),
		IdempotencyKeyPepper: bytes.Repeat([]byte{0x82}, 32), RequestDigestPepper: bytes.Repeat([]byte{0x83}, 32),
		IdempotencyTTL: 24 * time.Hour, ReauthenticationAge: 15 * time.Minute, AutomationUserID: automationUser, Now: func() time.Time { return now },
	}
	metadata := func(clientID, key string) behaviorapi.CommandMetadata {
		requestID, _ := ids.DeterministicUUID(bytes.Repeat([]byte{0x84}, 32), "behavior-control-test-request", clientID)
		return behaviorapi.CommandMetadata{RequestID: requestID, ClientRequestID: clientID, IdempotencyKey: key, TenantID: tenantID, UserID: userID, SessionID: sessionID}
	}
	origin := behaviorManifest(now.Add(-3*time.Hour), '1')
	baseline := behaviorManifest(now.Add(-2*time.Hour), '2')
	candidate := behaviorManifest(now.Add(-time.Hour), '3')
	for index, item := range []struct {
		manifest behavior.Manifest
		clientID string
		key      string
	}{{origin, "snapshot-origin-001", "behavior-snapshot-origin-key"}, {baseline, "snapshot-baseline-001", "behavior-snapshot-baseline-key"}, {candidate, "snapshot-candidate-001", "behavior-snapshot-candidate-key"}} {
		result, err := service.CreateSnapshot(ctx, behaviorapi.CreateSnapshotCommand{CommandMetadata: metadata(item.clientID, item.key), Manifest: item.manifest})
		if err != nil || result.Hash == "" || result.EventID == "" || result.Replayed {
			t.Fatalf("CreateSnapshot(%d)=%#v err=%v", index, result, err)
		}
		if index == 2 {
			replayed, replayErr := service.CreateSnapshot(ctx, behaviorapi.CreateSnapshotCommand{CommandMetadata: metadata(item.clientID, item.key), Manifest: item.manifest})
			if replayErr != nil || replayed != result {
				t.Fatalf("snapshot idempotent replay=%#v err=%v want=%#v", replayed, replayErr, result)
			}
			changed := item.manifest
			changed.SourceCommit = "changed-source-commit"
			if _, conflictErr := service.CreateSnapshot(ctx, behaviorapi.CreateSnapshotCommand{CommandMetadata: metadata(item.clientID, item.key), Manifest: changed}); !errors.Is(conflictErr, behaviorapi.ErrIdempotencyConflict) {
				t.Fatalf("changed idempotency payload error=%v", conflictErr)
			}
		}
	}
	crashManifest := behaviorManifest(now.Add(-90*time.Minute), '4')
	crashMetadata := metadata("snapshot-crash-window-001", "behavior-snapshot-crash-window-key")
	crashCanonical, _ := crashManifest.Canonical()
	crashRequest, _ := json.Marshal(struct {
		ClientRequestID string            `json:"request_id"`
		Manifest        behavior.Manifest `json:"manifest"`
	}{crashMetadata.ClientRequestID, crashCanonical})
	crashInput, crashDescriptor, err := service.idempotencyInput(tenantID, userID, createSnapshotOperation, crashMetadata.IdempotencyKey, crashMetadata.RequestID, crashRequest)
	if err != nil {
		t.Fatal(err)
	}
	if completed, beginErr := service.beginIdempotency(ctx, crashInput); beginErr != nil || completed {
		t.Fatalf("begin crash window completed=%v err=%v", completed, beginErr)
	}
	crashIDs, _ := service.identifiers(createSnapshotOperation, crashInput.RecordID)
	crashSnapshotID, _ := crashCanonical.SnapshotID()
	crashHash, _ := crashCanonical.Hash()
	crashPayload, err := service.prepareEventPayload(ctx, crashInput, crashIDs.EventID, map[string]any{"snapshot_id": crashSnapshotID, "profile": crashCanonical.Profile, "manifest_hash": crashHash, "source_commit": crashCanonical.SourceCommit, "created_at": crashCanonical.CreatedAt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateSnapshot(ctx, CreateSnapshotCommand{ID: crashIDs.ResourceID, EventID: crashIDs.EventID, OutboxID: crashIDs.OutboxID, PublishCommandID: crashIDs.PublishID, TenantID: tenantID, UserID: userID, CorrelationID: crashMetadata.RequestID, Manifest: crashCanonical, Actor: adminActor(userID, sessionID), EventPayload: crashPayload}); err != nil {
		t.Fatal(err)
	}
	// Simulate process loss after the domain/event commit and before the
	// idempotency response update. The normal entrypoint must reuse the selected
	// encrypted pointer and complete the response.
	recovered, err := service.CreateSnapshot(ctx, behaviorapi.CreateSnapshotCommand{CommandMetadata: crashMetadata, Manifest: crashManifest})
	if err != nil || !recovered.Replayed || recovered.ResourceID != crashSnapshotID {
		t.Fatalf("crash-window recovery=%#v err=%v descriptor=%#v", recovered, err, crashDescriptor)
	}
	originSnapshot, _ := origin.SnapshotID()
	baselineSnapshot, _ := baseline.SnapshotID()
	candidateSnapshot, _ := candidate.SnapshotID()
	firstReport := behaviorReport(now.Add(-40*time.Minute), originSnapshot, baselineSnapshot)
	firstReport.ReportID = "route-planner-control-baseline"
	firstEvaluation, err := service.RecordEvaluation(ctx, behaviorapi.RecordEvaluationCommand{CommandMetadata: metadata("evaluation-baseline-001", "behavior-evaluation-baseline-key"), Report: firstReport})
	if err != nil {
		t.Fatal(err)
	}
	secondReport := behaviorReport(now.Add(-20*time.Minute), baselineSnapshot, candidateSnapshot)
	secondReport.ReportID = "route-planner-control-candidate"
	secondEvaluation, err := service.RecordEvaluation(ctx, behaviorapi.RecordEvaluationCommand{CommandMetadata: metadata("evaluation-candidate-001", "behavior-evaluation-candidate-key"), Report: secondReport})
	if err != nil {
		t.Fatal(err)
	}
	firstManifestHash, _ := baseline.Hash()
	firstPromotion := behavior.PromotionRequest{
		SchemaVersion: 1, TenantID: tenantID, Environment: "production", Profile: behavior.RoutePlanner, Sequence: 1,
		CandidateSnapshotID: baselineSnapshot, PreviousSnapshotID: originSnapshot, ManifestHash: firstManifestHash, EvaluationReportHash: firstEvaluation.Hash,
		Rollout:      behavior.RolloutPolicy{Percentages: []int{1, 10, 50, 100}, ObservationSecond: 600},
		AutoRollback: behavior.AutoRollbackPolicy{MaximumErrorRate: 0.01, MaximumLatencyRatio: 1.2, MaximumCostRatio: 1.1, MinimumQualityRatio: 0.98, ZeroToleranceEnabled: true},
		RequestedAt:  now.Add(-2 * time.Minute),
	}
	firstPromotion.Approvals = controlApprovals(t, firstPromotion, now, privateKeys)
	if result, promoteErr := service.Promote(ctx, behaviorapi.PromoteCommand{CommandMetadata: metadata("promotion-baseline-001", "behavior-promotion-baseline-key"), Request: firstPromotion}); promoteErr != nil || result.Sequence != 1 {
		t.Fatalf("first promotion=%#v err=%v", result, promoteErr)
	}
	secondManifestHash, _ := candidate.Hash()
	secondPromotion := firstPromotion
	secondPromotion.Sequence = 2
	secondPromotion.CandidateSnapshotID = candidateSnapshot
	secondPromotion.PreviousSnapshotID = baselineSnapshot
	secondPromotion.ManifestHash = secondManifestHash
	secondPromotion.EvaluationReportHash = secondEvaluation.Hash
	secondPromotion.RequestedAt = now.Add(-time.Minute)
	secondPromotion.Approvals = controlApprovals(t, secondPromotion, now, privateKeys)
	if result, promoteErr := service.Promote(ctx, behaviorapi.PromoteCommand{CommandMetadata: metadata("promotion-candidate-001", "behavior-promotion-candidate-key"), Request: secondPromotion}); promoteErr != nil || result.Sequence != 2 {
		t.Fatalf("second promotion=%#v err=%v", result, promoteErr)
	}
	rollback := behavior.RollbackRequest{
		SchemaVersion: 1, TenantID: tenantID, Environment: "production", Profile: behavior.RoutePlanner, Sequence: 3,
		FromSnapshotID: candidateSnapshot, ToSnapshotID: baselineSnapshot, Trigger: "zero_tolerance", ObservedValue: 1, Threshold: 0,
		IncidentEvidenceHash: repeatHex('9'), AutomationKeyID: "rollback-control", OccurredAt: now.Add(-time.Second),
	}
	rollbackPayload, _, _ := rollback.SigningPayload()
	rollback.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKeys["rollback-control"], rollbackPayload))
	rollbackRequestID, _ := ids.DeterministicUUID(bytes.Repeat([]byte{0x84}, 32), "behavior-control-test-request", "rollback-control-001")
	rolledBack, err := service.AutomaticRollback(ctx, behaviorapi.AutomaticRollbackCommand{RequestID: rollbackRequestID, ClientRequestID: "rollback-control-001", IdempotencyKey: "behavior-rollback-control-key", Request: rollback})
	if err != nil || rolledBack.Sequence != 3 || rolledBack.ResourceID != baselineSnapshot {
		t.Fatalf("rollback=%#v err=%v", rolledBack, err)
	}
	current, err := service.Current(ctx, tenantID, behavior.RoutePlanner, "production")
	if err != nil || current.Sequence != 3 || current.SnapshotID != baselineSnapshot {
		t.Fatalf("current=%#v err=%v", current, err)
	}
	var completed int
	var responseRef, responseHash, eventRef, eventHash string
	if err = admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent.idempotency_responses WHERE tenant_id=$1 AND status='completed'),i.response_payload_ref,i.response_hash,e.payload_ref,e.payload_hash FROM agent.idempotency_responses i JOIN agent.events e ON e.tenant_id=i.tenant_id AND e.id=$2 WHERE i.tenant_id=$1 AND i.operation_id=$3 LIMIT 1`, tenantID, rolledBack.EventID, rollbackOperation).Scan(&completed, &responseRef, &responseHash, &eventRef, &eventHash); err != nil {
		t.Fatal(err)
	}
	if completed != 9 || responseRef == "" || responseHash == "" || eventRef == "" || eventHash == "" || bytes.Contains(blobs.values[eventRef[len("memory://"):]], []byte(baselineSnapshot)) {
		t.Fatalf("completed=%d response=%q/%q event=%q/%q encrypted=%v", completed, responseRef, responseHash, eventRef, eventHash, !bytes.Contains(blobs.values[eventRef[len("memory://"):]], []byte(baselineSnapshot)))
	}
}

func controlApprovals(t *testing.T, request behavior.PromotionRequest, now time.Time, privateKeys map[string]ed25519.PrivateKey) []behavior.Approval {
	t.Helper()
	_, promotionHash, err := request.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	result := make([]behavior.Approval, 0, 2)
	for _, approval := range []behavior.Approval{
		{Role: "risk_owner", ApproverID: "risk-control-owner", KeyID: "risk-control", SignedAt: now.Add(-30 * time.Second), ExpiresAt: now.Add(time.Hour)},
		{Role: "release_owner", ApproverID: "release-control-owner", KeyID: "release-control", SignedAt: now.Add(-20 * time.Second), ExpiresAt: now.Add(time.Hour)},
	} {
		payload, payloadErr := approval.SigningPayload(promotionHash)
		if payloadErr != nil {
			t.Fatal(payloadErr)
		}
		approval.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKeys[approval.KeyID], payload))
		result = append(result, approval)
	}
	return result
}

func repeatHex(value byte) string { return string(bytes.Repeat([]byte{value}, 64)) }
