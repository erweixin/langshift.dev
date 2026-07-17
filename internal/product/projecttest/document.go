// Package projecttest defines the closed, immutable output contract for a
// trusted Project Evaluator run.
package projecttest

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

var ErrInvalid = errors.New("project test evaluator document is invalid")

type Check struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}

type Document struct {
	SchemaVersion int     `json:"schema_version"`
	Result        string  `json:"result"`
	Summary       string  `json:"summary"`
	Checks        []Check `json:"checks"`
	Uncertainty   string  `json:"uncertainty"`
}

func Parse(encoded []byte) (Document, error) {
	if len(encoded) == 0 || len(encoded) > 256<<10 {
		return Document{}, ErrInvalid
	}
	var document Document
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) || !document.valid() {
		return Document{}, ErrInvalid
	}
	return document, nil
}

func (document Document) valid() bool {
	if document.SchemaVersion != 1 || document.Result != "passed" && document.Result != "failed" || !validText(document.Summary, 1, 4000) || !validText(document.Uncertainty, 1, 2000) || len(document.Checks) < 1 || len(document.Checks) > 64 {
		return false
	}
	seen := map[string]bool{}
	allPassed := true
	for _, check := range document.Checks {
		if !validText(check.Name, 1, 200) || seen[check.Name] || check.Status != "pass" && check.Status != "fail" || !validText(check.Evidence, 1, 4000) {
			return false
		}
		seen[check.Name] = true
		allPassed = allPassed && check.Status == "pass"
	}
	// An Evaluator cannot issue a passing aggregate while any exact check is
	// failed. Failed is allowed for an all-pass check set when the evaluator's
	// uncertainty is material and it deliberately chooses the safe outcome.
	return document.Result != "passed" || allPassed
}

func validText(value string, minimum, maximum int) bool {
	count := utf8.RuneCountInString(value)
	return utf8.ValidString(value) && count >= minimum && count <= maximum
}
