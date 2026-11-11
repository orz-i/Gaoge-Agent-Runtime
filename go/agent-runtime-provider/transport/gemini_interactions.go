package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	interactionKeyAllowedTools          = "allowed_tools"
	interactionKeyArguments             = "arguments"
	interactionKeyCallID                = "call_id"
	interactionKeyContent               = "content"
	interactionKeyData                  = "data"
	interactionKeyDelta                 = "delta"
	interactionKeyDescription           = "description"
	interactionKeyEventType             = "event_type"
	interactionKeyGenerationConfig      = "generation_config"
	interactionKeyID                    = "id"
	interactionKeyInput                 = "input"
	interactionKeyInteraction           = "interaction"
	interactionKeyMimeType              = "mime_type"
	interactionKeyMode                  = "mode"
	interactionKeyModel                 = "model"
	interactionKeyName                  = "name"
	interactionKeyParameters            = "parameters"
	interactionKeyPreviousInteractionID = "previous_interaction_id"
	interactionKeyResponseFormat        = "response_format"
	interactionKeyResult                = "result"
	interactionKeySafetySettings        = "safety_settings"
	interactionKeySignature             = "signature"
	interactionKeyStatus                = "status"
	interactionKeyStep                  = "step"
	interactionKeySteps                 = "steps"
	interactionKeyStore                 = "store"
	interactionKeyStream                = "stream"
	interactionKeySummary               = "summary"
	interactionKeySystemInstruction     = "system_instruction"
	interactionKeyText                  = "text"
	interactionKeyToolChoice            = "tool_choice"
	interactionKeyTools                 = "tools"
	interactionKeyType                  = "type"
	interactionKeyUsage                 = "usage"
	interactionTypeArgumentsDelta       = "arguments_delta"
	interactionTypeFunction             = "function"
	interactionTypeFunctionCall         = "function_call"
	interactionTypeFunctionResult       = "function_result"
	interactionTypeGoogleSearch         = "google_search"
	interactionTypeImage                = "image"
	interactionTypeModelOutput          = "model_output"
	interactionTypeText                 = "text"
	interactionTypeThought              = "thought"
	interactionTypeThoughtSignature     = "thought_signature"
	interactionTypeThoughtSummary       = "thought_summary"
	interactionTypeUserInput            = "user_input"
	interactionEventCompleted           = "interaction.completed"
	interactionEventCreated             = "interaction.created"
	interactionEventInProgress          = "interaction.in_progress"
	interactionEventRequiresAction      = "interaction.requires_action"
	interactionEventStepDelta           = "step.delta"
	interactionEventStepStart           = "step.start"
	interactionEventStepStop            = "step.stop"
	interactionToolChoiceAny            = "any"
	interactionToolChoiceNone           = "none"
	interactionRoleDeveloper            = "developer"
	interactionRoleModel                = "model"
	interactionUsageGoogleCodeExecution = "google.code_execution"
	interactionUsageGoogleFileSearch    = "google.file_search"
	interactionUsageGoogleSearch        = "google.google_search"
	interactionUsageGoogleURLContext    = "google.url_context"
	interactionTypeCodeExecution        = "code_execution"
	interactionTypeFileSearch           = "file_search"
	interactionTypeURLContext           = "url_context"
	interactionAnnotationURLCitation    = "url_citation"
	interactionDefaultImageMIME         = "image/jpeg"
	interactionJSONNull                 = "null"
)

// geminiInteractionsAdapter is the Google text/agent transport. GenerateContent
// is intentionally not exposed as a text adapter: new Google text routes use
// the stable Interactions API surface directly.
type geminiInteractionsAdapter struct {
	client *Client
}

func (a *geminiInteractionsAdapter) Name() string { return AdapterGoogleInteractions }

func (a *geminiInteractionsAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	return a.client.generateGeminiInteraction(ctx, route, input)
}

func (a *geminiInteractionsAdapter) GenerateStream(ctx context.Context, route RouteConfig, input GenerateInput, onEvent func(GenerateStreamEvent) error) (*GenerateOutput, error) {
	return a.client.generateGeminiInteractionStream(ctx, route, input, onEvent)
}

func (a *geminiInteractionsAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	return a.client.listModelsGemini(ctx, route)
}

func buildGeminiInteractionsURL(base string) string {
	return buildVersionedEndpointURL(normalizeGeminiEndpointBaseURL(base), "v1", "/interactions")
}

func buildGeminiInteractionsRequestBody(model string, input GenerateInput, stream bool) (map[string]interface{}, error) {
	providerTools, toolDefinitions, toolsEnabled, err := toolDeclarationsForInput(input)
	if err != nil {
		return nil, err
	}
	systemParts, steps := geminiInteractionPrompt(input)
	payload := map[string]interface{}{
		interactionKeyModel: strings.TrimPrefix(strings.TrimSpace(model), "models/"),
		interactionKeyInput: steps,
		interactionKeyStore: false,
	}
	applyGeminiInteractionRequestMetadata(payload, input, stream, systemParts)
	if generationConfig := geminiInteractionGenerationConfig(input); len(generationConfig) > 0 {
		payload[interactionKeyGenerationConfig] = generationConfig
	}
	if tools := geminiInteractionTools(providerTools, toolDefinitions, toolsEnabled); len(tools) > 0 {
		payload[interactionKeyTools] = tools
	}
	return payload, nil
}

func geminiInteractionPrompt(input GenerateInput) ([]string, []map[string]interface{}) {
	systemParts := make([]string, 0, 2)
	if text := strings.TrimSpace(input.Instructions); text != "" {
		systemParts = append(systemParts, text)
	}
	steps := make([]map[string]interface{}, 0, len(input.Messages)*2)
	for _, message := range normalizeMessages(input.Messages) {
		if message.Role == valueSystemF78794CC || message.Role == interactionRoleDeveloper {
			if text := extractMessageText(message); text != "" {
				systemParts = append(systemParts, text)
			}
			continue
		}
		steps = append(steps, geminiInteractionMessageSteps(message)...)
	}
	return systemParts, steps
}

func applyGeminiInteractionRequestMetadata(payload map[string]interface{}, input GenerateInput, stream bool, systemParts []string) {
	if stream {
		payload[interactionKeyStream] = true
	}
	if len(systemParts) > 0 {
		payload[interactionKeySystemInstruction] = strings.Join(systemParts, "\n\n")
	}
	copyInteractionOption(payload, input.Options, interactionKeyResponseFormat)
	copyInteractionOption(payload, input.Options, interactionKeySafetySettings)
}

func copyInteractionOption(payload map[string]interface{}, options map[string]interface{}, key string) {
	if value, ok := options[key]; ok && value != nil {
		payload[key] = value
	}
}

func geminiInteractionGenerationConfig(input GenerateInput) map[string]interface{} {
	config := cloneMap(asMap(input.Options[interactionKeyGenerationConfig]))
	for _, key := range []string{"max_output_tokens", "seed", "stop_sequences", "thinking_level", "thinking_summaries"} {
		if value, ok := input.Options[key]; ok {
			config[key] = value
		}
	}
	applyGeminiInteractionsToolChoice(config, input.ToolChoice)
	return config
}

func geminiInteractionTools(providerTools []map[string]interface{}, definitions []ToolDefinition, enabled bool) []map[string]interface{} {
	if !enabled {
		return nil
	}
	tools := make([]map[string]interface{}, 0, len(providerTools)+len(definitions))
	for _, tool := range providerTools {
		tools = append(tools, cloneMap(tool))
	}
	return append(tools, buildGeminiInteractionFunctionTools(definitions)...)
}

func buildGeminiInteractionFunctionTools(definitions []ToolDefinition) []map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(definitions))
	for _, tool := range definitions {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		item := map[string]interface{}{
			interactionKeyType:       interactionTypeFunction,
			interactionKeyName:       name,
			interactionKeyParameters: decodeToolSchema(tool.InputSchema),
		}
		if description := strings.TrimSpace(tool.Description); description != "" {
			item[interactionKeyDescription] = description
		}
		items = append(items, item)
	}
	return items
}

func applyGeminiInteractionsToolChoice(config map[string]interface{}, choice ToolChoice) {
	switch choice.Mode {
	case ToolChoiceAuto:
	case ToolChoiceNone:
		config[interactionKeyToolChoice] = interactionToolChoiceNone
	case ToolChoiceRequired:
		if len(choice.AllowedToolNames) == 0 {
			config[interactionKeyToolChoice] = interactionToolChoiceAny
			return
		}
		config[interactionKeyToolChoice] = map[string]interface{}{
			interactionKeyAllowedTools: map[string]interface{}{
				interactionKeyMode:  interactionToolChoiceAny,
				interactionKeyTools: append([]string(nil), choice.AllowedToolNames...),
			},
		}
	}
}

func geminiInteractionMessageSteps(message Message) []map[string]interface{} {
	steps := make([]map[string]interface{}, 0, 1+len(message.ToolCalls)+len(message.ToolResults))
	content := geminiInteractionContent(message)
	switch message.Role {
	case valueAssistant6B7057DB, interactionRoleModel:
		steps = append(steps, geminiAssistantInteractionSteps(message, content)...)
	default:
		if len(content) > 0 {
			steps = append(steps, map[string]interface{}{interactionKeyType: interactionTypeUserInput, interactionKeyContent: content})
		}
	}
	for _, result := range message.ToolResults {
		steps = append(steps, map[string]interface{}{
			interactionKeyType: interactionTypeFunctionResult, interactionKeyCallID: strings.TrimSpace(result.ToolCallID),
			interactionKeyName:   strings.TrimSpace(result.ToolName),
			interactionKeyResult: []map[string]interface{}{{interactionKeyType: interactionTypeText, interactionKeyText: buildToolResultContent(result)}},
		})
	}
	return steps
}

// Replayed steps are authoritative when present; otherwise the canonical
// model-output step must precede function calls in the provider wire payload.
func geminiAssistantInteractionSteps(message Message, content []map[string]interface{}) []map[string]interface{} {
	if replay := decodeProviderReplayItems(message.ReplayStateJSON, AdapterGoogleInteractions); len(replay) > 0 {
		return replay
	}
	steps := make([]map[string]interface{}, 0, 1+len(message.ToolCalls))
	if len(content) > 0 {
		steps = append(steps, map[string]interface{}{interactionKeyType: interactionTypeModelOutput, interactionKeyContent: content})
	}
	for _, call := range message.ToolCalls {
		arguments := decodeJSONObject(call.ArgumentsJSON)
		step := map[string]interface{}{
			interactionKeyType: interactionTypeFunctionCall, interactionKeyID: strings.TrimSpace(call.ToolCallID),
			interactionKeyName: strings.TrimSpace(call.ToolName), interactionKeyArguments: arguments,
		}
		if signature := strings.TrimSpace(call.ThoughtSignature); signature != "" {
			step[interactionKeySignature] = signature
		}
		steps = append(steps, step)
	}
	return steps
}

func geminiInteractionContent(message Message) []map[string]interface{} {
	parts := message.Parts
	if len(parts) == 0 {
		if text := strings.TrimSpace(message.Content); text != "" {
			return []map[string]interface{}{{interactionKeyType: interactionTypeText, interactionKeyText: text}}
		}
		return nil
	}
	result := make([]map[string]interface{}, 0, len(parts))
	for _, part := range parts {
		switch strings.TrimSpace(part.Kind) {
		case ContentPartImage, ContentPartAudio, ContentPartVideo:
			if len(part.Data) == 0 {
				continue
			}
			mime := strings.TrimSpace(part.MimeType)
			if mime == "" {
				mime = interactionDefaultImageMIME
			}
			result = append(result, map[string]interface{}{
				interactionKeyType: part.Kind, interactionKeyData: base64.StdEncoding.EncodeToString(part.Data), interactionKeyMimeType: mime,
			})
		default:
			if text := strings.TrimSpace(part.Text); text != "" {
				result = append(result, map[string]interface{}{interactionKeyType: interactionTypeText, interactionKeyText: text})
			}
		}
	}
	return result
}

func decodeJSONObject(raw string) map[string]interface{} {
	value := map[string]interface{}{}
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &value) != nil {
		return map[string]interface{}{}
	}
	return value
}

func (c *Client) generateGeminiInteraction(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	requestURL := buildGeminiInteractionsURL(geminiBaseURL(route))
	requestBody, err := buildGeminiInteractionsRequestBody(route.UpstreamModel, input, false)
	if err != nil {
		return nil, err
	}
	payload, err := marshalGeminiInteractionPayload(requestBody)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()
	req, err := c.newGeminiRequest(requestCtx, http.MethodPost, requestURL, bytes.NewReader(payload), route)
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
	debug := upstreamDebugSnapshot(req, payload, resp, body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseGeminiError(resp.StatusCode, body, debug)
	}
	output, err := parseGeminiInteraction(body)
	if err != nil {
		return nil, attachUpstreamDebug(err, debug)
	}
	output.Debug = debug
	return output, nil
}

func parseGeminiInteraction(body []byte) (*GenerateOutput, error) {
	parsed := map[string]interface{}{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	result := &GenerateOutput{
		ResponseID: strings.TrimSpace(getString(parsed[interactionKeyID])), RawJSON: string(body),
		ToolCalls: make([]ToolCall, 0), ServerToolCalls: make([]ToolCall, 0),
		ServerSideToolUsage: map[string]int64{},
	}
	result.ReplayStateJSON = encodeProviderReplayItems(AdapterGoogleInteractions, asSlice(parsed[interactionKeySteps]))
	reasoning := &ReasoningOutput{}
	for _, raw := range asSlice(parsed[interactionKeySteps]) {
		if err := applyGeminiInteractionStep(result, reasoning, asMap(raw)); err != nil {
			return nil, err
		}
	}
	if reasoning.Text != "" || reasoning.Signature != "" {
		result.Reasoning = reasoning
	}
	result.Usage = parseGeminiInteractionUsage(asMap(parsed[interactionKeyUsage]))
	result.Citations = appendUniqueStrings(nil, result.Citations...)
	result.StopReason = strings.TrimSpace(getString(parsed[interactionKeyStatus]))
	return result, nil
}

func applyGeminiInteractionStep(result *GenerateOutput, reasoning *ReasoningOutput, step map[string]interface{}) error {
	typeName := strings.TrimSpace(getString(step[interactionKeyType]))
	switch typeName {
	case interactionTypeModelOutput:
		applyGeminiInteractionModelOutput(result, step)
	case interactionTypeThought:
		reasoning.Text += geminiInteractionStepText(step)
		reasoning.Signature = firstNonEmptyString(reasoning.Signature, getString(step[interactionKeySignature]))
	case interactionTypeFunctionCall:
		call, err := geminiInteractionFunctionCall(step)
		if err != nil {
			return err
		}
		result.ToolCalls = append(result.ToolCalls, call)
	default:
		applyGeminiInteractionServerTool(result, typeName, step)
	}
	return nil
}

func applyGeminiInteractionModelOutput(result *GenerateOutput, step map[string]interface{}) {
	for _, contentRaw := range asSlice(step[interactionKeyContent]) {
		content := asMap(contentRaw)
		if getString(content[interactionKeyType]) != interactionTypeText {
			continue
		}
		result.Text += getString(content[interactionKeyText])
		result.Citations = append(result.Citations, geminiInteractionCitationURLs(content)...)
	}
}

func geminiInteractionFunctionCall(step map[string]interface{}) (ToolCall, error) {
	arguments, err := json.Marshal(step[interactionKeyArguments])
	if err != nil {
		return ToolCall{}, err
	}
	return ToolCall{
		ProviderItemID:   getString(step[interactionKeyID]),
		ToolCallID:       getString(step[interactionKeyID]),
		ToolType:         interactionTypeFunction,
		ToolName:         getString(step[interactionKeyName]),
		ArgumentsJSON:    string(arguments),
		ThoughtSignature: getString(step[interactionKeySignature]),
		Status:           getString(step[interactionKeyStatus]),
	}, nil
}

func applyGeminiInteractionServerTool(result *GenerateOutput, typeName string, step map[string]interface{}) {
	if result == nil {
		return
	}
	if strings.HasSuffix(typeName, "_call") {
		call, created := upsertGeminiInteractionServerToolCall(result, typeName, step)
		if created {
			result.ServerSideToolUsage[geminiInteractionUsageKey(typeName)]++
		}
		_ = call
		return
	}
	if strings.HasSuffix(typeName, "_result") {
		mergeGeminiInteractionServerToolResult(result, typeName, step)
	}
}

func upsertGeminiInteractionServerToolCall(result *GenerateOutput, typeName string, step map[string]interface{}) (ToolCall, bool) {
	toolType := strings.TrimSuffix(strings.TrimSpace(typeName), "_call")
	callID := strings.TrimSpace(getString(step[interactionKeyID]))
	if toolType == "" || callID == "" {
		return ToolCall{}, false
	}
	for index := range result.ServerToolCalls {
		if strings.TrimSpace(result.ServerToolCalls[index].ToolCallID) != callID {
			continue
		}
		item := result.ServerToolCalls[index]
		item.ProviderItemID = firstNonEmptyString(getString(step[interactionKeyID]), item.ProviderItemID)
		item.ToolType = firstNonEmptyString(toolType, item.ToolType)
		item.ToolName = firstNonEmptyString(toolType, item.ToolName)
		if arguments := normalizeJSONString(step[interactionKeyArguments]); arguments != "" && arguments != interactionJSONNull {
			item.ArgumentsJSON = arguments
		}
		item.Status = normalizeGeminiExplicitServerToolStatus(firstNonEmptyString(getString(step[interactionKeyStatus]), item.Status))
		result.ServerToolCalls[index] = item
		return item, false
	}
	item := ToolCall{
		ProviderItemID: getString(step[interactionKeyID]),
		ToolCallID:     callID,
		ToolType:       toolType,
		ToolName:       toolType,
		ArgumentsJSON:  normalizeJSONString(step[interactionKeyArguments]),
		Status:         normalizeGeminiExplicitServerToolStatus(getString(step[interactionKeyStatus])),
	}
	if item.ArgumentsJSON == interactionJSONNull {
		item.ArgumentsJSON = ""
	}
	result.ServerToolCalls = append(result.ServerToolCalls, item)
	return item, true
}

func mergeGeminiInteractionServerToolResult(result *GenerateOutput, typeName string, step map[string]interface{}) (ToolCall, bool) {
	callID := strings.TrimSpace(getString(step[interactionKeyCallID]))
	if result == nil || callID == "" {
		return ToolCall{}, false
	}
	toolType := strings.TrimSuffix(strings.TrimSpace(typeName), "_result")
	for index := range result.ServerToolCalls {
		if strings.TrimSpace(result.ServerToolCalls[index].ToolCallID) != callID {
			continue
		}
		item := result.ServerToolCalls[index]
		item.ToolType = firstNonEmptyString(toolType, item.ToolType)
		item.ToolName = firstNonEmptyString(toolType, item.ToolName)
		item.OutputJSON = normalizeJSONString(step[interactionKeyResult])
		if item.OutputJSON == "" || item.OutputJSON == interactionJSONNull {
			item.OutputJSON = "{}"
		}
		isError, _ := step["is_error"].(bool)
		if isError {
			item.Status = valueError8694DAEB
			item.ErrorJSON = item.OutputJSON
		} else {
			item.Status = valueCompleted9E9778C8
		}
		result.ServerToolCalls[index] = item
		return item, true
	}
	return ToolCall{}, false
}

func geminiInteractionStepText(step map[string]interface{}) string {
	var result string
	for _, raw := range asSlice(step[interactionKeySummary]) {
		result += getString(asMap(raw)[interactionKeyText])
	}
	return result
}

func geminiInteractionCitationURLs(content map[string]interface{}) []string {
	result := make([]string, 0)
	for _, raw := range asSlice(content["annotations"]) {
		annotation := asMap(raw)
		if strings.TrimSpace(getString(annotation[interactionKeyType])) == interactionAnnotationURLCitation {
			if url := strings.TrimSpace(getString(annotation["url"])); url != "" {
				result = append(result, url)
			}
		}
	}
	return result
}

func parseGeminiInteractionUsage(usage map[string]interface{}) Usage {
	return Usage{
		InputTokens: getInt64FromPath(usage, "total_input_tokens"), OutputTokens: getInt64FromPath(usage, "total_output_tokens"),
		CacheReadTokens: getInt64FromPath(usage, "total_cached_tokens"), ReasoningTokens: getInt64FromPath(usage, "total_thought_tokens"),
		RawUsageJSON: normalizeJSONString(usage),
	}
}

func geminiInteractionUsageKey(stepType string) string {
	switch strings.TrimSuffix(stepType, "_call") {
	case interactionTypeGoogleSearch:
		return interactionUsageGoogleSearch
	case interactionTypeCodeExecution:
		return interactionUsageGoogleCodeExecution
	case interactionTypeFileSearch:
		return interactionUsageGoogleFileSearch
	case interactionTypeURLContext:
		return interactionUsageGoogleURLContext
	default:
		return strings.TrimSuffix(stepType, "_call")
	}
}

func (c *Client) generateGeminiInteractionStream(ctx context.Context, route RouteConfig, input GenerateInput, onEvent func(GenerateStreamEvent) error) (*GenerateOutput, error) {
	requestURL := buildGeminiInteractionsURL(geminiBaseURL(route))
	requestBody, err := buildGeminiInteractionsRequestBody(route.UpstreamModel, input, true)
	if err != nil {
		return nil, err
	}
	payload, err := marshalGeminiInteractionPayload(requestBody)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	firstByteTimer := time.AfterFunc(resolveReadTimeout(route.ReadTimeoutMS), cancel)
	defer firstByteTimer.Stop()
	req, err := c.newGeminiRequest(requestCtx, http.MethodPost, requestURL, bytes.NewReader(payload), route)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.httpClientForRoute(route).Do(req)
	firstByteTimer.Stop()
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := readUpstreamBody(resp.Body)
		return nil, parseGeminiError(resp.StatusCode, body, upstreamDebugSnapshot(req, payload, resp, body))
	}
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0), ServerToolCalls: make([]ToolCall, 0), ServerSideToolUsage: map[string]int64{}}
	streamBody := newUpstreamBodyRecorder(newIdleTimeoutReader(resp.Body, resolveStreamIdleTimeout(route.StreamIdleTimeoutMS)))
	if err := consumeGeminiInteractionsStream(streamBody, result, onEvent); err != nil {
		return nil, attachUpstreamDebug(err, upstreamDebugSnapshot(req, payload, resp, streamErrorBody(streamBody, err)))
	}
	return result, nil
}

func consumeGeminiInteractionsStream(reader io.Reader, result *GenerateOutput, onEvent func(GenerateStreamEvent) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxUpstreamBodyBytes)
	parser := geminiInteractionsStreamParser{
		result: result, onEvent: onEvent,
		steps: make(map[int]geminiInteractionStreamStep), replaySteps: make(map[int]map[string]interface{}),
	}
	for scanner.Scan() {
		if err := parser.consumeLine(scanner.Text()); err != nil {
			return err
		}
	}
	if err := parser.flush(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	parser.finalizeReplayState()
	return nil
}

type geminiInteractionsStreamParser struct {
	dataLines   []string
	result      *GenerateOutput
	onEvent     func(GenerateStreamEvent) error
	steps       map[int]geminiInteractionStreamStep
	replaySteps map[int]map[string]interface{}
}

type geminiInteractionStreamStep struct {
	stepType     string
	callID       string
	functionCall *ToolCall
	replay       map[string]interface{}
}

func (p *geminiInteractionsStreamParser) consumeLine(raw string) error {
	line := strings.TrimRight(raw, "\r")
	if line == "" {
		return p.flush()
	}
	if strings.HasPrefix(line, "data:") {
		p.dataLines = append(p.dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
	}
	return nil
}

func (p *geminiInteractionsStreamParser) flush() error {
	data := strings.TrimSpace(strings.Join(p.dataLines, "\n"))
	p.dataLines = p.dataLines[:0]
	if data == "" {
		return nil
	}
	parsed, err := decodeIndexedStreamObject(data)
	if err != nil {
		return err
	}
	return p.applyEvent(parsed)
}

func (p *geminiInteractionsStreamParser) applyEvent(parsed map[string]interface{}) error {
	index, err := streamIndexField(parsed, "index")
	if err != nil {
		return err
	}
	eventType := firstNonEmptyString(getString(parsed[interactionKeyEventType]), getString(parsed[interactionKeyType]))
	switch eventType {
	case interactionEventCreated, interactionEventInProgress, interactionEventRequiresAction, interactionEventCompleted:
		return p.applyLifecycle(eventType, asMap(parsed[interactionKeyInteraction]))
	case interactionEventStepStart:
		return p.applyStepStart(eventType, index, asMap(parsed[interactionKeyStep]))
	case interactionEventStepDelta:
		return p.applyStepDelta(eventType, index, asMap(parsed[interactionKeyDelta]))
	case interactionEventStepStop:
		return p.applyStepStop(index)
	}
	return nil
}

func (p *geminiInteractionsStreamParser) applyLifecycle(eventType string, interaction map[string]interface{}) error {
	if id := strings.TrimSpace(getString(interaction[interactionKeyID])); id != "" {
		p.result.ResponseID = id
		if err := p.emit(GenerateStreamEvent{ResponseID: id}); err != nil {
			return err
		}
	}
	if eventType == interactionEventCompleted {
		p.result.Usage = parseGeminiInteractionUsage(asMap(interaction[interactionKeyUsage]))
		p.result.StopReason = getString(interaction[interactionKeyStatus])
		if steps := asSlice(interaction[interactionKeySteps]); len(steps) > 0 {
			p.result.ReplayStateJSON = encodeProviderReplayItems(AdapterGoogleInteractions, steps)
		}
	}
	return nil
}

func (p *geminiInteractionsStreamParser) applyStepStart(eventType string, index int, step map[string]interface{}) error {
	stepType := strings.TrimSpace(getString(step[interactionKeyType]))
	state := geminiInteractionStreamStep{
		stepType: stepType,
		replay:   cloneMap(step),
		callID: firstNonEmptyString(
			getString(step[interactionKeyCallID]),
			getString(step[interactionKeyID]),
		),
	}
	switch stepType {
	case interactionTypeFunctionCall:
		state.functionCall = &ToolCall{
			ProviderItemID: getString(step[interactionKeyID]),
			ToolCallID:     getString(step[interactionKeyID]),
			ToolType:       interactionTypeFunction,
			ToolName:       getString(step[interactionKeyName]),
			Status:         valueInProgress89487480,
		}
		state.functionCall.ThoughtSignature = getString(step[interactionKeySignature])
		if arguments := normalizeJSONString(step[interactionKeyArguments]); arguments != "" && arguments != interactionJSONNull {
			state.functionCall.ArgumentsJSON = arguments
		}
	case interactionTypeThought:
		text := geminiInteractionStepText(step)
		reasoning := p.reasoning()
		reasoning.Text += text
		reasoning.Signature = firstNonEmptyString(reasoning.Signature, getString(step[interactionKeySignature]))
		if text != "" {
			p.steps[index] = state
			return p.emit(GenerateStreamEvent{Reasoning: &ReasoningDelta{EventType: eventType, Kind: interactionTypeThoughtSummary, Text: text}, ResponseID: p.result.ResponseID})
		}
	default:
		if err := p.applyServerToolStepStart(stepType, step, &state); err != nil {
			return err
		}
	}
	p.steps[index] = state
	return nil
}

func (p *geminiInteractionsStreamParser) applyServerToolStepStart(stepType string, step map[string]interface{}, state *geminiInteractionStreamStep) error {
	if strings.HasSuffix(stepType, "_call") {
		call, created := upsertGeminiInteractionServerToolCall(p.result, stepType, step)
		if created {
			p.result.ServerSideToolUsage[geminiInteractionUsageKey(stepType)]++
		}
		state.callID = firstNonEmptyString(call.ToolCallID, state.callID)
		if call.ToolCallID == "" {
			return nil
		}
		return p.emit(GenerateStreamEvent{ServerToolCall: &call, ResponseID: p.result.ResponseID})
	}
	if !strings.HasSuffix(stepType, "_result") || step[interactionKeyResult] == nil {
		return nil
	}
	call, ok := mergeGeminiInteractionServerToolResult(p.result, stepType, step)
	if !ok {
		return nil
	}
	return p.emit(GenerateStreamEvent{ServerToolCall: &call, ResponseID: p.result.ResponseID})
}

func (p *geminiInteractionsStreamParser) applyStepDelta(eventType string, index int, delta map[string]interface{}) error {
	deltaType := strings.TrimSpace(getString(delta[interactionKeyType]))
	switch deltaType {
	case interactionTypeText:
		p.appendReplayText(index, getString(delta[interactionKeyText]))
		return p.applyTextDelta(getString(delta[interactionKeyText]))
	case interactionTypeThoughtSummary:
		text := getString(asMap(delta[interactionKeyContent])[interactionKeyText])
		p.appendReplayThoughtSummary(index, text)
		return p.applyThoughtSummary(eventType, text)
	case interactionTypeThoughtSignature:
		signature := getString(delta[interactionKeySignature])
		p.setReplaySignature(index, signature)
		return p.applyThoughtSignature(eventType, signature)
	case interactionTypeArgumentsDelta:
		state := p.steps[index]
		if state.functionCall != nil {
			state.functionCall.ArgumentsJSON += getString(delta[interactionKeyArguments])
			p.steps[index] = state
		}
	default:
		if strings.HasSuffix(deltaType, "_call") {
			p.mergeReplayDelta(index, delta)
			return p.applyServerToolCallDelta(index, deltaType, delta)
		}
		if strings.HasSuffix(deltaType, "_result") {
			p.mergeReplayDelta(index, delta)
			return p.applyServerToolResultDelta(index, deltaType, delta)
		}
	}
	return nil
}

func (p *geminiInteractionsStreamParser) applyServerToolCallDelta(index int, deltaType string, delta map[string]interface{}) error {
	state := p.steps[index]
	step := cloneMap(delta)
	if step == nil {
		step = make(map[string]interface{})
	}
	if strings.TrimSpace(getString(step[interactionKeyID])) == "" && state.callID != "" {
		step[interactionKeyID] = state.callID
	}
	call, created := upsertGeminiInteractionServerToolCall(p.result, deltaType, step)
	if created {
		p.result.ServerSideToolUsage[geminiInteractionUsageKey(deltaType)]++
	}
	if call.ToolCallID != "" {
		state.callID = call.ToolCallID
		state.stepType = deltaType
		p.steps[index] = state
		return p.emit(GenerateStreamEvent{ServerToolCall: &call, ResponseID: p.result.ResponseID})
	}
	return nil
}

func (p *geminiInteractionsStreamParser) applyServerToolResultDelta(index int, deltaType string, delta map[string]interface{}) error {
	state := p.steps[index]
	step := cloneMap(delta)
	if step == nil {
		step = make(map[string]interface{})
	}
	if strings.TrimSpace(getString(step[interactionKeyCallID])) == "" && state.callID != "" {
		step[interactionKeyCallID] = state.callID
	}
	call, ok := mergeGeminiInteractionServerToolResult(p.result, deltaType, step)
	if !ok {
		return nil
	}
	return p.emit(GenerateStreamEvent{ServerToolCall: &call, ResponseID: p.result.ResponseID})
}

func (p *geminiInteractionsStreamParser) applyTextDelta(text string) error {
	p.result.Text += text
	if text == "" {
		return nil
	}
	return p.emit(GenerateStreamEvent{Delta: text, ResponseID: p.result.ResponseID})
}

func (p *geminiInteractionsStreamParser) applyThoughtSummary(eventType string, text string) error {
	p.reasoning().Text += text
	if text == "" {
		return nil
	}
	return p.emit(GenerateStreamEvent{Reasoning: &ReasoningDelta{EventType: eventType, Kind: interactionTypeThoughtSummary, Text: text}, ResponseID: p.result.ResponseID})
}

func (p *geminiInteractionsStreamParser) applyThoughtSignature(eventType string, raw string) error {
	signature := strings.TrimSpace(raw)
	p.reasoning().Signature = signature
	if signature == "" {
		return nil
	}
	return p.emit(GenerateStreamEvent{Reasoning: &ReasoningDelta{EventType: eventType, Kind: interactionTypeThoughtSignature, Signature: signature}, ResponseID: p.result.ResponseID})
}

func (p *geminiInteractionsStreamParser) applyStepStop(index int) error {
	state, ok := p.steps[index]
	if !ok {
		return nil
	}
	if state.functionCall != nil {
		if strings.TrimSpace(state.functionCall.ArgumentsJSON) == "" {
			state.functionCall.ArgumentsJSON = "{}"
		}
		state.functionCall.Status = valueCompleted9E9778C8
		p.result.ToolCalls = append(p.result.ToolCalls, *state.functionCall)
		if state.replay == nil {
			state.replay = map[string]interface{}{}
		}
		state.replay[interactionKeyArguments] = decodeJSONObject(state.functionCall.ArgumentsJSON)
		state.replay[interactionKeyStatus] = valueCompleted9E9778C8
	}
	if len(state.replay) > 0 {
		p.replaySteps[index] = cloneMap(state.replay)
	}
	delete(p.steps, index)
	return nil
}

func (p *geminiInteractionsStreamParser) appendReplayText(index int, text string) {
	if text == "" {
		return
	}
	state, ok := p.steps[index]
	if !ok || state.replay == nil {
		return
	}
	content := asSlice(state.replay[interactionKeyContent])
	if len(content) > 0 {
		last := asMap(content[len(content)-1])
		if getString(last[interactionKeyType]) == interactionTypeText {
			last[interactionKeyText] = getString(last[interactionKeyText]) + text
			content[len(content)-1] = last
			state.replay[interactionKeyContent] = content
			p.steps[index] = state
			return
		}
	}
	state.replay[interactionKeyContent] = append(content, map[string]interface{}{
		interactionKeyType: interactionTypeText, interactionKeyText: text,
	})
	p.steps[index] = state
}

func (p *geminiInteractionsStreamParser) appendReplayThoughtSummary(index int, text string) {
	if text == "" {
		return
	}
	state, ok := p.steps[index]
	if !ok || state.replay == nil {
		return
	}
	summary := asSlice(state.replay[interactionKeySummary])
	if len(summary) > 0 {
		last := asMap(summary[len(summary)-1])
		if last != nil {
			last[interactionKeyText] = getString(last[interactionKeyText]) + text
			summary[len(summary)-1] = last
			state.replay[interactionKeySummary] = summary
			p.steps[index] = state
			return
		}
	}
	state.replay[interactionKeySummary] = append(summary, map[string]interface{}{interactionKeyText: text})
	p.steps[index] = state
}

func (p *geminiInteractionsStreamParser) setReplaySignature(index int, signature string) {
	state, ok := p.steps[index]
	if !ok || state.replay == nil || strings.TrimSpace(signature) == "" {
		return
	}
	state.replay[interactionKeySignature] = strings.TrimSpace(signature)
	p.steps[index] = state
}

func (p *geminiInteractionsStreamParser) mergeReplayDelta(index int, delta map[string]interface{}) {
	state, ok := p.steps[index]
	if !ok {
		return
	}
	if state.replay == nil {
		state.replay = map[string]interface{}{interactionKeyType: state.stepType}
	}
	for key, value := range delta {
		if key == interactionKeyType {
			continue
		}
		state.replay[key] = value
	}
	p.steps[index] = state
}

func (p *geminiInteractionsStreamParser) finalizeReplayState() {
	if p == nil || p.result == nil || strings.TrimSpace(p.result.ReplayStateJSON) != "" || len(p.replaySteps) == 0 {
		return
	}
	indexes := make([]int, 0, len(p.replaySteps))
	for index := range p.replaySteps {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	items := make([]interface{}, 0, len(indexes))
	for _, index := range indexes {
		items = append(items, cloneMap(p.replaySteps[index]))
	}
	p.result.ReplayStateJSON = encodeProviderReplayItems(AdapterGoogleInteractions, items)
}

func (p *geminiInteractionsStreamParser) reasoning() *ReasoningOutput {
	if p.result.Reasoning == nil {
		p.result.Reasoning = &ReasoningOutput{}
	}
	return p.result.Reasoning
}

func (p *geminiInteractionsStreamParser) emit(event GenerateStreamEvent) error {
	if p.onEvent == nil {
		return nil
	}
	return p.onEvent(event)
}
