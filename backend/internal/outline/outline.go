package outline

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"lites/backend/internal/contracts"
	"lites/backend/internal/llm"
)

func buildGeneratedOutline(input GenerationInput, runID string, response llm.Response, now time.Time) (contracts.LearningOutline, error) {
	var generated contracts.LearningOutline
	raw := []byte(strings.TrimSpace(response.Content))
	if len(raw) == 0 {
		return contracts.LearningOutline{}, fmt.Errorf("%w: response content is empty", ErrInvalidOutline)
	}
	if err := json.Unmarshal(raw, &generated); err != nil {
		return contracts.LearningOutline{}, fmt.Errorf("%w: response content is not valid outline json: %v", ErrInvalidOutline, err)
	}

	generated.SchemaVersion = learningOutlineSchemaVersion
	generated.OutlineID = outlineID(runID)
	generated.Goal = firstNonEmptyString(generated.Goal, input.Goal)
	generated.TargetRole = firstNonEmptyString(generated.TargetRole, input.TargetRole)
	generated.TargetStack = firstNonEmptyString(generated.TargetStack, input.TargetStack)
	generated.LevelBand = firstNonEmptyString(generated.LevelBand, levelBand(input.CurrentLevel))
	if generated.DurationDays == 0 {
		generated.DurationDays = input.DurationDays
	}
	if generated.DailyMinutes == 0 {
		generated.DailyMinutes = input.DailyMinutes
	}
	for i := range generated.Stages {
		if strings.TrimSpace(generated.Stages[i].ID) == "" {
			generated.Stages[i].ID = fmt.Sprintf("stage_%d", i+1)
		}
	}
	for i := range generated.Tasks {
		if generated.Tasks[i].DayIndex == 0 {
			generated.Tasks[i].DayIndex = i + 1
		}
		if strings.TrimSpace(generated.Tasks[i].TaskTemplateID) == "" {
			generated.Tasks[i].TaskTemplateID = fmt.Sprintf("generated-day-%02d", generated.Tasks[i].DayIndex)
		}
		if strings.TrimSpace(generated.Tasks[i].TargetStack) == "" {
			generated.Tasks[i].TargetStack = input.TargetStack
		}
		if strings.TrimSpace(generated.Tasks[i].LevelBand) == "" {
			generated.Tasks[i].LevelBand = generated.LevelBand
		}
		if generated.Tasks[i].Minutes == 0 {
			generated.Tasks[i].Minutes = input.DailyMinutes
		}
	}
	if generated.Meta == nil {
		generated.Meta = &contracts.LearningOutlineMeta{}
	}
	if generated.Meta.CreatedAt.IsZero() {
		generated.Meta.CreatedAt = now.UTC()
	}
	generated.Meta.TokIn = response.Usage.InputTokens
	generated.Meta.TokOut = response.Usage.OutputTokens

	if err := validateOutline(generated); err != nil {
		return contracts.LearningOutline{}, err
	}
	return generated, nil
}

func validateOutline(outline contracts.LearningOutline) error {
	switch {
	case outline.SchemaVersion != learningOutlineSchemaVersion:
		return fmt.Errorf("%w: schema_version must be %d", ErrInvalidOutline, learningOutlineSchemaVersion)
	case strings.TrimSpace(outline.OutlineID) == "":
		return fmt.Errorf("%w: outline_id is required", ErrInvalidOutline)
	case strings.TrimSpace(outline.Goal) == "":
		return fmt.Errorf("%w: goal is required", ErrInvalidOutline)
	case strings.TrimSpace(outline.TargetRole) == "":
		return fmt.Errorf("%w: target_role is required", ErrInvalidOutline)
	case strings.TrimSpace(outline.TargetStack) == "":
		return fmt.Errorf("%w: target_stack is required", ErrInvalidOutline)
	case strings.TrimSpace(outline.LevelBand) == "":
		return fmt.Errorf("%w: level_band is required", ErrInvalidOutline)
	case outline.DurationDays <= 0:
		return fmt.Errorf("%w: duration_days must be positive", ErrInvalidOutline)
	case outline.DailyMinutes <= 0:
		return fmt.Errorf("%w: daily_minutes must be positive", ErrInvalidOutline)
	case strings.TrimSpace(outline.Summary) == "":
		return fmt.Errorf("%w: summary is required", ErrInvalidOutline)
	case len(outline.Stages) == 0:
		return fmt.Errorf("%w: stages are required", ErrInvalidOutline)
	case len(outline.Tasks) == 0:
		return fmt.Errorf("%w: tasks are required", ErrInvalidOutline)
	}
	for _, stage := range outline.Stages {
		if strings.TrimSpace(stage.ID) == "" || strings.TrimSpace(stage.Title) == "" || strings.TrimSpace(stage.Goal) == "" || len(stage.Days) == 0 {
			return fmt.Errorf("%w: each stage requires id, title, goal, and days", ErrInvalidOutline)
		}
	}
	for _, task := range outline.Tasks {
		if task.DayIndex <= 0 || strings.TrimSpace(task.TaskTemplateID) == "" || strings.TrimSpace(task.TargetStack) == "" || strings.TrimSpace(task.LevelBand) == "" || strings.TrimSpace(task.Title) == "" || strings.TrimSpace(task.Judge) == "" || task.Minutes <= 0 {
			return fmt.Errorf("%w: each task requires day_index, dimensions, title, judge, and minutes", ErrInvalidOutline)
		}
	}
	return nil
}

func levelBand(currentLevel string) string {
	lower := strings.ToLower(strings.TrimSpace(currentLevel))
	switch {
	case strings.Contains(lower, "senior"), strings.Contains(lower, "advanced"), strings.Contains(lower, "高阶"):
		return "advanced"
	case strings.Contains(lower, "beginner"), strings.Contains(lower, "zero"), strings.Contains(lower, "入门"):
		return "beginner"
	default:
		return "default"
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
