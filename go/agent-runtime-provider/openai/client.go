// Package openai implements model.Client and model.StreamingClient via
// OpenAI Responses API. No provider types escape this adapter.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const endpoint = "https://api.openai.com/v1/responses"

type Config struct {
	APIKey       string
	DefaultModel string
	Endpoint     string
	HTTPClient   *http.Client
	// BindingScope identifies the Host's custom transport/policy revision.
	// It is mandatory when HTTPClient is supplied and never persisted raw.
	BindingScope string
	// StructuredOutputModels opts explicitly named models/gateways into strict
	// JSON Schema output. Unknown models otherwise fail closed.
	StructuredOutputModels []string
}
type Client struct {
	http                   *providerhttp.Client
	defaultModel           string
	bindingFingerprint     string
	structuredOutputModels map[string]bool
}

func New(config Config) (*Client, error) {
	if config.BindingScope != strings.TrimSpace(config.BindingScope) ||
		len(config.BindingScope) > 128 || config.HTTPClient != nil && config.BindingScope == "" {
		return nil, providerhttp.ErrConfig
	}
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
	actualEndpoint := config.Endpoint
	if actualEndpoint == "" {
		actualEndpoint = endpoint
	}
	fingerprint := providerhttp.FingerprintBinding(config.APIKey, providerhttp.BindingSpec{
		Protocol: "openai.responses.v1", Endpoint: actualEndpoint,
		DefaultModel: strings.TrimSpace(config.DefaultModel), BindingScope: config.BindingScope,
		StructuredOutputModels: config.StructuredOutputModels,
	})
	return &Client{http: httpClient, defaultModel: strings.TrimSpace(config.DefaultModel),
		bindingFingerprint: fingerprint, structuredOutputModels: allowed}, nil
}
func (*Client) ProviderName() string                                { return "openai" }
func (c *Client) BindingFingerprintForRequest(model.Request) string { return c.bindingFingerprint }

type outputItem struct {
	Type    string  `json:"type"`
	Role    string  `json:"role"`
	Phase   *string `json:"phase"`
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
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	Status            string            `json:"status"`
	Error             json.RawMessage   `json:"error"`
	IncompleteDetails json.RawMessage   `json:"incomplete_details"`
	Output            []json.RawMessage `json:"output"`
	Usage             *struct {
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
		if err := model.ValidateMessage(m); err != nil {
			return prepared{}, errors.Join(ErrUnsupported, err)
		}
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
			text, textOnly := model.PlainText(m.Content)
			if m.Role == model.RoleSystem {
				if !textOnly {
					return prepared{}, ErrUnsupported
				}
				input = append(input, map[string]any{"role": "system", "content": text})
				continue
			}
			content, contentErr := inputContent(m.Content)
			if contentErr != nil {
				return prepared{}, contentErr
			}
			input = append(input, map[string]any{"role": "user", "content": content})
		case model.RoleAssistant:
			if m.ToolCallID != "" {
				return prepared{}, ErrUnsupported
			}
			text, textOnly := model.PlainText(m.Content)
			if !textOnly {
				return prepared{}, ErrUnsupported
			}
			if text != "" {
				input = append(input, map[string]any{"role": "assistant", "content": text})
			}
			for _, call := range m.ToolCalls {
				toolName := call.Name
				if toolName == "" {
					toolName = byKey[call.ToolKey]
				}
				if call.ID == "" || toolName == "" || providerhttp.ValidateUniqueJSONObject(call.Arguments) != nil {
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
			text, textOnly := model.PlainText(m.Content)
			if !textOnly {
				return prepared{}, ErrUnsupported
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": text})
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
func decodeResponse(body responseBody, names map[string]string, required bool) (model.Response, error) {
	// An outer object from a different API cannot authorize any local
	// function_call, even if it copied the Responses output/usage shape.
	if body.Object != "response" {
		return model.Response{}, ErrUnsupported
	}
	if body.Status == "incomplete" {
		return model.Response{}, errors.Join(ErrUnsupported, ErrIncomplete)
	}
	// A physically completed Responses envelope cannot also carry error
	// or truncation evidence. Do not authorize local Tools or manufacture
	// completed billing from contradictory gateway terminal fields. Absent
	// and explicit-null fields remain compatible with valid Responses.
	if len(body.Error) != 0 && !bytes.Equal(bytes.TrimSpace(body.Error), []byte("null")) ||
		len(body.IncompleteDetails) != 0 && !bytes.Equal(bytes.TrimSpace(body.IncompleteDetails), []byte("null")) {
		return model.Response{}, ErrUnsupported
	}
	// Agent normalizes ProviderResponseID for durable receipts. A padded
	// physical response identity cannot be used to authorize local Tools:
	// its recorded identifier would differ from the one the Provider sent.
	if body.Status != "completed" || body.ID == "" || strings.TrimSpace(body.ID) != body.ID {
		return model.Response{}, ErrUnsupported
	}
	result := model.Response{ResponseID: body.ID}
	if body.Usage != nil && body.Usage.Input != nil && body.Usage.Output != nil {
		result.Usage = &model.Usage{
			InputTokens: *body.Usage.Input, OutputTokens: *body.Usage.Output,
			CacheReadTokens: body.Usage.InputDetails.Cached, ReasoningTokens: body.Usage.OutputDetails.Reasoning,
		}
	}
	// Keep the physical completion receipt separate from decoded content:
	// any later rejection must not leak valid Tool output or discard paid Usage.
	receipt := result
	// A completed outer response can still contain an unfinished function
	// item with partially formed arguments. Fence that item before decoding
	// executable calls so known final Usage is not lost to an argument parse
	// error and no incomplete Tool can enter an Agent dispatch intent.
	result, err := verifyCompletedFunctionCallItems(result, body.Output, names)
	if err != nil {
		if model.IsResponseRejectedError(err) {
			return result, err
		}
		return rejectCompletedToolOutput(receipt, body.Output, required, err)
	}
	projection, err := projectOutput(body.Output)
	if err != nil {
		return rejectCompletedToolOutput(receipt, body.Output, required, err)
	}
	result.Content, result.Refused = projection.content, projection.refused
	for _, item := range projection.calls {
		key := names[item.Name]
		if key == "" {
			return rejectCompletedToolOutput(receipt, body.Output, required, ErrUnsupported)
		}
		result.ToolCalls = append(result.ToolCalls, tools.Call{
			ID: item.CallID, ToolKey: key, Name: item.Name, Arguments: json.RawMessage(item.Arguments),
		})
	}
	if projection.phased || projection.reasoning && len(result.ToolCalls) != 0 {
		outputs := body.Output
		if projection.phased && len(result.ToolCalls) == 0 {
			// Preserve real message phases on later turns. As before, completed
			// text-only responses do not replay unsigned reasoning evidence.
			outputs = projection.messages
		}
		replay, err := encodeReplay(outputs)
		if err != nil {
			return rejectCompletedToolOutput(receipt, body.Output, required, err)
		}
		result.ProviderReplay = replay
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
	result, err := decodeResponse(body, prepared.names, request.RequireToolCall)
	if err != nil {
		return result, err
	}
	return verifyRequiredToolResponse(request, result, body.Output)
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
	var completedItems completedOutputItems
	var completedArguments completedToolArguments
	var completedContent completedMessageContent
	err = providerhttp.Stream(resp, func(event string, data []byte) error {
		if string(data) == "[DONE]" && final != nil {
			return nil
		}
		if final != nil {
			return providerhttp.ErrProtocol
		}
		// Never decode ambiguous SSE authority fields with last-key-wins
		// semantics, even when the final response appears valid.
		if err := providerhttp.ValidateUniqueJSONKeys(data); err != nil {
			return err
		}
		var frame struct {
			Type         string          `json:"type"`
			Code         string          `json:"code"`
			Delta        string          `json:"delta"`
			ItemID       string          `json:"item_id"`
			Arguments    *string         `json:"arguments"`
			ContentIndex *int            `json:"content_index"`
			Refusal      *string         `json:"refusal"`
			Text         *string         `json:"text"`
			Part         json.RawMessage `json:"part"`
			OutputIndex  *int            `json:"output_index"`
			Item         json.RawMessage `json:"item"`
			Response     json.RawMessage `json:"response"`
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
		case "response.output_text.done":
			if err := completedContent.observeText(frame.OutputIndex, frame.ContentIndex, frame.ItemID, frame.Text); err != nil {
				return err
			}
		case "response.refusal.done":
			if err := completedContent.observeRefusal(frame.OutputIndex, frame.ContentIndex, frame.ItemID, frame.Refusal); err != nil {
				return err
			}
		case "response.content_part.done":
			if err := completedContent.observePart(frame.OutputIndex, frame.ContentIndex, frame.ItemID, frame.Part); err != nil {
				return err
			}
		case "response.function_call_arguments.done":
			if err := completedArguments.observe(frame.OutputIndex, frame.ItemID, frame.Arguments); err != nil {
				return err
			}
		case "response.output_item.done":
			if err := completedItems.observe(frame.OutputIndex, frame.Item); err != nil {
				return err
			}
		case "response.completed":
			var result responseBody
			if final != nil || json.Unmarshal(frame.Response, &result) != nil {
				return providerhttp.ErrProtocol
			}
			if err := completedContent.reconcile(&result); err != nil {
				return err
			}
			if err := completedArguments.reconcile(&result); err != nil {
				return err
			}
			if err := completedItems.reconcile(&result); err != nil {
				return err
			}
			final = &result
		case "response.incomplete":
			return errors.Join(ErrUnsupported, ErrIncomplete)
		case "response.failed":
			var failed struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(frame.Response, &failed) != nil {
				return providerhttp.ErrProtocol
			}
			return streamFailure(failed.Error.Code)
		case "error":
			return streamFailure(frame.Code)
		}
		return nil
	})
	if err != nil {
		return model.Response{}, err
	}
	if final == nil {
		return model.Response{}, providerhttp.ErrProtocol
	}
	result, err := decodeResponse(*final, prepared.names, request.RequireToolCall)
	if err != nil {
		// An unfinished Tool item can carry a completed rejection receipt,
		// but never executable Calls or a successful terminal SSE event.
		return result, err
	}
	result, err = verifyRequiredToolResponse(request, result, final.Output)
	if err != nil {
		// Completed but rejected responses still carry their trusted Usage
		// to Agent for one durable receipt; never emit SSE terminal success.
		return result, err
	}
	if emit != nil {
		if emitErr := emit(model.StreamEvent{ResponseID: result.ResponseID, Usage: model.CloneUsage(result.Usage)}); emitErr != nil {
			return model.Response{}, emitErr
		}
	}
	return result, err
}

// rejectCompletedToolOutput settles post-completion semantic/encoding
// errors involving a physical Tool or a missing mandatory Tool. The output
// remains unauthorized; a known completed, billable response must be consumed
// exactly once. Optional text-only failures and unavailable encrypted replay
// retain their original Provider error classification.
func rejectCompletedToolOutput(receipt model.Response, output []json.RawMessage, required bool, cause error) (model.Response, error) {
	if errors.Is(cause, ErrReplayUnavailable) {
		return model.Response{}, cause
	}
	// The mandatory Tool obligation is also violated when an already
	// completed response contains only unprojectable non-Tool content.
	// Preserve its verified billing without authorizing any payload.
	if required {
		return providerhttp.RejectCompletedResponse(receipt, cause)
	}
	for _, raw := range output {
		var item struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &item) == nil && item.Type == "function_call" {
			return providerhttp.RejectCompletedResponse(receipt, cause)
		}
	}
	return model.Response{}, cause
}

// verifyCompletedFunctionCallItems fences physical Tool execution even when
// the caller did not require a Tool. A completed response envelope is not
// proof that every function_call item was completed: downstream Agent code
// would otherwise queue and execute an explicitly unfinished Tool.
// Preserve only verifiable billable completion evidence, never an executable
// ToolCall, its arguments, unrelated text or replay payload on rejection.
func verifyCompletedFunctionCallItems(result model.Response, output []json.RawMessage, names map[string]string) (model.Response, error) {
	for _, raw := range output {
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &kind) != nil {
			return model.Response{}, ErrUnsupported
		}
		if kind.Type != "function_call" {
			continue
		}
		var item struct {
			Status    json.RawMessage `json:"status"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Arguments string          `json:"arguments"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return providerhttp.RejectCompletedResponse(result, ErrUnsupported)
		}
		// Only omitted status preserves the legacy contract. Every explicit
		// status must be completed, and even a completed item still needs
		// valid executable arguments and a declared local Tool identity.
		if len(item.Status) != 0 {
			var status string
			if json.Unmarshal(item.Status, &status) != nil || status != "completed" {
				return providerhttp.RejectCompletedResponse(result, ErrUnsupported)
			}
		}
		// Agent trims Call IDs and generates a replacement when trimming
		// produces an empty string. That normalization must never turn an
		// unverified physical provider ID into an executable local Tool.
		if item.CallID == "" || strings.TrimSpace(item.CallID) != item.CallID ||
			item.Name == "" || names[item.Name] == "" ||
			providerhttp.ValidateUniqueJSONObject([]byte(item.Arguments)) != nil {
			return providerhttp.RejectCompletedResponse(result, ErrUnsupported)
		}
	}
	return result, nil
}

// A provider's tool_choice=required hint is not proof that a gateway actually
// returned a Tool call. Preserve genuine completed response identity and
// cumulative Usage so Agent can consume a post-generation contract rejection
// exactly once. Missing/invalid Usage cannot mint a known-completion receipt.
func verifyRequiredToolResponse(request model.Request, result model.Response, output []json.RawMessage) (model.Response, error) {
	if !request.RequireToolCall || len(result.ToolCalls) > 0 {
		return result, nil
	}
	// A refusal in earlier commentary remains part of the global safety
	// observation, but cannot authorize a different ordinary final_answer
	// to bypass a mandatory Tool call. Prove the selected terminal message.
	if result.Refused && terminalSafetyRefusal(output) {
		return result, nil
	}
	if !providerhttp.ValidCompletedUsage(result.Usage) {
		return model.Response{}, ErrUnsupported
	}
	// A rejected required-Tool response is not an assistant Message.
	// Persist only its trusted physical receipt, never projected text,
	// a global refusal flag or phase replay.
	return providerhttp.RejectCompletedResponse(result, ErrUnsupported)
}

// Only documented transient codes opt into durable retries. Never retain the
// upstream message: it may include prompts, credentials or provider internals.
func streamFailure(code string) error {
	switch code {
	case "server_error", "rate_limit_exceeded", "slow_down", "server_is_overloaded":
		return model.NewRetryableError(ErrUnsupported)
	default:
		return ErrUnsupported
	}
}

var _ model.Client = (*Client)(nil)
var _ model.StreamingClient = (*Client)(nil)
var _ model.RequestBindingFingerprinter = (*Client)(nil)
var _ model.ProviderNamer = (*Client)(nil)
