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

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const (
	endpoint         = "https://api.anthropic.com/v1/messages"
	apiVersion       = "2023-06-01"
	defaultMaxTokens = 4096
)

type Config struct {
	APIKey       string
	DefaultModel string
	Endpoint     string
	HTTPClient   *http.Client
	// BindingScope identifies the Host's custom transport/policy revision.
	// It is mandatory when HTTPClient is supplied and never persisted raw.
	BindingScope           string
	DefaultMaxOutputTokens int64
	// StructuredOutputModels opts explicitly verified private/gateway model IDs
	// into native strict JSON Schema output; unknown IDs otherwise fail closed.
	StructuredOutputModels []string
}
type Client struct {
	http                   *providerhttp.Client
	defaultModel           string
	maxTokens              int64
	bindingFingerprint     string
	structuredOutputModels map[string]bool
}

var ErrUnsupported = errors.New("unsupported Anthropic model request or response")

// ErrIncomplete distinguishes token-truncated output from a user refusal or
// a completed response that violates a frozen JSON output contract.
var ErrIncomplete = errors.New("anthropic response ended at max_tokens")

func New(config Config) (*Client, error) {
	if config.BindingScope != strings.TrimSpace(config.BindingScope) ||
		len(config.BindingScope) > 128 || config.HTTPClient != nil && config.BindingScope == "" {
		return nil, providerhttp.ErrConfig
	}
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
	actualEndpoint := config.Endpoint
	if actualEndpoint == "" {
		actualEndpoint = endpoint
	}
	fingerprint := providerhttp.FingerprintBinding(config.APIKey, providerhttp.BindingSpec{
		Protocol: "anthropic.messages." + apiVersion, Endpoint: actualEndpoint,
		DefaultModel: strings.TrimSpace(config.DefaultModel), BindingScope: config.BindingScope,
		MaxOutputTokens: tokens, StructuredOutputModels: config.StructuredOutputModels,
	})
	return &Client{http: h, defaultModel: strings.TrimSpace(config.DefaultModel),
		maxTokens: tokens, bindingFingerprint: fingerprint, structuredOutputModels: allowed}, nil
}
func (*Client) ProviderName() string                                { return "anthropic" }
func (c *Client) BindingFingerprintForRequest(model.Request) string { return c.bindingFingerprint }

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
		if err := model.ValidateMessage(m); err != nil {
			return prepared{}, errors.Join(ErrUnsupported, err)
		}
		switch m.Role {
		case model.RoleSystem:
			if seenNonSystem || m.ToolCallID != "" || len(m.ToolCalls) != 0 {
				return prepared{}, ErrUnsupported
			}
			text, textOnly := model.PlainText(m.Content)
			if !textOnly {
				return prepared{}, ErrUnsupported
			}
			systems = append(systems, text)
		case model.RoleUser:
			if m.ProviderReplay != nil {
				return prepared{}, ErrUnsupported
			}
			seenNonSystem = true
			if m.ToolCallID != "" || len(m.ToolCalls) != 0 {
				return prepared{}, ErrUnsupported
			}
			content, contentErr := inputContent(m.Content)
			if contentErr != nil {
				return prepared{}, contentErr
			}
			messages = append(messages, map[string]any{"role": "user", "content": content})
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
			text, textOnly := model.PlainText(m.Content)
			if !textOnly {
				return prepared{}, ErrUnsupported
			}
			if text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
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
			text, textOnly := model.PlainText(m.Content)
			if !textOnly {
				return prepared{}, ErrUnsupported
			}
			block := map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": text}
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

// completedMessageReceipt validates the final Anthropic completion envelope
// and normalizes cumulative Usage independently of Tool-block semantics.
func completedMessageReceipt(body messageBody) (model.Response, error) {
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
	return result, nil
}

func decodeResponse(body messageBody, names map[string]string, required bool) (model.Response, error) {
	result, err := completedMessageReceipt(body)
	if err != nil {
		return model.Response{}, err
	}
	// Keep the original completed receipt independent of progressively decoded
	// content. A malformed sibling must not erase billing for a physical Tool.
	receipt := result
	var output strings.Builder
	for _, raw := range body.Content {
		block, err := parseContentBlock(raw)
		if err != nil {
			return rejectCompletedToolOutput(receipt, body.Content, required, err)
		}
		switch block.Type {
		case "text":
			output.WriteString(block.Text)
		case "tool_use":
			key := names[block.Name]
			if key == "" || block.ID == "" || !json.Valid(block.Input) {
				return rejectCompletedToolOutput(receipt, body.Content, required, ErrUnsupported)
			}
			result.ToolCalls = append(result.ToolCalls, tools.Call{
				ID: block.ID, ToolKey: key, Name: block.Name, Arguments: append(json.RawMessage(nil), block.Input...),
			})
		case "thinking":
			if block.Signature == "" {
				return rejectCompletedToolOutput(receipt, body.Content, required, ErrUnsupported)
			}
		case "redacted_thinking":
			if block.Data == "" {
				return rejectCompletedToolOutput(receipt, body.Content, required, ErrUnsupported)
			}
		default:
			return rejectCompletedToolOutput(receipt, body.Content, required, ErrUnsupported)
		}
	}
	result.Content = output.String()
	if len(result.ToolCalls) != 0 {
		if body.StopReason != "tool_use" {
			return providerhttp.RejectCompletedResponse(result, ErrUnsupported)
		}
		serializedBlocks, err := normalizedContentForReplay(body.Content)
		if err != nil {
			return providerhttp.RejectCompletedResponse(receipt, err)
		}
		serialized, err := json.Marshal(serializedBlocks)
		if err != nil || len(serialized) > 16<<20 {
			return providerhttp.RejectCompletedResponse(receipt, ErrUnsupported)
		}
		result.ProviderReplay = &model.ProviderReplay{Provider: "anthropic", Format: "messages.content.v1", Data: serialized}
	}
	if result.Content == "" && len(result.ToolCalls) == 0 {
		if required {
			return providerhttp.RejectCompletedResponse(receipt, ErrUnsupported)
		}
		return model.Response{}, ErrUnsupported
	}
	return result, nil
}

// A completed Anthropic message can contain a valid physical tool_use *after*
// an invalid sibling block. Check the entire original output, not just the
// first failing block or the partially decoded ToolCalls. Only the minimal
// verified completion receipt survives; no partial Tool, text or signed replay
// becomes authorized on any post-completion semantic rejection.
func rejectCompletedToolOutput(receipt model.Response, content []json.RawMessage, required bool, cause error) (model.Response, error) {
	// A completed mandatory-Tool reply with unsupported non-Tool content
	// must still be settled if its final cumulative Usage is trustworthy.
	if required {
		return providerhttp.RejectCompletedResponse(receipt, cause)
	}
	for _, raw := range content {
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &kind) == nil && kind.Type == "tool_use" {
			return providerhttp.RejectCompletedResponse(receipt, cause)
		}
	}
	return model.Response{}, cause
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
	result, err := decodeResponse(body, prepared.names, request.RequireToolCall)
	if err != nil {
		return result, err
	}
	return verifyRequiredToolResponse(request, result)
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
	// Content blocks precede final message deltas. Keep an explicit active
	// block, so late/interleaved Tool events cannot be reconstructed as a
	// legitimate executable response after the final stop boundary.
	activeBlock := -1
	var finalDeltaSeen, finalStopReasonSeen bool
	var blocks = map[int]*contentBlock{}
	var chunks = map[int]*strings.Builder{}
	var closed = map[int]bool{}
	var resultBytes int
	// Unsupported content is never authorized. Drain only a bounded,
	// well-framed stream to establish final completion and paid Usage.
	var rejectedContent bool
	var initialUsagePresent bool
	var finalOutputUsageObserved bool
	err = providerhttp.Stream(resp, func(event string, data []byte) error {
		var frame struct {
			Type  string `json:"type"`
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
			Message      messageBody     `json:"message"`
			Index        *int            `json:"index"`
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
		// An absent or null index must not silently become block 0.
		index := -1
		if frame.Index != nil {
			index = *frame.Index
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
			if !started || finalDeltaSeen || activeBlock != -1 ||
				index != len(blocks) || index > 1024 {
				return providerhttp.ErrProtocol
			}
			b, err := parseContentBlock(frame.ContentBlock)
			if err != nil {
				// A structurally valid content block with unsupported rich
				// citations/metadata is a semantic rejection, not yet proof
				// of an incomplete physical Provider response. Keep only its
				// type for Tool evidence, never its untrusted payload.
				var kind struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(frame.ContentBlock, &kind) != nil || kind.Type == "" {
					return providerhttp.ErrProtocol
				}
				rejectedContent = true
				b = contentBlock{Type: kind.Type}
				resultBytes += len(frame.ContentBlock)
			} else {
				resultBytes += len(b.Text) + len(b.Input) + len(b.Thinking) + len(b.Signature) + len(b.Data)
			}
			if resultBytes > 16<<20 {
				return providerhttp.ErrProtocol
			}
			blocks[index] = &b
			chunks[index] = &strings.Builder{}
			activeBlock = index
		case "content_block_delta":
			b := blocks[index]
			if !started || finalDeltaSeen || activeBlock != index ||
				b == nil || closed[index] || frame.Delta.Type == "" {
				return providerhttp.ErrProtocol
			}
			if rejectedContent {
				// After irreversible semantic rejection, later deltas are
				// framing evidence only. Bound raw bytes; never emit content.
				resultBytes += len(data)
				if resultBytes > 16<<20 {
					return providerhttp.ErrProtocol
				}
				break
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
				if _, err := chunks[index].WriteString(frame.Delta.PartialJSON); err != nil {
					return providerhttp.ErrProtocol
				}
			default:
				return ErrUnsupported
			}
		case "content_block_stop":
			b := blocks[index]
			if !started || finalDeltaSeen || activeBlock != index ||
				b == nil || closed[index] {
				return providerhttp.ErrProtocol
			}
			closed[index] = true
			activeBlock = -1
			if b.Type == "tool_use" && chunks[index].Len() > 0 {
				b.Input = json.RawMessage(chunks[index].String())
			}
		case "message_delta":
			if !started || activeBlock != -1 {
				return providerhttp.ErrProtocol
			}
			// Claude may send multiple message_delta events with cumulative
			// Usage. Never accept a conflicting final stop reason or declining
			// token count; both would undermine the final Tool/Usage proof.
			finalDeltaSeen = true
			if frame.Delta.StopReason != "" {
				if body.StopReason != "" && body.StopReason != frame.Delta.StopReason {
					return providerhttp.ErrProtocol
				}
				body.StopReason = frame.Delta.StopReason
				finalStopReasonSeen = true
			}
			if frame.Usage != nil && frame.Usage.Output != nil && initialUsagePresent {
				if body.Usage.Output != nil && *frame.Usage.Output < *body.Usage.Output {
					return providerhttp.ErrProtocol
				}
				body.Usage.Output = frame.Usage.Output
				finalOutputUsageObserved = true
			}
		case "message_stop":
			if !started || !finalDeltaSeen || !finalStopReasonSeen || activeBlock != -1 ||
				len(blocks) != len(closed) {
				return providerhttp.ErrProtocol
			}
			// Completion proof requires a contiguous sequence of closed
			// blocks. Matching map lengths alone can conceal index gaps,
			// which must not certify even a minimal rejected receipt.
			for index := 0; index < len(blocks); index++ {
				if blocks[index] == nil || !closed[index] {
					return providerhttp.ErrProtocol
				}
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
	if rejectedContent {
		// Only after message_stop: never reconstruct or trust rejected
		// content, even if another block contains a legitimate local Tool.
		return rejectCompletedStreamToolOutput(body, blocks, request.RequireToolCall, ErrUnsupported)
	}
	for index := 0; index < len(blocks); index++ {
		b := blocks[index]
		if b == nil {
			return model.Response{}, providerhttp.ErrProtocol
		}
		encoded, err := marshalStreamBlock(b)
		if err != nil {
			return rejectCompletedStreamToolOutput(body, blocks, request.RequireToolCall, providerhttp.ErrProtocol)
		}
		body.Content = append(body.Content, encoded)
	}
	result, err := decodeResponse(body, prepared.names, request.RequireToolCall)
	if err != nil {
		return result, err
	}
	result, err = verifyRequiredToolResponse(request, result)
	if err != nil {
		// Return trusted completion/usage for Agent's durable rejected
		// receipt, without signaling a successful terminal SSE event.
		return result, err
	}
	if emit != nil {
		if emitErr := emit(model.StreamEvent{ResponseID: result.ResponseID, Usage: model.CloneUsage(result.Usage)}); emitErr != nil {
			return model.Response{}, emitErr
		}
	}
	return result, err
}

// Stream block reconstruction runs only after message_stop and final cumulative
// Usage checks. All observed blocks are available, so an invalid sibling
// before a valid Tool cannot hide a paid physical Tool response. Earlier
// streaming/protocol failures do not use this completed-response receipt path.
func rejectCompletedStreamToolOutput(body messageBody, blocks map[int]*contentBlock, required bool, noToolCause error) (model.Response, error) {
	// The entire SSE framing and message_stop have already been verified.
	// Missing a mandatory Tool is itself a semantic rejection, even when
	// the only observed blocks were unrepresentable text.
	hasTool := required
	for _, block := range blocks {
		if block != nil && block.Type == "tool_use" {
			hasTool = true
		}
	}
	if !hasTool {
		return model.Response{}, noToolCause
	}
	receipt, err := completedMessageReceipt(body)
	if err != nil {
		return model.Response{}, providerhttp.ErrProtocol
	}
	return providerhttp.RejectCompletedResponse(receipt, ErrUnsupported)
}

// A gateway may ignore tool_choice=any and return text instead of a required
// Tool call. Known cumulative Usage proves a completed but locally rejected
// response, which Agent must account once without retrying a billed request.
// An unknown/invalid Usage cannot be promoted to a completed receipt.
func verifyRequiredToolResponse(request model.Request, result model.Response) (model.Response, error) {
	if !request.RequireToolCall || len(result.ToolCalls) > 0 || result.Refused {
		return result, nil
	}
	if !providerhttp.ValidCompletedUsage(result.Usage) {
		return model.Response{}, ErrUnsupported
	}
	// Tool-free rejected text is not an authorized transcript item.
	return providerhttp.RejectCompletedResponse(result, ErrUnsupported)
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
var _ model.RequestBindingFingerprinter = (*Client)(nil)
var _ model.ProviderNamer = (*Client)(nil)
