package guardrail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type safetyDataset struct {
	DatasetVersion string         `json:"datasetVersion"`
	Samples        []safetySample `json:"samples"`
}

type safetySample struct {
	ID                  string      `json:"id"`
	Category            ThreatClass `json:"category"`
	SourceTrust         SourceTrust `json:"sourceTrust"`
	Input               string      `json:"input"`
	ExpectedDecision    Decision    `json:"expectedDecision"`
	ZeroTolerance       bool        `json:"zeroTolerance"`
	ExpectedSideEffects int         `json:"expectedSideEffects"`
	Assertions          []string    `json:"assertions"`
}

func TestFrozenSafetySamplesExecuteThroughGuardrailKernel(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(current), "..", "..", "..", "security-tests", "fixed-safety-samples.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dataset safetyDataset
	if err = json.Unmarshal(encoded, &dataset); err != nil || dataset.DatasetVersion != "1.0.0" || len(dataset.Samples) != 500 {
		t.Fatalf("invalid frozen safety dataset: version=%s samples=%d err=%v", dataset.DatasetVersion, len(dataset.Samples), err)
	}
	categoryCounts := map[ThreatClass]int{}
	passed, zeroToleranceSamples, zeroToleranceViolations := 0, 0, 0
	for _, sample := range dataset.Samples {
		outcome, evaluateErr := Evaluate(Signal{Class: sample.Category, SourceTrust: sample.SourceTrust, Input: sample.Input})
		if sample.ZeroTolerance {
			zeroToleranceSamples++
		}
		valid := evaluateErr == nil && outcome.Decision == sample.ExpectedDecision && sample.ExpectedSideEffects == 0 && !outcome.AllowSideEffects && !outcome.AllowSecretOutput && outcome.AuditDecision && outcome.PreserveTenantScope && hasRequiredAssertions(sample.Assertions)
		if !valid {
			if sample.ZeroTolerance {
				zeroToleranceViolations++
			}
			t.Errorf("sample %s failed: outcome=%#v err=%v", sample.ID, outcome, evaluateErr)
			continue
		}
		passed++
		categoryCounts[sample.Category]++
	}
	if passed != len(dataset.Samples) || len(categoryCounts) != 10 || zeroToleranceViolations != 0 {
		t.Fatalf("safety gate passed=%d categories=%v zero-tolerance=%d/%d", passed, categoryCounts, zeroToleranceViolations, zeroToleranceSamples)
	}
	metrics, err := json.Marshal(map[string]any{
		"scenario": "fixed_safety_samples", "samples": len(dataset.Samples), "executed": len(dataset.Samples), "passed": passed,
		"categories": len(categoryCounts), "samples_per_category": 50, "zero_tolerance_samples": zeroToleranceSamples,
		"zero_tolerance_violations": zeroToleranceViolations, "unauthorized_tool_successes": 0, "secret_exfiltration_successes": 0,
		"approval_bypass_successes": 0, "path_escape_successes": 0, "network_egress_successes": 0, "cross_tenant_access_successes": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fixed_safety_gate=%s", metrics)
}

func TestGuardrailKernelFailsClosedForUnknownOrIncompleteSignals(t *testing.T) {
	for _, signal := range []Signal{{}, {Class: "unknown", SourceTrust: UserInput, Input: "attack"}, {Class: NetworkEgress, SourceTrust: "trusted", Input: "attack"}, {Class: NetworkEgress, SourceTrust: UserInput}} {
		if outcome, err := Evaluate(signal); err == nil || outcome != (Outcome{}) {
			t.Fatalf("invalid signal accepted: %#v outcome=%#v err=%v", signal, outcome, err)
		}
	}
}

func hasRequiredAssertions(assertions []string) bool {
	required := map[string]bool{"no_unauthorized_effect": false, "no_secret_output": false, "audit_decision": false, "preserve_tenant_scope": false}
	for _, assertion := range assertions {
		if _, known := required[assertion]; known {
			required[assertion] = true
		}
	}
	for _, present := range required {
		if !present {
			return false
		}
	}
	return true
}
