package content

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"lites/backend/internal/contracts"
)

type ArtifactValidator interface {
	Validate(ctx context.Context, request ValidationRequest) (ValidationResult, error)
}

type ExerciseRuntime interface {
	Check(ctx context.Context, request ExerciseRuntimeRequest) (ExerciseRuntimeResult, error)
}

type StaticValidator struct {
	runtime ExerciseRuntime
}

type StaticValidatorOptions struct {
	Runtime ExerciseRuntime
}

func NewStaticValidator(options StaticValidatorOptions) StaticValidator {
	return StaticValidator{runtime: options.Runtime}
}

func (v StaticValidator) Validate(ctx context.Context, request ValidationRequest) (ValidationResult, error) {
	result := ValidationResult{Attempts: 1}
	if err := validateArtifact(request.Artifact); err != nil {
		result.Issues = append(result.Issues, ValidationIssue{
			Code:    "invalid_structure",
			Message: err.Error(),
		})
		return result, nil
	}

	result.Issues = append(result.Issues, validateLesson(request.Artifact.Lesson)...)
	result.Issues = append(result.Issues, validateExercise(request.Artifact.Exercise)...)
	result.Issues = append(result.Issues, validateInputMatch(request)...)
	result.Issues = append(result.Issues, validatePrivacyLint(request.Artifact)...)

	if v.runtime != nil {
		runtimeResult, err := v.runtime.Check(ctx, ExerciseRuntimeRequest{Artifact: request.Artifact})
		if err != nil {
			return ValidationResult{}, fmt.Errorf("check content exercise runtime: %w", err)
		}
		result.Issues = append(result.Issues, runtimeResult.Issues...)
		if !runtimeResult.Skipped {
			if !runtimeResult.ReferencePassed {
				result.Issues = append(result.Issues, ValidationIssue{
					Code:    "reference_solution_failed",
					Field:   "exercise.reference_solution",
					Message: "reference solution must pass all tests",
				})
			}
			if !runtimeResult.StarterFailed {
				result.Issues = append(result.Issues, ValidationIssue{
					Code:    "starter_does_not_fail",
					Field:   "exercise.starter_code",
					Message: "starter code must fail at least one judge test",
				})
			}
		}
	}
	return result, nil
}

type NoopExerciseRuntime struct{}

func (NoopExerciseRuntime) Check(context.Context, ExerciseRuntimeRequest) (ExerciseRuntimeResult, error) {
	return ExerciseRuntimeResult{Skipped: true}, nil
}

func validateLesson(lesson contracts.Lesson) []ValidationIssue {
	var issues []ValidationIssue
	if len(lesson.Sections) > 8 {
		issues = append(issues, ValidationIssue{
			Code:    "too_many_sections",
			Field:   "lesson.sections",
			Message: "lesson must have at most 8 sections",
		})
	}
	if len([]rune(lesson.Title)) > 120 {
		issues = append(issues, ValidationIssue{
			Code:    "title_too_long",
			Field:   "lesson.title",
			Message: "lesson title must be at most 120 characters",
		})
	}
	if len([]rune(lesson.Judge)) > 240 {
		issues = append(issues, ValidationIssue{
			Code:    "judge_too_long",
			Field:   "lesson.judge",
			Message: "lesson judge must be at most 240 characters",
		})
	}
	for i, section := range lesson.Sections {
		field := fmt.Sprintf("lesson.sections[%d].body_md", i)
		body := strings.TrimSpace(section.BodyMD)
		if len([]rune(body)) < 24 {
			issues = append(issues, ValidationIssue{
				Code:    "section_too_short",
				Field:   field,
				Message: "lesson section body is too short to teach the concept",
			})
		}
		if len([]rune(body)) > 5000 {
			issues = append(issues, ValidationIssue{
				Code:    "section_too_long",
				Field:   field,
				Message: "lesson section body must be at most 5000 characters",
			})
		}
	}
	return issues
}

func validateExercise(exercise contracts.Exercise) []ValidationIssue {
	var issues []ValidationIssue
	language := strings.ToLower(strings.TrimSpace(exercise.Language))
	switch language {
	case "javascript", "js", "typescript", "ts":
	default:
		issues = append(issues, ValidationIssue{
			Code:    "unsupported_language",
			Field:   "exercise.language",
			Message: "exercise language must be javascript or typescript",
		})
	}

	if normalizeCode(exercise.StarterCode) == normalizeCode(exercise.ReferenceSolution) {
		issues = append(issues, ValidationIssue{
			Code:    "starter_equals_reference",
			Field:   "exercise.starter_code",
			Message: "starter code must not be identical to the reference solution",
		})
	}
	issues = append(issues, validateUnsafeCode("exercise.starter_code", exercise.StarterCode)...)
	issues = append(issues, validateUnsafeCode("exercise.reference_solution", exercise.ReferenceSolution)...)

	judgeCount := 0
	for i, test := range exercise.Tests {
		if test.Judge {
			judgeCount++
		}
		if strings.TrimSpace(test.Call) == "" {
			issues = append(issues, ValidationIssue{
				Code:    "empty_test_call",
				Field:   fmt.Sprintf("exercise.tests[%d].call", i),
				Message: "test call is required",
			})
		}
		if test.Expect == nil {
			issues = append(issues, ValidationIssue{
				Code:    "missing_test_expectation",
				Field:   fmt.Sprintf("exercise.tests[%d].expect", i),
				Message: "test expectation is required",
			})
		}
	}
	if judgeCount == 0 {
		issues = append(issues, ValidationIssue{
			Code:    "missing_judge_test",
			Field:   "exercise.tests",
			Message: "at least one test must be marked as judge coverage",
		})
	}
	return issues
}

func validateInputMatch(request ValidationRequest) []ValidationIssue {
	var issues []ValidationIssue
	artifact := request.Artifact
	input := request.Input
	if artifact.TaskTemplateID != input.TaskTemplateID {
		issues = append(issues, dimensionIssue("task_template_id"))
	}
	if artifact.TargetStack != input.TargetStack {
		issues = append(issues, dimensionIssue("target_stack"))
	}
	if artifact.LevelBand != input.LevelBand {
		issues = append(issues, dimensionIssue("level_band"))
	}
	if artifact.Lesson.Minutes < input.Minutes/2 || artifact.Lesson.Minutes > input.Minutes*2 {
		issues = append(issues, ValidationIssue{
			Code:    "minutes_out_of_range",
			Field:   "lesson.minutes",
			Message: "lesson minutes drift too far from the requested task minutes",
		})
	}
	return issues
}

func dimensionIssue(field string) ValidationIssue {
	return ValidationIssue{
		Code:    "dimension_mismatch",
		Field:   field,
		Message: "artifact dimension must match generation input",
	}
}

func validatePrivacyLint(artifact contracts.ContentArtifact) []ValidationIssue {
	encoded, err := json.Marshal(artifact)
	if err != nil {
		return []ValidationIssue{{
			Code:    "artifact_json_invalid",
			Message: "artifact must be JSON serializable",
		}}
	}
	text := string(encoded)
	var issues []ValidationIssue
	for _, pattern := range privacyPatterns {
		if pattern.re.MatchString(text) {
			issues = append(issues, ValidationIssue{
				Code:    pattern.code,
				Message: pattern.message,
			})
		}
	}
	return issues
}

func validateUnsafeCode(field string, code string) []ValidationIssue {
	lower := strings.ToLower(code)
	var issues []ValidationIssue
	for _, pattern := range unsafeCodePatterns {
		if strings.Contains(lower, pattern.needle) {
			issues = append(issues, ValidationIssue{
				Code:    pattern.code,
				Field:   field,
				Message: pattern.message,
			})
		}
	}
	return issues
}

func normalizeCode(code string) string {
	return strings.Join(strings.Fields(code), " ")
}

var privacyPatterns = []struct {
	code    string
	re      *regexp.Regexp
	message string
}{
	{
		code:    "email_detected",
		re:      regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
		message: "artifact must not contain email addresses",
	},
	{
		code:    "api_key_detected",
		re:      regexp.MustCompile(`(?i)\b(sk|api[_-]?key|secret)[_-]?[A-Za-z0-9]{12,}\b`),
		message: "artifact must not contain API keys or secrets",
	},
	{
		code:    "user_identifier_detected",
		re:      regexp.MustCompile(`(?i)\b(user_id|tenant_id|local-user)\b`),
		message: "shared artifact must not contain user or tenant identifiers",
	},
}

var unsafeCodePatterns = []struct {
	code    string
	needle  string
	message string
}{
	{"network_call", "fetch(", "exercise code must not perform network calls"},
	{"network_call", "xmlhttprequest", "exercise code must not perform network calls"},
	{"filesystem_access", "require('fs", "exercise code must not access the filesystem"},
	{"filesystem_access", "require(\"fs", "exercise code must not access the filesystem"},
	{"process_access", "process.env", "exercise code must not read process environment"},
	{"process_access", "child_process", "exercise code must not spawn child processes"},
}
