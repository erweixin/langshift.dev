package route

import (
	"errors"
	"testing"
)

func TestRouteDocumentIsClosedAndGrounded(t *testing.T) {
	valid := []byte(`{"schema_version":1,"summary":"A grounded transition.","transferable_experience":[],"gaps":[{"statement":"Practice production delivery.","capability_ids":["capability-1"],"evidence_ids":[],"confidence":"inferred"}],"bridge":[{"id":"bridge_1","title":"Ship safely","rationale":"Turns existing analysis into delivery evidence.","from_capability_ids":[],"to_capability_ids":["capability-1"]}],"stages":[{"id":"stage_1","title":"Foundation","outcome":"A reviewed plan.","capability_ids":["capability-1"],"evidence_required":["reviewed plan"]},{"id":"stage_2","title":"Delivery","outcome":"A deployed artifact.","capability_ids":["capability-1"],"evidence_required":["deployment record"]}],"first_task":{"title":"Draft the plan","objective":"Produce a reviewable delivery plan.","estimated_minutes":45,"difficulty":"standard","capability_ids":["capability-1"],"success_criteria":["Plan is reviewed"]}}`)
	if _, err := DecodeDocument(valid); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{
		append(valid[:len(valid)-1], []byte(`,"unexpected":true}`)...),
		[]byte(`{"schema_version":1}`),
		[]byte(`null`),
	} {
		if _, err := DecodeDocument(invalid); !errors.Is(err, ErrInvalidDocument) {
			t.Fatalf("invalid document accepted: %s", invalid)
		}
	}
}
