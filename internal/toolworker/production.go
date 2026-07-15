package toolworker

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/toolregistry"
)

var ErrProductionConfiguration = errors.New("production tool worker configuration is invalid")

type ProductionConfig struct {
	ArtifactPath, ArtifactFileHash string
	Pool                           *pgxpool.Pool
	Payloads                       payload.Store
	Tools                          ToolStore
	Registrations                  []HandlerRegistration
	ConsumerName, WorkerID         string
	Actor                          json.RawMessage
	IDKey                          []byte
	HeartbeatInterval              time.Duration
	MaximumCommand                 int
	MaximumInput                   int
	MaximumResult                  int
	Resume                         Schedule
	Reconcile                      Schedule
	DefaultReconcileAfter          time.Duration
}

type ProductionRuntime struct {
	Artifact toolregistry.Artifact
	Handler  Handler
}

// NewProductionRuntime is the ToolWorker composition root. The exact same
// content-pinned registry instance is shared by execution dispatch and the
// current PostgreSQL overlay evaluator.
func NewProductionRuntime(configuration ProductionConfig) (ProductionRuntime, error) {
	if configuration.Pool == nil || configuration.Payloads == nil || configuration.Tools == nil {
		return ProductionRuntime{}, ErrProductionConfiguration
	}
	artifact, err := toolregistry.LoadArtifact(configuration.ArtifactPath, configuration.ArtifactFileHash)
	if err != nil {
		return ProductionRuntime{}, errors.Join(ErrProductionConfiguration, err)
	}
	registry := artifact.Registry
	executor, err := NewRegistryExecutor(&registry, configuration.Registrations, configuration.DefaultReconcileAfter)
	if err != nil {
		return ProductionRuntime{}, errors.Join(ErrProductionConfiguration, err)
	}
	handler := Handler{
		Payloads: configuration.Payloads, Tools: configuration.Tools,
		Policy:   PostgresPolicyEvaluator{Pool: configuration.Pool, Registry: &registry},
		Executor: executor, ConsumerName: configuration.ConsumerName, WorkerID: configuration.WorkerID,
		Actor: configuration.Actor, IDKey: append([]byte(nil), configuration.IDKey...),
		HeartbeatInterval: configuration.HeartbeatInterval,
		MaximumCommand:    configuration.MaximumCommand, MaximumInput: configuration.MaximumInput,
		MaximumResult: configuration.MaximumResult, Resume: configuration.Resume,
		Reconcile: configuration.Reconcile, ReconcileDelay: configuration.DefaultReconcileAfter,
	}
	if err = handler.validate(); err != nil {
		return ProductionRuntime{}, errors.Join(ErrProductionConfiguration, err)
	}
	artifact.Registry = registry
	return ProductionRuntime{Artifact: artifact, Handler: handler}, nil
}
