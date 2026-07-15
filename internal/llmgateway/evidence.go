package llmgateway

import (
	"context"
	"encoding/json"
	"errors"

	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/payload"
)

var ErrEvidenceConfiguration = errors.New("LLM evidence writer configuration is invalid")

// PayloadEvidence writes sanitized, content-addressed provider evidence. It
// never records credential material or raw provider headers.
type PayloadEvidence struct {
	Payloads payload.Store
}

func (writer PayloadEvidence) BuildProviderResult(ctx context.Context, evidence ProviderEvidence) (llmpostgres.PayloadPointer, llmpostgres.PayloadPointer, error) {
	authorization := evidence.Authorization
	if writer.Payloads == nil || evidence.TenantID == "" || authorization.AttemptID == "" || authorization.ProviderAttemptID == "" || authorization.LLMAttemptID == "" || evidence.Status == "" || evidence.UsageStatus == "" {
		return llmpostgres.PayloadPointer{}, llmpostgres.PayloadPointer{}, ErrEvidenceConfiguration
	}
	errorClass, errorCode, outcomeUnknown := "", "", false
	if evidence.CallError != nil {
		errorClass, errorCode, outcomeUnknown = evidence.CallError.Class, evidence.CallError.Code, evidence.CallError.OutcomeUnknown
	}
	recorded, err := writer.put(ctx, evidence.TenantID, authorization, "provider-result", map[string]any{
		"schema_version": 1, "llm_attempt_id": authorization.LLMAttemptID,
		"provider_attempt_id": authorization.ProviderAttemptID, "dispatch_fence": authorization.DispatchFence,
		"provider_id": authorization.Candidate.ProviderID, "model_id": authorization.Candidate.ModelID,
		"model_version": authorization.Candidate.ModelVersion, "pricing_version": authorization.Candidate.PricingVersion,
		"request_hash": authorization.RequestHash, "response_hash": evidence.Result.ResponseHash,
		"provider_request_id": evidence.Result.ProviderRequestID, "finish_reason": evidence.Result.FinishReason,
		"status": evidence.Status, "usage_status": evidence.UsageStatus,
		"input_tokens": evidence.Accounting.InputTokens, "output_tokens": evidence.Accounting.OutputTokens,
		"cost_microunits": evidence.Accounting.CostMicrounits, "billable_units": evidence.Accounting.BillableUnits,
		"error_class": errorClass, "error_code": errorCode, "outcome_unknown": outcomeUnknown,
	})
	if err != nil {
		return llmpostgres.PayloadPointer{}, llmpostgres.PayloadPointer{}, err
	}
	settled, err := writer.put(ctx, evidence.TenantID, authorization, "usage-settlement", map[string]any{
		"schema_version": 1, "provider_attempt_id": authorization.ProviderAttemptID,
		"usage_status": evidence.UsageStatus, "input_tokens": evidence.Accounting.InputTokens,
		"output_tokens": evidence.Accounting.OutputTokens, "cost_microunits": evidence.Accounting.CostMicrounits,
		"billable_units": evidence.Accounting.BillableUnits,
	})
	return recorded, settled, err
}

func (writer PayloadEvidence) put(ctx context.Context, tenantID string, authorization llmpostgres.DispatchAuthorization, suffix string, value any) (llmpostgres.PayloadPointer, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return llmpostgres.PayloadPointer{}, err
	}
	manifest, err := writer.Payloads.Put(ctx, payload.Descriptor{TenantID: tenantID, ObjectID: authorization.AttemptID + ":" + suffix, Class: "event-payload", ContentType: "application/json"}, encoded)
	return llmpostgres.PayloadPointer{Ref: manifest.Ref, Hash: manifest.Hash}, err
}
