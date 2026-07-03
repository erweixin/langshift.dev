package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type hashedRequest struct {
	Surface         string          `json:"surface"`
	Tier            string          `json:"tier"`
	Provider        string          `json:"provider"`
	Model           string          `json:"model"`
	Messages        []Message       `json:"messages"`
	JSONMode        bool            `json:"json_mode"`
	SchemaName      string          `json:"schema_name"`
	MaxTokens       int             `json:"max_tokens"`
	Temperature     *float64        `json:"temperature,omitempty"`
	Thinking        Thinking        `json:"thinking"`
	PromptVersion   string          `json:"prompt_version"`
	ContextManifest json.RawMessage `json:"context_manifest"`
}

type hashedResponse struct {
	Content           string          `json:"content"`
	ProviderRequestID string          `json:"provider_request_id"`
	FinishReason      string          `json:"finish_reason"`
	Raw               json.RawMessage `json:"raw,omitempty"`
}

func requestHash(req Request) (string, error) {
	encoded, err := json.Marshal(hashedRequest{
		Surface:         req.Surface,
		Tier:            req.Tier,
		Provider:        req.Provider,
		Model:           req.Model,
		Messages:        req.Messages,
		JSONMode:        req.JSONMode,
		SchemaName:      req.SchemaName,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		Thinking:        req.Thinking,
		PromptVersion:   req.PromptVersion,
		ContextManifest: req.ContextManifest,
	})
	if err != nil {
		return "", fmt.Errorf("marshal llm request hash input: %w", err)
	}
	return sha256Hex(encoded), nil
}

func resultHash(response Response) (string, error) {
	encoded, err := json.Marshal(hashedResponse{
		Content:           response.Content,
		ProviderRequestID: response.ProviderRequestID,
		FinishReason:      response.FinishReason,
		Raw:               response.Raw,
	})
	if err != nil {
		return "", fmt.Errorf("marshal llm result hash input: %w", err)
	}
	return sha256Hex(encoded), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
