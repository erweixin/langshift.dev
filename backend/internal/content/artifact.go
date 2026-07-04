package content

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"lites/backend/internal/contracts"
	"lites/backend/internal/llm"
)

func buildGeneratedArtifact(input GenerationInput, response llm.Response, now time.Time) (contracts.ContentArtifact, error) {
	var artifact contracts.ContentArtifact
	raw := []byte(strings.TrimSpace(response.Content))
	if len(raw) == 0 {
		return contracts.ContentArtifact{}, fmt.Errorf("%w: response content is empty", ErrInvalidArtifact)
	}
	if err := json.Unmarshal(raw, &artifact); err != nil {
		return contracts.ContentArtifact{}, fmt.Errorf("%w: response content is not valid artifact json: %v", ErrInvalidArtifact, err)
	}

	applyArtifactCompatibility(raw, input, &artifact)
	artifact.SchemaVersion = contentArtifactSchemaVersion
	artifact.TaskTemplateID = input.TaskTemplateID
	artifact.TargetStack = input.TargetStack
	artifact.LevelBand = input.LevelBand
	artifact.ContentVersion = contentArtifactVersion
	artifact.PromptVersion = contentArtifactPromptVersion
	artifact.ContentKey = contentKey(input)

	artifact.Lesson.Title = firstNonEmptyString(artifact.Lesson.Title, input.Title)
	artifact.Lesson.Judge = firstNonEmptyString(artifact.Lesson.Judge, input.Judge)
	if artifact.Lesson.Minutes == 0 {
		artifact.Lesson.Minutes = input.Minutes
	}
	for i := range artifact.Lesson.Sections {
		if strings.TrimSpace(artifact.Lesson.Sections[i].ID) == "" {
			artifact.Lesson.Sections[i].ID = fmt.Sprintf("section_%d", i+1)
		}
	}

	if strings.TrimSpace(artifact.Exercise.Language) == "" {
		artifact.Exercise.Language = "javascript"
	}
	if artifact.Exercise.HarnessVersion == 0 {
		artifact.Exercise.HarnessVersion = 1
	}
	for i := range artifact.Exercise.Tests {
		if strings.TrimSpace(artifact.Exercise.Tests[i].ID) == "" {
			artifact.Exercise.Tests[i].ID = fmt.Sprintf("case_%d", i+1)
		}
		if strings.TrimSpace(artifact.Exercise.Tests[i].Label) == "" {
			artifact.Exercise.Tests[i].Label = artifact.Exercise.Tests[i].ID
		}
	}

	if artifact.Meta == nil {
		artifact.Meta = &contracts.ContentArtifactMeta{}
	}
	if artifact.Meta.CreatedAt.IsZero() {
		artifact.Meta.CreatedAt = now.UTC()
	}
	artifact.Meta.TokIn = response.Usage.InputTokens
	artifact.Meta.TokOut = response.Usage.OutputTokens

	if err := validateArtifact(artifact); err != nil {
		return contracts.ContentArtifact{}, err
	}
	return artifact, nil
}

func validateArtifact(artifact contracts.ContentArtifact) error {
	switch {
	case artifact.SchemaVersion != contentArtifactSchemaVersion:
		return fmt.Errorf("%w: schema_version must be %d", ErrInvalidArtifact, contentArtifactSchemaVersion)
	case strings.TrimSpace(artifact.ContentKey) == "":
		return fmt.Errorf("%w: content_key is required", ErrInvalidArtifact)
	case strings.TrimSpace(artifact.TaskTemplateID) == "":
		return fmt.Errorf("%w: task_template_id is required", ErrInvalidArtifact)
	case strings.TrimSpace(artifact.TargetStack) == "":
		return fmt.Errorf("%w: target_stack is required", ErrInvalidArtifact)
	case strings.TrimSpace(artifact.LevelBand) == "":
		return fmt.Errorf("%w: level_band is required", ErrInvalidArtifact)
	case artifact.ContentVersion <= 0:
		return fmt.Errorf("%w: content_version must be positive", ErrInvalidArtifact)
	case artifact.PromptVersion <= 0:
		return fmt.Errorf("%w: prompt_version must be positive", ErrInvalidArtifact)
	case strings.TrimSpace(artifact.Lesson.Title) == "":
		return fmt.Errorf("%w: lesson.title is required", ErrInvalidArtifact)
	case artifact.Lesson.Minutes <= 0:
		return fmt.Errorf("%w: lesson.minutes must be positive", ErrInvalidArtifact)
	case strings.TrimSpace(artifact.Lesson.Judge) == "":
		return fmt.Errorf("%w: lesson.judge is required", ErrInvalidArtifact)
	case len(artifact.Lesson.Sections) == 0:
		return fmt.Errorf("%w: lesson.sections is required", ErrInvalidArtifact)
	case strings.TrimSpace(artifact.Exercise.Language) == "":
		return fmt.Errorf("%w: exercise.language is required", ErrInvalidArtifact)
	case strings.TrimSpace(artifact.Exercise.StarterCode) == "":
		return fmt.Errorf("%w: exercise.starter_code is required", ErrInvalidArtifact)
	case strings.TrimSpace(artifact.Exercise.ReferenceSolution) == "":
		return fmt.Errorf("%w: exercise.reference_solution is required", ErrInvalidArtifact)
	case artifact.Exercise.HarnessVersion <= 0:
		return fmt.Errorf("%w: exercise.harness_version must be positive", ErrInvalidArtifact)
	case len(artifact.Exercise.Tests) == 0:
		return fmt.Errorf("%w: exercise.tests is required", ErrInvalidArtifact)
	}
	for _, section := range artifact.Lesson.Sections {
		if strings.TrimSpace(section.ID) == "" || strings.TrimSpace(section.Title) == "" || strings.TrimSpace(section.BodyMD) == "" {
			return fmt.Errorf("%w: each lesson section requires id, title, and body_md", ErrInvalidArtifact)
		}
	}
	for _, test := range artifact.Exercise.Tests {
		if strings.TrimSpace(test.ID) == "" || strings.TrimSpace(test.Call) == "" || strings.TrimSpace(test.Label) == "" {
			return fmt.Errorf("%w: each exercise test requires id, call, and label", ErrInvalidArtifact)
		}
	}
	return nil
}

func PublicArtifact(record ArtifactRecord) (map[string]any, error) {
	encoded, err := json.Marshal(record.Artifact)
	if err != nil {
		return nil, fmt.Errorf("marshal public content artifact: %w", err)
	}
	var public map[string]any
	if err := json.Unmarshal(encoded, &public); err != nil {
		return nil, fmt.Errorf("unmarshal public content artifact: %w", err)
	}
	exercise, ok := public["exercise"].(map[string]any)
	if ok {
		delete(exercise, "reference_solution")
	}
	public["review_status"] = record.ReviewStatus
	public["validation_attempts"] = record.ValidationAttempts
	return public, nil
}

func applyArtifactCompatibility(raw []byte, input GenerationInput, artifact *contracts.ContentArtifact) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return
	}

	if artifact.Lesson.Title == "" {
		artifact.Lesson.Title = firstNonEmptyString(readString(envelope["title"]), input.Title)
	}
	if artifact.Lesson.Judge == "" {
		artifact.Lesson.Judge = firstNonEmptyString(readString(envelope["judge"]), input.Judge)
	}
	if artifact.Lesson.Minutes == 0 {
		artifact.Lesson.Minutes = input.Minutes
	}
	if len(artifact.Lesson.Sections) == 0 {
		artifact.Lesson.Sections = legacyLessonSections(envelope)
	}
	if len(artifact.Lesson.Sections) == 0 {
		if description := readString(envelope["description"]); description != "" {
			artifact.Lesson.Sections = []contracts.LessonSection{{
				ID:     "overview",
				Title:  "Overview",
				BodyMD: description,
			}}
		}
	}

	legacyExercise(envelope, artifact)
}

func legacyLessonSections(envelope map[string]json.RawMessage) []contracts.LessonSection {
	raw := envelope["lesson_sections"]
	if len(raw) == 0 {
		raw = envelope["sections"]
	}
	if len(raw) == 0 {
		return nil
	}

	var sections []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sections); err != nil {
		return nil
	}
	result := make([]contracts.LessonSection, 0, len(sections))
	for i, section := range sections {
		title := firstNonEmptyString(readString(section["title"]), readString(section["heading"]))
		body := firstNonEmptyString(readString(section["body_md"]), readString(section["body"]), readString(section["content"]))
		if title == "" && body == "" {
			continue
		}
		result = append(result, contracts.LessonSection{
			ID:     fmt.Sprintf("section_%d", i+1),
			Title:  firstNonEmptyString(title, fmt.Sprintf("Section %d", i+1)),
			BodyMD: body,
		})
	}
	return result
}

func legacyExercise(envelope map[string]json.RawMessage, artifact *contracts.ContentArtifact) {
	raw := envelope["exercise"]
	if len(raw) == 0 {
		return
	}
	var exercise map[string]json.RawMessage
	if err := json.Unmarshal(raw, &exercise); err != nil {
		return
	}
	if artifact.Exercise.Language == "" {
		artifact.Exercise.Language = firstNonEmptyString(readString(exercise["language"]), "javascript")
	}
	if artifact.Exercise.StarterCode == "" {
		artifact.Exercise.StarterCode = readString(exercise["starter_code"])
	}
	if artifact.Exercise.ReferenceSolution == "" {
		artifact.Exercise.ReferenceSolution = readString(exercise["reference_solution"])
	}
	if artifact.Exercise.HarnessVersion == 0 {
		artifact.Exercise.HarnessVersion = 1
	}
	if len(artifact.Exercise.Tests) == 0 {
		artifact.Exercise.Tests = legacyExerciseTests(exercise["tests"])
	}
}

func legacyExerciseTests(raw json.RawMessage) []contracts.ExerciseTest {
	if len(raw) == 0 {
		return nil
	}
	var tests []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tests); err != nil {
		return nil
	}
	result := make([]contracts.ExerciseTest, 0, len(tests))
	for i, test := range tests {
		label := firstNonEmptyString(readString(test["label"]), readString(test["description"]), fmt.Sprintf("Case %d", i+1))
		call := firstNonEmptyString(readString(test["call"]), readRawString(test["input"]))
		expect := readAny(test["expect"])
		if expect == nil {
			expect = readAny(test["expected"])
		}
		result = append(result, contracts.ExerciseTest{
			ID:     firstNonEmptyString(readString(test["id"]), fmt.Sprintf("case_%d", i+1)),
			Call:   call,
			Expect: expect,
			Judge:  readBool(test["judge"], i == 0),
			Label:  label,
		})
	}
	return result
}

func readString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return strings.TrimSpace(value)
	}
	return ""
}

func readRawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return strings.TrimSpace(value)
	}
	if json.Valid(raw) {
		return string(raw)
	}
	return ""
}

func readAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

func readBool(raw json.RawMessage, fallback bool) bool {
	if len(raw) == 0 {
		return fallback
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return fallback
	}
	return value
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
