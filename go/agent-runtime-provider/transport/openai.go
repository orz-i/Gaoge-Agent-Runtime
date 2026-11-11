package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	valueType945D3026 = "type"
)

const (
	valueTextA2BECF6A        = "24h"
	valueDONEF318DF9A        = "[DONE]"
	valueDescription05130FFF = "description"
	valueFunction4B89C155    = "function"
	valueInMemoryD0D36D1B    = "in-memory"
	valueName9213B529        = "name"
	valueParametersF49C8F89  = "parameters"
)

const (
	openRouterDefaultAttributionReferer = "https://gaoge.example.com"
	openRouterDefaultAttributionTitle   = "Gaoge"
	openRouterDefaultCategories         = "general-chat"
)

// generateOpenAICompatible 调用 OpenAI 兼容接口并解析响应（非流式）。
// 超时策略：ReadTimeoutMS 控制整体超时（含 LLM 推理等待）。
func (c *Client) generateOpenAICompatible(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	endpoint := normalizeEndpoint(route.Endpoint)
	requestURL := buildOpenAIRequestURL(route.BaseURL, endpoint)
	if requestURL == "" {
		return nil, errCategory8FDD2C1D16
	}

	requestBody, err := buildOpenAIRequestBody(route.Protocol, route.UpstreamModel, endpoint, input, false)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	setBearerAuthHeader(req, route.APIKey)
	setOpenRouterAttributionHeaders(req, route)
	setAdditionalHeaders(req, route.HeadersJSON)

	resp, err := c.httpClientForRoute(route).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readUpstreamBody(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseUpstreamError(resp.StatusCode, body, upstreamDebugSnapshot(req, payload, resp, body))
	}

	debug := upstreamDebugSnapshot(req, payload, resp, body)
	output, err := parseOpenAIGenerateOutput(endpoint, route.Protocol, body, deepSeekTextEncodedToolCallsEnabled(route))
	if err != nil {
		return nil, attachUpstreamDebug(err, debug)
	}
	output.Debug = debug
	return output, nil
}

// generateStreamOpenAICompatible 调用上游流式推理接口并实时回传增量文本。
// 超时策略：
//   - ReadTimeoutMS  控制 TCP 建连 + 等待首字节（含 LLM 推理排队）
//   - StreamIdleTimeoutMS 控制流传输中两个 chunk 之间的最大间隔（防假死）
func (c *Client) generateStreamOpenAICompatible(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	endpoint := normalizeEndpoint(route.Endpoint)
	requestURL := buildOpenAIRequestURL(route.BaseURL, endpoint)
	if requestURL == "" {
		return nil, errCategory8FDD2C1D16
	}

	requestBody, err := buildOpenAIRequestBody(route.Protocol, route.UpstreamModel, endpoint, input, true)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	// 用父 ctx 发起请求，不绑定超时 context，避免 cancel 关闭底层连接。
	// 首字节超时通过独立 timer + 取消专用 context 实现。
	firstByteCtx, firstByteCancel := context.WithCancel(ctx)
	defer firstByteCancel()

	readTimeout := resolveReadTimeout(route.ReadTimeoutMS)
	firstByteTimer := time.AfterFunc(readTimeout, func() {
		firstByteCancel()
	})

	req, err := http.NewRequestWithContext(firstByteCtx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		firstByteTimer.Stop()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	setBearerAuthHeader(req, route.APIKey)
	setOpenRouterAttributionHeaders(req, route)
	setAdditionalHeaders(req, route.HeadersJSON)

	resp, err := c.httpClientForRoute(route).Do(req)
	firstByteTimer.Stop()
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := readUpstreamBody(resp.Body)
		if readErr != nil {
			return nil, readErr
		}
		return nil, parseUpstreamError(resp.StatusCode, body, upstreamDebugSnapshot(req, payload, resp, body))
	}

	result := &GenerateOutput{
		ResponseID:      "",
		Text:            "",
		Usage:           Usage{},
		ToolCalls:       make([]ToolCall, 0),
		ServerToolCalls: make([]ToolCall, 0),
		RawJSON:         "",
	}

	idleTimeout := resolveStreamIdleTimeout(route.StreamIdleTimeoutMS)
	idleReader := newIdleTimeoutReader(resp.Body, idleTimeout)
	streamBody := newUpstreamBodyRecorder(idleReader)
	if err = consumeOpenAIGenerateStream(endpoint, route.Protocol, streamBody, result, onEvent, deepSeekTextEncodedToolCallsEnabled(route)); err != nil {
		return nil, attachUpstreamDebug(err, upstreamDebugSnapshot(req, payload, resp, streamErrorBody(streamBody, err)))
	}
	return result, nil
}

// listModelsOpenAICompatible 调用上游 models 目录接口。
func (c *Client) listModelsOpenAICompatible(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	requestURL := buildOpenAIModelsURL(route.BaseURL)
	if requestURL == "" {
		return nil, errCategory8FDD2C1D16
	}

	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	setBearerAuthHeader(req, route.APIKey)
	setOpenRouterAttributionHeaders(req, route)
	setAdditionalHeaders(req, route.HeadersJSON)

	resp, err := c.httpClientForRoute(route).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readUpstreamBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseUpstreamError(resp.StatusCode, body, upstreamDebugSnapshot(req, nil, resp, body))
	}

	return parseOpenAIModelList(body)
}

func buildOpenAIRequestBody(protocol string, model string, endpoint string, input GenerateInput, stream bool) (map[string]interface{}, error) {
	endpoint = normalizeEndpoint(endpoint)
	messages := normalizeMessages(input.Messages)
	adapter := NormalizeAdapter(protocol)
	providerTools, toolDefinitions, toolsEnabled, err := toolDeclarationsForInput(input)
	if err != nil {
		return nil, err
	}
	providerStreamOptions, err := providerStreamOptionsFromOptions(input.Options)
	if err != nil {
		return nil, err
	}

	switch endpoint {
	case EndpointChatCompletions:
		return buildChatCompletionsRequestBody(adapter, model, input, messages, providerTools, toolDefinitions, providerStreamOptions, stream), nil
	default:
		return buildResponsesRequestBody(adapter, model, input, messages, providerTools, toolDefinitions, toolsEnabled, providerStreamOptions, stream), nil
	}
}

func buildOpenAITools(tools []ToolDefinition, chatCompletions bool) []map[string]interface{} {
	if len(tools) == 0 {
		return nil
	}
	items := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		schema := decodeToolSchema(tool.InputSchema)
		if chatCompletions {
			items = append(items, map[string]interface{}{
				valueType945D3026: valueFunction4B89C155,
				valueFunction4B89C155: map[string]interface{}{
					valueName9213B529:        name,
					valueDescription05130FFF: strings.TrimSpace(tool.Description),
					valueParametersF49C8F89:  schema,
				},
			})
			continue
		}
		items = append(items, map[string]interface{}{
			valueType945D3026:        valueFunction4B89C155,
			valueName9213B529:        name,
			valueDescription05130FFF: strings.TrimSpace(tool.Description),
			valueParametersF49C8F89:  schema,
		})
	}
	return items
}

func applyOpenAICompatibleSamplingParams(payload map[string]interface{}, params map[string]interface{}, chatCompletions bool) {
	if value, ok := modelParamFloat(params, "temperature"); ok {
		payload["temperature"] = value
	}
	if value, ok := modelParamFloat(params, "top_p"); ok {
		payload["top_p"] = value
	}
	if chatCompletions {
		applyOpenAIChatSamplingParams(payload, params)
		return
	}
	if format, ok := normalizedJSONResponseFormat(params); ok {
		setOpenAIResponseTextParam(payload, "format", format)
	}
}

func applyOpenAIChatSamplingParams(payload map[string]interface{}, params map[string]interface{}) {
	applyOpenAIChatPenaltyParams(payload, params)
	applyOpenAIChatSeedParam(payload, params)
	applyOpenAIChatStopParam(payload, params)
	if format, ok := normalizedChatCompletionResponseFormat(params); ok {
		payload["response_format"] = format
	}
}

func applyOpenAIChatPenaltyParams(payload map[string]interface{}, params map[string]interface{}) {
	if value, ok := modelParamFloat(params, "frequency_penalty"); ok {
		payload["frequency_penalty"] = value
	}
	if value, ok := modelParamFloat(params, "presence_penalty"); ok {
		payload["presence_penalty"] = value
	}
}

func applyOpenAIChatSeedParam(payload map[string]interface{}, params map[string]interface{}) {
	if seed := modelParamInt(params, "seed"); seed > 0 {
		payload["seed"] = seed
	}
}

func applyOpenAIChatStopParam(payload map[string]interface{}, params map[string]interface{}) {
	if stops := modelParamStringList(params, "stop"); len(stops) == 1 {
		payload["stop"] = stops[0]
	} else if len(stops) > 1 {
		payload["stop"] = stops
	} else if raw, ok := params["stop"]; ok && raw == nil {
		payload["stop"] = nil
	}
}

func setOpenAIResponseTextParam(payload map[string]interface{}, key string, value interface{}) {
	text, _ := payload["text"].(map[string]interface{})
	if text == nil {
		text = map[string]interface{}{}
		payload["text"] = text
	}
	text[key] = value
}

func normalizePromptCacheRetention(value string) string {
	switch strings.TrimSpace(value) {
	case valueInMemoryD0D36D1B:
		return valueInMemoryD0D36D1B
	case "in_memory":
		return valueInMemoryD0D36D1B
	case valueTextA2BECF6A:
		return valueTextA2BECF6A
	default:
		return ""
	}
}

func buildOpenAIRequestURL(baseURL string, endpoint string) string {
	switch endpoint {
	case EndpointChatCompletions:
		return buildVersionedEndpointURL(baseURL, "v1", "/chat/completions")
	case EndpointImageGenerations:
		return buildVersionedEndpointURL(baseURL, "v1", "/images/generations")
	case EndpointImageEdits:
		return buildVersionedEndpointURL(baseURL, "v1", "/images/edits")
	default:
		return buildVersionedEndpointURL(baseURL, "v1", "/responses")
	}
}

func buildOpenAIModelsURL(baseURL string) string {
	return buildVersionedEndpointURL(baseURL, "v1", "/models")
}

func setOpenRouterAttributionHeaders(req *http.Request, route RouteConfig) {
	if req == nil || !isOpenRouterBaseURL(route.BaseURL) {
		return
	}
	setOpenRouterRefererHeader(req, route)
	setOpenRouterTitleHeaders(req, route)
	setOpenRouterCategoriesHeader(req, route)
}

func setOpenRouterRefererHeader(req *http.Request, route RouteConfig) {
	if req.Header.Get("HTTP-Referer") == "" && !hasAdditionalHeader(route.HeadersJSON, "HTTP-Referer") {
		referer := strings.TrimRight(strings.TrimSpace(route.AttributionReferer), "/")
		if referer == "" {
			referer = openRouterDefaultAttributionReferer
		}
		req.Header.Set("HTTP-Referer", referer)
	}
}

func setOpenRouterTitleHeaders(req *http.Request, route RouteConfig) {
	if !hasAdditionalHeader(route.HeadersJSON, "X-Title", "X-OpenRouter-Title") &&
		(req.Header.Get("X-Title") == "" || req.Header.Get("X-OpenRouter-Title") == "") {
		title := strings.TrimSpace(route.AttributionTitle)
		if title == "" {
			title = openRouterDefaultAttributionTitle
		}
		if req.Header.Get("X-Title") == "" {
			req.Header.Set("X-Title", title)
		}
		if req.Header.Get("X-OpenRouter-Title") == "" {
			req.Header.Set("X-OpenRouter-Title", title)
		}
	}
}

func setOpenRouterCategoriesHeader(req *http.Request, route RouteConfig) {
	if req.Header.Get("X-OpenRouter-Categories") == "" && !hasAdditionalHeader(route.HeadersJSON, "X-OpenRouter-Categories") {
		req.Header.Set("X-OpenRouter-Categories", openRouterDefaultCategories)
	}
}

func setBearerAuthHeader(req *http.Request, apiKey string) {
	if value := strings.TrimSpace(apiKey); value != "" {
		req.Header.Set("Authorization", "Bearer "+value)
	}
}

func hasAdditionalHeader(headersJSON string, names ...string) bool {
	value := strings.TrimSpace(headersJSON)
	if value == "" {
		return false
	}
	parsed := make(map[string]interface{})
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return false
	}
	for key := range parsed {
		headerKey := strings.TrimSpace(key)
		for _, name := range names {
			if strings.EqualFold(headerKey, name) {
				return true
			}
		}
	}
	return false
}

func isOpenRouterBaseURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	return host == "openrouter.ai" || strings.HasSuffix(host, ".openrouter.ai")
}

func consumeOpenAIGenerateStream(
	endpoint string,
	adapter string,
	reader io.Reader,
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
	allowTextEncodedToolCalls bool,
) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	parser := newOpenAIGenerateSSEParser(endpoint, adapter, result, onEvent, allowTextEncodedToolCalls)

	for scanner.Scan() {
		if err := parser.consumeLine(scanner.Text()); err != nil {
			if errors.Is(err, errStreamDone) {
				return nil
			}
			return err
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	if err := parser.flush(); err != nil && !errors.Is(err, errStreamDone) {
		return err
	}
	return parser.flushChatVisibleBuffer()
}

type openAIGenerateSSEParser struct {
	endpoint                  string
	adapter                   string
	eventName                 string
	dataLines                 []string
	result                    *GenerateOutput
	onEvent                   func(GenerateStreamEvent) error
	allowTextEncodedToolCalls bool
}

func newOpenAIGenerateSSEParser(
	endpoint string,
	adapter string,
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
	allowTextEncodedToolCalls bool,
) *openAIGenerateSSEParser {
	return &openAIGenerateSSEParser{
		endpoint:                  normalizeEndpoint(endpoint),
		adapter:                   adapter,
		dataLines:                 make([]string, 0, 4),
		result:                    result,
		onEvent:                   onEvent,
		allowTextEncodedToolCalls: allowTextEncodedToolCalls,
	}
}

func (p *openAIGenerateSSEParser) consumeLine(raw string) error {
	line := strings.TrimRight(raw, "\r")
	if line == "" {
		return p.flush()
	}
	if strings.HasPrefix(line, "event:") {
		p.eventName = strings.TrimSpace(line[len("event:"):])
		return nil
	}
	if strings.HasPrefix(line, "data:") {
		p.dataLines = append(p.dataLines, strings.TrimPrefix(line[len("data:"):], " "))
	}
	return nil
}

func (p *openAIGenerateSSEParser) flush() error {
	if len(p.dataLines) == 0 && strings.TrimSpace(p.eventName) == "" {
		return nil
	}
	eventName := strings.TrimSpace(p.eventName)
	payloadText := strings.Join(p.dataLines, "\n")
	p.eventName = ""
	p.dataLines = p.dataLines[:0]
	return p.dispatch(eventName, payloadText)
}

func (p *openAIGenerateSSEParser) dispatch(eventName string, payloadText string) error {
	trimmedPayload := strings.TrimSpace(payloadText)
	if trimmedPayload == "" {
		return nil
	}
	if trimmedPayload == valueDONEF318DF9A {
		if err := p.flushChatVisibleBuffer(); err != nil {
			return err
		}
		return errStreamDone
	}
	return p.dispatchJSONPayload(eventName, payloadText)
}

func (p *openAIGenerateSSEParser) dispatchJSONPayload(eventName string, payloadText string) error {
	parsed := make(map[string]interface{})
	if err := json.Unmarshal([]byte(payloadText), &parsed); err != nil {
		return err
	}
	if NormalizeAdapter(p.adapter) == AdapterBailianChat {
		if err := bailianResponseError(parsed, []byte(payloadText), nil); err != nil {
			return err
		}
	}
	if NormalizeAdapter(p.adapter) == AdapterMiniMaxChat {
		if err := miniMaxResponseError(parsed, []byte(payloadText), nil); err != nil {
			return err
		}
	}
	if err := parseStreamUpstreamError(parsed, payloadText); err != nil {
		return err
	}
	if p.endpoint == EndpointChatCompletions {
		return applyChatStreamEvent(p.adapter, parsed, p.result, p.onEvent, p.allowTextEncodedToolCalls)
	}
	return applyResponsesStreamEvent(p.adapter, eventName, parsed, payloadText, p.result, p.onEvent)
}

func (p *openAIGenerateSSEParser) flushChatVisibleBuffer() error {
	if p.endpoint != EndpointChatCompletions || !p.allowTextEncodedToolCalls {
		return nil
	}
	return flushChatVisibleBuffer(p.result, p.onEvent, true)
}

func parseOpenAIGenerateOutput(endpoint string, adapter string, body []byte, allowTextEncodedToolCalls bool) (*GenerateOutput, error) {
	parsed := make(map[string]interface{})
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}

	if NormalizeAdapter(adapter) == AdapterBailianChat {
		parsed, err := decodeBailianJSON(body)
		if err != nil {
			return nil, err
		}
		if err = bailianResponseError(parsed, body, nil); err != nil {
			return nil, err
		}
	}
	if NormalizeAdapter(adapter) == AdapterMiniMaxChat {
		if err := miniMaxResponseError(parsed, body, nil); err != nil {
			return nil, err
		}
	}
	result := buildGenerateOutputFromParsedForAdapter(endpoint, adapter, parsed, allowTextEncodedToolCalls)
	if normalizeEndpoint(endpoint) == EndpointChatCompletions && allowTextEncodedToolCalls && maybeDSMLToolCallsPrefix(result.Text) {
		return nil, errDeepSeekDSMLToolCallsIncomplete
	}
	result.RawJSON = string(body)
	return result, nil
}

func buildGenerateOutputFromParsed(endpoint string, parsed map[string]interface{}) *GenerateOutput {
	return buildGenerateOutputFromParsedForAdapter(endpoint, AdapterOpenAIResponses, parsed, false)
}

func buildGenerateOutputFromParsedForAdapter(endpoint string, adapter string, parsed map[string]interface{}, allowTextEncodedToolCalls bool) *GenerateOutput {
	result := &GenerateOutput{
		ResponseID:      strings.TrimSpace(getString(parsed["id"])),
		Text:            "",
		Usage:           Usage{},
		ToolCalls:       make([]ToolCall, 0),
		ServerToolCalls: make([]ToolCall, 0),
		RawJSON:         "",
	}

	switch normalizeEndpoint(endpoint) {
	case EndpointChatCompletions:
		parseChatCompletionsOutput(adapter, parsed, result, allowTextEncodedToolCalls)
	default:
		parseResponsesOutput(adapter, parsed, result)
	}

	if result.ResponseID == "" {
		result.ResponseID = strings.TrimSpace(getStringFromPath(parsed, "response", "id"))
	}
	if result.Text == "" {
		result.Text = getString(parsed["text"])
	}
	return result
}

func parseOpenAIModelList(body []byte) ([]ModelItem, error) {
	parsed := struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}

	results := make([]ModelItem, 0, len(parsed.Data))
	for _, item := range parsed.Data {
		modelID := strings.TrimSpace(item.ID)
		if modelID == "" {
			continue
		}
		results = append(results, ModelItem{
			ID:      modelID,
			OwnedBy: strings.TrimSpace(item.OwnedBy),
		})
	}
	return results, nil
}

func mergeGenerateOutput(dst *GenerateOutput, src *GenerateOutput) {
	if dst == nil || src == nil {
		return
	}
	mergeGenerateOutputIdentity(dst, src)
	mergeGenerateOutputContent(dst, src)
	mergeGenerateOutputCollections(dst, src)
}

func mergeGenerateOutputIdentity(dst *GenerateOutput, src *GenerateOutput) {
	if dst.ResponseID == "" {
		dst.ResponseID = strings.TrimSpace(src.ResponseID)
	}
	if dst.Text == "" {
		dst.Text = src.Text
	}
}

func mergeGenerateOutputContent(dst *GenerateOutput, src *GenerateOutput) {
	if strings.TrimSpace(src.ReplayStateJSON) != "" {
		dst.ReplayStateJSON = src.ReplayStateJSON
	}
	if src.Usage != (Usage{}) {
		usage := src.Usage
		if usage.ServiceTier == "" {
			usage.ServiceTier = dst.Usage.ServiceTier
		}
		dst.Usage = usage
	}
	if src.Reasoning != nil {
		mergeReasoningOutput(&dst.Reasoning, src.Reasoning)
	}
	if strings.TrimSpace(src.RawJSON) != "" {
		dst.RawJSON = src.RawJSON
	}
}

func mergeGenerateOutputCollections(dst *GenerateOutput, src *GenerateOutput) {
	if len(src.ToolCalls) > 0 {
		dst.ToolCalls = append(dst.ToolCalls[:0], src.ToolCalls...)
	}
	if len(src.ServerToolCalls) > 0 {
		dst.ServerToolCalls = append(dst.ServerToolCalls[:0], src.ServerToolCalls...)
	}
	if len(src.ServerSideToolUsage) > 0 {
		dst.ServerSideToolUsage = cloneInt64Map(src.ServerSideToolUsage)
	}
	if len(src.Citations) > 0 {
		dst.Citations = append(dst.Citations[:0], src.Citations...)
	}
}

var (
	errCategory8FDD2C1D16 = errors.New("invalid base url")
)
