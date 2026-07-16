package agentworker

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	billingpostgres "github.com/langshift/lites/internal/billing/postgres"
	"github.com/langshift/lites/internal/llmgateway"
	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/payload"
)

var (
	ErrTurnConfiguration      = errors.New("agent LLM turn configuration is invalid")
	ErrTurnPlan               = errors.New("agent LLM turn plan is invalid")
	ErrProviderOutcomeUnknown = errors.New("provider outcome requires reconciliation")
)

type LLMAttemptStore interface {
	StartLLMAttempt(context.Context, llmpostgres.StartLLMAttemptCommand) (llmpostgres.LLMAttempt, error)
	IssuePrepareToken() (string, error)
	IssueCompletionToken() (string, error)
	PrepareProviderAttempt(context.Context, llmpostgres.PrepareProviderAttemptCommand) (llmpostgres.PreparedProviderAttempt, error)
	FinalizeLLMAttempt(context.Context, llmpostgres.FinalizeLLMAttemptCommand) (llmpostgres.FinalizedLLMAttempt, error)
}

type UsageReserver interface {
	Reserve(context.Context, billingpostgres.ReserveCommand) (billingpostgres.Reservation, error)
}

type ProviderExecutor interface {
	PrepareRequest(llmpostgres.ModelCandidate, provider.Request) (string, error)
	Execute(context.Context, llmgateway.ExecuteCommand) (llmgateway.Execution, error)
}

type TurnBuilder interface {
	BuildTurn(context.Context, Execution) (TurnPlan, error)
}

type ProviderPlan struct {
	AttemptID, ProviderAttemptID                  string
	ReservationID, ReservationRequestID, BucketID string
	ReservedUnits                                 uint64
	Candidate                                     llmpostgres.ModelCandidate
	BYOK                                          *llmpostgres.BYOKBinding
	FallbackOn                                    []string
}

type TurnPlan struct {
	LLMAttemptID, AttemptKey string
	StreamGeneration         uint64
	Manifest                 llmpostgres.ContextManifest
	RetrievalInput           any
	Request                  provider.Request
	EstimatedInputTokens     uint64
	FailureEstimate          llmgateway.FailureEstimate
	Candidates               []ProviderPlan
	Sink                     provider.DeltaSink
}

type TurnResult struct {
	Plan              TurnPlan
	Provider          provider.Result
	SelectedAttemptID string
	Finalized         llmpostgres.FinalizedLLMAttempt
}

// LLMTurnExecutor composes the logical-attempt, hard credit reservation,
// physical-attempt and provider protocols. Every fallback is a new physical
// attempt with a new reservation and one-shot dispatch authorization.
type LLMTurnExecutor struct {
	Builder             TurnBuilder
	Attempts            LLMAttemptStore
	Billing             UsageReserver
	Gateway             ProviderExecutor
	Payloads            payload.Store
	Actor               json.RawMessage
	PrepareTTL          time.Duration
	CompletionTTL       time.Duration
	ReconciliationDelay time.Duration
	Now                 func() time.Time
	Metrics             WorkerMetrics
}

func (executor LLMTurnExecutor) Execute(ctx context.Context, execution Execution) (TurnResult, error) {
	if !executor.valid() {
		return TurnResult{}, ErrTurnConfiguration
	}
	plan, err := executor.Builder.BuildTurn(ctx, execution)
	if err != nil {
		return TurnResult{}, err
	}
	if !validTurnPlan(plan, execution) {
		return TurnResult{}, ErrTurnPlan
	}
	startedEvent, err := executor.event(ctx, execution.Claim.TenantID, plan.LLMAttemptID+":started", "llm_attempt_started", map[string]any{"run_id": execution.Claim.RunID, "run_attempt_id": execution.Claim.AttemptID, "attempt_key": plan.AttemptKey, "stream_generation": plan.StreamGeneration})
	if err != nil {
		return TurnResult{}, err
	}
	started, err := executor.Attempts.StartLLMAttempt(ctx, llmpostgres.StartLLMAttemptCommand{
		AttemptID: plan.LLMAttemptID, TenantID: execution.Claim.TenantID, UserID: execution.Claim.UserID,
		RunID: execution.Claim.RunID, RunAttemptID: execution.Claim.AttemptID, RunVersion: execution.Claim.RunVersion,
		RunFence: execution.Claim.Fence, StreamGeneration: plan.StreamGeneration, AttemptKey: plan.AttemptKey,
		CorrelationID: execution.Payload.CorrelationID, Manifest: plan.Manifest, Actor: executor.Actor, StartedEvent: startedEvent,
		RetrievalInput: plan.RetrievalInput,
	})
	if err != nil {
		return TurnResult{}, err
	}
	result := TurnResult{Plan: plan}
	fallbackFrom, failureCode := "", ""
	for ordinal, candidate := range plan.Candidates {
		requestHash, prepareErr := executor.Gateway.PrepareRequest(candidate.Candidate, plan.Request)
		if prepareErr != nil {
			return result, prepareErr
		}
		now := executor.now()
		reservedEvent, putErr := executor.event(ctx, execution.Claim.TenantID, candidate.ReservationID+":reserved", "usage_reserved", map[string]any{"provider_attempt_id": candidate.AttemptID, "reserved_units": candidate.ReservedUnits})
		if putErr != nil {
			return result, putErr
		}
		_, err = executor.Billing.Reserve(ctx, billingpostgres.ReserveCommand{
			ReservationID: candidate.ReservationID, RequestID: candidate.ReservationRequestID,
			TenantID: execution.Claim.TenantID, UserID: execution.Claim.UserID, BucketID: candidate.BucketID,
			OperationKey: candidate.ProviderAttemptID, SubjectKind: "provider_attempt", SubjectID: candidate.AttemptID,
			SubjectVersion: 1, ReservedUnits: candidate.ReservedUnits, ExpiresAt: now.Add(executor.PrepareTTL + executor.CompletionTTL),
			CorrelationID: execution.Payload.CorrelationID, Actor: executor.Actor,
			ReservedEvent: billingpostgres.PayloadPointer{Ref: reservedEvent.Ref, Hash: reservedEvent.Hash},
		})
		if err != nil {
			return result, err
		}
		prepareToken, tokenErr := executor.Attempts.IssuePrepareToken()
		if tokenErr != nil {
			return result, tokenErr
		}
		preparedEvent, putErr := executor.event(ctx, execution.Claim.TenantID, candidate.AttemptID+":prepared", "provider_attempt_prepared", map[string]any{"request_hash": requestHash, "ordinal": ordinal + 1, "candidate": candidate.Candidate})
		if putErr != nil {
			return result, putErr
		}
		_, err = executor.Attempts.PrepareProviderAttempt(ctx, llmpostgres.PrepareProviderAttemptCommand{
			AttemptID: candidate.AttemptID, ProviderAttemptID: candidate.ProviderAttemptID, LLMAttemptID: plan.LLMAttemptID,
			UsageReservationID: candidate.ReservationID, TenantID: execution.Claim.TenantID, UserID: execution.Claim.UserID,
			AttemptKey: plan.AttemptKey, Ordinal: ordinal + 1, Candidate: candidate.Candidate, RequestHash: requestHash,
			ContextManifestHash: started.ContextManifestHash, FallbackFromID: fallbackFrom, PrepareToken: prepareToken,
			PrepareTokenExpiresAt: now.Add(executor.PrepareTTL), BYOK: candidate.BYOK,
			CorrelationID: execution.Payload.CorrelationID, Actor: executor.Actor, PreparedEvent: preparedEvent,
		})
		if err != nil {
			return result, err
		}
		completionToken, tokenErr := executor.Attempts.IssueCompletionToken()
		if tokenErr != nil {
			return result, tokenErr
		}
		dispatchEvent, putErr := executor.event(ctx, execution.Claim.TenantID, candidate.AttemptID+":dispatch", "provider_dispatch_authorized", map[string]any{"request_hash": requestHash, "ordinal": ordinal + 1})
		if putErr != nil {
			return result, putErr
		}
		providerStartedAt := executor.now()
		firstSafeTokenOrigin := execution.Claim.CreatedAt
		if firstSafeTokenOrigin.IsZero() {
			firstSafeTokenOrigin = providerStartedAt
		}
		if executor.Metrics != nil {
			executor.Metrics.AddActiveProviderRequests(ctx, 1, candidate.Candidate.ProviderID)
		}
		providerExecution, callErr := executor.Gateway.Execute(ctx, llmgateway.ExecuteCommand{
			Candidate: candidate.Candidate, Request: plan.Request, EstimatedInputTokens: plan.EstimatedInputTokens,
			FailureEstimate: plan.FailureEstimate, Sink: plan.Sink,
			Dispatch: llmpostgres.AuthorizeDispatchCommand{AttemptID: candidate.AttemptID, TenantID: execution.Claim.TenantID,
				RequestHash: requestHash, PrepareToken: prepareToken, CompletionToken: completionToken,
				CompletionDeadline: now.Add(executor.CompletionTTL), CorrelationID: execution.Payload.CorrelationID,
				Actor: executor.Actor, DispatchEvent: dispatchEvent},
			ReconciliationDueAt: now.Add(executor.CompletionTTL + executor.ReconciliationDelay),
		})
		if executor.Metrics != nil {
			executor.Metrics.AddActiveProviderRequests(ctx, -1, candidate.Candidate.ProviderID)
			if providerExecution.Durable {
				tokens := providerExecution.Accounting.InputTokens + providerExecution.Accounting.OutputTokens
				if tokens <= uint64(^uint64(0)>>1) {
					executor.Metrics.AddProviderTokens(ctx, int64(tokens), candidate.Candidate.ProviderID, "custom")
				}
				if visible := providerExecution.ProviderResult.VisibleOutputStartedAt; visible != nil && !visible.Before(firstSafeTokenOrigin) {
					executor.Metrics.ObserveFirstSafeToken(ctx, visible.Sub(firstSafeTokenOrigin), candidate.Candidate.ProviderID)
				}
			}
		}
		if !providerExecution.Durable {
			return result, errors.Join(llmgateway.ErrResultNotRecorded, callErr)
		}
		if callErr == nil {
			result.Provider, result.SelectedAttemptID = providerExecution.ProviderResult, candidate.AttemptID
			finalized, finalizeErr := executor.finalize(ctx, execution, plan, "completed", candidate.AttemptID, providerExecution.ProviderResult.ResponseHash, "")
			result.Finalized = finalized
			return result, finalizeErr
		}
		var classified *provider.CallError
		if !errors.As(callErr, &classified) {
			return result, callErr
		}
		if classified.OutcomeUnknown {
			return result, errors.Join(ErrProviderOutcomeUnknown, classified)
		}
		failureCode = classified.Class
		fallbackFrom = candidate.AttemptID
		if ordinal == len(plan.Candidates)-1 || !containsClass(candidate.FallbackOn, classified.Class) {
			break
		}
	}
	if failureCode == "" {
		failureCode = provider.ClassProviderError
	}
	finalized, err := executor.finalize(ctx, execution, plan, "failed", "", "", failureCode)
	result.Finalized = finalized
	if err != nil {
		return result, err
	}
	return result, &provider.CallError{Class: failureCode}
}

func (executor LLMTurnExecutor) finalize(ctx context.Context, execution Execution, plan TurnPlan, status, selected, resultHash, failureCode string) (llmpostgres.FinalizedLLMAttempt, error) {
	pointer, err := executor.event(ctx, execution.Claim.TenantID, plan.LLMAttemptID+":finalized", "llm_attempt_finalized", map[string]any{"status": status, "selected_provider_attempt_id": selected, "result_hash": resultHash, "failure_code": failureCode})
	if err != nil {
		return llmpostgres.FinalizedLLMAttempt{}, err
	}
	return executor.Attempts.FinalizeLLMAttempt(ctx, llmpostgres.FinalizeLLMAttemptCommand{AttemptID: plan.LLMAttemptID, TenantID: execution.Claim.TenantID, Status: status, SelectedProviderAttemptID: selected, ResultHash: resultHash, FailureCode: failureCode, CorrelationID: execution.Payload.CorrelationID, Actor: executor.Actor, FinalizedEvent: pointer})
}

func (executor LLMTurnExecutor) event(ctx context.Context, tenantID, objectID, eventType string, data any) (llmpostgres.PayloadPointer, error) {
	encoded, err := json.Marshal(map[string]any{"schema_version": 1, "event_type": eventType, "data": data})
	if err != nil {
		return llmpostgres.PayloadPointer{}, err
	}
	manifest, err := executor.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: objectID, Class: "event-payload", ContentType: "application/json"}, encoded)
	return llmpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, err
}

func (executor LLMTurnExecutor) valid() bool {
	var actor map[string]any
	return executor.Builder != nil && executor.Attempts != nil && executor.Billing != nil && executor.Gateway != nil && executor.Payloads != nil && executor.PrepareTTL > 0 && executor.CompletionTTL > 0 && executor.ReconciliationDelay > 0 && json.Unmarshal(executor.Actor, &actor) == nil && actor != nil
}

func validTurnPlan(plan TurnPlan, execution Execution) bool {
	if plan.LLMAttemptID == "" || plan.AttemptKey == "" || plan.StreamGeneration < 1 || plan.EstimatedInputTokens < 1 || len(plan.Candidates) < 1 || len(plan.Candidates) > 100 || len(plan.Manifest.CandidateModels) != len(plan.Candidates) {
		return false
	}
	seenAttempt, seenProvider, seenReservation := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for index, candidate := range plan.Candidates {
		if candidate.AttemptID == "" || candidate.ProviderAttemptID == "" || candidate.ReservationID == "" || candidate.ReservationRequestID == "" || candidate.BucketID == "" || candidate.ReservedUnits < 1 || candidate.Candidate != plan.Manifest.CandidateModels[index] || seenAttempt[candidate.AttemptID] || seenProvider[candidate.ProviderAttemptID] || seenReservation[candidate.ReservationID] || !validFallbackClasses(candidate.FallbackOn) {
			return false
		}
		seenAttempt[candidate.AttemptID], seenProvider[candidate.ProviderAttemptID], seenReservation[candidate.ReservationID] = true, true, true
	}
	return execution.Claim.RunID == execution.Payload.RunID && execution.Payload.CorrelationID != ""
}

func validFallbackClasses(classes []string) bool {
	seen := map[string]bool{}
	for _, class := range classes {
		switch class {
		case provider.ClassRateLimited, provider.ClassContextTooLong, provider.ClassContentFiltered,
			provider.ClassModelUnavailable, provider.ClassAuthFailed, provider.ClassProviderError,
			provider.ClassTimeout, provider.ClassInvalidRequest, provider.ClassCancelled:
		default:
			return false
		}
		if seen[class] {
			return false
		}
		seen[class] = true
	}
	return true
}

func containsClass(classes []string, expected string) bool {
	for _, class := range classes {
		if class == expected {
			return true
		}
	}
	return false
}

func (executor LLMTurnExecutor) now() time.Time {
	if executor.Now != nil {
		return executor.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}
