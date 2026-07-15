package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type AnthropicConfig struct {
	APIVersion          string
	MaximumRequestBytes int
	MaximumEventBytes   int
	MaximumTextBytes    int
	Now                 func() time.Time
}

type Anthropic struct {
	apiVersion string
	limits     limits
}

func NewAnthropic(config AnthropicConfig) (*Anthropic, error) {
	if config.APIVersion == "" {
		config.APIVersion = "2023-06-01"
	}
	if len(config.APIVersion) != 10 || config.APIVersion[4] != '-' || config.APIVersion[7] != '-' {
		return nil, ErrInvalidRequest
	}
	configured, err := newLimits(config.MaximumRequestBytes, config.MaximumEventBytes, config.MaximumTextBytes, config.Now)
	if err != nil {
		return nil, err
	}
	return &Anthropic{apiVersion: config.APIVersion, limits: configured}, nil
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	ToolChoice  any                `json:"tool_choice,omitempty"`
	MaxTokens   uint32             `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	Stream      bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Source    *anthropicImage `json:"source,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type anthropicImage struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func (adapter *Anthropic) Prepare(request Request) (PreparedRequest, error) {
	if adapter == nil {
		return PreparedRequest{}, ErrInvalidRequest
	}
	wire, err := buildAnthropicRequest(request)
	if err != nil {
		return PreparedRequest{}, err
	}
	payload, requestHash, err := marshalRequest(request, wire, adapter.limits.requestBytes)
	if err != nil {
		return PreparedRequest{}, err
	}
	return prepared("anthropic", payload, requestHash, request.Stream), nil
}

func (adapter *Anthropic) ExecutePrepared(ctx context.Context, client Client, request PreparedRequest, sink DeltaSink) (Result, error) {
	if adapter == nil || client == nil || ctx == nil || !request.valid("anthropic") {
		return Result{}, ErrInvalidRequest
	}
	headers := http.Header{"Content-Type": []string{"application/json"}, "Accept": []string{"application/json"}, "Anthropic-Version": []string{adapter.apiVersion}}
	if request.stream {
		headers.Set("Accept", "text/event-stream")
	}
	response, err := client.Do(ctx, http.MethodPost, "messages", headers, bytes.NewReader(request.payload))
	if err != nil {
		return Result{RequestHash: request.requestHash}, transportCallError(ctx, err)
	}
	if response == nil || response.Body == nil {
		return Result{RequestHash: request.requestHash}, transportCallError(ctx, io.ErrUnexpectedEOF)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _, readErr := readResponse(response.Body, adapter.limits.eventBytes)
		if readErr != nil {
			return Result{RequestHash: request.requestHash}, transportCallError(ctx, readErr)
		}
		return Result{RequestHash: request.requestHash}, httpCallError(response, body, adapter.limits.now())
	}
	if request.stream {
		if err = ensureContentType(response, "text/event-stream"); err != nil {
			return Result{RequestHash: request.requestHash}, transportCallError(ctx, err)
		}
		result, streamErr := adapter.consumeStream(ctx, response.Body, sink)
		result.RequestHash = request.requestHash
		if streamErr != nil {
			return result, transportCallError(ctx, streamErr)
		}
		return result, nil
	}
	if err = ensureContentType(response, "application/json"); err != nil {
		return Result{RequestHash: request.requestHash}, transportCallError(ctx, err)
	}
	body, responseHash, err := readResponse(response.Body, adapter.limits.textBytes)
	if err != nil {
		return Result{RequestHash: request.requestHash}, transportCallError(ctx, err)
	}
	result, err := parseAnthropicResponse(body, adapter.limits.textBytes)
	result.RequestHash, result.ResponseHash = request.requestHash, responseHash
	if err != nil {
		return result, parseCallError(err, result)
	}
	return result, nil
}

func buildAnthropicRequest(request Request) (anthropicRequest, error) {
	if err := validateRequest(request); err != nil {
		return anthropicRequest{}, err
	}
	messages := make([]anthropicMessage, 0, len(request.Messages))
	for _, message := range request.Messages {
		blocks := make([]anthropicBlock, 0, len(message.Content))
		for _, block := range message.Content {
			switch block.Type {
			case "text":
				blocks = append(blocks, anthropicBlock{Type: "text", Text: block.Text})
			case "image":
				if message.Role != "user" {
					return anthropicRequest{}, ErrInvalidRequest
				}
				blocks = append(blocks, anthropicBlock{Type: "image", Source: &anthropicImage{Type: "base64", MediaType: block.MediaType, Data: block.DataBase64}})
			case "tool_use":
				blocks = append(blocks, anthropicBlock{Type: "tool_use", ID: block.ToolUseID, Name: block.ToolName, Input: block.ToolInput})
			case "tool_result":
				blocks = append(blocks, anthropicBlock{Type: "tool_result", ToolUseID: block.ToolUseID, Content: block.Text, IsError: block.IsError})
			}
		}
		messages = append(messages, anthropicMessage{Role: message.Role, Content: blocks})
	}
	tools := make([]anthropicTool, 0, len(request.Tools))
	for _, tool := range request.Tools {
		tools = append(tools, anthropicTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	choice := any(nil)
	switch request.ToolChoice.Mode {
	case "auto":
		choice = map[string]any{"type": "auto"}
	case "required":
		choice = map[string]any{"type": "any"}
	case "none":
		choice = map[string]any{"type": "none"}
	case "tool":
		choice = map[string]any{"type": "tool", "name": request.ToolChoice.Name}
	}
	return anthropicRequest{Model: request.Model, System: request.System, Messages: messages, Tools: tools, ToolChoice: choice, MaxTokens: request.MaxOutputTokens, Temperature: request.Temperature, TopP: request.TopP, Stream: request.Stream}, nil
}

type anthropicResponse struct {
	ID         string `json:"id"`
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	Usage struct {
		InputTokens  uint64 `json:"input_tokens"`
		OutputTokens uint64 `json:"output_tokens"`
	} `json:"usage"`
}

func parseAnthropicResponse(payload []byte, maximumTextBytes int) (Result, error) {
	var response anthropicResponse
	if json.Unmarshal(payload, &response) != nil || response.ID == "" || response.Model == "" || response.StopReason == "" {
		return Result{}, ErrResponseInvalid
	}
	result := Result{ProviderRequestID: response.ID, Model: response.Model, Status: "completed", FinishReason: anthropicFinishReason(response.StopReason), InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens}
	if result.FinishReason == "" {
		return result, ErrResponseInvalid
	}
	var text strings.Builder
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			if text.Len()+len(block.Text) > maximumTextBytes {
				return result, ErrResponseInvalid
			}
			text.WriteString(block.Text)
		case "tool_use":
			if block.ID == "" || !validName(block.Name) || !validJSONObject(block.Input) {
				return result, ErrResponseInvalid
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{ID: block.ID, Name: block.Name, Input: append(json.RawMessage(nil), block.Input...)})
		}
	}
	result.Text = text.String()
	return result, nil
}

func anthropicFinishReason(value string) string {
	switch value {
	case "end_turn", "stop_sequence", "pause_turn":
		return "stop"
	case "tool_use":
		return "tool_use"
	case "max_tokens", "model_context_window_exceeded":
		return "max_tokens"
	case "refusal":
		return "content_filter"
	default:
		return ""
	}
}

func (adapter *Anthropic) consumeStream(ctx context.Context, body io.Reader, sink DeltaSink) (Result, error) {
	result := Result{Status: "completed"}
	var terminal bool
	var visible *time.Time
	blocks := make(map[int]*ToolCall)
	responseHash, err := consumeSSE(body, adapter.limits.eventBytes, func(event sseEvent) error {
		if terminal {
			return ErrResponseInvalid
		}
		switch event.Name {
		case "message_start":
			var start struct {
				Message struct {
					ID    string `json:"id"`
					Model string `json:"model"`
					Usage struct {
						InputTokens uint64 `json:"input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(event.Data, &start) != nil || start.Message.ID == "" || start.Message.Model == "" {
				return ErrResponseInvalid
			}
			result.ProviderRequestID, result.Model, result.InputTokens = start.Message.ID, start.Message.Model, start.Message.Usage.InputTokens
		case "content_block_start":
			var start struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type  string          `json:"type"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content_block"`
			}
			if json.Unmarshal(event.Data, &start) != nil || start.Index < 0 {
				return ErrResponseInvalid
			}
			if start.ContentBlock.Type == "tool_use" {
				if start.ContentBlock.ID == "" || !validName(start.ContentBlock.Name) {
					return ErrResponseInvalid
				}
				blocks[start.Index] = &ToolCall{ID: start.ContentBlock.ID, Name: start.ContentBlock.Name}
			}
		case "content_block_delta":
			var delta struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if json.Unmarshal(event.Data, &delta) != nil || delta.Index < 0 {
				return ErrResponseInvalid
			}
			switch delta.Delta.Type {
			case "text_delta":
				if delta.Delta.Text == "" || len(result.Text)+len(delta.Delta.Text) > adapter.limits.textBytes {
					return ErrResponseInvalid
				}
				result.Text += delta.Delta.Text
				return emitDelta(ctx, sink, adapter.limits.eventBytes, &visible, adapter.limits.now, Delta{Kind: "text", Text: delta.Delta.Text})
			case "input_json_delta":
				call := blocks[delta.Index]
				if call == nil || len(call.Input)+len(delta.Delta.PartialJSON) > adapter.limits.eventBytes {
					return ErrResponseInvalid
				}
				call.Input = append(call.Input, delta.Delta.PartialJSON...)
				return emitDelta(ctx, sink, adapter.limits.eventBytes, &visible, adapter.limits.now, Delta{Kind: "tool_input", ToolUseID: call.ID, ToolName: call.Name, JSON: delta.Delta.PartialJSON})
			}
		case "content_block_stop":
			var stop struct {
				Index int `json:"index"`
			}
			if json.Unmarshal(event.Data, &stop) != nil {
				return ErrResponseInvalid
			}
			if call := blocks[stop.Index]; call != nil {
				if len(call.Input) == 0 {
					call.Input = json.RawMessage(`{}`)
				}
				if !validJSONObject(call.Input) {
					return ErrResponseInvalid
				}
				result.ToolCalls = append(result.ToolCalls, *call)
				delete(blocks, stop.Index)
			}
		case "message_delta":
			var delta struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					InputTokens  uint64 `json:"input_tokens"`
					OutputTokens uint64 `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(event.Data, &delta) != nil {
				return ErrResponseInvalid
			}
			result.FinishReason = anthropicFinishReason(delta.Delta.StopReason)
			if result.FinishReason == "" {
				return ErrResponseInvalid
			}
			if delta.Usage.InputTokens > 0 {
				result.InputTokens = delta.Usage.InputTokens
			}
			result.OutputTokens = delta.Usage.OutputTokens
		case "message_stop":
			if result.ProviderRequestID == "" || result.FinishReason == "" || len(blocks) != 0 {
				return ErrResponseInvalid
			}
			terminal = true
		case "error":
			var failure providerErrorEnvelope
			_ = json.Unmarshal(event.Data, &failure)
			return &CallError{Class: classifyHTTP(http.StatusInternalServerError, failure.Error.Type, failure.Error.Message), Code: failure.Error.Type, ProviderRequestID: result.ProviderRequestID}
		}
		return nil
	})
	result.ResponseHash, result.VisibleOutputStartedAt = responseHash, visible
	if err != nil {
		return result, err
	}
	if !terminal {
		return result, ErrStreamIncomplete
	}
	return result, nil
}
