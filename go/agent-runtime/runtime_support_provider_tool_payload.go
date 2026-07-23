package agentruntime

import (
	"encoding/json"
	"errors"
	"strings"
)

// Protocol constants mirrored from modelgateway adapters (no provider import —
// main API must not depend on modelgateway/provider).
const (
	protocolOpenAIResponses       = "openai_responses"
	protocolOpenRouterChat        = "openrouter_chat_completions"
	protocolOpenRouterResponses   = "openrouter_responses"
	protocolOpenAIChatCompletions = "openai_chat_completions"
	protocolAnthropicMessages     = "anthropic_messages"
	protocolGoogleGenerateContent = "google_generate_content"
	protocolXAIResponses          = "xai_responses"

	wireKeyParameters    = "parameters"
	wireKeyInputSchema   = "input_schema"
	wireKeyFunctionDecls = "functionDeclarations"
	wireKeyType          = "type"
	wireKeyProperties    = "properties"
	wireToolTypeFunction = "function"
)

var errProviderToolPayloadProtocolRequired = errors.New("provider tool payload measure requires protocol")

// measureProviderToolPayloadBytes estimates the JSON size of tool declarations
// as they appear on the provider request wire. Keep shapes aligned with
// modelgateway/provider buildGeminiTools / buildOpenAITools / buildAnthropicTools.
func measureProviderToolPayloadBytes(protocol string, tools []ToolDefinition) (int, error) {
	payload, err := providerToolWirePayload(protocol, tools)
	if err != nil {
		return 0, err
	}
	if payload == nil {
		return 0, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	return len(raw), nil
}

func providerToolWirePayload(protocol string, tools []ToolDefinition) (interface{}, error) {
	switch strings.TrimSpace(protocol) {
	case protocolGoogleGenerateContent:
		return wireGeminiTools(tools), nil
	case protocolOpenAIResponses, protocolOpenRouterResponses, protocolXAIResponses:
		return wireOpenAITools(tools, false), nil
	case protocolOpenAIChatCompletions, protocolOpenRouterChat:
		return wireOpenAITools(tools, true), nil
	case protocolAnthropicMessages:
		return wireAnthropicTools(tools), nil
	case "":
		return nil, errProviderToolPayloadProtocolRequired
	default:
		// Unknown protocol: stable internal estimate (Name/Description/InputSchema).
		return tools, nil
	}
}

func wireGeminiTools(tools []ToolDefinition) []map[string]interface{} {
	if len(tools) == 0 {
		return nil
	}
	declarations := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		declarations = append(declarations, map[string]interface{}{
			valueName09837852:        name,
			valueDescription97BF8ECD: strings.TrimSpace(tool.Description),
			// Align measure path with modelgateway sanitizeGeminiSchema null-union collapse.
			wireKeyParameters: normalizeGeminiSchemaForMeasure(decodeToolSchemaMap(tool.InputSchema)),
		})
	}
	if len(declarations) == 0 {
		return nil
	}
	return []map[string]interface{}{{wireKeyFunctionDecls: declarations}}
}

// normalizeGeminiSchemaForMeasure mirrors the provider-side pure null-union rewrite
// so conversation payload bytes stay close to the real Gemini wire without importing
// modelgateway/provider (main-api process boundary).
func normalizeGeminiSchemaForMeasure(schema map[string]interface{}) map[string]interface{} {
	if len(schema) == 0 {
		return schema
	}
	result := make(map[string]interface{}, len(schema))
	for key, value := range schema {
		if applyMeasureSchemaField(result, key, value) {
			continue
		}
		result[key] = value
	}
	return result
}

func applyMeasureSchemaField(result map[string]interface{}, key string, value interface{}) bool {
	switch key {
	case "anyOf":
		if collapsed, ok := collapseMeasureNullUnion(asInterfaceSlice(value)); ok {
			for field, fieldValue := range collapsed {
				result[field] = fieldValue
			}
			return true
		}
		result[key] = normalizeGeminiSchemaListForMeasure(asInterfaceSlice(value))
		return true
	case wireKeyProperties:
		result[key] = normalizeGeminiSchemaPropertiesForMeasure(asStringMap(value))
		return true
	case "items":
		if nested := asStringMap(value); len(nested) > 0 {
			result[key] = normalizeGeminiSchemaForMeasure(nested)
			return true
		}
	}
	return false
}

func normalizeGeminiSchemaPropertiesForMeasure(properties map[string]interface{}) map[string]interface{} {
	if len(properties) == 0 {
		return properties
	}
	result := make(map[string]interface{}, len(properties))
	for name, raw := range properties {
		if nested := asStringMap(raw); len(nested) > 0 {
			result[name] = normalizeGeminiSchemaForMeasure(nested)
			continue
		}
		result[name] = raw
	}
	return result
}

func normalizeGeminiSchemaListForMeasure(items []interface{}) []interface{} {
	if len(items) == 0 {
		return items
	}
	result := make([]interface{}, 0, len(items))
	for _, raw := range items {
		if nested := asStringMap(raw); len(nested) > 0 {
			result = append(result, normalizeGeminiSchemaForMeasure(nested))
			continue
		}
		result = append(result, raw)
	}
	return result
}

func collapseMeasureNullUnion(anyOf []interface{}) (map[string]interface{}, bool) {
	if len(anyOf) != 2 {
		return nil, false
	}
	first := asStringMap(anyOf[0])
	second := asStringMap(anyOf[1])
	if len(first) == 0 || len(second) == 0 {
		return nil, false
	}
	nonNull, nullSide := first, second
	if isMeasureNullTypeSchema(first) {
		nonNull, nullSide = second, first
	} else if !isMeasureNullTypeSchema(second) {
		return nil, false
	}
	if !isMeasureNullTypeSchema(nullSide) || isMeasureNullTypeSchema(nonNull) {
		return nil, false
	}
	collapsed := make(map[string]interface{}, len(nonNull)+1)
	for key, value := range nonNull {
		collapsed[key] = value
	}
	collapsed["nullable"] = true
	return collapsed, true
}

func isMeasureNullTypeSchema(schema map[string]interface{}) bool {
	text, ok := schema[wireKeyType].(string)
	return ok && strings.EqualFold(strings.TrimSpace(text), "null")
}

func asStringMap(value interface{}) map[string]interface{} {
	if typed, ok := value.(map[string]interface{}); ok {
		return typed
	}
	return nil
}

func asInterfaceSlice(value interface{}) []interface{} {
	if typed, ok := value.([]interface{}); ok {
		return typed
	}
	return nil
}

func wireOpenAITools(tools []ToolDefinition, chatCompletions bool) []map[string]interface{} {
	if len(tools) == 0 {
		return nil
	}
	items := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		schema := decodeToolSchemaMap(tool.InputSchema)
		if chatCompletions {
			items = append(items, map[string]interface{}{
				wireKeyType: wireToolTypeFunction,
				wireToolTypeFunction: map[string]interface{}{
					valueName09837852:        name,
					valueDescription97BF8ECD: strings.TrimSpace(tool.Description),
					wireKeyParameters:        schema,
				},
			})
			continue
		}
		items = append(items, map[string]interface{}{
			wireKeyType:              wireToolTypeFunction,
			valueName09837852:        name,
			valueDescription97BF8ECD: strings.TrimSpace(tool.Description),
			wireKeyParameters:        schema,
		})
	}
	return items
}

func wireAnthropicTools(tools []ToolDefinition) []map[string]interface{} {
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
			valueName09837852:        name,
			valueDescription97BF8ECD: strings.TrimSpace(tool.Description),
			wireKeyInputSchema:       decodeToolSchemaMap(tool.InputSchema),
		})
	}
	return items
}

func decodeToolSchemaMap(raw json.RawMessage) map[string]interface{} {
	empty := map[string]interface{}{wireKeyType: valueObjectE97B31A9, wireKeyProperties: map[string]interface{}{}}
	if len(raw) == 0 {
		return empty
	}
	var schema map[string]interface{}
	if err := json.Unmarshal(raw, &schema); err != nil || schema == nil {
		return empty
	}
	return schema
}
