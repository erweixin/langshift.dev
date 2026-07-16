// Package contentcatalog loads immutable, bilingual Stage 4 product content releases.
package contentcatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

var (
	ErrInvalid = errors.New("product content release is invalid")
	digestRE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const maxDocumentBytes = 32 << 20

var requiredFiles = []string{
	"career-ontology.json",
	"content-sources.json",
	"rubric-versions.json",
	"task-templates.json",
	"transition-templates.json",
}

type Localized struct {
	EN string `json:"en"`
	ZH string `json:"zh-CN"`
}

type ManifestFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Manifest struct {
	ManifestVersion        string         `json:"manifestVersion"`
	ReleaseVersion         string         `json:"releaseVersion"`
	Status                 string         `json:"status"`
	SupportedLocales       []string       `json:"supportedLocales"`
	Files                  []ManifestFile `json:"files"`
	ContentRootSHA256      string         `json:"contentRootSha256"`
	ActivationRequirements []string       `json:"activationRequirements"`
}

type RoleRequirement struct {
	CapabilityID         string `json:"capabilityId"`
	MinimumEvidenceLevel string `json:"minimumEvidenceLevel"`
}

type Role struct {
	ID             string            `json:"id"`
	Name           Localized         `json:"name"`
	Revision       int               `json:"revision"`
	Status         string            `json:"status"`
	RequirementIDs []RoleRequirement `json:"requirementIds"`
	SourceIDs      []string          `json:"sourceIds"`
}

type Capability struct {
	ID             string    `json:"id"`
	Name           Localized `json:"name"`
	Revision       int       `json:"revision"`
	Status         string    `json:"status"`
	Description    Localized `json:"description"`
	EvidenceLevels []string  `json:"evidenceLevels"`
	Usage          []string  `json:"usage"`
	SourceIDs      []string  `json:"sourceIds"`
}

type Ontology struct {
	SchemaVersion      string       `json:"schemaVersion"`
	ReleaseVersion     string       `json:"releaseVersion"`
	SupportedLocales   []string     `json:"supportedLocales"`
	EvidenceLevelOrder []string     `json:"evidenceLevelOrder"`
	Roles              []Role       `json:"roles"`
	Capabilities       []Capability `json:"capabilities"`
}

type TransferBridge struct {
	FromCapabilityID string    `json:"fromCapabilityId"`
	ToCapabilityID   string    `json:"toCapabilityId"`
	Rationale        Localized `json:"rationale"`
	EvidenceRule     string    `json:"evidenceRule"`
}

type Transition struct {
	ID                  string           `json:"id"`
	Revision            int              `json:"revision"`
	Status              string           `json:"status"`
	SourceRoleID        string           `json:"sourceRoleId"`
	TargetRoleID        string           `json:"targetRoleId"`
	TransferBridges     []TransferBridge `json:"transferBridges"`
	GapCapabilityIDs    []string         `json:"gapCapabilityIds"`
	FirstTaskTemplateID string           `json:"firstTaskTemplateId"`
	SourceIDs           []string         `json:"sourceIds"`
}

type TransitionDocument struct {
	SchemaVersion    string       `json:"schemaVersion"`
	ReleaseVersion   string       `json:"releaseVersion"`
	SupportedLocales []string     `json:"supportedLocales"`
	Transitions      []Transition `json:"transitions"`
}

type EvidenceProposal struct {
	Level                              string `json:"level"`
	RequiresDeterministicOrHumanReview bool   `json:"requiresDeterministicOrHumanReview"`
	AutomaticCapabilityUpgrade         bool   `json:"automaticCapabilityUpgrade"`
}

type SuccessCriteria struct {
	EN []string `json:"en"`
	ZH []string `json:"zh-CN"`
}

type Task struct {
	ID                 string           `json:"id"`
	Revision           int              `json:"revision"`
	Status             string           `json:"status"`
	TransitionID       string           `json:"transitionId"`
	TargetCapabilityID string           `json:"targetCapabilityId"`
	PracticeKind       string           `json:"practiceKind"`
	EstimatedMinutes   int              `json:"estimatedMinutes"`
	Title              Localized        `json:"title"`
	Brief              Localized        `json:"brief"`
	SuccessCriteria    SuccessCriteria  `json:"successCriteria"`
	RubricID           string           `json:"rubricId"`
	EvidenceProposal   EvidenceProposal `json:"evidenceProposal"`
	SourceIDs          []string         `json:"sourceIds"`
}

type TaskDocument struct {
	SchemaVersion    string   `json:"schemaVersion"`
	ReleaseVersion   string   `json:"releaseVersion"`
	SupportedLocales []string `json:"supportedLocales"`
	Tasks            []Task   `json:"tasks"`
}

type RubricScale struct {
	Minimum           int `json:"minimum"`
	Maximum           int `json:"maximum"`
	AcceptableMinimum int `json:"acceptableMinimum"`
}

type RubricDimension struct {
	ID    string    `json:"id"`
	Label Localized `json:"label"`
}

type Rubric struct {
	ID           string            `json:"id"`
	Revision     int               `json:"revision"`
	Status       string            `json:"status"`
	PracticeKind string            `json:"practiceKind"`
	Scale        RubricScale       `json:"scale"`
	Dimensions   []RubricDimension `json:"dimensions"`
	ScoringRule  string            `json:"scoringRule"`
	SourceIDs    []string          `json:"sourceIds"`
}

type RubricDocument struct {
	SchemaVersion    string   `json:"schemaVersion"`
	ReleaseVersion   string   `json:"releaseVersion"`
	SupportedLocales []string `json:"supportedLocales"`
	Rubrics          []Rubric `json:"rubrics"`
}

type ContentSource struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	Locator       string `json:"locator"`
	Revision      string `json:"revision"`
	ClaimBoundary string `json:"claimBoundary"`
	ReviewOwner   string `json:"reviewOwner"`
}

type UpdatePolicy struct {
	CadenceDays         int      `json:"cadenceDays"`
	TriggerEvents       []string `json:"triggerEvents"`
	RequiredReviewRoles []string `json:"requiredReviewRoles"`
	ActivationRule      string   `json:"activationRule"`
	RollbackRule        string   `json:"rollbackRule"`
}

type SourceDocument struct {
	SchemaVersion  string          `json:"schemaVersion"`
	ReleaseVersion string          `json:"releaseVersion"`
	Sources        []ContentSource `json:"sources"`
	UpdatePolicy   UpdatePolicy    `json:"updatePolicy"`
}

type Release struct {
	Manifest    Manifest
	Ontology    Ontology
	Transitions TransitionDocument
	Tasks       TaskDocument
	Rubrics     RubricDocument
	Sources     SourceDocument
}

func Load(directory string) (Release, error) {
	if !filepath.IsAbs(directory) {
		return Release{}, ErrInvalid
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Release{}, ErrInvalid
	}
	manifest, err := decodeFile[Manifest](directory, "manifest.json")
	if err != nil || !validManifestShape(manifest) {
		return Release{}, ErrInvalid
	}
	if err := verifyFiles(directory, manifest); err != nil {
		return Release{}, err
	}
	release := Release{Manifest: manifest}
	if release.Ontology, err = decodeFile[Ontology](directory, "career-ontology.json"); err != nil {
		return Release{}, err
	}
	if release.Transitions, err = decodeFile[TransitionDocument](directory, "transition-templates.json"); err != nil {
		return Release{}, err
	}
	if release.Tasks, err = decodeFile[TaskDocument](directory, "task-templates.json"); err != nil {
		return Release{}, err
	}
	if release.Rubrics, err = decodeFile[RubricDocument](directory, "rubric-versions.json"); err != nil {
		return Release{}, err
	}
	if release.Sources, err = decodeFile[SourceDocument](directory, "content-sources.json"); err != nil {
		return Release{}, err
	}
	if err := validate(release); err != nil {
		return Release{}, err
	}
	return release, nil
}

func decodeFile[T any](directory, name string) (T, error) {
	var zero T
	path := filepath.Join(directory, name)
	if filepath.Dir(path) != filepath.Clean(directory) {
		return zero, ErrInvalid
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxDocumentBytes {
		return zero, ErrInvalid
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return zero, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var value T
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return zero, ErrInvalid
	}
	return value, nil
}

func validManifestShape(manifest Manifest) bool {
	if manifest.ManifestVersion != "1.0.0" || manifest.ReleaseVersion == "" || manifest.Status != "release_candidate" || !sameStrings(manifest.SupportedLocales, []string{"en", "zh-CN"}) || len(manifest.ActivationRequirements) == 0 || !digestRE.MatchString(manifest.ContentRootSHA256) || len(manifest.Files) != len(requiredFiles) {
		return false
	}
	names := make([]string, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		if filepath.Base(file.Name) != file.Name || !digestRE.MatchString(file.SHA256) || file.Bytes <= 0 || file.Bytes > maxDocumentBytes {
			return false
		}
		names = append(names, file.Name)
	}
	sort.Strings(names)
	return sameStrings(names, requiredFiles)
}

func verifyFiles(directory string, manifest Manifest) error {
	for _, file := range manifest.Files {
		info, err := os.Lstat(filepath.Join(directory, file.Name))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != file.Bytes {
			return ErrInvalid
		}
		body, err := os.ReadFile(filepath.Join(directory, file.Name))
		if err != nil || hash(body) != file.SHA256 {
			return ErrInvalid
		}
	}
	encoded, err := json.MarshalIndent(manifest.Files, "", "  ")
	if err != nil || hash(append(encoded, '\n')) != manifest.ContentRootSHA256 {
		return ErrInvalid
	}
	return nil
}

func validate(release Release) error {
	if release.Ontology.ReleaseVersion != release.Manifest.ReleaseVersion || release.Transitions.ReleaseVersion != release.Manifest.ReleaseVersion || release.Tasks.ReleaseVersion != release.Manifest.ReleaseVersion || release.Rubrics.ReleaseVersion != release.Manifest.ReleaseVersion || release.Sources.ReleaseVersion != release.Manifest.ReleaseVersion {
		return ErrInvalid
	}
	for _, documentLocales := range [][]string{release.Ontology.SupportedLocales, release.Transitions.SupportedLocales, release.Tasks.SupportedLocales, release.Rubrics.SupportedLocales} {
		if !sameStrings(documentLocales, release.Manifest.SupportedLocales) {
			return ErrInvalid
		}
	}
	if !sameStrings(release.Ontology.EvidenceLevelOrder, []string{"inferred", "user_confirmed", "demonstrated", "applied", "reviewer_verified"}) {
		return ErrInvalid
	}
	roleIDs, capabilityIDs, transitionIDs, taskIDs, rubricIDs, sourceIDs := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	for _, source := range release.Sources.Sources {
		if !addID(sourceIDs, source.ID) || source.Kind == "" || source.Title == "" || source.Locator == "" || source.Revision == "" || source.ClaimBoundary == "" || source.ReviewOwner == "" {
			return ErrInvalid
		}
	}
	if len(sourceIDs) == 0 || release.Sources.UpdatePolicy.CadenceDays <= 0 || len(release.Sources.UpdatePolicy.TriggerEvents) == 0 || len(release.Sources.UpdatePolicy.RequiredReviewRoles) == 0 || release.Sources.UpdatePolicy.ActivationRule == "" || release.Sources.UpdatePolicy.RollbackRule == "" {
		return ErrInvalid
	}
	for _, capability := range release.Ontology.Capabilities {
		if !addID(capabilityIDs, capability.ID) || !validLocalized(capability.Name) || !validLocalized(capability.Description) || capability.Revision < 1 || capability.Status != "active" || !sameStrings(capability.EvidenceLevels, release.Ontology.EvidenceLevelOrder) || !references(capability.SourceIDs, sourceIDs) {
			return ErrInvalid
		}
	}
	for _, role := range release.Ontology.Roles {
		if !addID(roleIDs, role.ID) || !validLocalized(role.Name) || role.Revision < 1 || role.Status != "active" || !references(role.SourceIDs, sourceIDs) {
			return ErrInvalid
		}
		for _, requirement := range role.RequirementIDs {
			if _, ok := capabilityIDs[requirement.CapabilityID]; !ok || requirement.MinimumEvidenceLevel == "" {
				return ErrInvalid
			}
		}
	}
	for _, rubric := range release.Rubrics.Rubrics {
		if !addID(rubricIDs, rubric.ID) || rubric.Revision < 1 || rubric.Status != "active" || !oneOf(rubric.PracticeKind, "code", "writing", "design") || rubric.Scale.Minimum != 1 || rubric.Scale.Maximum != 5 || rubric.Scale.AcceptableMinimum != 4 || len(rubric.Dimensions) < 4 || rubric.ScoringRule == "" || !references(rubric.SourceIDs, sourceIDs) {
			return ErrInvalid
		}
		seen := map[string]struct{}{}
		for _, dimension := range rubric.Dimensions {
			if !addID(seen, dimension.ID) || !validLocalized(dimension.Label) {
				return ErrInvalid
			}
		}
	}
	for _, transition := range release.Transitions.Transitions {
		if !addID(transitionIDs, transition.ID) || transition.Revision < 1 || transition.Status != "active" || !hasID(roleIDs, transition.SourceRoleID) || !hasID(roleIDs, transition.TargetRoleID) || len(transition.TransferBridges) == 0 || len(transition.GapCapabilityIDs) == 0 || !references(transition.GapCapabilityIDs, capabilityIDs) || !references(transition.SourceIDs, sourceIDs) {
			return ErrInvalid
		}
		for _, bridge := range transition.TransferBridges {
			if !hasID(capabilityIDs, bridge.FromCapabilityID) || !hasID(capabilityIDs, bridge.ToCapabilityID) || !validLocalized(bridge.Rationale) || bridge.EvidenceRule == "" {
				return ErrInvalid
			}
		}
	}
	for _, task := range release.Tasks.Tasks {
		if !addID(taskIDs, task.ID) || task.Revision < 1 || task.Status != "active" || !hasID(transitionIDs, task.TransitionID) || !hasID(capabilityIDs, task.TargetCapabilityID) || !oneOf(task.PracticeKind, "code", "writing", "design") || task.EstimatedMinutes < 5 || task.EstimatedMinutes > 480 || !validLocalized(task.Title) || !validLocalized(task.Brief) || len(task.SuccessCriteria.EN) == 0 || len(task.SuccessCriteria.EN) != len(task.SuccessCriteria.ZH) || !hasID(rubricIDs, task.RubricID) || task.EvidenceProposal.AutomaticCapabilityUpgrade || !task.EvidenceProposal.RequiresDeterministicOrHumanReview || !references(task.SourceIDs, sourceIDs) {
			return ErrInvalid
		}
	}
	for _, transition := range release.Transitions.Transitions {
		if !hasID(taskIDs, transition.FirstTaskTemplateID) {
			return ErrInvalid
		}
	}
	if len(roleIDs) == 0 || len(capabilityIDs) == 0 || len(transitionIDs) == 0 || len(taskIDs) == 0 || len(rubricIDs) == 0 {
		return ErrInvalid
	}
	return nil
}

func validLocalized(value Localized) bool { return value.EN != "" && value.ZH != "" }
func oneOf(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
func addID(index map[string]struct{}, id string) bool {
	if id == "" {
		return false
	}
	if _, exists := index[id]; exists {
		return false
	}
	index[id] = struct{}{}
	return true
}
func hasID(index map[string]struct{}, id string) bool { _, ok := index[id]; return ok }
func references(ids []string, index map[string]struct{}) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if !hasID(index, id) {
			return false
		}
	}
	return true
}
func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
func hash(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func (release Release) Identity() string {
	return fmt.Sprintf("%s:%s", release.Manifest.ReleaseVersion, release.Manifest.ContentRootSHA256)
}
