package transport

import (
	"encoding/json"
	"slices"
	"strings"
)

const (
	InputModalityText  = "text"
	InputModalityImage = "image"
	InputModalityAudio = "audio"
	InputModalityVideo = "video"
)

// TextInputModalities intersects model declarations with implemented text wire formats.
// Output kinds and display names never establish an input capability.
func TextInputModalities(protocol, capabilitiesJSON string) []string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	implemented := textProtocolInputModalities(protocol)
	var capabilities struct {
		InputModalities json.RawMessage `json:"inputModalities"`
	}
	if strings.TrimSpace(capabilitiesJSON) != "" && json.Unmarshal([]byte(capabilitiesJSON), &capabilities) != nil {
		return []string{InputModalityText}
	}
	if capabilities.InputModalities == nil {
		if protocol == AdapterMiniMaxChat || protocol == AdapterMiniMaxAnthropic {
			return []string{InputModalityText}
		}
		// Video uses a provider extension and must be declared, even on an
		// OpenAI/Anthropic-compatible route. Google has native media wire support.
		if protocol != AdapterGoogleInteractions {
			implemented = slices.DeleteFunc(implemented, func(value string) bool { return value == InputModalityVideo })
		}
		return implemented
	}
	result := []string{InputModalityText}
	var declared []string
	if json.Unmarshal(capabilities.InputModalities, &declared) != nil {
		return result
	}
	for _, modality := range implemented {
		if modality != InputModalityText && slices.Contains(declared, modality) {
			result = append(result, modality)
		}
	}
	return result
}

// PublicTextInputModalities uses the common capabilities of the model's text protocols.
func PublicTextInputModalities(protocolsJSON, capabilitiesJSON string) []string {
	var protocols []string
	_ = json.Unmarshal([]byte(protocolsJSON), &protocols)
	var result []string
	for _, protocol := range protocols {
		if !isTextInputProtocol(protocol) {
			continue
		}
		current := TextInputModalities(protocol, capabilitiesJSON)
		if result == nil {
			result = current
		} else {
			result = slices.DeleteFunc(result, func(value string) bool { return !slices.Contains(current, value) })
		}
	}
	if result == nil {
		return TextInputModalities(AdapterOpenAIResponses, capabilitiesJSON)
	}
	return result
}

func textProtocolInputModalities(protocol string) []string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case AdapterGoogleInteractions:
		return []string{InputModalityText, InputModalityImage, InputModalityAudio, InputModalityVideo}
	case AdapterOpenAIResponses, AdapterOpenAIChatCompletions, AdapterAnthropicMessages, AdapterMiniMaxChat, AdapterMiniMaxAnthropic:
		return []string{InputModalityText, InputModalityImage, InputModalityVideo}
	case AdapterOpenRouterResponses, AdapterOpenRouterChat, AdapterXAIResponses, AdapterVolcengineResponses, AdapterBailianChat, AdapterSenseAudioChat:
		return []string{InputModalityText, InputModalityImage}
	default:
		return []string{InputModalityText}
	}
}

func isTextInputProtocol(protocol string) bool {
	return len(textProtocolInputModalities(protocol)) > 1
}
