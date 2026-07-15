package behavior

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestPromotionRequiresPassingGateAndTwoIndependentSignedOwners(t *testing.T) {
	now := time.Date(2026, 7, 15, 21, 0, 0, 0, time.UTC)
	manifest := validManifest()
	snapshotID, _ := manifest.SnapshotID()
	manifestHash, _ := manifest.Hash()
	report := passingReport(manifest.Profile)
	report.CandidateSnapshotID = snapshotID
	reportHash, _ := report.Hash()
	request := PromotionRequest{
		SchemaVersion: 1, TenantID: "20000000-0000-4000-8000-000000000001", Environment: "production", Profile: manifest.Profile,
		Sequence: 7, CandidateSnapshotID: snapshotID, PreviousSnapshotID: report.BaselineSnapshotID,
		ManifestHash: manifestHash, EvaluationReportHash: reportHash,
		Rollout:      RolloutPolicy{Percentages: []int{1, 5, 25, 100}, ObservationSecond: 900},
		AutoRollback: AutoRollbackPolicy{MaximumErrorRate: 0.01, MaximumLatencyRatio: 1.2, MaximumCostRatio: 1.1, MinimumQualityRatio: 0.98, ZeroToleranceEnabled: true},
		RequestedAt:  now.Add(-time.Minute),
	}
	keys := map[string]ed25519.PublicKey{}
	for _, identity := range []struct{ role, approver, key string }{{"risk_owner", "risk-1", "risk-key"}, {"release_owner", "release-1", "release-key"}} {
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		approval := Approval{Role: identity.role, ApproverID: identity.approver, KeyID: identity.key, SignedAt: now.Add(-30 * time.Second), ExpiresAt: now.Add(time.Hour)}
		_, promotionHash, _ := request.SigningPayload()
		payload, _ := approval.SigningPayload(promotionHash)
		approval.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
		request.Approvals = append(request.Approvals, approval)
		keys[identity.key] = publicKey
	}
	decision, err := VerifyPromotion(manifest, report, request, keys, now)
	if err != nil || !decision.Approved || len(decision.PromotionHash) != 64 {
		t.Fatalf("VerifyPromotion()=%#v err=%v", decision, err)
	}
}

func TestPromotionRejectsRoleRelabelReplayAndPostSignatureMutation(t *testing.T) {
	now := time.Date(2026, 7, 15, 21, 5, 0, 0, time.UTC)
	manifest := validManifest()
	snapshotID, _ := manifest.SnapshotID()
	manifestHash, _ := manifest.Hash()
	report := passingReport(manifest.Profile)
	report.CandidateSnapshotID = snapshotID
	reportHash, _ := report.Hash()
	request := PromotionRequest{
		SchemaVersion: 1, TenantID: "tenant", Environment: "production", Profile: manifest.Profile, Sequence: 1,
		CandidateSnapshotID: snapshotID, PreviousSnapshotID: report.BaselineSnapshotID, ManifestHash: manifestHash, EvaluationReportHash: reportHash,
		Rollout:      RolloutPolicy{Percentages: []int{10, 100}, ObservationSecond: 600},
		AutoRollback: AutoRollbackPolicy{MaximumErrorRate: 0.01, MaximumLatencyRatio: 1.2, MaximumCostRatio: 1.2, MinimumQualityRatio: 0.98, ZeroToleranceEnabled: true}, RequestedAt: now.Add(-time.Minute),
	}
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	_, promotionHash, _ := request.SigningPayload()
	approval := Approval{Role: "risk_owner", ApproverID: "same-person", KeyID: "shared", SignedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Hour)}
	payload, _ := approval.SigningPayload(promotionHash)
	approval.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	request.Approvals = []Approval{approval, approval}
	request.Approvals[1].Role = "release_owner"
	if _, err := VerifyPromotion(manifest, report, request, map[string]ed25519.PublicKey{"shared": publicKey}, now); err == nil {
		t.Fatal("one signature was replayed as two accountable roles")
	}
	request.Approvals = nil
	request.Rollout.Percentages = []int{100, 10}
	if _, err := VerifyPromotion(manifest, report, request, nil, now); err == nil {
		t.Fatal("invalid rollout ordering accepted")
	}
}

func TestAutomaticRollbackRequiresObservedThresholdBreachAndSignedEvidence(t *testing.T) {
	now := time.Date(2026, 7, 15, 21, 10, 0, 0, time.UTC)
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	request := RollbackRequest{
		SchemaVersion: 1, TenantID: "tenant", Environment: "production", Profile: Coach, Sequence: 8,
		FromSnapshotID: "behavior-" + strings.Repeat("d", 64), ToSnapshotID: "behavior-" + strings.Repeat("c", 64),
		Trigger: "zero_tolerance", ObservedValue: 1, Threshold: 0, IncidentEvidenceHash: strings.Repeat("e", 64),
		AutomationKeyID: "rollback-key", OccurredAt: now.Add(-time.Second),
	}
	payload, expectedHash, _ := request.SigningPayload()
	request.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	hash, err := VerifyRollback(request, map[string]ed25519.PublicKey{"rollback-key": publicKey}, now)
	if err != nil || hash != expectedHash {
		t.Fatalf("VerifyRollback()=%q err=%v", hash, err)
	}
	request.ObservedValue = 0
	if _, err = VerifyRollback(request, map[string]ed25519.PublicKey{"rollback-key": publicKey}, now); err == nil {
		t.Fatal("rollback without a threshold breach was accepted")
	}
}
