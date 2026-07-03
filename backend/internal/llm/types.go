package llm

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

type Thinking struct {
	Type            string
	ReasoningEffort string
}

type Request struct {
	Surface     string
	Tier        string
	Provider    string
	Model       string
	Messages    []Message
	JSONMode    bool
	SchemaName  string
	MaxTokens   int
	Temperature *float64
	Thinking    Thinking
}

type Usage struct {
	InputTokens     int
	OutputTokens    int
	CacheReadTokens int
}

type Response struct {
	Content           string
	Usage             Usage
	ProviderRequestID string
	FinishReason      string
	Raw               json.RawMessage
}

type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
}
