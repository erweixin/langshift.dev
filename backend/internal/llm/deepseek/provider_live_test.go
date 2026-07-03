package deepseek_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"lites/backend/internal/config"
	"lites/backend/internal/llm"
	"lites/backend/internal/llm/deepseek"
)

func TestProviderLiveSmoke(t *testing.T) {
	if os.Getenv("LITES_DEEPSEEK_LIVE") != "1" {
		t.Skip("set LITES_DEEPSEEK_LIVE=1 to run the live DeepSeek smoke test")
	}

	cfg, err := config.LoadLLMConfig(liveConfigPath())
	if err != nil {
		t.Fatalf("load llm config: %v", err)
	}
	tier := cfg.Tiers["small"]
	if tier.Provider != "deepseek" || tier.Model == "" {
		t.Fatalf("small tier route = %q/%q, want deepseek/model", tier.Provider, tier.Model)
	}
	providerConfig := cfg.Providers[tier.Provider]
	if providerConfig.APIKey == "" && (providerConfig.APIKeyEnv == "" || os.Getenv(providerConfig.APIKeyEnv) == "") {
		t.Skip("set api_key in .llm.yaml or set the configured api_key_env to run the live DeepSeek smoke test")
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	provider := deepseek.NewProvider(providerConfig, httpClient)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	response, err := provider.Complete(ctx, llm.Request{
		Model:     tier.Model,
		MaxTokens: 64,
		Thinking:  llm.Thinking{Type: "disabled"},
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: "Reply with exactly: lites-ok",
		}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if strings.TrimSpace(response.Content) == "" {
		t.Fatal("response content is empty")
	}
	if response.ProviderRequestID == "" {
		t.Fatal("provider request id is empty")
	}
	if response.Usage.InputTokens == 0 || response.Usage.OutputTokens == 0 {
		t.Fatalf("usage = %+v, want nonzero input and output tokens", response.Usage)
	}
}

func liveConfigPath() string {
	if path := os.Getenv("LITES_LLM_CONFIG"); path != "" {
		return path
	}
	if _, err := os.Stat("../../../../.llm.yaml"); err == nil {
		return "../../../../.llm.yaml"
	}
	return "../../../../config/llm.example.yaml"
}
