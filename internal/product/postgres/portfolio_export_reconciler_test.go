package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/langshift/lites/internal/payload"
)

type portfolioReconcilerPayloadStore struct {
	values map[string][]byte
}

func (store portfolioReconcilerPayloadStore) Put(context.Context, payload.Descriptor, []byte) (payload.Manifest, error) {
	return payload.Manifest{}, errors.New("unexpected put")
}

func (store portfolioReconcilerPayloadStore) Get(_ context.Context, descriptor payload.Descriptor, manifest payload.Manifest) ([]byte, error) {
	value, found := store.values[descriptor.ObjectID+"\x00"+manifest.Ref+"\x00"+manifest.Hash]
	if !found {
		return nil, errors.New("unexpected payload descriptor")
	}
	return append([]byte(nil), value...), nil
}

func TestPortfolioExportReconcilerLoadsDirectAndReconciledReceipts(t *testing.T) {
	item := portfolioFinalizationCandidate{ExportID: "b5000000-0000-4000-8000-000000000001", RunID: "b5000000-0000-4000-8000-000000000002", Format: "pdf"}
	receipt := portfolioReceipt{SchemaVersion: 1, TargetKind: "portfolio_export", TargetID: item.ExportID, ObjectRef: "s3://portfolio-artifacts/exports/result.pdf", ObjectVersion: "version-1", ContentHash: strings.Repeat("a", 64), MediaType: "application/pdf", ByteSize: 4096, ScanResultHash: strings.Repeat("b", 64)}
	receiptBody, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		tool      portfolioToolCandidate
		eventBody []byte
		result    []byte
		objectID  string
	}{
		{
			name:   "direct tool completion",
			tool:   portfolioToolCandidate{ToolCallID: "b5000000-0000-4000-8000-000000000003", EventRef: "event-direct", EventHash: strings.Repeat("c", 64), OriginalAttemptID: "b5000000-0000-4000-8000-000000000004", ExternalResourceRef: receipt.ObjectRef},
			result: receiptBody,
		},
		{
			name: "reconciliation confirmation",
			tool: portfolioToolCandidate{ToolCallID: "b5000000-0000-4000-8000-000000000005", EventRef: "event-reconciled", EventHash: strings.Repeat("d", 64), OriginalAttemptID: "b5000000-0000-4000-8000-000000000006", ReconciliationAttemptID: "b5000000-0000-4000-8000-000000000007", ExternalResourceRef: receipt.ObjectRef},
		},
	}
	for index := range tests {
		test := &tests[index]
		if test.name == "direct tool completion" {
			data := directArtifactToolEvent{SchemaVersion: 1, ToolCallID: test.tool.ToolCallID, RunID: item.RunID, AttemptID: test.tool.OriginalAttemptID, Fence: 1, ToolName: "artifact_export", DescriptorSnapshotID: "artifact_export@sha256:" + strings.Repeat("e", 64), RequestHash: strings.Repeat("f", 64), EffectClass: "reconcilable_write", ProviderRequestID: "b5000000-0000-4000-8000-000000000008", ResultRef: "result-direct", ResultPayloadHash: strings.Repeat("1", 64), ResultHash: strings.Repeat("2", 64), TargetState: "succeeded", ExternalResourceRef: receipt.ObjectRef, EffectDisposition: "confirmed", PolicySnapshotID: "policy-direct", PolicySnapshotHash: strings.Repeat("3", 64), OverlayVersion: 1}
			test.eventBody, err = json.Marshal(map[string]any{"event_type": "tool_call_completed", "data": data})
			test.objectID = test.tool.OriginalAttemptID + ":tool-completed"
		} else {
			data := reconciledArtifactToolEvent{SchemaVersion: 1, ToolCallID: test.tool.ToolCallID, RunID: item.RunID, EffectID: "b5000000-0000-4000-8000-000000000009", AttemptID: test.tool.ReconciliationAttemptID, Fence: 2, ToolName: "artifact_export", DescriptorSnapshotID: "artifact_export@sha256:" + strings.Repeat("4", 64), DescriptorHash: strings.Repeat("5", 64), EffectClass: "reconcilable_write", EffectKey: "portfolio_export:" + item.ExportID, EffectScope: "tenant:portfolio_export:" + item.ExportID, ProviderID: "lites-artifact-store", ProviderRequestID: "b5000000-0000-4000-8000-000000000010", ReconciliationRound: 1, Disposition: "confirmed", ExternalResourceRef: receipt.ObjectRef, TargetState: "succeeded", ProviderEvidence: receiptBody}
			test.eventBody, err = json.Marshal(map[string]any{"event_type": "tool_effect_reconciled", "data": data})
			test.objectID = test.tool.ReconciliationAttemptID + ":tool-reconciled"
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Run(test.name, func(t *testing.T) {
			values := map[string][]byte{test.objectID + "\x00" + test.tool.EventRef + "\x00" + test.tool.EventHash: test.eventBody}
			if len(test.result) > 0 {
				values[test.tool.ToolCallID+"\x00result-direct\x00"+strings.Repeat("1", 64)] = test.result
			}
			reconciler := PortfolioExportReconciler{Payloads: portfolioReconcilerPayloadStore{values: values}}
			actual, loadErr := reconciler.loadReceipt(context.Background(), "b5000000-0000-4000-8000-000000000011", item, test.tool)
			if loadErr != nil || actual != receipt {
				t.Fatalf("receipt=%#v err=%v", actual, loadErr)
			}
		})
	}
}

func TestPortfolioExportReceiptContractRejectsSubstitution(t *testing.T) {
	item := portfolioFinalizationCandidate{ExportID: "b5000000-0000-4000-8000-000000000001", Format: "pdf"}
	receipt := portfolioReceipt{SchemaVersion: 1, TargetKind: "portfolio_export", TargetID: item.ExportID, ObjectRef: "s3://portfolio-artifacts/exports/result.pdf", ObjectVersion: "version-1", ContentHash: strings.Repeat("a", 64), MediaType: "application/pdf", ByteSize: 4096, ScanResultHash: strings.Repeat("b", 64)}
	if !validPortfolioReceipt(receipt, item, receipt.ObjectRef) {
		t.Fatal("valid receipt was rejected")
	}
	wrongTarget := receipt
	wrongTarget.TargetID = "b5000000-0000-4000-8000-000000000099"
	oversized := receipt
	oversized.ByteSize = 11<<20 + 1
	wrongMedia := receipt
	wrongMedia.MediaType = "text/html"
	if validPortfolioReceipt(wrongTarget, item, receipt.ObjectRef) || validPortfolioReceipt(oversized, item, receipt.ObjectRef) || validPortfolioReceipt(wrongMedia, item, receipt.ObjectRef) || validPortfolioReceipt(receipt, item, "s3://portfolio-artifacts/exports/substituted.pdf") {
		t.Fatal("receipt substitution was accepted")
	}
}
