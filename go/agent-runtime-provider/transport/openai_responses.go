package transport

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	valueSources6CEF08E9   = "sources"
	valueWebSearch29553E49 = "web_search"
)

const (
	valueArgumentsEF974DA2 = "arguments"
	valueAssistant598CDD53 = "assistant"
	valueCallIdFD3C070D    =

	// openAIResponsesAdapter 实现 OpenAI Responses API（POST /v1/responses）。
	"call_id"
	valueCitations47439605                  = "citations"
	valueCodeInterpreterEE1C409F            = "code_interpreter"
	valueContent95A71030                    = "content"
	valueContentText122E4760                = "content_text"
	valueContents80305C22                   = "contents"
	valueCustomToolCall09C0F085             = "custom_tool_call"
	valueFunction24B5C282                   = "function"
	valueFunctionCall1DF305D6               = "function_call"
	valueFunctionCallOutput9A6E5C46         = "function_call_output"
	valueImageJpegE9507F70                  = "image/jpeg"
	valueImageUrl576B81CD                   = "image_url"
	valueInclude49007A03                    = "include"
	valueInput53CC344B                      = "input"
	valueInputTextD2589EBB                  = "input_text"
	valueMessages831844B9                   = "messages"
	valueModel252459E2                      = "model"
	valueNameC25F4D03                       = "name"
	valueOutputAD4FEFDA                     = "output"
	valueOutputTextFDC62B9B                 = "output_text"
	valueReasoning506E158B                  = "reasoning"
	valueReasoningEncryptedContentEDEC5D3E  = "reasoning.encrypted_content"
	valueRequested53D2A287                  = "requested"
	valueResponseFormat00805748             = "response_format"
	valueRole9834B164                       = "role"
	valueStream092D3276                     = "stream"
	valueStreamOptions8DC87B09              = "stream_options"
	valueSummaryText2108AD9C                = "summary_text"
	valueSystemE9E56F73                     = "system"
	valueSystemInstruction8281F7D2          = "systemInstruction"
	valueText239B340C                       = "text"
	valueTools2B19537D                      = "tools"
	valueType1F400FCE                       = "type"
	valueWebSearchCallActionSources18134759 = "web_search_call.action.sources"
)

type openAIResponsesAdapter struct {
	client *Client
}

func upsertOpenAIResponsesReplayItem(raw string, outputIndex int, item map[string]interface{}) string {
	if outputIndex < 0 || len(item) == 0 {
		return raw
	}
	existing := decodeProviderReplayItems(raw, AdapterOpenAIResponses)
	if outputIndex > len(existing) {
		return raw
	}
	if outputIndex == len(existing) {
		existing = append(existing, cloneMap(item))
	} else {
		existing[outputIndex] = cloneMap(item)
	}
	return encodeProviderReplayItems(AdapterOpenAIResponses, providerReplayItemsAsInterfaces(existing))
}

func parseResponseServerToolCallAt(item map[string]interface{}, outputIndex interface{}) ToolCall {
	call := parseResponseServerToolCall(item)
	if strings.TrimSpace(call.ToolCallID) != "" || outputIndex == nil {
		return call
	}
	index := toInt64(outputIndex)
	if index < 0 {
		return call
	}
	call.ToolCallID = fmt.Sprintf("gaoge_server_tool:%s:%d", strings.TrimSpace(call.ToolType), index)
	return call
}

func applyResponsesNativeToolChoice(
	payload map[string]interface{},
	adapter string,
	model string,
	input GenerateInput,
	toolsEnabled bool,
) {
	// DeepSeek V4 Responses runs in thinking mode and rejects an explicit
	// tool_choice=required request. The runtime still supplies the full tool set
	// and the corrective instruction; leaving tool choice implicit lets the
	// model perform the requested tool call without turning a recoverable
	// invalid-arguments retry into a provider 400.
	if NormalizeAdapter(adapter) == AdapterOpenAIResponses &&
		strings.Contains(strings.ToLower(strings.TrimSpace(model)), "deepseek-v4") &&
		strings.EqualFold(strings.TrimSpace(string(input.ToolChoice.Mode)), string(ToolChoiceRequired)) {
		return
	}
	applyNativeToolChoice(payload, adapter, input, toolsEnabled)
}

func appendResponsesGeneratedImages(result *GenerateOutput, item map[string]interface{}) {
	if result == nil || strings.TrimSpace(getString(item[valueType1F400FCE])) != "image_generation_call" {
		return
	}
	image, ok := responseGeneratedImage(item)
	if !ok {
		return
	}
	for _, current := range result.GeneratedImages {
		if image.B64JSON != "" && current.B64JSON == image.B64JSON {
			return
		}
		if image.URL != "" && current.URL == image.URL {
			return
		}
	}
	result.GeneratedImages = append(result.GeneratedImages, image)
}

func responseGeneratedImage(item map[string]interface{}) (GeneratedImage, bool) {
	resultValue := item["result"]
	mediaType := firstNonEmptyString(
		getString(item["mime_type"]),
		imageMIMETypeFromFormat(getString(item["output_format"])),
	)
	if raw, ok := resultValue.(string); ok {
		return responseGeneratedImageString(raw, mediaType)
	}
	return responseGeneratedImageObject(asMap(resultValue), mediaType)
}

func responseGeneratedImageString(raw string, mediaType string) (GeneratedImage, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return GeneratedImage{}, false
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return GeneratedImage{URL: raw, MIMEType: mediaType}, true
	}
	if mediaType == "" {
		mediaType = valueImagePng415383B9
	}
	return GeneratedImage{B64JSON: raw, MIMEType: mediaType}, true
}

func responseGeneratedImageObject(value map[string]interface{}, mediaType string) (GeneratedImage, bool) {
	if len(value) == 0 {
		return GeneratedImage{}, false
	}
	if mediaType == "" {
		mediaType = firstNonEmptyString(
			getString(value["mime_type"]),
			imageMIMETypeFromFormat(getString(value["output_format"])),
		)
	}
	image := GeneratedImage{
		URL:           strings.TrimSpace(getString(value["url"])),
		B64JSON:       firstNonEmptyString(getString(value["b64_json"]), getString(value["base64"])),
		MIMEType:      mediaType,
		RevisedPrompt: strings.TrimSpace(getString(value["revised_prompt"])),
	}
	if image.B64JSON != "" && image.MIMEType == "" {
		image.MIMEType = valueImagePng415383B9
	}
	return image, image.URL != "" || image.B64JSON != ""
}

func imageMIMETypeFromFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "png":
		return valueImagePng415383B9
	case "jpeg", "jpg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}

func (a *openAIResponsesAdapter) Name() string { return AdapterOpenAIResponses }

func (a *openAIResponsesAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	route.Endpoint = EndpointResponses
	return a.client.generateOpenAICompatible(ctx, route, input)
}

func (a *openAIResponsesAdapter) GenerateStream(ctx context.Context, route RouteConfig, input GenerateInput, onEvent func(GenerateStreamEvent) error) (*GenerateOutput, error) {
	route.Endpoint = EndpointResponses
	return a.client.generateStreamOpenAICompatible(ctx, route, input, onEvent)
}

func (a *openAIResponsesAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	return a.client.listModelsOpenAICompatible(ctx, route)
}

func buildResponsesRequestBody(
	adapter string,
	model string,
	input GenerateInput,
	messages []Message,
	providerTools []map[string]interface{},
	toolDefinitions []ToolDefinition,
	toolsEnabled bool,
	providerStreamOptions map[string]interface{},
	stream bool,
) map[string]interface{} {
	if adapter == AdapterOpenRouterResponses {
		return buildOpenRouterResponsesRequestBody(model, input, messages, providerTools, toolDefinitions, providerStreamOptions, stream)
	}
	items := buildResponsesAPIInput(messages)
	payload := map[string]interface{}{
		valueModel252459E2:  strings.TrimSpace(model),
		valueInput53CC344B:  items,
		valueStream092D3276: stream,
	}
	applyResponsesRequestScalarParams(payload, adapter, input)
	applyOpenAICompatibleSamplingParams(payload, input.Options, false)
	applyOpenAIResponsesReasoningParams(payload, input.Options)
	applyOpenAIResponsesTextParams(payload, input.Options, adapter == AdapterOpenAIResponses)
	webSearchTools := responsesWebSearchTools(adapter, toolsEnabled, input.Options)
	nativeTools := responsesNativeTools(providerTools, webSearchTools)
	appendToolDeclarations(payload, providerTools, webSearchTools, buildOpenAITools(toolDefinitions, false))
	applyResponsesNativeToolChoice(payload, adapter, model, input, len(toolDefinitions) > 0 || len(nativeTools) > 0)
	applyResponsesStatefulParams(payload, input)
	applyResponsesStreamParams(payload, providerStreamOptions, stream)
	applyProviderOptions(payload, input.Options, responsesProtectedProviderOptionKeys(adapter, strings.TrimSpace(input.Instructions) != "")...)
	applyResponsesIncludeParams(payload, adapter, input.Options, stream, nativeTools)
	return payload
}

func applyResponsesRequestScalarParams(payload map[string]interface{}, adapter string, input GenerateInput) {
	if instructions := strings.TrimSpace(input.Instructions); adapter == AdapterOpenAIResponses && instructions != "" {
		payload["instructions"] = instructions
	}
	if maxTokens := modelParamInt(input.Options, "max_output_tokens"); maxTokens > 0 {
		payload["max_output_tokens"] = maxTokens
	}
	if retention := normalizePromptCacheRetention(modelParamString(input.Options, "prompt_cache_retention")); retention != "" {
		payload["prompt_cache_retention"] = retention
	}
}

func responsesWebSearchTools(adapter string, toolsEnabled bool, options map[string]interface{}) []map[string]interface{} {
	if toolsEnabled && modelParamBool(options, valueWebSearch29553E49) && adapter == AdapterOpenAIResponses {
		return []map[string]interface{}{{valueType1F400FCE: valueWebSearch29553E49}}
	}
	return nil
}

func responsesNativeTools(providerTools []map[string]interface{}, webSearchTools []map[string]interface{}) []map[string]interface{} {
	nativeTools := append([]map[string]interface{}{}, providerTools...)
	return append(nativeTools, webSearchTools...)
}

func applyResponsesStatefulParams(payload map[string]interface{}, input GenerateInput) {
	if prevID := strings.TrimSpace(input.PreviousResponseID); prevID != "" {
		payload["previous_response_id"] = prevID
	}
}

func applyResponsesStreamParams(payload map[string]interface{}, providerStreamOptions map[string]interface{}, stream bool) {
	if streamOptions := responsesStreamOptions(providerStreamOptions); stream && len(streamOptions) > 0 {
		payload[valueStreamOptions8DC87B09] = streamOptions
	}
}

func applyResponsesIncludeParams(
	payload map[string]interface{},
	adapter string,
	options map[string]interface{},
	stream bool,
	nativeTools []map[string]interface{},
) {
	if supportsResponsesIncludeDefaults(adapter) {
		defaultIncludes := responsesDefaultIncludeValues(adapter, stream, nativeTools)
		appendResponseInclude(payload, responseIncludeValues(options, defaultIncludes...)...)
		return
	}
	appendResponseInclude(payload, responseIncludeValues(options)...)
}

func responsesProtectedProviderOptionKeys(adapter string, hasManagedInstructions bool) []string {
	keys := []string{
		valueContents80305C22,
		valueInclude49007A03,
		valueInput53CC344B,
		valueMessages831844B9,
		"max_completion_tokens",
		valueMaxTokens8DDF2E0E,
		valueModel252459E2,
		"previous_response_id",
		valueReasoning506E158B,
		valueResponseFormat00805748,
		valueStream092D3276,
		valueStreamOptions8DC87B09,
		valueSystemE9E56F73,
		valueSystemInstruction8281F7D2,
		valueText239B340C,
		valueTools2B19537D,
	}
	if adapter != AdapterOpenAIResponses {
		keys = append(keys, "instructions", "metadata", "prompt")
	} else if hasManagedInstructions {
		keys = append(keys, "instructions")
	}
	return keys
}

func responsesStreamOptions(options map[string]interface{}) map[string]interface{} {
	if len(options) == 0 {
		return nil
	}
	result := map[string]interface{}{}
	if value, ok := options["include_obfuscation"]; ok {
		result["include_obfuscation"] = value
	}
	return result
}

func applyOpenAIResponsesReasoningParams(payload map[string]interface{}, options map[string]interface{}) {
	reasoning := map[string]interface{}{}
	if existing := modelParamMap(options, valueReasoning506E158B); len(existing) > 0 {
		for key, value := range existing {
			reasoning[key] = value
		}
	}
	if effort := modelParamString(options, "reasoning_effort"); effort != "" {
		reasoning["effort"] = effort
	}
	if summary := modelParamString(options, "reasoning_summary"); summary != "" {
		reasoning["summary"] = summary
	}
	mergeObjectParam(payload, valueReasoning506E158B, reasoning)
}

func applyOpenAIResponsesTextParams(payload map[string]interface{}, options map[string]interface{}, allowVerbosity bool) {
	text := map[string]interface{}{}
	if existing := modelParamMap(options, valueText239B340C); len(existing) > 0 {
		for key, value := range existing {
			text[key] = value
		}
	}
	if format, ok := normalizedJSONResponseFormat(options); ok {
		text["format"] = format
	}
	if verbosity := modelParamString(options, "verbosity"); verbosity != "" && allowVerbosity {
		text["verbosity"] = verbosity
	}
	mergeObjectParam(payload, valueText239B340C, text)
}

type responsesProtocolExtension struct {
	matchesAdapter                  func(adapter string) bool
	includeDefaults                 func(stream bool, tools []map[string]interface{}) []string
	serverToolIdentifierKeys        func() []string
	serverToolCallID                func(item map[string]interface{}, itemType string) (string, bool)
	isServerToolCallItem            func(item map[string]interface{}) bool
	isServerToolCallType            func(itemType string) bool
	normalizeServerSideToolUsageKey func(value string, original string) (string, bool)
}

func supportsResponsesIncludeDefaults(adapter string) bool {
	return adapter == AdapterOpenAIResponses || len(responsesProtocolExtensionsForAdapter(adapter)) > 0
}

func responsesDefaultIncludeValues(adapter string, stream bool, providerTools []map[string]interface{}) []string {
	values := []string{valueReasoningEncryptedContentEDEC5D3E}
	if adapter == AdapterOpenAIResponses {
		values = append(values, openAIResponsesDefaultIncludeValues(providerTools)...)
	}
	for _, extension := range responsesProtocolExtensionsForAdapter(adapter) {
		if extension.includeDefaults != nil {
			values = append(values, extension.includeDefaults(stream, providerTools)...)
		}
	}
	return appendUniqueStrings(nil, values...)
}

func openAIResponsesDefaultIncludeValues(tools []map[string]interface{}) []string {
	if !responsesToolsIncludeType(tools, valueWebSearch29553E49) {
		return nil
	}
	return []string{valueWebSearchCallActionSources18134759}
}

func responsesToolsIncludeType(tools []map[string]interface{}, toolType string) bool {
	expected := strings.TrimSpace(toolType)
	if expected == "" {
		return false
	}
	for _, tool := range tools {
		if strings.TrimSpace(getString(tool[valueType1F400FCE])) == expected {
			return true
		}
	}
	return false
}

func buildResponsesAPIInput(messages []Message) []map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(messages))
	for _, msg := range messages {
		items = appendResponsesAPIMessageItems(items, msg)
	}
	return items
}

func appendResponsesAPIMessageItems(items []map[string]interface{}, msg Message) []map[string]interface{} {
	if normalizeRole(msg.Role) == valueAssistant598CDD53 {
		if replay := decodeProviderReplayItems(msg.ReplayStateJSON, AdapterOpenAIResponses); len(replay) > 0 {
			return append(items, replay...)
		}
	}
	if len(msg.ToolCalls) > 0 {
		for _, item := range msg.ToolCalls {
			args := strings.TrimSpace(item.ArgumentsJSON)
			if args == "" {
				args = "{}"
			}
			items = append(items, map[string]interface{}{
				valueType1F400FCE:      valueFunctionCall1DF305D6,
				valueCallIdFD3C070D:    strings.TrimSpace(item.ToolCallID),
				valueNameC25F4D03:      strings.TrimSpace(item.ToolName),
				valueArgumentsEF974DA2: args,
			})
		}
		return items
	}
	if len(msg.ToolResults) > 0 {
		for _, item := range msg.ToolResults {
			items = append(items, map[string]interface{}{
				valueType1F400FCE:   valueFunctionCallOutput9A6E5C46,
				valueCallIdFD3C070D: strings.TrimSpace(item.ToolCallID),
				valueOutputAD4FEFDA: buildToolResultContent(item),
			})
		}
		return items
	}
	return append(items, map[string]interface{}{
		valueRole9834B164:    normalizeRole(msg.Role),
		valueContent95A71030: buildResponsesAPIContent(msg),
	})
}

// buildResponsesAPIContent 将消息内容序列化为 Responses API 格式（content 数组）。
func buildResponsesAPIContent(msg Message) []map[string]interface{} {
	textType := responsesTextContentType(msg.Role)
	parts := make([]map[string]interface{}, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		if content := buildResponsesAPIContentPart(part, msg.Role, textType); content != nil {
			parts = append(parts, content)
		}
	}
	if len(parts) == 0 {
		return []map[string]interface{}{
			{valueType1F400FCE: textType, valueText239B340C: msg.Content},
		}
	}
	return parts
}

func buildResponsesAPIContentPart(part ContentPart, role, textType string) map[string]interface{} {
	if part.Kind == ContentPartFile && len(part.Data) > 0 {
		return map[string]interface{}{
			valueType1F400FCE: "input_file", "filename": part.FileName, "file_data": nativeFileDataURL(part),
		}
	}
	switch part.Kind {
	case ContentPartVideo:
		return map[string]interface{}{
			valueType1F400FCE: "input_video", nativeVideoURLKey: nativeVideoDataURL(part),
		}
	case ContentPartImage:
		if normalizeRole(role) == valueAssistant598CDD53 || len(part.Data) == 0 {
			return nil
		}
		return responsesInputImageContent(part)
	default: // text, file
		if strings.TrimSpace(part.Text) == "" {
			return nil
		}
		return map[string]interface{}{
			valueType1F400FCE: textType,
			valueText239B340C: part.Text,
		}
	}
}

func responsesInputImageContent(part ContentPart) map[string]interface{} {
	mimeType := strings.TrimSpace(part.MimeType)
	if mimeType == "" {
		mimeType = valueImageJpegE9507F70
	}
	return map[string]interface{}{
		valueType1F400FCE:     "input_image",
		valueImageUrl576B81CD: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(part.Data),
	}
}

func responsesTextContentType(role string) string {
	if normalizeRole(role) == valueAssistant598CDD53 {
		return valueOutputTextFDC62B9B
	}
	return valueInputTextD2589EBB
}

func applyResponsesStreamEvent(
	adapter string,
	eventName string,
	parsed map[string]interface{},
	rawBody string,
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	eventType := strings.TrimSpace(getString(parsed[valueType1F400FCE]))
	if eventType == "" {
		eventType = strings.TrimSpace(eventName)
	}

	if handled, err := applyResponsesServerToolStatusEvent(eventType, parsed, result, onEvent); handled {
		return err
	}
	if handler, ok := responsesStreamNoErrorEventHandlers[eventType]; ok {
		handler(responsesStreamEventContext{
			adapter:   adapter,
			eventType: eventType,
			parsed:    parsed,
			rawBody:   rawBody,
			result:    result,
			onEvent:   onEvent,
		})
		return nil
	}
	if handler, ok := responsesStreamEventHandlers[eventType]; ok {
		return handler(responsesStreamEventContext{
			adapter:   adapter,
			eventType: eventType,
			parsed:    parsed,
			rawBody:   rawBody,
			result:    result,
			onEvent:   onEvent,
		})
	}
	return nil
}

type responsesStreamEventContext struct {
	adapter   string
	eventType string
	parsed    map[string]interface{}
	rawBody   string
	result    *GenerateOutput
	onEvent   func(GenerateStreamEvent) error
}

var responsesStreamNoErrorEventHandlers = map[string]func(responsesStreamEventContext){
	"response.created":                     handleResponsesCreatedStreamEvent,
	"response.reasoning_summary_text.done": handleResponsesReasoningDoneStreamEvent,
	"response.reasoning_text.done":         handleResponsesReasoningDoneStreamEvent,
	"response.thinking.done":               handleResponsesReasoningDoneStreamEvent,
}

var responsesStreamEventHandlers = map[string]func(responsesStreamEventContext) error{
	"response.output_text.done":              handleResponsesOutputTextDoneStreamEvent,
	"response.refusal.done":                  handleResponsesRefusalDoneStreamEvent,
	"response.output_item.added":             handleResponsesOutputItemStreamEvent,
	"response.output_item.in_progress":       handleResponsesOutputItemStreamEvent,
	"response.output_item.done":              handleResponsesOutputItemStreamEvent,
	"response.function_call_arguments.delta": handleResponsesFunctionCallArgumentsStreamEvent,
	"response.function_call_arguments.done":  handleResponsesFunctionCallArgumentsStreamEvent,
	"response.custom_tool_call_input.delta":  handleResponsesCustomToolInputStreamEvent,
	"response.custom_tool_call_input.done":   handleResponsesCustomToolInputStreamEvent,
	"response.output_text.delta":             handleResponsesTextDeltaStreamEvent,
	"response.refusal.delta":                 handleResponsesTextDeltaStreamEvent,
	"response.reasoning_summary_text.delta":  handleResponsesReasoningDeltaStreamEvent,
	"response.reasoning_text.delta":          handleResponsesReasoningDeltaStreamEvent,
	"response.thinking.delta":                handleResponsesReasoningDeltaStreamEvent,
	"response.completed":                     handleResponsesCompletedStreamEvent,
	"response.failed":                        handleResponsesErrorStreamEvent,
	"response.error":                         handleResponsesErrorStreamEvent,
}

func handleResponsesCreatedStreamEvent(ctx responsesStreamEventContext) {
	applyResponsesCreatedEvent(ctx.parsed, ctx.result)
}

func handleResponsesOutputItemStreamEvent(ctx responsesStreamEventContext) error {
	index, err := streamIndexField(ctx.parsed, "output_index")
	if err != nil {
		return err
	}
	item := asMap(ctx.parsed["item"])
	if err := rememberResponsesPhase(ctx.result, item, index); err != nil {
		return err
	}
	if err := mergeResponsesStreamOutputItem(ctx.result, item, ctx.parsed["output_index"], ctx.onEvent); err != nil {
		return err
	}
	if ctx.eventType == "response.output_item.done" && NormalizeAdapter(ctx.adapter) == AdapterOpenAIResponses {
		ctx.result.ReplayStateJSON = upsertOpenAIResponsesReplayItem(
			ctx.result.ReplayStateJSON, index, item,
		)
	}
	return nil
}

func handleResponsesCustomToolInputStreamEvent(ctx responsesStreamEventContext) error {
	return mergeResponsesToolInputEvent(ctx.result, ctx.parsed, ctx.onEvent)
}

func handleResponsesFunctionCallArgumentsStreamEvent(ctx responsesStreamEventContext) error {
	return mergeResponsesToolInputEvent(ctx.result, ctx.parsed, ctx.onEvent)
}

func handleResponsesTextDeltaStreamEvent(ctx responsesStreamEventContext) error {
	if ctx.eventType == "response.refusal.delta" {
		ctx.result.Refused = true
	}
	return applyResponsesTextDeltaEvent(ctx.parsed, ctx.result, ctx.onEvent)
}

func handleResponsesOutputTextDoneStreamEvent(ctx responsesStreamEventContext) error {
	return applyResponsesTextDoneEvent(ctx.parsed, ctx.result, valueText239B340C, "delta")
}

func handleResponsesRefusalDoneStreamEvent(ctx responsesStreamEventContext) error {
	ctx.result.Refused = true
	return applyResponsesTextDoneEvent(ctx.parsed, ctx.result, "refusal", valueText239B340C, "delta")
}

func handleResponsesReasoningDeltaStreamEvent(ctx responsesStreamEventContext) error {
	return applyResponsesReasoningDeltaEvent(ctx.eventType, ctx.parsed, ctx.result, ctx.onEvent)
}

func handleResponsesReasoningDoneStreamEvent(ctx responsesStreamEventContext) {
	applyResponsesReasoningDoneEvent(ctx.eventType, ctx.parsed, ctx.result)
}

func handleResponsesCompletedStreamEvent(ctx responsesStreamEventContext) error {
	return applyResponsesCompletedEvent(ctx.adapter, ctx.eventType, ctx.parsed, ctx.result, ctx.onEvent)
}

func handleResponsesErrorStreamEvent(ctx responsesStreamEventContext) error {
	return parseResponsesStreamErrorEvent(ctx.parsed, ctx.rawBody)
}

func applyResponsesServerToolStatusEvent(
	eventType string,
	parsed map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) (bool, error) {
	call, ok := parseResponsesServerToolStatusEvent(eventType, parsed)
	if !ok {
		return false, nil
	}
	appendUniqueToolCall(&result.ServerToolCalls, call)
	return true, emitResponsesServerToolCall(result, onEvent, call)
}

func emitResponsesServerToolCall(
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

func applyResponsesCreatedEvent(parsed map[string]interface{}, result *GenerateOutput) {
	if responseID := strings.TrimSpace(getStringFromPath(parsed, "response", "id")); responseID != "" {
		result.ResponseID = responseID
	}
	if serviceTier := strings.TrimSpace(getStringFromPath(parsed, "response", "service_tier")); serviceTier != "" {
		result.Usage.ServiceTier = serviceTier
	}
}

func applyResponsesTextDeltaEvent(
	parsed map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	phase, err := responsesEventPhase(result, parsed)
	if err != nil {
		return err
	}
	if phase == "commentary" || phase == "pending" {
		return nil
	}
	delta := getString(parsed["delta"])
	if delta == "" {
		return nil
	}
	result.Text += delta
	return emitResponsesTextDelta(result, onEvent, delta)
}

func applyResponsesTextDoneEvent(parsed map[string]interface{}, result *GenerateOutput, keys ...string) error {
	phase, err := responsesEventPhase(result, parsed)
	if err != nil {
		return err
	}
	if phase == "commentary" || phase == "pending" {
		return nil
	}
	text := firstResponsesTextValue(parsed, keys...)
	if text != "" && !strings.Contains(result.Text, text) {
		result.Text += text
	}
	return nil
}

func firstResponsesTextValue(parsed map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if text := getString(parsed[key]); text != "" {
			return text
		}
	}
	return ""
}

func emitResponsesTextDelta(
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
	delta string,
) error {
	if onEvent == nil {
		return nil
	}
	if err := onEvent(GenerateStreamEvent{
		Delta:      delta,
		ResponseID: result.ResponseID,
	}); err != nil {
		return err
	}
	result.responsesEmittedText += delta
	return nil
}

func applyResponsesReasoningDeltaEvent(
	eventType string,
	parsed map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	reasoning := parseResponsesReasoningDelta(eventType, parsed)
	if reasoning == nil || reasoning.Text == "" {
		return nil
	}
	mergeReasoningDeltaOutput(&result.Reasoning, reasoning)
	return emitResponsesReasoning(result, onEvent, reasoning)
}

func applyResponsesReasoningDoneEvent(eventType string, parsed map[string]interface{}, result *GenerateOutput) {
	reasoning := parseResponsesReasoningDone(eventType, parsed)
	if reasoning == nil || reasoning.Text == "" {
		return
	}
	if result.Reasoning == nil || !reasoningOutputContains(result.Reasoning, reasoning.Text) {
		mergeReasoningDeltaOutput(&result.Reasoning, reasoning)
	}
}

func emitResponsesReasoning(
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

func applyResponsesCompletedEvent(
	adapter string,
	eventType string,
	parsed map[string]interface{},
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	output, err := buildGenerateOutputFromParsedForAdapter(EndpointResponses, adapter, asMap(parsed["response"]), false)
	if err != nil {
		return err
	}
	if output.Text != "" {
		if !strings.HasPrefix(output.Text, result.responsesEmittedText) {
			return errInvalidResponsesPhase
		}
		if suffix := strings.TrimPrefix(output.Text, result.responsesEmittedText); suffix != "" {
			if err := emitResponsesTextDelta(result, onEvent, suffix); err != nil {
				return err
			}
		}
	}
	if output.responsesPhased {
		result.Text = output.Text
	}
	if err := emitResponsesCompletedReasoning(eventType, result, output, onEvent); err != nil {
		return err
	}
	mergeGenerateOutput(result, output)
	return emitResponsesCompletedUsage(result, output, onEvent)
}

func emitResponsesCompletedReasoning(
	eventType string,
	result *GenerateOutput,
	output *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	if result.Reasoning != nil || output.Reasoning == nil || onEvent == nil {
		return nil
	}
	text := firstNonEmptyString(output.Reasoning.Text, output.Reasoning.Summary)
	if text == "" {
		return nil
	}
	return onEvent(GenerateStreamEvent{
		Reasoning: &ReasoningDelta{
			EventType:        eventType,
			ItemID:           output.Reasoning.ItemID,
			Status:           output.Reasoning.Status,
			Kind:             valueContentText122E4760,
			Text:             text,
			EncryptedContent: output.Reasoning.EncryptedContent,
		},
		ResponseID: firstNonEmptyString(result.ResponseID, output.ResponseID),
	})
}

func emitResponsesCompletedUsage(
	result *GenerateOutput,
	output *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	if output.Usage == (Usage{}) || onEvent == nil {
		return nil
	}
	return onEvent(GenerateStreamEvent{
		Usage:      output.Usage,
		ResponseID: result.ResponseID,
	})
}

func parseResponsesServerToolStatusEvent(eventType string, parsed map[string]interface{}) (ToolCall, bool) {
	value := strings.TrimSpace(eventType)
	if !strings.HasPrefix(value, "response.") {
		return ToolCall{}, false
	}
	status := ""
	for _, suffix := range []string{".in_progress", ".searching", ".completed", ".failed", ".error"} {
		if strings.HasSuffix(value, suffix) {
			status = strings.TrimPrefix(suffix, ".")
			value = strings.TrimSuffix(strings.TrimPrefix(value, "response."), suffix)
			break
		}
	}
	if status == "" || !isResponsesServerToolCallType(value) {
		return ToolCall{}, false
	}
	item := cloneMap(asMap(parsed["item"]))
	if len(item) == 0 {
		item = make(map[string]interface{})
	}
	mergeMapValueIfEmpty(item, valueType1F400FCE, value)
	mergeMapValueIfEmpty(item, "status", status)
	for _, key := range responseServerToolIdentifierKeys() {
		mergeMapValueIfEmpty(item, key, parsed[key])
	}
	for _, key := range []string{"action", valueArgumentsEF974DA2, valueInput53CC344B, "query", valueOutputAD4FEFDA, "outputs", "results", "search_results", valueSources6CEF08E9, valueCitations47439605, "data", "items", valueContent95A71030, "response", "result", "error"} {
		mergeMapValueIfEmpty(item, key, parsed[key])
	}
	return parseResponseServerToolCallAt(item, parsed["output_index"]), true
}

func responseServerToolIdentifierKeys() []string {
	keys := []string{"item_id", "id", valueCallIdFD3C070D, "tool_call_id"}
	for _, extension := range allResponsesProtocolExtensions() {
		if extension.serverToolIdentifierKeys != nil {
			keys = append(keys, extension.serverToolIdentifierKeys()...)
		}
	}
	return keys
}

func mergeResponsesStreamOutputItem(
	result *GenerateOutput,
	item map[string]interface{},
	outputIndex interface{},
	onEvent func(GenerateStreamEvent) error,
) error {
	if result == nil || len(item) == 0 {
		return nil
	}
	if !isResponsesServerToolCallItem(item) {
		mergeResponsesOutputItem(result, item, false, outputIndex)
		return nil
	}
	call := parseResponseServerToolCallAt(item, outputIndex)
	appendUniqueToolCall(&result.ServerToolCalls, call)
	appendResponsesGeneratedImages(result, item)
	result.Citations = appendUniqueStrings(result.Citations, parseResponseCitations(item)...)
	if onEvent == nil {
		return nil
	}
	return onEvent(GenerateStreamEvent{
		ServerToolCall: &call,
		ResponseID:     result.ResponseID,
	})
}

func mergeResponsesToolInputEvent(
	result *GenerateOutput,
	parsed map[string]interface{},
	onEvent func(GenerateStreamEvent) error,
) error {
	if result == nil {
		return nil
	}
	itemID := firstNonEmptyString(getString(parsed["item_id"]), getString(parsed[valueCallIdFD3C070D]), getString(parsed["id"]))
	if itemID == "" {
		return nil
	}
	input := firstNonEmptyString(
		getString(parsed["delta"]),
		getString(parsed[valueArgumentsEF974DA2]),
		getString(parsed[valueInput53CC344B]),
	)
	done := strings.HasSuffix(strings.TrimSpace(getString(parsed[valueType1F400FCE])), ".done")
	if call, ok := updateToolCallInput(&result.ServerToolCalls, itemID, input, done); ok {
		if onEvent != nil {
			return onEvent(GenerateStreamEvent{
				ServerToolCall: &call,
				ResponseID:     result.ResponseID,
			})
		}
		return nil
	}
	updateToolCallInput(&result.ToolCalls, itemID, input, done)
	return nil
}

func parseResponsesStreamErrorEvent(parsed map[string]interface{}, rawBody string) error {
	errorPayload := asMap(parsed["error"])
	if len(errorPayload) == 0 {
		errorPayload = asMap(asMap(parsed["response"])["error"])
	}
	statusCode := streamErrorStatusCode(parsed, errorPayload)
	message := firstNonEmptyString(
		getString(errorPayload["message"]),
		getString(errorPayload["msg"]),
		getString(parsed["message"]),
		"responses stream returned an error event",
	)
	return &UpstreamError{
		StatusCode: statusCode,
		Message:    message,
		Body:       rawBody,
	}
}

func parseResponsesOutput(adapter string, parsed map[string]interface{}, result *GenerateOutput) error {
	phaseItems := append([]any(nil), asSlice(parsed[valueOutputAD4FEFDA])...)
	phaseItems = append(phaseItems, asSlice(parsed["tool_calls"])...)
	phaseText, phased, refused, err := projectResponsesPhase(phaseItems)
	if err != nil {
		return err
	}
	result.responsesPhased = phased
	result.Refused = refused
	result.Text = getString(parsed[valueOutputTextFDC62B9B])
	outputItems := asSlice(parsed[valueOutputAD4FEFDA])
	if NormalizeAdapter(adapter) == AdapterOpenAIResponses {
		result.ReplayStateJSON = encodeProviderReplayItems(AdapterOpenAIResponses, outputItems)
	}
	textChunks := make([]string, 0, len(outputItems))

	for outputIndex, raw := range outputItems {
		item := asMap(raw)
		if chunk := mergeResponsesOutputItem(result, item, true, outputIndex); chunk != "" {
			textChunks = append(textChunks, chunk)
		}
	}

	if result.Text == "" && len(textChunks) > 0 {
		result.Text = strings.Join(textChunks, "")
	}

	mergeResponsesTopLevelToolCalls(result, parsed["tool_calls"])

	if phased {
		result.Text = phaseText
	}
	result.Usage = parseOpenAICompatibleUsageForAdapter(adapter, parsed)
	result.ServerSideToolUsage = parseServerSideToolUsage(parsed)
	result.Citations = appendUniqueStrings(result.Citations, parseResponseCitations(parsed)...)
	return nil
}

func mergeResponsesTopLevelToolCalls(result *GenerateOutput, raw interface{}) {
	if result == nil {
		return
	}
	for outputIndex, value := range asSlice(raw) {
		item := asMap(value)
		if len(item) == 0 {
			continue
		}
		if isResponsesServerToolCallItem(item) {
			appendUniqueToolCall(&result.ServerToolCalls, parseResponseServerToolCallAt(item, outputIndex))
			result.Citations = appendUniqueStrings(result.Citations, parseResponseCitations(item)...)
			continue
		}
		itemType := strings.TrimSpace(getString(item[valueType1F400FCE]))
		if isResponsesClientToolCallType(itemType) {
			appendUniqueToolCall(&result.ToolCalls, parseResponseToolCall(item))
		}
	}
}

func mergeReasoningDeltaOutput(dst **ReasoningOutput, delta *ReasoningDelta) {
	if delta == nil {
		return
	}
	if *dst == nil {
		*dst = &ReasoningOutput{}
	}
	if strings.TrimSpace(delta.ItemID) != "" {
		(*dst).ItemID = strings.TrimSpace(delta.ItemID)
	}
	if strings.TrimSpace(delta.Status) != "" {
		(*dst).Status = strings.TrimSpace(delta.Status)
	}
	switch delta.Kind {
	case valueSummaryText2108AD9C:
		(*dst).Summary += delta.Text
	default:
		(*dst).Text += delta.Text
	}
	if strings.TrimSpace(delta.Signature) != "" {
		(*dst).Signature = strings.TrimSpace(delta.Signature)
	}
	if strings.TrimSpace(delta.EncryptedContent) != "" {
		(*dst).EncryptedContent = strings.TrimSpace(delta.EncryptedContent)
	}
}

func parseResponsesReasoningDelta(eventType string, parsed map[string]interface{}) *ReasoningDelta {
	text := extractReasoningDeltaText(parsed["delta"])
	if text == "" {
		return nil
	}

	kind := valueContentText122E4760
	if strings.Contains(eventType, "summary") {
		kind = valueSummaryText2108AD9C
	}

	return &ReasoningDelta{
		EventType:        eventType,
		ItemID:           firstNonEmptyString(getString(parsed["item_id"]), getStringFromPath(parsed, "item", "id")),
		Status:           firstNonEmptyString(getString(parsed["status"]), getStringFromPath(parsed, "item", "status")),
		Kind:             kind,
		Text:             text,
		EncryptedContent: firstNonEmptyString(getString(parsed["encrypted_content"]), getStringFromPath(parsed, "item", "encrypted_content")),
	}
}

func parseResponsesReasoningDone(eventType string, parsed map[string]interface{}) *ReasoningDelta {
	text := firstNonEmptyString(
		extractReasoningDeltaText(parsed[valueText239B340C]),
		extractReasoningDeltaText(parsed["summary"]),
		extractReasoningDeltaText(parsed["delta"]),
	)
	if text == "" {
		return nil
	}

	kind := valueContentText122E4760
	if strings.Contains(eventType, "summary") {
		kind = valueSummaryText2108AD9C
	}

	return &ReasoningDelta{
		EventType:        eventType,
		ItemID:           firstNonEmptyString(getString(parsed["item_id"]), getStringFromPath(parsed, "item", "id")),
		Status:           firstNonEmptyString(getString(parsed["status"]), getStringFromPath(parsed, "item", "status")),
		Kind:             kind,
		Text:             text,
		EncryptedContent: firstNonEmptyString(getString(parsed["encrypted_content"]), getStringFromPath(parsed, "item", "encrypted_content")),
	}
}

func reasoningOutputContains(output *ReasoningOutput, text string) bool {
	if output == nil || strings.TrimSpace(text) == "" {
		return false
	}
	return strings.Contains(output.Text, text) || strings.Contains(output.Summary, text)
}

func parseReasoningOutputItem(item map[string]interface{}) *ReasoningOutput {
	if len(item) == 0 {
		return nil
	}

	summaryParts := make([]string, 0)
	for _, raw := range asSlice(item["summary"]) {
		if text := extractReasoningDeltaText(raw); text != "" {
			summaryParts = append(summaryParts, text)
		}
	}

	contentParts := make([]string, 0)
	for _, raw := range asSlice(item[valueContent95A71030]) {
		if text := extractReasoningDeltaText(raw); text != "" {
			contentParts = append(contentParts, text)
		}
	}

	text := reasoningOutputText(item, contentParts)

	result := &ReasoningOutput{
		ItemID:           getString(item["id"]),
		Status:           getString(item["status"]),
		Summary:          strings.Join(summaryParts, ""),
		Text:             text,
		EncryptedContent: getString(item["encrypted_content"]),
	}
	if isEmptyReasoningOutput(result) {
		return nil
	}
	return result
}

func reasoningOutputText(item map[string]interface{}, contentParts []string) string {
	text := strings.Join(contentParts, "")
	if text != "" {
		return text
	}
	return extractReasoningDeltaText(item[valueText239B340C])
}

func isEmptyReasoningOutput(result *ReasoningOutput) bool {
	return strings.TrimSpace(result.ItemID) == "" &&
		strings.TrimSpace(result.Status) == "" &&
		strings.TrimSpace(result.Summary) == "" &&
		strings.TrimSpace(result.Text) == "" &&
		strings.TrimSpace(result.EncryptedContent) == ""
}

func mergeReasoningOutput(dst **ReasoningOutput, src *ReasoningOutput) {
	if src == nil {
		return
	}
	if *dst == nil {
		value := *src
		*dst = &value
		return
	}
	if strings.TrimSpace(src.ItemID) != "" {
		(*dst).ItemID = strings.TrimSpace(src.ItemID)
	}
	if strings.TrimSpace(src.Status) != "" {
		(*dst).Status = strings.TrimSpace(src.Status)
	}
	if strings.TrimSpace(src.Summary) != "" {
		(*dst).Summary = strings.TrimSpace(src.Summary)
	}
	if strings.TrimSpace(src.Text) != "" {
		(*dst).Text = strings.TrimSpace(src.Text)
	}
	if strings.TrimSpace(src.Signature) != "" {
		(*dst).Signature = strings.TrimSpace(src.Signature)
	}
	if strings.TrimSpace(src.EncryptedContent) != "" {
		(*dst).EncryptedContent = strings.TrimSpace(src.EncryptedContent)
	}
}

func mergeResponsesOutputItem(result *GenerateOutput, item map[string]interface{}, collectText bool, outputIndex interface{}) string {
	if result == nil || len(item) == 0 {
		return ""
	}
	itemType := strings.TrimSpace(getString(item[valueType1F400FCE]))
	switch {
	case itemType == valueReasoning506E158B:
		mergeReasoningOutput(&result.Reasoning, parseReasoningOutputItem(item))
	case isResponsesServerToolCallItem(item):
		appendUniqueToolCall(&result.ServerToolCalls, parseResponseServerToolCallAt(item, outputIndex))
		appendResponsesGeneratedImages(result, item)
		result.Citations = appendUniqueStrings(result.Citations, parseResponseCitations(item)...)
	case isResponsesClientToolCallType(itemType):
		appendUniqueToolCall(&result.ToolCalls, parseResponseToolCall(item))
	default:
		result.Citations = appendUniqueStrings(result.Citations, parseResponseCitations(item)...)
		if collectText {
			return extractOutputTextChunk(item)
		}
	}
	return ""
}

func parseResponseToolCall(item map[string]interface{}) ToolCall {
	arguments := normalizeJSONString(item[valueArgumentsEF974DA2])
	if arguments == "" {
		arguments = normalizeJSONString(item[valueInput53CC344B])
	}
	if arguments == "" {
		arguments = "{}"
	}

	providerItemID := strings.TrimSpace(getString(item["id"]))
	toolCallID := strings.TrimSpace(getString(item[valueCallIdFD3C070D]))
	if toolCallID == "" {
		toolCallID = providerItemID
	}

	toolName := strings.TrimSpace(getString(item[valueNameC25F4D03]))
	if toolName == "" {
		toolName = strings.TrimSpace(getStringFromPath(item, valueFunction24B5C282, valueNameC25F4D03))
	}

	toolType := strings.TrimSpace(getString(item[valueType1F400FCE]))
	if toolType == "" {
		toolType = valueFunction24B5C282
	}

	status := strings.TrimSpace(getString(item["status"]))
	if status == "" {
		status = valueRequested53D2A287
	}

	return ToolCall{
		ProviderItemID: providerItemID,
		ToolCallID:     toolCallID,
		ToolType:       toolType,
		ToolName:       toolName,
		ArgumentsJSON:  arguments,
		Status:         status,
	}
}

func parseResponseServerToolCall(item map[string]interface{}) ToolCall {
	itemType := strings.TrimSpace(getString(item[valueType1F400FCE]))
	if itemType == "" {
		itemType = "server_tool_call"
	}
	toolCallID := responseServerToolCallID(item, itemType)
	toolName := firstNonEmptyString(getString(item[valueNameC25F4D03]), responseServerToolNameFromType(itemType))
	status := firstNonEmptyString(getString(item["status"]), "completed")
	actionInputJSON, actionOutputJSON := splitResponsesServerToolAction(item["action"])
	inputJSON := firstNonEmptyString(
		actionInputJSON,
		normalizeJSONString(item[valueArgumentsEF974DA2]),
		normalizeJSONString(item[valueInput53CC344B]),
		normalizeJSONString(item["query"]),
	)
	outputJSON := firstNonEmptyString(
		normalizeJSONString(item[valueOutputAD4FEFDA]),
		normalizeJSONString(item["outputs"]),
		normalizeJSONString(item["results"]),
		normalizeJSONString(item["search_results"]),
		normalizeJSONString(item[valueSources6CEF08E9]),
		normalizeJSONString(item[valueCitations47439605]),
		normalizeJSONString(item["data"]),
		normalizeJSONString(item["items"]),
		normalizeJSONString(item[valueContent95A71030]),
		normalizeJSONString(item["response"]),
		normalizeJSONString(item["result"]),
		actionOutputJSON,
	)
	errorJSON := normalizeJSONString(item["error"])
	return ToolCall{
		ToolCallID:    toolCallID,
		ToolType:      itemType,
		ToolName:      toolName,
		ArgumentsJSON: inputJSON,
		Status:        status,
		OutputJSON:    outputJSON,
		ErrorJSON:     errorJSON,
	}
}

func responseServerToolCallID(item map[string]interface{}, itemType string) string {
	for _, extension := range allResponsesProtocolExtensions() {
		if extension.serverToolCallID == nil {
			continue
		}
		if toolCallID, ok := extension.serverToolCallID(item, itemType); ok {
			return toolCallID
		}
	}
	return firstNonEmptyString(
		getString(item["item_id"]),
		getString(item[valueCallIdFD3C070D]),
		getString(item["id"]),
		getString(item["tool_call_id"]),
	)
}

func responseServerToolNameFromType(itemType string) string {
	value := strings.TrimSpace(itemType)
	for _, suffix := range []string{"_call_output", "_call"} {
		value = strings.TrimSuffix(value, suffix)
	}
	return value
}

func splitResponsesServerToolAction(raw interface{}) (string, string) {
	action := asMap(raw)
	if len(action) == 0 {
		return normalizeJSONString(raw), ""
	}
	input := cloneMap(action)
	delete(input, valueSources6CEF08E9)
	output := make(map[string]interface{})
	if query := strings.TrimSpace(getString(action["query"])); query != "" {
		output["query"] = query
	}
	if actionType := strings.TrimSpace(getString(action[valueType1F400FCE])); actionType != "" {
		output[valueType1F400FCE] = actionType
	}
	if sources := asSlice(action[valueSources6CEF08E9]); len(sources) > 0 {
		output[valueSources6CEF08E9] = sources
	}
	outputJSON := ""
	if len(output) > 0 && len(asSlice(output[valueSources6CEF08E9])) > 0 {
		outputJSON = normalizeJSONString(output)
	}
	return normalizeJSONString(input), outputJSON
}

func isResponsesServerToolCallItem(item map[string]interface{}) bool {
	itemType := strings.TrimSpace(getString(item[valueType1F400FCE]))
	if isResponsesServerToolCallType(itemType) {
		return true
	}
	for _, extension := range allResponsesProtocolExtensions() {
		if extension.isServerToolCallItem != nil && extension.isServerToolCallItem(item) {
			return true
		}
	}
	return false
}

func isResponsesServerToolCallType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "web_search_call",
		"web_search_call_output",
		"file_search_call",
		"file_search_call_output",
		"code_interpreter_call",
		"code_interpreter_call_output",
		"code_execution_call",
		"code_execution_call_output",
		"collections_search_call",
		"collections_search_call_output",
		"attachment_search_call",
		"attachment_search_call_output",
		"computer_call",
		"computer_call_output",
		"mcp_call",
		"mcp_call_output",
		"image_generation_call",
		"image_generation_call_output",
		"shell_call",
		"shell_call_output",
		"local_shell_call",
		"local_shell_call_output":
		return true
	default:
		for _, extension := range allResponsesProtocolExtensions() {
			if extension.isServerToolCallType != nil && extension.isServerToolCallType(itemType) {
				return true
			}
		}
		return false
	}
}

func isResponsesClientToolCallType(itemType string) bool {
	value := strings.TrimSpace(itemType)
	if value == "" {
		return false
	}
	if value == valueFunctionCall1DF305D6 || value == "tool_call" || value == valueCustomToolCall09C0F085 {
		return true
	}
	return strings.HasSuffix(value, "_tool_call") && !isResponsesServerToolCallType(value)
}

func parseResponseCitations(parsed map[string]interface{}) []string {
	if len(parsed) == 0 {
		return nil
	}
	result := make([]string, 0)
	for _, key := range []string{valueCitations47439605, valueSources6CEF08E9, "urls"} {
		for _, raw := range asSlice(parsed[key]) {
			if text := firstNonEmptyString(getString(raw), getStringFromPath(asMap(raw), "url"), getStringFromPath(asMap(raw), "uri")); text != "" {
				result = append(result, text)
			}
		}
	}
	collectResponseCitationURLs(parsed, &result)
	return appendUniqueStrings(nil, result...)
}

func collectResponseCitationURLs(raw interface{}, result *[]string) {
	if result == nil {
		return
	}
	switch value := raw.(type) {
	case []interface{}:
		for _, item := range value {
			collectResponseCitationURLs(item, result)
		}
	case map[string]interface{}:
		itemType := strings.TrimSpace(getString(value[valueType1F400FCE]))
		switch itemType {
		case "url_citation", "file_citation":
			if text := firstNonEmptyString(
				getString(value["url"]),
				getString(value["uri"]),
				getStringFromPath(value, "url_citation", "url"),
				getStringFromPath(value, "file_citation", "url"),
			); text != "" {
				*result = append(*result, text)
			}
		}
		if text := firstNonEmptyString(getString(value["url"]), getString(value["uri"])); text != "" {
			*result = append(*result, text)
		}
		for _, key := range []string{"action", valueContent95A71030, "annotations", valueOutputAD4FEFDA, "outputs", "results", "search_results", valueSources6CEF08E9, valueCitations47439605, "url_citation", "file_citation", "data", "items", "response", "result"} {
			collectResponseCitationURLs(value[key], result)
		}
	}
}

func parseServerSideToolUsage(parsed map[string]interface{}) map[string]int64 {
	usage := asMap(parsed["usage"])
	if len(usage) == 0 {
		return nil
	}
	raw := asMap(usage["server_side_tool_usage"])
	if len(raw) == 0 {
		raw = asMap(usage["tool_usage"])
	}
	if len(raw) == 0 {
		raw = asMap(usage["server_side_tool_usage_details"])
	}
	if len(raw) == 0 {
		return nil
	}
	result := make(map[string]int64, len(raw))
	for key, value := range raw {
		if normalized := normalizeServerSideToolUsageKey(key); normalized != "" {
			result[normalized] = toInt64(value)
		}
	}
	return result
}

func normalizeServerSideToolUsageKey(key string) string {
	value := strings.TrimSpace(key)
	value = strings.TrimSuffix(value, "_calls")
	value = strings.TrimSuffix(value, "_call")
	switch value {
	case valueWebSearch29553E49, valueCodeInterpreterEE1C409F, "image_generation", "shell", "file_search", "mcp", "document_search":
		return value
	default:
		for _, extension := range allResponsesProtocolExtensions() {
			if extension.normalizeServerSideToolUsageKey == nil {
				continue
			}
			if normalized, ok := extension.normalizeServerSideToolUsageKey(value, key); ok {
				return normalized
			}
		}
		return key
	}
}

func extractOutputTextChunk(item map[string]interface{}) string {
	if chunk := extractContentText(item[valueContent95A71030]); chunk != "" {
		return chunk
	}
	if chunk := getString(item[valueText239B340C]); chunk != "" {
		return chunk
	}
	return ""
}
