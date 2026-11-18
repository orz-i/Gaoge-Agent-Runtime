package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
)

const (
	miMoDefaultBaseURL   = "https://api.xiaomimimo.com/v1"
	miMoSpeechModel      = "mimo-v2.5-tts"
	miMoVoiceDesignModel = "mimo-v2.5-tts-voicedesign"
	miMoVoiceCloneModel  = "mimo-v2.5-tts-voiceclone"
	miMoDefaultVoice     = "mimo_default"
	miMoOptimizePreview  = "optimize_text_preview"
	miMoFormatPCM16      = "pcm16"
	miMoVoiceOption      = "voice"
	miMoMIMEWAV          = "audio/wav"
	miMoMIMEMP3          = "audio/mpeg"
	miMoMIMEPCM          = "audio/pcm"
)

var (
	errMiMoSpeechInput    = errors.New("invalid MiMo speech input")
	errMiMoSpeechResponse = errors.New("invalid MiMo speech response")
)

// MiMo TTS uses Chat Completions on the wire, but returns an audio asset rather
// than a chat answer. Keep it separate from the existing text adapter.
type miMoSpeechAdapter struct{ client *Client }

func (a *miMoSpeechAdapter) Name() string { return AdapterMiMoSpeech }

func (a *miMoSpeechAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	if err := rejectUnsupportedImageMask(input.ImageEditMask, AdapterMiMoSpeech); err != nil {
		return nil, err
	}
	request, err := buildMiMoSpeechRequest(route.UpstreamModel, input)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(route.BaseURL) == "" {
		route.BaseURL = miMoDefaultBaseURL
	}
	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()
	body, debug, err := a.client.doMediaJSONRequest(requestCtx, route, http.MethodPost, buildOpenAIRequestURL(route.BaseURL, EndpointChatCompletions), payload)
	if err != nil {
		return nil, err
	}
	output, err := parseMiMoSpeechOutput(body, getString(asMap(request[ContentPartAudio])[miniMaxFormatKey]))
	if err != nil {
		return nil, attachUpstreamDebug(err, debug)
	}
	output.Debug = debug
	return output, nil
}

// The current stream ABI carries text events, not PCM audio chunks. Returning
// this error before dispatch lets the existing media path use Generate once.
func (a *miMoSpeechAdapter) GenerateStream(context.Context, RouteConfig, GenerateInput, func(GenerateStreamEvent) error) (*GenerateOutput, error) {
	return nil, ErrUnsupportedStream
}

func (a *miMoSpeechAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	if strings.TrimSpace(route.BaseURL) == "" {
		route.BaseURL = miMoDefaultBaseURL
	}
	return a.client.listModelsOpenAICompatible(ctx, route)
}

func buildMiMoSpeechRequest(model string, input GenerateInput) (map[string]interface{}, error) {
	model = strings.TrimSpace(model)
	if !slices.Contains([]string{miMoSpeechModel, miMoVoiceDesignModel, miMoVoiceCloneModel}, model) {
		return nil, fmt.Errorf("%w: unsupported model %s", errMiMoSpeechInput, model)
	}
	audio, err := miMoSpeechAudioOptions(model, input.Options)
	if err != nil {
		return nil, err
	}
	style, text := miMoSpeechTexts(input)
	style, err = miMoSpeechStyle(input.Options, style)
	if err != nil {
		return nil, err
	}
	if model == miMoVoiceDesignModel && style == "" {
		return nil, fmt.Errorf("%w: voice design requires a style description", errMiMoSpeechInput)
	}
	if text == "" && audio[miMoOptimizePreview] != true {
		return nil, fmt.Errorf("%w: synthesis text is required", errMiMoSpeechInput)
	}
	messages := make([]map[string]interface{}, 0, 2)
	if style != "" {
		messages = append(messages, map[string]interface{}{valueRole01C0A112: valueUserD301E989, valueContent03EE2C26: style})
	}
	if text != "" {
		messages = append(messages, map[string]interface{}{valueRole01C0A112: valueAssistantCAAB15A3, valueContent03EE2C26: text})
	}
	return map[string]interface{}{"model": model, "messages": messages, ContentPartAudio: audio, valueStream1E52954F: false}, nil
}

func miMoSpeechStyle(options map[string]interface{}, fallback string) (string, error) {
	raw, exists := options["style"]
	if !exists {
		return fallback, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%w: style must be a string", errMiMoSpeechInput)
	}
	return strings.TrimSpace(value), nil
}

func miMoSpeechTexts(input GenerateInput) (string, string) {
	style := strings.TrimSpace(input.Instructions)
	var userText, assistantText string
	for _, message := range input.Messages {
		text := strings.TrimSpace(messagePromptText(message))
		if text == "" {
			continue
		}
		switch normalizeRole(message.Role) {
		case valueUserD301E989:
			userText = text
		case valueAssistantCAAB15A3:
			assistantText = text
		}
	}
	if assistantText != "" {
		if style == "" {
			style = userText
		}
		return style, assistantText
	}
	// Product media prompts arrive as user messages; MiMo expects the text to be
	// spoken in an assistant message. Instructions remain the optional style.
	return style, userText
}

func miMoSpeechAudioOptions(model string, options map[string]interface{}) (map[string]interface{}, error) {
	audio, err := miMoRawSpeechAudio(options)
	if err != nil {
		return nil, err
	}
	if err = validateMiMoSpeechAudioOptions(model, audio); err != nil {
		return nil, err
	}
	format, err := miMoSpeechFormat(audio)
	if err != nil {
		return nil, err
	}
	audio[miniMaxFormatKey] = format
	return validateMiMoSpeechVoice(model, audio)
}

func miMoRawSpeechAudio(options map[string]interface{}) (map[string]interface{}, error) {
	audio := make(map[string]interface{})
	if raw, exists := options[ContentPartAudio]; exists {
		values, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("%w: audio options must be an object", errMiMoSpeechInput)
		}
		maps.Copy(audio, values)
	}
	for _, key := range []string{miMoVoiceOption, miniMaxFormatKey} {
		if _, exists := audio[key]; !exists {
			if value, exists := options[key]; exists && value != "" {
				audio[key] = value
			}
		}
	}
	return audio, nil
}

func validateMiMoSpeechAudioOptions(model string, audio map[string]interface{}) error {
	for key := range audio {
		if !slices.Contains([]string{miMoVoiceOption, miniMaxFormatKey, miMoOptimizePreview}, key) {
			return fmt.Errorf("%w: unsupported audio option %s", errMiMoSpeechInput, key)
		}
	}
	if value, exists := audio[miMoOptimizePreview]; exists {
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%w: optimize_text_preview must be a boolean", errMiMoSpeechInput)
		}
		if model != miMoVoiceDesignModel {
			return fmt.Errorf("%w: optimize_text_preview is only supported for voice design", errMiMoSpeechInput)
		}
	}
	return nil
}

func miMoSpeechFormat(audio map[string]interface{}) (string, error) {
	format := miniMaxFormatWAV
	if raw, exists := audio[miniMaxFormatKey]; exists {
		value, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("%w: audio format must be a string", errMiMoSpeechInput)
		}
		format = strings.ToLower(strings.TrimSpace(value))
	}
	if format == bailianFormatPCM {
		format = miMoFormatPCM16
	}
	if !slices.Contains([]string{miniMaxFormatWAV, miniMaxFormatMP3, miMoFormatPCM16}, format) {
		return "", fmt.Errorf("%w: audio format must be wav, mp3, pcm or pcm16", errMiMoSpeechInput)
	}
	return format, nil
}

func validateMiMoSpeechVoice(model string, audio map[string]interface{}) (map[string]interface{}, error) {
	raw, exists := audio[miMoVoiceOption]
	voice, valid := raw.(string)
	if exists && !valid {
		return nil, fmt.Errorf("%w: voice must be a string", errMiMoSpeechInput)
	}
	voice = strings.TrimSpace(voice)
	switch model {
	case miMoVoiceDesignModel:
		if exists {
			return nil, fmt.Errorf("%w: voice design does not accept a voice parameter", errMiMoSpeechInput)
		}
	case miMoVoiceCloneModel:
		if !miMoValidVoiceSample(voice) {
			return nil, fmt.Errorf("%w: voice clone requires a base64-encoded WAV or MP3 sample", errMiMoSpeechInput)
		}
		audio[miMoVoiceOption] = voice
	default:
		preset, err := miMoPresetVoice(voice)
		if err != nil {
			return nil, err
		}
		audio[miMoVoiceOption] = preset
	}
	return audio, nil
}

func miMoValidVoiceSample(voice string) bool {
	decoded, err := base64.StdEncoding.DecodeString(voice)
	return err == nil && (miMoWAVAudio(decoded) || miMoMP3Audio(decoded))
}

func miMoPresetVoice(voice string) (string, error) {
	if voice == "" {
		voice = miMoDefaultVoice
	}
	if !slices.Contains([]string{miMoDefaultVoice, "冰糖", "茉莉", "苏打", "白桦", "Mia", "Chloe", "Milo", "Dean"}, voice) {
		return "", fmt.Errorf("%w: unsupported preset voice", errMiMoSpeechInput)
	}
	return voice, nil
}

func parseMiMoSpeechOutput(body []byte, format string) (*GenerateOutput, error) {
	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w: %w", errMiMoSpeechResponse, err)
	}
	choice := firstMapItem(asSlice(parsed["choices"]))
	message := asMap(choice[valueMessageF06D0A65])
	audio := asMap(message[ContentPartAudio])
	encoded := strings.TrimSpace(getString(audio[valueDataE4BFD03E]))
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !miMoEncodedAudio(data, format) {
		return nil, fmt.Errorf("%w: missing or invalid %s audio data", errMiMoSpeechResponse, format)
	}
	mimeTypes := map[string]string{miniMaxFormatWAV: miMoMIMEWAV, miniMaxFormatMP3: miMoMIMEMP3, miMoFormatPCM16: miMoMIMEPCM}
	text := getString(message["final_text_preview"])
	return &GenerateOutput{
		ResponseID: getString(parsed["id"]),
		Text:       text,
		Usage:      parseOpenAICompatibleUsageForAdapter(AdapterOpenAIChatCompletions, parsed),
		GeneratedAssets: []GeneratedAsset{{
			Kind:          GeneratedAssetKindAudio,
			B64JSON:       base64.StdEncoding.EncodeToString(data),
			MIMEType:      mimeTypes[format],
			ExternalID:    getString(audio["id"]),
			RevisedPrompt: text,
		}},
	}, nil
}

func miMoEncodedAudio(data []byte, format string) bool {
	switch format {
	case miniMaxFormatWAV:
		return miMoWAVAudio(data)
	case miniMaxFormatMP3:
		return miMoMP3Audio(data)
	case miMoFormatPCM16:
		return len(data) > 0 && len(data)%2 == 0
	default:
		return false
	}
}

func miMoWAVAudio(data []byte) bool {
	return len(data) >= 44 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE"
}

func miMoMP3Audio(data []byte) bool {
	return len(data) >= 3 && string(data[:3]) == "ID3" || len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0
}
