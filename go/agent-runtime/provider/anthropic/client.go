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
	// StructuredOutputModels opts explicitly verified private/gateway model IDs
	// into native strict JSON Schema output; unknown IDs otherwise fail closed.
	StructuredOutputModels []string
}
type Client struct {
	http                   *providerhttp.Client
	defaultModel           string
	maxTokens              int64
	structuredOutputModels map[string]bool
}

var ErrUnsupported = errors.New("unsupported Anthropic model request or response")

// ErrIncomplete distinguishes token-truncated output from a user refusal or
// a completed response that violates a frozen JSON output contract.
var ErrIncomplete = errors.New("anthropic response ended at max_tokens")

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
	allowed := make(map[string]bool, len(config.StructuredOutputModels))
	for _, name := range config.StructuredOutputModels {
		if name == "" || strings.TrimSpace(name) != name {
			return nil, providerhttp.ErrConfig
		}
		allowed[name] = true
	}
	return &Client{http: h, defaultModel: strings.TrimSpace(config.DefaultModel), maxTokens: tokens, structuredOutputModels: allowed}, nil
}
func (*Client) ProviderName() string { return "anthropic" }

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
	// Preserve only vetted optional metadata in stateless Tool replay.
	Caller      json.RawMessage `json:"caller,omitempty"`
	ToolsetName json.RawMessage `json:"toolset_name,omitempty"`
	Citations   json.RawMessage `json:"citations,omitempty"`
}
type usage struct {
	Input      *int64 `json:"input_tokens"`
	Output     *int64 `json:"output_tokens"`
	CacheRead  *int64 `json:"cache_read_input_tokens"`
	CacheWrite *int64 `json:"cache_creation_input_tokens"`
}

func tokenCount(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

type messageBody struct {
	ID         string            `json:"id"`
	StopReason string            `json:"stop_reason"`
	Content    []json.RawMessage `json:"content"`
	Usage      *usage            `json:"usage"`
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
	if request.OutputContract != nil {
		if err := model.ValidateOutputContract(request.OutputContract); err != nil {
			return prepared{}, errors.Join(ErrUnsupported, err)
		}
		caps, err := c.Capabilities(context.Background(), name)
		if err != nil || !caps.StructuredJSONSchema {
			return prepared{}, errors.Join(ErrUnsupported, model.ErrCapabilityUnavailable)
		}
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
			if m.ProviderReplay != nil {
				return prepared{}, ErrUnsupported
			}
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
			if m.ProviderReplay != nil {
				replayed, err := verifiedReplay(m)
				if err != nil {
					return prepared{}, err
				}
				messages = append(messages, map[string]any{"role": "assistant", "content": replayed})
				continue
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
			if m.ProviderReplay != nil {
				return prepared{}, ErrUnsupported
			}
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
	if request.OutputContract != nil {
		payload["output_config"] = map[string]any{"format": map[string]any{
			"type": "json_schema", "schema": request.OutputContract.Schema,
		}}
	}
	if len(definitions) > 0 {
		payload["tools"] = definitions
		if request.RequireToolCall {
			if forcedChoiceUnsupported(name) {
				return prepared{}, ErrUnsupported
			}
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
	if body.StopReason == "max_tokens" {
		return model.Response{}, errors.Join(ErrUnsupported, ErrIncomplete)
	}
	switch body.StopReason {
	case "end_turn", "tool_use", "stop_sequence", "refusal":
	default:
		return model.Response{}, ErrUnsupported
	}
	if body.ID == "" {
		return model.Response{}, ErrUnsupported
	}
	result := model.Response{ResponseID: body.ID, Refused: body.StopReason == "refusal"}
	if body.Usage != nil && body.Usage.Input != nil && body.Usage.Output != nil {
		input, output := tokenCount(body.Usage.Input), tokenCount(body.Usage.Output)
		read, write := tokenCount(body.Usage.CacheRead), tokenCount(body.Usage.CacheWrite)
		if input < 0 || output < 0 || read < 0 || write < 0 ||
			input > math.MaxInt64-read || input+read > math.MaxInt64-write {
			return model.Response{}, ErrUnsupported
		}
		result.Usage = &model.Usage{
			InputTokens:  input + read + write,
			OutputTokens: output, CacheReadTokens: read, CacheWriteTokens: write,
		}
	}
	var output strings.Builder
	for _, raw := range body.Content {
		block, err := parseContentBlock(raw)
		if err != nil {
			return model.Response{}, err
		}
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
		case "thinking":
			if block.Signature == "" {
				return model.Response{}, ErrUnsupported
			}
		case "redacted_thinking":
			if block.Data == "" {
				return model.Response{}, ErrUnsupported
			}
		default:
			return model.Response{}, ErrUnsupported
		}
	}
	result.Content = output.String()
	if len(result.ToolCalls) != 0 {
		if body.StopReason != "tool_use" {
			return model.Response{}, ErrUnsupported
		}
		serializedBlocks, err := normalizedContentForReplay(body.Content)
		if err != nil {
			return model.Response{}, err
		}
		serialized, err := json.Marshal(serializedBlocks)
		if err != nil || len(serialized) > 16<<20 {
			return model.Response{}, ErrUnsupported
		}
		result.ProviderReplay = &model.ProviderReplay{Provider: "anthropic", Format: "messages.content.v1", Data: serialized}
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
	var initialUsagePresent bool
	var finalOutputUsageObserved bool
	err = providerhttp.Stream(resp, func(event string, data []byte) error {
		var frame struct {
			Type  string `json:"type"`
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
			Message      messageBody     `json:"message"`
			Index        int             `json:"index"`
			ContentBlock json.RawMessage `json:"content_block"`
			Delta        struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
			} `json:"delta"`
			Usage *usage `json:"usage"`
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
			initialUsagePresent = body.Usage != nil && body.Usage.Input != nil
			body.Content = nil
		case "content_block_start":
			if !started || blocks[frame.Index] != nil || frame.Index < 0 || frame.Index > 1024 {
				return providerhttp.ErrProtocol
			}
			b, err := parseContentBlock(frame.ContentBlock)
			if err != nil {
				return err
			}
			if b.Type != "text" && b.Type != "tool_use" && b.Type != "thinking" && b.Type != "redacted_thinking" {
				return ErrUnsupported
			}
			resultBytes += len(b.Text) + len(b.Input) + len(b.Thinking) + len(b.Signature) + len(b.Data)
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
			resultBytes += len(frame.Delta.Text) + len(frame.Delta.PartialJSON) + len(frame.Delta.Thinking) + len(frame.Delta.Signature)
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
			case "thinking_delta":
				if b.Type != "thinking" || b.Signature != "" {
					return providerhttp.ErrProtocol
				}
				b.Thinking += frame.Delta.Thinking
			case "signature_delta":
				if b.Type != "thinking" || b.Signature != "" || frame.Delta.Signature == "" {
					return providerhttp.ErrProtocol
				}
				b.Signature = frame.Delta.Signature
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
			if !started {
				return providerhttp.ErrProtocol
			}
			if frame.Delta.StopReason != "" {
				body.StopReason = frame.Delta.StopReason
			}
			if frame.Usage != nil && frame.Usage.Output != nil && initialUsagePresent {
				body.Usage.Output = frame.Usage.Output
				finalOutputUsageObserved = true
			}
		case "message_stop":
			if !started || len(blocks) != len(closed) {
				return providerhttp.ErrProtocol
			}
			stopped = true
		case "ping":
		case "error":
			return streamFailure(frame.Error.Type)
		default:
			// Anthropic explicitly permits adding new non-critical top-level
			// SSE event types. Never ignore unknown content blocks/deltas.
		}
		return nil
	})
	if err != nil {
		return model.Response{}, err
	}
	if !started || !stopped {
		return model.Response{}, providerhttp.ErrProtocol
	}
	// The initial SSE usage is not final billing evidence. If the final
	// cumulative output observation is missing, the Agent must not treat
	// the initial zero/partial usage as a settled token receipt.
	if !initialUsagePresent || !finalOutputUsageObserved {
		body.Usage = nil
	}
	for index := 0; index < len(blocks); index++ {
		b := blocks[index]
		if b == nil {
			return model.Response{}, providerhttp.ErrProtocol
		}
		encoded, err := marshalStreamBlock(b)
		if err != nil {
			return model.Response{}, providerhttp.ErrProtocol
		}
		body.Content = append(body.Content, encoded)
	}
	result, err := decodeResponse(body, prepared.names)
	if err == nil && emit != nil {
		if emitErr := emit(model.StreamEvent{ResponseID: result.ResponseID, Usage: model.CloneUsage(result.Usage)}); emitErr != nil {
			return model.Response{}, emitErr
		}
	}
	return result, err
}

// Error events can follow HTTP 200. Retry only documented transient types,
// without exposing the server-supplied message through errors or durable state.
func streamFailure(errorType string) error {
	switch errorType {
	case "api_error", "overloaded_error", "rate_limit_error", "timeout_error":
		return model.NewRetryableError(ErrUnsupported)
	default:
		return ErrUnsupported
	}
}

var _ model.Client = (*Client)(nil)
var _ model.StreamingClient = (*Client)(nil)
var _ model.ProviderNamer = (*Client)(nil)
