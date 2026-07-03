package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"lites/backend/internal/config"
	"lites/backend/internal/llm"
)

type Provider struct {
	baseURL   string
	apiKeyEnv string
	client    *http.Client
}

func NewProvider(config config.LLMProviderConfig, client *http.Client) *Provider {
	if client == nil {
		client = http.DefaultClient
	}
	return &Provider{
		baseURL:   strings.TrimRight(config.BaseURL, "/"),
		apiKeyEnv: config.APIKeyEnv,
		client:    client,
	}
}

func (p *Provider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	apiKey := os.Getenv(p.apiKeyEnv)
	if apiKey == "" {
		return llm.Response{}, fmt.Errorf("deepseek api key env %s is not set", p.apiKeyEnv)
	}
	if p.baseURL == "" {
		return llm.Response{}, fmt.Errorf("deepseek base_url is required")
	}
	if req.Model == "" {
		return llm.Response{}, fmt.Errorf("deepseek model is required")
	}

	body := chatCompletionRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Stream:      false,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}
	if req.JSONMode {
		body.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	if req.Thinking.Type != "" {
		body.Thinking = &thinking{Type: req.Thinking.Type}
	}
	if req.Thinking.ReasoningEffort != "" {
		body.ReasoningEffort = req.Thinking.ReasoningEffort
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return llm.Response{}, fmt.Errorf("encode deepseek request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return llm.Response{}, fmt.Errorf("create deepseek request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return llm.Response{}, fmt.Errorf("call deepseek: %w", err)
	}
	defer httpResp.Body.Close()

	responseBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return llm.Response{}, fmt.Errorf("read deepseek response: %w", err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return llm.Response{}, fmt.Errorf("deepseek returned status %d: %s", httpResp.StatusCode, truncate(responseBody, 1024))
	}

	var decoded chatCompletionResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return llm.Response{}, fmt.Errorf("decode deepseek response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return llm.Response{}, fmt.Errorf("deepseek response contains no choices")
	}

	return llm.Response{
		Content:           decoded.Choices[0].Message.Content,
		Usage:             decoded.Usage.toUsage(),
		ProviderRequestID: decoded.ID,
		FinishReason:      decoded.Choices[0].FinishReason,
		Raw:               json.RawMessage(responseBody),
	}, nil
}

type chatCompletionRequest struct {
	Model           string          `json:"model"`
	Messages        []llm.Message   `json:"messages"`
	Stream          bool            `json:"stream"`
	MaxTokens       int             `json:"max_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	ResponseFormat  *responseFormat `json:"response_format,omitempty"`
	Thinking        *thinking       `json:"thinking,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type thinking struct {
	Type string `json:"type"`
}

type chatCompletionResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage usage `json:"usage"`
}

type usage struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
}

func (u usage) toUsage() llm.Usage {
	return llm.Usage{
		InputTokens:     u.PromptTokens,
		OutputTokens:    u.CompletionTokens,
		CacheReadTokens: u.PromptCacheHitTokens,
	}
}

func truncate(data []byte, limit int) string {
	if len(data) <= limit {
		return string(data)
	}
	return string(data[:limit]) + "...(truncated)"
}
