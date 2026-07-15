package behavior

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

var ErrAuthorization = errors.New("AI behavior promotion authorization is invalid")

type RolloutPolicy struct {
	Percentages       []int `json:"percentages"`
	ObservationSecond int   `json:"observation_seconds"`
}

type AutoRollbackPolicy struct {
	MaximumErrorRate     float64 `json:"maximum_error_rate"`
	MaximumLatencyRatio  float64 `json:"maximum_latency_ratio"`
	MaximumCostRatio     float64 `json:"maximum_cost_ratio"`
	MinimumQualityRatio  float64 `json:"minimum_quality_ratio"`
	ZeroToleranceEnabled bool    `json:"zero_tolerance_enabled"`
}

type Approval struct {
	Role       string    `json:"role"`
	ApproverID string    `json:"approver_id"`
	KeyID      string    `json:"key_id"`
	SignedAt   time.Time `json:"signed_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Signature  string    `json:"signature"`
}

type PromotionRequest struct {
	SchemaVersion        int                `json:"schema_version"`
	TenantID             string             `json:"tenant_id"`
	Environment          string             `json:"environment"`
	Profile              Profile            `json:"profile"`
	Sequence             uint64             `json:"sequence"`
	CandidateSnapshotID  string             `json:"candidate_snapshot_id"`
	PreviousSnapshotID   string             `json:"previous_snapshot_id"`
	ManifestHash         string             `json:"manifest_hash"`
	EvaluationReportHash string             `json:"evaluation_report_hash"`
	Rollout              RolloutPolicy      `json:"rollout"`
	AutoRollback         AutoRollbackPolicy `json:"auto_rollback"`
	RequestedAt          time.Time          `json:"requested_at"`
	Approvals            []Approval         `json:"approvals"`
}

type PromotionDecision struct {
	PromotionHash string `json:"promotion_hash"`
	Approved      bool   `json:"approved"`
}

type promotionPayload struct {
	SchemaVersion        int                `json:"schema_version"`
	TenantID             string             `json:"tenant_id"`
	Environment          string             `json:"environment"`
	Profile              Profile            `json:"profile"`
	Sequence             uint64             `json:"sequence"`
	CandidateSnapshotID  string             `json:"candidate_snapshot_id"`
	PreviousSnapshotID   string             `json:"previous_snapshot_id"`
	ManifestHash         string             `json:"manifest_hash"`
	EvaluationReportHash string             `json:"evaluation_report_hash"`
	Rollout              RolloutPolicy      `json:"rollout"`
	AutoRollback         AutoRollbackPolicy `json:"auto_rollback"`
	RequestedAt          time.Time          `json:"requested_at"`
}

type approvalPayload struct {
	PromotionHash string    `json:"promotion_hash"`
	Role          string    `json:"role"`
	ApproverID    string    `json:"approver_id"`
	KeyID         string    `json:"key_id"`
	SignedAt      time.Time `json:"signed_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func VerifyPromotion(manifest Manifest, report EvaluationReport, request PromotionRequest, keys map[string]ed25519.PublicKey, now time.Time) (PromotionDecision, error) {
	canonical, err := manifest.Canonical()
	if err != nil {
		return PromotionDecision{}, err
	}
	manifestHash, _ := canonical.Hash()
	snapshotID, _ := canonical.SnapshotID()
	reportHash, err := report.Hash()
	gate := report.Evaluate()
	if err != nil || !gate.Passed || request.SchemaVersion != 1 || request.TenantID == "" || len(request.TenantID) > 128 || request.Environment != "staging" && request.Environment != "production" || request.Profile != canonical.Profile || request.Profile != report.Profile || request.Sequence < 1 || request.CandidateSnapshotID != snapshotID || request.CandidateSnapshotID != report.CandidateSnapshotID || request.PreviousSnapshotID != report.BaselineSnapshotID || request.ManifestHash != manifestHash || request.EvaluationReportHash != reportHash || !validRollout(request.Rollout) || !validAutoRollback(request.AutoRollback) || request.RequestedAt.IsZero() || request.RequestedAt.Location() != time.UTC || now.IsZero() || now.Location() != time.UTC || now.Before(request.RequestedAt) || now.Sub(request.RequestedAt) > 24*time.Hour {
		return PromotionDecision{}, ErrAuthorization
	}
	_, payloadHash, err := request.authorizationPayload()
	if err != nil {
		return PromotionDecision{}, err
	}
	roles := map[string]Approval{}
	identities := map[string]struct{}{}
	for _, approval := range request.Approvals {
		if approval.Role != "risk_owner" && approval.Role != "release_owner" {
			return PromotionDecision{}, ErrAuthorization
		}
		if _, duplicate := roles[approval.Role]; duplicate || approval.ApproverID == "" || approval.KeyID == "" || approval.SignedAt.IsZero() || approval.ExpiresAt.IsZero() || approval.SignedAt.Location() != time.UTC || approval.ExpiresAt.Location() != time.UTC || approval.SignedAt.Before(request.RequestedAt) || approval.SignedAt.After(now) || !approval.ExpiresAt.After(now) || approval.ExpiresAt.Sub(approval.SignedAt) > 24*time.Hour {
			return PromotionDecision{}, ErrAuthorization
		}
		if _, duplicate := identities[approval.ApproverID]; duplicate {
			return PromotionDecision{}, ErrAuthorization
		}
		publicKey, exists := keys[approval.KeyID]
		signature, decodeErr := base64.RawURLEncoding.DecodeString(approval.Signature)
		signedPayload, payloadErr := approval.signingPayload(payloadHash)
		if !exists || decodeErr != nil || payloadErr != nil || len(publicKey) != ed25519.PublicKeySize || !ed25519.Verify(publicKey, signedPayload, signature) {
			return PromotionDecision{}, ErrAuthorization
		}
		roles[approval.Role] = approval
		identities[approval.ApproverID] = struct{}{}
	}
	if len(roles) != 2 {
		return PromotionDecision{}, ErrAuthorization
	}
	return PromotionDecision{PromotionHash: payloadHash, Approved: true}, nil
}

func (approval Approval) signingPayload(promotionHash string) ([]byte, error) {
	if !digestPattern.MatchString(promotionHash) {
		return nil, ErrAuthorization
	}
	encoded, err := json.Marshal(approvalPayload{
		PromotionHash: promotionHash, Role: approval.Role, ApproverID: approval.ApproverID, KeyID: approval.KeyID,
		SignedAt: approval.SignedAt.Truncate(time.Microsecond), ExpiresAt: approval.ExpiresAt.Truncate(time.Microsecond),
	})
	if err != nil {
		return nil, ErrAuthorization
	}
	return encoded, nil
}

func (request PromotionRequest) authorizationPayload() ([]byte, string, error) {
	payload := promotionPayload{
		SchemaVersion: request.SchemaVersion, TenantID: request.TenantID, Environment: request.Environment,
		Profile: request.Profile, Sequence: request.Sequence, CandidateSnapshotID: request.CandidateSnapshotID,
		PreviousSnapshotID: request.PreviousSnapshotID, ManifestHash: request.ManifestHash,
		EvaluationReportHash: request.EvaluationReportHash, Rollout: request.Rollout,
		AutoRollback: request.AutoRollback, RequestedAt: request.RequestedAt.Truncate(time.Microsecond),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, "", ErrAuthorization
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}

func validRollout(policy RolloutPolicy) bool {
	if policy.ObservationSecond < 60 || policy.ObservationSecond > 86400 || len(policy.Percentages) < 1 || len(policy.Percentages) > 16 {
		return false
	}
	percentages := append([]int(nil), policy.Percentages...)
	if !sort.IntsAreSorted(percentages) || percentages[len(percentages)-1] != 100 {
		return false
	}
	for index, percentage := range percentages {
		if percentage < 1 || percentage > 100 || index > 0 && percentages[index-1] == percentage {
			return false
		}
	}
	return true
}

func validAutoRollback(policy AutoRollbackPolicy) bool {
	return policy.ZeroToleranceEnabled && validRate(policy.MaximumErrorRate) && policy.MaximumErrorRate > 0 && policy.MaximumErrorRate <= 0.05 && policy.MaximumLatencyRatio >= 1 && policy.MaximumLatencyRatio <= 2 && policy.MaximumCostRatio >= 1 && policy.MaximumCostRatio <= 2 && policy.MinimumQualityRatio >= 0.95 && policy.MinimumQualityRatio <= 1
}
