package postgres

import (
	"bytes"
	"testing"

	executionpostgres "github.com/langshift/lites/internal/execution/postgres"
)

func TestProductRunPayloadIdentifiersMatchExecutionKernel(t *testing.T) {
	productKey := bytes.Repeat([]byte{0x71}, 32)
	kernelKey := bytes.Repeat([]byte{0x72}, 32)

	assertStart := func(name, runID, startCommandID string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s identifiers: %v", name, err)
		}
		expected, expectedErr := executionpostgres.RunStartCommandID(kernelKey, runID)
		if expectedErr != nil {
			t.Fatalf("%s expected identifier: %v", name, expectedErr)
		}
		if startCommandID != expected {
			t.Fatalf("%s start command drifted: got %s want %s", name, startCommandID, expected)
		}
	}

	route, err := (RoutePlannerService{IDKey: productKey, Runs: executionpostgres.RunStore{IDKey: kernelKey}}).identifiers("route-command")
	assertStart("route planner", route.runID, route.startCommandID, err)

	daily, err := (DailyTaskPlannerService{IDKey: productKey, Runs: executionpostgres.RunStore{IDKey: kernelKey}}).identifiers("daily-command")
	assertStart("daily planner", daily.runID, daily.startCommandID, err)

	review, err := (ReviewService{IDKey: productKey, Runs: executionpostgres.RunStore{IDKey: kernelKey}}).identifiers("review-request")
	assertStart("review evaluator", review.run, review.startCommand, err)

	project, err := (ProjectTestGenerationService{IDKey: productKey, Runs: executionpostgres.RunStore{IDKey: kernelKey}}).identifiers("project-request")
	assertStart("project evaluator", project.run, project.startCommand, err)

	portfolio, err := (PortfolioExportService{IDKey: productKey, Store: PortfolioExportStore{IDKey: kernelKey}}).identifiers("portfolio-request")
	assertStart("portfolio builder", portfolio.run, portfolio.startPayload, err)
}
