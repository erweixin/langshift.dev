// Package behavior defines immutable AI behavior snapshots and promotion
// gates. A snapshot binds every model-controlled input used by a Run so a
// rollback changes only the active channel, never historical execution facts.
package behavior

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"time"
)

var (
	ErrInvalidManifest = errors.New("AI behavior snapshot manifest is invalid")
	ErrInvalidReport   = errors.New("AI behavior evaluation report is invalid")
	ErrGateFailed      = errors.New("AI behavior promotion gate failed")
)

type Profile string

const (
	RoutePlanner    Profile = "route_planner"
	DailyPlanner    Profile = "daily_planner"
	Coach           Profile = "coach"
	Evaluator       Profile = "evaluator"
	ArtifactBuilder Profile = "artifact_builder"
)

// Valid reports whether profile is one of the production Agent profiles.
func (profile Profile) Valid() bool { return validProfile(profile) }

// ChannelBinding is the immutable behavior deployment selected at a Run's
// acceptance linearization point.
type ChannelBinding struct {
	ChannelID   string    `json:"channel_id"`
	Sequence    uint64    `json:"sequence"`
	SnapshotID  string    `json:"snapshot_id"`
	Profile     Profile   `json:"profile"`
	Environment string    `json:"environment"`
	ActivatedAt time.Time `json:"activated_at"`
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Binding struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Hash    string `json:"hash"`
}

type Manifest struct {
	SchemaVersion     int       `json:"schema_version"`
	Profile           Profile   `json:"profile"`
	Model             Binding   `json:"model"`
	Prompt            Binding   `json:"prompt"`
	Tools             []Binding `json:"tools"`
	ProfileDefinition Binding   `json:"profile_definition"`
	GuardrailPolicy   Binding   `json:"guardrail_policy"`
	RouterPolicy      Binding   `json:"router_policy"`
	SourceCommit      string    `json:"source_commit"`
	CreatedAt         time.Time `json:"created_at"`
}

func (manifest Manifest) Canonical() (Manifest, error) {
	if manifest.SchemaVersion != 1 || !validProfile(manifest.Profile) || !validBinding(manifest.Model) || !validBinding(manifest.Prompt) || !validBinding(manifest.ProfileDefinition) || !validBinding(manifest.GuardrailPolicy) || !validBinding(manifest.RouterPolicy) || manifest.SourceCommit == "" || len(manifest.SourceCommit) > 128 || manifest.CreatedAt.IsZero() || manifest.CreatedAt.Location() != time.UTC || len(manifest.Tools) > 256 {
		return Manifest{}, ErrInvalidManifest
	}
	canonical := manifest
	canonical.CreatedAt = canonical.CreatedAt.Truncate(time.Microsecond)
	canonical.Tools = append([]Binding(nil), manifest.Tools...)
	sort.Slice(canonical.Tools, func(left, right int) bool {
		if canonical.Tools[left].ID == canonical.Tools[right].ID {
			return canonical.Tools[left].Version < canonical.Tools[right].Version
		}
		return canonical.Tools[left].ID < canonical.Tools[right].ID
	})
	for index, tool := range canonical.Tools {
		if !validBinding(tool) || index > 0 && canonical.Tools[index-1].ID == tool.ID {
			return Manifest{}, ErrInvalidManifest
		}
	}
	return canonical, nil
}

func (manifest Manifest) Hash() (string, error) {
	canonical, err := manifest.Canonical()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", ErrInvalidManifest
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (manifest Manifest) SnapshotID() (string, error) {
	hash, err := manifest.Hash()
	if err != nil {
		return "", err
	}
	return "behavior-" + hash, nil
}

func validProfile(profile Profile) bool {
	switch profile {
	case RoutePlanner, DailyPlanner, Coach, Evaluator, ArtifactBuilder:
		return true
	default:
		return false
	}
}

func validBinding(binding Binding) bool {
	return binding.ID != "" && len(binding.ID) <= 256 && binding.Version != "" && len(binding.Version) <= 128 && digestPattern.MatchString(binding.Hash)
}
