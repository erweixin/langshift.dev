package toolreconciler

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/toolregistry"
)

var ErrProductionConfiguration = errors.New("production tool reconciliation runtime configuration is invalid")

type ProductionConfig struct {
	ArtifactPath, ArtifactFileHash string
	Payloads                       payload.Store
	Store                          ReconciliationStore
	LookupRegistrations            []LookupRegistration
	ConsumerName, WorkerID         string
	Actor                          json.RawMessage
	IDKey                          []byte
	HeartbeatInterval              time.Duration
	MaximumCommand                 int
	MaximumEvidence                int
	MaximumRounds                  int
	RetryDelay, MaximumRetryDelay  time.Duration
	Resume, Retry                  Schedule
	Metrics                        ReconciliationMetrics
}

type ProductionRuntime struct {
	Artifact toolregistry.Artifact
	Handler  Handler
}

func NewProductionRuntime(configuration ProductionConfig) (ProductionRuntime, error) {
	if configuration.Payloads == nil || configuration.Store == nil || configuration.Metrics == nil {
		return ProductionRuntime{}, ErrProductionConfiguration
	}
	artifact, err := toolregistry.LoadArtifact(configuration.ArtifactPath, configuration.ArtifactFileHash)
	if err != nil {
		return ProductionRuntime{}, errors.Join(ErrProductionConfiguration, err)
	}
	registry := artifact.Registry
	lookup, err := NewRegistryLookup(&registry, configuration.LookupRegistrations)
	if err != nil {
		return ProductionRuntime{}, errors.Join(ErrProductionConfiguration, err)
	}
	handler := Handler{Payloads: configuration.Payloads, Store: configuration.Store, Lookup: lookup, ConsumerName: configuration.ConsumerName, WorkerID: configuration.WorkerID, Actor: configuration.Actor, IDKey: append([]byte(nil), configuration.IDKey...), HeartbeatInterval: configuration.HeartbeatInterval, MaximumCommand: configuration.MaximumCommand, MaximumEvidence: configuration.MaximumEvidence, MaximumRounds: configuration.MaximumRounds, RetryDelay: configuration.RetryDelay, MaximumRetryDelay: configuration.MaximumRetryDelay, Resume: configuration.Resume, Retry: configuration.Retry, Metrics: configuration.Metrics}
	if !handler.valid() {
		return ProductionRuntime{}, ErrProductionConfiguration
	}
	artifact.Registry = registry
	return ProductionRuntime{Artifact: artifact, Handler: handler}, nil
}
