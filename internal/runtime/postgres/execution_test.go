package postgres

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestValidOutcomeManifest(t *testing.T) {
	exitCode := 0
	completed, err := json.Marshal(ExecutionOutcomeManifest{
		SchemaVersion: 1, RequestID: "runtime-request-0001", Kind: "guest_result", ExitCode: &exitCode,
		StartedUnixMillis: 1000, FinishedUnixMillis: 1001,
	})
	if err != nil || !validOutcomeManifest(completed, "runtime-request-0001", "completed", "") {
		t.Fatalf("valid completed manifest rejected: %v", err)
	}
	unknown, err := json.Marshal(ExecutionOutcomeManifest{
		SchemaVersion: 1, RequestID: "runtime-request-0001", Kind: "transport_unknown",
		FailureCode: "execution_interrupted", ObservedUnixMillis: time.Now().UnixMilli(), TerminationReason: "run_cancelled",
	})
	if err != nil || !validOutcomeManifest(unknown, "runtime-request-0001", "outcome_unknown", "execution_interrupted") {
		t.Fatalf("valid unknown manifest rejected: %v", err)
	}
	if validOutcomeManifest(unknown, "runtime-request-0002", "outcome_unknown", "execution_interrupted") {
		t.Fatal("manifest was accepted for a different request")
	}
	oversizedReason := ExecutionOutcomeManifest{
		SchemaVersion: 1, RequestID: "runtime-request-0001", Kind: "transport_unknown",
		FailureCode: "execution_interrupted", ObservedUnixMillis: time.Now().UnixMilli(), TerminationReason: strings.Repeat("x", 129),
	}
	encoded, err := json.Marshal(oversizedReason)
	if err != nil || validOutcomeManifest(encoded, "runtime-request-0001", "outcome_unknown", "execution_interrupted") {
		t.Fatalf("oversized termination reason accepted: %v", err)
	}
}

func TestValidFinishExecutionRequiresExactOutcomeContract(t *testing.T) {
	exitCode := 0
	manifest, err := json.Marshal(ExecutionOutcomeManifest{
		SchemaVersion: 1, RequestID: "runtime-request-0001", Kind: "guest_result", ExitCode: &exitCode,
		StartedUnixMillis: 1000, FinishedUnixMillis: 1001,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := FinishExecutionCommand{
		LifecycleCommand: LifecycleCommand{
			TenantID: "tenant", SessionID: "session", ProvisionAttemptID: "attempt", ProvisionFence: 1,
			ExpectedVersion: 4, LeaseToken: "lease", ObservedAt: time.Now(),
			Payload: PayloadPointer{Ref: "encrypted://runtime/result", Hash: strings.Repeat("a", 64)},
			Actor:   json.RawMessage(`{"kind":"system"}`), CorrelationID: "correlation",
		},
		RequestID: "runtime-request-0001", RequestHash: strings.Repeat("b", 64),
		OutcomeHash: strings.Repeat("c", 64), OutcomeManifest: manifest,
	}
	if !validFinishExecution(command, "completed") {
		t.Fatal("valid completed execution rejected")
	}
	command.FailureCode = "process_failed"
	if validFinishExecution(command, "completed") {
		t.Fatal("completed execution accepted a top-level failure code")
	}
}
