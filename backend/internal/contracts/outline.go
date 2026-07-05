package contracts

import "time"

type LearningOutline struct {
	SchemaVersion int                  `json:"schema_version"`
	OutlineID     string               `json:"outline_id"`
	Goal          string               `json:"goal"`
	TargetRole    string               `json:"target_role"`
	TargetStack   string               `json:"target_stack"`
	LevelBand     string               `json:"level_band"`
	DurationDays  int                  `json:"duration_days"`
	DailyMinutes  int                  `json:"daily_minutes"`
	Summary       string               `json:"summary"`
	Stages        []OutlineStage       `json:"stages"`
	Tasks         []OutlineTaskSpec    `json:"tasks"`
	Meta          *LearningOutlineMeta `json:"meta,omitempty"`
}

type OutlineStage struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Goal  string `json:"goal"`
	Days  []int  `json:"days"`
}

type OutlineTaskSpec struct {
	DayIndex       int    `json:"day_index"`
	TaskTemplateID string `json:"task_template_id"`
	TargetStack    string `json:"target_stack"`
	LevelBand      string `json:"level_band"`
	Title          string `json:"title"`
	Judge          string `json:"judge"`
	Minutes        int    `json:"minutes"`
}

type LearningOutlineMeta struct {
	CreatedAt time.Time `json:"created_at,omitempty"`
	Model     string    `json:"model,omitempty"`
	TokIn     int       `json:"tok_in,omitempty"`
	TokOut    int       `json:"tok_out,omitempty"`
}
