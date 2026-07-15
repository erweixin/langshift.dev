package llmgateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/llmgateway/egress"
	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/llmgateway/provider"
)

type executorClientFunc func(context.Context, string, string, http.Header, io.Reader) (*http.Response, error)

func (function executorClientFunc) Do(ctx context.Context, method, path string, headers http.Header, body io.Reader) (*http.Response, error) {
	return function(ctx, method, path, headers, body)
}

func executorHTTPResponse(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

type dispatchStoreStub struct {
	authorization llmpostgres.DispatchAuthorization
	recordCommand llmpostgres.RecordProviderResultCommand
	sequence      *[]string
	authorizeErr  error
	recordErr     error
}

func (store *dispatchStoreStub) AuthorizeDispatch(_ context.Context, command llmpostgres.AuthorizeDispatchCommand) (llmpostgres.DispatchAuthorization, error) {
	*store.sequence = append(*store.sequence, "authorize")
	if command.RequestHash != store.authorization.RequestHash {
		return llmpostgres.DispatchAuthorization{}, errors.New("hash drift")
	}
	return store.authorization, store.authorizeErr
}

func (store *dispatchStoreStub) RecordProviderResult(_ context.Context, command llmpostgres.RecordProviderResultCommand) (llmpostgres.ProviderResult, error) {
	*store.sequence = append(*store.sequence, "record")
	store.recordCommand = command
	return llmpostgres.ProviderResult{AttemptID: command.AttemptID, Status: command.Status, UsageStatus: command.UsageStatus, Version: 3}, store.recordErr
}

type clientBuilderFunc func(context.Context, provider.ResolvedProvider, provider.ManagedCredential) (provider.Client, error)

func (function clientBuilderFunc) Build(ctx context.Context, resolved provider.ResolvedProvider, credential provider.ManagedCredential) (provider.Client, error) {
	return function(ctx, resolved, credential)
}

type evidenceBuilderFunc func(context.Context, ProviderEvidence) (llmpostgres.PayloadPointer, llmpostgres.PayloadPointer, error)

func (function evidenceBuilderFunc) BuildProviderResult(ctx context.Context, evidence ProviderEvidence) (llmpostgres.PayloadPointer, llmpostgres.PayloadPointer, error) {
	return function(ctx, evidence)
}

func executorRegistry(t *testing.T) provider.Registry {
	t.Helper()
	registry, err := provider.LoadRegistry(strings.NewReader(`{
      "schema_version":1,"snapshot_id":"registry","version":1,"providers":[{
        "id":"openai-primary","type":"openai","status":"active","endpoint":"https://api.openai.com/v1/","bound_host":"api.openai.com","region":"us-east-1",
        "credential":{"mode":"bearer","secret_ref":"lites/providers/openai","secret_version":"4"},
        "request_timeout":"2m","maximum_request_bytes":8388608,"maximum_response_bytes":33554432,
        "models":[{"id":"reasoning","version":"2026-07-01","wire_model":"gpt-5.6-2026-07-01","pricing_version":"price-v1","capabilities":["text","tool_use","streaming"],"maximum_input_tokens":1000000,"maximum_output_tokens":128000,"input_microunits_per_million":5000000,"output_microunits_per_million":30000000,"input_credit_units_per_million":1000000,"output_credit_units_per_million":4000000}]
      }]}`))
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func executorCandidate() llmpostgres.ModelCandidate {
	return llmpostgres.ModelCandidate{ProviderID: "openai-primary", ModelID: "reasoning", ModelVersion: "2026-07-01", BoundHost: "api.openai.com", PricingVersion: "price-v1"}
}

func executorRequest() provider.Request {
	return provider.Request{MaxOutputTokens: 100, Messages: []provider.Message{{Role: "user", Content: []provider.ContentBlock{{Type: "text", Text: "hello"}}}}}
}

func executorDispatch(hash string) llmpostgres.AuthorizeDispatchCommand {
	return llmpostgres.AuthorizeDispatchCommand{AttemptID: "attempt-1", TenantID: "tenant-1", RequestHash: hash, PrepareToken: "prepare-token", CompletionToken: "completion-token", CompletionDeadline: time.Now().Add(time.Minute), CorrelationID: "correlation-1", Actor: json.RawMessage(`{"kind":"service"}`), DispatchEvent: llmpostgres.PayloadPointer{Ref: "encrypted://dispatch", Hash: "dispatch-hash"}}
}

func TestExecutorAuthorizesBeforeSendAndRecordsExactBYOKUsage(t *testing.T) {
	registry, candidate := executorRegistry(t), executorCandidate()
	sequence := []string{}
	store := &dispatchStoreStub{sequence: &sequence}
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	executor := Executor{Store: store, Registry: registry, Now: func() time.Time { return now }}
	hash, err := executor.PrepareRequest(candidate, executorRequest())
	if err != nil {
		t.Fatal(err)
	}
	store.authorization = llmpostgres.DispatchAuthorization{AttemptID: "attempt-1", LLMAttemptID: "llm-1", ProviderAttemptID: "provider-attempt-1", Candidate: candidate, RequestHash: hash, DispatchFence: 1, CompletionDeadline: now.Add(time.Minute), BYOK: &llmpostgres.AuthorizedCredential{CredentialID: "credential-1", Version: 7, SecretRef: "lites/byok/tenant/credential", SecretVersion: "11"}}
	executor.Clients = clientBuilderFunc(func(_ context.Context, resolved provider.ResolvedProvider, credential provider.ManagedCredential) (provider.Client, error) {
		sequence = append(sequence, "build")
		if resolved.Model.WireModel != "gpt-5.6-2026-07-01" || credential.SecretRef != "lites/byok/tenant/credential" || credential.SecretVersion != "11" || credential.Mode != "bearer" {
			t.Fatalf("resolved=%#v credential=%#v", resolved, credential)
		}
		return executorClientFunc(func(_ context.Context, _, _ string, _ http.Header, body io.Reader) (*http.Response, error) {
			sequence = append(sequence, "send")
			payload, _ := io.ReadAll(body)
			if !strings.Contains(string(payload), `"model":"gpt-5.6-2026-07-01"`) {
				t.Fatalf("wire model not pinned: %s", payload)
			}
			return executorHTTPResponse(200, "application/json", `{"id":"resp_1","model":"gpt-5.6-2026-07-01","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":100,"output_tokens":10}}`), nil
		}), nil
	})
	executor.Evidence = evidenceBuilderFunc(func(_ context.Context, evidence ProviderEvidence) (llmpostgres.PayloadPointer, llmpostgres.PayloadPointer, error) {
		sequence = append(sequence, "evidence")
		if evidence.Status != "completed" || evidence.Accounting.CostMicrounits != 0 || evidence.Accounting.BillableUnits != 140 {
			t.Fatalf("evidence=%#v", evidence)
		}
		return llmpostgres.PayloadPointer{Ref: "encrypted://recorded", Hash: "recorded-hash"}, llmpostgres.PayloadPointer{Ref: "encrypted://settled", Hash: "settled-hash"}, nil
	})
	execution, err := executor.Execute(t.Context(), ExecuteCommand{Candidate: candidate, Request: executorRequest(), EstimatedInputTokens: 100, Dispatch: executorDispatch(hash), ReconciliationDueAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(sequence, ",") != "authorize,build,send,evidence,record" || !execution.Dispatched || !execution.Durable || execution.ProviderResult.Text != "ok" || store.recordCommand.Status != "completed" || store.recordCommand.CostMicrounits != 0 || store.recordCommand.BillableUnits != 140 || store.recordCommand.ProviderRequestID != "resp_1" {
		t.Fatalf("sequence=%v execution=%#v record=%#v", sequence, execution, store.recordCommand)
	}
}

func TestExecutorRejectsHashDriftBeforeAuthorization(t *testing.T) {
	sequence := []string{}
	executor := Executor{Store: &dispatchStoreStub{sequence: &sequence}, Registry: executorRegistry(t), Clients: clientBuilderFunc(func(context.Context, provider.ResolvedProvider, provider.ManagedCredential) (provider.Client, error) {
		return nil, errors.New("must not build")
	}), Evidence: evidenceBuilderFunc(func(context.Context, ProviderEvidence) (llmpostgres.PayloadPointer, llmpostgres.PayloadPointer, error) {
		return llmpostgres.PayloadPointer{}, llmpostgres.PayloadPointer{}, errors.New("must not build")
	})}
	_, err := executor.Execute(t.Context(), ExecuteCommand{Candidate: executorCandidate(), Request: executorRequest(), EstimatedInputTokens: 1, Dispatch: executorDispatch("wrong-hash")})
	if !errors.Is(err, ErrDispatchBinding) || len(sequence) != 0 {
		t.Fatalf("hash drift sequence=%v err=%v", sequence, err)
	}
}

func TestExecutorRecordsAmbiguousNetworkFailureAsOutcomeUnknown(t *testing.T) {
	registry, candidate := executorRegistry(t), executorCandidate()
	sequence := []string{}
	store := &dispatchStoreStub{sequence: &sequence}
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	executor := Executor{Store: store, Registry: registry, Now: func() time.Time { return now }}
	hash, _ := executor.PrepareRequest(candidate, executorRequest())
	store.authorization = llmpostgres.DispatchAuthorization{AttemptID: "attempt-1", Candidate: candidate, RequestHash: hash, DispatchFence: 1, CompletionDeadline: now.Add(time.Minute)}
	executor.Clients = clientBuilderFunc(func(context.Context, provider.ResolvedProvider, provider.ManagedCredential) (provider.Client, error) {
		return executorClientFunc(func(context.Context, string, string, http.Header, io.Reader) (*http.Response, error) {
			return nil, &egress.SendError{MayHaveSent: true, Cause: context.DeadlineExceeded}
		}), nil
	})
	executor.Evidence = evidenceBuilderFunc(func(_ context.Context, evidence ProviderEvidence) (llmpostgres.PayloadPointer, llmpostgres.PayloadPointer, error) {
		if evidence.Status != "outcome_unknown" || evidence.UsageStatus != "unknown" || evidence.CallError == nil || !evidence.CallError.OutcomeUnknown {
			t.Fatalf("evidence=%#v", evidence)
		}
		return llmpostgres.PayloadPointer{Ref: "encrypted://unknown", Hash: "unknown-hash"}, llmpostgres.PayloadPointer{}, nil
	})
	execution, err := executor.Execute(t.Context(), ExecuteCommand{Candidate: candidate, Request: executorRequest(), EstimatedInputTokens: 100, Dispatch: executorDispatch(hash), ReconciliationDueAt: now.Add(time.Hour)})
	var callErr *provider.CallError
	if !errors.As(err, &callErr) || callErr.Class != provider.ClassTimeout || !callErr.OutcomeUnknown || !execution.Durable || store.recordCommand.Status != "outcome_unknown" || store.recordCommand.UsageStatus != "unknown" || store.recordCommand.ResponseHash != "" || store.recordCommand.ReconciliationDueAt == nil || store.recordCommand.SettledEvent.Ref != "" {
		t.Fatalf("execution=%#v record=%#v err=%#v", execution, store.recordCommand, err)
	}
}

func TestExecutorRecordsLocalCredentialFailureAsKnownFailure(t *testing.T) {
	registry, candidate := executorRegistry(t), executorCandidate()
	sequence := []string{}
	store := &dispatchStoreStub{sequence: &sequence}
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	executor := Executor{Store: store, Registry: registry, Now: func() time.Time { return now }}
	hash, _ := executor.PrepareRequest(candidate, executorRequest())
	store.authorization = llmpostgres.DispatchAuthorization{AttemptID: "attempt-1", Candidate: candidate, RequestHash: hash, DispatchFence: 1, CompletionDeadline: now.Add(time.Minute)}
	executor.Clients = clientBuilderFunc(func(context.Context, provider.ResolvedProvider, provider.ManagedCredential) (provider.Client, error) {
		return executorClientFunc(func(context.Context, string, string, http.Header, io.Reader) (*http.Response, error) {
			return nil, &egress.SendError{MayHaveSent: false, Class: provider.ClassAuthFailed, Cause: egress.ErrInvalidCredential}
		}), nil
	})
	executor.Evidence = evidenceBuilderFunc(func(_ context.Context, evidence ProviderEvidence) (llmpostgres.PayloadPointer, llmpostgres.PayloadPointer, error) {
		return llmpostgres.PayloadPointer{Ref: "encrypted://failed", Hash: "failed-hash"}, llmpostgres.PayloadPointer{Ref: "encrypted://settled", Hash: "settled-hash"}, nil
	})
	_, err := executor.Execute(t.Context(), ExecuteCommand{Candidate: candidate, Request: executorRequest(), EstimatedInputTokens: 100, FailureEstimate: FailureEstimate{InputTokens: 100}, Dispatch: executorDispatch(hash), ReconciliationDueAt: now.Add(time.Hour)})
	var callErr *provider.CallError
	if !errors.As(err, &callErr) || callErr.Class != provider.ClassAuthFailed || callErr.OutcomeUnknown || store.recordCommand.Status != "failed" || store.recordCommand.UsageStatus != "estimated" || store.recordCommand.InputTokens != 100 || store.recordCommand.ErrorClass != provider.ClassAuthFailed || store.recordCommand.ReconciliationDueAt != nil {
		t.Fatalf("record=%#v err=%#v", store.recordCommand, err)
	}
}

func TestExecutorRecordsProviderModelDriftWithConfirmedUsage(t *testing.T) {
	registry, candidate := executorRegistry(t), executorCandidate()
	sequence := []string{}
	store := &dispatchStoreStub{sequence: &sequence}
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	executor := Executor{Store: store, Registry: registry, Now: func() time.Time { return now }}
	hash, _ := executor.PrepareRequest(candidate, executorRequest())
	store.authorization = llmpostgres.DispatchAuthorization{AttemptID: "attempt-1", Candidate: candidate, RequestHash: hash, DispatchFence: 1, CompletionDeadline: now.Add(time.Minute)}
	executor.Clients = clientBuilderFunc(func(context.Context, provider.ResolvedProvider, provider.ManagedCredential) (provider.Client, error) {
		return executorClientFunc(func(context.Context, string, string, http.Header, io.Reader) (*http.Response, error) {
			return executorHTTPResponse(200, "application/json", `{"id":"resp_drift","model":"provider-hidden-fallback","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"drifted"}]}],"usage":{"input_tokens":100,"output_tokens":10}}`), nil
		}), nil
	})
	executor.Evidence = evidenceBuilderFunc(func(_ context.Context, evidence ProviderEvidence) (llmpostgres.PayloadPointer, llmpostgres.PayloadPointer, error) {
		if evidence.Status != "failed" || evidence.UsageStatus != "confirmed" || evidence.CallError == nil || evidence.CallError.Class != provider.ClassModelUnavailable {
			t.Fatalf("evidence=%#v", evidence)
		}
		return llmpostgres.PayloadPointer{Ref: "encrypted://drift", Hash: "drift-hash"}, llmpostgres.PayloadPointer{Ref: "encrypted://settled", Hash: "settled-hash"}, nil
	})
	_, err := executor.Execute(t.Context(), ExecuteCommand{Candidate: candidate, Request: executorRequest(), EstimatedInputTokens: 100, Dispatch: executorDispatch(hash), ReconciliationDueAt: now.Add(time.Hour)})
	var callErr *provider.CallError
	if !errors.As(err, &callErr) || callErr.Class != provider.ClassModelUnavailable || store.recordCommand.Status != "failed" || store.recordCommand.UsageStatus != "confirmed" || store.recordCommand.InputTokens != 100 || store.recordCommand.OutputTokens != 10 || store.recordCommand.CostMicrounits != 800 || store.recordCommand.BillableUnits != 140 || store.recordCommand.ResponseHash == "" {
		t.Fatalf("record=%#v err=%#v", store.recordCommand, err)
	}
}
