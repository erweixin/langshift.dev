package route

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"unicode/utf8"
)

var ErrInvalidDocument = errors.New("route document is invalid")

var documentIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

// Document is the model-produced, user-visible RouteRevision payload. The
// schema is deliberately closed: accepting arbitrary model JSON would make
// persisted routes impossible to migrate, render, or evaluate safely.
type Document struct {
	SchemaVersion          int                  `json:"schema_version"`
	Summary                string               `json:"summary"`
	TransferableExperience []GroundedAssessment `json:"transferable_experience"`
	Gaps                   []GroundedAssessment `json:"gaps"`
	Bridge                 []BridgeStep         `json:"bridge"`
	Stages                 []Stage              `json:"stages"`
	FirstTask              FirstTask            `json:"first_task"`
}

type GroundedAssessment struct {
	Statement     string   `json:"statement"`
	CapabilityIDs []string `json:"capability_ids"`
	EvidenceIDs   []string `json:"evidence_ids"`
	Confidence    string   `json:"confidence"`
}

type BridgeStep struct {
	ID                string   `json:"id"`
	Title             string   `json:"title"`
	Rationale         string   `json:"rationale"`
	FromCapabilityIDs []string `json:"from_capability_ids"`
	ToCapabilityIDs   []string `json:"to_capability_ids"`
}

type Stage struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Outcome          string   `json:"outcome"`
	CapabilityIDs    []string `json:"capability_ids"`
	EvidenceRequired []string `json:"evidence_required"`
}

type FirstTask struct {
	Title            string   `json:"title"`
	Objective        string   `json:"objective"`
	EstimatedMinutes int      `json:"estimated_minutes"`
	Difficulty       string   `json:"difficulty"`
	CapabilityIDs    []string `json:"capability_ids"`
	SuccessCriteria  []string `json:"success_criteria"`
}

func DecodeDocument(encoded []byte) (Document, error) {
	if len(encoded) == 0 || len(encoded) > 1<<20 {
		return Document{}, ErrInvalidDocument
	}
	var document Document
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || !document.Valid() {
		return Document{}, ErrInvalidDocument
	}
	return document, nil
}

func (document Document) Valid() bool {
	if document.SchemaVersion != 1 || !bounded(document.Summary, 1, 4000) || len(document.TransferableExperience) > 32 || len(document.Gaps) > 32 || len(document.Bridge) < 1 || len(document.Bridge) > 16 || len(document.Stages) < 2 || len(document.Stages) > 12 || !document.FirstTask.valid() {
		return false
	}
	seenBridge, seenStages := map[string]bool{}, map[string]bool{}
	for _, item := range document.TransferableExperience {
		if !item.valid() {
			return false
		}
	}
	for _, item := range document.Gaps {
		if !item.valid() {
			return false
		}
	}
	for _, item := range document.Bridge {
		if !item.valid() || seenBridge[item.ID] {
			return false
		}
		seenBridge[item.ID] = true
	}
	for _, item := range document.Stages {
		if !item.valid() || seenStages[item.ID] {
			return false
		}
		seenStages[item.ID] = true
	}
	return true
}

func (item GroundedAssessment) valid() bool {
	return bounded(item.Statement, 1, 2000) && len(item.CapabilityIDs) <= 64 && len(item.EvidenceIDs) <= 64 && uniqueNonempty(item.CapabilityIDs, 128) && uniqueNonempty(item.EvidenceIDs, 128) && (item.Confidence == "inferred" || item.Confidence == "supported" || item.Confidence == "verified")
}

func (item BridgeStep) valid() bool {
	return documentIDPattern.MatchString(item.ID) && bounded(item.Title, 1, 200) && bounded(item.Rationale, 1, 2000) && len(item.FromCapabilityIDs) <= 64 && len(item.ToCapabilityIDs) > 0 && len(item.ToCapabilityIDs) <= 64 && uniqueNonempty(item.FromCapabilityIDs, 128) && uniqueNonempty(item.ToCapabilityIDs, 128)
}

func (item Stage) valid() bool {
	return documentIDPattern.MatchString(item.ID) && bounded(item.Title, 1, 200) && bounded(item.Outcome, 1, 2000) && len(item.CapabilityIDs) > 0 && len(item.CapabilityIDs) <= 64 && len(item.EvidenceRequired) > 0 && len(item.EvidenceRequired) <= 32 && uniqueNonempty(item.CapabilityIDs, 128) && uniqueNonempty(item.EvidenceRequired, 500)
}

func (task FirstTask) valid() bool {
	return bounded(task.Title, 1, 200) && bounded(task.Objective, 1, 2000) && task.EstimatedMinutes >= 5 && task.EstimatedMinutes <= 480 && (task.Difficulty == "easy" || task.Difficulty == "standard" || task.Difficulty == "stretch") && len(task.CapabilityIDs) > 0 && len(task.CapabilityIDs) <= 32 && len(task.SuccessCriteria) > 0 && len(task.SuccessCriteria) <= 16 && uniqueNonempty(task.CapabilityIDs, 128) && uniqueNonempty(task.SuccessCriteria, 500)
}

func bounded(value string, minimum, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) >= minimum && utf8.RuneCountInString(value) <= maximum
}

func uniqueNonempty(values []string, maximumRunes int) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !bounded(value, 1, maximumRunes) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
