package deepseek_test

import (
	"context"
	"io"
	"net/http"
	"strings"
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

func TestProviderUsesDirectAPIKeyFromConfig(t *testing.T) {
	var authorization string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		authorization = r.Header.Get("Authorization")
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"id": "req_1",
				"choices": [{"message": {"content": "ok"}}],
				"usage": {"prompt_tokens": 1, "completion_tokens": 1}
			}`)),
			Header: make(http.Header),
		}, nil
	})}

	t.Setenv("LITES_TEST_DEEPSEEK_KEY", "")
	provider := deepseek.NewProvider(config.LLMProviderConfig{
		BaseURL:   "https://deepseek.test",
		APIKeyEnv: "LITES_TEST_DEEPSEEK_KEY",
		APIKey:    "direct-key",
	}, client)

	_, err := provider.Complete(context.Background(), llm.Request{
		Model:    "deepseek-v4-flash",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if authorization != "Bearer direct-key" {
		t.Fatalf("authorization = %q, want Bearer direct-key", authorization)
	}
}

type roundTripFunc func(r *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
