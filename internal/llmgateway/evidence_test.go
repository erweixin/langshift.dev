package llmgateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/payload"
)

type evidencePayloads struct {
	descriptors []payload.Descriptor
	values      [][]byte
}

func (store *evidencePayloads) Put(_ context.Context, descriptor payload.Descriptor, value []byte) (payload.Manifest, error) {
	store.descriptors = append(store.descriptors, descriptor)
	store.values = append(store.values, append([]byte(nil), value...))
	digest := sha256.Sum256(value)
	hash := hex.EncodeToString(digest[:])
	return payload.Manifest{Ref: "encrypted://" + descriptor.ObjectID + "/" + hash, Hash: hash}, nil
}

func (*evidencePayloads) Get(context.Context, payload.Descriptor, payload.Manifest) ([]byte, error) {
	return nil, nil
}

func TestPayloadEvidenceWritesSanitizedResultAndSettlement(t *testing.T) {
	store := &evidencePayloads{}
	writer := PayloadEvidence{Payloads: store}
	recorded, settled, err := writer.BuildProviderResult(t.Context(), ProviderEvidence{
		TenantID:      "tenant-1",
		Authorization: llmpostgres.DispatchAuthorization{AttemptID: "attempt-1", LLMAttemptID: "llm-1", ProviderAttemptID: "provider-wire-1", RequestHash: "request-hash", DispatchFence: 1, Candidate: llmpostgres.ModelCandidate{ProviderID: "openai", ModelID: "gpt", ModelVersion: "2026-07-01", PricingVersion: "pricing-1"}},
		Result:        provider.Result{ProviderRequestID: "request-provider-1", ResponseHash: "response-hash", FinishReason: "stop"},
		Accounting:    Accounting{InputTokens: 20, OutputTokens: 5, CostMicrounits: 7, BillableUnits: 3},
		Status:        "completed", UsageStatus: "confirmed",
	})
	if err != nil || recorded.Hash == "" || settled.Hash == "" || len(store.values) != 2 {
		t.Fatalf("recorded=%#v settled=%#v err=%v", recorded, settled, err)
	}
	for _, descriptor := range store.descriptors {
		if descriptor.TenantID != "tenant-1" || descriptor.Class != "event-payload" {
			t.Fatalf("descriptor=%#v", descriptor)
		}
	}
	var result map[string]any
	if json.Unmarshal(store.values[0], &result) != nil || result["provider_id"] != "openai" || result["request_hash"] != "request-hash" {
		t.Fatalf("result evidence=%s", store.values[0])
	}
	for _, forbidden := range []string{"authorization", "api_key", "secret_ref", "headers"} {
		if _, exists := result[forbidden]; exists {
			t.Fatalf("sensitive field %q was persisted", forbidden)
		}
	}
}

func TestPayloadEvidenceRejectsMissingTenantScope(t *testing.T) {
	_, _, err := (PayloadEvidence{Payloads: &evidencePayloads{}}).BuildProviderResult(t.Context(), ProviderEvidence{Authorization: llmpostgres.DispatchAuthorization{AttemptID: "attempt", LLMAttemptID: "llm", ProviderAttemptID: "provider"}, Status: "failed", UsageStatus: "estimated"})
	if err != ErrEvidenceConfiguration {
		t.Fatalf("error=%v", err)
	}
}
