package transport

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	senseAudioVideoReferenceRole = "reference"
	senseAudioStatusSuccess      = "success"
	senseAudioImagePath          = "/v1/image/sync"
	senseAudioSpeechPath         = "/v1/t2a_v2"
	senseAudioVideoCreatePath    = "/v1/video/create"
	senseAudioVideoStatusPath    = "/v1/video/status"
	senseAudioMusicCreatePath    = "/v2/music/song/create"
	senseAudioMusicStatusPath    = "/v1/music/song/pending/"
)

func (c *Client) generateSenseAudioMedia(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	if err := rejectUnsupportedImageMask(input.ImageEditMask, route.Protocol); err != nil {
		return nil, err
	}
	timeout := resolveReadTimeout(route.ReadTimeoutMS)
	if senseAudioAsyncMedia(route.Protocol) {
		timeout = resolveMediaPollTimeout(RouteConfig{}, input.Options)
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if input.ResumeMediaTaskID != "" {
		return c.resumeSenseAudioMedia(requestCtx, route, input)
	}
	return c.createSenseAudioMedia(requestCtx, route, input)
}

func (c *Client) resumeSenseAudioMedia(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	taskID := strings.TrimSpace(input.ResumeMediaTaskID)
	if !senseAudioAsyncMedia(route.Protocol) || input.CreateMediaTaskOnly ||
		taskID == "" || len(taskID) > 256 || taskID != input.ResumeMediaTaskID || strings.ContainsAny(taskID, "\r\n\t ") {
		return nil, fmt.Errorf("%w: invalid provider task recovery", errSenseAudioInput)
	}
	// This is GET-only: never generate a second paid task on recovery.
	return c.pollSenseAudioTask(ctx, route, taskID, input.Options)
}

func (c *Client) createSenseAudioMedia(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	if input.CreateMediaTaskOnly && !senseAudioAsyncMedia(route.Protocol) {
		return nil, fmt.Errorf("%w: create-only is restricted to paid asynchronous media", errSenseAudioInput)
	}
	path, payload, err := senseAudioMediaRequest(route, input)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	route.SingleProviderAttempt = true // Never follow paid POST redirects.
	parsed, body, debug, err := c.doSenseAudioJSON(ctx, route, http.MethodPost, path, encoded)
	if err != nil {
		return nil, err
	}
	output, err := c.senseAudioMediaOutput(ctx, route, input.Options, parsed, body, debug, payload, input.CreateMediaTaskOnly)
	if err != nil {
		if senseAudioAsyncMedia(route.Protocol) {
			return nil, err // Preserve uncertain task result and cancellation.
		}
		return nil, attachUpstreamDebug(err, debug)
	}
	if output.RawJSON == "" {
		output.RawJSON = string(body)
	}
	if output.Debug == nil {
		output.Debug = debug
	}
	return output, nil
}

func senseAudioAsyncMedia(protocol string) bool {
	return protocol == AdapterSenseAudioVideo || protocol == AdapterSenseAudioMusic
}

func senseAudioMediaRequest(route RouteConfig, input GenerateInput) (string, map[string]interface{}, error) {
	switch route.Protocol {
	case AdapterSenseAudioImage:
		payload, err := senseAudioImageRequest(route.UpstreamModel, input)
		return senseAudioImagePath, payload, err
	case AdapterSenseAudioSpeech:
		payload, err := senseAudioSpeechRequest(route.UpstreamModel, input)
		return senseAudioSpeechPath, payload, err
	case AdapterSenseAudioVideo:
		payload, err := senseAudioVideoRequest(route.UpstreamModel, input)
		return senseAudioVideoCreatePath, payload, err
	case AdapterSenseAudioMusic:
		payload, err := senseAudioMusicRequest(route.UpstreamModel, input)
		return senseAudioMusicCreatePath, payload, err
	default:
		return "", nil, ErrUnsupportedAdapter
	}
}

func (c *Client) senseAudioMediaOutput(ctx context.Context, route RouteConfig, options map[string]interface{},
	parsed map[string]interface{}, body []byte, debug *UpstreamDebugSnapshot, payload map[string]interface{}, createOnly bool,
) (*GenerateOutput, error) {
	switch route.Protocol {
	case AdapterSenseAudioImage:
		return senseAudioImageOutput(parsed)
	case AdapterSenseAudioSpeech:
		return senseAudioSpeechOutput(parsed, payload)
	case AdapterSenseAudioVideo, AdapterSenseAudioMusic:
		taskID := strings.TrimSpace(getString(parsed["task_id"]))
		if taskID == "" {
			return nil, &UpstreamError{StatusCode: http.StatusBadGateway,
				Message: "SenseAudio creation outcome unknown: missing task_id; do not retry creation",
				Body:    string(body), Debug: debug}
		}
		if createOnly {
			// Unary response is the host's checkpoint input. This RPC never
			// polls; Conversation must durably CAS the task ID first.
			return &GenerateOutput{ResponseID: taskID}, nil
		}
		return c.pollSenseAudioTask(ctx, route, taskID, options)
	default:
		return nil, ErrUnsupportedAdapter
	}
}

func senseAudioImageRequest(model string, input GenerateInput) (map[string]interface{}, error) {
	prompt := strings.TrimSpace(buildOpenAIImageGenerationPrompt(input.Messages))
	if prompt == "" || strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("%w: image requires model and prompt", errSenseAudioInput)
	}
	refs := collectImageInputParts(input.Messages)
	if len(refs) > 1 {
		return nil, fmt.Errorf("%w: image accepts at most one reference", errSenseAudioInput)
	}
	payload := map[string]interface{}{valueModel0397160C: strings.TrimSpace(model), "prompt": prompt}
	copyMiniMaxOptions(payload, input.Options, "size", "seed")
	if len(refs) == 1 {
		payload[senseAudioVideoReferenceRole] = miniMaxImageDataURL(refs[0])
	} else if reference := modelParamString(input.Options, senseAudioVideoReferenceRole); reference != "" {
		payload[senseAudioVideoReferenceRole] = reference
	}
	// Without a reference the upstream requires size. Only provide a default
	// for models whose accepted dimensions are explicitly documented.
	if payload[senseAudioVideoReferenceRole] == nil && modelParamString(payload, "size") == "" {
		size := senseAudioDefaultImageSize(model)
		if size == "" {
			return nil, fmt.Errorf("%w: size is required for image model %q without reference", errSenseAudioInput, model)
		}
		payload["size"] = size
	}
	return payload, nil
}

func senseAudioDefaultImageSize(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "senseaudio-image-2.0-260319":
		return "1024x1024"
	case "doubao-seedream-5-0-260128", "sensenova-u1-fast":
		return "2048x2048"
	default:
		return ""
	}
}

func senseAudioImageOutput(parsed map[string]interface{}) (*GenerateOutput, error) {
	uri := strings.TrimSpace(getString(parsed[valueUrl5D7AD561]))
	if uri == "" {
		return nil, fmt.Errorf("%w: image generation returned no URL", errSenseAudioResponse)
	}
	return &GenerateOutput{GeneratedImages: []GeneratedImage{{URL: uri}}}, nil
}

func senseAudioSpeechRequest(model string, input GenerateInput) (map[string]interface{}, error) {
	prompt := strings.TrimSpace(buildOpenAIImageGenerationPrompt(input.Messages))
	if prompt == "" || strings.TrimSpace(model) == "" || len(collectImageInputParts(input.Messages)) != 0 {
		return nil, fmt.Errorf("%w: speech requires model and text without image references", errSenseAudioInput)
	}
	voiceSetting := map[string]interface{}{"voice_id": "female_0033_b", "speed": 1.0, "vol": 1, "pitch": 0}
	if voice := modelParamString(input.Options, "voice"); voice != "" {
		voiceSetting["voice_id"] = voice
	}
	if speed, ok := modelParamFloat(input.Options, "speed"); ok {
		voiceSetting["speed"] = speed
	}
	if options := modelParamMap(input.Options, "voice_setting"); options != nil {
		copyMiniMaxOptions(voiceSetting, options, "voice_id", "speed", "vol", "pitch")
	}
	audioSetting := map[string]interface{}{"format": "mp3", "sample_rate": 32000, "bitrate": 128000, "channel": 2}
	if format := modelParamString(input.Options, "format"); format != "" {
		audioSetting["format"] = format
	}
	if options := modelParamMap(input.Options, "audio_setting"); options != nil {
		copyMiniMaxOptions(audioSetting, options, "format", "sample_rate", "bitrate", "channel")
	}
	if _, err := miniMaxAudioMIMEType(modelParamString(audioSetting, "format")); err != nil {
		return nil, fmt.Errorf("%w: %w", errSenseAudioInput, err)
	}
	return map[string]interface{}{valueModel0397160C: strings.TrimSpace(model), valueText53ED4E15: prompt, "stream": false, "voice_setting": voiceSetting, "audio_setting": audioSetting}, nil
}

func senseAudioSpeechOutput(parsed, request map[string]interface{}) (*GenerateOutput, error) {
	data := asMap(parsed["data"])
	audio := strings.TrimSpace(getString(data["audio"]))
	if audio == "" {
		return nil, fmt.Errorf("%w: speech returned no audio data", errSenseAudioResponse)
	}
	extra := asMap(parsed["extra_info"])
	format := modelParamString(extra, "audio_format")
	if format == "" {
		format = modelParamString(asMap(request["audio_setting"]), "format")
	}
	mimeType, err := miniMaxAudioMIMEType(format)
	if err != nil {
		return nil, err
	}
	asset := GeneratedAsset{Kind: GeneratedAssetKindAudio, MIMEType: mimeType, ExternalID: getString(parsed["trace_id"])}
	// The official SenseAudio quick start specifies hexadecimal data.audio.
	// Guessing base64 from the content can silently corrupt generated assets.
	decoded, decodeErr := hex.DecodeString(audio)
	if decodeErr != nil || len(decoded) == 0 {
		return nil, fmt.Errorf("%w: invalid hexadecimal speech audio", errSenseAudioResponse)
	}
	asset.B64JSON = base64.StdEncoding.EncodeToString(decoded)
	rawUsage, err := json.Marshal(extra)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid usage: %w", errSenseAudioResponse, err)
	}
	asset.MetadataJSON, err = senseAudioSpeechDurationMetadata(extra)
	if err != nil {
		return nil, err
	}
	// Avoid storing the large encoded audio bytes a second time in RawJSON.
	responseSummary, err := json.Marshal(map[string]interface{}{"trace_id": asset.ExternalID, "extra_info": extra})
	if err != nil {
		return nil, fmt.Errorf("%w: invalid response metadata: %w", errSenseAudioResponse, err)
	}
	return &GenerateOutput{ResponseID: asset.ExternalID, Text: asset.URL, GeneratedAssets: []GeneratedAsset{asset}, Usage: Usage{RawUsageJSON: string(rawUsage)}, RawJSON: string(responseSummary)}, nil
}

func senseAudioSpeechDurationMetadata(extra map[string]interface{}) (string, error) {
	ms, ok := modelParamFloat(extra, "audio_length")
	if !ok || ms <= 0 {
		return "", nil
	}
	return miniMaxAssetMetadata(ms / 1000)
}

func senseAudioVideoRequest(model string, input GenerateInput) (map[string]interface{}, error) {
	prompt := strings.TrimSpace(buildOpenAIImageGenerationPrompt(input.Messages))
	if strings.TrimSpace(model) == "" || prompt == "" {
		return nil, fmt.Errorf("%w: video requires model and prompt", errSenseAudioInput)
	}
	media, err := senseAudioVideoMediaParts(input)
	if err != nil {
		return nil, err
	}
	content := append([]map[string]interface{}{{"type": "text", valueText53ED4E15: prompt}}, media...)
	// All three fields are required by the upstream. These documented defaults
	// can be overridden via authorized per-model options.
	payload := map[string]interface{}{
		valueModel0397160C: strings.TrimSpace(model), "content": content,
		"duration": 10, "resolution": "720p", "ratio": "16:9",
	}
	copyMiniMaxOptions(payload, input.Options, "duration", "resolution", "ratio", "watermark", "provider_specific", "timeout")
	if _, exists := input.Options["ratio"]; !exists {
		if ratio := modelParamString(input.Options, "aspect_ratio"); ratio != "" {
			payload["ratio"] = ratio
		}
	}
	return payload, nil
}

func senseAudioVideoMediaParts(input GenerateInput) ([]map[string]interface{}, error) {
	refs := miniMaxVideoImageURLs(input)
	roles := modelParamStringList(input.Options, "image_roles")
	if len(roles) > 0 && len(roles) != len(refs) {
		return nil, fmt.Errorf("%w: image roles must match reference count", errSenseAudioInput)
	}
	if len(refs) > 9 {
		return nil, fmt.Errorf("%w: too many video image references", errSenseAudioInput)
	}
	parts := make([]map[string]interface{}, 0, len(refs))
	firstFrames, lastFrames, usedReferences := 0, 0, false
	for idx, imageURL := range refs {
		role := senseAudioVideoRole(modelParamString(input.Options, "image_role"), roles, len(refs), idx)
		switch role {
		case miniMaxRoleFirstFrame:
			firstFrames++
		case miniMaxRoleLastFrame:
			lastFrames++
		case senseAudioVideoReferenceRole:
			usedReferences = true
		default:
			return nil, fmt.Errorf("%w: unknown video image role %q", errSenseAudioInput, role)
		}
		parts = append(parts, map[string]interface{}{"type": ContentPartImage, valueUrl5D7AD561: imageURL, "role": role})
	}
	if !validSenseAudioVideoRoleCounts(firstFrames, lastFrames, usedReferences) {
		return nil, fmt.Errorf("%w: invalid first/last frames or mixed reference modes", errSenseAudioInput)
	}
	return parts, nil
}

func validSenseAudioVideoRoleCounts(firstFrames, lastFrames int, usedReferences bool) bool {
	return firstFrames <= 1 && lastFrames <= 1 &&
		(lastFrames == 0 || firstFrames == 1) &&
		(!usedReferences || firstFrames+lastFrames == 0)
}

func senseAudioVideoRole(defaultRole string, roles []string, count, index int) string {
	role := defaultRole
	if len(roles) > 0 {
		role = roles[index]
	}
	if role == miniMaxRoleReferenceImage {
		return senseAudioVideoReferenceRole // Gaoge's generic authoring role uses this alias.
	}
	if role != "" {
		return role
	}
	switch count {
	case 1:
		return miniMaxRoleFirstFrame
	case 2:
		if index == 0 {
			return miniMaxRoleFirstFrame
		}
		return miniMaxRoleLastFrame
	default:
		return senseAudioVideoReferenceRole
	}
}

func senseAudioMusicRequest(model string, input GenerateInput) (map[string]interface{}, error) {
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("%w: music requires model", errSenseAudioInput)
	}
	if len(collectImageInputParts(input.Messages)) != 0 {
		return nil, fmt.Errorf("%w: music does not accept image references", errSenseAudioInput)
	}
	payload := map[string]interface{}{valueModel0397160C: strings.TrimSpace(model), "mode": "sing"}
	if prompt := strings.TrimSpace(buildOpenAIImageGenerationPrompt(input.Messages)); prompt != "" {
		payload["prompt"] = prompt
	}
	if modelParamBool(input.Options, "is_instrumental") {
		payload["mode"] = "instrumental"
	}
	copyMiniMaxOptions(payload, input.Options, "mode", "style", "lyrics", "audio_settings", "lyrics_settings", "vocal_settings", "cover_settings", "prebuilt_lyrics", "attachments", "model_settings", "music_settings")
	if _, hasLyrics := payload["lyrics"]; hasLyrics && payload["prebuilt_lyrics"] != nil {
		return nil, fmt.Errorf("%w: lyrics and prebuilt_lyrics are exclusive", errSenseAudioInput)
	}
	if _, exists := payload["audio_settings"]; !exists {
		payload["audio_settings"] = map[string]interface{}{"format": "mp3"}
	}
	return payload, nil
}

func (c *Client) pollSenseAudioTask(ctx context.Context, route RouteConfig, taskID string, options map[string]interface{}) (*GenerateOutput, error) {
	interval := resolveMediaPollInterval(options)
	if _, specified := modelParamIntValue(options, mediaPollIntervalOption); !specified {
		interval = 5 * time.Second // Official video guidance: poll every 5–10 seconds.
	}
	for {
		if err := waitMediaPoll(ctx, interval); err != nil {
			return nil, senseAudioTaskFailure(ctx, taskID, err, nil)
		}
		path := senseAudioTaskPollPath(route.Protocol, taskID)
		parsed, body, debug, err := c.doSenseAudioJSON(ctx, route, http.MethodGet, path, nil)
		if err != nil {
			return nil, senseAudioTaskFailure(ctx, taskID, err, debug)
		}
		if returnedID := strings.TrimSpace(getString(parsed["task_id"])); returnedID != "" && returnedID != taskID {
			return nil, senseAudioTaskFailure(ctx, taskID, fmt.Errorf("%w: query returned different task_id", errSenseAudioResponse), debug)
		}
		output, done, err := senseAudioTaskPollResult(route.Protocol, taskID, parsed, body, debug)
		if err != nil {
			return nil, err
		}
		if done {
			return output, nil
		}
	}
}

func senseAudioTaskPollPath(protocol, taskID string) string {
	if protocol == AdapterSenseAudioVideo {
		return senseAudioVideoStatusPath + "?id=" + url.QueryEscape(taskID)
	}
	return senseAudioMusicStatusPath + url.PathEscape(taskID)
}

func senseAudioTaskPollResult(protocol, taskID string, parsed map[string]interface{},
	body []byte, debug *UpstreamDebugSnapshot,
) (*GenerateOutput, bool, error) {
	status := strings.ToLower(strings.TrimSpace(getString(parsed["status"])))
	switch status {
	case "pending", "processing":
		return nil, false, nil
	case "completed":
		if protocol != AdapterSenseAudioVideo {
			return nil, true, senseAudioTaskFailure(context.Background(), taskID, fmt.Errorf("%w: unexpected music status completed", errSenseAudioResponse), debug)
		}
		output, err := senseAudioVideoOutput(parsed, taskID, body, debug)
		return output, true, err
	case senseAudioStatusSuccess:
		if protocol != AdapterSenseAudioMusic {
			return nil, true, senseAudioTaskFailure(context.Background(), taskID, fmt.Errorf("%w: unexpected video status success", errSenseAudioResponse), debug)
		}
		output, err := senseAudioMusicOutput(parsed, taskID, body, debug)
		return output, true, err
	case miniMaxStatusFailed:
		message := getString(parsed["error_message"])
		if message == "" {
			message = getString(parsed["fail_reason"])
		}
		// A confirmed terminal FAILED is a definite provider rejection and
		// must not be mistaken for an unknown paid outcome.
		return nil, true, &UpstreamError{StatusCode: http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("SenseAudio task_id=%s failed: %s", taskID, message),
			Body:    string(body), Debug: debug}
	default:
		return nil, true, senseAudioTaskFailure(context.Background(), taskID, fmt.Errorf("%w: unknown task status %q", errSenseAudioResponse, status), debug)
	}
}

func senseAudioTaskFailure(ctx context.Context, taskID string, err error, debug *UpstreamDebugSnapshot) error {
	if ctx.Err() != nil {
		return fmt.Errorf("SenseAudio task_id=%s may still be running: %w", taskID, ctx.Err())
	}
	return &UpstreamError{StatusCode: http.StatusBadGateway, Message: fmt.Sprintf("SenseAudio task_id=%s outcome unknown; do not recreate task: %v", taskID, err), Debug: debug}
}

func senseAudioVideoOutput(parsed map[string]interface{}, taskID string, body []byte, debug *UpstreamDebugSnapshot) (*GenerateOutput, error) {
	mediaURL := strings.TrimSpace(getString(parsed["video_url"]))
	if mediaURL == "" {
		return nil, senseAudioTaskFailure(context.Background(), taskID, fmt.Errorf("%w: completed without video URL", errSenseAudioResponse), debug)
	}
	var metadata string
	if duration, ok := modelParamFloat(parsed, "duration"); ok && duration > 0 {
		var err error
		metadata, err = miniMaxAssetMetadata(duration)
		if err != nil {
			return nil, senseAudioTaskFailure(context.Background(), taskID, err, debug)
		}
	}
	return &GenerateOutput{ResponseID: taskID, Text: mediaURL, GeneratedAssets: []GeneratedAsset{{Kind: GeneratedAssetKindVideo, URL: mediaURL, MIMEType: "video/mp4", ExternalID: taskID, MetadataJSON: metadata}}, RawJSON: string(body), Debug: debug}, nil
}

func senseAudioMusicOutput(parsed map[string]interface{}, taskID string, body []byte, debug *UpstreamDebugSnapshot) (*GenerateOutput, error) {
	response := asMap(parsed["response"])
	raw, ok := response["data"].([]interface{})
	if !ok || len(raw) == 0 {
		return nil, senseAudioTaskFailure(context.Background(), taskID, fmt.Errorf("%w: music success without data", errSenseAudioResponse), debug)
	}
	assets := make([]GeneratedAsset, 0, len(raw))
	for _, item := range raw {
		data := asMap(item)
		mediaURL := strings.TrimSpace(getString(data["audio_url"]))
		if mediaURL == "" {
			return nil, senseAudioTaskFailure(context.Background(), taskID, fmt.Errorf("%w: music output missing audio URL", errSenseAudioResponse), debug)
		}
		mimeType := miMoMIMEMP3
		if strings.HasSuffix(strings.ToLower(strings.Split(mediaURL, "?")[0]), ".wav") {
			mimeType = miMoMIMEWAV
		}
		asset := GeneratedAsset{Kind: GeneratedAssetKindAudio, URL: mediaURL, MIMEType: mimeType, ExternalID: getString(data["id"])}
		if duration, ok := modelParamFloat(data, "duration"); ok && duration > 0 {
			var err error
			asset.MetadataJSON, err = miniMaxAssetMetadata(duration)
			if err != nil {
				return nil, senseAudioTaskFailure(context.Background(), taskID, err, debug)
			}
		}
		assets = append(assets, asset)
	}
	return &GenerateOutput{ResponseID: taskID, Text: assets[0].URL, GeneratedAssets: assets, RawJSON: string(body), Debug: debug}, nil
}
