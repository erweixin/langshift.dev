package llm

import (
	"context"
	"fmt"

	"lites/backend/internal/config"
)

type Router struct {
	config    config.LLMConfig
	providers map[string]Provider
}

type Route struct {
	Surface  string
	Tier     string
	Provider string
	Model    string
}

func NewRouter(config config.LLMConfig, providers map[string]Provider) (*Router, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	for name := range config.Providers {
		if providers[name] == nil {
			return nil, fmt.Errorf("llm provider %q is configured but not registered", name)
		}
	}

	return &Router{
		config:    config,
		providers: providers,
	}, nil
}

func (r *Router) Route(surface string) (Route, error) {
	tierName, ok := r.config.Surfaces[surface]
	if !ok {
		return Route{}, fmt.Errorf("unknown llm surface %q", surface)
	}

	tier, ok := r.config.Tiers[tierName]
	if !ok {
		return Route{}, fmt.Errorf("surface %q references unknown tier %q", surface, tierName)
	}

	return Route{
		Surface:  surface,
		Tier:     tierName,
		Provider: tier.Provider,
		Model:    tier.Model,
	}, nil
}

func (r *Router) Complete(ctx context.Context, req Request) (Response, error) {
	req, err := r.Prepare(req)
	if err != nil {
		return Response{}, err
	}

	provider := r.providers[req.Provider]
	if provider == nil {
		return Response{}, fmt.Errorf("provider %q is not registered", req.Provider)
	}
	return provider.Complete(ctx, req)
}

func (r *Router) Prepare(req Request) (Request, error) {
	route, err := r.Route(req.Surface)
	if err != nil {
		return Request{}, err
	}

	tier := r.config.Tiers[route.Tier]
	req.Tier = route.Tier
	req.Provider = route.Provider
	req.Model = route.Model
	if req.MaxTokens == 0 {
		req.MaxTokens = tier.MaxOutputTokens
	}
	if req.Thinking.Type == "" {
		req.Thinking.Type = tier.ThinkingMode
	}
	if req.Thinking.ReasoningEffort == "" {
		req.Thinking.ReasoningEffort = tier.ReasoningEffort
	}
	if tier.DefaultJSONOutput {
		req.JSONMode = true
	}

	if r.providers[route.Provider] == nil {
		return Request{}, fmt.Errorf("provider %q is not registered", route.Provider)
	}
	return req, nil
}

func (r *Router) EstimateCostUSD(req Request, usage Usage) float64 {
	tierName := req.Tier
	if tierName == "" {
		route, err := r.Route(req.Surface)
		if err != nil {
			return 0
		}
		tierName = route.Tier
	}

	tier, ok := r.config.Tiers[tierName]
	if !ok {
		return 0
	}

	inputTokens := maxInt(usage.InputTokens, 0)
	outputTokens := maxInt(usage.OutputTokens, 0)
	cacheReadTokens := minInt(maxInt(usage.CacheReadTokens, 0), inputTokens)
	regularInputTokens := inputTokens - cacheReadTokens

	return (float64(regularInputTokens)*tier.PriceInPerMTok +
		float64(cacheReadTokens)*tier.CacheHitPerMTok +
		float64(outputTokens)*tier.PriceOutPerMTok) / 1_000_000
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}
