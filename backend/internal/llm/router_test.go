package llm_test

import (
	"context"
	"math"
	"testing"

	"lites/backend/internal/config"
	"lites/backend/internal/llm"
)

func TestRouterRoutesSurfaceToConfiguredProviderAndModel(t *testing.T) {
	cfg := config.LLMConfig{
		Providers: map[string]config.LLMProviderConfig{
			"deepseek": {BaseURL: "https://api.deepseek.com", APIKeyEnv: "DEEPSEEK_API_KEY"},
		},
		Tiers: map[string]config.LLMTierConfig{
			"strong": {
				Provider:        "deepseek",
				Model:           "deepseek-v4-pro",
				MaxOutputTokens: 8192,
				ThinkingMode:    "enabled",
				ReasoningEffort: "high",
			},
			"small": {
				Provider:        "deepseek",
				Model:           "deepseek-v4-flash",
				MaxOutputTokens: 2048,
			},
		},
		Surfaces: map[string]string{
			"review": "strong",
			"chat":   "small",
		},
	}

	spy := &spyProvider{}
	router, err := llm.NewRouter(cfg, map[string]llm.Provider{"deepseek": spy})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}

	_, err = router.Complete(context.Background(), llm.Request{
		Surface:  "review",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if spy.request.Provider != "deepseek" {
		t.Fatalf("provider = %q, want deepseek", spy.request.Provider)
	}
	if spy.request.Tier != "strong" {
		t.Fatalf("tier = %q, want strong", spy.request.Tier)
	}
	if spy.request.Model != "deepseek-v4-pro" {
		t.Fatalf("model = %q, want deepseek-v4-pro", spy.request.Model)
	}
	if spy.request.MaxTokens != 8192 {
		t.Fatalf("max tokens = %d, want 8192", spy.request.MaxTokens)
	}
	if spy.request.Thinking.Type != "enabled" {
		t.Fatalf("thinking type = %q, want enabled", spy.request.Thinking.Type)
	}
}

func TestRouterEstimateCostUsesTierPricing(t *testing.T) {
	cfg := config.LLMConfig{
		Providers: map[string]config.LLMProviderConfig{
			"deepseek": {BaseURL: "https://api.deepseek.com", APIKeyEnv: "DEEPSEEK_API_KEY"},
		},
		Tiers: map[string]config.LLMTierConfig{
			"strong": {
				Provider:        "deepseek",
				Model:           "deepseek-v4-pro",
				PriceInPerMTok:  0.40,
				CacheHitPerMTok: 0.01,
				PriceOutPerMTok: 0.80,
			},
		},
		Surfaces: map[string]string{"review": "strong"},
	}

	router, err := llm.NewRouter(cfg, map[string]llm.Provider{"deepseek": &spyProvider{}})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}

	got := router.EstimateCostUSD(llm.Request{Surface: "review"}, llm.Usage{
		InputTokens:     1_000_000,
		OutputTokens:    500_000,
		CacheReadTokens: 100_000,
	})
	want := 0.761
	if math.Abs(got-want) > 0.000000001 {
		t.Fatalf("estimated cost = %.12f, want %.12f", got, want)
	}
}

type spyProvider struct {
	request llm.Request
}

func (p *spyProvider) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	p.request = req
	return llm.Response{Content: "ok"}, nil
}
