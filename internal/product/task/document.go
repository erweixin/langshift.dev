package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"unicode/utf8"
)

var (
	ErrDocument = errors.New("daily task document is invalid")
	idPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,127}$`)
)

type Document struct {
	SchemaVersion    int               `json:"schema_version"`
	Title            string            `json:"title"`
	Objective        string            `json:"objective"`
	WhyThisTask      string            `json:"why_this_task"`
	KeyJudgment      string            `json:"key_judgment"`
	EstimatedMinutes int               `json:"estimated_minutes"`
	Difficulty       string            `json:"difficulty"`
	PracticeKind     string            `json:"practice_kind"`
	Explanation      []ExplanationStep `json:"explanation"`
	Example          string            `json:"example"`
	Practice         Practice          `json:"practice"`
	CapabilityIDs    []string          `json:"capability_ids"`
	EvidenceTargets  []string          `json:"evidence_targets"`
	NextTaskHint     string            `json:"next_task_hint"`
}

type ExplanationStep struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type Practice struct {
	Instructions        string   `json:"instructions"`
	StarterContent      string   `json:"starter_content"`
	DeterministicChecks []string `json:"deterministic_checks"`
	SuccessCriteria     []string `json:"success_criteria"`
}

func ParseDocument(encoded []byte) (Document, error) {
	if len(encoded) == 0 || len(encoded) > 256<<10 || !utf8.Valid(encoded) {
		return Document{}, ErrDocument
	}
	var document Document
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || !document.valid() {
		return Document{}, ErrDocument
	}
	return document, nil
}

func (document Document) valid() bool {
	if document.SchemaVersion != 1 || !bounded(document.Title, 1, 160) || !bounded(document.Objective, 1, 2000) || !bounded(document.WhyThisTask, 1, 2000) || !bounded(document.KeyJudgment, 1, 1000) || document.EstimatedMinutes < 5 || document.EstimatedMinutes > 480 || document.Difficulty != "easier" && document.Difficulty != "standard" && document.Difficulty != "harder" || document.PracticeKind != "code" && document.PracticeKind != "writing" && document.PracticeKind != "design" || len(document.Explanation) < 1 || len(document.Explanation) > 8 || !bounded(document.Example, 1, 12000) || !bounded(document.Practice.Instructions, 1, 8000) || len(document.Practice.StarterContent) > 100000 || len(document.Practice.DeterministicChecks) < 1 || len(document.Practice.DeterministicChecks) > 32 || len(document.Practice.SuccessCriteria) < 1 || len(document.Practice.SuccessCriteria) > 16 || len(document.CapabilityIDs) < 1 || len(document.CapabilityIDs) > 32 || len(document.EvidenceTargets) < 1 || len(document.EvidenceTargets) > 16 || !bounded(document.NextTaskHint, 1, 1000) {
		return false
	}
	for _, step := range document.Explanation {
		if !bounded(step.Title, 1, 160) || !bounded(step.Content, 1, 8000) {
			return false
		}
	}
	if !uniqueIDs(document.CapabilityIDs) {
		return false
	}
	for _, values := range [][]string{document.Practice.DeterministicChecks, document.Practice.SuccessCriteria, document.EvidenceTargets} {
		seen := map[string]struct{}{}
		for _, value := range values {
			if !bounded(value, 1, 1000) {
				return false
			}
			if _, exists := seen[value]; exists {
				return false
			}
			seen[value] = struct{}{}
		}
	}
	return true
}

func uniqueIDs(values []string) bool {
	seen := map[string]struct{}{}
	for _, value := range values {
		if !idPattern.MatchString(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func bounded(value string, minimum, maximum int) bool {
	size := utf8.RuneCountInString(value)
	return size >= minimum && size <= maximum
}
