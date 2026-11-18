package transport

import "strings"

const (
	geminiFunctionModeANY  = "ANY"
	geminiFunctionModeNONE = "NONE"
	geminiFunctionModeAUTO = "AUTO"
	anthropicToolChoiceAny = "any"
)

// applyNativeToolChoice maps the formal ToolChoice contract onto each provider
// request body. Explicit Options win unless the formal contract forbids tools.
func applyNativeToolChoice(payload map[string]interface{}, adapter string, input GenerateInput, toolsEnabled bool) {
	mode := strings.ToLower(strings.TrimSpace(string(input.ToolChoice.Mode)))
	if input.DisableTools || mode == string(ToolChoiceNone) {
		// Omitting declarations does not forbid replayed/historical tool names.
		// A final answer must explicitly disable calls, including stale Options.
		delete(payload, "tool_choice")
		applyNativeToolChoiceForAdapter(payload, NormalizeAdapter(adapter), ToolChoice{Mode: ToolChoiceNone})
		return
	}
	if !toolsEnabled {
		return
	}
	if mode == "" || mode == string(ToolChoiceAuto) {
		return
	}
	applyNativeToolChoiceForAdapter(payload, NormalizeAdapter(adapter), input.ToolChoice)
}

func applyNativeToolChoiceForAdapter(payload map[string]interface{}, adapter string, choice ToolChoice) {
	switch adapter {
	case AdapterOpenAIChatCompletions, AdapterOpenRouterChat, AdapterMiniMaxChat, AdapterBailianChat, AdapterSenseAudioChat:
		applyOpenAIChatToolChoice(payload, choice)
	case AdapterOpenAIResponses, AdapterOpenRouterResponses, AdapterXAIResponses, AdapterVolcengineResponses:
		applyOpenAIResponsesToolChoice(payload, choice)
	case AdapterAnthropicMessages, AdapterMiniMaxAnthropic:
		applyAnthropicToolChoice(payload, choice)
	default:
		if strings.Contains(adapter, "chat") {
			applyOpenAIChatToolChoice(payload, choice)
		}
	}
}

func applyOpenAIChatToolChoice(payload map[string]interface{}, choice ToolChoice) {
	if _, exists := payload["tool_choice"]; exists {
		return
	}
	if value := openAIToolChoiceValue(choice.Mode); value != "" {
		payload["tool_choice"] = value
	}
}

func applyOpenAIResponsesToolChoice(payload map[string]interface{}, choice ToolChoice) {
	if _, exists := payload["tool_choice"]; exists {
		return
	}
	if value := openAIToolChoiceValue(choice.Mode); value != "" {
		payload["tool_choice"] = value
	}
}

func openAIToolChoiceValue(mode ToolChoiceMode) string {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case string(ToolChoiceRequired):
		return string(ToolChoiceRequired)
	case string(ToolChoiceNone):
		return string(ToolChoiceNone)
	case string(ToolChoiceAuto):
		return string(ToolChoiceAuto)
	default:
		return ""
	}
}

func applyAnthropicToolChoice(payload map[string]interface{}, choice ToolChoice) {
	if _, exists := payload["tool_choice"]; exists {
		return
	}
	switch strings.ToLower(strings.TrimSpace(string(choice.Mode))) {
	case string(ToolChoiceRequired):
		payload["tool_choice"] = map[string]interface{}{valueType945D3026: anthropicToolChoiceAny}
	case string(ToolChoiceNone):
		payload["tool_choice"] = map[string]interface{}{valueType945D3026: string(ToolChoiceNone)}
	case string(ToolChoiceAuto):
		payload["tool_choice"] = map[string]interface{}{valueType945D3026: string(ToolChoiceAuto)}
	}
}
