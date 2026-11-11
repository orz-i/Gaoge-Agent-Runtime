package transport

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"
)

func (c *Client) generateMiniMaxMedia(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	if err := rejectUnsupportedImageMask(input.ImageEditMask, route.Protocol); err != nil {
		return nil, err
	}
	var payload map[string]interface{}
	var path string
	var err error
	switch route.Protocol {
	case AdapterMiniMaxImage:
		payload, err = buildMiniMaxImageRequest(route.UpstreamModel, input)
		path = miniMaxImagePath
	case AdapterMiniMaxSpeech, AdapterMiniMaxMusic:
		payload, err = buildMiniMaxAudioRequest(route.UpstreamModel, route.Protocol, input)
		path = miniMaxSpeechPath
		if route.Protocol == AdapterMiniMaxMusic {
			path = miniMaxMusicPath
		}
	default:
		return nil, ErrUnsupportedAdapter
	}
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()
	parsed, body, debug, err := c.doMiniMaxRequest(requestCtx, route, http.MethodPost, path, encoded)
	if err != nil {
		return nil, err
	}
	output, err := parseMiniMaxMediaOutput(route.Protocol, parsed, payload)
	if err != nil {
		return nil, attachUpstreamDebug(err, debug)
	}
	output.RawJSON = string(body)
	output.Debug = debug
	return output, nil
}

func buildMiniMaxImageRequest(model string, input GenerateInput) (map[string]interface{}, error) {
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	if strings.TrimSpace(prompt) == "" || utf8.RuneCountInString(prompt) > 1500 {
		return nil, fmt.Errorf("%w: MiniMax image prompt must contain 1-1500 characters", errCategoryEF97804E6D)
	}
	payload := map[string]interface{}{valueModel2D31E8EB: strings.TrimSpace(model), valuePrompt9071DA16: prompt, valueResponseFormat1C776FA1: valueUrl5D7AD561}
	copyMiniMaxOptions(payload, input.Options, valueAspectRatioA5C1E4FE, "width", "height", valueResponseFormat1C776FA1, valueSeedC8D041FC, "n", "prompt_optimizer", "aigc_watermark", "style")
	if _, specified := payload["n"]; specified && !miniMaxIntegerInRange(payload, "n", 1, 9) {
		return nil, fmt.Errorf("%w: MiniMax image n must be an integer from 1 to 9", errCategoryEF97804E6D)
	}
	format := modelParamString(payload, valueResponseFormat1C776FA1)
	if format != valueUrl5D7AD561 && format != miniMaxImageBase64 {
		return nil, fmt.Errorf("%w: MiniMax image response_format must be url or base64", errCategoryEF97804E6D)
	}
	references := make([]map[string]interface{}, 0)
	for _, image := range collectImageInputParts(input.Messages) {
		references = append(references, map[string]interface{}{valueTypeF2B37301: "character", "image_file": miniMaxImageDataURL(image)})
	}
	if len(references) > 0 {
		payload["subject_reference"] = references
	}
	return payload, nil
}

func miniMaxImageDataURL(image ContentPart) string {
	mimeType := strings.TrimSpace(image.MimeType)
	if mimeType == "" {
		mimeType = http.DetectContentType(image.Data)
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(image.Data)
}

func parseMiniMaxImageOutput(parsed map[string]interface{}) (*GenerateOutput, error) {
	data := asMap(parsed[valueData7C9F25C7])
	output := &GenerateOutput{ResponseID: getString(parsed["id"])}
	for _, url := range modelParamStringList(data, miniMaxImageURLsInput) {
		output.GeneratedImages = append(output.GeneratedImages, GeneratedImage{URL: url})
	}
	for _, encoded := range modelParamStringList(data, "image_base64") {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("invalid MiniMax image base64: %w", err)
		}
		mimeType := http.DetectContentType(decoded)
		if !strings.HasPrefix(mimeType, "image/") {
			return nil, fmt.Errorf("%w: MiniMax returned non-image base64 data", errMiniMaxInvalidResponse)
		}
		output.GeneratedImages = append(output.GeneratedImages, GeneratedImage{B64JSON: encoded, MIMEType: mimeType})
	}
	if len(output.GeneratedImages) == 0 {
		return nil, fmt.Errorf("%w: MiniMax returned no images", errMiniMaxInvalidResponse)
	}
	return output, nil
}

func buildMiniMaxAudioRequest(model, protocol string, input GenerateInput) (map[string]interface{}, error) {
	if len(collectImageInputParts(input.Messages)) > 0 {
		return nil, fmt.Errorf("%w: MiniMax audio generation does not accept image input", ErrUnsupportedAdapter)
	}
	payload := map[string]interface{}{valueModel2D31E8EB: strings.TrimSpace(model), valueStream1E52954F: false, miniMaxOutputFormatOption: miniMaxOutputHex, miniMaxAudioSettingOption: map[string]interface{}{miniMaxFormatKey: miniMaxFormatMP3}}
	copyMiniMaxOptions(payload, input.Options, miniMaxOutputFormatOption, miniMaxAudioSettingOption, "aigc_watermark")
	if err := validateMiniMaxAudioSettings(protocol, payload); err != nil {
		return nil, err
	}
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	if protocol == AdapterMiniMaxSpeech {
		if err := applyMiniMaxSpeechRequest(payload, prompt, input.Options); err != nil {
			return nil, err
		}
	} else {
		payload[valuePrompt9071DA16] = prompt
		copyMiniMaxOptions(payload, input.Options, "lyrics", "lyrics_optimizer", "is_instrumental", miniMaxAudioURLInput, "audio_base64", miniMaxCoverFeatureInput)
		if err := normalizeMiniMaxMusicRequest(payload); err != nil {
			return nil, err
		}
	}
	return payload, nil
}

func validateMiniMaxAudioSettings(protocol string, payload map[string]interface{}) error {
	setting, ok := payload[miniMaxAudioSettingOption].(map[string]interface{})
	if !ok {
		return fmt.Errorf("%w: MiniMax audio_setting must be an object", errCategoryEF97804E6D)
	}
	audioFormat := modelParamString(setting, miniMaxFormatKey)
	if _, err := miniMaxAudioMIMEType(audioFormat); err != nil {
		return err
	}
	if protocol == AdapterMiniMaxMusic && !slices.Contains([]string{"", miniMaxFormatMP3, miniMaxFormatWAV, "pcm"}, audioFormat) {
		return fmt.Errorf("%w: MiniMax music format must be mp3, wav or pcm", errCategoryEF97804E6D)
	}
	format := modelParamString(payload, miniMaxOutputFormatOption)
	if format != valueUrl5D7AD561 && format != miniMaxOutputHex {
		return fmt.Errorf("%w: MiniMax audio output_format must be url or hex", errCategoryEF97804E6D)
	}
	return nil
}

func applyMiniMaxSpeechRequest(payload map[string]interface{}, prompt string, options map[string]interface{}) error {
	if strings.TrimSpace(prompt) == "" || utf8.RuneCountInString(prompt) >= 10000 {
		return fmt.Errorf("%w: MiniMax speech text must contain 1-9999 characters", errCategoryEF97804E6D)
	}
	payload[valueText53ED4E15] = prompt
	payload[miniMaxVoiceSettingOption] = map[string]interface{}{"voice_id": "male-qn-qingse"}
	copyMiniMaxOptions(payload, options, miniMaxVoiceSettingOption, "pronunciation_dict", "timbre_weights", "language_boost", "voice_modify", "subtitle_enable", "subtitle_type")
	voice, ok := payload[miniMaxVoiceSettingOption].(map[string]interface{})
	if !ok {
		return fmt.Errorf("%w: MiniMax voice_setting must be an object", errCategoryEF97804E6D)
	}
	voice = cloneMap(voice)
	if _, specified := voice["voice_id"]; !specified {
		voice["voice_id"] = "male-qn-qingse"
	}
	if _, specified := asMap(options[miniMaxVoiceSettingOption])["voice_id"]; !specified && len(asSlice(payload["timbre_weights"])) > 0 {
		voice["voice_id"] = ""
	}
	payload[miniMaxVoiceSettingOption] = voice
	return nil
}

func normalizeMiniMaxMusicRequest(payload map[string]interface{}) error {
	prompt := modelParamString(payload, valuePrompt9071DA16)
	lyrics := modelParamString(payload, "lyrics")
	if utf8.RuneCountInString(prompt) > 2000 || utf8.RuneCountInString(lyrics) > 3500 {
		return fmt.Errorf("%w: MiniMax music prompt or lyrics exceeds its character limit", errCategoryEF97804E6D)
	}
	if modelParamString(payload, valueModel2D31E8EB) == miniMaxModelMusicCover {
		return normalizeMiniMaxMusicCover(payload, prompt, lyrics)
	}
	if prompt == "" && lyrics == "" {
		return fmt.Errorf("%w: MiniMax music requires a prompt or lyrics", errCategoryEF97804E6D)
	}
	if modelParamBool(payload, "is_instrumental") && prompt == "" {
		return fmt.Errorf("%w: MiniMax instrumental music requires a prompt", errCategoryEF97804E6D)
	}
	return normalizeMiniMaxVocalLyrics(payload, lyrics)
}

func normalizeMiniMaxVocalLyrics(payload map[string]interface{}, lyrics string) error {
	if lyrics != "" || modelParamBool(payload, "is_instrumental") {
		return nil
	}
	if enabled, specified := modelParamBoolValue(payload, "lyrics_optimizer"); specified && !enabled {
		return fmt.Errorf("%w: MiniMax vocal music requires lyrics or lyrics_optimizer", errCategoryEF97804E6D)
	}
	payload["lyrics_optimizer"] = true
	return nil
}

func normalizeMiniMaxMusicCover(payload map[string]interface{}, prompt, lyrics string) error {
	sources := 0
	for _, key := range []string{miniMaxAudioURLInput, "audio_base64", miniMaxCoverFeatureInput} {
		if modelParamString(payload, key) != "" {
			sources++
		}
	}
	if sources != 1 || utf8.RuneCountInString(prompt) < 10 || utf8.RuneCountInString(prompt) > 300 {
		return fmt.Errorf("%w: MiniMax cover requires a 10-300 character prompt and exactly one audio source", errCategoryEF97804E6D)
	}
	lyricsRequired := lyrics != "" || modelParamString(payload, miniMaxCoverFeatureInput) != ""
	if lyricsRequired && (utf8.RuneCountInString(lyrics) < 10 || utf8.RuneCountInString(lyrics) > 1000) {
		return fmt.Errorf("%w: MiniMax cover lyrics must contain 10-1000 characters", errCategoryEF97804E6D)
	}
	delete(payload, "lyrics_optimizer")
	delete(payload, "is_instrumental")
	return nil
}

func parseMiniMaxAudioOutput(parsed, payload map[string]interface{}) (*GenerateOutput, error) {
	audio, err := miniMaxAudioData(parsed)
	if err != nil {
		return nil, err
	}
	extra := asMap(parsed["extra_info"])
	format := modelParamString(extra, "audio_format")
	if format == "" {
		format = modelParamString(asMap(payload[miniMaxAudioSettingOption]), miniMaxFormatKey)
	}
	mimeType, err := miniMaxAudioMIMEType(format)
	if err != nil {
		return nil, err
	}
	asset := GeneratedAsset{Kind: GeneratedAssetKindAudio, MIMEType: mimeType, ExternalID: getString(parsed["trace_id"])}
	if modelParamString(payload, miniMaxOutputFormatOption) == valueUrl5D7AD561 {
		asset.URL = audio
	} else {
		decoded, decodeErr := hex.DecodeString(audio)
		if decodeErr != nil {
			return nil, fmt.Errorf("invalid MiniMax audio hex: %w", decodeErr)
		}
		asset.B64JSON = base64.StdEncoding.EncodeToString(decoded)
	}
	durationMS, _ := modelParamFloat(extra, "audio_length")
	if durationMS <= 0 {
		durationMS, _ = modelParamFloat(extra, "music_duration")
	}
	if durationMS > 0 {
		asset.MetadataJSON, err = miniMaxAssetMetadata(durationMS / 1000)
		if err != nil {
			return nil, err
		}
	}
	// Character/duration usage must not be invented as token usage.
	rawUsage, err := json.Marshal(extra)
	if err != nil {
		return nil, err
	}
	return &GenerateOutput{ResponseID: asset.ExternalID, Text: asset.URL, GeneratedAssets: []GeneratedAsset{asset}, Usage: Usage{RawUsageJSON: string(rawUsage)}}, nil
}

func miniMaxAudioMIMEType(format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", miniMaxFormatMP3:
		return "audio/mpeg", nil
	case miniMaxFormatWAV, "pcmu_wav":
		return "audio/wav", nil
	case "flac":
		return "audio/flac", nil
	case "pcm":
		return "audio/pcm", nil
	case "pcmu_raw":
		return "audio/basic", nil
	case "opus":
		return "audio/ogg", nil
	default:
		return "", fmt.Errorf("%w: unsupported MiniMax audio format %q", errCategoryEF97804E6D, format)
	}
}

func copyMiniMaxOptions(payload, options map[string]interface{}, keys ...string) {
	for _, key := range keys {
		if value, ok := options[key]; ok && value != nil {
			payload[key] = value
		}
	}
}

func parseMiniMaxMediaOutput(protocol string, parsed, payload map[string]interface{}) (*GenerateOutput, error) {
	if protocol == AdapterMiniMaxImage {
		return parseMiniMaxImageOutput(parsed)
	}
	return parseMiniMaxAudioOutput(parsed, payload)
}

func miniMaxAudioData(parsed map[string]interface{}) (string, error) {
	data := asMap(parsed[valueData7C9F25C7])
	if rawStatus, specified := data[interactionKeyStatus]; specified {
		if status, ok := modelParamFloat(data, interactionKeyStatus); !ok || status != 2 {
			return "", fmt.Errorf("%w: MiniMax returned incomplete audio (status=%v)", errMiniMaxInvalidResponse, rawStatus)
		}
	}
	audio := strings.TrimSpace(getString(data[GeneratedAssetKindAudio]))
	if audio == "" {
		return "", fmt.Errorf("%w: MiniMax returned no audio", errMiniMaxInvalidResponse)
	}
	return audio, nil
}

func miniMaxAssetMetadata(duration interface{}) (string, error) {
	metadata, err := json.Marshal(map[string]interface{}{"durationSeconds": duration, "source": "provider"})
	return string(metadata), err
}
