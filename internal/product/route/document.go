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

// ErrUngroundedDocument means that a shape-valid model response refers to a
// capability or evidence item outside the immutable planner input, or claims a
// stronger confidence level than that input can justify.
var ErrUngroundedDocument = errors.New("route document is not grounded in the planner input")

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

// GroundingInput is the deterministic, model-independent policy input used
// before a route is persisted. It deliberately contains only immutable IDs and
// review state; prompt wording cannot weaken these checks.
type GroundingInput struct {
	Claims              []GroundingClaim
	Evidence            []GroundingEvidence
	TargetCapabilityIDs []string
}

type GroundingClaim struct {
	CapabilityID          string
	Status                string
	VerificationLevel     string
	SupportingEvidenceIDs []string
}

type GroundingEvidence struct {
	ID          string
	Status      string
	Invalidated bool
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

// ValidateGrounding enforces the boundary between model inference and durable
// product truth. A route may plan toward target-role requirements, but it may
// only describe a capability as supported or verified when the current active
// claim revision links the cited, non-invalidated evidence.
func ValidateGrounding(document Document, input GroundingInput) error {
	allowedCapabilities := make(map[string]struct{}, len(input.Claims)+len(input.TargetCapabilityIDs))
	claims := make(map[string]GroundingClaim, len(input.Claims))
	for _, claim := range input.Claims {
		if claim.Status != "active" || claim.CapabilityID == "" {
			continue
		}
		allowedCapabilities[claim.CapabilityID] = struct{}{}
		claims[claim.CapabilityID] = claim
	}
	for _, capabilityID := range input.TargetCapabilityIDs {
		if capabilityID == "" {
			return ErrUngroundedDocument
		}
		allowedCapabilities[capabilityID] = struct{}{}
	}
	evidence := make(map[string]GroundingEvidence, len(input.Evidence))
	for _, item := range input.Evidence {
		if item.ID != "" && !item.Invalidated && (item.Status == "recorded" || item.Status == "verified") {
			evidence[item.ID] = item
		}
	}
	if len(allowedCapabilities) == 0 {
		return ErrUngroundedDocument
	}

	for _, assessment := range append(append([]GroundedAssessment(nil), document.TransferableExperience...), document.Gaps...) {
		if len(assessment.CapabilityIDs) == 0 || !capabilitiesAllowed(assessment.CapabilityIDs, allowedCapabilities) {
			return ErrUngroundedDocument
		}
		for _, evidenceID := range assessment.EvidenceIDs {
			if _, ok := evidence[evidenceID]; !ok {
				return ErrUngroundedDocument
			}
		}
		if assessment.Confidence == "inferred" {
			continue
		}
		if len(assessment.EvidenceIDs) == 0 {
			return ErrUngroundedDocument
		}
		for _, capabilityID := range assessment.CapabilityIDs {
			claim, ok := claims[capabilityID]
			if !ok || !allEvidenceLinked(assessment.EvidenceIDs, claim.SupportingEvidenceIDs) {
				return ErrUngroundedDocument
			}
			if assessment.Confidence == "verified" && !verifiedClaimLevel(claim.VerificationLevel) {
				return ErrUngroundedDocument
			}
		}
		if assessment.Confidence == "verified" {
			for _, evidenceID := range assessment.EvidenceIDs {
				if evidence[evidenceID].Status != "verified" {
					return ErrUngroundedDocument
				}
			}
		}
	}
	for _, step := range document.Bridge {
		if !capabilitiesAllowed(step.FromCapabilityIDs, allowedCapabilities) || !capabilitiesAllowed(step.ToCapabilityIDs, allowedCapabilities) {
			return ErrUngroundedDocument
		}
	}
	for _, stage := range document.Stages {
		if !capabilitiesAllowed(stage.CapabilityIDs, allowedCapabilities) {
			return ErrUngroundedDocument
		}
	}
	if !capabilitiesAllowed(document.FirstTask.CapabilityIDs, allowedCapabilities) {
		return ErrUngroundedDocument
	}
	return nil
}

func capabilitiesAllowed(capabilityIDs []string, allowed map[string]struct{}) bool {
	for _, capabilityID := range capabilityIDs {
		if _, ok := allowed[capabilityID]; !ok {
			return false
		}
	}
	return true
}

func allEvidenceLinked(cited, linked []string) bool {
	index := make(map[string]struct{}, len(linked))
	for _, evidenceID := range linked {
		index[evidenceID] = struct{}{}
	}
	for _, evidenceID := range cited {
		if _, ok := index[evidenceID]; !ok {
			return false
		}
	}
	return true
}

func verifiedClaimLevel(level string) bool {
	return level == "demonstrated" || level == "applied" || level == "reviewer_verified"
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
