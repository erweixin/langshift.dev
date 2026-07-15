package llmgateway

import (
	"context"
	"errors"
	"time"

	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/llmgateway/provider"
)

var (
	ErrExecutorConfiguration = errors.New("LLM executor configuration is invalid")
	ErrDispatchBinding       = errors.New("provider request does not match durable dispatch binding")
	ErrResultNotRecorded     = errors.New("provider result was not durably recorded")
)

type DispatchStore interface {
	AuthorizeDispatch(context.Context, llmpostgres.AuthorizeDispatchCommand) (llmpostgres.DispatchAuthorization, error)
	RecordProviderResult(context.Context, llmpostgres.RecordProviderResultCommand) (llmpostgres.ProviderResult, error)
}

type ClientBuilder interface {
	Build(context.Context, provider.ResolvedProvider, provider.ManagedCredential) (provider.Client, error)
}

type EvidenceBuilder interface {
	BuildProviderResult(context.Context, ProviderEvidence) (llmpostgres.PayloadPointer, llmpostgres.PayloadPointer, error)
}

type ProviderEvidence struct {
	Authorization llmpostgres.DispatchAuthorization
	Result        provider.Result
	CallError     *provider.CallError
	Accounting    Accounting
	Status        string
	UsageStatus   string
}

type FailureEstimate struct {
	InputTokens, OutputTokens uint64
}

type ExecuteCommand struct {
	Candidate            llmpostgres.ModelCandidate
	Request              provider.Request
	EstimatedInputTokens uint64
	FailureEstimate      FailureEstimate
	Dispatch             llmpostgres.AuthorizeDispatchCommand
	ReconciliationDueAt  time.Time
	Sink                 provider.DeltaSink
}

type Execution struct {
	Authorization  llmpostgres.DispatchAuthorization
	ProviderResult provider.Result
	Accounting     Accounting
	Recorded       llmpostgres.ProviderResult
	Dispatched     bool
	Durable        bool
}

type Executor struct {
	Store          DispatchStore
	Registry       provider.Registry
	Clients        ClientBuilder
	Evidence       EvidenceBuilder
	AdapterFactory func(provider.ResolvedProvider) (provider.Adapter, error)
	Now            func() time.Time
}

func (executor Executor) PrepareRequest(candidate llmpostgres.ModelCandidate, request provider.Request) (string, error) {
	_, _, prepared, err := executor.prepare(candidate, request)
	if err != nil {
		return "", err
	}
	return prepared.RequestHash(), nil
}

func (executor Executor) Execute(ctx context.Context, command ExecuteCommand) (Execution, error) {
	if ctx == nil || executor.Store == nil || executor.Clients == nil || executor.Evidence == nil || command.Dispatch.RequestHash == "" || command.Dispatch.CompletionToken == "" || command.EstimatedInputTokens == 0 {
		return Execution{}, ErrExecutorConfiguration
	}
	resolved, adapter, prepared, err := executor.prepare(command.Candidate, command.Request)
	if err != nil {
		return Execution{}, err
	}
	if command.EstimatedInputTokens > resolved.Model.MaximumInputTokens || prepared.RequestHash() != command.Dispatch.RequestHash {
		return Execution{}, ErrDispatchBinding
	}
	authorization, err := executor.Store.AuthorizeDispatch(ctx, command.Dispatch)
	if err != nil {
		return Execution{}, err
	}
	execution := Execution{Authorization: authorization}
	if authorization.Candidate != command.Candidate || authorization.RequestHash != prepared.RequestHash() || authorization.DispatchFence != 1 {
		return execution, ErrDispatchBinding
	}
	byokRef, byokVersion := "", ""
	if authorization.BYOK != nil {
		byokRef, byokVersion = authorization.BYOK.SecretRef, authorization.BYOK.SecretVersion
	}
	credential, err := resolved.Credential(byokRef, byokVersion)
	if err != nil {
		return executor.record(ctx, command, execution, resolved.Model, provider.Result{RequestHash: prepared.RequestHash()}, &provider.CallError{Class: provider.ClassAuthFailed, Cause: err, OutcomeUnknown: false})
	}
	client, err := executor.Clients.Build(ctx, resolved, credential)
	if err != nil {
		return executor.record(ctx, command, execution, resolved.Model, provider.Result{RequestHash: prepared.RequestHash()}, &provider.CallError{Class: provider.ClassProviderError, Cause: err, OutcomeUnknown: false})
	}
	execution.Dispatched = true
	result, callErr := adapter.ExecutePrepared(ctx, client, prepared, command.Sink)
	execution.ProviderResult = result
	if callErr == nil {
		if result.Model != resolved.Model.WireModel || result.RequestHash != prepared.RequestHash() || result.ProviderRequestID == "" || result.ResponseHash == "" {
			return executor.record(ctx, command, execution, resolved.Model, result, &provider.CallError{Class: provider.ClassModelUnavailable, ProviderRequestID: result.ProviderRequestID, OutcomeUnknown: false, Cause: ErrDispatchBinding})
		}
		return executor.record(ctx, command, execution, resolved.Model, result, nil)
	}
	var classified *provider.CallError
	if !errors.As(callErr, &classified) {
		classified = &provider.CallError{Class: provider.ClassProviderError, Cause: callErr, OutcomeUnknown: true}
	}
	return executor.record(ctx, command, execution, resolved.Model, result, classified)
}

func (executor Executor) prepare(candidate llmpostgres.ModelCandidate, request provider.Request) (provider.ResolvedProvider, provider.Adapter, provider.PreparedRequest, error) {
	resolved, err := executor.Registry.Resolve(candidate.ProviderID, candidate.ModelID, candidate.ModelVersion, candidate.BoundHost, candidate.PricingVersion)
	if err != nil {
		return provider.ResolvedProvider{}, nil, provider.PreparedRequest{}, err
	}
	if request.Model != "" && request.Model != resolved.Model.WireModel {
		return provider.ResolvedProvider{}, nil, provider.PreparedRequest{}, ErrDispatchBinding
	}
	request.Model = resolved.Model.WireModel
	factory := executor.AdapterFactory
	if factory == nil {
		factory = NewAdapter
	}
	adapter, err := factory(resolved)
	if err != nil {
		return provider.ResolvedProvider{}, nil, provider.PreparedRequest{}, err
	}
	prepared, err := adapter.Prepare(request)
	return resolved, adapter, prepared, err
}

func (executor Executor) record(ctx context.Context, command ExecuteCommand, execution Execution, model provider.ModelDefinition, result provider.Result, callError *provider.CallError) (Execution, error) {
	execution.ProviderResult = result
	status, usageStatus := "completed", "confirmed"
	accounting, err := calculateAccounting(model, result.InputTokens, result.OutputTokens, execution.Authorization.BYOK != nil, usageStatus)
	if callError != nil && callError.OutcomeUnknown {
		status, usageStatus, accounting = "outcome_unknown", "unknown", Accounting{UsageStatus: "unknown"}
		if !command.ReconciliationDueAt.After(executor.now()) {
			return execution, errors.Join(ErrResultNotRecorded, ErrExecutorConfiguration)
		}
	} else if callError != nil {
		status = "failed"
		if result.ResponseHash != "" {
			usageStatus = "confirmed"
			accounting, err = calculateAccounting(model, result.InputTokens, result.OutputTokens, execution.Authorization.BYOK != nil, usageStatus)
		} else {
			usageStatus = "estimated"
			accounting, err = calculateAccounting(model, command.FailureEstimate.InputTokens, command.FailureEstimate.OutputTokens, execution.Authorization.BYOK != nil, usageStatus)
		}
	}
	if err != nil {
		return execution, errors.Join(ErrResultNotRecorded, err)
	}
	execution.Accounting = accounting
	evidence := ProviderEvidence{Authorization: execution.Authorization, Result: result, CallError: callError, Accounting: accounting, Status: status, UsageStatus: usageStatus}
	recordedPointer, settledPointer, err := executor.Evidence.BuildProviderResult(ctx, evidence)
	if err != nil {
		return execution, errors.Join(ErrResultNotRecorded, err)
	}
	providerRequestID, errorClass := result.ProviderRequestID, ""
	if callError != nil {
		errorClass = callError.Class
		if providerRequestID == "" {
			providerRequestID = callError.ProviderRequestID
		}
	}
	reconciliationDue := (*time.Time)(nil)
	responseHash := result.ResponseHash
	if status == "outcome_unknown" {
		value := command.ReconciliationDueAt.UTC().Truncate(time.Microsecond)
		reconciliationDue = &value
		settledPointer = llmpostgres.PayloadPointer{}
		responseHash = ""
	}
	recorded, err := executor.Store.RecordProviderResult(ctx, llmpostgres.RecordProviderResultCommand{
		AttemptID: execution.Authorization.AttemptID, TenantID: command.Dispatch.TenantID, CompletionToken: command.Dispatch.CompletionToken,
		Status: status, ResponseHash: responseHash, UsageStatus: usageStatus, ProviderRequestID: providerRequestID, ErrorClass: errorClass,
		InputTokens: accounting.InputTokens, OutputTokens: accounting.OutputTokens, CostMicrounits: accounting.CostMicrounits, BillableUnits: accounting.BillableUnits,
		VisibleOutputStartedAt: result.VisibleOutputStartedAt, ReconciliationDueAt: reconciliationDue,
		CorrelationID: command.Dispatch.CorrelationID, Actor: command.Dispatch.Actor, RecordedEvent: recordedPointer, SettledEvent: settledPointer,
	})
	if err != nil {
		return execution, errors.Join(ErrResultNotRecorded, err)
	}
	execution.Recorded, execution.Durable = recorded, true
	if callError != nil {
		return execution, callError
	}
	return execution, nil
}

func (executor Executor) now() time.Time {
	if executor.Now != nil {
		return executor.Now().UTC()
	}
	return time.Now().UTC()
}
