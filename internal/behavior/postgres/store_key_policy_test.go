package postgres

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/langshift/lites/internal/behavior"
)

func TestStoreKeyWindowsAndPurposesFailClosed(t *testing.T) {
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	key := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	store := Store{
		PublicKeys: map[string]ed25519.PublicKey{"risk": key, "release": key, "rollback": key, "expired": key},
		PublicKeyWindows: map[string]KeyWindow{
			"risk": {NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}, "release": {NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)},
			"rollback": {NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}, "expired": {NotBefore: now.Add(-2 * time.Hour), NotAfter: now},
		},
		PublicKeyPurposes: map[string]string{"risk": "risk_owner", "release": "release_owner", "rollback": "rollback_automation", "expired": "risk_owner"},
	}
	active := store.activePublicKeys(now)
	if len(active) != 3 || active["expired"] != nil {
		t.Fatalf("active keys=%v", active)
	}
	promotion := behavior.PromotionRequest{Approvals: []behavior.Approval{{Role: "risk_owner", KeyID: "risk"}, {Role: "release_owner", KeyID: "release"}}}
	if !store.validPromotionKeyPurposes(promotion) {
		t.Fatal("valid purpose-separated approvals rejected")
	}
	promotion.Approvals[0].KeyID = "release"
	if store.validPromotionKeyPurposes(promotion) {
		t.Fatal("release key accepted for risk approval")
	}
	if !store.validRollbackKeyPurpose(behavior.RollbackRequest{AutomationKeyID: "rollback"}) || store.validRollbackKeyPurpose(behavior.RollbackRequest{AutomationKeyID: "risk"}) {
		t.Fatal("rollback key purpose policy is not enforced")
	}
}
