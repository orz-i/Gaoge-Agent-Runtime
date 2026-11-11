package transport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	volcengineDefaultAPIBase = "https://ark.cn-beijing.volces.com/api/v3"
	volcengineOptionMode     = "mode"
)

// volcengineResponsesAdapter implements the native Volcengine Ark Responses API.
// The wire format is OpenAI Responses compatible, while the adapter identity keeps
// provider-specific capabilities and policy isolated from OpenAI.
type volcengineResponsesAdapter struct {
	client *Client
}

func (a *volcengineResponsesAdapter) Name() string { return AdapterVolcengineResponses }

func (a *volcengineResponsesAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	route = normalizeVolcengineRoute(route, AdapterVolcengineResponses, EndpointResponses)
	return a.client.generateOpenAICompatible(ctx, route, input)
}

func (a *volcengineResponsesAdapter) GenerateStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	route = normalizeVolcengineRoute(route, AdapterVolcengineResponses, EndpointResponses)
	return a.client.generateStreamOpenAICompatible(ctx, route, input, onEvent)
}

func (a *volcengineResponsesAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	route = normalizeVolcengineRoute(route, AdapterVolcengineResponses, EndpointResponses)
	return a.client.listModelsOpenAICompatible(ctx, route)
}

// volcengineImageGenerationAdapter implements Ark's unified Seedream endpoint,
// which accepts both text-only generation and reference-image editing requests.
type volcengineImageGenerationAdapter struct {
	client *Client
}

func (a *volcengineImageGenerationAdapter) Name() string { return AdapterVolcengineImage }

func (a *volcengineImageGenerationAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	route = normalizeVolcengineRoute(route, AdapterVolcengineImage, EndpointImageGenerations)
	return a.client.generateVolcengineImage(ctx, route, input)
}

func (a *volcengineImageGenerationAdapter) GenerateStream(
	_ context.Context,
	_ RouteConfig,
	_ GenerateInput,
	_ func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	return nil, fmt.Errorf("%w: %s", ErrUnsupportedStream, AdapterVolcengineImage)
}

func (a *volcengineImageGenerationAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	route = normalizeVolcengineRoute(route, AdapterVolcengineImage, EndpointImageGenerations)
	return a.client.listModelsOpenAICompatible(ctx, route)
}

func normalizeVolcengineRoute(route RouteConfig, protocol string, endpoint string) RouteConfig {
	if strings.TrimSpace(route.BaseURL) == "" {
		route.BaseURL = volcengineDefaultAPIBase
	}
	route.Protocol = protocol
	route.Endpoint = endpoint
	return route
}

func (c *Client) generateVolcengineImage(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	requestURL := buildOpenAIRequestURL(route.BaseURL, EndpointImageGenerations)
	if requestURL == "" {
		return nil, errCategory17F3874D69
	}
	requestBody, err := buildVolcengineImageGenerationRequestBody(route.UpstreamModel, input)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", valueApplicationJson5943A7D2)
	setBearerAuthHeader(req, route.APIKey)
	setAdditionalHeaders(req, route.HeadersJSON)

	resp, err := c.httpClientForRoute(route).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readUpstreamBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseUpstreamError(resp.StatusCode, body, upstreamDebugSnapshot(req, payload, resp, body))
	}
	return parseOpenAIImageOutput(body, "")
}

func buildVolcengineImageGenerationRequestBody(model string, input GenerateInput) (map[string]interface{}, error) {
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	if strings.TrimSpace(prompt) == "" {
		return nil, errCategoryEF97804E6D
	}
	payload := map[string]interface{}{
		valueModel37CF506F:          strings.TrimSpace(model),
		valuePromptF12B8E58:         prompt,
		valueResponseFormat00805748: "url",
	}
	if images := volcengineImageInputs(input.Messages); len(images) > 0 {
		payload["image"] = images
	}
	applyVolcengineImageGenerationParams(payload, input.Options)
	return payload, nil
}

func applyVolcengineImageGenerationParams(payload map[string]interface{}, options map[string]interface{}) {
	for _, key := range []string{"size", valueResponseFormat00805748, "sequential_image_generation"} {
		if value := modelParamString(options, key); value != "" {
			payload[key] = value
		}
	}
	if value, ok := modelParamIntValue(options, valueSeedC8D041FC); ok {
		payload[valueSeedC8D041FC] = value
	}
	if value, ok := modelParamIntValue(asMap(options["sequential_image_generation_options"]), "max_images"); ok {
		payload["sequential_image_generation_options"] = map[string]interface{}{"max_images": value}
	}
	if value, ok := modelParamFloat(options, "guidance_scale"); ok {
		payload["guidance_scale"] = value
	}
	if value, ok := modelParamBoolValue(options, volcengineVideoWatermarkOption); ok {
		payload[volcengineVideoWatermarkOption] = value
	}
	if mode := modelParamString(asMap(options["optimize_prompt_options"]), volcengineOptionMode); mode != "" {
		payload["optimize_prompt_options"] = map[string]interface{}{volcengineOptionMode: mode}
	}
}

func volcengineImageInputs(messages []Message) []string {
	parts := collectImageInputParts(messages)
	images := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part.Data) == 0 {
			continue
		}
		mimeType := strings.TrimSpace(part.MimeType)
		if mimeType == "" {
			mimeType = valueImagePng8EC23E97
		}
		images = append(images, "data:"+mimeType+";base64,"+base64.StdEncoding.EncodeToString(part.Data))
	}
	return images
}
