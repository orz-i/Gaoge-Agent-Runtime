package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	senseAudioDefaultBase   = "https://api.senseaudio.cn"
	senseAudioKindsAudioGen = `["audio_gen"]`
)

var errSenseAudioResponse = errors.New("invalid SenseAudio response")
var errSenseAudioInput = errors.New("invalid SenseAudio request")

type senseAudioAdapter struct {
	client   *Client
	protocol string
}

func (a *senseAudioAdapter) Name() string { return a.protocol }

func normalizeSenseAudioRoute(route RouteConfig, protocol string) RouteConfig {
	base := strings.TrimRight(strings.TrimSpace(route.BaseURL), "/")
	if base == "" {
		base = senseAudioDefaultBase
	}
	for _, suffix := range []string{"/v1", "/v2"} {
		if strings.HasSuffix(base, suffix) {
			base = strings.TrimSuffix(base, suffix)
			break
		}
	}
	route.BaseURL = base
	route.Protocol = protocol
	route.Endpoint = DefaultEndpointForAdapter(protocol)
	return route
}

func (a *senseAudioAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	route = normalizeSenseAudioRoute(route, a.protocol)
	if a.protocol == AdapterSenseAudioChat {
		return a.client.generateOpenAICompatible(ctx, route, input)
	}
	return a.client.generateSenseAudioMedia(ctx, route, input)
}

func (a *senseAudioAdapter) GenerateStream(ctx context.Context, route RouteConfig, input GenerateInput, onEvent func(GenerateStreamEvent) error) (*GenerateOutput, error) {
	if senseAudioAsyncMedia(a.protocol) && input.ResumeMediaTaskID != "" && !input.CreateMediaTaskOnly {
		route = normalizeSenseAudioRoute(route, a.protocol)
		// Stream is reserved for reading a durably checkpointed task. Never
		// permit a new paid POST through this path.
		return a.client.generateSenseAudioMedia(ctx, route, input)
	}
	if a.protocol != AdapterSenseAudioChat {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedStream, a.protocol)
	}
	route = normalizeSenseAudioRoute(route, a.protocol)
	// Reuse the Chat Completions SSE / tool-call parser, not the nonstandard
	// SenseAudio Responses event schema.
	return a.client.generateStreamOpenAICompatible(ctx, route, input, onEvent)
}

// SenseAudio documents max_tokens (not max_completion_tokens). Keep the most
// restrictive valid limit if both a host ceiling and an alias were supplied.
func normalizeSenseAudioChatOptions(payload map[string]interface{}) {
	limit := 0
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if value, ok := modelParamIntValue(payload, key); ok && value > 0 && (limit == 0 || value < limit) {
			limit = value
		}
	}
	delete(payload, "max_completion_tokens")
	delete(payload, "max_output_tokens")
	if limit > 0 {
		payload["max_tokens"] = limit
	}
}

type senseAudioCatalogModel struct {
	ID        string   `json:"id"`
	OwnedBy   string   `json:"owned_by"`
	Mode      string   `json:"mode"`
	Protocols []string `json:"protocols"`
}

func (a *senseAudioAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	route = normalizeSenseAudioRoute(route, a.protocol)
	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()
	body, debug, err := a.client.doMediaJSONRequest(requestCtx, route, http.MethodGet, route.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Object string                   `json:"object"`
		Data   []senseAudioCatalogModel `json:"data"`
	}
	if err = json.Unmarshal(body, &result); err != nil || result.Data == nil || result.Object != "list" {
		return nil, attachUpstreamDebug(fmt.Errorf("%w: invalid models catalog", errSenseAudioResponse), debug)
	}
	return senseAudioCatalogItems(result.Data), nil
}

func senseAudioCatalogItems(items []senseAudioCatalogModel) []ModelItem {
	models := make([]ModelItem, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" || !senseAudioSupportsHTTP(item.Protocols) {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		protocol, kinds := senseAudioModeProtocol(item.Mode)
		if protocol == "" {
			continue // Unsupported / WebSocket-only modes must not become chat.
		}
		seen[id] = struct{}{}
		ownedBy := strings.TrimSpace(item.OwnedBy)
		if ownedBy == "" {
			ownedBy = "senseaudio"
		}
		models = append(models, ModelItem{ID: id, OwnedBy: ownedBy, SuggestedProtocol: protocol, KindsJSON: kinds})
	}
	return models
}

func senseAudioSupportsHTTP(protocols []string) bool {
	if len(protocols) == 0 {
		return true
	}
	for _, protocol := range protocols {
		if strings.EqualFold(strings.TrimSpace(protocol), "http") {
			return true
		}
	}
	return false
}

func senseAudioModeProtocol(raw string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "llm":
		return AdapterSenseAudioChat, `["chat"]`
	case ContentPartImage:
		return AdapterSenseAudioImage, `["image_gen","image_edit"]`
	case "video":
		return AdapterSenseAudioVideo, `["video_gen"]`
	case "tts":
		return AdapterSenseAudioSpeech, senseAudioKindsAudioGen
	case "music":
		return AdapterSenseAudioMusic, `["music_gen"]`
	default:
		return "", ""
	}
}

func (c *Client) doSenseAudioJSON(ctx context.Context, route RouteConfig, method, path string, payload []byte) (map[string]interface{}, []byte, *UpstreamDebugSnapshot, error) {
	body, debug, err := c.doMediaJSONRequest(ctx, route, method, route.BaseURL+path, payload)
	if err != nil {
		if route.Protocol == AdapterSenseAudioSpeech {
			redactSenseAudioSpeechError(err, debug)
		}
		return nil, body, debug, err
	}
	parsed, err := decodeBailianJSON(body)
	if err != nil {
		if route.Protocol == AdapterSenseAudioSpeech {
			body = []byte(`{"response":"[redacted]"}`)
			redactSenseAudioSpeechDebug(debug, body)
		}
		return nil, body, debug, attachUpstreamDebug(fmt.Errorf("%w: %w", errSenseAudioResponse, err), debug)
	}
	if route.Protocol == AdapterSenseAudioSpeech {
		body = senseAudioSafeSpeechResponse(parsed)
		redactSenseAudioSpeechDebug(debug, body)
	}
	if businessError := senseAudioBusinessError(parsed, body, debug); businessError != nil {
		return nil, body, debug, businessError
	}
	return parsed, body, debug, nil
}

// Native APIs may report business failures in an HTTP 200 envelope.
func senseAudioBusinessError(parsed map[string]interface{}, body []byte, debug *UpstreamDebugSnapshot) error {
	baseResponse := asMap(parsed["base_resp"])
	code, present := baseResponse["status_code"]
	if !present || toInt64(code) == 0 {
		return nil
	}
	return &UpstreamError{
		StatusCode: senseAudioBusinessStatus(toInt64(code)),
		Message:    fmt.Sprintf("SenseAudio status %v: %s", code, getString(baseResponse["status_msg"])),
		Body:       string(body), Debug: debug,
	}
}

func senseAudioBusinessStatus(code int64) int {
	switch code {
	case 1004, 2049:
		return http.StatusUnauthorized
	case 1002, 1041, 2045:
		return http.StatusTooManyRequests
	case 1008:
		return http.StatusPaymentRequired
	case 1026, 1027:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusBadGateway
	}
}
