// Package localengineeringassets defines the single immutable asset set used
// by the macOS Compose product gate. Every artifact and behavior snapshot is
// explicitly fixture-only; this package is not imported by production service
// entry points.
package localengineeringassets

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/langshift/lites/internal/agentworker"
	"github.com/langshift/lites/internal/behavior"
	"github.com/langshift/lites/internal/llmgateway/provider"
	"github.com/langshift/lites/internal/releaseassets"
	"github.com/langshift/lites/internal/toolregistry"
	"github.com/langshift/lites/internal/toolworker"
)

const (
	SourceCommit       = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	ProviderHost       = "model-adapter.lites.test"
	ProviderID         = "local-model-adapter"
	ProviderModelID    = "fixture"
	ProviderModelWire  = "lites-macos-fixture-v1"
	ProviderModelVer   = "2026-07-18"
	ProviderPricingVer = "fixture-2026-07"
	ProviderSecretRef  = "lites/providers/local-model-adapter"
	ProviderSecretVer  = "1"
)

var ErrInvalid = errors.New("local engineering assets are invalid")

func Definitions() (releaseassets.Definitions, error) {
	profiles := []behavior.Profile{behavior.RoutePlanner, behavior.DailyPlanner, behavior.Coach, behavior.Evaluator, behavior.ArtifactBuilder}
	prompts := make([]agentworker.PromptDefinition, 0, len(profiles))
	routes := make([]agentworker.RouteDefinition, 0, len(profiles))
	for _, profile := range profiles {
		id := strings.ReplaceAll(string(profile), "_", "-")
		content := "You are the deterministic " + string(profile) + " used only by the macOS engineering fixture. Follow the closed product output contract in the user message. Do not claim production quality, upgrade a capability, invent evidence, or perform a side effect."
		prompts = append(prompts, agentworker.PromptDefinition{TenantID: "*", ID: id + "-macos", Version: "2026.07.1", Content: content, Hash: digest(content)})
		temperature, topP := 0.0, 1.0
		routes = append(routes, agentworker.RouteDefinition{
			Profile: profile, ModelID: id + "-macos-model", ModelVersion: "2026.07.1", RouterID: id + "-macos-router", RouterVersion: "2026.07.1",
			Candidates:  []agentworker.RouteCandidateDefinition{{ProviderID: ProviderID, ModelID: ProviderModelID, ModelVersion: ProviderModelVer, BoundHost: ProviderHost, PricingVersion: ProviderPricingVer, CredentialMode: "managed", FallbackOn: []string{}}},
			Temperature: &temperature, TopP: &topP, ToolChoice: provider.ToolChoice{Mode: "none"}, Stream: true,
		})
	}
	finalized, err := agentworker.FinalizeRouteDefinitions(routes)
	if err != nil {
		return releaseassets.Definitions{}, errors.Join(ErrInvalid, err)
	}
	definitions := releaseassets.Definitions{
		DefinitionVersion: releaseassets.DefinitionVersion, Prompts: prompts, Routes: finalized,
		Tools: []toolregistry.Descriptor{toolworker.ArtifactExportDescriptor("lites/tool-worker@sha256:" + strings.Repeat("a", 64))},
		Providers: provider.RegistryDocument{SchemaVersion: 1, SnapshotID: "provider-registry:macos-engineering-fixture", Version: 1, Providers: []provider.ProviderDefinition{{
			ID: ProviderID, Type: "openai_compatible", Status: "active", Endpoint: "https://" + ProviderHost + "/v1/", BoundHost: ProviderHost, Region: "local-compose",
			Credential: provider.ManagedCredential{Mode: "bearer", SecretRef: ProviderSecretRef, SecretVersion: ProviderSecretVer}, RequestTimeoutText: "1m", MaximumRequestBytes: 8 << 20, MaximumResponseBytes: 16 << 20,
			Models: []provider.ModelDefinition{{ID: ProviderModelID, Version: ProviderModelVer, WireModel: ProviderModelWire, PricingVersion: ProviderPricingVer, Capabilities: []string{"streaming", "text"}, MaximumInputTokens: 1_000_000, MaximumOutputTokens: 65_536, InputMicrounitsPerMillion: 0, OutputMicrounitsPerMillion: 0, InputCreditUnitsPerMillion: 1, OutputCreditUnitsPerMillion: 1}},
		}}},
	}
	if _, err = releaseassets.Build(definitions, SourceCommit, time.Unix(1, 0).UTC()); err != nil {
		return releaseassets.Definitions{}, errors.Join(ErrInvalid, err)
	}
	return definitions, nil
}

func Build(at time.Time) (releaseassets.Bundle, error) {
	definitions, err := Definitions()
	if err != nil {
		return releaseassets.Bundle{}, err
	}
	return releaseassets.Build(definitions, SourceCommit, at.UTC().Truncate(time.Microsecond))
}

func BehaviorManifest(profile behavior.Profile, at time.Time, variant string) (behavior.Manifest, error) {
	if !profile.Valid() || at.IsZero() || at.Location() != time.UTC || variant != "baseline" && variant != "candidate" {
		return behavior.Manifest{}, ErrInvalid
	}
	definitions, err := Definitions()
	if err != nil {
		return behavior.Manifest{}, err
	}
	var prompt agentworker.PromptDefinition
	var route agentworker.RouteDefinition
	for _, candidate := range definitions.Prompts {
		if candidate.ID == strings.ReplaceAll(string(profile), "_", "-")+"-macos" {
			prompt = candidate
			break
		}
	}
	for _, candidate := range definitions.Routes {
		if candidate.Profile == profile {
			route = candidate
			break
		}
	}
	if prompt.ID == "" || route.ModelHash == "" || route.RouterHash == "" {
		return behavior.Manifest{}, ErrInvalid
	}
	manifest := behavior.Manifest{
		SchemaVersion: 1, Profile: profile,
		Model:  behavior.Binding{ID: route.ModelID, Version: route.ModelVersion, Hash: route.ModelHash},
		Prompt: behavior.Binding{ID: prompt.ID, Version: prompt.Version, Hash: prompt.Hash}, Tools: []behavior.Binding{},
		ProfileDefinition: behavior.Binding{ID: string(profile) + "-fixture-profile", Version: "2026.07.1", Hash: digest(string(profile) + "\x00profile\x00" + variant)},
		GuardrailPolicy:   behavior.Binding{ID: string(profile) + "-fixture-guardrail", Version: "2026.07.1", Hash: digest(string(profile) + "\x00guardrail\x00" + variant)},
		RouterPolicy:      behavior.Binding{ID: route.RouterID, Version: route.RouterVersion, Hash: route.RouterHash},
		SourceCommit:      SourceCommit, CreatedAt: at.UTC().Truncate(time.Microsecond),
	}
	if _, err = manifest.Canonical(); err != nil {
		return behavior.Manifest{}, errors.Join(ErrInvalid, err)
	}
	return manifest, nil
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
