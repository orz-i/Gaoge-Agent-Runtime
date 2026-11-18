package transport

import (
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

func buildBailianAudioRequest(model, protocol string, input GenerateInput) (map[string]interface{}, string, error) {
	text := buildOpenAIImageGenerationPrompt(input.Messages)
	payloadInput := bailianOptionSubset(input.Options, miniMaxFormatKey, "sample_rate", "volume", "rate", "seed", "enable_aigc_tag", "aigc_propagator", "aigc_propagate_id")
	path := bailianSpeechPath
	var err error
	switch protocol {
	case AdapterBailianSpeech:
		payloadInput[valueText1D495308] = text
		for key, value := range bailianOptionSubset(input.Options, "voice", "instruction", "language_hints", "pitch", "bit_rate", "enable_ssml", "hot_fix") {
			payloadInput[key] = value
		}
		if modelParamString(payloadInput, "voice") == "" {
			payloadInput["voice"] = "longanhuan_v3.6"
		}
		err = validateBailianSpeech(payloadInput)
	case AdapterBailianAudio:
		payloadInput["text_prompt"] = text
		for key, value := range bailianOptionSubset(input.Options, "references", "channels", "enable_cbr", "bit_rate", "quality") {
			payloadInput[key] = value
		}
		err = validateBailianAudioScene(payloadInput)
	case AdapterBailianMusic:
		path = bailianMusicPath
		payloadInput = bailianOptionSubset(input.Options, "lyrics", "is_instrumental", "gender", miniMaxFormatKey, "enable_aigc_watermark")
		if text != "" {
			payloadInput["prompt"] = text
		}
		err = validateBailianMusic(model, payloadInput)
	default:
		err = ErrUnsupportedAdapter
	}
	if err != nil {
		return nil, "", err
	}
	return bailianNativePayload(model, payloadInput, nil), path, nil
}

func validateBailianSpeech(input map[string]interface{}) error {
	if strings.TrimSpace(getString(input[valueText1D495308])) == "" {
		return fmt.Errorf("%w: speech text is required", errBailianInput)
	}
	if err := validateBailianSpeechCodec(input); err != nil {
		return err
	}
	if err := validateBailianInteger(input, "seed", 0, 65535); err != nil {
		return err
	}
	return validateBailianAudioControls(input)
}
func validateBailianSpeechCodec(input map[string]interface{}) error {
	if err := validateBailianEnum(input, miniMaxFormatKey, bailianFormatMP3, bailianFormatWAV, bailianFormatPCM, bailianFormatOpus); err != nil {
		return err
	}
	if err := validateBailianSampleRate(input, false); err != nil {
		return err
	}
	if modelParamString(input, miniMaxFormatKey) == bailianFormatOpus && (toInt64(input["sample_rate"]) == 22050 || toInt64(input["sample_rate"]) == 44100) {
		return fmt.Errorf("%w: Opus sample rate is unsupported", errBailianInput)
	}
	if _, exists := input["bit_rate"]; exists && modelParamString(input, miniMaxFormatKey) != bailianFormatOpus {
		return fmt.Errorf("%w: speech bit_rate requires Opus", errBailianInput)
	}
	return validateBailianInteger(input, "bit_rate", 6, 510)
}

func validateBailianSampleRate(input map[string]interface{}, scene bool) error {
	if _, exists := input["sample_rate"]; !exists {
		return nil
	}
	rates := []float64{8000, 12000, 16000, 22050, 24000, 44100, 48000}
	if scene {
		rates = []float64{8000, 16000, 24000, 44100, 48000}
	}
	for _, rate := range rates {
		if bailianIntegerInRange(input, "sample_rate", rate, rate) {
			return nil
		}
	}
	return fmt.Errorf("%w: unsupported sample_rate", errBailianInput)
}

func validateBailianAudioControls(input map[string]interface{}) error {
	if err := validateBailianInteger(input, "volume", 0, 100); err != nil {
		return err
	}
	for _, key := range []string{"rate", "pitch"} {
		if _, specified := input[key]; specified {
			value, valid := modelParamFloat(input, key)
			if !valid || math.IsNaN(value) || math.IsInf(value, 0) || value < 0.5 || value > 2 {
				return fmt.Errorf("%w: %s must be in [0.5,2.0]", errBailianInput, key)
			}
		}
	}
	return nil
}

func validateBailianAudioScene(input map[string]interface{}) error {
	length := utf8.RuneCountInString(getString(input["text_prompt"]))
	if length == 0 || length > 3000 {
		return fmt.Errorf("%w: audio text_prompt must contain 1-3000 characters", errBailianInput)
	}
	if err := validateBailianEnum(input, miniMaxFormatKey, bailianFormatWAV, bailianFormatMP3, bailianFormatPCM); err != nil {
		return err
	}
	if err := validateBailianSampleRate(input, true); err != nil {
		return err
	}
	if err := validateBailianInteger(input, "channels", 1, 2); err != nil {
		return err
	}
	if err := validateBailianInteger(input, "quality", 0, 9); err != nil {
		return err
	}
	if err := validateBailianAudioControls(input); err != nil {
		return err
	}
	return validateBailianAudioReferences(input)
}

func validateBailianAudioReferences(input map[string]interface{}) error {
	if raw, exists := input["references"]; exists {
		references, valid := raw.([]interface{})
		if !valid || len(references) > 3 {
			return fmt.Errorf("%w: audio references must be an array of at most 3 items", errBailianInput)
		}
		for _, rawReference := range references {
			if err := validateBailianAudioReference(asMap(rawReference)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateBailianMusic(model string, input map[string]interface{}) error {
	prompt, lyrics := getString(input["prompt"]), getString(input["lyrics"])
	if strings.TrimSpace(prompt) == "" && strings.TrimSpace(lyrics) == "" {
		return fmt.Errorf("%w: music prompt or lyrics is required", errBailianInput)
	}
	if utf8.RuneCountInString(prompt) > 2000 {
		return fmt.Errorf("%w: music prompt exceeds 2000 characters", errBailianInput)
	}
	if err := validateBailianLyrics(lyrics); err != nil {
		return err
	}
	if err := validateBailianMusicMode(model, input); err != nil {
		return err
	}
	if err := validateBailianEnum(input, "gender", "male", "female"); err != nil {
		return err
	}
	return validateBailianEnum(input, miniMaxFormatKey, bailianFormatMP3, bailianFormatWAV)
}
func validateBailianLyrics(lyrics string) error {
	if lyrics == "" {
		return nil
	}
	limit := 2000
	for _, r := range lyrics {
		if unicode.Is(unicode.Han, r) {
			limit = 350
			break
		}
	}
	length := utf8.RuneCountInString(lyrics)
	if length < 5 || length > limit {
		return fmt.Errorf("%w: lyrics length exceeds language limits", errBailianInput)
	}
	return nil
}
func validateBailianMusicMode(model string, input map[string]interface{}) error {
	if model == "fun-music-preview" {
		if getString(input["prompt"]) == "" {
			return fmt.Errorf("%w: preview model requires prompt", errBailianInput)
		}
		if _, exists := input["gender"]; exists {
			return fmt.Errorf("%w: preview model does not support gender", errBailianInput)
		}
	}
	if modelParamBool(input, "is_instrumental") && getString(input["lyrics"]) != "" {
		return fmt.Errorf("%w: instrumental music does not use lyrics", errBailianInput)
	}
	return nil
}

func validateBailianAudioReference(reference map[string]interface{}) error {
	uri, data := getString(reference["audio_url"]), getString(reference["audio_data"])
	if (uri == "") == (data == "") {
		return fmt.Errorf("%w: specify one audio_url or audio_data", errBailianInput)
	}
	if uri != "" {
		if !bailianMediaURI(uri, false) {
			return errBailianInput
		}
		return nil
	}
	if !strings.HasPrefix(data, "data:audio/") || strings.HasPrefix(data, "data:audio/pcm") || len(data) > 10*1024*1024*4/3 || !bailianMediaURI(data, true) {
		return fmt.Errorf("%w: invalid reference audio_data", errBailianInput)
	}
	return nil
}
