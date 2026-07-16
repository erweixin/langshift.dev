package observability

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// AgentMetrics is the bounded production metric surface used by SLOs and
// capacity gates. Every attribute is selected from a closed vocabulary; IDs,
// tenant names, provider endpoints, prompts, and payload-derived values are
// deliberately impossible to attach through this API.
type AgentMetrics struct {
	eventAppends          metric.Int64Counter
	eventAppendDuration   metric.Float64Histogram
	queueWait             metric.Float64Histogram
	activeRuns            metric.Int64UpDownCounter
	activeProvider        metric.Int64UpDownCounter
	providerTokens        metric.Int64Counter
	activeRuntimeSessions metric.Int64UpDownCounter
	runtimeColdStart      metric.Float64Histogram
	artifactBytes         metric.Int64Counter
	retainedBytes         metric.Int64Counter
	leaseHeartbeats       metric.Int64Counter
	runReplays            metric.Int64Counter
	runCompletions        metric.Int64Counter
	runExecutions         metric.Int64Counter
	firstSafeToken        metric.Float64Histogram
	toolCalls             metric.Int64Counter
	toolUnknown           metric.Int64Counter
	realtimeConnections   metric.Int64UpDownCounter
	realtimeGapRecovery   metric.Float64Histogram
	invariantViolations   metric.Int64Counter
}

var invariantNames = []string{
	"lost_event_fact",
	"duplicate_external_effect",
	"cross_tenant_access",
	"approval_bypass",
	"terminal_state_regression",
}

func newAgentMetrics(meter metric.Meter) (*AgentMetrics, error) {
	value := &AgentMetrics{}
	var err error
	if value.eventAppends, err = meter.Int64Counter("event.appends", metric.WithUnit("{event}")); err != nil {
		return nil, err
	}
	if value.eventAppendDuration, err = meter.Float64Histogram("event.append.duration", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.001, .0025, .005, .01, .025, .05, .1, .2, .5, 1, 2.5)); err != nil {
		return nil, err
	}
	if value.queueWait, err = meter.Float64Histogram("queue.wait", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.005, .01, .025, .05, .1, .25, .5, 1, 2, 5, 10, 30)); err != nil {
		return nil, err
	}
	if value.activeRuns, err = meter.Int64UpDownCounter("runs.executing", metric.WithUnit("{run}")); err != nil {
		return nil, err
	}
	if value.activeProvider, err = meter.Int64UpDownCounter("provider.requests.active", metric.WithUnit("{request}")); err != nil {
		return nil, err
	}
	if value.providerTokens, err = meter.Int64Counter("provider.tokens", metric.WithUnit("{token}")); err != nil {
		return nil, err
	}
	if value.activeRuntimeSessions, err = meter.Int64UpDownCounter("runtime.sessions.owned", metric.WithUnit("{session}")); err != nil {
		return nil, err
	}
	if value.runtimeColdStart, err = meter.Float64Histogram("runtime.cold_start", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.05, .1, .25, .5, 1, 2, 5, 10, 15, 30, 60)); err != nil {
		return nil, err
	}
	if value.artifactBytes, err = meter.Int64Counter("artifact.bytes", metric.WithUnit("By")); err != nil {
		return nil, err
	}
	if value.retainedBytes, err = meter.Int64Counter("retained.bytes", metric.WithUnit("By")); err != nil {
		return nil, err
	}
	if value.leaseHeartbeats, err = meter.Int64Counter("lease.heartbeats", metric.WithUnit("{heartbeat}")); err != nil {
		return nil, err
	}
	if value.runReplays, err = meter.Int64Counter("run.replays", metric.WithUnit("{replay}")); err != nil {
		return nil, err
	}
	if value.runCompletions, err = meter.Int64Counter("run.deadline_transitions", metric.WithUnit("{transition}")); err != nil {
		return nil, err
	}
	if value.runExecutions, err = meter.Int64Counter("run.executions", metric.WithUnit("{execution}")); err != nil {
		return nil, err
	}
	if value.firstSafeToken, err = meter.Float64Histogram("first_safe_token", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.05, .1, .25, .5, 1, 2, 5, 10, 15, 30, 60)); err != nil {
		return nil, err
	}
	if value.toolCalls, err = meter.Int64Counter("tool.calls", metric.WithUnit("{call}")); err != nil {
		return nil, err
	}
	if value.toolUnknown, err = meter.Int64Counter("tool.unknown", metric.WithUnit("{call}")); err != nil {
		return nil, err
	}
	if value.realtimeConnections, err = meter.Int64UpDownCounter("realtime.connections", metric.WithUnit("{connection}")); err != nil {
		return nil, err
	}
	if value.realtimeGapRecovery, err = meter.Float64Histogram("realtime.gap_recovery", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.005, .01, .025, .05, .1, .25, .5, 1, 2, 5, 10, 30)); err != nil {
		return nil, err
	}
	if value.invariantViolations, err = meter.Int64Counter("invariant.violations", metric.WithUnit("{violation}")); err != nil {
		return nil, err
	}
	for _, invariant := range invariantNames {
		value.invariantViolations.Add(context.Background(), 0, metric.WithAttributes(attribute.String("invariant", invariant)))
	}
	return value, nil
}

func (metrics *AgentMetrics) ObserveEventAppend(ctx context.Context, duration time.Duration, outcome string) {
	if metrics == nil {
		return
	}
	outcome = bounded(outcome, "committed", "replayed", "version_conflict", "command_conflict", "failed")
	options := metric.WithAttributes(attribute.String("outcome", outcome))
	metrics.eventAppends.Add(ctx, 1, options)
	metrics.eventAppendDuration.Record(ctx, duration.Seconds(), options)
}

func (metrics *AgentMetrics) ObserveQueueWait(ctx context.Context, duration time.Duration, queueClass string) {
	if metrics == nil {
		return
	}
	metrics.queueWait.Record(ctx, duration.Seconds(), metric.WithAttributes(attribute.String("queue_class", bounded(queueClass, "interactive", "background"))))
}

func (metrics *AgentMetrics) AddExecutingRuns(ctx context.Context, delta int64, queueClass string) {
	if metrics == nil || delta == 0 {
		return
	}
	metrics.activeRuns.Add(ctx, delta, metric.WithAttributes(attribute.String("queue_class", bounded(queueClass, "interactive", "background"))))
}

func (metrics *AgentMetrics) AddActiveProviderRequests(ctx context.Context, delta int64, provider string) {
	if metrics == nil || delta == 0 {
		return
	}
	metrics.activeProvider.Add(ctx, delta, metric.WithAttributes(attribute.String("provider", bounded(provider, "openai", "anthropic", "google", "azure_openai", "custom"))))
}

func (metrics *AgentMetrics) AddProviderTokens(ctx context.Context, count int64, provider, modelFamily string) {
	if metrics == nil || count <= 0 {
		return
	}
	metrics.providerTokens.Add(ctx, count, metric.WithAttributes(attribute.String("provider", bounded(provider, "openai", "anthropic", "google", "azure_openai", "custom")), attribute.String("model_family", bounded(modelFamily, "gpt", "claude", "gemini", "custom"))))
}

func (metrics *AgentMetrics) AddOwnedRuntimeSessions(ctx context.Context, delta int64, trustTier string) {
	if metrics == nil || delta == 0 {
		return
	}
	metrics.activeRuntimeSessions.Add(ctx, delta, metric.WithAttributes(attribute.String("trust_tier", bounded(trustTier, "trusted", "untrusted"))))
}

func (metrics *AgentMetrics) ObserveRuntimeColdStart(ctx context.Context, duration time.Duration, trustTier string) {
	if metrics == nil {
		return
	}
	metrics.runtimeColdStart.Record(ctx, duration.Seconds(), metric.WithAttributes(attribute.String("trust_tier", bounded(trustTier, "trusted", "untrusted"))))
}

func (metrics *AgentMetrics) AddArtifactBytes(ctx context.Context, count int64, direction string) {
	if metrics == nil || count <= 0 {
		return
	}
	metrics.artifactBytes.Add(ctx, count, metric.WithAttributes(attribute.String("direction", bounded(direction, "read", "write"))))
}

func (metrics *AgentMetrics) AddRetainedBytes(ctx context.Context, count int64) {
	if metrics == nil || count <= 0 {
		return
	}
	metrics.retainedBytes.Add(ctx, count)
}

func (metrics *AgentMetrics) AddLeaseHeartbeats(ctx context.Context, count int64, leaseKind string) {
	if metrics == nil || count <= 0 {
		return
	}
	metrics.leaseHeartbeats.Add(ctx, count, metric.WithAttributes(attribute.String("lease_kind", bounded(leaseKind, "run", "tool", "reconciliation", "runtime"))))
}

func (metrics *AgentMetrics) AddRunReplays(ctx context.Context, count int64, outcome string) {
	if metrics == nil || count <= 0 {
		return
	}
	metrics.runReplays.Add(ctx, count, metric.WithAttributes(attribute.String("outcome", bounded(outcome, "success", "checksum_rejected", "failed"))))
}

func (metrics *AgentMetrics) AddRunDeadlineTransition(ctx context.Context, queueClass string, deadlineAdherent bool, platformOutcome string) {
	if metrics == nil {
		return
	}
	metrics.runCompletions.Add(ctx, 1, metric.WithAttributes(attribute.String("queue_class", bounded(queueClass, "interactive", "background")), attribute.Bool("deadline_adherent", deadlineAdherent), attribute.String("platform_outcome", bounded(platformOutcome, "success", "failed"))))
}

func (metrics *AgentMetrics) AddRunExecution(ctx context.Context, queueClass, platformOutcome string) {
	if metrics == nil {
		return
	}
	metrics.runExecutions.Add(ctx, 1, metric.WithAttributes(attribute.String("queue_class", bounded(queueClass, "interactive", "background")), attribute.String("platform_outcome", bounded(platformOutcome, "success", "failed"))))
}

func (metrics *AgentMetrics) ObserveFirstSafeToken(ctx context.Context, duration time.Duration, provider string) {
	if metrics == nil || duration < 0 {
		return
	}
	metrics.firstSafeToken.Record(ctx, duration.Seconds(), metric.WithAttributes(attribute.String("provider", bounded(provider, "openai", "anthropic", "google", "azure_openai", "custom"))))
}

func (metrics *AgentMetrics) AddToolCall(ctx context.Context, outcome string, effectful bool) {
	if metrics == nil {
		return
	}
	metrics.toolCalls.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", bounded(outcome, "succeeded", "failed", "unknown", "resolved_unknown")), attribute.Bool("effectful", effectful)))
}

func (metrics *AgentMetrics) AddToolUnknown(ctx context.Context, convergedWithin15Minutes bool) {
	if metrics == nil {
		return
	}
	metrics.toolUnknown.Add(ctx, 1, metric.WithAttributes(attribute.Bool("converged_15m", convergedWithin15Minutes)))
}

func (metrics *AgentMetrics) AddRealtimeConnections(ctx context.Context, delta int64) {
	if metrics == nil || delta == 0 {
		return
	}
	metrics.realtimeConnections.Add(ctx, delta)
}

func (metrics *AgentMetrics) ObserveRealtimeGapRecovery(ctx context.Context, duration time.Duration) {
	if metrics == nil {
		return
	}
	metrics.realtimeGapRecovery.Record(ctx, duration.Seconds())
}

func (metrics *AgentMetrics) AddInvariantViolation(ctx context.Context, invariant string) {
	if metrics == nil {
		return
	}
	metrics.invariantViolations.Add(ctx, 1, metric.WithAttributes(attribute.String("invariant", bounded(invariant, invariantNames...))))
}

func bounded(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "unknown"
}
