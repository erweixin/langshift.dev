package contracts

type ExerciseResult struct {
	Status         string         `json:"status"`
	Cases          []ExerciseCase `json:"cases"`
	JudgeHit       bool           `json:"judge_hit"`
	Logs           []string       `json:"logs,omitempty"`
	DurationMS     int            `json:"duration_ms"`
	CodeHash       string         `json:"code_hash"`
	HarnessVersion int            `json:"harness_version"`
	Runtime        string         `json:"runtime,omitempty"`
}

type ExerciseCase struct {
	ID     string `json:"id"`
	Pass   bool   `json:"pass"`
	Actual string `json:"actual,omitempty"`
	Error  string `json:"error,omitempty"`
}
