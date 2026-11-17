package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
)

const miniMaxDefaultAPIBase = "https://api.minimax.cn"

var errMiniMaxInvalidResponse = errors.New("invalid MiniMax response")

const (
	miniMaxOutputFormatOption = "output_format"
	miniMaxFormatMP3          = "mp3"
	miniMaxResolution768P     = "768P"
	miniMaxVideoRatioOption   = "ratio"
	miniMaxModelM3            = "MiniMax-M3"
	miniMaxModelH3            = "MiniMax-H3"
	miniMaxModelH3Max         = "MiniMax-H3-Max"
	miniMaxModelImage01       = "image-01"
	miniMaxModelSpeech28HD    = "speech-2.8-hd"
	miniMaxModelMusic30       = "music-3.0"
	miniMaxModelMusicCover    = "music-cover"
	miniMaxImagePath          = "/v1/image_generation"
	miniMaxSpeechPath         = "/v1/t2a_v2"
	miniMaxMusicPath          = "/v1/music_generation"
	miniMaxImageBase64        = "base64"
	miniMaxImageURLsInput     = "image_urls"
	miniMaxOutputHex          = "hex"
	miniMaxAudioSettingOption = "audio_setting"
	miniMaxFormatKey          = "format"
	miniMaxAudioURLInput      = "audio_url"
	miniMaxCoverFeatureInput  = "cover_feature_id"
	miniMaxFormatWAV          = "wav"
	miniMaxVoiceSettingOption = "voice_setting"
	miniMaxPortraitRatio      = "3:4"
	miniMaxRoleReferenceImage = "reference_image"
	miniMaxRoleFirstFrame     = "first_frame"
	miniMaxRoleLastFrame      = "last_frame"
	miniMaxStatusQueued       = "queued"
	miniMaxStatusRunning      = "running"
	miniMaxStatusSucceeded    = "succeeded"
	miniMaxStatusFailed       = "failed"
	miniMaxStatusCancelled    = "cancelled"
)

// MiniMax media APIs use native wire formats. Its text APIs reuse the existing
// Chat Completions and Anthropic transports, including tool and reasoning replay.
type miniMaxAdapter struct {
	client   *Client
	protocol string
}

func (a *miniMaxAdapter) Name() string { return a.protocol }

func (a *miniMaxAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	route = normalizeMiniMaxRoute(route, a.protocol)
	switch a.protocol {
	case AdapterMiniMaxChat:
		return a.client.generateOpenAICompatible(ctx, route, input)
	case AdapterMiniMaxAnthropic:
		return a.client.generateAnthropic(ctx, route, input, false)
	case AdapterMiniMaxVideo:
		return a.client.generateMiniMaxVideo(ctx, route, input)
	default:
		return a.client.generateMiniMaxMedia(ctx, route, input)
	}
}

func (a *miniMaxAdapter) GenerateStream(ctx context.Context, route RouteConfig, input GenerateInput, onEvent func(GenerateStreamEvent) error) (*GenerateOutput, error) {
	route = normalizeMiniMaxRoute(route, a.protocol)
	switch a.protocol {
	case AdapterMiniMaxChat:
		return a.client.generateStreamOpenAICompatible(ctx, route, input, onEvent)
	case AdapterMiniMaxAnthropic:
		return a.client.generateAnthropicStream(ctx, route, input, onEvent)
	default:
		// The gateway stream event contract carries text/images, not audio chunks.
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedStream, a.protocol)
	}
}

func (a *miniMaxAdapter) ListModels(ctx context.Context, _ RouteConfig) ([]ModelItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// This adapter uses a documented static catalog rather than querying account
	// model availability. Retired free music
	// and legacy video models are intentionally absent.
	ids := []string{
		miniMaxModelM3, "MiniMax-M3.1-Flash-Preview", "MiniMax-M2.7", "MiniMax-M2.7-highspeed",
		"MiniMax-M2.5", "MiniMax-M2.5-highspeed", "MiniMax-M2.1", "MiniMax-M2.1-highspeed", "MiniMax-M2",
		miniMaxModelH3, miniMaxModelH3Max, miniMaxModelImage01, "image-01-live",
		miniMaxModelSpeech28HD, "speech-2.8-turbo", "speech-2.6-hd", "speech-2.6-turbo", "speech-02-hd", "speech-02-turbo",
		miniMaxModelMusic30, "music-2.6", miniMaxModelMusicCover,
	}
	items := make([]ModelItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, ModelItem{ID: id, OwnedBy: "minimax"})
	}
	return items, nil
}

func normalizeMiniMaxRoute(route RouteConfig, protocol string) RouteConfig {
	base := strings.TrimRight(strings.TrimSpace(route.BaseURL), "/")
	if base == "" {
		base = miniMaxDefaultAPIBase
	}
	base = strings.TrimSuffix(strings.TrimSuffix(base, "/v1"), "/v2")
	base = strings.TrimSuffix(base, "/anthropic")
	route.BaseURL = base
	route.Protocol = protocol
	route.Endpoint = DefaultEndpointForAdapter(protocol)
	if protocol == AdapterMiniMaxAnthropic {
		route.BaseURL += "/anthropic"
	}
	return route
}

func (c *Client) doMiniMaxRequest(ctx context.Context, route RouteConfig, method, path string, payload []byte) (map[string]interface{}, []byte, *UpstreamDebugSnapshot, error) {
	body, debug, err := c.doMediaJSONRequest(ctx, route, method, route.BaseURL+path, payload)
	if err != nil {
		return nil, body, debug, err
	}
	parsed := make(map[string]interface{})
	if err = json.Unmarshal(body, &parsed); err != nil {
		return nil, body, debug, attachUpstreamDebug(err, debug)
	}
	if err = miniMaxResponseError(parsed, body, debug); err != nil {
		return nil, body, debug, err
	}
	return parsed, body, debug, nil
}

// MiniMax v1 APIs can report a business error inside an HTTP 200 response.
func miniMaxResponseError(parsed map[string]interface{}, body []byte, debug *UpstreamDebugSnapshot) error {
	status := asMap(parsed["base_resp"])
	code := toInt64(status["status_code"])
	if code == 0 {
		return nil
	}
	httpCode := http.StatusBadRequest
	switch code {
	case 1000, 1024, 1033:
		httpCode = http.StatusBadGateway
	case 1001:
		httpCode = http.StatusRequestTimeout
	case 1002, 1041, 2045:
		httpCode = http.StatusTooManyRequests
	case 1004, 2049:
		httpCode = http.StatusUnauthorized
	case 1008:
		httpCode = http.StatusPaymentRequired
	case 1026, 1027:
		httpCode = http.StatusUnprocessableEntity
	}
	return &UpstreamError{StatusCode: httpCode, Message: fmt.Sprintf("MiniMax %d: %s", code, getString(status["status_msg"])), Body: string(body), Debug: debug}
}

func normalizeMiniMaxChatOptions(payload, options map[string]interface{}) {
	if enabled, ok := modelParamBoolValue(options, valueThinking0F938ED2); ok {
		thinkingType := valueDisabledF0113E8C
		if enabled {
			thinkingType = valueAdaptive8B08E1F4
		}
		payload[valueThinking0F938ED2] = map[string]interface{}{valueTypeF2B37301: thinkingType}
	}
	if _, exists := options["reasoning_split"]; !exists {
		payload["reasoning_split"] = true
	}
}

func miniMaxIntegerInRange(payload map[string]interface{}, key string, minimum, maximum float64) bool {
	value, ok := modelParamFloat(payload, key)
	return ok && !math.IsNaN(value) && !math.IsInf(value, 0) && math.Trunc(value) == value && value >= minimum && value <= maximum
}
