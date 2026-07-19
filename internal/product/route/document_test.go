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

func TestValidateGroundingRejectsUngroundedAndOverstatedOutput(t *testing.T) {
	valid := []byte(`{"schema_version":1,"summary":"A grounded transition.","transferable_experience":[{"statement":"Production delivery is demonstrated.","capability_ids":["cap-delivery"],"evidence_ids":["evidence-1"],"confidence":"verified"}],"gaps":[],"bridge":[{"id":"bridge_1","title":"Ship safely","rationale":"Turns analysis into delivery evidence.","from_capability_ids":["cap-delivery"],"to_capability_ids":["cap-target"]}],"stages":[{"id":"stage_1","title":"Foundation","outcome":"A reviewed plan.","capability_ids":["cap-target"],"evidence_required":["reviewed plan"]},{"id":"stage_2","title":"Delivery","outcome":"A deployed artifact.","capability_ids":["cap-target"],"evidence_required":["deployment record"]}],"first_task":{"title":"Draft the plan","objective":"Produce a reviewable delivery plan.","estimated_minutes":45,"difficulty":"standard","capability_ids":["cap-target"],"success_criteria":["Plan is reviewed"]}}`)
	document, err := DecodeDocument(valid)
	if err != nil {
		t.Fatal(err)
	}
	grounding := GroundingInput{
		Claims:              []GroundingClaim{{CapabilityID: "cap-delivery", Status: "active", VerificationLevel: "demonstrated", SupportingEvidenceIDs: []string{"evidence-1"}}},
		Evidence:            []GroundingEvidence{{ID: "evidence-1", Status: "verified"}},
		TargetCapabilityIDs: []string{"cap-target"},
	}
	if err = ValidateGrounding(document, grounding); err != nil {
		t.Fatalf("grounded document rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*GroundingInput, *Document)
	}{
		{"unknown capability", func(_ *GroundingInput, value *Document) { value.FirstTask.CapabilityIDs = []string{"cap-unknown"} }},
		{"unknown evidence", func(_ *GroundingInput, value *Document) {
			value.TransferableExperience[0].EvidenceIDs = []string{"evidence-unknown"}
		}},
		{"unlinked evidence", func(value *GroundingInput, _ *Document) { value.Claims[0].SupportingEvidenceIDs = nil }},
		{"recorded evidence cannot verify", func(value *GroundingInput, _ *Document) { value.Evidence[0].Status = "recorded" }},
		{"user confirmation cannot verify", func(value *GroundingInput, _ *Document) { value.Claims[0].VerificationLevel = "user_confirmed" }},
		{"invalidated evidence", func(value *GroundingInput, _ *Document) { value.Evidence[0].Invalidated = true }},
		{"inactive claim", func(value *GroundingInput, _ *Document) { value.Claims[0].Status = "disputed" }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := GroundingInput{
				Claims:              []GroundingClaim{{CapabilityID: "cap-delivery", Status: "active", VerificationLevel: "demonstrated", SupportingEvidenceIDs: []string{"evidence-1"}}},
				Evidence:            []GroundingEvidence{{ID: "evidence-1", Status: "verified"}},
				TargetCapabilityIDs: []string{"cap-target"},
			}
			candidate := document
			candidate.TransferableExperience = append([]GroundedAssessment(nil), document.TransferableExperience...)
			candidate.FirstTask.CapabilityIDs = append([]string(nil), document.FirstTask.CapabilityIDs...)
			testCase.mutate(&input, &candidate)
			if err := ValidateGrounding(candidate, input); !errors.Is(err, ErrUngroundedDocument) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
