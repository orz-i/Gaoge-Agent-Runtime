package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// CountInputTokens reuses generation's content/tool/replay encoder, then sends it
// to the matching MiniMax tokenizer endpoint. It never calls a generation URL.
func (c *Client) CountInputTokens(ctx context.Context, route RouteConfig, input GenerateInput) (int64, error) {
	if !SupportsNativeInputTokenCounting(route.Protocol, route.UpstreamModel) {
		return 0, ErrUnsupportedAdapter
	}
	if err := validateNativeMediaInput(route, input); err != nil {
		return 0, err
	}
	payload, url, err := miniMaxTokenCountRequest(route, input)
	if err != nil {
		return 0, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	countCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()
	if NormalizeAdapter(route.Protocol) == AdapterMiniMaxAnthropic {
		route = normalizeMiniMaxRoute(route, AdapterMiniMaxAnthropic)
	}
	body, debug, err := c.doInputTokenCountRequest(countCtx, route, url, payload, data)
	if err != nil {
		return 0, err
	}
	return parseMiniMaxInputTokenCount(body, debug, route.Protocol)
}

type miniMaxTokenCountResponse struct {
	InputTokens *int64                  `json:"input_tokens"`
	BaseResp    map[string]interface{}  `json:"base_resp"`
	Usage       *miniMaxTokenCountUsage `json:"usage"`
}

type miniMaxTokenCountUsage struct {
	InputTokens   *int64 `json:"input_tokens"`
	CacheCreation int64  `json:"cache_creation_input_tokens"`
	CacheRead     int64  `json:"cache_read_input_tokens"`
}

func parseMiniMaxInputTokenCount(body []byte, debug *UpstreamDebugSnapshot, protocol string) (int64, error) {
	var result miniMaxTokenCountResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, attachUpstreamDebug(fmt.Errorf("%w: token count", errMiniMaxInvalidResponse), debug)
	}
	if err := miniMaxResponseError(map[string]interface{}{"base_resp": result.BaseResp}, body, debug); err != nil {
		return 0, err
	}
	// count_tokens uses the Anthropic count response, not a generated Message's
	// usage envelope. Some compatible routes retain the latter shape.
	if result.InputTokens != nil {
		if validInputTokenCount(result.InputTokens) && *result.InputTokens > 0 {
			return *result.InputTokens, nil
		}
		return 0, attachUpstreamDebug(fmt.Errorf("%w: invalid input_tokens", errMiniMaxInvalidResponse), debug)
	}
	if NormalizeAdapter(protocol) != AdapterOpenAIResponses {
		count, err := miniMaxUsageInputTokenCount(result.Usage)
		return count, attachUpstreamDebug(err, debug)
	}
	if !validInputTokenCount(result.InputTokens) || *result.InputTokens == 0 {
		return 0, attachUpstreamDebug(fmt.Errorf("%w: missing or invalid input_tokens", errMiniMaxInvalidResponse), debug)
	}
	return *result.InputTokens, nil
}

func validInputTokenCount(count *int64) bool {
	return count != nil && *count >= 0 && *count <= maxTokenCount
}

func miniMaxUsageInputTokenCount(usage *miniMaxTokenCountUsage) (int64, error) {
	if usage == nil || !validInputTokenCount(usage.InputTokens) {
		return 0, errMiniMaxInvalidResponse
	}
	input := *usage.InputTokens
	if usage.CacheCreation < 0 || usage.CacheRead < 0 || usage.CacheCreation > maxTokenCount-input || usage.CacheRead > maxTokenCount-input-usage.CacheCreation {
		return 0, errMiniMaxInvalidResponse
	}
	total := input + usage.CacheCreation + usage.CacheRead
	if total == 0 {
		return 0, errMiniMaxInvalidResponse
	}
	return total, nil
}

const maxTokenCount int64 = 1 << 40

func miniMaxTokenCountRequest(route RouteConfig, input GenerateInput) (map[string]interface{}, string, error) {
	if NormalizeAdapter(route.Protocol) == AdapterOpenAIResponses {
		if endpoint := strings.TrimSpace(route.Endpoint); endpoint != "" && endpoint != EndpointResponses {
			return nil, "", ErrUnsupportedAdapter
		}
		payload, err := buildOpenAIRequestBody(route.Protocol, route.UpstreamModel, EndpointResponses, input, false)
		if err != nil {
			return nil, "", err
		}
		counted := make(map[string]interface{})
		// The tokenizer endpoint also applies the output reserve. Omitting the final
		// request's limit can return a successful zero count near the Context boundary.
		for _, key := range []string{valueModel2D31E8EB, valueInputF8378501, valueInstructions6059BB26, "tools", interactionKeyToolChoice, valueText1D495308, "reasoning", valueMaxOutputTokens5044A27A} {
			if value, exists := payload[key]; exists {
				counted[key] = value
			}
		}
		return counted, buildOpenAIRequestURL(route.BaseURL, EndpointResponses) + "/input_tokens", nil
	}
	if NormalizeAdapter(route.Protocol) == AdapterMiniMaxAnthropic {
		route = normalizeMiniMaxRoute(route, AdapterMiniMaxAnthropic)
	}
	payload, err := buildAnthropicRequestBody(route.UpstreamModel, input, false)
	if err != nil {
		return nil, "", err
	}
	applyAnthropicReplayRequest(route.Protocol, payload, input)
	if NormalizeAdapter(route.Protocol) == AdapterMiniMaxAnthropic {
		// This endpoint counts input, not generation admission. MiniMax returns
		// a successful zero when input plus the requested output exceeds its
		// window. Count with the minimum output reserve so the host can measure
		// real input pressure before applying the generation's output ceiling.
		payload[valueMaxTokens8DDF2E0E] = 1
	}
	delete(payload, valueStream26D75EAE)
	return payload, buildAnthropicMessagesURL(route.BaseURL) + "/count_tokens", nil
}

func (c *Client) doInputTokenCountRequest(ctx context.Context, route RouteConfig, url string, payload map[string]interface{}, data []byte) ([]byte, *UpstreamDebugSnapshot, error) {
	if NormalizeAdapter(route.Protocol) == AdapterOpenAIResponses {
		return c.doMediaJSONRequest(ctx, route, http.MethodPost, url, data)
	}
	request, err := c.newAnthropicRequest(ctx, http.MethodPost, url, bytes.NewReader(data), route)
	if err != nil {
		return nil, nil, err
	}
	applyAnthropicBetaHeaders(request, payload, nil)
	return c.doMediaJSONHTTPRequest(route, request, data)
}
