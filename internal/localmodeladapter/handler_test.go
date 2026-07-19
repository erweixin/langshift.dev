package localmodeladapter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langshift/lites/internal/product/review"
	"github.com/langshift/lites/internal/product/route"
	"github.com/langshift/lites/internal/product/task"
)

const testToken = "engineering-test-token-0123456789abcdef"

func TestDeterministicOutputsSatisfyClosedProductContracts(t *testing.T) {
	capabilityID := "11111111-1111-4111-8111-111111111111"
	routePrompt := `Generate a production route revision\nINPUT_MANIFEST={"claim_revisions":[],"target_requirements":[{"capability_id":"` + capabilityID + `"}]}`
	routeJSON, err := deterministicOutput(routePrompt)
	if err != nil {
		t.Fatal(err)
	}
	routeDocument, err := route.DecodeDocument(routeJSON)
	if err != nil {
		t.Fatal(err)
	}
	accepted, _ := json.Marshal(routeDocument)
	dailyPrompt := `Generate today's production practice task\nINPUT_MANIFEST={"command":{"difficulty":"standard","available_minutes":25}}\nACCEPTED_ROUTE=` + string(accepted)
	dailyJSON, err := deterministicOutput(dailyPrompt)
	if err != nil {
		t.Fatal(err)
	}
	daily, err := task.ParseDocument(dailyJSON)
	if err != nil || daily.EstimatedMinutes != 25 || daily.Difficulty != "standard" || daily.CapabilityIDs[0] != capabilityID {
		t.Fatalf("daily=%#v error=%v", daily, err)
	}
	reviewJSON, err := deterministicOutput("Evaluate this immutable submission")
	if err != nil {
		t.Fatal(err)
	}
	evaluation, err := review.Parse(reviewJSON)
	if err != nil || len(evaluation.CapabilityEvidence) != 0 {
		t.Fatalf("review=%#v error=%v", evaluation, err)
	}
	coach, err := deterministicOutput("You are the deterministic coach used only by the macOS engineering fixture.\nHelp me plan the next step.")
	if err != nil || string(coach) != deterministicCoachAnswer {
		t.Fatalf("coach=%q error=%v", coach, err)
	}
}

func TestRouteOutputPrioritizesCurrentActiveClaimOverCatalogRequirement(t *testing.T) {
	claimCapabilityID := "11111111-1111-4111-8111-111111111111"
	requirementCapabilityID := "22222222-2222-4222-8222-222222222222"
	prompt := `Generate a production route revision
INPUT_MANIFEST={"claim_revisions":[{"capability_id":"` + claimCapabilityID + `","status":"active"}],"target_requirements":[{"capability_id":"` + requirementCapabilityID + `"}]}`
	encoded, err := deterministicOutput(prompt)
	if err != nil {
		t.Fatal(err)
	}
	document, err := route.DecodeDocument(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Gaps) != 1 || len(document.Gaps[0].CapabilityIDs) != 1 || document.Gaps[0].CapabilityIDs[0] != claimCapabilityID || document.FirstTask.CapabilityIDs[0] != claimCapabilityID {
		t.Fatalf("route was not grounded in active claim: %#v", document)
	}
}

func TestHandlerRequiresBearerAndProducesValidStreamingResponse(t *testing.T) {
	handler, err := New(Config{Model: "lites-macos-fixture-v1", BearerToken: testToken})
	if err != nil {
		t.Fatal(err)
	}
	prompt := `Generate a production route revision\nINPUT_MANIFEST={"claim_revisions":[],"target_requirements":[{"capability_id":"11111111-1111-4111-8111-111111111111"}]}`
	input, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": prompt}}})
	body, _ := json.Marshal(wireRequest{Model: "lites-macos-fixture-v1", Input: []json.RawMessage{input}, MaxOutputTokens: 4096, Stream: true, ParallelToolCalls: true})
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)))
	if unauthorized.Code != http.StatusNotFound {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d content-type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	var terminal bool
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		terminal = terminal || strings.HasPrefix(scanner.Text(), "event: response.completed")
	}
	if !terminal {
		t.Fatal("missing terminal stream event")
	}
}

func TestHandlerExposesOnlyTheBoundedPasswordRangeFixture(t *testing.T) {
	handler, err := New(Config{Model: "lites-macos-fixture-v1", BearerToken: testToken})
	if err != nil {
		t.Fatal(err)
	}
	valid := httptest.NewRecorder()
	handler.ServeHTTP(valid, httptest.NewRequest(http.MethodGet, "/range/ABCDE", nil))
	if valid.Code != http.StatusOK || valid.Body.Len() != 0 || valid.Header().Get("X-Lites-Fixture-Validated") != "true" {
		t.Fatalf("valid range status=%d body=%q", valid.Code, valid.Body.String())
	}
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/range/../../secret", nil))
	if invalid.Code != http.StatusNotFound {
		t.Fatalf("invalid range status=%d", invalid.Code)
	}
}
