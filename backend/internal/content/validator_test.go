package content

import (
	"context"
	"testing"

	"lites/backend/internal/contracts"
)

func TestStaticValidatorRejectsMissingJudgeTestAndPrivacyLeak(t *testing.T) {
	artifact := validValidationArtifact()
	artifact.Lesson.Sections[0].BodyMD = "API boundaries separate request acceptance from worker execution. Contact alice@example.com for details."
	artifact.Exercise.Tests[0].Judge = false

	result, err := NewStaticValidator(StaticValidatorOptions{}).Validate(context.Background(), ValidationRequest{
		Input: GenerationInput{
			TaskTemplateID: "fe2agent-d01",
			TargetStack:    "frontend_to_agent",
			LevelBand:      "default",
			Title:          "API boundaries",
			Judge:          "Explain API acceptance versus worker execution.",
			Minutes:        30,
		},
		Artifact: artifact,
	})
	if err != nil {
		t.Fatalf("validate artifact: %v", err)
	}
	if result.Passed() {
		t.Fatal("validation passed, want issues")
	}
	if !hasIssue(result.Issues, "missing_judge_test") {
		t.Fatalf("missing judge issue in %#v", result.Issues)
	}
	if !hasIssue(result.Issues, "email_detected") {
		t.Fatalf("missing privacy issue in %#v", result.Issues)
	}
}

func TestStaticValidatorAcceptsValidArtifact(t *testing.T) {
	result, err := NewStaticValidator(StaticValidatorOptions{}).Validate(context.Background(), ValidationRequest{
		Input: GenerationInput{
			TaskTemplateID: "fe2agent-d01",
			TargetStack:    "frontend_to_agent",
			LevelBand:      "default",
			Title:          "API boundaries",
			Judge:          "Explain API acceptance versus worker execution.",
			Minutes:        30,
		},
		Artifact: validValidationArtifact(),
	})
	if err != nil {
		t.Fatalf("validate artifact: %v", err)
	}
	if !result.Passed() {
		t.Fatalf("validation issues = %#v, want none", result.Issues)
	}
}

func validValidationArtifact() contracts.ContentArtifact {
	return contracts.ContentArtifact{
		SchemaVersion:  1,
		ContentKey:     "content_test",
		TaskTemplateID: "fe2agent-d01",
		TargetStack:    "frontend_to_agent",
		LevelBand:      "default",
		ContentVersion: 1,
		PromptVersion:  1,
		Lesson: contracts.Lesson{
			Title:   "API boundaries",
			Minutes: 30,
			Judge:   "Explain API acceptance versus worker execution.",
			Sections: []contracts.LessonSection{{
				ID:     "section_1",
				Title:  "The boundary",
				BodyMD: "API acceptance records intent and queues work. Workers execute that work later.",
			}},
		},
		Exercise: contracts.Exercise{
			Language:          "javascript",
			StarterCode:       "function explainBoundary() { return ''; }",
			ReferenceSolution: "function explainBoundary() { return 'API acceptance queues work; workers execute it.'; }",
			HarnessVersion:    1,
			Tests: []contracts.ExerciseTest{{
				ID:     "case_1",
				Call:   "explainBoundary()",
				Expect: "API acceptance queues work; workers execute it.",
				Judge:  true,
				Label:  "explains the boundary",
			}},
		},
	}
}

func hasIssue(issues []ValidationIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
