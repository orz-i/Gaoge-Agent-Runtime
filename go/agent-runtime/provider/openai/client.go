// Package openai implements model.Client and model.StreamingClient via
// OpenAI Responses API. No provider types escape this adapter.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const endpoint = "https://api.openai.com/v1/responses"

type Config struct {
	APIKey       string
	DefaultModel string
	Endpoint     string
	HTTPClient   *http.Client
	// StructuredOutputModels opts explicitly named models/gateways into strict
	// JSON Schema output. Unknown models otherwise fail closed.
	StructuredOutputModels []string
}
type Client struct {
	http                   *providerhttp.Client
	defaultModel           string
	structuredOutputModels map[string]bool
}

func New(config Config) (*Client, error) {
	httpClient, err := providerhttp.New(providerhttp.Config{
		APIKey: config.APIKey, Endpoint: config.Endpoint, HTTPClient: config.HTTPClient,
	}, endpoint, http.Header{"Authorization": {"Bearer " + config.APIKey}})
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(config.StructuredOutputModels))
	for _, name := range config.StructuredOutputModels {
		if name == "" || strings.TrimSpace(name) != name {
			return nil, providerhttp.ErrConfig
		}
		allowed[name] = true
	}
	return &Client{http: httpClient, defaultModel: strings.TrimSpace(config.DefaultModel), structuredOutputModels: allowed}, nil
}
func (*Client) ProviderName() string { return "openai" }

type outputItem struct {
	Type    string `json:"type"`
	Content []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	CallID           string `json:"call_id"`
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
	EncryptedContent string `json:"encrypted_content"`
}
type responseBody struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Output []json.RawMessage `json:"output"`
	Usage  *struct {
		Input        *int64 `json:"input_tokens"`
		Output       *int64 `json:"output_tokens"`
		InputDetails struct {
			Cached int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputDetails struct {
			Reasoning int64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}
type prepared struct {
	body  map[string]any
	names map[string]string
}

var ErrUnsupported = errors.New("unsupported OpenAI model request or response")

// ErrReplayUnavailable means a reasoning Tool response lacks the encrypted
// evidence required for stateless continuation. Never retry by dropping it.
var ErrReplayUnavailable = errors.New("OpenAI encrypted reasoning replay unavailable")

// ErrIncomplete means the provider did not return a completed logical response.
var ErrIncomplete = errors.New("OpenAI response incomplete")

func (c *Client) prepare(request model.Request, streaming bool) (prepared, error) {
	if c == nil || len(request.HostedTools) != 0 {
		return prepared{}, ErrUnsupported
	}
	options, err := providerhttp.ParseOptions(request.ModelOptions)
	if err != nil {
		return prepared{}, err
	}
	name := strings.TrimSpace(request.Model)
	if name == "" {
		name = c.defaultModel
	}
	if name == "" {
		return prepared{}, ErrUnsupported
	}
	if request.OutputContract != nil {
		if err := model.ValidateOutputContract(request.OutputContract); err != nil {
			return prepared{}, errors.Join(ErrUnsupported, err)
		}
		caps, err := c.Capabilities(context.Background(), name)
		if err != nil || !caps.StructuredJSONSchema {
			return prepared{}, errors.Join(ErrUnsupported, model.ErrCapabilityUnavailable)
		}
	}
	byName := make(map[string]string, len(request.Tools))
	byKey := make(map[string]string, len(request.Tools))
	definitions := make([]map[string]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		if tool.Key == "" || tool.Name == "" || !json.Valid(tool.InputSchema) || byName[tool.Name] != "" {
			return prepared{}, ErrUnsupported
		}
		byName[tool.Name], byKey[tool.Key] = tool.Key, tool.Name
		definitions = append(definitions, map[string]any{
			"type": "function", "name": tool.Name, "description": tool.Description,
			"parameters": tool.InputSchema, "strict": false,
		})
	}
	input := make([]any, 0, len(request.Messages))
	for _, m := range request.Messages {
		if m.ProviderReplay != nil {
			if m.Role != model.RoleAssistant || m.ToolCallID != "" {
				return prepared{}, ErrUnsupported
			}
			items, replayErr := verifiedReplay(m)
			if replayErr != nil {
				return prepared{}, replayErr
			}
			for _, item := range items {
				input = append(input, item)
			}
			continue
		}
		switch m.Role {
		case model.RoleSystem, model.RoleUser:
			if len(m.ToolCalls) != 0 || m.ToolCallID != "" {
				return prepared{}, ErrUnsupported
			}
			input = append(input, map[string]any{"role": string(m.Role), "content": m.Content})
		case model.RoleAssistant:
			if m.ToolCallID != "" {
				return prepared{}, ErrUnsupported
			}
			if m.Content != "" {
				input = append(input, map[string]any{"role": "assistant", "content": m.Content})
			}
			for _, call := range m.ToolCalls {
				toolName := call.Name
				if toolName == "" {
					toolName = byKey[call.ToolKey]
				}
				if call.ID == "" || toolName == "" || !json.Valid(call.Arguments) {
					return prepared{}, ErrUnsupported
				}
				input = append(input, map[string]any{
					"type": "function_call", "call_id": call.ID, "name": toolName, "arguments": string(call.Arguments),
				})
			}
		case model.RoleTool:
			if m.ToolCallID == "" || len(m.ToolCalls) != 0 {
				return prepared{}, ErrUnsupported
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
		default:
			return prepared{}, ErrUnsupported
		}
	}
	payload := map[string]any{
		"model": name, "input": input, "store": false, "stream": streaming,
	}
	if request.OutputContract != nil {
		payload["text"] = map[string]any{"format": map[string]any{
			"type": "json_schema", "name": request.OutputContract.Name,
			"schema": request.OutputContract.Schema, "strict": true,
		}}
	}
	if len(definitions) != 0 {
		// Tool continuation needs encrypted reasoning in stateless mode.
		// Do not impose this parameter on ordinary text-only proxies.
		payload["include"] = []string{"reasoning.encrypted_content"}
	}
	if request.MaxOutputTokens > 0 {
		payload["max_output_tokens"] = request.MaxOutputTokens
	}
	if len(definitions) != 0 {
		payload["tools"] = definitions
		if request.RequireToolCall {
			payload["tool_choice"] = "required"
		}
	} else if request.RequireToolCall {
		return prepared{}, ErrUnsupported
	}
	for key, value := range options {
		payload[key] = value
	}
	return prepared{body: payload, names: byName}, nil
}
func decodeResponse(body responseBody, names map[string]string) (model.Response, error) {
	if body.Status == "incomplete" {
		return model.Response{}, errors.Join(ErrUnsupported, ErrIncomplete)
	}
	if body.Status != "completed" || strings.TrimSpace(body.ID) == "" {
		return model.Response{}, ErrUnsupported
	}
	result := model.Response{ResponseID: body.ID}
	if body.Usage != nil && body.Usage.Input != nil && body.Usage.Output != nil {
		result.Usage = &model.Usage{
			InputTokens: *body.Usage.Input, OutputTokens: *body.Usage.Output,
			CacheReadTokens: body.Usage.InputDetails.Cached, ReasoningTokens: body.Usage.OutputDetails.Reasoning,
		}
	}
	var output strings.Builder
	var sawReasoning, missingEncrypted, sawRefusal bool
	for _, raw := range body.Output {
		var item outputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return model.Response{}, ErrUnsupported
		}
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					output.WriteString(part.Text)
				case "refusal":
					if part.Refusal == "" {
						return model.Response{}, ErrUnsupported
					}
					sawRefusal = true
					output.WriteString(part.Refusal)
				default:
					return model.Response{}, ErrUnsupported
				}
			}
		case "function_call":
			key := names[item.Name]
			if key == "" || item.CallID == "" || !json.Valid([]byte(item.Arguments)) {
				return model.Response{}, ErrUnsupported
			}
			result.ToolCalls = append(result.ToolCalls, tools.Call{
				ID: item.CallID, ToolKey: key, Name: item.Name, Arguments: json.RawMessage(item.Arguments),
			})
		case "reasoning":
			sawReasoning = true
			if item.EncryptedContent == "" {
				missingEncrypted = true
			}
		default:
			return model.Response{}, ErrUnsupported
		}
	}
	result.Content = output.String()
	result.Refused = sawRefusal
	if sawRefusal && len(result.ToolCalls) != 0 {
		return model.Response{}, ErrUnsupported
	}
	if sawReasoning && len(result.ToolCalls) != 0 {
		if missingEncrypted {
			return model.Response{}, errors.Join(ErrUnsupported, ErrReplayUnavailable)
		}
		replay, err := encodeReplay(body.Output)
		if err != nil {
			return model.Response{}, err
		}
		result.ProviderReplay = replay
	}
	if result.Content == "" && len(result.ToolCalls) == 0 {
		return model.Response{}, ErrUnsupported
	}
	return result, nil
}
func (c *Client) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	prepared, err := c.prepare(request, false)
	if err != nil {
		return model.Response{}, err
	}
	resp, err := c.http.Post(ctx, prepared.body, false)
	if err != nil {
		return model.Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var body responseBody
	if err := providerhttp.ReadJSON(resp, &body); err != nil {
		return model.Response{}, err
	}
	return decodeResponse(body, prepared.names)
}
func (c *Client) GenerateStream(ctx context.Context, request model.Request, emit func(model.StreamEvent) error) (model.Response, error) {
	prepared, err := c.prepare(request, true)
	if err != nil {
		return model.Response{}, err
	}
	resp, err := c.http.Post(ctx, prepared.body, true)
	if err != nil {
		return model.Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var final *responseBody
	err = providerhttp.Stream(resp, func(event string, data []byte) error {
		if string(data) == "[DONE]" && final != nil {
			return nil
		}
		if final != nil {
			return providerhttp.ErrProtocol
		}
		var frame struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta"`
			ItemID   string          `json:"item_id"`
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			return providerhttp.ErrProtocol
		}
		if event != "" && event != frame.Type {
			return providerhttp.ErrProtocol
		}
		switch frame.Type {
		case "response.output_text.delta", "response.refusal.delta":
			if emit != nil {
				return emit(model.StreamEvent{Delta: frame.Delta})
			}
		case "response.reasoning_summary_text.delta":
			if emit != nil {
				return emit(model.StreamEvent{Reasoning: &model.ReasoningDelta{
					EventType: frame.Type, ItemID: frame.ItemID, Kind: "summary", Text: frame.Delta,
				}})
			}
		case "response.completed":
			var result responseBody
			if final != nil || json.Unmarshal(frame.Response, &result) != nil {
				return providerhttp.ErrProtocol
			}
			final = &result
		case "response.incomplete":
			return errors.Join(ErrUnsupported, ErrIncomplete)
		case "response.failed", "error":
			return ErrUnsupported
		}
		return nil
	})
	if err != nil {
		return model.Response{}, err
	}
	if final == nil {
		return model.Response{}, providerhttp.ErrProtocol
	}
	result, err := decodeResponse(*final, prepared.names)
	if err == nil && emit != nil {
		if emitErr := emit(model.StreamEvent{ResponseID: result.ResponseID, Usage: model.CloneUsage(result.Usage)}); emitErr != nil {
			return model.Response{}, emitErr
		}
	}
	return result, err
}

var _ model.Client = (*Client)(nil)
var _ model.StreamingClient = (*Client)(nil)
var _ model.ProviderNamer = (*Client)(nil)
