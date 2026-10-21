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
}
type Client struct {
	http         *providerhttp.Client
	defaultModel string
}

func New(config Config) (*Client, error) {
	httpClient, err := providerhttp.New(providerhttp.Config{
		APIKey: config.APIKey, Endpoint: config.Endpoint, HTTPClient: config.HTTPClient,
	}, endpoint, http.Header{"Authorization": {"Bearer " + config.APIKey}})
	if err != nil {
		return nil, err
	}
	return &Client{http: httpClient, defaultModel: strings.TrimSpace(config.DefaultModel)}, nil
}
func (*Client) ProviderName() string { return "openai" }

type outputItem struct {
	Type    string `json:"type"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type responseBody struct {
	ID     string       `json:"id"`
	Status string       `json:"status"`
	Output []outputItem `json:"output"`
	Usage  struct {
		Input        int64 `json:"input_tokens"`
		Output       int64 `json:"output_tokens"`
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
	input := make([]map[string]any, 0, len(request.Messages))
	for _, m := range request.Messages {
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
	payload := map[string]any{"model": name, "input": input, "store": false, "stream": streaming}
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
	if body.Status != "completed" || strings.TrimSpace(body.ID) == "" {
		return model.Response{}, ErrUnsupported
	}
	result := model.Response{ResponseID: body.ID, Usage: &model.Usage{
		InputTokens: body.Usage.Input, OutputTokens: body.Usage.Output,
		CacheReadTokens: body.Usage.InputDetails.Cached, ReasoningTokens: body.Usage.OutputDetails.Reasoning,
	}}
	var output strings.Builder
	var sawReasoning bool
	for _, item := range body.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					output.WriteString(part.Text)
				} else if part.Type != "refusal" {
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
			// The current model.Message cannot replay opaque reasoning items.
			// A reasoning + Tool response must not be silently replayed with
			// missing provider state in the next Agent step.
			sawReasoning = true
		default:
			return model.Response{}, ErrUnsupported
		}
	}
	result.Content = output.String()
	if sawReasoning && len(result.ToolCalls) != 0 {
		return model.Response{}, ErrUnsupported
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
		case "response.output_text.delta":
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
		case "response.failed", "response.incomplete", "error":
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
