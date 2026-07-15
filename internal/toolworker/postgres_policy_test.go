package toolworker

import (
	"errors"
	"testing"
	"time"
)

func TestDecideOverlayAllowsImmutableBaseline(t *testing.T) {
	registry, snapshot := workerRegistry(t, "read_only")
	_ = registry
	platform := overlayRecord{Scope: "platform"}
	tenant := overlayRecord{Scope: "tenant"}
	first, err := decideOverlay("tenant-1", snapshot, platform, tenant)
	if err != nil || !first.Allowed || first.ReasonCode != "" || first.OverlayVersion != 1 || len(first.SnapshotHash) != 64 || first.SnapshotID != "tool-overlay-"+first.SnapshotHash {
		t.Fatalf("decision=%#v error=%v", first, err)
	}
	second, err := decideOverlay("tenant-1", snapshot, platform, tenant)
	if err != nil || second != first {
		t.Fatalf("baseline snapshot is not deterministic: first=%#v second=%#v error=%v", first, second, err)
	}
}

func TestDecideOverlayAppliesPlatformAndTenantDenials(t *testing.T) {
	_, snapshot := workerRegistry(t, "read_only")
	now := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	platform := overlayRecord{Scope: "platform", Revision: 7}
	tenant := overlayRecord{Scope: "tenant", Revision: 12, ID: "tenant-policy", PolicyVersion: 12, Decision: "deny", ReasonCode: "tenant_policy", EffectiveAt: &now}
	decision, err := decideOverlay("tenant-1", snapshot, platform, tenant)
	if err != nil || decision.Allowed || decision.ReasonCode != "tenant:tenant_policy" || decision.OverlayVersion != 12 {
		t.Fatalf("tenant decision=%#v error=%v", decision, err)
	}
	platform.ID, platform.PolicyVersion, platform.Decision, platform.ReasonCode, platform.EffectiveAt = "platform-policy", 7, "deny", "credential_exposure", &now
	decision, err = decideOverlay("tenant-1", snapshot, platform, tenant)
	if err != nil || decision.Allowed || decision.ReasonCode != "platform:credential_exposure" || decision.OverlayVersion != 12 {
		t.Fatalf("platform precedence=%#v error=%v", decision, err)
	}
}

func TestDecideOverlayRejectsMalformedDatabaseSnapshot(t *testing.T) {
	_, snapshot := workerRegistry(t, "read_only")
	malformed := overlayRecord{Scope: "platform", Revision: 1, ID: "policy", PolicyVersion: 2, Decision: "deny", ReasonCode: "bad"}
	if _, err := decideOverlay("tenant-1", snapshot, malformed, overlayRecord{Scope: "tenant"}); !errors.Is(err, ErrPolicySnapshot) {
		t.Fatalf("error=%v", err)
	}
}
