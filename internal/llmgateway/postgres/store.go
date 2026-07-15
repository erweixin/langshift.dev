// Package postgres owns the durable logical LLM and physical provider-attempt
// protocol. A physical attempt receives exactly one database-backed dispatch
// authorization; losing its outcome never permits replaying the network send.
package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	billingpostgres "github.com/langshift/lites/internal/billing/postgres"
	eventpostgres "github.com/langshift/lites/internal/eventstore/postgres"
	"github.com/langshift/lites/internal/platform/ids"
	"github.com/langshift/lites/internal/security/opaque"
)

var (
	ErrConfiguration     = errors.New("LLM attempt store configuration is invalid")
	ErrInvalidCommand    = errors.New("LLM attempt command is invalid")
	ErrRunFence          = errors.New("LLM attempt is not authorized by the active Run fence")
	ErrAttemptConflict   = errors.New("LLM attempt conflicts with durable state")
	ErrProviderConflict  = errors.New("provider attempt conflicts with durable state")
	ErrModelNotCandidate = errors.New("provider model is not in the immutable context manifest")
	ErrDispatchToken     = errors.New("provider dispatch token is invalid or consumed")
	ErrAlreadyDispatched = errors.New("provider attempt dispatch was already authorized")
	ErrNotFinalizable    = errors.New("logical LLM attempt is not finalizable")
	ErrStaleEpoch        = errors.New("LLM attempt store epoch is stale")
)

type EpochAuthority interface {
	CurrentStoreEpoch(context.Context) (string, error)
}

type Store struct {
	Pool        *pgxpool.Pool
	Appender    eventpostgres.Appender
	Epochs      EpochAuthority
	StoreEpoch  string
	IDKey       []byte
	TokenPepper []byte
	Billing     billingpostgres.Store
	Retrieval   RetrievalManifestCommitter
	Now         func() time.Time
}

type PayloadPointer struct{ Ref, Hash string }

type SnapshotBinding struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
	Hash    string `json:"hash"`
}

type ModelCandidate struct {
	ProviderID     string `json:"provider_id"`
	ModelID        string `json:"model_id"`
	ModelVersion   string `json:"model_version"`
	BoundHost      string `json:"bound_host"`
	PricingVersion string `json:"pricing_version"`
}

type ContextManifest struct {
	SchemaVersion   int               `json:"schema_version"`
	Run             SnapshotBinding   `json:"run"`
	Prompt          SnapshotBinding   `json:"prompt"`
	Messages        []SnapshotBinding `json:"messages"`
	Tools           []SnapshotBinding `json:"tools"`
	Memory          []SnapshotBinding `json:"memory"`
	Retrieval       *RetrievalBinding `json:"retrieval,omitempty"`
	Router          SnapshotBinding   `json:"router"`
	Budget          SnapshotBinding   `json:"budget"`
	Policy          SnapshotBinding   `json:"policy"`
	CandidateModels []ModelCandidate  `json:"candidate_models"`
}

type RetrievalBinding struct {
	ManifestID string `json:"manifest_id"`
}

type RetrievalManifestContext struct {
	ManifestID, AttemptID, TenantID, UserID, RunID, StoreEpoch string
	ContextManifestHash, StartedEventID                        string
	Actor                                                      json.RawMessage
	CorrelationID                                              string
	Now                                                        time.Time
}

type RetrievalManifestCommitter interface {
	Validate(any) bool
	Commit(context.Context, pgx.Tx, RetrievalManifestContext, any) error
}

func (store Store) IssuePrepareToken() (string, error) {
	credential, err := store.prepareTokens().Issue()
	return credential.Raw, err
}

func (store Store) IssueCompletionToken() (string, error) {
	credential, err := store.completionTokens().Issue()
	return credential.Raw, err
}

func (store Store) valid() bool {
	return store.Pool != nil && store.Epochs != nil && store.StoreEpoch != "" && len(store.IDKey) >= 32 && len(store.TokenPepper) >= 32 && store.Billing.Compatible(store.Pool, store.StoreEpoch)
}

func (store Store) requireEpoch(ctx context.Context) error {
	epoch, err := store.Epochs.CurrentStoreEpoch(ctx)
	if err != nil || epoch == "" {
		return ErrConfiguration
	}
	if epoch != store.StoreEpoch {
		return ErrStaleEpoch
	}
	return nil
}

func (store Store) now() time.Time {
	now := time.Now().UTC()
	if store.Now != nil {
		now = store.Now().UTC()
	}
	return now.Truncate(time.Microsecond)
}

func (store Store) prepareTokens() opaque.Manager {
	return opaque.Manager{Purpose: "llm-provider-prepare-v1", Pepper: store.TokenPepper}
}

func (store Store) completionTokens() opaque.Manager {
	return opaque.Manager{Purpose: "llm-provider-completion-v1", Pepper: store.TokenPepper}
}

func canonicalManifest(manifest ContextManifest) ([]byte, string, error) {
	if !validManifest(manifest) {
		return nil, "", ErrInvalidCommand
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}

func validManifest(manifest ContextManifest) bool {
	if manifest.SchemaVersion != 1 && manifest.SchemaVersion != 2 || manifest.SchemaVersion == 1 && manifest.Retrieval != nil || manifest.SchemaVersion == 2 && (manifest.Retrieval == nil || manifest.Retrieval.ManifestID == "") || !validSnapshot(manifest.Run) || !validSnapshot(manifest.Prompt) || !validSnapshot(manifest.Router) || !validSnapshot(manifest.Budget) || !validSnapshot(manifest.Policy) || len(manifest.Messages) > 10000 || len(manifest.Tools) > 1000 || len(manifest.Memory) > 10000 || len(manifest.CandidateModels) < 1 || len(manifest.CandidateModels) > 100 {
		return false
	}
	for _, list := range [][]SnapshotBinding{manifest.Messages, manifest.Tools, manifest.Memory} {
		for _, snapshot := range list {
			if !validSnapshot(snapshot) {
				return false
			}
		}
	}
	seen := make(map[string]struct{}, len(manifest.CandidateModels))
	for _, candidate := range manifest.CandidateModels {
		key := candidate.ProviderID + "\x00" + candidate.ModelID + "\x00" + candidate.ModelVersion
		if candidate.ProviderID == "" || candidate.ModelID == "" || candidate.ModelVersion == "" || candidate.BoundHost == "" || candidate.PricingVersion == "" || candidate.BoundHost != canonicalHost(candidate.BoundHost) {
			return false
		}
		if _, exists := seen[key]; exists {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func validSnapshot(snapshot SnapshotBinding) bool {
	return snapshot.ID != "" && snapshot.Version > 0 && snapshot.Hash != ""
}

func canonicalHost(value string) string {
	for _, char := range value {
		if char >= 'A' && char <= 'Z' || char == '/' || char == '@' || char == ':' || char <= ' ' || char > '~' {
			return ""
		}
	}
	return value
}

func containsCandidate(manifest ContextManifest, expected ModelCandidate) bool {
	for _, candidate := range manifest.CandidateModels {
		if candidate == expected {
			return true
		}
	}
	return false
}

type eventIDs struct{ Event, Outbox, Publish string }

func (store Store) eventIDs(scope, aggregateID string, version uint64) (eventIDs, error) {
	values := make([]string, 3)
	for index, part := range []string{"event", "outbox", "publish"} {
		value, err := ids.DeterministicUUID(store.IDKey, scope+":"+part, aggregateID+":"+strconv.FormatUint(version, 10))
		if err != nil {
			return eventIDs{}, err
		}
		values[index] = value
	}
	return eventIDs{Event: values[0], Outbox: values[1], Publish: values[2]}, nil
}

func publishEvent(ids eventIDs, event eventpostgres.Event, pointer PayloadPointer) eventpostgres.Input {
	event.ID, event.PayloadRef, event.PayloadHash = ids.Event, pointer.Ref, pointer.Hash
	return eventpostgres.Input{Event: event, Commands: []eventpostgres.OutboxCommand{{ID: ids.Outbox, CommandID: ids.Publish, CommandType: "events.publish", PayloadRef: pointer.Ref, PayloadHash: pointer.Hash}}}
}

func validPointer(pointer PayloadPointer) bool { return pointer.Ref != "" && pointer.Hash != "" }

func validActor(actor json.RawMessage) bool {
	var value map[string]any
	return json.Unmarshal(actor, &value) == nil && value != nil
}
