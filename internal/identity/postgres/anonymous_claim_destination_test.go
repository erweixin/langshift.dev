package postgres

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/langshift/lites/internal/identity/anonymousclaim"
	productroute "github.com/langshift/lites/internal/product/route"
)

func TestClaimContentCapabilityIDsIncludesUnselectedManifestRequirements(t *testing.T) {
	document := productroute.Document{
		Gaps:      []productroute.GroundedAssessment{{CapabilityIDs: []string{"cap-route"}}},
		FirstTask: productroute.FirstTask{CapabilityIDs: []string{"cap-route"}},
	}
	manifest := json.RawMessage(`{"target_requirements":[{"capability_id":"cap-route"},{"capability_id":"cap-unselected"}],"claim_revisions":[{"capability_id":"cap-claim"}],"future":{"capability_ids":["cap-array"]}}`)
	actual, err := claimContentCapabilityIDs(document, manifest)
	want := []string{"cap-array", "cap-claim", "cap-route", "cap-unselected"}
	if err != nil || !slices.Equal(actual, want) {
		t.Fatalf("capability IDs=%v err=%v", actual, err)
	}
}

func TestClaimContentCapabilityIDsRejectsMalformedReferences(t *testing.T) {
	document := productroute.Document{FirstTask: productroute.FirstTask{CapabilityIDs: []string{"cap-route"}}}
	for _, manifest := range []json.RawMessage{
		json.RawMessage(`{"capability_id":1}`),
		json.RawMessage(`{"capability_ids":"cap-invalid"}`),
		json.RawMessage(`{"capability_ids":[""]}`),
		json.RawMessage(`null`),
	} {
		if _, err := claimContentCapabilityIDs(document, manifest); !errors.Is(err, anonymousclaim.ErrInvariant) {
			t.Fatalf("manifest=%s err=%v", manifest, err)
		}
	}
}
