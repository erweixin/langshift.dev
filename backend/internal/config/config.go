package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

const (
	defaultHTTPAddr      = ":8080"
	defaultDatabaseURL   = "postgres://lites:lites@localhost:5432/lites?sslmode=disable"
	defaultLLMConfigPath = "../config/llm.example.yaml"
	defaultMigrationsDir = "migrations"
)

type Config struct {
	DatabaseURL   string
	HTTPAddr      string
	LLMConfigPath string
	MigrationsDir string
	SingleUser    bool
}

func Load() (Config, error) {
	config := Config{
		DatabaseURL:   envOrDefault("DATABASE_URL", defaultDatabaseURL),
		HTTPAddr:      envOrDefault("LITES_HTTP_ADDR", defaultHTTPAddr),
		LLMConfigPath: envOrDefault("LITES_LLM_CONFIG", detectLLMConfigPath()),
		MigrationsDir: envOrDefault("LITES_MIGRATIONS_DIR", defaultMigrationsDir),
		SingleUser:    os.Getenv("LITES_SINGLE_USER") == "1",
	}
	return config, nil
}

type LLMConfig struct {
	Providers map[string]LLMProviderConfig `yaml:"providers"`
	Tiers     map[string]LLMTierConfig     `yaml:"tiers"`
	Surfaces  map[string]string            `yaml:"surfaces"`
	Budgets   LLMBudgetConfig              `yaml:"budgets"`
}

type LLMProviderConfig struct {
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
	APIKey    string `yaml:"api_key"`
}

type LLMTierConfig struct {
	Provider          string  `yaml:"provider"`
	Model             string  `yaml:"model"`
	PriceInPerMTok    float64 `yaml:"price_in_per_mtok"`
	PriceOutPerMTok   float64 `yaml:"price_out_per_mtok"`
	CacheHitPerMTok   float64 `yaml:"cache_hit_per_mtok"`
	ReasoningEffort   string  `yaml:"reasoning_effort"`
	ThinkingMode      string  `yaml:"thinking_mode"`
	MaxOutputTokens   int     `yaml:"max_output_tokens"`
	DefaultJSONOutput bool    `yaml:"default_json_output"`
}

type LLMBudgetConfig struct {
	PerUserDailyUSD  float64 `yaml:"per_user_daily_usd"`
	GlobalDailyAlert float64 `yaml:"global_daily_usd_alert"`
}

func LoadLLMConfig(path string) (LLMConfig, error) {
	if path == "" {
		path = defaultLLMConfigPath
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return LLMConfig{}, fmt.Errorf("read llm config: %w", err)
	}

	var config LLMConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return LLMConfig{}, fmt.Errorf("parse llm config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return LLMConfig{}, err
	}
	return config, nil
}

func (c LLMConfig) Validate() error {
	if len(c.Providers) == 0 {
		return errors.New("llm config requires at least one provider")
	}
	if len(c.Tiers) == 0 {
		return errors.New("llm config requires at least one tier")
	}
	if len(c.Surfaces) == 0 {
		return errors.New("llm config requires at least one surface")
	}

	for name, provider := range c.Providers {
		if provider.BaseURL == "" {
			return fmt.Errorf("llm provider %q requires base_url", name)
		}
		if provider.APIKeyEnv == "" && provider.APIKey == "" {
			return fmt.Errorf("llm provider %q requires api_key_env or api_key", name)
		}
	}

	for name, tier := range c.Tiers {
		if tier.Provider == "" {
			return fmt.Errorf("llm tier %q requires provider", name)
		}
		if _, ok := c.Providers[tier.Provider]; !ok {
			return fmt.Errorf("llm tier %q references unknown provider %q", name, tier.Provider)
		}
		if tier.Model == "" {
			return fmt.Errorf("llm tier %q requires model", name)
		}
	}

	for surface, tier := range c.Surfaces {
		if _, ok := c.Tiers[tier]; !ok {
			return fmt.Errorf("llm surface %q references unknown tier %q", surface, tier)
		}
	}

	return nil
}

func envOrDefault(name string, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func detectLLMConfigPath() string {
	for _, candidate := range []string{
		".llm.yaml",
		"../.llm.yaml",
		"config/llm.yaml",
		"../config/llm.yaml",
		"config/llm.example.yaml",
		"../config/llm.example.yaml",
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return defaultLLMConfigPath
}
