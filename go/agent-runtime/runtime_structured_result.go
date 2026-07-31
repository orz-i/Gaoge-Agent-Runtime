package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/modelcap"
)

type structuredRunOutputSpec struct {
	Mode       modelcap.StructuredOutputMode
	Schema     interface{}
	SchemaJSON string
}

func resolveStructuredRunOutput(route *LLMRoute, schemaJSON json.RawMessage) (structuredRunOutputSpec, error) {
	if route == nil || len(schemaJSON) == 0 {
		return structuredRunOutputSpec{}, ErrInvalidInput
	}
	resolution := modelcap.ResolveStructuredOutput(route.ModelCapabilitiesJSON)
	if resolution.Mode == modelcap.StructuredOutputUnsupported {
		return structuredRunOutputSpec{}, fmt.Errorf(
			"%w: model=%s protocol=%s configuration=%s",
			ErrStructuredOutputUnsupported,
			strings.TrimSpace(route.UpstreamModel),
			strings.TrimSpace(route.Protocol),
			resolution.ConfigurationStatus,
		)
	}
	spec, err := decodeStructuredRunOutput(schemaJSON)
	if err != nil {
		return structuredRunOutputSpec{}, err
	}
	spec.Mode = resolution.Mode
	return spec, nil
}

func decodeStructuredRunOutput(schemaJSON json.RawMessage) (structuredRunOutputSpec, error) {
	schema, err := decodeWorkflowJSON(schemaJSON)
	if err != nil {
		return structuredRunOutputSpec{}, errors.Join(ErrWorkflowSchemaInvalid, err)
	}
	canonical, err := canonicalWorkflowJSON(schema)
	if err != nil {
		return structuredRunOutputSpec{}, errors.Join(ErrWorkflowSchemaInvalid, err)
	}
	return structuredRunOutputSpec{Schema: schema, SchemaJSON: string(canonical)}, nil
}

func applyStructuredRunOutput(options map[string]interface{}, spec structuredRunOutputSpec) map[string]interface{} {
	result := cloneRunModelOptions(options)
	delete(result, plannerResponseFormatKey)
	delete(result, plannerResponseJSONSchemaKey)
	switch spec.Mode {
	case modelcap.StructuredOutputStrictJSONSchema:
		result[plannerResponseFormatKey] = map[string]interface{}{
			valueType5EE8C955: plannerJSONSchemaType,
			plannerJSONSchemaType: map[string]interface{}{
				valueName68D33990:     "agent_runtime_result",
				workflowPayloadStrict: true,
				workflowPayloadSchema: spec.Schema,
			},
		}
	case modelcap.StructuredOutputJSONObject:
		result[plannerResponseFormatKey] = map[string]interface{}{valueType5EE8C955: plannerJSONObjectType}
	case modelcap.StructuredOutputJSONText:
		// The prompt plus the local Draft 2020-12 validator enforce the
		// contract. No provider-specific response option is required.
	}
	return result
}

func cloneRunModelOptions(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return make(map[string]interface{})
	}
	result := make(map[string]interface{}, len(input)+1)
	for key, value := range input {
		result[key] = value
	}
	return result
}

func structuredRunInstructions(base string, spec structuredRunOutputSpec) string {
	return strings.TrimSpace(base) + "\n\nReturn exactly one JSON value that validates against the following self-contained Draft 2020-12 JSON Schema. Do not use Markdown fences or add prose.\n" + spec.SchemaJSON
}

func structuredRunCorrectionMessages(messages []Message, invalidText string, validationErr error, spec structuredRunOutputSpec) []Message {
	result := cloneLLMMessages(messages)
	result = append(result,
		Message{Role: "assistant", Content: invalidText},
		Message{Role: valueUser19341906, Content: "The previous result did not satisfy the required JSON contract. Correct it and return only the replacement JSON value.\nValidation error: " + truncateStructuredValidationError(validationErr) + "\nSchema:\n" + spec.SchemaJSON},
	)
	return result
}

func truncateStructuredValidationError(err error) string {
	value := strings.TrimSpace(fmt.Sprint(err))
	runes := []rune(value)
	if len(runes) > 2000 {
		return string(runes[:2000])
	}
	return value
}

func normalizeStructuredRunText(text string, schemaJSON json.RawMessage) (string, error) {
	decoded, err := decodeWorkflowJSON([]byte(unwrapStructuredRunJSON(text)))
	if err != nil {
		return "", errors.Join(ErrWorkflowResultInvalid, err)
	}
	schema, schemaErr := decodeWorkflowJSON(schemaJSON)
	if schemaErr != nil {
		return "", errors.Join(ErrWorkflowSchemaInvalid, schemaErr)
	}
	decoded = pruneStructuredRunTopLevelProperties(decoded, schema)
	if err = validateWorkflowJSON(schemaJSON, decoded); err != nil {
		return "", errors.Join(ErrWorkflowResultInvalid, err)
	}
	canonical, err := canonicalWorkflowJSON(decoded)
	if err != nil {
		return "", errors.Join(ErrWorkflowResultInvalid, err)
	}
	return string(canonical), nil
}

func pruneStructuredRunTopLevelProperties(value, schema interface{}) interface{} {
	object, objectOK := value.(map[string]interface{})
	contract, schemaOK := schema.(map[string]interface{})
	if !objectOK || !schemaOK || contract["additionalProperties"] != false || contract["patternProperties"] != nil {
		return value
	}
	properties, ok := contract["properties"].(map[string]interface{})
	if !ok {
		return value
	}
	result := make(map[string]interface{}, len(object))
	for key, item := range object {
		if _, allowed := properties[key]; allowed {
			result[key] = item
		}
	}
	return result
}

func validateStructuredRunText(text string, schemaJSON json.RawMessage) error {
	_, err := normalizeStructuredRunText(text, schemaJSON)
	return err
}

func unwrapStructuredRunJSON(text string) string {
	value := strings.TrimSpace(text)
	if fenced := fencedStructuredRunJSON(value); fenced != "" {
		return fenced
	}
	if !strings.HasPrefix(value, "```") || !strings.HasSuffix(value, "```") {
		return value
	}
	value = strings.TrimSuffix(value, "```")
	value = strings.TrimPrefix(value, "```json")
	value = strings.TrimPrefix(value, "```JSON")
	value = strings.TrimPrefix(value, "```")
	return strings.TrimSpace(value)
}

func fencedStructuredRunJSON(value string) string {
	for _, marker := range []string{"```json", "```JSON"} {
		start := strings.Index(value, marker)
		if start < 0 {
			continue
		}
		contentStart := start + len(marker)
		endOffset := strings.Index(value[contentStart:], "```")
		if endOffset < 0 {
			continue
		}
		if candidate := strings.TrimSpace(value[contentStart : contentStart+endOffset]); candidate != "" {
			return candidate
		}
	}
	return ""
}

func structuredRunCorrectionAttempts(effective effectiveTextRunConfig) int {
	if len(effective.StructuredOutputSchema) == 0 {
		return 0
	}
	if effective.ResultAttempts <= 0 {
		return 1
	}
	if effective.ResultAttempts > 2 {
		return 2
	}
	return effective.ResultAttempts
}

func mergeModelUsage(left, right Usage) Usage {
	result := Usage{
		InputTokens:        left.InputTokens + right.InputTokens,
		OutputTokens:       left.OutputTokens + right.OutputTokens,
		CacheReadTokens:    left.CacheReadTokens + right.CacheReadTokens,
		CacheWriteTokens:   left.CacheWriteTokens + right.CacheWriteTokens,
		CacheWrite5mTokens: left.CacheWrite5mTokens + right.CacheWrite5mTokens,
		CacheWrite1hTokens: left.CacheWrite1hTokens + right.CacheWrite1hTokens,
		ReasoningTokens:    left.ReasoningTokens + right.ReasoningTokens,
		Speed:              firstNonEmptyString(right.Speed, left.Speed),
		ServiceTier:        firstNonEmptyString(right.ServiceTier, left.ServiceTier),
		BillingRateClass:   firstNonEmptyString(right.BillingRateClass, left.BillingRateClass),
		RawUsageJSON:       MergeRawUsageJSON(left.RawUsageJSON, right.RawUsageJSON),
	}
	return result
}

func (s *Engine) recordStructuredRunCorrection(ctx context.Context, run model.Run, stepID string, attempt int, validationErr error) error {
	return s.appendRunEvent(
		context.WithoutCancel(ctx),
		&run,
		"result.correction.started",
		stepID,
		"Structured result correction",
		map[string]interface{}{workflowPayloadAttempt: attempt, workflowPayloadError: truncateStructuredValidationError(validationErr)},
		nil,
	)
}
