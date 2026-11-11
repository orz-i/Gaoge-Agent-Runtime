package transport

// anthropic.go — Anthropic Messages API 适配器实现。
//
// 协议文档：https://docs.anthropic.com/en/api/messages
//
// 与 OpenAI 格式的主要差异：
//   - 鉴权头：x-api-key（非 Authorization: Bearer）
//   - 必须携带 anthropic-version 头
//   - 端点：POST /v1/messages
//   - 系统提示通过顶层 system 字段传递（不放在 messages 数组中）
//   - max_tokens 为必填字段，默认使用较高上限，模型配置可覆盖
//   - 图片 source 格式不同于 OpenAI（base64 + media_type）
//   - 流式事件类型从 JSON payload 的 type 字段读取，SSE event 行仅作为补充信息

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	valueTypeF2B37301      = "type"
	valueWebSearchFB08AB8E = "web_search"
)

const (
	valueAdaptive8B08E1F4              = "adaptive"
	valueAdvisor2026030190BC19D5       = "advisor_20260301"
	valueArguments088297CF             = "arguments"
	valueAssistantE3AFA514             = "assistant"
	valueAuto0CAC2EE5                  = "auto"
	valueBudgetTokens151C7447          = "budget_tokens"
	valueCodeExecution86E8B41F         = "code_execution"
	valueCodeExecution202601208CD0E0F6 = "code_execution_20260120"
	valueCodeExecution20260521CE602076 = "code_execution_20260521"
	valueCompletedFC6E652A             = "completed"
	valueContent3EAD52FD               = "content"
	valueContentTextE041C189           = "content_text"
	valueData7C9F25C7                  = "data"
	valueDescription77B4CAC9           = "description"
	valueDisabledF0113E8C              = "disabled"
	valueEnabledD8AC0A8D               = "enabled"
	valueEphemeral834E0F93             = "ephemeral"
	valueErrorAE1827EC                 = "error"
	valueFunctionD9DEDB9D              = "function"
	valueImageJpeg429185C2             = "image/jpeg"
	valueInProgressFCF26CA0            = "in_progress"
	valueInputF8378501                 = "input"
	valueMaxTokens8DDF2E0E             = "max_tokens"
	valueMessages8CBF9CA9              = "messages"
	valueModel0397160C                 =

	// anthropicVersion 固定版本头，Anthropic 要求必须携带。
	"model"
	valueNameBEEAEB35      = "name"
	valueRequested2551EBD8 = "requested"
	valueRoleE6F72470      = "role"
	valueStream26D75EAE    =

	// anthropicDefaultMaxTokens 是 Anthropic Messages API 必填 max_tokens 的默认值。
	"stream"
	valueSystem730C6E5F                     = "system"
	valueText53ED4E15                       = "text"
	valueTool12ABE035                       = "tool"
	valueToolSearchToolBm252025111945235696 =

	// anthropicMessagesAdapter 实现 Anthropic Messages API（POST /v1/messages）。
	"tool_search_tool_bm25_20251119"
	valueToolSearchToolRegex20251119C3775DA4 = "tool_search_tool_regex_20251119"
	valueToolUse6527A599                     = "tool_use"
	valueUser72795C11                        = "user"
	valueWebFetch2026020973801918            = "web_fetch_20260209"
	valueWebFetch20260318CCDD671D            = "web_fetch_20260318"
	valueWebFetchB439CCFC                    = "web_fetch"
	valueWebSearch202503055988DFC7           = "web_search_20250305"
	valueWebSearch20260209FEE30E33           = "web_search_20260209"
	valueWebSearch202603182EE73157           = "web_search_20260318"
)

const (
	anthropicVersion          = "2023-06-01"
	anthropicEventMessageStop = "message_stop"
	anthropicInputTokensKey   = "input_tokens"

	anthropicDefaultMaxTokens = 64000
)

type anthropicMessagesAdapter struct {
	client *Client
}

func (a *anthropicMessagesAdapter) Name() string { return AdapterAnthropicMessages }

func (a *anthropicMessagesAdapter) Generate(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
) (*GenerateOutput, error) {
	return a.client.generateAnthropic(ctx, route, input, false)
}

func (a *anthropicMessagesAdapter) GenerateStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	return a.client.generateAnthropicStream(ctx, route, input, onEvent)
}

func (a *anthropicMessagesAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	return a.client.listModelsAnthropic(ctx, route)
}

// ── URL 构造 ───────────────────────────────────────────────────────────────────

func buildAnthropicMessagesURL(baseURL string) string {
	return buildVersionedEndpointURL(baseURL, "v1", "/messages")
}

func buildAnthropicModelsURL(baseURL string) string {
	return buildVersionedEndpointURL(baseURL, "v1", "/models")
}

// ── 请求构造 ──────────────────────────────────────────────────────────────────

// buildAnthropicRequestBody 构造 Anthropic Messages API 请求体。
// system role 消息会被提取为顶层 system 字段；其余 user/assistant 消息按顺序放入 messages。
func buildAnthropicRequestBody(model string, input GenerateInput, stream bool) (map[string]interface{}, error) {
	messages := normalizeMessages(input.Messages)
	providerTools, toolDefinitions, toolsEnabled, err := toolDeclarationsForInput(input)
	if err != nil {
		return nil, err
	}

	maxTokens := anthropicMaxTokensFromOptions(input.Options)
	messagePayload := collectAnthropicRequestMessages(messages, input.Options)
	payload := newAnthropicRequestPayload(model, messagePayload.messages, maxTokens, stream)

	applyAnthropicSamplingOptions(payload, input.Options)
	applyAnthropicRequestOptions(payload, input.Options, maxTokens)
	appendToolDeclarations(payload, providerTools, anthropicWebSearchTools(toolsEnabled, input.Options), buildAnthropicTools(toolDefinitions))
	applyNativeToolChoice(payload, AdapterAnthropicMessages, input, toolsEnabled && len(toolDefinitions) > 0)
	applyAnthropicSystemPayload(payload, messagePayload)
	applyAnthropicCacheControl(payload, input.Options, messagePayload.explicitCacheControl)
	applyProviderOptions(payload, input.Options,
		"anthropic-beta", "anthropic_beta", "betas", "cache_control", "contents", valueInputF8378501, "instructions", "max_output_tokens", valueMaxTokens8DDF2E0E, valueMessages8CBF9CA9, valueModel0397160C, "output_config", "output_format", "prompt", "prompt_cache", "response_format", "speed", valueStream26D75EAE, "stream_options", valueSystem730C6E5F, "systemInstruction", "thinking", "tool_choice", "tools",
		"enable_cache", "cache_timeout", "enable_thinking", "thinking_display", "effort",
	)
	normalizeAnthropicNativeTools(payload)
	return payload, nil
}

type anthropicRequestMessages struct {
	systemParts          []string
	systemBlocks         []map[string]interface{}
	explicitCacheControl bool
	messages             []map[string]interface{}
}

func collectAnthropicRequestMessages(messages []Message, options map[string]interface{}) anthropicRequestMessages {
	payload := anthropicRequestMessages{
		messages: make([]map[string]interface{}, 0, len(messages)),
	}
	for _, msg := range messages {
		if msg.Role == valueSystem730C6E5F {
			payload.appendSystemMessage(msg, options)
			continue
		}
		payload.messages = append(payload.messages, buildAnthropicMessage(msg))
	}
	return payload
}

func (payload *anthropicRequestMessages) appendSystemMessage(msg Message, options map[string]interface{}) {
	text := extractMessageText(msg)
	if text == "" {
		return
	}
	payload.systemParts = append(payload.systemParts, text)
	block := map[string]interface{}{valueTypeF2B37301: valueText53ED4E15, valueText53ED4E15: text}
	if cacheControl := anthropicCacheControlFromHint(msg.CacheControl, options); len(cacheControl) > 0 {
		block["cache_control"] = cacheControl
		payload.explicitCacheControl = true
	}
	payload.systemBlocks = append(payload.systemBlocks, block)
}

func buildAnthropicMessage(msg Message) map[string]interface{} {
	return map[string]interface{}{
		valueRoleE6F72470:    normalizeAnthropicRole(msg.Role),
		valueContent3EAD52FD: buildAnthropicContent(msg),
	}
}

func newAnthropicRequestPayload(model string, messages []map[string]interface{}, maxTokens int, stream bool) map[string]interface{} {
	return map[string]interface{}{
		valueModel0397160C:     strings.TrimSpace(model),
		valueMaxTokens8DDF2E0E: maxTokens,
		valueMessages8CBF9CA9:  messages,
		valueStream26D75EAE:    stream,
	}
}

func applyAnthropicSamplingOptions(payload map[string]interface{}, options map[string]interface{}) {
	if value, ok := modelParamFloat(options, "temperature"); ok {
		payload["temperature"] = value
	}
	if value, ok := modelParamFloat(options, "top_p"); ok {
		payload["top_p"] = value
	}
	if topK := modelParamInt(options, "top_k"); topK > 0 {
		payload["top_k"] = topK
	}
	if stops := modelParamStringList(options, "stop"); len(stops) > 0 {
		payload["stop_sequences"] = stops
	}
}

func applyAnthropicRequestOptions(payload map[string]interface{}, options map[string]interface{}, maxTokens int) {
	if thinking := anthropicThinkingFromOptions(options, maxTokens); len(thinking) > 0 {
		payload["thinking"] = thinking
	}
	if toolChoice := anthropicToolChoiceFromOptions(options); len(toolChoice) > 0 {
		payload["tool_choice"] = toolChoice
	}
	// Formal ToolChoice is applied by the caller via applyNativeToolChoice after tools are attached.
	if outputConfig := anthropicOutputConfigFromOptions(options); len(outputConfig) > 0 {
		payload["output_config"] = outputConfig
	}
	if speed := strings.TrimSpace(modelParamString(options, "speed")); speed != "" {
		payload["speed"] = speed
	}
}

func anthropicWebSearchTools(toolsEnabled bool, options map[string]interface{}) []map[string]interface{} {
	if toolsEnabled && modelParamBool(options, valueWebSearchFB08AB8E) {
		return []map[string]interface{}{{
			valueTypeF2B37301: valueWebSearch202503055988DFC7,
			valueNameBEEAEB35: valueWebSearchFB08AB8E,
		}}
	}
	return nil
}

func applyAnthropicSystemPayload(payload map[string]interface{}, messages anthropicRequestMessages) {
	if len(messages.systemBlocks) == 0 {
		return
	}
	if messages.explicitCacheControl {
		payload[valueSystem730C6E5F] = messages.systemBlocks
		return
	}
	payload[valueSystem730C6E5F] = strings.Join(messages.systemParts, "\n\n")
}

func applyAnthropicCacheControl(payload map[string]interface{}, options map[string]interface{}, explicitCacheControl bool) {
	if explicitCacheControl {
		return
	}
	if cacheControl := anthropicCacheControlFromOptions(options); len(cacheControl) > 0 {
		payload["cache_control"] = cacheControl
	}
}

func anthropicCacheControlFromHint(hint *CacheControl, options map[string]interface{}) map[string]interface{} {
	if hint == nil || !anthropicPromptCacheEnabled(options) {
		return nil
	}
	cacheControl := map[string]interface{}{valueTypeF2B37301: valueEphemeral834E0F93}
	if value := strings.TrimSpace(hint.Type); value != "" {
		cacheControl[valueTypeF2B37301] = value
	}
	ttl := strings.TrimSpace(hint.TTL)
	if ttl == "" {
		ttl = anthropicPromptCacheTTL(options)
	}
	if ttl != "" {
		cacheControl["ttl"] = ttl
	}
	return cacheControl
}

func anthropicMaxTokensFromOptions(options map[string]interface{}) int {
	if requestedMaxTokens, ok := modelParamIntValue(options, "max_output_tokens"); ok && requestedMaxTokens >= 0 {
		return requestedMaxTokens
	}
	if requestedMaxTokens, ok := modelParamIntValue(options, valueMaxTokens8DDF2E0E); ok && requestedMaxTokens >= 0 {
		return requestedMaxTokens
	}
	return anthropicDefaultMaxTokens
}

func anthropicPromptCacheEnabled(options map[string]interface{}) bool {
	if options == nil {
		return true
	}
	if enabled, ok := modelParamBoolValue(options, "enable_cache"); ok {
		return enabled
	}
	raw, ok := options["prompt_cache"]
	if !ok || raw == nil {
		return true
	}
	typed, ok := raw.(bool)
	if !ok {
		return true
	}
	return typed
}

func anthropicCacheControlFromOptions(options map[string]interface{}) map[string]interface{} {
	if !anthropicPromptCacheEnabled(options) {
		return nil
	}
	if raw, ok := options["cache_control"]; ok && raw != nil {
		if typed := asMap(raw); len(typed) > 0 {
			cacheControl := cloneMap(typed)
			if strings.TrimSpace(getString(cacheControl[valueTypeF2B37301])) == "" {
				cacheControl[valueTypeF2B37301] = valueEphemeral834E0F93
			}
			return cacheControl
		}
	}
	cacheControl := map[string]interface{}{valueTypeF2B37301: valueEphemeral834E0F93}
	if ttl := anthropicPromptCacheTTL(options); ttl != "" {
		cacheControl["ttl"] = ttl
	}
	return cacheControl
}

func anthropicPromptCacheTTL(options map[string]interface{}) string {
	switch strings.TrimSpace(strings.ToLower(modelParamString(options, "cache_timeout"))) {
	case "5m":
		return "5m"
	case "1h":
		return "1h"
	default:
		return ""
	}
}

func anthropicThinkingFromOptions(options map[string]interface{}, maxTokens int) map[string]interface{} {
	result := anthropicBaseThinkingFromOptions(options, maxTokens)
	if enabled, ok := modelParamBoolValue(options, "enable_thinking"); ok {
		if result == nil {
			result = map[string]interface{}{}
		}
		if !enabled {
			result[valueTypeF2B37301] = valueDisabledF0113E8C
			delete(result, valueBudgetTokens151C7447)
		} else if budgetTokens := modelParamInt(options, valueBudgetTokens151C7447); budgetTokens >= 1024 {
			result[valueTypeF2B37301] = valueEnabledD8AC0A8D
			result[valueBudgetTokens151C7447] = budgetTokens
		} else {
			result[valueTypeF2B37301] = valueAdaptive8B08E1F4
		}
	}
	if display := strings.TrimSpace(modelParamString(options, "thinking_display")); display != "" {
		if result == nil {
			result = map[string]interface{}{}
		}
		result["display"] = display
	}
	if len(result) == 0 {
		return nil
	}
	if strings.TrimSpace(getString(result[valueTypeF2B37301])) == "" {
		result[valueTypeF2B37301] = valueEnabledD8AC0A8D
	}
	return result
}

func anthropicBaseThinkingFromOptions(options map[string]interface{}, maxTokens int) map[string]interface{} {
	if thinking := modelParamMap(options, "thinking"); len(thinking) > 0 {
		result := make(map[string]interface{}, len(thinking))
		for key, value := range thinking {
			result[key] = value
		}
		return result
	}
	if value := modelParamString(options, "thinking"); value != "" {
		return map[string]interface{}{valueTypeF2B37301: value}
	}
	if raw, ok := options["thinking"].(bool); ok {
		if !raw {
			return map[string]interface{}{valueTypeF2B37301: valueDisabledF0113E8C}
		}
		budgetTokens := modelParamInt(options, valueBudgetTokens151C7447)
		if budgetTokens < 1024 {
			budgetTokens = 1024
			if maxTokens > 2048 {
				budgetTokens = min(maxTokens/4, maxTokens-1)
			}
		}
		return map[string]interface{}{valueTypeF2B37301: valueEnabledD8AC0A8D, valueBudgetTokens151C7447: budgetTokens}
	}
	if budgetTokens := modelParamInt(options, valueBudgetTokens151C7447); budgetTokens >= 1024 {
		return map[string]interface{}{
			valueTypeF2B37301:         valueEnabledD8AC0A8D,
			valueBudgetTokens151C7447: budgetTokens,
		}
	}
	return nil
}

func anthropicToolChoiceFromOptions(options map[string]interface{}) map[string]interface{} {
	raw, ok := options["tool_choice"]
	if !ok || raw == nil {
		return nil
	}
	if payload := asMap(raw); len(payload) > 0 {
		return payload
	}
	value := strings.TrimSpace(getString(raw))
	if value == "" {
		return nil
	}
	switch value {
	case valueAuto0CAC2EE5, "any", "none":
		return map[string]interface{}{valueTypeF2B37301: value}
	default:
		return map[string]interface{}{valueTypeF2B37301: valueTool12ABE035, valueNameBEEAEB35: value}
	}
}

func anthropicOutputConfigFromOptions(options map[string]interface{}) map[string]interface{} {
	var result map[string]interface{}
	if outputConfig := modelParamMap(options, "output_config"); len(outputConfig) > 0 {
		result = cloneMap(outputConfig)
	}
	result = applyAnthropicOutputFormat(result, options)
	result = applyAnthropicOutputEffort(result, options)
	if len(result) == 0 {
		return nil
	}
	return result
}

func applyAnthropicOutputFormat(result map[string]interface{}, options map[string]interface{}) map[string]interface{} {
	format := anthropicOutputFormatFromOptions(options)
	if len(format) == 0 || hasMapKey(result, "format") {
		return result
	}
	result = ensureMap(result)
	result["format"] = format
	return result
}

func anthropicOutputFormatFromOptions(options map[string]interface{}) map[string]interface{} {
	if format := modelParamMap(options, "output_format"); len(format) > 0 {
		return format
	}
	normalizedFormat, ok := normalizedJSONResponseFormat(options)
	if !ok {
		return nil
	}
	return asMap(normalizedFormat)
}

func applyAnthropicOutputEffort(result map[string]interface{}, options map[string]interface{}) map[string]interface{} {
	if effort := strings.TrimSpace(modelParamString(options, "effort")); effort != "" {
		return setAnthropicOutputEffort(result, effort)
	}
	return result
}

func setAnthropicOutputEffort(result map[string]interface{}, effort string) map[string]interface{} {
	if strings.TrimSpace(getString(result["effort"])) != "" {
		return result
	}
	result = ensureMap(result)
	result["effort"] = effort
	return result
}

func hasMapKey(payload map[string]interface{}, key string) bool {
	if payload == nil {
		return false
	}
	_, exists := payload[key]
	return exists
}

func ensureMap(payload map[string]interface{}) map[string]interface{} {
	if payload != nil {
		return payload
	}
	return map[string]interface{}{}
}

func buildAnthropicTools(tools []ToolDefinition) []map[string]interface{} {
	if len(tools) == 0 {
		return nil
	}
	items := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		items = append(items, map[string]interface{}{
			valueNameBEEAEB35:        name,
			valueDescription77B4CAC9: strings.TrimSpace(tool.Description),
			"input_schema":           decodeToolSchema(tool.InputSchema),
		})
	}
	return items
}

func normalizeAnthropicNativeTools(payload map[string]interface{}) {
	if payload == nil {
		return
	}
	tools := anthropicToolPayloads(payload["tools"])
	if len(tools) == 0 {
		return
	}
	changed := false
	for index, tool := range tools {
		next, hasChange := normalizeAnthropicNativeTool(tool)
		if !hasChange {
			continue
		}
		tools[index] = next
		changed = true
	}
	if changed {
		payload["tools"] = tools
	}
}

func normalizeAnthropicNativeTool(tool map[string]interface{}) (map[string]interface{}, bool) {
	toolType := strings.TrimSpace(getString(tool[valueTypeF2B37301]))
	if toolType == "" {
		return tool, false
	}
	next := cloneMap(tool)
	changed := setAnthropicNativeToolName(next, toolType)
	if setAnthropicNativeToolAllowedCallers(next, toolType) {
		changed = true
	}
	return next, changed
}

func setAnthropicNativeToolName(tool map[string]interface{}, toolType string) bool {
	if strings.TrimSpace(getString(tool[valueNameBEEAEB35])) != "" {
		return false
	}
	name := anthropicNativeToolName(toolType)
	if name == "" {
		return false
	}
	tool[valueNameBEEAEB35] = name
	return true
}

func setAnthropicNativeToolAllowedCallers(tool map[string]interface{}, toolType string) bool {
	if _, ok := tool["allowed_callers"]; ok {
		return false
	}
	callers := anthropicNativeToolAllowedCallers(toolType)
	if len(callers) == 0 {
		return false
	}
	tool["allowed_callers"] = callers
	return true
}

func anthropicNativeToolName(toolType string) string {
	switch strings.TrimSpace(toolType) {
	case valueWebSearch202503055988DFC7, valueWebSearch20260209FEE30E33, valueWebSearch202603182EE73157:
		return valueWebSearchFB08AB8E
	case "web_fetch_20250910", valueWebFetch2026020973801918, valueWebFetch20260318CCDD671D:
		return valueWebFetchB439CCFC
	case "code_execution_20250825", valueCodeExecution202601208CD0E0F6, valueCodeExecution20260521CE602076:
		return valueCodeExecution86E8B41F
	case valueAdvisor2026030190BC19D5:
		return "advisor"
	case valueToolSearchToolRegex20251119C3775DA4:
		return "tool_search_tool_regex"
	case valueToolSearchToolBm252025111945235696:
		return "tool_search_tool_bm25"
	default:
		return ""
	}
}

func anthropicNativeToolAllowedCallers(toolType string) []string {
	switch strings.TrimSpace(toolType) {
	case valueWebSearch20260209FEE30E33, valueWebSearch202603182EE73157,
		valueWebFetch2026020973801918, valueWebFetch20260318CCDD671D:
		return []string{"direct"}
	default:
		return nil
	}
}

type anthropicToolClassifier struct {
	nativeServerToolNames map[string]struct{}
	clientToolNames       map[string]struct{}
}

func newAnthropicToolClassifier(payload map[string]interface{}, clientTools []ToolDefinition) anthropicToolClassifier {
	classifier := anthropicToolClassifier{}
	for _, tool := range anthropicToolPayloads(payload["tools"]) {
		toolType := strings.TrimSpace(getString(tool[valueTypeF2B37301]))
		if !isAnthropicNativeToolType(toolType) {
			continue
		}
		toolName := strings.TrimSpace(getString(tool[valueNameBEEAEB35]))
		if toolName == "" {
			toolName = anthropicNativeToolName(toolType)
		}
		if toolName == "" {
			continue
		}
		if classifier.nativeServerToolNames == nil {
			classifier.nativeServerToolNames = map[string]struct{}{}
		}
		classifier.nativeServerToolNames[toolName] = struct{}{}
	}
	for _, tool := range clientTools {
		toolName := strings.TrimSpace(tool.Name)
		if toolName == "" {
			continue
		}
		if classifier.clientToolNames == nil {
			classifier.clientToolNames = map[string]struct{}{}
		}
		classifier.clientToolNames[toolName] = struct{}{}
	}
	return classifier
}

func (c anthropicToolClassifier) isUnsupportedNativeClientToolUse(toolName string) bool {
	name := strings.TrimSpace(toolName)
	if name == "" || len(c.nativeServerToolNames) == 0 {
		return false
	}
	if _, ok := c.nativeServerToolNames[name]; !ok {
		return false
	}
	_, clientToolDeclared := c.clientToolNames[name]
	return !clientToolDeclared
}

func isAnthropicNativeToolType(toolType string) bool {
	switch strings.TrimSpace(toolType) {
	case valueWebSearch202503055988DFC7,
		valueWebSearch20260209FEE30E33,
		valueWebSearch202603182EE73157,
		"web_fetch_20250910",
		valueWebFetch2026020973801918,
		valueWebFetch20260318CCDD671D,
		"code_execution_20250825",
		valueCodeExecution202601208CD0E0F6,
		valueCodeExecution20260521CE602076,
		valueAdvisor2026030190BC19D5,
		valueToolSearchToolRegex20251119C3775DA4,
		valueToolSearchToolBm252025111945235696:
		return true
	default:
		return false
	}
}

func isAnthropicServerToolResultBlockType(blockType string) bool {
	value := strings.TrimSpace(blockType)
	return value != "" && strings.HasSuffix(value, "_tool_result")
}

func anthropicUnsupportedNativeToolError(toolName string, rawBody string) error {
	return &UpstreamError{
		StatusCode: http.StatusBadGateway,
		Message:    anthropicUnsupportedNativeToolMessage(toolName),
		Body:       rawBody,
	}
}

func anthropicUnsupportedNativeToolMessage(toolName string) string {
	name := strings.TrimSpace(toolName)
	if name == "" {
		name = "native_tool"
	}
	return fmt.Sprintf("Anthropic native tool %q must be executed server-side, but the selected upstream returned it as a client-side tool call. Disable this Claude native tool for the selected model route or use an MCP tool instead.", name)
}

// normalizeAnthropicRole 将内部 role 映射到 Anthropic 的 user/assistant。
// Anthropic Messages API 只接受这两种角色（system 已被提升到顶层）。
func normalizeAnthropicRole(role string) string {
	switch role {
	case valueAssistantE3AFA514:
		return valueAssistantE3AFA514
	default:
		return valueUser72795C11
	}
}

// extractMessageText 从纯文本消息中提取文字（用于 system 提升）。
func extractMessageText(msg Message) string {
	if len(msg.Parts) == 0 {
		return msg.Content
	}
	chunks := make([]string, 0, len(msg.Parts))
	for _, p := range msg.Parts {
		if p.Kind == ContentPartText || p.Kind == ContentPartFile {
			chunks = append(chunks, p.Text)
		}
	}
	return strings.Join(chunks, "\n")
}

// buildAnthropicContent 将 Message 转换为 Anthropic content 数组。
// 纯文本消息可简化为字符串（Anthropic 支持简化形式）；多模态保持数组。
func buildAnthropicContent(msg Message) interface{} {
	if len(msg.Parts) == 0 && len(msg.ToolCalls) == 0 && len(msg.ToolResults) == 0 {
		// 简化形式：纯文本直接用字符串
		return msg.Content
	}

	blocks := make([]map[string]interface{}, 0, len(msg.Parts)+len(msg.ToolCalls)+len(msg.ToolResults)+1)
	// Anthropic requires tool_result blocks to appear first in the user message
	// immediately following the corresponding assistant tool_use blocks.
	blocks = appendAnthropicToolResultBlocks(blocks, msg.ToolResults)
	if text := strings.TrimSpace(msg.Content); text != "" {
		blocks = append(blocks, anthropicTextContentBlock(text))
	}
	blocks = appendAnthropicPartBlocks(blocks, msg.Parts)
	blocks = appendAnthropicToolUseBlocks(blocks, msg.ToolCalls)

	if len(blocks) == 0 {
		return msg.Content
	}
	return blocks
}

func appendAnthropicPartBlocks(blocks []map[string]interface{}, parts []ContentPart) []map[string]interface{} {
	for _, part := range parts {
		block, ok := anthropicContentPartBlock(part)
		if ok {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func anthropicContentPartBlock(part ContentPart) (map[string]interface{}, bool) {
	if part.Kind == ContentPartVideo {
		return map[string]interface{}{
			valueTypeF2B37301: ContentPartVideo,
			nativeVideoSourceKey: map[string]interface{}{
				valueTypeF2B37301: miniMaxImageBase64, nativeMediaTypeKey: nativeVideoMIME(part),
				valueData7C9F25C7: base64.StdEncoding.EncodeToString(part.Data),
			},
		}, true
	}
	if part.Kind == ContentPartImage {
		return anthropicImageContentBlock(part)
	}
	text := strings.TrimSpace(part.Text)
	if text == "" {
		return nil, false
	}
	return anthropicTextContentBlock(part.Text), true
}

func anthropicImageContentBlock(part ContentPart) (map[string]interface{}, bool) {
	if len(part.Data) == 0 {
		return nil, false
	}
	mime := strings.TrimSpace(part.MimeType)
	if mime == "" {
		mime = valueImageJpeg429185C2
	}
	return map[string]interface{}{
		valueTypeF2B37301: "image",
		nativeVideoSourceKey: map[string]interface{}{
			valueTypeF2B37301:  miniMaxImageBase64,
			nativeMediaTypeKey: mime,
			valueData7C9F25C7:  base64.StdEncoding.EncodeToString(part.Data),
		},
	}, true
}

func anthropicTextContentBlock(text string) map[string]interface{} {
	return map[string]interface{}{
		valueTypeF2B37301: valueText53ED4E15,
		valueText53ED4E15: text,
	}
}

func appendAnthropicToolUseBlocks(blocks []map[string]interface{}, toolCalls []ToolCall) []map[string]interface{} {
	for _, item := range toolCalls {
		blocks = append(blocks, anthropicToolUseBlock(item))
	}
	return blocks
}

func anthropicToolUseBlock(item ToolCall) map[string]interface{} {
	return map[string]interface{}{
		valueTypeF2B37301:  valueToolUse6527A599,
		"id":               strings.TrimSpace(item.ToolCallID),
		valueNameBEEAEB35:  strings.TrimSpace(item.ToolName),
		valueInputF8378501: decodeAnthropicToolInput(item.ArgumentsJSON),
	}
}

func decodeAnthropicToolInput(raw string) map[string]interface{} {
	args := strings.TrimSpace(raw)
	if args == "" {
		args = "{}"
	}
	input := make(map[string]interface{})
	if err := json.Unmarshal([]byte(args), &input); err != nil {
		return map[string]interface{}{valueArguments088297CF: args}
	}
	return input
}

func appendAnthropicToolResultBlocks(blocks []map[string]interface{}, toolResults []ToolResult) []map[string]interface{} {
	for _, item := range toolResults {
		blocks = append(blocks, anthropicToolResultBlock(item))
	}
	return blocks
}

func anthropicToolResultBlock(item ToolResult) map[string]interface{} {
	block := map[string]interface{}{
		valueTypeF2B37301:    "tool_result",
		"tool_use_id":        strings.TrimSpace(item.ToolCallID),
		valueContent3EAD52FD: buildToolResultContent(item),
	}
	if strings.TrimSpace(item.Error) != "" {
		block["is_error"] = true
	}
	return block
}

// ── HTTP 请求辅助 ──────────────────────────────────────────────────────────────

func (c *Client) newAnthropicRequest(
	ctx context.Context,
	method, url string,
	body io.Reader,
	route RouteConfig,
) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", anthropicVersion)
	if apiKey := strings.TrimSpace(route.APIKey); apiKey != "" {
		if NormalizeAdapter(route.Protocol) == AdapterMiniMaxAnthropic {
			setBearerAuthHeader(req, apiKey)
		} else {
			req.Header.Set("x-api-key", apiKey)
		}
	}
	setAdditionalHeaders(req, route.HeadersJSON)
	return req, nil
}

// parseAnthropicError 从 Anthropic 错误响应中提取 error.message。
// 格式：{"type":"error","error":{"type":"...","message":"..."}}
func parseAnthropicError(statusCode int, body []byte, debug *UpstreamDebugSnapshot) error {
	parsed := make(map[string]interface{})
	if err := json.Unmarshal(body, &parsed); err == nil {
		if msg := getStringFromPath(parsed, valueErrorAE1827EC, "message"); msg != "" {
			return &UpstreamError{StatusCode: statusCode, Message: msg, Body: string(body), Debug: debug}
		}
	}
	return &UpstreamError{
		StatusCode: statusCode,
		Message:    fmt.Sprintf("upstream_status_%d", statusCode),
		Body:       string(body),
		Debug:      debug,
	}
}

func applyAnthropicBetaHeaders(req *http.Request, payload map[string]interface{}, options map[string]interface{}) {
	if req == nil || len(payload) == 0 {
		return
	}
	betas := make([]string, 0, 2)
	betas = appendUniqueStrings(betas, modelParamStringList(options, "betas")...)
	betas = appendUniqueStrings(betas, modelParamStringList(options, "anthropic_beta")...)
	betas = appendUniqueStrings(betas, modelParamStringList(options, "anthropic-beta")...)
	if strings.EqualFold(strings.TrimSpace(getString(payload["speed"])), "fast") {
		betas = appendUniqueStrings(betas, "fast-mode-2026-02-01")
	}
	for _, tool := range anthropicToolPayloads(payload["tools"]) {
		switch strings.TrimSpace(getString(tool[valueTypeF2B37301])) {
		case valueAdvisor2026030190BC19D5:
			betas = appendUniqueStrings(betas, "advisor-2026-03-01")
		case "mcp_toolset_20251119":
			betas = appendUniqueStrings(betas, "mcp-client-2025-11-20")
		}
	}
	if len(betas) == 0 {
		return
	}
	existing := strings.Split(req.Header.Get("anthropic-beta"), ",")
	for _, item := range existing {
		betas = appendUniqueStrings(betas, strings.TrimSpace(item))
	}
	req.Header.Set("anthropic-beta", strings.Join(betas, ","))
}

func anthropicToolPayloads(raw interface{}) []map[string]interface{} {
	switch typed := raw.(type) {
	case []map[string]interface{}:
		return append([]map[string]interface{}(nil), typed...)
	case []interface{}:
		items := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			if payload := asMap(item); len(payload) > 0 {
				items = append(items, payload)
			}
		}
		return items
	default:
		return nil
	}
}

// ── 非流式调用 ────────────────────────────────────────────────────────────────

func (c *Client) generateAnthropic(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	_ bool, // stream 参数保留，始终传 false
) (*GenerateOutput, error) {
	requestURL := buildAnthropicMessagesURL(route.BaseURL)
	if requestURL == "" {
		return nil, errCategory2398445EC8
	}

	requestBody, err := buildAnthropicRequestBody(route.UpstreamModel, input, false)
	if err != nil {
		return nil, err
	}
	applyAnthropicReplayRequest(route.Protocol, requestBody, input)
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()

	req, err := c.newAnthropicRequest(requestCtx, http.MethodPost, requestURL, bytes.NewReader(payload), route)
	if err != nil {
		return nil, err
	}
	applyAnthropicBetaHeaders(req, requestBody, input.Options)

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
		return nil, parseAnthropicError(resp.StatusCode, body, upstreamDebugSnapshot(req, payload, resp, body))
	}

	debug := upstreamDebugSnapshot(req, payload, resp, body)
	output, err := parseAnthropicAdapterResponse(route.Protocol, body, newAnthropicToolClassifier(requestBody, input.Tools), debug)
	if err == nil {
		reconcileAnthropicPendingResults(route.Protocol, input, output)
	}
	return output, err
}

func parseAnthropicAdapterResponse(adapter string, body []byte, classifier anthropicToolClassifier, debug *UpstreamDebugSnapshot) (*GenerateOutput, error) {
	output, err := parseAnthropicResponseWithDebug(body, classifier, debug)
	if err == nil {
		err = recordAnthropicResponseReplay(adapter, output, body)
	}
	return output, attachUpstreamDebug(err, debug)
}

func parseAnthropicResponseWithDebug(body []byte, classifier anthropicToolClassifier, debug *UpstreamDebugSnapshot) (*GenerateOutput, error) {
	output, err := parseAnthropicResponse(body, classifier)
	if err != nil {
		return nil, attachUpstreamDebug(err, debug)
	}
	if err = anthropicCompletionError(output); err != nil {
		return nil, attachUpstreamDebug(err, debug)
	}
	output.Debug = debug
	return output, nil
}

// parseAnthropicResponse 解析 Anthropic 非流式响应。
//
// 响应格式：
//
//	{
//	  "id": "msg_...",
//	  "content": [{"type":"text","text":"..."}],
//	  "usage": {
//	    "input_tokens": 10,
//	    "output_tokens": 100,
//	    "cache_creation_input_tokens": 0,
//	    "cache_read_input_tokens": 0
//	  }
//	}
func parseAnthropicResponse(body []byte, classifier anthropicToolClassifier) (*GenerateOutput, error) {
	parsed := make(map[string]interface{})
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	toolCalls, err := parseAnthropicToolUse(parsed, classifier, string(body))
	if err != nil {
		return nil, err
	}

	result := &GenerateOutput{
		ResponseID:          strings.TrimSpace(getString(parsed["id"])),
		Text:                extractAnthropicText(parsed),
		Reasoning:           extractAnthropicReasoning(parsed),
		Usage:               parseAnthropicUsage(parsed),
		ToolCalls:           toolCalls,
		ServerToolCalls:     parseAnthropicServerToolUse(parsed),
		ServerSideToolUsage: parseAnthropicServerSideToolUsage(parsed),
		RawJSON:             string(body),
		StopReason:          strings.TrimSpace(getString(parsed["stop_reason"])),
		StopSequence:        strings.TrimSpace(getString(parsed["stop_sequence"])),
	}
	return result, nil
}

// extractAnthropicText 从 content 数组中提取所有 text block 的文字。
func extractAnthropicText(parsed map[string]interface{}) string {
	chunks := make([]string, 0)
	for _, raw := range asSlice(parsed[valueContent3EAD52FD]) {
		block := asMap(raw)
		if getString(block[valueTypeF2B37301]) == valueText53ED4E15 {
			if text := strings.TrimSpace(getString(block[valueText53ED4E15])); text != "" {
				chunks = append(chunks, text)
			}
		}
	}
	return strings.Join(chunks, "")
}

func extractAnthropicReasoning(parsed map[string]interface{}) *ReasoningOutput {
	result := &ReasoningOutput{}
	for _, raw := range asSlice(parsed[valueContent3EAD52FD]) {
		block := asMap(raw)
		blockType := strings.TrimSpace(getString(block[valueTypeF2B37301]))
		switch {
		case strings.Contains(blockType, "thinking") || strings.Contains(blockType, "reason"):
			result.Text += extractReasoningDeltaText(block)
			if signature := strings.TrimSpace(getString(block["signature"])); signature != "" {
				result.Signature = signature
			}
		}
	}
	if strings.TrimSpace(result.Text) == "" && strings.TrimSpace(result.Signature) == "" {
		return nil
	}
	return result
}

// parseAnthropicUsage 解析 Anthropic usage 字段。
func parseAnthropicUsage(parsed map[string]interface{}) Usage {
	cacheReadInputTokens := getInt64FromPath(parsed, "usage", "cache_read_input_tokens")
	cacheCreation1hInputTokens := getInt64FromPath(parsed, "usage", "cache_creation", "ephemeral_1h_input_tokens")
	cacheCreation5mInputTokens := getInt64FromPath(parsed, "usage", "cache_creation", "ephemeral_5m_input_tokens")
	cacheCreationByTTL := cacheCreation1hInputTokens + cacheCreation5mInputTokens
	cacheCreationInputTokens := cacheCreationByTTL
	if cacheCreationInputTokens <= 0 {
		cacheCreationInputTokens = getInt64FromPath(parsed, "usage", "cache_creation_input_tokens")
	}
	return Usage{
		InputTokens:        getInt64FromPath(parsed, "usage", anthropicInputTokensKey),
		OutputTokens:       getInt64FromPath(parsed, "usage", "output_tokens"),
		CacheReadTokens:    cacheReadInputTokens,
		CacheWriteTokens:   cacheCreationInputTokens,
		CacheWrite5mTokens: cacheCreation5mInputTokens,
		CacheWrite1hTokens: cacheCreation1hInputTokens,
		Speed:              strings.TrimSpace(getStringFromPath(parsed, "usage", "speed")),
		RawUsageJSON:       rawUsageJSONFromPath(parsed, "usage"),
	}
}

func parseAnthropicServerSideToolUsage(parsed map[string]interface{}) map[string]int64 {
	usage := asMap(parsed["usage"])
	if len(usage) == 0 {
		return nil
	}
	raw := asMap(usage["server_tool_use"])
	if len(raw) == 0 {
		raw = asMap(usage["server_side_tool_usage"])
	}
	if len(raw) == 0 {
		return nil
	}
	result := make(map[string]int64, len(raw))
	for key, value := range raw {
		normalized := normalizeAnthropicServerSideToolUsageKey(key)
		count := toInt64(value)
		if normalized == "" || count <= 0 {
			continue
		}
		result[normalized] += count
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func normalizeAnthropicServerSideToolUsageKey(key string) string {
	value := strings.TrimSpace(key)
	value = strings.TrimSuffix(value, "_requests")
	value = strings.TrimSuffix(value, "_request")
	value = strings.TrimSuffix(value, "_calls")
	value = strings.TrimSuffix(value, "_call")
	return strings.TrimSpace(value)
}

// parseAnthropicToolUse 解析需要本地执行的 Anthropic tool_use content block。
func parseAnthropicToolUse(parsed map[string]interface{}, classifier anthropicToolClassifier, rawBody string) ([]ToolCall, error) {
	var result []ToolCall
	for _, raw := range asSlice(parsed[valueContent3EAD52FD]) {
		block := asMap(raw)
		if getString(block[valueTypeF2B37301]) != valueToolUse6527A599 {
			continue
		}
		toolName := strings.TrimSpace(getString(block[valueNameBEEAEB35]))
		if classifier.isUnsupportedNativeClientToolUse(toolName) {
			return nil, anthropicUnsupportedNativeToolError(toolName, rawBody)
		}
		arguments := normalizeJSONString(block[valueInputF8378501])
		if arguments == "" {
			arguments = "{}"
		}
		result = append(result, ToolCall{
			ToolCallID:    strings.TrimSpace(getString(block["id"])),
			ToolType:      valueFunctionD9DEDB9D,
			ToolName:      toolName,
			ArgumentsJSON: arguments,
			Status:        valueRequested2551EBD8,
		})
	}
	if result == nil {
		return make([]ToolCall, 0), nil
	}
	return result, nil
}

// parseAnthropicServerToolUse 解析 Anthropic server-side tool trace。
func parseAnthropicServerToolUse(parsed map[string]interface{}) []ToolCall {
	items := make([]ToolCall, 0)
	indexByID := map[string]int{}
	for _, raw := range asSlice(parsed[valueContent3EAD52FD]) {
		block := asMap(raw)
		blockType := strings.TrimSpace(getString(block[valueTypeF2B37301]))
		switch {
		case blockType == "server_tool_use":
			toolCall := anthropicServerToolUseCall(block)
			if toolCall.ToolCallID != "" {
				indexByID[toolCall.ToolCallID] = len(items)
			}
			items = append(items, toolCall)
		case isAnthropicServerToolResultBlockType(blockType):
			applyAnthropicServerToolResult(items, indexByID, block)
		}
	}
	if items == nil {
		return make([]ToolCall, 0)
	}
	return items
}

func anthropicServerToolUseCall(block map[string]interface{}) ToolCall {
	arguments := normalizeJSONString(block[valueInputF8378501])
	if arguments == "" {
		arguments = "{}"
	}
	return ToolCall{
		ToolCallID:    strings.TrimSpace(getString(block["id"])),
		ToolType:      strings.TrimSpace(getString(block[valueTypeF2B37301])),
		ToolName:      strings.TrimSpace(getString(block[valueNameBEEAEB35])),
		ArgumentsJSON: arguments, Status: valueInProgressFCF26CA0,
	}
}

func applyAnthropicServerToolResult(items []ToolCall, indexByID map[string]int, block map[string]interface{}) {
	toolUseID := strings.TrimSpace(getString(block["tool_use_id"]))
	if toolUseID == "" {
		return
	}
	index, ok := indexByID[toolUseID]
	if !ok {
		return
	}
	output := anthropicServerToolResultOutputJSON(block)
	if output == "" {
		output = "{}"
	}
	items[index].OutputJSON = output
	items[index].Status = valueCompletedFC6E652A
}

// ── 流式调用 ──────────────────────────────────────────────────────────────────

func (c *Client) generateAnthropicStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	requestURL := buildAnthropicMessagesURL(route.BaseURL)
	if requestURL == "" {
		return nil, errCategory2398445EC8
	}

	requestBody, err := buildAnthropicRequestBody(route.UpstreamModel, input, true)
	if err != nil {
		return nil, err
	}
	applyAnthropicReplayRequest(route.Protocol, requestBody, input)
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	firstByteCtx, firstByteCancel := context.WithCancel(ctx)
	defer firstByteCancel()

	firstByteTimer := time.AfterFunc(resolveReadTimeout(route.ReadTimeoutMS), firstByteCancel)
	defer firstByteTimer.Stop()

	req, err := c.newAnthropicRequest(firstByteCtx, http.MethodPost, requestURL, bytes.NewReader(payload), route)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	applyAnthropicBetaHeaders(req, requestBody, input.Options)

	resp, err := c.httpClientForRoute(route).Do(req)
	firstByteTimer.Stop()
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := readUpstreamBody(resp.Body)
		return nil, parseAnthropicError(resp.StatusCode, body, upstreamDebugSnapshot(req, payload, resp, body))
	}

	result := &GenerateOutput{
		ToolCalls:       make([]ToolCall, 0),
		ServerToolCalls: make([]ToolCall, 0),
	}

	idleReader := newIdleTimeoutReader(resp.Body, resolveStreamIdleTimeout(route.StreamIdleTimeoutMS))
	streamBody := newUpstreamBodyRecorder(idleReader)
	if err = consumeAnthropicStreamWithReplay(streamBody, result, onEvent, newAnthropicToolClassifier(requestBody, input.Tools), newAnthropicReplay(route.Protocol)); err != nil {
		return nil, attachUpstreamDebug(err, upstreamDebugSnapshot(req, payload, resp, streamErrorBody(streamBody, err)))
	}
	compactAnthropicStreamToolCalls(result)
	reconcileAnthropicPendingResults(route.Protocol, input, result)
	return result, nil
}

// consumeAnthropicStream 解析 Anthropic SSE 流事件并回填 result。
//
// Anthropic SSE 格式：
//
//	event: message_start
//	data: {"type":"message_start","message":{"id":"msg_...","usage":{...}}}
//
//	event: content_block_delta
//	data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}
//
//	event: message_delta
//	data: {"type":"message_delta","usage":{"output_tokens":100}}
//
//	event: message_stop
//	data: {"type":"message_stop"}
func consumeAnthropicStream(
	reader io.Reader,
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
	classifier anthropicToolClassifier,
) error {
	return consumeAnthropicStreamWithReplay(reader, result, onEvent, classifier, nil)
}

func consumeAnthropicStreamWithReplay(
	reader io.Reader,
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
	classifier anthropicToolClassifier,
	replay *anthropicReplay,
) error {
	parser := newAnthropicSSEParser(result, onEvent, classifier)
	parser.replay = replay
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if err := parser.consumeLine(scanner.Text()); err != nil {
			if isAnthropicStreamDone(err) {
				return anthropicCompletionError(result)
			}
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := parser.flush(); err != nil {
		if isAnthropicStreamDone(err) {
			return anthropicCompletionError(result)
		}
		return err
	}
	return &UpstreamError{
		StatusCode: http.StatusOK,
		Message:    "anthropic stream ended before message_stop",
	}
}

type anthropicSSEParser struct {
	eventName  string
	dataLines  []string
	result     *GenerateOutput
	onEvent    func(GenerateStreamEvent) error
	classifier anthropicToolClassifier
	replay     *anthropicReplay
}

func newAnthropicSSEParser(
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
	classifier anthropicToolClassifier,
) *anthropicSSEParser {
	return &anthropicSSEParser{
		dataLines:  make([]string, 0, 4),
		result:     result,
		onEvent:    onEvent,
		classifier: classifier,
	}
}

func (p *anthropicSSEParser) consumeLine(raw string) error {
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

func (p *anthropicSSEParser) flush() error {
	data := strings.Join(p.dataLines, "\n")
	p.eventName = ""
	p.dataLines = p.dataLines[:0]
	return p.dispatch(data)
}

func (p *anthropicSSEParser) dispatch(data string) error {
	if strings.TrimSpace(data) == "" {
		return nil
	}
	parsed, ok := decodeStreamObject(data)
	if !ok {
		return nil
	}
	if p.replay != nil {
		if err := p.replay.consume(parsed); err != nil {
			return err
		}
		if getString(parsed["type"]) == anthropicEventMessageStop {
			p.result.ReplayStateJSON = p.replay.encode()
		}
	}
	return applyAnthropicStreamEvent(parsed, data, p.result, p.onEvent, p.classifier)
}

var errAnthropicStreamDone = fmt.Errorf("anthropic stream done")

func isAnthropicStreamDone(err error) bool {
	return errors.Is(err, errAnthropicStreamDone)
}

// applyAnthropicStreamEvent 处理单个 Anthropic 流式事件 payload。
func applyAnthropicStreamEvent(
	parsed map[string]interface{},
	rawBody string,
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
	classifier anthropicToolClassifier,
) error {
	eventType := strings.TrimSpace(getString(parsed[valueTypeF2B37301]))

	switch eventType {
	case "message_start":
		return applyAnthropicMessageStart(parsed, result, onEvent)
	case "content_block_start":
		return applyAnthropicContentBlockStart(parsed, rawBody, result, onEvent, classifier)
	case "content_block_delta":
		return applyAnthropicContentBlockDelta(parsed, result, onEvent)
	case "content_block_stop":
		index := anthropicContentBlockIndex(parsed)
		_, _ = markAnthropicStreamToolCallComplete(result, index)

	case "message_delta":
		return applyAnthropicMessageDelta(parsed, result, onEvent)
	case anthropicEventMessageStop:
		return errAnthropicStreamDone

	case valueErrorAE1827EC:
		return anthropicStreamError(parsed, rawBody)
	}

	return nil
}

func applyAnthropicMessageStart(
	parsed map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	msg := asMap(parsed["message"])
	if id := strings.TrimSpace(getString(msg["id"])); id != "" {
		result.ResponseID = id
	}
	result.Usage = parseAnthropicUsage(msg)
	if serverSideToolUsage := parseAnthropicServerSideToolUsage(msg); len(serverSideToolUsage) > 0 {
		result.ServerSideToolUsage = serverSideToolUsage
	}
	return emitAnthropicUsage(result, onEvent)
}

func applyAnthropicContentBlockStart(
	parsed map[string]interface{},
	rawBody string,
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
	classifier anthropicToolClassifier,
) error {
	block := asMap(parsed["content_block"])
	index := anthropicContentBlockIndex(parsed)
	switch strings.TrimSpace(getString(block[valueTypeF2B37301])) {
	case valueToolUse6527A599:
		return applyAnthropicToolUseBlockStart(block, index, rawBody, result, onEvent, classifier)
	case "server_tool_use":
		return applyAnthropicServerToolUseBlockStart(block, index, result, onEvent)
	default:
		return applyAnthropicServerToolResultBlockStart(block, result, onEvent)
	}
}

func applyAnthropicToolUseBlockStart(
	block map[string]interface{},
	index int,
	rawBody string,
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
	classifier anthropicToolClassifier,
) error {
	toolName := strings.TrimSpace(getString(block[valueNameBEEAEB35]))
	if classifier.isUnsupportedNativeClientToolUse(toolName) {
		return rejectAnthropicNativeClientToolUse(block, index, toolName, rawBody, result, onEvent)
	}
	upsertAnthropicStreamToolCall(result, index, ToolCall{
		ToolCallID:    strings.TrimSpace(getString(block["id"])),
		ToolType:      valueFunctionD9DEDB9D,
		ToolName:      toolName,
		ArgumentsJSON: normalizeJSONString(block[valueInputF8378501]),
		Status:        valueRequested2551EBD8,
	})
	return nil
}

func rejectAnthropicNativeClientToolUse(
	block map[string]interface{},
	index int,
	toolName string,
	rawBody string,
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	message := anthropicUnsupportedNativeToolMessage(toolName)
	toolCall := upsertAnthropicStreamServerToolCall(result, index, ToolCall{
		ToolCallID:    strings.TrimSpace(getString(block["id"])),
		ToolType:      "native_tool_use",
		ToolName:      toolName,
		ArgumentsJSON: normalizeJSONString(block[valueInputF8378501]),
		Status:        valueErrorAE1827EC,
		ErrorJSON:     message,
	})
	if err := emitAnthropicServerToolCall(result, onEvent, toolCall); err != nil {
		return err
	}
	return anthropicUnsupportedNativeToolError(toolName, rawBody)
}

func applyAnthropicServerToolUseBlockStart(
	block map[string]interface{},
	index int,
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	toolCall := upsertAnthropicStreamServerToolCall(result, index, ToolCall{
		ToolCallID:    strings.TrimSpace(getString(block["id"])),
		ToolType:      strings.TrimSpace(getString(block[valueTypeF2B37301])),
		ToolName:      strings.TrimSpace(getString(block[valueNameBEEAEB35])),
		ArgumentsJSON: normalizeJSONString(block[valueInputF8378501]),
		Status:        valueInProgressFCF26CA0,
	})
	return emitAnthropicServerToolCall(result, onEvent, toolCall)
}

func applyAnthropicServerToolResultBlockStart(
	block map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	if !isAnthropicServerToolResultBlockType(strings.TrimSpace(getString(block[valueTypeF2B37301]))) {
		return nil
	}
	toolCall, ok := mergeAnthropicStreamServerToolResult(result, block)
	if !ok {
		return nil
	}
	return emitAnthropicServerToolCall(result, onEvent, toolCall)
}

func applyAnthropicContentBlockDelta(
	parsed map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	delta := asMap(parsed["delta"])
	switch deltaType := strings.TrimSpace(getString(delta[valueTypeF2B37301])); {
	case deltaType == "text_delta":
		return applyAnthropicTextDelta(delta, result, onEvent)
	case deltaType == "input_json_delta":
		return applyAnthropicInputJSONDelta(parsed, delta, result, onEvent)
	case deltaType == "signature_delta":
		return applyAnthropicSignatureDelta(parsed, delta, result, onEvent)
	case isAnthropicReasoningDeltaType(deltaType):
		return applyAnthropicReasoningDelta(parsed, delta, deltaType, result, onEvent)
	default:
		return nil
	}
}

func applyAnthropicTextDelta(
	delta map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	text := getString(delta[valueText53ED4E15])
	if text == "" {
		return nil
	}
	result.Text += text
	return emitAnthropicTextDelta(result, onEvent, text)
}

func applyAnthropicInputJSONDelta(
	parsed map[string]interface{},
	delta map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	partial := getString(delta["partial_json"])
	if partial == "" {
		return nil
	}
	toolCall := mergeAnthropicInputJSONDelta(result, anthropicContentBlockIndex(parsed), partial)
	return emitAnthropicServerToolCall(result, onEvent, toolCall)
}

func mergeAnthropicInputJSONDelta(result *GenerateOutput, index int, partial string) ToolCall {
	if toolCall, ok := appendAnthropicStreamServerToolInput(result, index, partial); ok {
		return toolCall
	}
	return appendAnthropicStreamToolInput(result, index, partial)
}

func applyAnthropicSignatureDelta(
	parsed map[string]interface{},
	delta map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	signature := strings.TrimSpace(getString(delta["signature"]))
	if signature == "" {
		return nil
	}
	reasoning := anthropicSignatureReasoningDelta(parsed, delta, signature)
	mergeReasoningDeltaOutput(&result.Reasoning, reasoning)
	return emitAnthropicReasoning(result, onEvent, reasoning)
}

func anthropicSignatureReasoningDelta(
	parsed map[string]interface{},
	delta map[string]interface{},
	signature string,
) *ReasoningDelta {
	return &ReasoningDelta{
		EventType: "anthropic.content_block_delta",
		ItemID:    fmt.Sprintf("%v", parsed["index"]),
		Status:    strings.TrimSpace(getString(delta[valueTypeF2B37301])),
		Kind:      "signature",
		Signature: signature,
	}
}

func isAnthropicReasoningDeltaType(deltaType string) bool {
	return strings.Contains(deltaType, "thinking") || strings.Contains(deltaType, "reason")
}

func applyAnthropicReasoningDelta(
	parsed map[string]interface{},
	delta map[string]interface{},
	deltaType string,
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	think := extractReasoningDeltaText(delta)
	if think == "" {
		return nil
	}
	reasoning := &ReasoningDelta{
		EventType: "anthropic.content_block_delta",
		ItemID:    fmt.Sprintf("%v", parsed["index"]),
		Status:    deltaType,
		Kind:      valueContentTextE041C189,
		Text:      think,
	}
	mergeReasoningDeltaOutput(&result.Reasoning, reasoning)
	return emitAnthropicReasoning(result, onEvent, reasoning)
}

func applyAnthropicMessageDelta(
	parsed map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	delta := asMap(parsed["delta"])
	if stopReason := strings.TrimSpace(getString(delta["stop_reason"])); stopReason != "" {
		result.StopReason = stopReason
	}
	if stopSequence := strings.TrimSpace(getString(delta["stop_sequence"])); stopSequence != "" {
		result.StopSequence = stopSequence
	}
	if serverSideToolUsage := parseAnthropicServerSideToolUsage(parsed); len(serverSideToolUsage) > 0 {
		result.ServerSideToolUsage = serverSideToolUsage
	}
	deltaUsage := asMap(parsed["usage"])
	if len(deltaUsage) == 0 {
		return nil
	}
	mergeAnthropicUsage(&result.Usage, deltaUsage)
	result.Usage.RawUsageJSON = MergeRawUsageJSON(result.Usage.RawUsageJSON, rawUsageJSONFromPath(parsed, "usage"))
	return emitAnthropicUsage(result, onEvent)
}

func mergeAnthropicUsage(usage *Usage, deltaUsage map[string]interface{}) {
	// Usage frames are cumulative, sparse snapshots. A final frame can revise
	// input/cache totals; an omitted field preserves its earlier value, while an
	// explicitly reported zero replaces it. Never add cumulative frames.
	for key, destination := range map[string]*int64{
		anthropicInputTokensKey: &usage.InputTokens, "output_tokens": &usage.OutputTokens,
		"cache_read_input_tokens": &usage.CacheReadTokens,
	} {
		if value, present := deltaUsage[key]; present {
			*destination = toInt64(value)
		}
	}
	creation := asMap(deltaUsage["cache_creation"])
	for key, destination := range map[string]*int64{
		"ephemeral_5m_input_tokens": &usage.CacheWrite5mTokens,
		"ephemeral_1h_input_tokens": &usage.CacheWrite1hTokens,
	} {
		if value, present := creation[key]; present {
			*destination = toInt64(value)
		}
	}
	if value, present := deltaUsage["cache_creation_input_tokens"]; present {
		usage.CacheWriteTokens = toInt64(value)
	} else if len(creation) != 0 {
		usage.CacheWriteTokens = usage.CacheWrite5mTokens + usage.CacheWrite1hTokens
	}
	if value, present := deltaUsage["speed"]; present {
		usage.Speed = strings.TrimSpace(getString(value))
	}
}

func anthropicCompletionError(result *GenerateOutput) error {
	if result == nil {
		return nil
	}
	switch strings.TrimSpace(result.StopReason) {
	case valueMaxTokens8DDF2E0E, "model_context_window_exceeded":
		return &UpstreamError{
			StatusCode: http.StatusOK,
			Message:    "anthropic response truncated: stop_reason=" + result.StopReason,
		}
	default:
		return nil
	}
}

func emitAnthropicUsage(result *GenerateOutput, onEvent func(GenerateStreamEvent) error) error {
	if result.Usage == (Usage{}) || onEvent == nil {
		return nil
	}
	return onEvent(GenerateStreamEvent{
		Usage:      result.Usage,
		ResponseID: result.ResponseID,
	})
}

func emitAnthropicTextDelta(
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
	text string,
) error {
	if onEvent == nil {
		return nil
	}
	return onEvent(GenerateStreamEvent{
		Delta:      text,
		ResponseID: result.ResponseID,
	})
}

func emitAnthropicServerToolCall(
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
	toolCall ToolCall,
) error {
	if onEvent == nil {
		return nil
	}
	return onEvent(GenerateStreamEvent{
		ServerToolCall: &toolCall,
		ResponseID:     result.ResponseID,
	})
}

func emitAnthropicReasoning(
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

func anthropicStreamError(parsed map[string]interface{}, rawBody string) error {
	errBlock := asMap(parsed[valueErrorAE1827EC])
	msg := strings.TrimSpace(getString(errBlock["message"]))
	if msg == "" {
		msg = "anthropic stream error"
	}
	return &UpstreamError{
		StatusCode: http.StatusBadGateway,
		Message:    msg,
		Body:       rawBody,
	}
}

func anthropicContentBlockIndex(parsed map[string]interface{}) int {
	index := int(toInt64(parsed["index"]))
	if index < 0 {
		return 0
	}
	return index
}

func upsertAnthropicStreamToolCall(result *GenerateOutput, index int, item ToolCall) {
	if result == nil {
		return
	}
	if result.ToolCalls == nil {
		result.ToolCalls = make([]ToolCall, 0)
	}
	for len(result.ToolCalls) <= index {
		result.ToolCalls = append(result.ToolCalls, ToolCall{Status: valueRequested2551EBD8})
	}
	current := result.ToolCalls[index]
	if strings.TrimSpace(item.ToolCallID) != "" {
		current.ToolCallID = item.ToolCallID
	}
	if strings.TrimSpace(item.ToolType) != "" {
		current.ToolType = item.ToolType
	}
	if strings.TrimSpace(item.ToolName) != "" {
		current.ToolName = item.ToolName
	}
	if strings.TrimSpace(item.ArgumentsJSON) != "" && item.ArgumentsJSON != "{}" {
		current.ArgumentsJSON = item.ArgumentsJSON
	}
	if strings.TrimSpace(item.Status) != "" {
		current.Status = item.Status
	}
	result.ToolCalls[index] = current
}

func appendAnthropicStreamToolInput(result *GenerateOutput, index int, partial string) ToolCall {
	if result == nil || partial == "" {
		return ToolCall{}
	}
	upsertAnthropicStreamToolCall(result, index, ToolCall{ToolType: valueFunctionD9DEDB9D, Status: valueRequested2551EBD8})
	result.ToolCalls[index].ArgumentsJSON += partial
	return result.ToolCalls[index]
}

func markAnthropicStreamToolCallComplete(result *GenerateOutput, index int) (ToolCall, bool) {
	if result == nil || index < 0 || index >= len(result.ToolCalls) {
		return ToolCall{}, false
	}
	if strings.TrimSpace(result.ToolCalls[index].ToolName) == "" && strings.TrimSpace(result.ToolCalls[index].ToolCallID) == "" {
		return ToolCall{}, false
	}
	if strings.TrimSpace(result.ToolCalls[index].ArgumentsJSON) == "" {
		result.ToolCalls[index].ArgumentsJSON = "{}"
	}
	if strings.TrimSpace(result.ToolCalls[index].Status) == "" {
		result.ToolCalls[index].Status = valueRequested2551EBD8
	}
	return result.ToolCalls[index], true
}

func upsertAnthropicStreamServerToolCall(result *GenerateOutput, index int, item ToolCall) ToolCall {
	if result == nil {
		return item
	}
	if result.ServerToolCalls == nil {
		result.ServerToolCalls = make([]ToolCall, 0)
	}
	for len(result.ServerToolCalls) <= index {
		result.ServerToolCalls = append(result.ServerToolCalls, ToolCall{Status: valueInProgressFCF26CA0})
	}
	current := result.ServerToolCalls[index]
	if strings.TrimSpace(item.ToolCallID) != "" {
		current.ToolCallID = item.ToolCallID
	}
	if strings.TrimSpace(item.ToolType) != "" {
		current.ToolType = item.ToolType
	}
	if strings.TrimSpace(item.ToolName) != "" {
		current.ToolName = item.ToolName
	}
	if strings.TrimSpace(item.ArgumentsJSON) != "" && item.ArgumentsJSON != "{}" {
		current.ArgumentsJSON = item.ArgumentsJSON
	}
	if strings.TrimSpace(item.Status) != "" {
		current.Status = item.Status
	}
	result.ServerToolCalls[index] = current
	return current
}

func appendAnthropicStreamServerToolInput(result *GenerateOutput, index int, partial string) (ToolCall, bool) {
	if result == nil || partial == "" || index < 0 || index >= len(result.ServerToolCalls) {
		return ToolCall{}, false
	}
	item := result.ServerToolCalls[index]
	if strings.TrimSpace(item.ToolCallID) == "" && strings.TrimSpace(item.ToolName) == "" && strings.TrimSpace(item.ToolType) == "" {
		return ToolCall{}, false
	}
	item.ArgumentsJSON += partial
	if strings.TrimSpace(item.Status) == "" {
		item.Status = valueInProgressFCF26CA0
	}
	result.ServerToolCalls[index] = item
	return item, true
}

func mergeAnthropicStreamServerToolResult(result *GenerateOutput, block map[string]interface{}) (ToolCall, bool) {
	if result == nil {
		return ToolCall{}, false
	}
	toolUseID := strings.TrimSpace(getString(block["tool_use_id"]))
	if toolUseID == "" {
		return ToolCall{}, false
	}
	for index := range result.ServerToolCalls {
		item := result.ServerToolCalls[index]
		if strings.TrimSpace(item.ToolCallID) != toolUseID {
			continue
		}
		item.OutputJSON = anthropicServerToolResultOutputJSON(block)
		if item.OutputJSON == "" {
			item.OutputJSON = "{}"
		}
		item.Status = valueCompletedFC6E652A
		result.ServerToolCalls[index] = item
		return item, true
	}
	return ToolCall{}, false
}

func anthropicServerToolResultOutputJSON(block map[string]interface{}) string {
	return normalizeJSONString(sanitizeAnthropicServerToolResultValue(block))
}

func sanitizeAnthropicServerToolResultValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			if isAnthropicOpaqueToolResultKey(key) {
				continue
			}
			result[key] = sanitizeAnthropicServerToolResultValue(item)
		}
		return result
	case []interface{}:
		items := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			items = append(items, sanitizeAnthropicServerToolResultValue(item))
		}
		return items
	default:
		return value
	}
}

func isAnthropicOpaqueToolResultKey(key string) bool {
	switch strings.TrimSpace(key) {
	case "encrypted_content", "encryptedContent":
		return true
	default:
		return false
	}
}

func compactAnthropicStreamToolCalls(result *GenerateOutput) {
	if result == nil || len(result.ToolCalls) == 0 {
		compactAnthropicStreamServerToolCalls(result)
		return
	}
	items := make([]ToolCall, 0, len(result.ToolCalls))
	for _, item := range result.ToolCalls {
		if strings.TrimSpace(item.ToolCallID) == "" && strings.TrimSpace(item.ToolName) == "" {
			continue
		}
		if strings.TrimSpace(item.ArgumentsJSON) == "" {
			item.ArgumentsJSON = "{}"
		}
		if strings.TrimSpace(item.ToolType) == "" {
			item.ToolType = valueFunctionD9DEDB9D
		}
		if strings.TrimSpace(item.Status) == "" {
			item.Status = valueRequested2551EBD8
		}
		items = append(items, item)
	}
	result.ToolCalls = items
	compactAnthropicStreamServerToolCalls(result)
}

func compactAnthropicStreamServerToolCalls(result *GenerateOutput) {
	if result == nil || len(result.ServerToolCalls) == 0 {
		return
	}
	items := make([]ToolCall, 0, len(result.ServerToolCalls))
	for _, item := range result.ServerToolCalls {
		if strings.TrimSpace(item.ToolCallID) == "" && strings.TrimSpace(item.ToolName) == "" && strings.TrimSpace(item.ToolType) == "" {
			continue
		}
		if strings.TrimSpace(item.ArgumentsJSON) == "" {
			item.ArgumentsJSON = "{}"
		}
		if strings.TrimSpace(item.Status) == "" {
			item.Status = valueInProgressFCF26CA0
		}
		items = append(items, item)
	}
	result.ServerToolCalls = items
}

// ── Models 目录 ───────────────────────────────────────────────────────────────

// listModelsAnthropic 调用 Anthropic GET /v1/models 接口。
//
// 响应格式：
//
//	{"data":[{"id":"claude-opus-4-5","display_name":"...","type":"model"},...]}
func (c *Client) listModelsAnthropic(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	requestURL := buildAnthropicModelsURL(route.BaseURL)
	if requestURL == "" {
		return nil, errCategory2398445EC8
	}

	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()

	req, err := c.newAnthropicRequest(requestCtx, http.MethodGet, requestURL, nil, route)
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
		return nil, parseAnthropicError(resp.StatusCode, body, upstreamDebugSnapshot(req, nil, resp, body))
	}

	parsed := struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}{}
	if err = json.Unmarshal(body, &parsed); err != nil {
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
			OwnedBy: "anthropic",
		})
	}
	return results, nil
}

var (
	errCategory2398445EC8 = errors.New("invalid base url")
)
