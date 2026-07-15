package agentworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/langshift/lites/internal/behavior"
	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/llmgateway/provider"
)

type routeResourcesStub struct {
	decisions map[string]RouteResourceDecision
	requests  []RouteResourceRequest
}

func (stub *routeResourcesStub) ResolveRouteResources(_ context.Context, request RouteResourceRequest) (RouteResourceDecision, error) {
	stub.requests = append(stub.requests, request)
	decision, ok := stub.decisions[request.ProviderID]
	if !ok {
		return RouteResourceDecision{}, ErrRouteResources
	}
	return decision, nil
}

func TestImmutableModelRouterPinsCandidatesBudgetCreditsAndBYOK(t *testing.T) {
	artifact := testRouteArtifact(t, []RouteCandidateDefinition{
		{ProviderID: "openai", ModelID: "reasoning", ModelVersion: "2026-07-01", BoundHost: "api.openai.com", PricingVersion: "price-v1", CredentialMode: "prefer_byok", FallbackOn: []string{provider.ClassRateLimited}},
		{ProviderID: "anthropic", ModelID: "reasoning", ModelVersion: "2026-06-15", BoundHost: "api.anthropic.com", PricingVersion: "price-v2", CredentialMode: "managed"},
	})
	route := onlyRoute(t, artifact)
	resources := &routeResourcesStub{decisions: map[string]RouteResourceDecision{
		"openai":    {BucketID: "bucket-1", BYOK: &llmpostgres.BYOKBinding{CredentialID: "credential-1", Version: 7, SecretVersion: "11"}},
		"anthropic": {BucketID: "bucket-1"},
	}}
	now := time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC)
	router := ImmutableModelRouter{Artifact: artifact, Providers: testProviderRegistry(t, true), Resources: resources, Now: func() time.Time { return now }, BucketTTL: 15 * time.Minute}
	manifest := routeBehavior(route, true)
	decision, err := router.Route(context.Background(), RouteRequest{TenantID: "tenant-1", UserID: "user-1", RunID: "run-1", Behavior: manifest, Budget: json.RawMessage(`{"max_cost_microunits":4000}`), EstimatedInputTokens: 1000, MaximumOutputTokens: 1000})
	if err != nil || len(decision.Candidates) != 2 {
		t.Fatalf("decision=%#v error=%v", decision, err)
	}
	if decision.Candidates[0].ReservedUnits != 6000 || decision.Candidates[0].BYOK == nil || decision.Candidates[1].ReservedUnits != 6000 || decision.Candidates[1].BYOK != nil || decision.FailureEstimate.InputTokens != 1000 || decision.FailureEstimate.OutputTokens != 1000 {
		t.Fatalf("unexpected route decision: %#v", decision)
	}
	if len(resources.requests) != 2 || !resources.requests[0].RequiredUntil.Equal(now.Add(15*time.Minute)) || resources.requests[0].CredentialMode != "prefer_byok" {
		t.Fatalf("resource requests=%#v", resources.requests)
	}

	decision, err = router.Route(context.Background(), RouteRequest{TenantID: "tenant-1", UserID: "user-1", RunID: "run-2", Behavior: manifest, Budget: json.RawMessage(`{"max_cost_microunits":1}`), EstimatedInputTokens: 1000, MaximumOutputTokens: 1000})
	if err != nil || len(decision.Candidates) != 1 || decision.Candidates[0].Candidate.ProviderID != "openai" {
		t.Fatalf("budget route=%#v error=%v", decision, err)
	}
}

func TestImmutableModelRouterRejectsBindingDriftAndMissingToolCapability(t *testing.T) {
	artifact := testRouteArtifact(t, []RouteCandidateDefinition{{ProviderID: "openai", ModelID: "reasoning", ModelVersion: "2026-07-01", BoundHost: "api.openai.com", PricingVersion: "price-v1", CredentialMode: "managed"}})
	route := onlyRoute(t, artifact)
	resources := &routeResourcesStub{decisions: map[string]RouteResourceDecision{"openai": {BucketID: "bucket-1"}}}
	request := RouteRequest{TenantID: "tenant-1", UserID: "user-1", RunID: "run-1", Behavior: routeBehavior(route, false), Budget: json.RawMessage(`{"max_cost_microunits":10000}`), EstimatedInputTokens: 1000, MaximumOutputTokens: 1000}
	router := ImmutableModelRouter{Artifact: artifact, Providers: testProviderRegistry(t, false), Resources: resources}
	request.Behavior.Model.Hash = strings.Repeat("f", 64)
	if _, err := router.Route(context.Background(), request); !errors.Is(err, ErrContextRoute) {
		t.Fatalf("binding error=%v", err)
	}
	request.Behavior = routeBehavior(route, true)
	if _, err := router.Route(context.Background(), request); !errors.Is(err, ErrContextRoute) {
		t.Fatalf("capability error=%v", err)
	}
}

func TestRouteArtifactPinsFileAndRejectsUnknownFields(t *testing.T) {
	definition := testRouteDefinition([]RouteCandidateDefinition{{ProviderID: "openai", ModelID: "reasoning", ModelVersion: "2026-07-01", BoundHost: "api.openai.com", PricingVersion: "price-v1", CredentialMode: "managed"}})
	encoded, fileHash, err := EncodeRouteArtifact([]RouteDefinition{definition}, strings.Repeat("c", 40), time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "routes.json")
	if err = os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := LoadRouteArtifact(path, fileHash)
	if err != nil || artifact.ArtifactID != "route-artifact-"+artifact.ArtifactHash || len(artifact.routes) != 1 {
		t.Fatalf("artifact=%#v error=%v", artifact, err)
	}
	if _, err = LoadRouteArtifact(path, strings.Repeat("e", 64)); !errors.Is(err, ErrRouteArtifact) {
		t.Fatalf("substitution error=%v", err)
	}
	var document map[string]any
	if json.Unmarshal(encoded, &document) != nil {
		t.Fatal("decode route artifact")
	}
	document["mutable_provider"] = true
	tampered, _ := json.Marshal(document)
	if err = os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadRouteArtifact(path, routeHash(tampered)); !errors.Is(err, ErrRouteArtifact) {
		t.Fatalf("unknown field error=%v", err)
	}
}

func testRouteArtifact(t *testing.T, candidates []RouteCandidateDefinition) RouteArtifact {
	t.Helper()
	encoded, hash, err := EncodeRouteArtifact([]RouteDefinition{testRouteDefinition(candidates)}, strings.Repeat("a", 40), time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "routes.json")
	if err = os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := LoadRouteArtifact(path, hash)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func testRouteDefinition(candidates []RouteCandidateDefinition) RouteDefinition {
	temperature, topP := 0.2, 0.9
	return RouteDefinition{Profile: behavior.Coach, ModelID: "coach-models", ModelVersion: "2026.07.1", RouterID: "coach-router", RouterVersion: "2026.07.1", Candidates: candidates, Temperature: &temperature, TopP: &topP, ToolChoice: provider.ToolChoice{Mode: "auto"}, Stream: true}
}

func onlyRoute(t *testing.T, artifact RouteArtifact) RouteDefinition {
	t.Helper()
	for _, route := range artifact.routes {
		return route
	}
	t.Fatal("route artifact empty")
	return RouteDefinition{}
}

func routeBehavior(route RouteDefinition, tools bool) behavior.Manifest {
	manifest := behavior.Manifest{Profile: route.Profile, Model: behavior.Binding{ID: route.ModelID, Version: route.ModelVersion, Hash: route.ModelHash}, RouterPolicy: behavior.Binding{ID: route.RouterID, Version: route.RouterVersion, Hash: route.RouterHash}}
	if tools {
		manifest.Tools = []behavior.Binding{{ID: "lookup", Version: "1.0.0", Hash: strings.Repeat("d", 64)}}
	}
	return manifest
}

func testProviderRegistry(t *testing.T, toolUse bool) provider.Registry {
	t.Helper()
	capabilities := `"text"`
	if toolUse {
		capabilities += `,"tool_use"`
	}
	document := `{"schema_version":1,"snapshot_id":"provider-registry:production","version":1,"providers":[` +
		`{"id":"openai","type":"openai","status":"active","endpoint":"https://api.openai.com/v1/","bound_host":"api.openai.com","region":"global","credential":{"mode":"bearer","secret_ref":"lites/providers/openai","secret_version":"1"},"request_timeout":"1m","maximum_request_bytes":1048576,"maximum_response_bytes":1048576,"models":[{"id":"reasoning","version":"2026-07-01","wire_model":"gpt-pinned","pricing_version":"price-v1","capabilities":[` + capabilities + `],"maximum_input_tokens":100000,"maximum_output_tokens":10000,"input_microunits_per_million":1000000,"output_microunits_per_million":1000000,"input_credit_units_per_million":2000000,"output_credit_units_per_million":4000000}]},` +
		`{"id":"anthropic","type":"anthropic","status":"active","endpoint":"https://api.anthropic.com/v1/","bound_host":"api.anthropic.com","region":"global","anthropic_version":"2023-06-01","credential":{"mode":"x-api-key","secret_ref":"lites/providers/anthropic","secret_version":"1"},"request_timeout":"1m","maximum_request_bytes":1048576,"maximum_response_bytes":1048576,"models":[{"id":"reasoning","version":"2026-06-15","wire_model":"claude-pinned","pricing_version":"price-v2","capabilities":[` + capabilities + `],"maximum_input_tokens":100000,"maximum_output_tokens":10000,"input_microunits_per_million":1000000,"output_microunits_per_million":1000000,"input_credit_units_per_million":2000000,"output_credit_units_per_million":4000000}]}]}`
	registry, err := provider.LoadRegistry(strings.NewReader(document))
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
