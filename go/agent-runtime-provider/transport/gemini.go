package transport

// gemini.go — Google Gemini generateContent / streamGenerateContent 适配器实现。
//
// 协议文档：
//   https://ai.google.dev/api/generate-content#method:-models.generatecontent
//   https://ai.google.dev/api/generate-content#method:-models.streamgeneratecontent
//
// 与 OpenAI/Anthropic 的主要差异：
//   - 模型名嵌入 URL 路径（不在请求体中）
//   - 鉴权：x-goog-api-key 头（非 Authorization/x-api-key）
//   - assistant 角色在 Gemini 中叫 "model"
//   - 图片使用 inlineData.data (base64) + mimeType 格式
//   - system 提示通过顶层 systemInstruction 字段传递
//   - 流式端点为 :streamGenerateContent?alt=sse，每个 SSE data 都是完整的 GenerateContentResponse 片段
//   - token 统计在 usageMetadata（promptTokenCount / candidatesTokenCount / cachedContentTokenCount / thoughtsTokenCount）

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	valueProperties1FA636B2 = "properties"
	valueRequired59A6EE7B   = "required"
	valueWebSearch71BE44EE  = "web_search"
)

const (
	valueDONE563F541F             = "[DONE]"
	valueApplicationJson71FA0DF4  = "application/json"
	valueArgumentsE4E1D3E4        = "arguments"
	valueAssistant3D4C3842        = "assistant"
	valueAuto50403957             = "auto"
	valueBudgetTokens59EDF9C6     = "budget_tokens"
	valueCandidateCount54DDDAF3   = "candidate_count"
	valueCodeExecutionBA90ADB2    = "code_execution"
	valueCompleted9E9778C8        = "completed"
	valueContent5E6EDAD6          = "content"
	valueContentTextE40B040A      = "content_text"
	valueContents2D4DCE0A         = "contents"
	valueDataE4BFD03E             = "data"
	valueDescription050AFC51      = "description"
	valueDisabled98E034E3         = "disabled"
	valueError8694DAEB            = "error"
	valueFrequencyPenalty76A31B59 = "frequency_penalty"
	valueFunction15369E93         = "function"
	valueGenerationConfig7E3D8D31 = "generationConfig"
	valueGoogleSearchC7F077A8     = "google_search"
	valueImageJpegA95B69D6        = "image/jpeg"
	valueImageConfig667744C6      = "imageConfig"
	valueInProgress89487480       = "in_progress"
	valueIncludeThoughts3D13A1A5  = "includeThoughts"
	valueIncludeThoughts95EC890A  = "include_thoughts"
	valueInput9B2BD505            = "input"
	valueInstructions6059BB26     = "instructions"
	valueItemsBA477C79            = "items"
	valueJson711BA93E             =

	// geminiDefaultAPIBase 是 Gemini REST API 的默认 base，用于未配置 BaseURL 的渠道。
	"json"
	valueLogprobsE2725FF6            = "logprobs"
	valueMaxOutputTokens2B600501     = "maxOutputTokens"
	valueMaxCompletionTokens57BB2E7A = "max_completion_tokens"
	valueMaxOutputTokens5E733041     =

	// geminiGenerateContentAdapter 实现 Google Gemini generateContent 协议。
	"max_output_tokens"
	valueMessages33B9095F           = "messages"
	valueModel2D31E8EB              = "model"
	valueName450B54B0               = "name"
	valueObject1E11CFC8             = "object"
	valueParametersE9638417         = "parameters"
	valueParts7676DF70              = "parts"
	valuePresencePenaltyD594B52E    = "presence_penalty"
	valuePrompt9071DA16             = "prompt"
	valueRequestedD954CC34          = "requested"
	valueResponse6042335E           = "response"
	valueResponseMimeTypeE9149A07   = "responseMimeType"
	valueResponseModalitiesC704CE25 = "responseModalities"
	valueResponseFormat1C776FA1     = "response_format"
	valueResponseLogprobs5B490BC8   = "response_logprobs"
	valueResponseModalities49048B42 = "response_modalities"
	valueRole01C0A112               = "role"
	valueSearch9D689AF1             = "search"
	valueSeed99BC07C9               = "seed"
	valueServiceTier0757A6BB        = "service_tier"
	valueSources84DBC413            = "sources"
	valueStop6A4AFF15               = "stop"
	valueStopSequences0F3B0E21      = "stopSequences"
	valueStream1E52954F             = "stream"
	valueStreamOptionsFD1CC6DF      =

	// ── URL 构造 ───────────────────────────────────────────────────────────────────
	"stream_options"
	valueSystemF78794CC            = "system"
	valueSystemInstruction374808AD = "systemInstruction"
	valueTextB3B82DD1              = "text"
	valueTextPlain8B4362BB         = "text/plain"
	valueThinking5F763D70          = "thinking"
	valueThinkingBudgetB01620C9    = "thinkingBudget"
	valueThinkingConfig201319B9    = "thinkingConfig"
	valueThinkingBudget0F588F78    = "thinking_budget"
	valueThinkingLevel88C56BB3     = "thinking_level"
	valueTools45A87F6B             = "tools"
	valueTopK3E314D82              =

	// buildGeminiGenerateURL 构造非流式端点 URL，模型名嵌入路径。
	//
	//	{base}/v1beta/models/{model}:generateContent
	"topK"
	valueTopPC6F5CB66       = "topP"
	valueTopK70B2B264       = "top_k"
	valueTopP90AF419F       = "top_p"
	valueType1C7A9623       = "type"
	valueUrl5D7AD561        = "url"
	valueUrlContextC9F68C1B = "url_context"
	valueUser1928BB58       = "user"
)

const (
	geminiDefaultAPIBase = "https://generativelanguage.googleapis.com"
)

func geminiBaseURL(route RouteConfig) string {
	base := strings.TrimRight(strings.TrimSpace(route.BaseURL), "/")
	if base == "" {
		return geminiDefaultAPIBase
	}
	return base
}

func buildGeminiGenerateURL(base, model string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		m = "gemini-2.0-flash"
	}
	// 支持调用方传入 "models/gemini-xxx" 或直接 "gemini-xxx"
	if !strings.HasPrefix(m, "models/") {
		m = "models/" + m
	}
	return buildGeminiEndpointURL(base, "/"+m+":generateContent")
}

// buildGeminiStreamURL 构造流式端点 URL。
//
//	{base}/v1beta/models/{model}:streamGenerateContent?alt=sse
func buildGeminiStreamURL(base, model string) string {
	return strings.Replace(buildGeminiGenerateURL(base, model), ":generateContent", ":streamGenerateContent?alt=sse", 1)
}

// buildGeminiModelsURL 构造模型列表端点 URL。
func buildGeminiModelsURL(base string) string {
	return buildGeminiEndpointURL(base, "/models")
}

func buildGeminiEndpointURL(baseURL string, endpointPath string) string {
	return buildVersionedEndpointURL(normalizeGeminiEndpointBaseURL(baseURL), "v1beta", endpointPath)
}

// normalizeGeminiEndpointBaseURL removes an already-versioned Google API suffix
// before the concrete adapter appends its own stable or beta API version.
func normalizeGeminiEndpointBaseURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	lower := strings.ToLower(base)
	for _, suffix := range []string{"/v1beta2", "/v1beta", "/v1"} {
		if strings.HasSuffix(lower, suffix) {
			return strings.TrimRight(base[:len(base)-len(suffix)], "/")
		}
	}
	return base
}

// ── 请求构造 ──────────────────────────────────────────────────────────────────

func buildGeminiProviderTools(tools []map[string]interface{}) []map[string]interface{} {
	if len(tools) == 0 {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		if googleSearch, ok := geminiGoogleSearchToolPayload(tool); ok {
			result = append(result, map[string]interface{}{valueGoogleSearchC7F077A8: googleSearch})
			continue
		}
		result = append(result, tool)
	}
	return result
}

func geminiGoogleSearchToolPayload(tool map[string]interface{}) (map[string]interface{}, bool) {
	if strings.TrimSpace(getString(tool[valueType1C7A9623])) == valueGoogleSearchC7F077A8 {
		return geminiToolParameterMap(tool[valueGoogleSearchC7F077A8], tool["googleSearch"]), true
	}
	if _, ok := tool[valueGoogleSearchC7F077A8]; ok {
		return geminiToolParameterMap(tool[valueGoogleSearchC7F077A8], tool["googleSearch"]), true
	}
	if _, ok := tool["googleSearch"]; ok {
		return geminiToolParameterMap(tool["googleSearch"]), true
	}
	return nil, false
}

func geminiToolParameterMap(values ...interface{}) map[string]interface{} {
	for _, value := range values {
		if typed, ok := value.(map[string]interface{}); ok {
			return typed
		}
	}
	return map[string]interface{}{}
}

// ── 公共 HTTP 辅助 ─────────────────────────────────────────────────────────────

func (c *Client) newGeminiRequest(
	ctx context.Context,
	method, url string,
	body io.Reader,
	route RouteConfig,
) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", valueApplicationJson71FA0DF4)
	// 优先用头部传 key（避免 key 出现在访问日志 URL 中）
	if apiKey := strings.TrimSpace(route.APIKey); apiKey != "" {
		req.Header.Set("x-goog-api-key", apiKey)
	}
	setAdditionalHeaders(req, route.HeadersJSON)
	return req, nil
}

// parseGeminiError 从 Gemini 错误响应中提取 error.message。
func parseGeminiError(statusCode int, body []byte, debug *UpstreamDebugSnapshot) error {
	parsed := make(map[string]interface{})
	if err := json.Unmarshal(body, &parsed); err == nil {
		if msg := getStringFromPath(parsed, valueError8694DAEB, "message"); msg != "" {
			return &UpstreamError{StatusCode: statusCode, Message: msg, Body: string(body), Debug: debug}
		}
	}
	if statusCode == http.StatusUnauthorized {
		return &UpstreamError{
			StatusCode: statusCode,
			Message:    "google authentication failed; check API key, upstream base URL, and custom auth headers",
			Body:       string(body),
			Debug:      debug,
		}
	}
	return &UpstreamError{
		StatusCode: statusCode,
		Message:    fmt.Sprintf("upstream_status_%d", statusCode),
		Body:       string(body),
		Debug:      debug,
	}
}

// extractGeminiText 从 candidates[0].content.parts 中拼接所有 text。
func extractGeminiText(parsed map[string]interface{}) string {
	candidate := firstMapItem(asSlice(parsed["candidates"]))
	content := asMap(candidate[valueContent5E6EDAD6])
	chunks := make([]string, 0)
	for _, raw := range asSlice(content[valueParts7676DF70]) {
		part := asMap(raw)
		if thought, ok := part["thought"].(bool); ok && thought {
			continue
		}
		if text := strings.TrimSpace(getString(part[valueTextB3B82DD1])); text != "" {
			chunks = append(chunks, text)
		}
	}
	return strings.Join(chunks, "")
}

// parseGeminiUsage 解析 usageMetadata。
func parseGeminiUsage(parsed map[string]interface{}) Usage {
	totalInputTokens := getInt64FromPath(parsed, "usageMetadata", "promptTokenCount")
	cacheReadTokens := getInt64FromPath(parsed, "usageMetadata", "cachedContentTokenCount")
	return Usage{
		InputTokens:     nonCachedInputTokens(totalInputTokens, cacheReadTokens),
		OutputTokens:    getInt64FromPath(parsed, "usageMetadata", "candidatesTokenCount"),
		CacheReadTokens: cacheReadTokens,
		ReasoningTokens: getInt64FromPath(parsed, "usageMetadata", "thoughtsTokenCount"),
		RawUsageJSON:    rawUsageJSONFromPath(parsed, "usageMetadata"),
	}
}

// attachGeminiReasoningSignatureToToolCalls backfills a thinking-part signature onto
// functionCall parts when the provider only stamped the thought block. Without this,
// multi-turn tool loops omit thoughtSignature and some Gemini 3 / proxy stacks return 422.
func attachGeminiReasoningSignatureToToolCalls(calls []ToolCall, reasoning *ReasoningOutput) []ToolCall {
	if len(calls) == 0 || reasoning == nil {
		return calls
	}
	signature := strings.TrimSpace(reasoning.Signature)
	if signature == "" {
		return calls
	}
	for i := range calls {
		if strings.TrimSpace(calls[i].ThoughtSignature) == "" {
			calls[i].ThoughtSignature = signature
		}
	}
	return calls
}

func parseGeminiServerToolCalls(parsed map[string]interface{}) []ToolCall {
	if len(parsed) == 0 {
		return make([]ToolCall, 0)
	}
	result := make([]ToolCall, 0)
	for _, call := range parseGeminiExplicitServerToolCalls(parsed, -1) {
		appendUniqueToolCall(&result, call)
	}
	for candidateIndex, rawCandidate := range asSlice(parsed["candidates"]) {
		candidate := asMap(rawCandidate)
		for _, call := range parseGeminiExplicitServerToolCalls(candidate, candidateIndex) {
			appendUniqueToolCall(&result, call)
		}
		if call, ok := parseGeminiGoogleSearchServerToolCall(candidate, candidateIndex); ok {
			appendUniqueToolCall(&result, call)
		}
		if call, ok := parseGeminiURLContextServerToolCall(candidate, candidateIndex); ok {
			appendUniqueToolCall(&result, call)
		}
		for _, call := range parseGeminiCodeExecutionServerToolCalls(candidate, candidateIndex) {
			appendUniqueToolCall(&result, call)
		}
	}
	return result
}

func parseGeminiExplicitServerToolCalls(payload map[string]interface{}, candidateIndex int) []ToolCall {
	if len(payload) == 0 {
		return nil
	}
	result := make([]ToolCall, 0)
	keys := []string{
		"serverSideToolInvocations",
		"server_side_tool_invocations",
		"toolInvocations",
		"tool_invocations",
	}
	for _, key := range keys {
		for itemIndex, raw := range asSlice(payload[key]) {
			if call, ok := parseGeminiExplicitServerToolCall(asMap(raw), candidateIndex, itemIndex); ok {
				result = append(result, call)
			}
		}
	}
	content := asMap(payload[valueContent5E6EDAD6])
	for partIndex, raw := range asSlice(content[valueParts7676DF70]) {
		part := asMap(raw)
		for _, key := range []string{"serverSideToolInvocation", "server_side_tool_invocation", "toolInvocation", "tool_invocation"} {
			if call, ok := parseGeminiExplicitServerToolCall(asMap(part[key]), candidateIndex, partIndex); ok {
				result = append(result, call)
			}
		}
	}
	return result
}

func parseGeminiExplicitServerToolCall(item map[string]interface{}, candidateIndex int, itemIndex int) (ToolCall, bool) {
	if len(item) == 0 {
		return ToolCall{}, false
	}
	name := firstNonEmptyString(
		getString(item[valueName450B54B0]),
		getString(item["toolName"]),
		getString(item["tool_name"]),
		getString(item[valueType1C7A9623]),
	)
	name = normalizeGeminiServerToolName(name)
	if name == "" {
		return ToolCall{}, false
	}
	status := firstNonEmptyString(getString(item["status"]), valueInProgress89487480)
	return ToolCall{
		ToolCallID: firstNonEmptyString(
			getString(item["id"]),
			getString(item["callId"]),
			getString(item["call_id"]),
			geminiServerToolCallID(name, candidateIndex, itemIndex),
		),
		ToolType:      name,
		ToolName:      name,
		ArgumentsJSON: firstNonEmptyString(normalizeJSONString(item[valueInput9B2BD505]), normalizeJSONString(item["args"]), normalizeJSONString(item[valueArgumentsE4E1D3E4])),
		Status:        normalizeGeminiExplicitServerToolStatus(status),
		OutputJSON:    firstNonEmptyString(normalizeJSONString(item["output"]), normalizeJSONString(item["result"]), normalizeJSONString(item[valueResponse6042335E])),
		ErrorJSON:     normalizeJSONString(item[valueError8694DAEB]),
	}, true
}

func normalizeGeminiServerToolName(name string) string {
	value := strings.ToLower(strings.TrimSpace(name))
	value = strings.TrimSuffix(value, "_call")
	value = strings.TrimSuffix(value, "_tool")
	switch value {
	case valueGoogleSearchC7F077A8, valueSearch9D689AF1, valueWebSearch71BE44EE:
		return valueGoogleSearchC7F077A8
	case valueUrlContextC9F68C1B, "urlcontext":
		return valueUrlContextC9F68C1B
	case valueCodeExecutionBA90ADB2, "codeexecution":
		return valueCodeExecutionBA90ADB2
	default:
		return value
	}
}

func normalizeGeminiExplicitServerToolStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "pending", "running", valueInProgress89487480, "searching", valueRequestedD954CC34:
		return valueInProgress89487480
	case "success", "succeeded", valueCompleted9E9778C8, "done":
		return valueCompleted9E9778C8
	case "failed", valueError8694DAEB:
		return valueError8694DAEB
	default:
		return strings.TrimSpace(status)
	}
}

func parseGeminiGoogleSearchServerToolCall(candidate map[string]interface{}, candidateIndex int) (ToolCall, bool) {
	grounding := asMap(candidate["groundingMetadata"])
	if !hasGeminiSearchGroundingMetadata(grounding) {
		return ToolCall{}, false
	}
	input := map[string]interface{}{}
	if queries := asSlice(grounding["webSearchQueries"]); len(queries) > 0 {
		input["queries"] = queries
	}
	output := compactGeminiServerToolPayload(map[string]interface{}{
		"queries":            grounding["webSearchQueries"],
		valueSources84DBC413: geminiGroundingSources(grounding),
		"support_count":      len(asSlice(grounding["groundingSupports"])),
		"search_entry_point": grounding["searchEntryPoint"],
		"retrieval_metadata": grounding["retrievalMetadata"],
	})
	return ToolCall{
		ToolCallID:    geminiServerToolCallID(valueGoogleSearchC7F077A8, candidateIndex, 0),
		ToolType:      valueGoogleSearchC7F077A8,
		ToolName:      valueGoogleSearchC7F077A8,
		ArgumentsJSON: normalizeJSONString(input),
		Status:        valueCompleted9E9778C8,
		OutputJSON:    normalizeJSONString(output),
	}, true
}

func parseGeminiURLContextServerToolCall(candidate map[string]interface{}, candidateIndex int) (ToolCall, bool) {
	metadata := asMap(candidate["urlContextMetadata"])
	urlMetadata := asSlice(metadata["urlMetadata"])
	if len(urlMetadata) == 0 {
		return ToolCall{}, false
	}
	urls := make([]string, 0, len(urlMetadata))
	for _, raw := range urlMetadata {
		item := asMap(raw)
		if uri := firstNonEmptyString(getString(item["retrievedUrl"]), getString(item[valueUrl5D7AD561]), getString(item["uri"])); uri != "" {
			urls = append(urls, uri)
		}
	}
	input := map[string]interface{}{}
	if len(urls) > 0 {
		input["urls"] = urls
	}
	output := compactGeminiServerToolPayload(map[string]interface{}{
		"urls": geminiURLContextItems(urlMetadata),
	})
	return ToolCall{
		ToolCallID:    geminiServerToolCallID(valueUrlContextC9F68C1B, candidateIndex, 0),
		ToolType:      valueUrlContextC9F68C1B,
		ToolName:      valueUrlContextC9F68C1B,
		ArgumentsJSON: normalizeJSONString(input),
		Status:        valueCompleted9E9778C8,
		OutputJSON:    normalizeJSONString(output),
	}, true
}

func parseGeminiCodeExecutionServerToolCalls(candidate map[string]interface{}, candidateIndex int) []ToolCall {
	content := asMap(candidate[valueContent5E6EDAD6])
	parts := asSlice(content[valueParts7676DF70])
	result := make([]ToolCall, 0)
	currentIndex := -1
	for _, raw := range parts {
		part := asMap(raw)
		executableCode := asMap(part["executableCode"])
		if len(executableCode) > 0 {
			currentIndex++
			result = append(result, ToolCall{
				ToolCallID:    geminiServerToolCallID(valueCodeExecutionBA90ADB2, candidateIndex, currentIndex),
				ToolType:      valueCodeExecutionBA90ADB2,
				ToolName:      valueCodeExecutionBA90ADB2,
				ArgumentsJSON: normalizeJSONString(executableCode),
				Status:        valueRequestedD954CC34,
			})
			continue
		}
		executionResult := asMap(part["codeExecutionResult"])
		if len(executionResult) == 0 {
			continue
		}
		if currentIndex < 0 {
			currentIndex = 0
			result = append(result, ToolCall{
				ToolCallID: geminiServerToolCallID(valueCodeExecutionBA90ADB2, candidateIndex, currentIndex),
				ToolType:   valueCodeExecutionBA90ADB2,
				ToolName:   valueCodeExecutionBA90ADB2,
			})
		}
		idx := len(result) - 1
		result[idx].OutputJSON = normalizeJSONString(executionResult)
		result[idx].Status = geminiCodeExecutionStatus(executionResult)
		if result[idx].Status == valueError8694DAEB {
			result[idx].ErrorJSON = normalizeJSONString(executionResult)
		}
	}
	return result
}

func geminiGroundingSources(grounding map[string]interface{}) []map[string]interface{} {
	chunks := asSlice(grounding["groundingChunks"])
	if len(chunks) == 0 {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(chunks))
	for _, raw := range chunks {
		chunk := asMap(raw)
		for _, key := range []string{"web", "retrievedContext"} {
			source := asMap(chunk[key])
			uri := firstNonEmptyString(getString(source["uri"]), getString(source[valueUrl5D7AD561]))
			if uri == "" {
				continue
			}
			item := map[string]interface{}{valueUrl5D7AD561: uri}
			if title := strings.TrimSpace(getString(source["title"])); title != "" {
				item["title"] = title
			}
			result = append(result, item)
		}
	}
	return result
}

func geminiURLContextItems(items []interface{}) []map[string]interface{} {
	if len(items) == 0 {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(items))
	for _, raw := range items {
		item := asMap(raw)
		url := firstNonEmptyString(getString(item["retrievedUrl"]), getString(item[valueUrl5D7AD561]), getString(item["uri"]))
		if url == "" {
			continue
		}
		next := map[string]interface{}{valueUrl5D7AD561: url}
		if status := strings.TrimSpace(getString(item["urlRetrievalStatus"])); status != "" {
			next["status"] = status
		}
		result = append(result, next)
	}
	return result
}

func compactGeminiServerToolPayload(payload map[string]interface{}) map[string]interface{} {
	if len(payload) == 0 {
		return nil
	}
	result := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		if isEmptyGeminiPayloadValue(value) {
			continue
		}
		result[key] = value
	}
	return result
}

func isEmptyGeminiPayloadValue(value interface{}) bool {
	if value == nil {
		return true
	}
	if len(asSlice(value)) > 0 || len(asMap(value)) > 0 {
		return false
	}
	switch typed := value.(type) {
	case []interface{}:
		return len(typed) == 0
	case map[string]interface{}:
		return len(typed) == 0
	case string:
		return strings.TrimSpace(typed) == ""
	default:
		return false
	}
}

func geminiCodeExecutionStatus(result map[string]interface{}) string {
	outcome := strings.ToUpper(strings.TrimSpace(getString(result["outcome"])))
	switch outcome {
	case "", "OUTCOME_OK", "OK":
		return valueCompleted9E9778C8
	default:
		return valueError8694DAEB
	}
}

func geminiServerToolCallID(tool string, candidateIndex int, itemIndex int) string {
	return fmt.Sprintf("gemini_%s_%d_%d", strings.TrimSpace(tool), candidateIndex, itemIndex)
}

func parseGeminiCitations(parsed map[string]interface{}) []string {
	if len(parsed) == 0 {
		return nil
	}
	citations := make([]string, 0)
	for _, rawCandidate := range asSlice(parsed["candidates"]) {
		candidate := asMap(rawCandidate)
		citations = appendGeminiGroundingCitations(citations, asMap(candidate["groundingMetadata"]))
		urlContext := asMap(candidate["urlContextMetadata"])
		for _, raw := range asSlice(urlContext["urlMetadata"]) {
			item := asMap(raw)
			if uri := firstNonEmptyString(getString(item["retrievedUrl"]), getString(item[valueUrl5D7AD561]), getString(item["uri"])); uri != "" {
				citations = append(citations, uri)
			}
		}
	}
	return appendUniqueStrings(nil, citations...)
}

func appendGeminiGroundingCitations(citations []string, grounding map[string]interface{}) []string {
	for _, raw := range asSlice(grounding["groundingChunks"]) {
		chunk := asMap(raw)
		for _, key := range []string{"web", "retrievedContext"} {
			source := asMap(chunk[key])
			if uri := firstNonEmptyString(getString(source["uri"]), getString(source[valueUrl5D7AD561])); uri != "" {
				citations = append(citations, uri)
			}
		}
	}
	return citations
}

func parseGeminiServerSideToolUsage(parsed map[string]interface{}) map[string]int64 {
	if len(parsed) == 0 {
		return nil
	}
	result := make(map[string]int64)
	for _, rawCandidate := range asSlice(parsed["candidates"]) {
		candidate := asMap(rawCandidate)
		if hasGeminiSearchGroundingMetadata(asMap(candidate["groundingMetadata"])) {
			result[valueGoogleSearchC7F077A8] = 1
		}
		if len(asSlice(asMap(candidate["urlContextMetadata"])["urlMetadata"])) > 0 {
			result[valueUrlContextC9F68C1B] = 1
		}
		codeExecutionCount := int64(len(parseGeminiCodeExecutionServerToolCalls(candidate, 0)))
		if codeExecutionCount > 0 {
			result[valueCodeExecutionBA90ADB2] += codeExecutionCount
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func hasGeminiSearchGroundingMetadata(grounding map[string]interface{}) bool {
	if len(grounding) == 0 {
		return false
	}
	if len(asSlice(grounding["groundingChunks"])) > 0 || len(asSlice(grounding["groundingSupports"])) > 0 {
		return true
	}
	if len(asSlice(grounding["webSearchQueries"])) > 0 {
		return true
	}
	if len(asMap(grounding["searchEntryPoint"])) > 0 || len(asMap(grounding["retrievalMetadata"])) > 0 {
		return true
	}
	return false
}

// consumeGeminiStream 解析 Gemini SSE 流。
//
// Gemini 流式格式：每个 SSE data 都是一个完整的 GenerateContentResponse 片段。
// 最后一个事件通常携带完整的 usageMetadata。
//
//	data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"}}]}
//	data: {"candidates":[{"content":{"parts":[{"text":" world"}],"role":"model"}}],"usageMetadata":{...}}
func consumeGeminiStream(
	reader io.Reader,
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
	semanticIdleTimeout time.Duration,
) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxUpstreamBodyBytes)
	lines, stopScanner := scanGeminiStreamLines(scanner)
	defer close(stopScanner)
	idleTimer := time.NewTimer(semanticIdleTimeout)
	defer idleTimer.Stop()

	parser := geminiSSEParser{result: result, onEvent: onEvent}

	for {
		select {
		case <-idleTimer.C:
			return streamIdleTimeoutError(semanticIdleTimeout)
		case scanned := <-lines:
			if scanned.done {
				if scanned.err != nil {
					return scanned.err
				}
				_, err := parser.flush(true)
				return err
			}
			terminal, err := parser.consumeLine(scanned.line, idleTimer, semanticIdleTimeout)
			if err != nil || terminal {
				return err
			}
		}
	}
}

type geminiSSEParser struct {
	dataLines []string
	result    *GenerateOutput
	onEvent   func(GenerateStreamEvent) error
}

func (p *geminiSSEParser) consumeLine(raw string, idleTimer *time.Timer, semanticIdleTimeout time.Duration) (bool, error) {
	line := strings.TrimRight(raw, "\r")
	if line == "" {
		return p.flush(true)
	}
	if strings.HasPrefix(line, "data:") {
		resetTimer(idleTimer, semanticIdleTimeout)
		p.dataLines = append(p.dataLines, strings.TrimPrefix(line[len("data:"):], " "))
		return p.flush(false)
	}
	// Gemini 仅使用 data 行，其他 SSE 前缀不参与内容解析或重置语义超时。
	return false, nil
}

func (p *geminiSSEParser) flush(force bool) (bool, error) {
	data := strings.TrimSpace(strings.Join(p.dataLines, "\n"))
	if data == "" {
		p.dataLines = p.dataLines[:0]
		return false, nil
	}
	if data == valueDONE563F541F {
		p.dataLines = p.dataLines[:0]
		return true, nil
	}
	parsed, ok := decodeStreamObject(data)
	if !ok {
		if force {
			p.dataLines = p.dataLines[:0]
		}
		return false, nil
	}
	p.dataLines = p.dataLines[:0]
	if err := parseStreamUpstreamError(parsed, data); err != nil {
		return false, err
	}
	if err := applyGeminiStreamChunk(parsed, p.result, p.onEvent); err != nil {
		return false, err
	}
	return geminiStreamChunkTerminal(parsed), nil
}

type geminiScannedLine struct {
	line string
	err  error
	done bool
}

func scanGeminiStreamLines(scanner *bufio.Scanner) (<-chan geminiScannedLine, chan struct{}) {
	lines := make(chan geminiScannedLine, 1)
	stop := make(chan struct{})
	go func() {
		for scanner.Scan() {
			select {
			case lines <- geminiScannedLine{line: scanner.Text()}:
			case <-stop:
				return
			}
		}
		select {
		case lines <- geminiScannedLine{err: scanner.Err(), done: true}:
		case <-stop:
		}
	}()
	return lines, stop
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func geminiStreamChunkTerminal(parsed map[string]interface{}) bool {
	candidate := firstMapItem(asSlice(parsed["candidates"]))
	reason := strings.TrimSpace(getString(candidate["finishReason"]))
	return reason != "" && !strings.EqualFold(reason, "FINISH_REASON_UNSPECIFIED")
}

// applyGeminiStreamChunk 将单个 GenerateContentResponse 片段合并到 result。
func applyGeminiStreamChunk(
	parsed map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	applyGeminiStreamMetadata(parsed, result)
	if err := applyGeminiStreamServerToolCalls(parsed, result, onEvent); err != nil {
		return err
	}
	applyGeminiStreamServerToolUsage(result)

	candidate := firstMapItem(asSlice(parsed["candidates"]))
	content := asMap(candidate[valueContent5E6EDAD6])
	if err := applyGeminiStreamParts(content, result, onEvent); err != nil {
		return err
	}
	result.GeneratedImages = append(result.GeneratedImages, extractGeminiGeneratedImages(parsed, result.Text)...)
	appendGeminiStreamFunctionCalls(content, result)
	result.ToolCalls = attachGeminiReasoningSignatureToToolCalls(result.ToolCalls, result.Reasoning)
	return emitGeminiStreamUsage(parsed, result, onEvent)
}

func applyGeminiStreamMetadata(parsed map[string]interface{}, result *GenerateOutput) {
	if responseID := strings.TrimSpace(getString(parsed["responseId"])); responseID != "" {
		result.ResponseID = responseID
	}
	result.Citations = appendUniqueStrings(result.Citations, parseGeminiCitations(parsed)...)
	result.ServerSideToolUsage = mergeGeminiServerSideToolUsage(result.ServerSideToolUsage, parseGeminiServerSideToolUsage(parsed))
}

func applyGeminiStreamServerToolCalls(
	parsed map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	for _, call := range parseGeminiServerToolCalls(parsed) {
		merged := appendGeminiServerToolCall(result, call)
		if err := emitGeminiServerToolCall(result, onEvent, merged); err != nil {
			return err
		}
	}
	return nil
}

func emitGeminiServerToolCall(
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
	call ToolCall,
) error {
	if onEvent == nil {
		return nil
	}
	return onEvent(GenerateStreamEvent{
		ServerToolCall: &call,
		ResponseID:     result.ResponseID,
	})
}

func applyGeminiStreamParts(
	content map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	for _, raw := range asSlice(content[valueParts7676DF70]) {
		if err := applyGeminiStreamPart(asMap(raw), result, onEvent); err != nil {
			return err
		}
	}
	return nil
}

func applyGeminiStreamPart(
	part map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	if thought, ok := part["thought"].(bool); ok && thought {
		return applyGeminiStreamReasoningPart(part, result, onEvent)
	}
	return applyGeminiStreamTextPart(part, result, onEvent)
}

func applyGeminiStreamReasoningPart(
	part map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	think := extractReasoningDeltaText(part)
	if think == "" {
		return nil
	}
	reasoning := &ReasoningDelta{
		EventType: "google.generate_content",
		Kind:      valueContentTextE40B040A,
		Text:      think,
		Signature: strings.TrimSpace(getString(part["thoughtSignature"])),
	}
	mergeReasoningDeltaOutput(&result.Reasoning, reasoning)
	return emitGeminiReasoning(result, onEvent, reasoning)
}

func applyGeminiStreamTextPart(
	part map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	text := getString(part[valueTextB3B82DD1])
	if text == "" {
		return nil
	}
	result.Text += text
	return emitGeminiTextDelta(result, onEvent, text)
}

func emitGeminiReasoning(
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
	reasoning *ReasoningDelta,
) error {
	if onEvent == nil {
		return nil
	}
	return onEvent(GenerateStreamEvent{
		Reasoning:  reasoning,
		ResponseID: result.ResponseID,
	})
}

func emitGeminiTextDelta(result *GenerateOutput, onEvent func(GenerateStreamEvent) error, text string) error {
	if onEvent == nil {
		return nil
	}
	return onEvent(GenerateStreamEvent{
		Delta:      text,
		ResponseID: result.ResponseID,
	})
}

func appendGeminiStreamFunctionCalls(content map[string]interface{}, result *GenerateOutput) {
	for _, raw := range asSlice(content[valueParts7676DF70]) {
		if call, ok := geminiStreamFunctionCall(asMap(raw)); ok {
			result.ToolCalls = append(result.ToolCalls, call)
		}
	}
}

func geminiStreamFunctionCall(part map[string]interface{}) (ToolCall, bool) {
	fc := asMap(part["functionCall"])
	if len(fc) == 0 {
		return ToolCall{}, false
	}
	arguments := normalizeJSONString(fc["args"])
	if arguments == "" {
		arguments = "{}"
	}
	return ToolCall{
		ToolType:      valueFunction15369E93,
		ToolName:      strings.TrimSpace(getString(fc[valueName450B54B0])),
		ArgumentsJSON: arguments,
		ThoughtSignature: firstNonEmptyString(
			strings.TrimSpace(getString(part["thoughtSignature"])),
			strings.TrimSpace(getString(fc["thoughtSignature"])),
		),
		Status: valueRequestedD954CC34,
	}, true
}

func emitGeminiStreamUsage(
	parsed map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	usage := parseGeminiUsage(parsed)
	if usage == (Usage{}) {
		return nil
	}
	result.Usage = usage
	if onEvent == nil {
		return nil
	}
	return onEvent(GenerateStreamEvent{
		Usage:      usage,
		ResponseID: result.ResponseID,
	})
}

func appendGeminiServerToolCall(result *GenerateOutput, call ToolCall) ToolCall {
	if result == nil {
		return call
	}
	call = normalizeGeminiStreamServerToolCallID(result.ServerToolCalls, call)
	appendUniqueToolCall(&result.ServerToolCalls, call)
	for _, item := range result.ServerToolCalls {
		if shouldMergeToolCall(item, call) {
			return item
		}
	}
	return call
}

func normalizeGeminiStreamServerToolCallID(existing []ToolCall, call ToolCall) ToolCall {
	if strings.TrimSpace(call.ToolName) != valueCodeExecutionBA90ADB2 && strings.TrimSpace(call.ToolType) != valueCodeExecutionBA90ADB2 {
		return call
	}
	hasInput := strings.TrimSpace(call.ArgumentsJSON) != ""
	hasOutput := strings.TrimSpace(call.OutputJSON) != "" || strings.TrimSpace(call.ErrorJSON) != ""
	if hasInput && !hasOutput {
		return normalizeGeminiCodeExecutionInputCall(existing, call)
	}
	if !hasInput && hasOutput {
		return normalizeGeminiCodeExecutionOutputCall(existing, call)
	}
	return call
}

func normalizeGeminiCodeExecutionInputCall(existing []ToolCall, call ToolCall) ToolCall {
	if item, ok := latestGeminiPendingCodeExecution(existing); ok && strings.TrimSpace(item.OutputJSON) == "" && strings.TrimSpace(item.ErrorJSON) == "" {
		call.ToolCallID = item.ToolCallID
		return call
	}
	call.ToolCallID = geminiServerToolCallID(valueCodeExecutionBA90ADB2, 0, countGeminiCodeExecutionCalls(existing))
	return call
}

func normalizeGeminiCodeExecutionOutputCall(existing []ToolCall, call ToolCall) ToolCall {
	if item, ok := latestGeminiPendingCodeExecution(existing); ok {
		call.ToolCallID = item.ToolCallID
		return call
	}
	call.ToolCallID = geminiServerToolCallID(valueCodeExecutionBA90ADB2, 0, countGeminiCodeExecutionCalls(existing))
	return call
}

func latestGeminiPendingCodeExecution(items []ToolCall) (ToolCall, bool) {
	for idx := len(items) - 1; idx >= 0; idx-- {
		item := items[idx]
		if strings.TrimSpace(item.ToolName) != valueCodeExecutionBA90ADB2 && strings.TrimSpace(item.ToolType) != valueCodeExecutionBA90ADB2 {
			continue
		}
		if strings.TrimSpace(item.OutputJSON) == "" && strings.TrimSpace(item.ErrorJSON) == "" {
			return item, true
		}
	}
	return ToolCall{}, false
}

func countGeminiCodeExecutionCalls(items []ToolCall) int {
	count := 0
	for _, item := range items {
		if strings.TrimSpace(item.ToolName) == valueCodeExecutionBA90ADB2 || strings.TrimSpace(item.ToolType) == valueCodeExecutionBA90ADB2 {
			count++
		}
	}
	return count
}

func applyGeminiStreamServerToolUsage(result *GenerateOutput) {
	if result == nil {
		return
	}
	codeExecutionCount := int64(countGeminiCodeExecutionCalls(result.ServerToolCalls))
	if codeExecutionCount <= 0 {
		return
	}
	if result.ServerSideToolUsage == nil {
		result.ServerSideToolUsage = make(map[string]int64)
	}
	if codeExecutionCount > result.ServerSideToolUsage[valueCodeExecutionBA90ADB2] {
		result.ServerSideToolUsage[valueCodeExecutionBA90ADB2] = codeExecutionCount
	}
}

func mergeGeminiServerSideToolUsage(current map[string]int64, next map[string]int64) map[string]int64 {
	if len(next) == 0 {
		return current
	}
	if current == nil {
		current = make(map[string]int64, len(next))
	}
	for key, count := range next {
		if count > current[key] {
			current[key] = count
		}
	}
	return current
}

// ── Models 目录 ───────────────────────────────────────────────────────────────

// listModelsGemini 调用 GET /v1beta/models。
//
// 响应：{"models":[{"name":"models/gemini-2.0-flash","displayName":"..."},...]}
func (c *Client) listModelsGemini(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	base := geminiBaseURL(route)
	requestURL := buildGeminiModelsURL(base)

	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()

	req, err := c.newGeminiRequest(requestCtx, http.MethodGet, requestURL, nil, route)
	if err != nil {
		return nil, err
	}

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
		return nil, parseGeminiError(resp.StatusCode, body, upstreamDebugSnapshot(req, nil, resp, body))
	}

	parsed := struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}{}
	if err = json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}

	results := make([]ModelItem, 0, len(parsed.Models))
	for _, item := range parsed.Models {
		// name 格式为 "models/gemini-xxx"，提取末段作为 ID
		id := strings.TrimPrefix(strings.TrimSpace(item.Name), "models/")
		if id == "" {
			continue
		}
		results = append(results, ModelItem{
			ID:      id,
			OwnedBy: "google",
		})
	}
	return results, nil
}
