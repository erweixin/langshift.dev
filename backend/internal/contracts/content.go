package contracts

import "time"

type ContentArtifact struct {
	SchemaVersion  int                  `json:"schema_version"`
	ContentKey     string               `json:"content_key"`
	TaskTemplateID string               `json:"task_template_id"`
	TargetStack    string               `json:"target_stack"`
	LevelBand      string               `json:"level_band"`
	ContentVersion int                  `json:"content_version"`
	PromptVersion  int                  `json:"prompt_version"`
	Lesson         Lesson               `json:"lesson"`
	Exercise       Exercise             `json:"exercise"`
	Meta           *ContentArtifactMeta `json:"meta,omitempty"`
}

type PublicContentArtifact struct {
	SchemaVersion      int                  `json:"schema_version"`
	ContentKey         string               `json:"content_key"`
	TaskTemplateID     string               `json:"task_template_id"`
	TargetStack        string               `json:"target_stack"`
	LevelBand          string               `json:"level_band"`
	ContentVersion     int                  `json:"content_version"`
	PromptVersion      int                  `json:"prompt_version"`
	ReviewStatus       string               `json:"review_status"`
	ValidationAttempts int                  `json:"validation_attempts"`
	Lesson             Lesson               `json:"lesson"`
	Exercise           PublicExercise       `json:"exercise"`
	Meta               *ContentArtifactMeta `json:"meta,omitempty"`
}

type Lesson struct {
	Title     string          `json:"title"`
	Minutes   int             `json:"minutes"`
	Judge     string          `json:"judge"`
	Why       string          `json:"why,omitempty"`
	Sections  []LessonSection `json:"sections"`
	CoachNote string          `json:"coach_note,omitempty"`
}

type LessonSection struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	BodyMD   string    `json:"body_md"`
	Runnable *Runnable `json:"runnable,omitempty"`
}

type Runnable struct {
	Kind     string `json:"kind"`
	DemoCode string `json:"demo_code,omitempty"`
}

type Exercise struct {
	Language          string         `json:"language"`
	StarterCode       string         `json:"starter_code"`
	ReferenceSolution string         `json:"reference_solution"`
	HarnessVersion    int            `json:"harness_version"`
	Tests             []ExerciseTest `json:"tests"`
}

type PublicExercise struct {
	Language       string         `json:"language"`
	StarterCode    string         `json:"starter_code"`
	HarnessVersion int            `json:"harness_version"`
	Tests          []ExerciseTest `json:"tests"`
}

type ExerciseTest struct {
	ID     string `json:"id"`
	Call   string `json:"call"`
	Expect any    `json:"expect"`
	Judge  bool   `json:"judge"`
	Label  string `json:"label"`
}

type ContentArtifactMeta struct {
	CreatedAt          time.Time `json:"created_at,omitempty"`
	Model              string    `json:"model,omitempty"`
	TokIn              int       `json:"tok_in,omitempty"`
	TokOut             int       `json:"tok_out,omitempty"`
	ValidationAttempts int       `json:"validation_attempts,omitempty"`
}
