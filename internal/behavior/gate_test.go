package behavior

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestEveryProfilePassesOnlyItsFrozenProductionGate(t *testing.T) {
	for profile := range profileDimensions {
		profile := profile
		t.Run(string(profile), func(t *testing.T) {
			report := passingReport(profile)
			result := report.Evaluate()
			if !result.Passed || len(result.Failures) != 0 {
				t.Fatalf("Evaluate()=%#v", result)
			}
			first, err := report.Hash()
			if err != nil || len(first) != 64 {
				t.Fatalf("Hash()=%q err=%v", first, err)
			}
			report.Slices[0], report.Slices[1] = report.Slices[1], report.Slices[0]
			second, err := report.Hash()
			if err != nil || second != first {
				t.Fatalf("canonical report hash changed: %q != %q", second, first)
			}
		})
	}
}

func TestPromotionGateRejectsSafetyRegressionLanguageAndBudgetFailures(t *testing.T) {
	report := passingReport(Coach)
	report.ZeroTolerance.AuthorizationFailures = 1
	report.CostMicrounitsP95 = report.CostBudget + 1
	report.Slices[1].Dimensions[0].AcceptableRate = 0.70
	report.DimensionRegression["groundedness"] = 0.021
	result := report.Evaluate()
	if result.Passed {
		t.Fatal("unsafe regression passed")
	}
	for _, expected := range []string{"core_dimension_regression", "cost_budget_exceeded", "human_rubric_threshold_failed", "language_dimension_gap_exceeded", "zero_tolerance_violation"} {
		if !contains(result.Failures, expected) {
			t.Fatalf("missing %q in %#v", expected, result.Failures)
		}
	}
}

func TestGateRejectsNaNAndProfileSpecificEvidenceGaps(t *testing.T) {
	evaluator := passingReport(Evaluator)
	evaluator.Slices[0].ExpertAgreement = math.NaN()
	if result := evaluator.Evaluate(); result.Passed || !contains(result.Failures, "evaluator_specialized_threshold_failed") {
		t.Fatalf("NaN expert agreement accepted: %#v", result)
	}
	daily := passingReport(DailyPlanner)
	daily.Slices[0].CausalReferenceRate = 0.99
	if result := daily.Evaluate(); result.Passed || !contains(result.Failures, "causal_reference_coverage_not_100_percent") {
		t.Fatalf("incomplete causal references accepted: %#v", result)
	}
	artifact := passingReport(ArtifactBuilder)
	artifact.Slices[0].ManifestCompleteRate = 0.99
	if result := artifact.Evaluate(); result.Passed || !contains(result.Failures, "artifact_manifest_completeness_not_100_percent") {
		t.Fatalf("incomplete artifact manifest accepted: %#v", result)
	}
}

func passingReport(profile Profile) EvaluationReport {
	dimensions := func(rate float64) []DimensionResult {
		result := make([]DimensionResult, 0, len(profileDimensions[profile]))
		for _, id := range profileDimensions[profile] {
			result = append(result, DimensionResult{ID: id, AverageScore: 4.3, AcceptableRate: rate})
		}
		return result
	}
	slice := func(language string, rate float64) SliceResult {
		return SliceResult{
			Language: language, DatasetHash: strings.Repeat("a", 64), SampleCount: 100,
			Dimensions: dimensions(rate), MinimumGroupAcceptable: 0.82,
			DeterministicAccuracy: 1, ExpertAgreement: 0.87, CausalReferenceRate: 1, ManifestCompleteRate: 1,
		}
	}
	regressions := map[string]float64{}
	for _, dimension := range profileDimensions[profile] {
		regressions[dimension] = 0.01
	}
	return EvaluationReport{
		SchemaVersion: 1, ReportID: "eval-2026-07-15", Profile: profile,
		CandidateSnapshotID: "behavior-" + strings.Repeat("b", 64), BaselineSnapshotID: "behavior-" + strings.Repeat("c", 64),
		Slices: []SliceResult{slice("en", 0.90), slice("zh-CN", 0.87)}, ReviewerAgreement: 0.88,
		DimensionRegression: regressions, CostMicrounitsP95: 80, CostBudget: 100, LatencyMillisP95: 1800, LatencyBudget: 2000,
		EvaluatedAt: time.Date(2026, 7, 15, 20, 30, 0, 0, time.UTC),
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
