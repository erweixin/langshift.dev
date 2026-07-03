package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"lites/backend/internal/config"
)

func TestLoadLLMConfigDeepSeekDefaults(t *testing.T) {
	cfg, err := config.LoadLLMConfig("../../../config/llm.example.yaml")
	if err != nil {
		t.Fatalf("load llm config: %v", err)
	}

	deepseek := cfg.Providers["deepseek"]
	if deepseek.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("deepseek base url = %q", deepseek.BaseURL)
	}
	if deepseek.APIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("deepseek api key env = %q", deepseek.APIKeyEnv)
	}
	if cfg.Tiers["strong"].Model != "deepseek-v4-pro" {
		t.Fatalf("strong model = %q", cfg.Tiers["strong"].Model)
	}
	if cfg.Tiers["small"].Model != "deepseek-v4-flash" {
		t.Fatalf("small model = %q", cfg.Tiers["small"].Model)
	}
	if cfg.Surfaces["review"] != "strong" {
		t.Fatalf("review surface tier = %q", cfg.Surfaces["review"])
	}
}

func TestLoadLLMConfigAllowsDirectAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llm.yaml")
	if err := os.WriteFile(path, []byte(`
providers:
  deepseek:
    base_url: https://api.deepseek.com
    api_key: test-key
tiers:
  small:
    provider: deepseek
    model: deepseek-v4-flash
surfaces:
  chat: small
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.LoadLLMConfig(path)
	if err != nil {
		t.Fatalf("load llm config: %v", err)
	}
	if cfg.Providers["deepseek"].APIKey != "test-key" {
		t.Fatalf("api key was not loaded")
	}
}
