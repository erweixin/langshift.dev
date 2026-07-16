// Package toolreconciler turns abandoned, provider-visible tool executions
// into fenced outcome-unknown records and durable reconciliation commands.
// It never replays the original tool call and never automatically resolves
// compensatable or irreversible effects.
package toolreconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
	"github.com/langshift/lites/internal/payload"
)

var ErrConfiguration = errors.New("tool effect sweeper configuration is invalid")

const automaticallyReconcilableEffectClass = "reconcilable_write"

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Store interface {
	ListExpiredEffectTenantIDs(context.Context, string, string, int, int, int) ([]string, error)
	ListExpiredEffectCandidates(context.Context, string, string, string, int) ([]executionpostgres.ExpiredToolEffectCandidate, error)
	SweepExpiredToolEffect(context.Context, executionpostgres.SweepExpiredToolEffectCommand) (executionpostgres.SweptToolEffect, error)
}

type TenantLocker interface {
	WithTenantLock(context.Context, string, func(context.Context) error) (bool, error)
}

type Schedule struct {
	QueueClass, ResourceClass string
	Priority                  int
	CostUnits                 int64
	MaxAttempts               int
}

type Sweeper struct {
	Store          Store
	Payloads       payload.Store
	Locker         TenantLocker
	IDKey          []byte
	StoreEpoch     string
	Now            func() time.Time
	ReconcileDelay time.Duration
	Reconcile      Schedule
	MaximumCommand int
	ShardIndex     int
	ShardCount     int
	TenantPage     int
	EffectPage     int
	Metrics        ReconciliationMetrics
}

type Result struct {
	Tenants, Swept, Contended, ManualReviewRequired int
}

type outcomeUnknownEvidence struct {
	SchemaVersion         int       `json:"schema_version"`
	TenantID              string    `json:"tenant_id"`
	RunID                 string    `json:"run_id"`
	ToolCallID            string    `json:"tool_call_id"`
	EffectID              string    `json:"effect_id"`
	EffectClass           string    `json:"effect_class"`
	ProviderRequestID     string    `json:"provider_request_id"`
	ExpiredAttemptID      string    `json:"expired_attempt_id"`
	ExpiredFence          uint64    `json:"expired_fence"`
	LeaseExpiredAt        time.Time `json:"lease_expired_at"`
	TerminalToolVersion   uint64    `json:"terminal_tool_version"`
	TerminalEffectVersion uint64    `json:"terminal_effect_version"`
	Reason                string    `json:"reason"`
	ManualReviewRequired  bool      `json:"manual_review_required"`
}

type attemptExpiredEvidence struct {
	SchemaVersion  int       `json:"schema_version"`
	TenantID       string    `json:"tenant_id"`
	RunID          string    `json:"run_id"`
	ToolCallID     string    `json:"tool_call_id"`
	JobID          string    `json:"job_id"`
	AttemptID      string    `json:"attempt_id"`
	Fence          uint64    `json:"fence"`
	LeaseExpiredAt time.Time `json:"lease_expired_at"`
	Reason         string    `json:"reason"`
}

// CommandPayload is the encrypted, immutable provider-lookup contract read by
// the reconciliation worker. Every registry and effect identity is bound to
// the exact terminal ToolCall version that created the command.
type CommandPayload struct {
	SchemaVersion        int       `json:"schema_version"`
	TenantID             string    `json:"tenant_id"`
	RunID                string    `json:"run_id"`
	ToolCallID           string    `json:"tool_call_id"`
	EffectID             string    `json:"effect_id"`
	ToolName             string    `json:"tool_name"`
	DescriptorSnapshotID string    `json:"descriptor_snapshot_id"`
	DescriptorHash       string    `json:"descriptor_hash"`
	EffectClass          string    `json:"effect_class"`
	EffectKey            string    `json:"effect_key"`
	EffectScope          string    `json:"effect_scope"`
	ProviderID           string    `json:"provider_id"`
	ProviderRequestID    string    `json:"provider_request_id"`
	RequestHash          string    `json:"request_hash"`
	TerminalToolVersion  uint64    `json:"terminal_tool_version"`
	ReconciliationDueAt  time.Time `json:"reconciliation_due_at"`
	OutcomeUnknownAt     time.Time `json:"outcome_unknown_at"`
	ReconciliationRound  int       `json:"reconciliation_round"`
	CorrelationID        string    `json:"correlation_id"`
}

func (sweeper Sweeper) RunOnce(ctx context.Context) (Result, error) {
	if !sweeper.valid() {
		return Result{}, ErrConfiguration
	}
	result := Result{}
	var tenantCursor string
	for {
		tenants, err := sweeper.Store.ListExpiredEffectTenantIDs(ctx, sweeper.StoreEpoch, tenantCursor, sweeper.TenantPage, sweeper.ShardIndex, sweeper.ShardCount)
		if err != nil {
			return result, err
		}
		for _, tenantID := range tenants {
			if tenantID == "" {
				return result, ErrConfiguration
			}
			locked, lockErr := sweeper.Locker.WithTenantLock(ctx, tenantID, func(lockCtx context.Context) error {
				result.Tenants++
				return sweeper.sweepTenant(lockCtx, tenantID, &result)
			})
			if lockErr != nil {
				return result, lockErr
			}
			if !locked {
				result.Contended++
			}
			tenantCursor = tenantID
		}
		if len(tenants) < sweeper.TenantPage {
			return result, nil
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
	}
}

func (sweeper Sweeper) sweepTenant(ctx context.Context, tenantID string, result *Result) error {
	var cursor string
	for {
		candidates, err := sweeper.Store.ListExpiredEffectCandidates(ctx, tenantID, sweeper.StoreEpoch, cursor, sweeper.EffectPage)
		if err != nil {
			return err
		}
		for _, candidate := range candidates {
			if candidate.TenantID != tenantID || candidate.StoreEpoch != sweeper.StoreEpoch || candidate.ToolCallID == "" {
				return ErrConfiguration
			}
			cursor = candidate.ToolCallID
			if err = sweeper.sweep(ctx, candidate); err != nil {
				return err
			}
			if candidate.EffectClass == automaticallyReconcilableEffectClass {
				result.Swept++
			} else {
				result.ManualReviewRequired++
			}
		}
		if len(candidates) < sweeper.EffectPage {
			return nil
		}
	}
}

func (sweeper Sweeper) sweep(ctx context.Context, candidate executionpostgres.ExpiredToolEffectCandidate) error {
	now := time.Now().UTC().Truncate(time.Microsecond)
	if sweeper.Now != nil {
		now = sweeper.Now().UTC().Truncate(time.Microsecond)
	}
	if candidate.LeaseExpiresAt.IsZero() || candidate.LeaseExpiresAt.After(now) || candidate.ToolVersion == 0 || candidate.EffectVersion == 0 || candidate.AttemptVersion == 0 || candidate.Fence == 0 || candidate.ToolName == "" || candidate.DescriptorSnapshotID == "" || candidate.ProviderRequestID == "" {
		return ErrConfiguration
	}
	source, err := sweeper.loadSourceCommand(ctx, candidate)
	if err != nil {
		return err
	}
	terminalToolVersion := candidate.ToolVersion + 1
	terminalEffectVersion := candidate.EffectVersion + 1
	dueAt := now.Add(sweeper.ReconcileDelay).UTC().Truncate(time.Microsecond)
	correlationID := source.CorrelationID
	unknown := outcomeUnknownEvidence{SchemaVersion: 1, TenantID: candidate.TenantID, RunID: candidate.RunID, ToolCallID: candidate.ToolCallID, EffectID: candidate.EffectID, EffectClass: candidate.EffectClass, ProviderRequestID: candidate.ProviderRequestID, ExpiredAttemptID: candidate.AttemptID, ExpiredFence: candidate.Fence, LeaseExpiredAt: candidate.LeaseExpiresAt.UTC().Truncate(time.Microsecond), TerminalToolVersion: terminalToolVersion, TerminalEffectVersion: terminalEffectVersion, Reason: "tool_execution_lease_expired_after_provider_request", ManualReviewRequired: candidate.EffectClass != automaticallyReconcilableEffectClass}
	attempt := attemptExpiredEvidence{SchemaVersion: 1, TenantID: candidate.TenantID, RunID: candidate.RunID, ToolCallID: candidate.ToolCallID, JobID: candidate.JobID, AttemptID: candidate.AttemptID, Fence: candidate.Fence, LeaseExpiredAt: candidate.LeaseExpiresAt.UTC().Truncate(time.Microsecond), Reason: "tool_execution_lease_expired"}
	unknownPointer, unknownJSON, err := sweeper.putJSON(ctx, candidate.TenantID, fmt.Sprintf("%s:outcome-unknown:%d", candidate.ToolCallID, terminalToolVersion), "tool-effect-evidence", unknown)
	if err != nil {
		return err
	}
	attemptPointer, _, err := sweeper.putJSON(ctx, candidate.TenantID, fmt.Sprintf("%s:expired:%d", candidate.AttemptID, candidate.AttemptVersion+1), "tool-effect-evidence", attempt)
	if err != nil {
		return err
	}
	reconciliation := executionpostgres.EffectCompletion{ReconciliationDueAt: dueAt}
	if candidate.EffectClass == automaticallyReconcilableEffectClass {
		reconcileCommandID, idErr := executionpostgres.ReconcileToolEffectCommandID(sweeper.IDKey, candidate.EffectID, terminalToolVersion)
		if idErr != nil {
			return idErr
		}
		reconcile := CommandPayload{SchemaVersion: 1, TenantID: candidate.TenantID, RunID: candidate.RunID, ToolCallID: candidate.ToolCallID, EffectID: candidate.EffectID, ToolName: candidate.ToolName, DescriptorSnapshotID: candidate.DescriptorSnapshotID, DescriptorHash: source.DescriptorHash, EffectClass: candidate.EffectClass, EffectKey: candidate.EffectKey, EffectScope: candidate.EffectScope, ProviderID: candidate.ProviderID, ProviderRequestID: candidate.ProviderRequestID, RequestHash: candidate.ToolRequestHash, TerminalToolVersion: terminalToolVersion, ReconciliationDueAt: dueAt, OutcomeUnknownAt: now, ReconciliationRound: 1, CorrelationID: correlationID}
		reconcilePointer, _, putErr := sweeper.putJSON(ctx, candidate.TenantID, reconcileCommandID, "tool-reconciliation-command", reconcile)
		if putErr != nil {
			return putErr
		}
		reconciliation.ReconcileCommand = reconcilePointer
		reconciliation.ReconcileQueueClass = sweeper.Reconcile.QueueClass
		reconciliation.ReconcileResource = sweeper.Reconcile.ResourceClass
		reconciliation.ReconcilePriority = sweeper.Reconcile.Priority
		reconciliation.ReconcileCostUnits = sweeper.Reconcile.CostUnits
		reconciliation.ReconcileAttempts = sweeper.Reconcile.MaxAttempts
	}
	hash := sha256.Sum256(unknownJSON)
	_, err = sweeper.Store.SweepExpiredToolEffect(ctx, executionpostgres.SweepExpiredToolEffectCommand{
		Candidate: candidate, ResultHash: hex.EncodeToString(hash[:]), Actor: json.RawMessage(`{"kind":"system","component":"tool-effect-sweeper"}`), CorrelationID: correlationID,
		OutcomeUnknownEvent: unknownPointer, AttemptExpiredEvent: attemptPointer,
		Reconciliation: reconciliation,
	})
	if err == nil && sweeper.Metrics != nil {
		sweeper.Metrics.AddToolCall(ctx, "unknown", true)
	}
	return err
}

type sourceCommandBinding struct {
	SchemaVersion        int    `json:"schema_version"`
	ToolCallID           string `json:"tool_call_id"`
	RunID                string `json:"run_id"`
	UserID               string `json:"user_id"`
	CorrelationID        string `json:"correlation_id"`
	ToolName             string `json:"tool_name"`
	DescriptorSnapshotID string `json:"descriptor_snapshot_id"`
	DescriptorHash       string `json:"descriptor_hash"`
	RequestHash          string `json:"request_hash"`
	EffectClass          string `json:"effect_class"`
	EffectKey            string `json:"effect_key"`
	EffectScope          string `json:"effect_scope"`
	ProviderID           string `json:"provider_id"`
}

func (sweeper Sweeper) loadSourceCommand(ctx context.Context, candidate executionpostgres.ExpiredToolEffectCandidate) (sourceCommandBinding, error) {
	if candidate.SourceCommandRef == "" || candidate.SourceCommandHash == "" || candidate.SourceCommandHash != candidate.RequestHash {
		return sourceCommandBinding{}, ErrConfiguration
	}
	encoded, err := sweeper.Payloads.Get(ctx, payload.Descriptor{TenantID: candidate.TenantID, ObjectID: candidate.CommandID, Class: "tool-execute-command", ContentType: "application/json"}, payload.Manifest{Ref: candidate.SourceCommandRef, Hash: candidate.SourceCommandHash})
	if err != nil || len(encoded) == 0 || len(encoded) > sweeper.MaximumCommand {
		return sourceCommandBinding{}, errors.Join(err, ErrConfiguration)
	}
	var source sourceCommandBinding
	if json.Unmarshal(encoded, &source) != nil || source.SchemaVersion != 1 || source.ToolCallID != candidate.ToolCallID || source.RunID != candidate.RunID || source.UserID != candidate.UserID || source.CorrelationID == "" || source.ToolName != candidate.ToolName || source.DescriptorSnapshotID != candidate.DescriptorSnapshotID || !sha256Pattern.MatchString(source.DescriptorHash) || source.RequestHash != candidate.ToolRequestHash || source.EffectClass != candidate.EffectClass || source.EffectKey != candidate.EffectKey || source.EffectScope != candidate.EffectScope || source.ProviderID != candidate.ProviderID {
		return sourceCommandBinding{}, ErrConfiguration
	}
	return source, nil
}

func (sweeper Sweeper) putJSON(ctx context.Context, tenantID, objectID, class string, value any) (executionpostgres.PayloadPointer, []byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return executionpostgres.PayloadPointer{}, nil, err
	}
	manifest, err := sweeper.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: class, ContentType: "application/json"}, encoded)
	if err != nil || manifest.Ref == "" || manifest.Hash == "" {
		return executionpostgres.PayloadPointer{}, nil, errors.Join(err, ErrConfiguration)
	}
	return executionpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, encoded, nil
}

func (sweeper Sweeper) valid() bool {
	return sweeper.Store != nil && sweeper.Payloads != nil && sweeper.Locker != nil && len(sweeper.IDKey) >= 32 && sweeper.StoreEpoch != "" && sweeper.ReconcileDelay > 0 && sweeper.ReconcileDelay <= 24*time.Hour && validSchedule(sweeper.Reconcile) && sweeper.MaximumCommand >= 1 && sweeper.MaximumCommand <= 16<<20 && sweeper.ShardCount >= 1 && sweeper.ShardCount <= 256 && sweeper.ShardIndex >= 0 && sweeper.ShardIndex < sweeper.ShardCount && sweeper.TenantPage >= 1 && sweeper.TenantPage <= 5000 && sweeper.EffectPage >= 1 && sweeper.EffectPage <= 500
}

func validSchedule(schedule Schedule) bool {
	return (schedule.QueueClass == "interactive" || schedule.QueueClass == "background") && schedule.ResourceClass != "" && schedule.Priority >= 0 && schedule.Priority <= 1000 && schedule.CostUnits > 0 && schedule.CostUnits <= 1_000_000_000_000 && schedule.MaxAttempts > 0 && schedule.MaxAttempts <= 100
}
