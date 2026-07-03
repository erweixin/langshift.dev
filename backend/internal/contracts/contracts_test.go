package contracts_test

import (
	"encoding/json"
	"testing"

	"lites/backend/internal/contracts"
)

func TestReviewCompletedJSONShape(t *testing.T) {
	payload := []byte(`{
		"evidence_id": "ev_01",
		"did": ["named the invariant"],
		"fix": "Make the failure mode concrete.",
		"next": "Add one retry case.",
		"cap_delta": {
			"capability_id": "agent_state",
			"from": 1,
			"to": 2,
			"label": "看过 -> 能解释"
		},
		"memo": "You noticed the crash window.",
		"next_task_seed": {
			"title": "Trace one run state",
			"minutes": 25
		}
	}`)

	var review contracts.ReviewCompleted
	if err := json.Unmarshal(payload, &review); err != nil {
		t.Fatalf("unmarshal review completed: %v", err)
	}
	if review.EvidenceID != "ev_01" {
		t.Fatalf("evidence id = %q", review.EvidenceID)
	}
	if review.CapDelta.CapabilityID != "agent_state" {
		t.Fatalf("capability id = %q", review.CapDelta.CapabilityID)
	}
}

func TestExerciseResultJSONShape(t *testing.T) {
	payload := []byte(`{
		"status": "ok",
		"cases": [{"id": "case_1", "pass": true, "actual": "42"}],
		"judge_hit": true,
		"duration_ms": 12,
		"code_hash": "sha256:abc",
		"harness_version": 1,
		"runtime": "browser-js"
	}`)

	var result contracts.ExerciseResult
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("unmarshal exercise result: %v", err)
	}
	if !result.JudgeHit {
		t.Fatal("expected judge_hit")
	}
}
