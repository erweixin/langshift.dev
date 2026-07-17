// Package review defines the strict immutable Evaluator output contract.
package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

var ErrInvalid = errors.New("review document is invalid")

type Check struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}
type DeterministicResults struct {
	Checks []Check `json:"checks"`
}
type Dimension struct {
	ID             string   `json:"id"`
	Score          int      `json:"score"`
	Rationale      string   `json:"rationale"`
	EvidenceQuotes []string `json:"evidence_quotes"`
}
type CapabilityEvidence struct {
	CapabilityID string `json:"capability_id"`
	Level        string `json:"level"`
	Statement    string `json:"statement"`
}
type Document struct {
	SchemaVersion        int                  `json:"schema_version"`
	Verdict              string               `json:"verdict"`
	Summary              string               `json:"summary"`
	DeterministicResults DeterministicResults `json:"deterministic_results"`
	Dimensions           []Dimension          `json:"dimensions"`
	Strengths            []string             `json:"strengths"`
	Improvements         []string             `json:"improvements"`
	CapabilityEvidence   []CapabilityEvidence `json:"capability_evidence"`
	NextAction           string               `json:"next_action"`
	Uncertainty          string               `json:"uncertainty"`
}

func Parse(encoded []byte) (Document, error) {
	if len(encoded) == 0 || len(encoded) > 256<<10 {
		return Document{}, ErrInvalid
	}
	var d Document
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.DisallowUnknownFields()
	if dec.Decode(&d) != nil || !errors.Is(dec.Decode(&struct{}{}), io.EOF) || !d.valid() {
		return Document{}, ErrInvalid
	}
	return d, nil
}
func (d Document) valid() bool {
	if d.SchemaVersion != 1 || d.Verdict != "pass" && d.Verdict != "needs_revision" || !text(d.Summary, 1, 4000) || !text(d.NextAction, 1, 2000) || !text(d.Uncertainty, 1, 2000) || len(d.DeterministicResults.Checks) > 64 || len(d.Dimensions) < 1 || len(d.Dimensions) > 32 || len(d.Strengths) > 32 || len(d.Improvements) > 32 || len(d.CapabilityEvidence) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, c := range d.DeterministicResults.Checks {
		if !text(c.Name, 1, 200) || c.Status != "pass" && c.Status != "fail" && c.Status != "not_applicable" || !text(c.Evidence, 1, 2000) {
			return false
		}
	}
	for _, v := range d.Dimensions {
		if !text(v.ID, 1, 200) || seen[v.ID] || v.Score < 0 || v.Score > 100 || !text(v.Rationale, 1, 2000) || len(v.EvidenceQuotes) > 16 {
			return false
		}
		seen[v.ID] = true
		for _, q := range v.EvidenceQuotes {
			if !text(q, 1, 1000) {
				return false
			}
		}
	}
	for _, items := range [][]string{d.Strengths, d.Improvements} {
		for _, v := range items {
			if !text(v, 1, 1000) {
				return false
			}
		}
	}
	for _, v := range d.CapabilityEvidence {
		if !text(v.CapabilityID, 1, 200) || v.Level != "demonstrated" && v.Level != "applied" && v.Level != "reviewer_verified" || !text(v.Statement, 1, 2000) {
			return false
		}
	}
	return true
}
func text(v string, min, max int) bool {
	return utf8.ValidString(v) && utf8.RuneCountInString(v) >= min && utf8.RuneCountInString(v) <= max
}
