package transport

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

func buildBailianTranscriptionRequest(model string, input GenerateInput) (bailianRequest, error) {
	audioURL, audioData, err := bailianTranscriptionSource(input.Options)
	if err != nil {
		return bailianRequest{}, err
	}
	if strings.HasPrefix(model, "qwen3-asr-flash") && !strings.Contains(model, "filetrans") {
		return buildBailianShortASR(model, input, audioURL, audioData), nil
	}
	if audioData != "" {
		return bailianRequest{}, fmt.Errorf("%w: file transcription requires a public audio URL", errBailianInput)
	}
	nativeInput := map[string]interface{}{"file_urls": []string{audioURL}}
	if value, exists := input.Options["context"]; exists {
		nativeInput["context"] = value
	}
	parameters := bailianOptionSubset(input.Options, "channel_id", "keep_dialect", "vocabulary_id", "vocabulary", "language_hints", "disfluency_removal_enabled", "diarization_enabled", "speaker_count", "timestamp_alignment_enabled", "enable_itn", "special_word_filter", "audio_event_detection_enabled")
	if err = validateBailianInteger(parameters, "speaker_count", 1, 100); err != nil {
		return bailianRequest{}, err
	}
	return bailianRequest{path: bailianNativeBase + bailianTranscriptionPath, payload: bailianNativePayload(model, nativeInput, parameters), async: true}, nil
}

func (c *Client) parseBailianTranscription(ctx context.Context, route RouteConfig, parsed map[string]interface{}) (*GenerateOutput, error) {
	if _, exists := parsed["choices"]; exists {
		output := buildGenerateOutputFromParsedForAdapter(EndpointChatCompletions, AdapterBailianChat, parsed, false)
		if output.Text == "" {
			return nil, fmt.Errorf("%w: empty transcript", errBailianResponse)
		}
		return output, nil
	}
	task := asMap(parsed["output"])
	results := asSlice(task["results"])
	if len(results) != 1 {
		return nil, fmt.Errorf("%w: expected one transcription result", errBailianResponse)
	}
	result := asMap(results[0])
	if getString(result["subtask_status"]) != "SUCCEEDED" {
		return nil, fmt.Errorf("%w: transcription subtask failed: %s %s", errBailianResponse, getString(result["code"]), getString(result["message"]))
	}
	resultURL := getString(result["transcription_url"])
	if !bailianMediaURI(resultURL, false) {
		return nil, fmt.Errorf("%w: missing transcription_url", errBailianResponse)
	}
	// Result URLs carry their own storage signature. Never forward upstream keys
	// or custom headers to storage, even when both URLs share a host.
	downloadRoute := route
	downloadRoute.APIKey, downloadRoute.HeadersJSON = "", ""
	body, _, err := c.doMediaJSONRequest(ctx, downloadRoute, http.MethodGet, resultURL, nil)
	if err != nil {
		return nil, err
	}
	transcript, err := decodeBailianJSON(body)
	if err != nil {
		return nil, err
	}
	text, err := bailianTranscriptText(transcript)
	if err != nil {
		return nil, err
	}
	return &GenerateOutput{ResponseID: getString(task[bailianTaskID]), Text: text, Usage: bailianUsage(parsed), GeneratedAssets: []GeneratedAsset{{Kind: "transcript", URL: resultURL, MIMEType: valueApplicationJson71FA0DF4, ExternalID: getString(task[bailianTaskID]), MetadataJSON: string(body)}}}, nil
}

func bailianTranscriptionSource(options map[string]interface{}) (string, string, error) {
	urls, err := bailianStringList(options, "file_urls", 1)
	if err != nil {
		return "", "", err
	}
	audioURL, audioData := modelParamString(options, "audio_url"), modelParamString(options, "audio_data")
	count := len(urls)
	for _, source := range []string{audioURL, audioData} {
		if source != "" {
			count++
		}
	}
	if count != 1 {
		return "", "", fmt.Errorf("%w: specify exactly one audio source", errBailianInput)
	}
	if len(urls) > 0 {
		audioURL = urls[0]
	}
	if err = validateBailianASRSource(audioURL, audioData); err != nil {
		return "", "", err
	}
	return audioURL, audioData, nil
}
func validateBailianASRSource(audioURL, audioData string) error {
	if audioURL != "" && !bailianMediaURI(audioURL, false) {
		return fmt.Errorf("%w: invalid audio URL", errBailianInput)
	}
	if audioData != "" && (!strings.HasPrefix(audioData, "data:audio/") || !bailianMediaURI(audioData, true)) {
		return fmt.Errorf("%w: audio_data must be an audio data URI", errBailianInput)
	}
	return nil
}

func buildBailianShortASR(model string, input GenerateInput, audioURL, audioData string) bailianRequest {
	if audioURL == "" {
		audioURL = audioData
	}
	messages := []interface{}{}
	if contextText := buildOpenAIImageGenerationPrompt(input.Messages); contextText != "" {
		messages = append(messages, map[string]interface{}{valueRole01C0A112: valueSystemF78794CC, valueContent03EE2C26: contextText})
	}
	messages = append(messages, map[string]interface{}{valueRole01C0A112: valueUserD301E989, valueContent03EE2C26: []interface{}{map[string]interface{}{valueType5AC5AA71: "input_audio", "input_audio": map[string]interface{}{valueDataE4BFD03E: audioURL}}}})
	payload := map[string]interface{}{valueModel2D31E8EB: model, EndpointMessages: messages, "stream": false}
	if options, exists := input.Options["asr_options"]; exists {
		payload["asr_options"] = options
	}
	return bailianRequest{path: "/compatible-mode/v1/chat/completions", payload: payload}
}

func bailianTranscriptText(transcript map[string]interface{}) (string, error) {
	tracks, valid := transcript["transcripts"].([]interface{})
	if !valid {
		return "", fmt.Errorf("%w: missing transcripts", errBailianResponse)
	}
	texts := []string{}
	for _, raw := range tracks {
		if text := getString(asMap(raw)[valueText1D495308]); text != "" {
			texts = append(texts, text)
		}
	}

	return strings.Join(texts, "\n"), nil
}
