package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	bailianDefaultBase             = "https://dashscope.aliyuncs.com"
	bailianNativeBase              = "/api/v1"
	bailianMultimodalPath          = "/services/aigc/multimodal-generation/generation"
	bailianImagePath               = "/services/aigc/image-generation/generation"
	bailianVideoPath               = "/services/aigc/video-generation/video-synthesis"
	bailian3DPath                  = "/services/aigc/video-generation/3d-generation"
	bailianSpeechPath              = "/services/audio/tts/SpeechSynthesizer"
	bailianMusicPath               = "/services/audio/music/generation"
	bailianTranscriptionPath       = "/services/audio/asr/transcription"
	bailianEmbeddingPath           = "/services/embeddings/text-embedding/text-embedding"
	bailianMultimodalEmbeddingPath = "/services/embeddings/multimodal-embedding/multimodal-embedding"
	bailianRerankPath              = "/services/rerank/text-rerank/text-rerank"
	bailianTaskID                  = "task_id"
	bailianTaskStatus              = "task_status"
	bailianParameters              = "parameters"
	bailianAsyncHeader             = "X-DashScope-Async"
	bailianWorldID                 = "encryptedWorldId"
	bailianModelWan3               = "wan3.0-video"
	bailianModelQwenImage3         = "qwen-image-3.0-pro"
	bailianModelTripo              = "Tripo/Tripo-H3.1"
	bailianModelWorldAdventure     = "happyoyster-1.0-adventure"
	bailianModelWorldDirecting     = "happyoyster-1.0-directing"
	bailianModelWorldActing        = "happyoyster-1.0-acting"
	bailianModelAudioNext          = "qwen-audio-3.1-tts-next"
	bailianModelMusic              = "fun-music-v1"
	bailianFormatWAV               = "wav"
	bailianFormatMP3               = "mp3"
)

var (
	errBailianInput    = errors.New("invalid Bailian request")
	errBailianResponse = errors.New("invalid Bailian response")
	bailianProtocols   = []string{AdapterBailianChat, AdapterBailianImage, AdapterBailianVideo,
		AdapterBailianWorld, AdapterBailian3D, AdapterBailianSpeech, AdapterBailianAudio,
		AdapterBailianTranscription, AdapterBailianMusic, AdapterBailianEmbedding, AdapterBailianRerank}
)

type bailianAdapter struct {
	client   *Client
	protocol string
}

func (a *bailianAdapter) Name() string { return a.protocol }

func (a *bailianAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	route = normalizeBailianRoute(route, a.protocol)
	switch a.protocol {
	case AdapterBailianChat:
		route.BaseURL += bailianCompatibleModeBase
		return a.client.generateOpenAICompatible(ctx, route, input)
	case AdapterBailianWorld:
		return a.client.generateBailianWorld(ctx, route, input)
	default:
		route.SingleProviderAttempt = true
		return a.client.generateBailianNative(ctx, route, input)
	}
}

func (a *bailianAdapter) GenerateStream(ctx context.Context, route RouteConfig, input GenerateInput, onEvent func(GenerateStreamEvent) error) (*GenerateOutput, error) {
	if a.protocol != AdapterBailianChat {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedStream, a.protocol)
	}
	route = normalizeBailianRoute(route, a.protocol)
	route.BaseURL += bailianCompatibleModeBase
	return a.client.generateStreamOpenAICompatible(ctx, route, input, onEvent)
}

func normalizeBailianRoute(route RouteConfig, protocol string) RouteConfig {
	base := strings.TrimRight(strings.TrimSpace(route.BaseURL), "/")
	if base == "" {
		base = bailianDefaultBase
	}
	for _, suffix := range []string{"/compatible-mode/v1", "/compatible-api/v1", "/api/v1", "/api/v2", bailianCompatibleModeBase, providerAPIVersionOne} {
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

type bailianRequest struct {
	path    string
	payload map[string]interface{}
	async   bool
}

func (c *Client) generateBailianNative(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	if err := rejectUnsupportedImageMask(input.ImageEditMask, route.Protocol); err != nil {
		return nil, err
	}
	request, err := buildBailianRequest(route, input)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(request.payload)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, resolveMediaPollTimeout(route, input.Options))
	defer cancel()
	parsed, body, debug, err := c.doBailianRequest(requestCtx, route, http.MethodPost, request.path, payload, request.async)
	if err != nil {
		return nil, err
	}
	if request.async {
		parsed, body, debug, err = c.pollBailianTask(requestCtx, route, parsed, body, debug, input.Options)
		if err != nil {
			return nil, err
		}
	}
	output, err := c.parseBailianOutput(requestCtx, route, input, parsed)
	if err != nil {
		if request.async {
			return nil, bailianTaskFailure(requestCtx, bailianOutputTaskID(parsed), err, debug)
		}
		return nil, attachUpstreamDebug(err, debug)
	}
	output.RawJSON = string(body)
	output.Debug = debug
	return output, nil
}

func (c *Client) doBailianRequest(ctx context.Context, route RouteConfig, method, path string, payload []byte, async bool) (map[string]interface{}, []byte, *UpstreamDebugSnapshot, error) {
	headerJSON, err := bailianRequestHeaders(route.HeadersJSON, async)
	if err != nil {
		return nil, nil, nil, err
	}
	route.HeadersJSON = headerJSON
	body, debug, err := c.doMediaJSONRequest(ctx, route, method, route.BaseURL+path, payload)
	if err != nil {
		return nil, body, debug, err
	}
	parsed, err := decodeBailianJSON(body)
	if err != nil {
		return nil, body, debug, attachUpstreamDebug(err, debug)
	}
	if err = bailianResponseError(parsed, body, debug); err != nil {
		return nil, body, debug, err
	}
	return parsed, body, debug, nil
}

func decodeBailianJSON(body []byte) (map[string]interface{}, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var result map[string]interface{}
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("%w: %w", errBailianResponse, err)
	}
	if result == nil || decoder.Decode(new(interface{})) != io.EOF {
		return nil, errBailianResponse
	}
	return result, nil
}

func bailianResponseError(parsed map[string]interface{}, body []byte, debug *UpstreamDebugSnapshot) error {
	code := strings.TrimSpace(getString(parsed["code"]))
	success, hasSuccess := parsed["success"].(bool)
	if (code == "" || code == "0" || strings.EqualFold(code, "success")) && (!hasSuccess || success) {
		return nil
	}
	return &UpstreamError{StatusCode: bailianErrorStatus(code), Message: "Bailian " + code + ": " + getString(parsed["message"]), Body: string(body), Debug: debug}
}

func bailianErrorStatus(code string) int {
	value := strings.ToLower(code)
	switch {
	case strings.Contains(value, "apikey"), strings.Contains(value, "unauthorized"), strings.HasPrefix(value, "401"):
		return http.StatusUnauthorized
	case strings.Contains(value, "inspection"), code == "403004", code == "403005":
		return http.StatusUnprocessableEntity
	case strings.Contains(value, "balance"), strings.Contains(value, "arrears"):
		return http.StatusPaymentRequired
	case strings.Contains(value, "throttl"), strings.Contains(value, "ratelimit"), strings.HasPrefix(value, "429"):
		return http.StatusTooManyRequests
	case strings.Contains(value, "accessdenied"), strings.Contains(value, "forbidden"), strings.HasPrefix(value, "403"):
		return http.StatusForbidden
	case strings.Contains(value, "internal"), strings.Contains(value, "unavailable"), strings.HasPrefix(value, "500"):
		return http.StatusBadGateway
	default:
		return http.StatusBadRequest
	}
}

func normalizeBailianChatOptions(payload map[string]interface{}, input GenerateInput) {
	options := input.Options
	delete(payload, valueThinking0F938ED2)
	if _, exists := payload["max_completion_tokens"]; !exists {
		if limit, alias := payload[valueMaxTokens8DDF2E0E]; alias {
			payload["max_completion_tokens"] = limit
		}
	}
	delete(payload, valueMaxTokens8DDF2E0E)
	delete(payload, "max_output_tokens")
	for _, key := range []string{"enable_thinking", "thinking_budget", "enable_search", "search_options", "response_format", "repetition_penalty", "top_k", "presence_penalty", "frequency_penalty", "seed", "logprobs", "top_logprobs", "stop"} {
		if value, exists := options[key]; exists {
			payload[key] = value
		}
	}
	if enabled, exists := bailianThinkingOption(options); exists {
		if _, explicit := options["enable_thinking"]; !explicit {
			payload["enable_thinking"] = enabled
		}
	}
	if input.DisableTools {
		payload["enable_search"] = false
		delete(payload, "search_options")
	}
}

func bailianOptionSubset(options map[string]interface{}, keys ...string) map[string]interface{} {
	result := make(map[string]interface{})
	for _, key := range keys {
		if value, exists := options[key]; exists {
			result[key] = value
		}
	}
	return result
}

func bailianUsage(parsed map[string]interface{}) Usage {
	usage := asMap(parsed["usage"])
	inputTokens := toInt64(usage["input_tokens"])
	if inputTokens == 0 {
		inputTokens = toInt64(usage["prompt_tokens"])
	}
	if inputTokens == 0 {
		inputTokens = toInt64(usage["total_tokens"]) - toInt64(usage["output_tokens"])
	}
	return Usage{InputTokens: inputTokens, OutputTokens: toInt64(usage["output_tokens"]), RawUsageJSON: bailianRawUsageJSON(parsed)}
}

func bailianRawUsageJSON(parsed map[string]interface{}) string {
	usage, exists := parsed["usage"]
	if !exists {
		return ""
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func bailianRequestHeaders(raw string, async bool) (string, error) {
	headers := map[string]string{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &headers); err != nil {
			return "", err
		}
	}
	if headers == nil {
		headers = map[string]string{}
	}
	for key := range headers {
		if strings.EqualFold(key, bailianAsyncHeader) || strings.EqualFold(key, "X-DashScope-SSE") {
			delete(headers, key)
		}
	}
	if async {
		headers[bailianAsyncHeader] = "enable"
	}
	headerJSON, err := json.Marshal(headers)
	if err != nil {
		return "", err
	}
	return string(headerJSON), nil
}

func bailianThinkingOption(options map[string]interface{}) (bool, bool) {
	if enabled, exists := modelParamBoolValue(options, valueThinking0F938ED2); exists {
		return enabled, true
	}
	thinkingType := getString(asMap(options[valueThinking0F938ED2])[valueType5AC5AA71])
	if thinkingType == "" {
		thinkingType = modelParamString(options, "thinking.type")
	}
	switch thinkingType {
	case valueEnabledCEB1C3B3:
		return true, true
	case "disabled":
		return false, true
	default:
		return false, false
	}
}
