package deepseek_test

import (
	"context"
	"testing"

	"lites/backend/internal/config"
	"lites/backend/internal/llm"
	"lites/backend/internal/llm/deepseek"
)

func TestProviderRefusesMissingAPIKeyBeforeNetworkCall(t *testing.T) {
	t.Setenv("LITES_TEST_DEEPSEEK_KEY", "")

	provider := deepseek.NewProvider(config.LLMProviderConfig{
		BaseURL:   "https://api.deepseek.com",
		APIKeyEnv: "LITES_TEST_DEEPSEEK_KEY",
	}, nil)

	_, err := provider.Complete(context.Background(), llm.Request{
		Model:    "deepseek-v4-flash",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected missing API key error")
	}
}
