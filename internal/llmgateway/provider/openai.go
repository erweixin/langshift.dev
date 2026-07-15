package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type OpenAIConfig struct {
	MaximumRequestBytes int
	MaximumEventBytes   int
	MaximumTextBytes    int
	Now                 func() time.Time
}

type OpenAI struct {
	limits limits
}

func NewOpenAI(config OpenAIConfig) (*OpenAI, error) {
	configured, err := newLimits(config.MaximumRequestBytes, config.MaximumEventBytes, config.MaximumTextBytes, config.Now)
	if err != nil {
		return nil, err
	}
	return &OpenAI{limits: configured}, nil
}

type openAIRequest struct {
	Model             string       `json:"model"`
	Instructions      string       `json:"instructions,omitempty"`
	Input             []any        `json:"input"`
	Tools             []openAITool `json:"tools,omitempty"`
	ToolChoice        any          `json:"tool_choice,omitempty"`
	MaxOutputTokens   uint32       `json:"max_output_tokens"`
	Temperature       *float64     `json:"temperature,omitempty"`
	TopP              *float64     `json:"top_p,omitempty"`
	Stream            bool         `json:"stream"`
	Store             bool         `json:"store"`
	ParallelToolCalls bool         `json:"parallel_tool_calls"`
}

type openAITool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      bool            `json:"strict"`
}

func (adapter *OpenAI) Prepare(request Request) (PreparedRequest, error) {
	if adapter == nil {
		return PreparedRequest{}, ErrInvalidRequest
	}
	wire, err := buildOpenAIRequest(request)
	if err != nil {
		return PreparedRequest{}, err
	}
	payload, requestHash, err := marshalRequest(request, wire, adapter.limits.requestBytes)
	if err != nil {
		return PreparedRequest{}, err
	}
	return prepared("openai", payload, requestHash, request.Stream), nil
}

func (adapter *OpenAI) ExecutePrepared(ctx context.Context, client Client, request PreparedRequest, sink DeltaSink) (Result, error) {
	if adapter == nil || client == nil || ctx == nil || !request.valid("openai") {
		return Result{}, ErrInvalidRequest
	}
	headers := http.Header{"Content-Type": []string{"application/json"}, "Accept": []string{"application/json"}}
	if request.stream {
		headers.Set("Accept", "text/event-stream")
	}
	response, err := client.Do(ctx, http.MethodPost, "responses", headers, bytes.NewReader(request.payload))
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
	result, err := parseOpenAIResponse(body, adapter.limits.textBytes)
	result.RequestHash, result.ResponseHash = request.requestHash, responseHash
	if err != nil {
		return result, parseCallError(err, result)
	}
	return result, nil
}

func buildOpenAIRequest(request Request) (openAIRequest, error) {
	if err := validateRequest(request); err != nil {
		return openAIRequest{}, err
	}
	input := make([]any, 0, len(request.Messages))
	for _, message := range request.Messages {
		content := make([]any, 0, len(message.Content))
		flush := func() {
			if len(content) > 0 {
				input = append(input, map[string]any{"type": "message", "role": message.Role, "content": content})
				content = nil
			}
		}
		for _, block := range message.Content {
			switch block.Type {
			case "text":
				blockType := "input_text"
				if message.Role == "assistant" {
					blockType = "output_text"
				}
				content = append(content, map[string]any{"type": blockType, "text": block.Text})
			case "image":
				if message.Role != "user" {
					return openAIRequest{}, ErrInvalidRequest
				}
				content = append(content, map[string]any{"type": "input_image", "image_url": "data:" + block.MediaType + ";base64," + block.DataBase64})
			case "tool_use":
				flush()
				input = append(input, map[string]any{"type": "function_call", "call_id": block.ToolUseID, "name": block.ToolName, "arguments": string(block.ToolInput)})
			case "tool_result":
				flush()
				input = append(input, map[string]any{"type": "function_call_output", "call_id": block.ToolUseID, "output": block.Text})
			}
		}
		flush()
	}
	tools := make([]openAITool, 0, len(request.Tools))
	for _, tool := range request.Tools {
		tools = append(tools, openAITool{Type: "function", Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema, Strict: tool.Strict})
	}
	choice := any(nil)
	switch request.ToolChoice.Mode {
	case "auto":
		choice = "auto"
	case "required":
		choice = "required"
	case "none":
		choice = "none"
	case "tool":
		choice = map[string]any{"type": "function", "name": request.ToolChoice.Name}
	}
	return openAIRequest{Model: request.Model, Instructions: request.System, Input: input, Tools: tools, ToolChoice: choice, MaxOutputTokens: request.MaxOutputTokens, Temperature: request.Temperature, TopP: request.TopP, Stream: request.Stream, Store: false, ParallelToolCalls: true}, nil
}

type openAIResponse struct {
	ID     string `json:"id"`
	Model  string `json:"model"`
	Status string `json:"status"`
	Output []struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Content   []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		InputTokens  uint64 `json:"input_tokens"`
		OutputTokens uint64 `json:"output_tokens"`
	} `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func parseOpenAIResponse(payload []byte, maximumTextBytes int) (Result, error) {
	var response openAIResponse
	if json.Unmarshal(payload, &response) != nil || response.ID == "" || response.Model == "" {
		return Result{}, ErrResponseInvalid
	}
	result := Result{ProviderRequestID: response.ID, Model: response.Model, Status: response.Status, InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens}
	if result.Status == "" {
		return result, ErrResponseInvalid
	}
	var text strings.Builder
	for _, output := range response.Output {
		switch output.Type {
		case "message":
			for _, content := range output.Content {
				switch content.Type {
				case "output_text":
					if text.Len()+len(content.Text) > maximumTextBytes {
						return result, ErrResponseInvalid
					}
					text.WriteString(content.Text)
				case "refusal":
					result.Refusal += content.Refusal
				}
			}
		case "function_call":
			arguments := json.RawMessage(output.Arguments)
			if output.CallID == "" || !validName(output.Name) || !validJSONObject(arguments) {
				return result, ErrResponseInvalid
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{ID: output.CallID, Name: output.Name, Input: append(json.RawMessage(nil), arguments...)})
		}
	}
	result.Text = text.String()
	switch response.Status {
	case "completed":
		if len(result.ToolCalls) > 0 {
			result.FinishReason = "tool_use"
		} else {
			result.FinishReason = "stop"
		}
	case "incomplete":
		if response.IncompleteDetails == nil {
			return result, ErrResponseInvalid
		}
		if response.IncompleteDetails.Reason == "max_output_tokens" {
			result.FinishReason = "max_tokens"
		} else if response.IncompleteDetails.Reason == "content_filter" {
			result.FinishReason = "content_filter"
		} else {
			return result, ErrResponseInvalid
		}
	case "failed":
		if response.Error == nil {
			return result, ErrResponseInvalid
		}
		return result, &CallError{Class: classifyHTTP(http.StatusInternalServerError, response.Error.Code, response.Error.Message), Code: response.Error.Code, ProviderRequestID: response.ID}
	default:
		return result, ErrResponseInvalid
	}
	return result, nil
}

func (adapter *OpenAI) consumeStream(ctx context.Context, body io.Reader, sink DeltaSink) (Result, error) {
	var result Result
	var terminal bool
	var visible *time.Time
	responseHash, err := consumeSSE(body, adapter.limits.eventBytes, func(event sseEvent) error {
		if terminal {
			return ErrResponseInvalid
		}
		if event.Name == "" {
			var named struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(event.Data, &named) != nil {
				return ErrResponseInvalid
			}
			event.Name = named.Type
		}
		switch event.Name {
		case "response.output_text.delta":
			var delta struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal(event.Data, &delta) != nil || delta.Delta == "" || len(result.Text)+len(delta.Delta) > adapter.limits.textBytes {
				return ErrResponseInvalid
			}
			result.Text += delta.Delta
			return emitDelta(ctx, sink, adapter.limits.eventBytes, &visible, adapter.limits.now, Delta{Kind: "text", Text: delta.Delta})
		case "response.function_call_arguments.delta":
			var delta struct {
				ItemID string `json:"item_id"`
				Delta  string `json:"delta"`
			}
			if json.Unmarshal(event.Data, &delta) != nil || delta.ItemID == "" {
				return ErrResponseInvalid
			}
			return emitDelta(ctx, sink, adapter.limits.eventBytes, &visible, adapter.limits.now, Delta{Kind: "tool_input", ToolUseID: delta.ItemID, JSON: delta.Delta})
		case "response.completed", "response.incomplete", "response.failed":
			var envelope struct {
				Response json.RawMessage `json:"response"`
			}
			if json.Unmarshal(event.Data, &envelope) != nil || len(envelope.Response) == 0 {
				return ErrResponseInvalid
			}
			parsed, parseErr := parseOpenAIResponse(envelope.Response, adapter.limits.textBytes)
			if parseErr != nil {
				return parseErr
			}
			if result.Text != "" && parsed.Text != result.Text {
				return fmt.Errorf("%w: streamed text does not match terminal response", ErrResponseInvalid)
			}
			result = parsed
			terminal = true
		case "error":
			var failure providerErrorEnvelope
			_ = json.Unmarshal(event.Data, &failure)
			return &CallError{Class: classifyHTTP(http.StatusInternalServerError, failure.Error.Code, failure.Error.Message), Code: failure.Error.Code}
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
