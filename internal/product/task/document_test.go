package task

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDailyTaskDocumentIsClosedAndEvidenceGrounded(t *testing.T) {
	valid := map[string]any{
		"schema_version": 1, "title": "Reject terminal-state rollback", "objective": "Implement one guarded transition.",
		"why_this_task": "The accepted route identifies lifecycle safety as the next bridge.", "key_judgment": "A completed run cannot execute again.",
		"estimated_minutes": 40, "difficulty": "standard", "practice_kind": "code",
		"explanation":    []map[string]any{{"title": "Protect facts", "content": "Terminal state records an outcome that cannot be undone."}},
		"example":        "completed -> running is rejected",
		"practice":       map[string]any{"instructions": "Implement transition and run the checks.", "starter_content": "func transition() {}", "deterministic_checks": []string{"completed_to_running_is_rejected"}, "success_criteria": []string{"All checks pass", "The reason is explained"}},
		"capability_ids": []string{"capability.lifecycle_safety"}, "evidence_targets": []string{"test_result", "user_explanation"}, "next_task_hint": "Design replacement attempts.",
	}
	encoded, _ := json.Marshal(valid)
	document, err := ParseDocument(encoded)
	if err != nil || document.PracticeKind != "code" || len(document.CapabilityIDs) != 1 {
		t.Fatalf("document=%#v err=%v", document, err)
	}
	valid["unsupported"] = true
	encoded, _ = json.Marshal(valid)
	if _, err = ParseDocument(encoded); !errors.Is(err, ErrDocument) {
		t.Fatalf("unknown field accepted: %v", err)
	}
	delete(valid, "unsupported")
	valid["capability_ids"] = []string{"capability.lifecycle_safety", "capability.lifecycle_safety"}
	encoded, _ = json.Marshal(valid)
	if _, err = ParseDocument(encoded); !errors.Is(err, ErrDocument) {
		t.Fatalf("duplicate causal capability accepted: %v", err)
	}
}
