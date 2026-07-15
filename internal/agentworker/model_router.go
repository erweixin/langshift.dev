package agentworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/langshift/lites/internal/behavior"
	"github.com/langshift/lites/internal/llmgateway"
	llmpostgres "github.com/langshift/lites/internal/llmgateway/postgres"
	"github.com/langshift/lites/internal/llmgateway/provider"
)

var (
	ErrRouteArtifact  = errors.New("model route artifact is invalid")
	ErrRouteResources = errors.New("model route resources are unavailable")
)

const maximumRouteArtifactBytes = 8 << 20

type RouteCandidateDefinition struct {
	ProviderID     string   `json:"provider_id"`
	ModelID        string   `json:"model_id"`
	ModelVersion   string   `json:"model_version"`
	BoundHost      string   `json:"bound_host"`
	PricingVersion string   `json:"pricing_version"`
	CredentialMode string   `json:"credential_mode"`
	FallbackOn     []string `json:"fallback_on"`
}

type RouteDefinition struct {
	Profile       behavior.Profile           `json:"profile"`
	ModelID       string                     `json:"model_id"`
	ModelVersion  string                     `json:"model_version"`
	ModelHash     string                     `json:"model_hash"`
	RouterID      string                     `json:"router_id"`
	RouterVersion string                     `json:"router_version"`
	RouterHash    string                     `json:"router_hash"`
	Candidates    []RouteCandidateDefinition `json:"candidates"`
	Temperature   *float64                   `json:"temperature,omitempty"`
	TopP          *float64                   `json:"top_p,omitempty"`
	ToolChoice    provider.ToolChoice        `json:"tool_choice"`
	Stream        bool                       `json:"stream"`
}

type routeArtifactEnvelope struct {
	SchemaVersion int               `json:"schema_version"`
	ArtifactID    string            `json:"artifact_id"`
	ArtifactHash  string            `json:"artifact_hash"`
	SourceCommit  string            `json:"source_commit"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Routes        []RouteDefinition `json:"routes"`
}

type RouteArtifact struct {
	ArtifactID, ArtifactHash, FileHash, SourceCommit string
	GeneratedAt                                      time.Time
	routes                                           map[string]RouteDefinition
}

func EncodeRouteArtifact(routes []RouteDefinition, sourceCommit string, generatedAt time.Time) ([]byte, string, error) {
	envelope := routeArtifactEnvelope{SchemaVersion: 1, SourceCommit: sourceCommit, GeneratedAt: generatedAt, Routes: routes}
	canonical, hash, _, err := canonicalRouteEnvelope(envelope, true)
	if err != nil {
		return nil, "", err
	}
	canonical.ArtifactID, canonical.ArtifactHash = "route-artifact-"+hash, hash
	encoded, err := json.Marshal(canonical)
	if err != nil || len(encoded) > maximumRouteArtifactBytes {
		return nil, "", ErrRouteArtifact
	}
	return encoded, routeHash(encoded), nil
}

func LoadRouteArtifact(path, expectedFileHash string) (RouteArtifact, error) {
	if path == "" || !policyDigestPattern.MatchString(expectedFileHash) {
		return RouteArtifact{}, ErrRouteArtifact
	}
	file, err := os.Open(path)
	if err != nil {
		return RouteArtifact{}, ErrRouteArtifact
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maximumRouteArtifactBytes+1))
	if err != nil || len(encoded) == 0 || len(encoded) > maximumRouteArtifactBytes || routeHash(encoded) != expectedFileHash {
		return RouteArtifact{}, ErrRouteArtifact
	}
	var envelope routeArtifactEnvelope
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return RouteArtifact{}, ErrRouteArtifact
	}
	_, hash, routes, err := canonicalRouteEnvelope(envelope, false)
	if err != nil || envelope.ArtifactHash != hash || envelope.ArtifactID != "route-artifact-"+hash {
		return RouteArtifact{}, ErrRouteArtifact
	}
	return RouteArtifact{ArtifactID: envelope.ArtifactID, ArtifactHash: hash, FileHash: expectedFileHash, SourceCommit: envelope.SourceCommit, GeneratedAt: envelope.GeneratedAt.UTC(), routes: routes}, nil
}

func canonicalRouteEnvelope(envelope routeArtifactEnvelope, fillHashes bool) (routeArtifactEnvelope, string, map[string]RouteDefinition, error) {
	_, offset := envelope.GeneratedAt.Zone()
	if envelope.SchemaVersion != 1 || !promptSourceCommitPattern.MatchString(envelope.SourceCommit) || envelope.GeneratedAt.IsZero() || offset != 0 || len(envelope.Routes) == 0 || len(envelope.Routes) > 256 {
		return routeArtifactEnvelope{}, "", nil, ErrRouteArtifact
	}
	canonical := envelope
	canonical.ArtifactID, canonical.ArtifactHash = "", ""
	canonical.GeneratedAt = canonical.GeneratedAt.UTC().Truncate(time.Microsecond)
	canonical.Routes = append([]RouteDefinition(nil), envelope.Routes...)
	for index := range canonical.Routes {
		route := &canonical.Routes[index]
		route.Candidates = append([]RouteCandidateDefinition(nil), route.Candidates...)
		for candidate := range route.Candidates {
			route.Candidates[candidate].FallbackOn = append([]string(nil), route.Candidates[candidate].FallbackOn...)
			sort.Strings(route.Candidates[candidate].FallbackOn)
		}
		modelHash, routerHash, err := routeDefinitionHashes(*route)
		if err != nil || !fillHashes && (route.ModelHash != modelHash || route.RouterHash != routerHash) {
			return routeArtifactEnvelope{}, "", nil, ErrRouteArtifact
		}
		route.ModelHash, route.RouterHash = modelHash, routerHash
	}
	sort.Slice(canonical.Routes, func(left, right int) bool {
		return routeKey(canonical.Routes[left]) < routeKey(canonical.Routes[right])
	})
	routes := make(map[string]RouteDefinition, len(canonical.Routes))
	for index, route := range canonical.Routes {
		key := routeKey(route)
		if index > 0 && routeKey(canonical.Routes[index-1]) == key {
			return routeArtifactEnvelope{}, "", nil, ErrRouteArtifact
		}
		routes[key] = route
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return routeArtifactEnvelope{}, "", nil, ErrRouteArtifact
	}
	return canonical, routeHash(encoded), routes, nil
}

func routeDefinitionHashes(route RouteDefinition) (string, string, error) {
	if !route.Profile.Valid() || !promptAssetIDPattern.MatchString(route.ModelID) || !promptVersionPattern.MatchString(route.ModelVersion) || !promptAssetIDPattern.MatchString(route.RouterID) || !promptVersionPattern.MatchString(route.RouterVersion) || len(route.Candidates) == 0 || len(route.Candidates) > 16 || route.ToolChoice.Mode != "auto" && route.ToolChoice.Mode != "none" && route.ToolChoice.Mode != "required" || route.ToolChoice.Name != "" || route.Temperature != nil && (*route.Temperature < 0 || *route.Temperature > 2) || route.TopP != nil && (*route.TopP <= 0 || *route.TopP > 1) {
		return "", "", ErrRouteArtifact
	}
	seen := map[string]bool{}
	for index, candidate := range route.Candidates {
		key := candidate.ProviderID + "\x00" + candidate.ModelID + "\x00" + candidate.ModelVersion
		if candidate.ProviderID == "" || candidate.ModelID == "" || candidate.ModelVersion == "" || candidate.BoundHost == "" || candidate.PricingVersion == "" || candidate.CredentialMode != "managed" && candidate.CredentialMode != "prefer_byok" && candidate.CredentialMode != "require_byok" || seen[key] || !validFallbackClasses(candidate.FallbackOn) || index == len(route.Candidates)-1 && len(candidate.FallbackOn) != 0 {
			return "", "", ErrRouteArtifact
		}
		seen[key] = true
	}
	modelBytes, err := json.Marshal(struct {
		SchemaVersion int                        `json:"schema_version"`
		ID            string                     `json:"id"`
		Version       string                     `json:"version"`
		Candidates    []RouteCandidateDefinition `json:"candidates"`
	}{1, route.ModelID, route.ModelVersion, route.Candidates})
	if err != nil {
		return "", "", ErrRouteArtifact
	}
	modelHash := routeHash(modelBytes)
	routerBytes, err := json.Marshal(struct {
		SchemaVersion int                 `json:"schema_version"`
		ID            string              `json:"id"`
		Version       string              `json:"version"`
		Profile       behavior.Profile    `json:"profile"`
		ModelHash     string              `json:"model_hash"`
		Temperature   *float64            `json:"temperature,omitempty"`
		TopP          *float64            `json:"top_p,omitempty"`
		ToolChoice    provider.ToolChoice `json:"tool_choice"`
		Stream        bool                `json:"stream"`
	}{1, route.RouterID, route.RouterVersion, route.Profile, modelHash, route.Temperature, route.TopP, route.ToolChoice, route.Stream})
	if err != nil {
		return "", "", ErrRouteArtifact
	}
	return modelHash, routeHash(routerBytes), nil
}

func routeKey(route RouteDefinition) string {
	return string(route.Profile) + "\x00" + route.ModelID + "\x00" + route.ModelVersion + "\x00" + route.RouterID + "\x00" + route.RouterVersion
}

func routeHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

type RouteResourceRequest struct {
	TenantID, UserID, ProviderID, BoundHost, CredentialMode string
	ReservedUnits                                           uint64
	RequiredUntil                                           time.Time
}

type RouteResourceDecision struct {
	BucketID string
	BYOK     *llmpostgres.BYOKBinding
}

type RouteResourceResolver interface {
	ResolveRouteResources(context.Context, RouteResourceRequest) (RouteResourceDecision, error)
}

type ImmutableModelRouter struct {
	Artifact  RouteArtifact
	Providers provider.Registry
	Resources RouteResourceResolver
	Now       func() time.Time
	BucketTTL time.Duration
}

func (router ImmutableModelRouter) Route(ctx context.Context, request RouteRequest) (RouteDecision, error) {
	if router.Artifact.ArtifactHash == "" || router.Resources == nil || request.TenantID == "" || request.UserID == "" || request.RunID == "" || request.EstimatedInputTokens == 0 || request.MaximumOutputTokens == 0 {
		return RouteDecision{}, ErrContextRoute
	}
	key := string(request.Behavior.Profile) + "\x00" + request.Behavior.Model.ID + "\x00" + request.Behavior.Model.Version + "\x00" + request.Behavior.RouterPolicy.ID + "\x00" + request.Behavior.RouterPolicy.Version
	definition, exists := router.Artifact.routes[key]
	if !exists || definition.ModelHash != request.Behavior.Model.Hash || definition.RouterHash != request.Behavior.RouterPolicy.Hash {
		return RouteDecision{}, ErrContextRoute
	}
	budget, err := parseRouteBudget(request.Budget)
	if err != nil {
		return RouteDecision{}, err
	}
	decision := RouteDecision{Temperature: definition.Temperature, TopP: definition.TopP, ToolChoice: definition.ToolChoice, Stream: definition.Stream, FailureEstimate: llmgateway.FailureEstimate{InputTokens: request.EstimatedInputTokens, OutputTokens: uint64(request.MaximumOutputTokens)}}
	remainingCost := budget.MaxCostMicrounits
	for _, declared := range definition.Candidates {
		candidate := llmpostgres.ModelCandidate{ProviderID: declared.ProviderID, ModelID: declared.ModelID, ModelVersion: declared.ModelVersion, BoundHost: declared.BoundHost, PricingVersion: declared.PricingVersion}
		resolved, resolveErr := router.Providers.Resolve(candidate.ProviderID, candidate.ModelID, candidate.ModelVersion, candidate.BoundHost, candidate.PricingVersion)
		if resolveErr != nil || request.EstimatedInputTokens > resolved.Model.MaximumInputTokens || uint64(request.MaximumOutputTokens) > resolved.Model.MaximumOutputTokens || !supportsRoute(resolved.Model, len(request.Behavior.Tools) > 0) {
			continue
		}
		units, unitsErr := routeUsage(request.EstimatedInputTokens, uint64(request.MaximumOutputTokens), resolved.Model.InputCreditUnitsPerMillion, resolved.Model.OutputCreditUnitsPerMillion)
		managedCost, costErr := routeUsage(request.EstimatedInputTokens, uint64(request.MaximumOutputTokens), resolved.Model.InputMicrounitsPerMillion, resolved.Model.OutputMicrounitsPerMillion)
		if unitsErr != nil || costErr != nil || units == 0 {
			continue
		}
		resources, resourceErr := router.Resources.ResolveRouteResources(ctx, RouteResourceRequest{TenantID: request.TenantID, UserID: request.UserID, ProviderID: candidate.ProviderID, BoundHost: candidate.BoundHost, CredentialMode: declared.CredentialMode, ReservedUnits: units, RequiredUntil: router.now().Add(router.bucketTTL())})
		if resourceErr != nil || resources.BucketID == "" {
			continue
		}
		cost := managedCost
		if resources.BYOK != nil {
			cost = 0
		}
		if cost > remainingCost {
			continue
		}
		remainingCost -= cost
		decision.Candidates = append(decision.Candidates, RoutedCandidate{Candidate: candidate, BucketID: resources.BucketID, ReservedUnits: units, BYOK: resources.BYOK, FallbackOn: append([]string(nil), declared.FallbackOn...)})
	}
	if len(decision.Candidates) == 0 {
		return RouteDecision{}, ErrContextRoute
	}
	return decision, nil
}

type routeBudget struct {
	MaxCostMicrounits uint64 `json:"max_cost_microunits"`
}

func parseRouteBudget(encoded json.RawMessage) (routeBudget, error) {
	var budget routeBudget
	if len(encoded) == 0 || json.Unmarshal(encoded, &budget) != nil || budget.MaxCostMicrounits == 0 {
		return routeBudget{}, ErrContextRoute
	}
	return budget, nil
}

func supportsRoute(model provider.ModelDefinition, tools bool) bool {
	text, toolUse := false, !tools
	for _, capability := range model.Capabilities {
		text = text || capability == "text"
		toolUse = toolUse || capability == "tool_use"
	}
	return text && toolUse
}

func routeUsage(input, output, inputRate, outputRate uint64) (uint64, error) {
	left, err := routeRate(input, inputRate)
	if err != nil {
		return 0, err
	}
	right, err := routeRate(output, outputRate)
	if err != nil || left > math.MaxUint64-right {
		return 0, ErrContextRoute
	}
	return left + right, nil
}

func routeRate(tokens, rate uint64) (uint64, error) {
	if tokens == 0 || rate == 0 {
		return 0, nil
	}
	if tokens > math.MaxUint64/rate {
		return 0, ErrContextRoute
	}
	product := tokens * rate
	result := product / 1_000_000
	if product%1_000_000 != 0 {
		result++
	}
	return result, nil
}

func (router ImmutableModelRouter) now() time.Time {
	if router.Now != nil {
		return router.Now().UTC()
	}
	return time.Now().UTC()
}

func (router ImmutableModelRouter) bucketTTL() time.Duration {
	if router.BucketTTL > 0 {
		return router.BucketTTL
	}
	return 10 * time.Minute
}

// PostgresRouteResources selects only currently active, sufficiently funded
// credit and the exact current BYOK version in one tenant-isolated snapshot.
// The later Reserve/Prepare transactions recheck both facts under lock.
type PostgresRouteResources struct{ Pool *pgxpool.Pool }

func (resources PostgresRouteResources) ResolveRouteResources(ctx context.Context, request RouteResourceRequest) (RouteResourceDecision, error) {
	if resources.Pool == nil || request.TenantID == "" || request.UserID == "" || request.ProviderID == "" || request.BoundHost == "" || request.ReservedUnits == 0 || request.ReservedUnits > math.MaxInt64 || request.RequiredUntil.IsZero() || request.CredentialMode != "managed" && request.CredentialMode != "prefer_byok" && request.CredentialMode != "require_byok" {
		return RouteResourceDecision{}, ErrRouteResources
	}
	tx, err := resources.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return RouteResourceDecision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('lites.tenant_id',$1,true)`, request.TenantID); err != nil {
		return RouteResourceDecision{}, err
	}
	var decision RouteResourceDecision
	err = tx.QueryRow(ctx, `SELECT id::text FROM contracts.credit_buckets WHERE tenant_id=$1 AND bucket_kind='llm' AND starts_at<=CURRENT_TIMESTAMP AND expires_at>=$2 AND granted_units-reserved_units-settled_units>=$3 ORDER BY expires_at,id LIMIT 1`, request.TenantID, request.RequiredUntil.UTC(), int64(request.ReservedUnits)).Scan(&decision.BucketID)
	if err != nil {
		return RouteResourceDecision{}, ErrRouteResources
	}
	if request.CredentialMode != "managed" {
		var credential llmpostgres.BYOKBinding
		var version int64
		err = tx.QueryRow(ctx, `SELECT id::text,version,secret_version FROM product.byok_credentials WHERE tenant_id=$1 AND user_id=$2 AND provider_id=$3 AND bound_host=$4 AND status='active'`, request.TenantID, request.UserID, request.ProviderID, request.BoundHost).Scan(&credential.CredentialID, &version, &credential.SecretVersion)
		if err == nil && version > 0 {
			credential.Version, decision.BYOK = uint64(version), &credential
		} else if request.CredentialMode == "require_byok" {
			return RouteResourceDecision{}, ErrRouteResources
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return RouteResourceDecision{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return RouteResourceDecision{}, err
	}
	return decision, nil
}
