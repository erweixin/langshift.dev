package agentworker

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/payload"
	"github.com/langshift/lites/internal/toolregistry"
	"github.com/langshift/lites/internal/toolworker"
)

var ErrProductionConfiguration = errors.New("production agent worker configuration is invalid")

type ProductionConfig struct {
	Pool                                   *pgxpool.Pool
	Payloads                               payload.Store
	Runs                                   RunStore
	Attempts                               LLMAttemptStore
	Billing                                UsageReserver
	Gateway                                ProviderExecutor
	Providers                              provider.Registry
	PromptArtifactPath, PromptArtifactHash string
	RouteArtifactPath, RouteArtifactHash   string
	ToolArtifactPath, ToolArtifactHash     string
	ConsumerName, WorkerID                 string
	Actor                                  json.RawMessage
	IDKey                                  []byte
	MaximumOutputTokens                    uint32
	MaximumCommand, MaximumMessageBytes    int
	MaximumMessages, MaximumCalls          int
	HeartbeatInterval, PrepareTTL          time.Duration
	CompletionTTL, ReconciliationDelay     time.Duration
	ApprovalTTL                            time.Duration
	Notification                           PlanSchedule
	BucketTTL                              time.Duration
	Metrics                                WorkerMetrics
}

type ProductionRuntime struct {
	PromptArtifact PromptArtifact
	RouteArtifact  RouteArtifact
	ToolArtifact   toolregistry.Artifact
	Handler        Handler
}

// NewProductionRuntime is the AgentWorker composition root. All model-
// controlled inputs come from content-pinned release artifacts shared by the
// context builder, result interpreter and execution admission path.
func NewProductionRuntime(configuration ProductionConfig) (ProductionRuntime, error) {
	if configuration.Pool == nil || configuration.Payloads == nil || configuration.Runs == nil || configuration.Attempts == nil || configuration.Billing == nil || configuration.Gateway == nil || configuration.Metrics == nil || len(configuration.IDKey) < 32 {
		return ProductionRuntime{}, ErrProductionConfiguration
	}
	promptArtifact, err := LoadPromptArtifact(configuration.PromptArtifactPath, configuration.PromptArtifactHash)
	if err != nil {
		return ProductionRuntime{}, errors.Join(ErrProductionConfiguration, err)
	}
	routeArtifact, err := LoadRouteArtifact(configuration.RouteArtifactPath, configuration.RouteArtifactHash)
	if err != nil {
		return ProductionRuntime{}, errors.Join(ErrProductionConfiguration, err)
	}
	toolArtifact, err := toolregistry.LoadArtifact(configuration.ToolArtifactPath, configuration.ToolArtifactHash)
	if err != nil {
		return ProductionRuntime{}, errors.Join(ErrProductionConfiguration, err)
	}
	providerID, providerVersion, providerHash := configuration.Providers.Snapshot()
	if providerID == "" || providerVersion == 0 || providerHash == "" || promptArtifact.SourceCommit != routeArtifact.SourceCommit || promptArtifact.SourceCommit != toolArtifact.SourceCommit {
		return ProductionRuntime{}, ErrProductionConfiguration
	}
	registry := toolArtifact.Registry
	contextBuilder := ProductionContextBuilder{
		Sources:   ContextSourceStore{Pool: configuration.Pool, Payloads: configuration.Payloads, MaximumMessageBytes: configuration.MaximumMessageBytes, MaximumMessages: configuration.MaximumMessages},
		Assets:    RegistryBehaviorAssets{Prompts: promptArtifact, Registry: &registry},
		Estimator: ConservativeTokenEstimator{},
		Router:    ImmutableModelRouter{Artifact: routeArtifact, Providers: configuration.Providers, Resources: PostgresRouteResources{Pool: configuration.Pool}, BucketTTL: configuration.BucketTTL},
		IDKey:     append([]byte(nil), configuration.IDKey...), MaximumOutputTokens: configuration.MaximumOutputTokens,
	}
	turns := LLMTurnExecutor{
		Builder: contextBuilder, Attempts: configuration.Attempts, Billing: configuration.Billing,
		Gateway: configuration.Gateway, Payloads: configuration.Payloads, Actor: append(json.RawMessage(nil), configuration.Actor...),
		PrepareTTL: configuration.PrepareTTL, CompletionTTL: configuration.CompletionTTL, ReconciliationDelay: configuration.ReconciliationDelay,
		Metrics: configuration.Metrics,
	}
	plans := ProductionResultPlanBuilder{
		Payloads: configuration.Payloads, IDKey: append([]byte(nil), configuration.IDKey...),
		ApprovalTTL: configuration.ApprovalTTL, Notification: configuration.Notification, MaximumCalls: configuration.MaximumCalls,
	}
	results := ResultInterpreter{
		Tools: RegistryToolResolver{Registry: &registry, Admission: PostgresToolAdmission{Pool: configuration.Pool, Overlay: toolworker.PostgresPolicyEvaluator{Pool: configuration.Pool, Registry: &registry}}},
		Plans: plans, IDKey: append([]byte(nil), configuration.IDKey...), MaxCalls: configuration.MaximumCalls,
	}
	handler := Handler{
		Payloads: configuration.Payloads, Runs: configuration.Runs,
		Runner:       ProductionRunner{Turns: turns, Results: results},
		ConsumerName: configuration.ConsumerName, WorkerID: configuration.WorkerID,
		Actor: append(json.RawMessage(nil), configuration.Actor...), HeartbeatInterval: configuration.HeartbeatInterval,
		MaximumCommand: configuration.MaximumCommand,
		Metrics:        configuration.Metrics,
	}
	if err = handler.validate(); err != nil || configuration.MaximumOutputTokens == 0 || configuration.PrepareTTL <= 0 || configuration.CompletionTTL <= 0 || configuration.ReconciliationDelay <= 0 || configuration.ApprovalTTL < time.Minute || !validPlanSchedule(configuration.Notification) {
		return ProductionRuntime{}, errors.Join(ErrProductionConfiguration, err)
	}
	toolArtifact.Registry = registry
	return ProductionRuntime{PromptArtifact: promptArtifact, RouteArtifact: routeArtifact, ToolArtifact: toolArtifact, Handler: handler}, nil
}
