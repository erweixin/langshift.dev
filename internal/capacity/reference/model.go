// Package reference implements the fixed Stage 3 reference-production load
// protocol. It deliberately drives only public product APIs; Prometheus remains
// the independent source of truth for achieved capacity and SLOs.
package reference

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
	"time"
)

const (
	ProfileID      = "stage3-reference-production-v1"
	ProfileVersion = "1.0.0"
	DatasetVersion = "1.0.0"
)

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	labelPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
	ErrInvalid    = errors.New("reference capacity configuration is invalid")
)

type Profile struct {
	ProfileVersion                string             `json:"profileVersion"`
	ProfileID                     string             `json:"profileId"`
	DurationSeconds               int                `json:"durationSeconds"`
	SampleIntervalSeconds         int                `json:"sampleIntervalSeconds"`
	MinimumObservationsPerMeasure int                `json:"minimumObservationsPerMeasurement"`
	Vectors                       map[string]float64 `json:"vectors"`
	SLOs                          map[string]float64 `json:"slos"`
	ZeroTolerance                 []string           `json:"zeroTolerance"`
}

type Dataset struct {
	DatasetVersion string      `json:"datasetVersion"`
	DatasetID      string      `json:"datasetId"`
	ProfileID      string      `json:"profileId"`
	SourceCommit   string      `json:"sourceCommit"`
	Principals     []Principal `json:"principals"`
}

type Principal struct {
	Label            string         `json:"label"`
	TenantBucket     string         `json:"tenantBucket"`
	HotUser          bool           `json:"hotUser"`
	SessionTokenFile string         `json:"sessionTokenFile"`
	CSRFTokenFile    string         `json:"csrfTokenFile"`
	ConnectionCount  int            `json:"connectionCount"`
	Conversations    []Conversation `json:"conversations"`
}

type Conversation struct {
	ID       string `json:"id"`
	Version  uint64 `json:"version"`
	Scenario string `json:"scenario"`
}

type LoadedDataset struct {
	Dataset
	SHA256 string
}

func LoadProfile(path string) (Profile, []byte, string, error) {
	raw, err := readStrictFile(path, 1<<20)
	if err != nil {
		return Profile{}, nil, "", err
	}
	var profile Profile
	if err = strictJSON(raw, &profile); err != nil || profile.Validate() != nil {
		return Profile{}, nil, "", ErrInvalid
	}
	digest := sha256.Sum256(raw)
	return profile, raw, hex.EncodeToString(digest[:]), nil
}

func (profile Profile) Validate() error {
	requiredVectors := map[string]float64{
		"concurrentConnections": 10000, "apiRequestsPerSecond": 500,
		"eventAppendsPerSecond": 2000, "activeRuns": 10000,
		"concurrentProviderRequests": 200, "activeRuntimeSessions": 500,
		"tokensPerSecond": 100000, "artifactMiBPerSecond": 500,
		"retentionTiBPerDay": 1, "hotTenantCapacityPercent": 20,
		"hotTenantUserEventAppendsPerSecond": 100,
		"leaseHeartbeatsPerSecond":           1000, "runReplaysPerSecond": 100,
	}
	if profile.ProfileVersion != ProfileVersion || profile.ProfileID != ProfileID || profile.DurationSeconds != 1800 || profile.SampleIntervalSeconds != 15 || profile.MinimumObservationsPerMeasure != 116 || len(profile.Vectors) != len(requiredVectors) || len(profile.SLOs) != 18 || len(profile.ZeroTolerance) != 5 {
		return ErrInvalid
	}
	for name, value := range requiredVectors {
		if profile.Vectors[name] != value {
			return ErrInvalid
		}
	}
	return nil
}

func LoadDataset(path, sourceCommit string, profile Profile) (LoadedDataset, error) {
	raw, err := readStrictFile(path, 64<<20)
	if err != nil {
		return LoadedDataset{}, err
	}
	var dataset Dataset
	if err = strictJSON(raw, &dataset); err != nil || dataset.Validate(sourceCommit, profile) != nil {
		return LoadedDataset{}, ErrInvalid
	}
	digest := sha256.Sum256(raw)
	return LoadedDataset{Dataset: dataset, SHA256: hex.EncodeToString(digest[:])}, nil
}

func (dataset Dataset) Validate(sourceCommit string, profile Profile) error {
	if dataset.DatasetVersion != DatasetVersion || !labelPattern.MatchString(dataset.DatasetID) || dataset.ProfileID != profile.ProfileID || dataset.SourceCommit != sourceCommit || !commitPattern.MatchString(sourceCommit) || len(dataset.Principals) < 5 || len(dataset.Principals) > 20000 {
		return ErrInvalid
	}
	connections := 0
	tenantConnections := map[string]int{}
	tenantPrincipals := map[string]int{}
	hotUsers := 0
	hotConversations := 0
	hotTenant := ""
	nonHotConversationsByTenant := map[string]int{}
	seenLabels := map[string]struct{}{}
	seenConversations := map[string]struct{}{}
	for _, principal := range dataset.Principals {
		if !labelPattern.MatchString(principal.Label) || !labelPattern.MatchString(principal.TenantBucket) || principal.ConnectionCount < 0 || principal.ConnectionCount > int(profile.Vectors["concurrentConnections"]) || !filepath.IsAbs(principal.SessionTokenFile) || !filepath.IsAbs(principal.CSRFTokenFile) || len(principal.Conversations) == 0 || len(principal.Conversations) > 100000 {
			return ErrInvalid
		}
		if _, exists := seenLabels[principal.Label]; exists {
			return ErrInvalid
		}
		seenLabels[principal.Label] = struct{}{}
		connections += principal.ConnectionCount
		tenantConnections[principal.TenantBucket] += principal.ConnectionCount
		tenantPrincipals[principal.TenantBucket]++
		if principal.HotUser {
			hotUsers++
			hotConversations += len(principal.Conversations)
			hotTenant = principal.TenantBucket
		} else {
			nonHotConversationsByTenant[principal.TenantBucket] += len(principal.Conversations)
		}
		for _, conversation := range principal.Conversations {
			if !idPattern.MatchString(conversation.ID) || conversation.Version < 1 || !labelPattern.MatchString(conversation.Scenario) {
				return ErrInvalid
			}
			if _, exists := seenConversations[conversation.ID]; exists {
				return ErrInvalid
			}
			seenConversations[conversation.ID] = struct{}{}
		}
	}
	targetConnections := int(profile.Vectors["concurrentConnections"])
	if connections != targetConnections || len(tenantConnections) != 5 || hotUsers != 1 || hotConversations < 34 || nonHotConversationsByTenant[hotTenant] == 0 {
		return ErrInvalid
	}
	for tenant, count := range tenantConnections {
		if tenantPrincipals[tenant] < 1 || count*100 != connections*int(profile.Vectors["hotTenantCapacityPercent"]) {
			return fmt.Errorf("%w: tenant bucket %s exceeds fixed share", ErrInvalid, tenant)
		}
	}
	return nil
}

func (dataset Dataset) SortedTenantBuckets() []string {
	values := map[string]struct{}{}
	for _, principal := range dataset.Principals {
		values[principal.TenantBucket] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func readStrictFile(path string, maximum int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 2 || info.Size() > maximum {
		return nil, ErrInvalid
	}
	return os.ReadFile(path)
}

func strictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

func ParseDurationSeconds(value int) (time.Duration, error) {
	if value != 1800 {
		return 0, ErrInvalid
	}
	return time.Duration(value) * time.Second, nil
}
