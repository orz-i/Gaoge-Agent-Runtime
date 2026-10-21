// Package anthropic implements model.Client and model.StreamingClient via
// Anthropic Messages API. Only stateless text and client Tool calls are exposed.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const (
	endpoint         = "https://api.anthropic.com/v1/messages"
	apiVersion       = "2023-06-01"
	defaultMaxTokens = 4096
)

type Config struct {
	APIKey                 string
	DefaultModel           string
	Endpoint               string
	HTTPClient             *http.Client
	DefaultMaxOutputTokens int64
}
type Client struct {
	http         *providerhttp.Client
	defaultModel string
	maxTokens    int64
}

var ErrUnsupported = errors.New("unsupported Anthropic model request or response")

func New(config Config) (*Client, error) {
	if config.DefaultMaxOutputTokens < 0 {
		return nil, providerhttp.ErrConfig
	}
	h, err := providerhttp.New(providerhttp.Config{
		APIKey: config.APIKey, Endpoint: config.Endpoint, HTTPClient: config.HTTPClient,
	}, endpoint, http.Header{"X-Api-Key": {config.APIKey}, "Anthropic-Version": {apiVersion}})
	if err != nil {
		return nil, err
	}
	tokens := config.DefaultMaxOutputTokens
	if tokens == 0 {
		tokens = defaultMaxTokens
	}
	return &Client{http: h, defaultModel: strings.TrimSpace(config.DefaultModel), maxTokens: tokens}, nil
}
func (*Client) ProviderName() string { return "anthropic" }

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}
type usage struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
}
type messageBody struct {
	ID         string         `json:"id"`
	StopReason string         `json:"stop_reason"`
	Content    []contentBlock `json:"content"`
	Usage      usage          `json:"usage"`
}
type prepared struct {
	body  map[string]any
	names map[string]string
}

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
	tokens := request.MaxOutputTokens
	if tokens == 0 {
		tokens = c.maxTokens
	}
	if tokens <= 0 {
		return prepared{}, ErrUnsupported
	}
	byName := make(map[string]string, len(request.Tools))
	byKey := make(map[string]string, len(request.Tools))
	definitions := make([]map[string]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		if tool.Name == "" || tool.Key == "" || !json.Valid(tool.InputSchema) || byName[tool.Name] != "" {
			return prepared{}, ErrUnsupported
		}
		byName[tool.Name], byKey[tool.Key] = tool.Key, tool.Name
		definitions = append(definitions, map[string]any{
			"name": tool.Name, "description": tool.Description,
			"input_schema": tool.InputSchema,
		})
	}
	var systems []string
	messages := make([]map[string]any, 0, len(request.Messages))
	seenNonSystem := false
	for _, m := range request.Messages {
		switch m.Role {
		case model.RoleSystem:
			if seenNonSystem || m.ToolCallID != "" || len(m.ToolCalls) != 0 {
				return prepared{}, ErrUnsupported
			}
			systems = append(systems, m.Content)
		case model.RoleUser:
			seenNonSystem = true
			if m.ToolCallID != "" || len(m.ToolCalls) != 0 {
				return prepared{}, ErrUnsupported
			}
			messages = append(messages, map[string]any{"role": "user", "content": m.Content})
		case model.RoleAssistant:
			seenNonSystem = true
			if m.ToolCallID != "" {
				return prepared{}, ErrUnsupported
			}
			blocks := make([]map[string]any, 0, len(m.ToolCalls)+1)
			if m.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			for _, call := range m.ToolCalls {
				toolName := call.Name
				if toolName == "" {
					toolName = byKey[call.ToolKey]
				}
				if call.ID == "" || toolName == "" || !json.Valid(call.Arguments) {
					return prepared{}, ErrUnsupported
				}
				blocks = append(blocks, map[string]any{
					"type": "tool_use", "id": call.ID, "name": toolName, "input": call.Arguments,
				})
			}
			if len(blocks) == 0 {
				return prepared{}, ErrUnsupported
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": blocks})
		case model.RoleTool:
			seenNonSystem = true
			if m.ToolCallID == "" || len(m.ToolCalls) != 0 {
				return prepared{}, ErrUnsupported
			}
			block := map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content}
			if len(messages) > 0 && messages[len(messages)-1]["role"] == "user" {
				if blocks, ok := messages[len(messages)-1]["content"].([]map[string]any); ok {
					messages[len(messages)-1]["content"] = append(blocks, block)
					continue
				}
			}
			messages = append(messages, map[string]any{"role": "user", "content": []map[string]any{block}})
		default:
			return prepared{}, ErrUnsupported
		}
	}
	if len(messages) == 0 {
		return prepared{}, ErrUnsupported
	}
	payload := map[string]any{
		"model": name, "max_tokens": tokens, "messages": messages, "stream": streaming,
	}
	if len(systems) > 0 {
		payload["system"] = strings.Join(systems, "\n\n")
	}
	if len(definitions) > 0 {
		payload["tools"] = definitions
		if request.RequireToolCall {
			payload["tool_choice"] = map[string]any{"type": "any"}
		}
	} else if request.RequireToolCall {
		return prepared{}, ErrUnsupported
	}
	for key, value := range options {
		payload[key] = value
	}
	return prepared{body: payload, names: byName}, nil
}

func decodeResponse(body messageBody, names map[string]string) (model.Response, error) {
	switch body.StopReason {
	case "end_turn", "tool_use", "stop_sequence", "refusal":
	default:
		return model.Response{}, ErrUnsupported
	}
	if body.ID == "" || body.Usage.Input < 0 || body.Usage.Output < 0 ||
		body.Usage.CacheRead < 0 || body.Usage.CacheWrite < 0 ||
		body.Usage.Input > math.MaxInt64-body.Usage.CacheRead ||
		body.Usage.Input+body.Usage.CacheRead > math.MaxInt64-body.Usage.CacheWrite {
		return model.Response{}, ErrUnsupported
	}
	result := model.Response{ResponseID: body.ID, Usage: &model.Usage{
		InputTokens:  body.Usage.Input + body.Usage.CacheRead + body.Usage.CacheWrite,
		OutputTokens: body.Usage.Output, CacheReadTokens: body.Usage.CacheRead,
		CacheWriteTokens: body.Usage.CacheWrite,
	}}
	var output strings.Builder
	for _, block := range body.Content {
		switch block.Type {
		case "text":
			output.WriteString(block.Text)
		case "tool_use":
			key := names[block.Name]
			if key == "" || block.ID == "" || !json.Valid(block.Input) {
				return model.Response{}, ErrUnsupported
			}
			result.ToolCalls = append(result.ToolCalls, tools.Call{
				ID: block.ID, ToolKey: key, Name: block.Name, Arguments: append(json.RawMessage(nil), block.Input...),
			})
		default:
			// Thinking/redacted-thinking cannot be losslessly replayed with
			// today's text-only model.Message; fail, do not discard it.
			return model.Response{}, ErrUnsupported
		}
	}
	result.Content = output.String()
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
	var body messageBody
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
	var body messageBody
	var started, stopped bool
	var blocks = map[int]*contentBlock{}
	var chunks = map[int]*strings.Builder{}
	var closed = map[int]bool{}
	var resultBytes int
	err = providerhttp.Stream(resp, func(event string, data []byte) error {
		var frame struct {
			Type         string       `json:"type"`
			Message      messageBody  `json:"message"`
			Index        int          `json:"index"`
			ContentBlock contentBlock `json:"content_block"`
			Delta        struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage usage `json:"usage"`
		}
		if err := json.Unmarshal(data, &frame); err != nil || (event != "" && event != frame.Type) {
			return providerhttp.ErrProtocol
		}
		if stopped {
			return providerhttp.ErrProtocol
		}
		switch frame.Type {
		case "message_start":
			if started {
				return providerhttp.ErrProtocol
			}
			started, body = true, frame.Message
			body.Content = nil
		case "content_block_start":
			if !started || blocks[frame.Index] != nil || frame.Index < 0 || frame.Index > 1024 {
				return providerhttp.ErrProtocol
			}
			b := frame.ContentBlock
			if b.Type != "text" && b.Type != "tool_use" {
				return ErrUnsupported
			}
			resultBytes += len(b.Text) + len(b.Input)
			if resultBytes > 16<<20 {
				return providerhttp.ErrProtocol
			}
			blocks[frame.Index] = &b
			chunks[frame.Index] = &strings.Builder{}
		case "content_block_delta":
			b := blocks[frame.Index]
			if b == nil || closed[frame.Index] {
				return providerhttp.ErrProtocol
			}
			resultBytes += len(frame.Delta.Text) + len(frame.Delta.PartialJSON)
			if resultBytes > 16<<20 {
				return providerhttp.ErrProtocol
			}
			switch frame.Delta.Type {
			case "text_delta":
				if b.Type != "text" {
					return providerhttp.ErrProtocol
				}
				b.Text += frame.Delta.Text
				if emit != nil {
					return emit(model.StreamEvent{Delta: frame.Delta.Text})
				}
			case "input_json_delta":
				if b.Type != "tool_use" {
					return providerhttp.ErrProtocol
				}
				if _, err := chunks[frame.Index].WriteString(frame.Delta.PartialJSON); err != nil {
					return providerhttp.ErrProtocol
				}
			default:
				return ErrUnsupported
			}
		case "content_block_stop":
			b := blocks[frame.Index]
			if b == nil || closed[frame.Index] {
				return providerhttp.ErrProtocol
			}
			closed[frame.Index] = true
			if b.Type == "tool_use" && chunks[frame.Index].Len() > 0 {
				b.Input = json.RawMessage(chunks[frame.Index].String())
			}
		case "message_delta":
			body.StopReason = frame.Delta.StopReason
			body.Usage.Output = frame.Usage.Output
		case "message_stop":
			if !started || len(blocks) != len(closed) {
				return providerhttp.ErrProtocol
			}
			stopped = true
		case "ping":
		case "error":
			return ErrUnsupported
		default:
			return ErrUnsupported
		}
		return nil
	})
	if err != nil {
		return model.Response{}, err
	}
	if !started || !stopped {
		return model.Response{}, providerhttp.ErrProtocol
	}
	for index := 0; index < len(blocks); index++ {
		b := blocks[index]
		if b == nil {
			return model.Response{}, providerhttp.ErrProtocol
		}
		body.Content = append(body.Content, *b)
	}
	result, err := decodeResponse(body, prepared.names)
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
