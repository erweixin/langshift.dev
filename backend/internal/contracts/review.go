package contracts

type ReviewOutput struct {
	Did           []string        `json:"did"`
	Fix           string          `json:"fix"`
	Next          string          `json:"next"`
	CapDelta      CapabilityDelta `json:"cap_delta"`
	Misconception *Misconception  `json:"misconception,omitempty"`
	Memo          string          `json:"memo"`
	NextTaskSeed  NextTaskSeed    `json:"next_task_seed"`
}

type ReviewCompleted struct {
	EvidenceID string `json:"evidence_id"`
	ReviewOutput
}

type CapabilityDelta struct {
	CapabilityID string `json:"capability_id"`
	From         int    `json:"from"`
	To           int    `json:"to"`
	Label        string `json:"label"`
}

type Misconception struct {
	Statement        string `json:"statement"`
	PlannedProbeHint string `json:"planned_probe_hint,omitempty"`
}

type NextTaskSeed struct {
	Title   string `json:"title"`
	Minutes int    `json:"minutes"`
}
