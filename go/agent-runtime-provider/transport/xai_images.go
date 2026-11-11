package transport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	valueType1A55AAD7 = "type"
)

const (
	valueAspectRatioA5C1E4FE = "aspect_ratio"
	valueImageJpeg4B4E8948   = "image/jpeg"
	valueImageUrl7B7D170D    = "image_url"
	valueModelBACA7634       =

	// xAIImageAdapter 实现 xAI 图片生成协议。
	"model"
	valuePrompt4DF1BEAD = "prompt"
	valueUrlA98F70A4    = "url"
)

type xAIImageAdapter struct {
	client *Client
}

func (a *xAIImageAdapter) Name() string { return AdapterXAIImage }

// Generate 调用 xAI 图片生成接口，返回结构化图片结果。
func (a *xAIImageAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	route.Protocol = AdapterXAIImage
	route.Endpoint = EndpointImageGenerations
	return a.client.generateXAIImage(ctx, route, input)
}

// GenerateStream 当前不伪造图片流式；媒体任务会通过非流式调用落库生成结果。
func (a *xAIImageAdapter) GenerateStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	return nil, fmt.Errorf("%w: %s", ErrUnsupportedStream, AdapterXAIImage)
}

// ListModels 复用 xAI models 目录，供渠道校验和展示使用。
func (a *xAIImageAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	route.Protocol = AdapterXAIImage
	return a.client.listModelsOpenAICompatible(ctx, route)
}

// xAIImageEditsAdapter 实现 xAI 图片编辑协议。
type xAIImageEditsAdapter struct {
	client *Client
}

func (a *xAIImageEditsAdapter) Name() string { return AdapterXAIImageEdits }

// Generate 调用 xAI 图片编辑接口，返回结构化图片结果。
func (a *xAIImageEditsAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	route.Protocol = AdapterXAIImageEdits
	route.Endpoint = EndpointImageEdits
	return a.client.generateXAIImage(ctx, route, input)
}

// GenerateStream 当前不伪造图片流式；媒体任务会通过非流式调用落库生成结果。
func (a *xAIImageEditsAdapter) GenerateStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	return nil, fmt.Errorf("%w: %s", ErrUnsupportedStream, AdapterXAIImageEdits)
}

// ListModels 复用 xAI models 目录，供渠道校验和展示使用。
func (a *xAIImageEditsAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	route.Protocol = AdapterXAIImageEdits
	return a.client.listModelsOpenAICompatible(ctx, route)
}

// generateXAIImage 构造并执行 xAI Images API 请求。
func (c *Client) generateXAIImage(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	protocol := normalizeXAIImageProtocol(route.Protocol)
	route.Protocol = protocol
	endpoint := DefaultEndpointForAdapter(protocol)
	requestURL := buildOpenAIRequestURL(route.BaseURL, endpoint)
	if requestURL == "" {
		return nil, errCategory6C43D04116
	}

	requestBody, debugBody, err := buildXAIImageRequest(route.UpstreamModel, endpoint, input)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}
	debugPayload := xaiImageDebugPayload(payload, debugBody)

	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()

	req, err := newXAIImageRequest(requestCtx, requestURL, payload, route)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClientForRoute(route).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readUpstreamBody(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseXAIImageResponse(body, protocol, resp, req, debugPayload)
}

func normalizeXAIImageProtocol(protocol string) string {
	normalized := NormalizeAdapter(protocol)
	if normalized == AdapterXAIImage || normalized == AdapterXAIImageEdits {
		return normalized
	}
	return AdapterXAIImage
}

func xaiImageDebugPayload(payload []byte, debugBody []byte) []byte {
	if len(debugBody) > 0 {
		return debugBody
	}
	return payload
}

func newXAIImageRequest(
	ctx context.Context,
	requestURL string,
	payload []byte,
	route RouteConfig,
) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	setBearerAuthHeader(req, route.APIKey)
	setAdditionalHeaders(req, route.HeadersJSON)
	return req, nil
}

func parseXAIImageResponse(
	body []byte,
	protocol string,
	resp *http.Response,
	req *http.Request,
	debugPayload []byte,
) (*GenerateOutput, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseUpstreamError(resp.StatusCode, body, upstreamDebugSnapshot(req, debugPayload, resp, body))
	}
	return parseXAIImageOutput(body, protocol)
}

// buildXAIImageRequest 根据任务端点构造 xAI 图片生成或编辑请求。
func buildXAIImageRequest(model string, endpoint string, input GenerateInput) (map[string]interface{}, []byte, error) {
	if endpoint == EndpointImageEdits {
		return buildXAIImageEditRequestBody(model, input)
	}
	payload, err := buildXAIImageRequestBody(model, input)
	return payload, nil, err
}

// buildXAIImageRequestBody 只允许 xAI 图片生成端点支持的字段进入上游。
func buildXAIImageRequestBody(model string, input GenerateInput) (map[string]interface{}, error) {
	if err := rejectUnsupportedImageMask(input.ImageEditMask, AdapterXAIImage); err != nil {
		return nil, err
	}
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	if strings.TrimSpace(prompt) == "" {
		return nil, errCategory97BB4350C4
	}
	payload := map[string]interface{}{
		valueModelBACA7634:  strings.TrimSpace(model),
		valuePrompt4DF1BEAD: prompt,
	}
	applyXAIImageParams(payload, input.Options)
	return payload, nil
}

// buildXAIImageEditRequestBody 只允许 xAI 图片编辑端点支持的字段进入上游。
func buildXAIImageEditRequestBody(model string, input GenerateInput) (map[string]interface{}, []byte, error) {
	if err := rejectUnsupportedImageMask(input.ImageEditMask, AdapterXAIImageEdits); err != nil {
		return nil, nil, err
	}
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	if strings.TrimSpace(prompt) == "" {
		return nil, nil, errCategoryCD3B6FC3A5
	}
	images := collectImageInputParts(input.Messages)
	if len(images) == 0 {
		return nil, nil, errCategory3EE87DFF16
	}
	if len(images) > 3 {
		return nil, nil, errCategoryEF01E5A47D
	}
	imageInputs := make([]map[string]interface{}, 0, len(images))
	for _, image := range images {
		imageInputs = append(imageInputs, xAIImageURLPayload(image))
	}
	payload := map[string]interface{}{
		valueModelBACA7634:  strings.TrimSpace(model),
		valuePrompt4DF1BEAD: prompt,
	}
	if len(imageInputs) == 1 {
		payload["image"] = imageInputs[0]
	} else {
		payload["image"] = imageInputs
	}
	applyXAIImageParams(payload, input.Options)
	debugBody, err := json.Marshal(map[string]interface{}{
		valueModelBACA7634:  payload[valueModelBACA7634],
		valuePrompt4DF1BEAD: payload[valuePrompt4DF1BEAD],
		"image_count":       len(imageInputs),
	})
	if err != nil {
		debugBody = nil
	}
	return payload, debugBody, nil
}

// xAIImageURLPayload 将内部图片输入转换为 xAI 文档要求的 image_url 对象。
func xAIImageURLPayload(image ContentPart) map[string]interface{} {
	mimeType := strings.TrimSpace(image.MimeType)
	if mimeType == "" {
		mimeType = valueImageJpeg4B4E8948
	}
	return map[string]interface{}{
		valueUrlA98F70A4:  "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(image.Data),
		valueType1A55AAD7: valueImageUrl7B7D170D,
	}
}

// applyXAIImageParams 从 options 中提取 xAI 图片生成官方参数。
func applyXAIImageParams(payload map[string]interface{}, options map[string]interface{}) {
	payload["response_format"] = defaultImageResponseFormat(options)
	for _, key := range []string{valueAspectRatioA5C1E4FE, "resolution"} {
		if value := modelParamString(options, key); value != "" {
			payload[key] = value
		}
	}
	if value := modelParamInt(options, "n"); value > 0 {
		payload["n"] = value
	}
}

// parseXAIImageOutput 解析 xAI 图片响应；图片字节只进入 GeneratedImages。
func parseXAIImageOutput(body []byte, protocol string) (*GenerateOutput, error) {
	parsed := make(map[string]interface{})
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	result := &GenerateOutput{
		ResponseID:      strings.TrimSpace(getString(parsed["id"])),
		ToolCalls:       make([]ToolCall, 0),
		ServerToolCalls: make([]ToolCall, 0),
		RawJSON:         string(body),
	}
	if usage := parseOpenAICompatibleUsageForAdapter(protocol, parsed); usage != (Usage{}) {
		result.Usage = usage
	}
	data := asSlice(parsed["data"])
	citations := make([]string, 0, len(data))
	for _, item := range data {
		if image, ok := parseXAIImagePayload(asMap(item)); ok {
			if url := strings.TrimSpace(image.URL); url != "" {
				citations = append(citations, url)
			}
			result.GeneratedImages = append(result.GeneratedImages, image)
		}
	}
	if len(data) == 0 {
		if image, ok := parseXAIImagePayload(parsed); ok {
			if url := strings.TrimSpace(image.URL); url != "" {
				citations = append(citations, url)
			}
			result.GeneratedImages = append(result.GeneratedImages, image)
		}
	}
	result.Citations = appendUniqueStrings(result.Citations, citations...)
	return result, nil
}

func parseXAIImagePayload(payload map[string]interface{}) (GeneratedImage, bool) {
	if len(payload) == 0 {
		return GeneratedImage{}, false
	}
	revisedPrompt := strings.TrimSpace(getString(payload["revised_prompt"]))
	if revisedPrompt == "" {
		revisedPrompt = strings.TrimSpace(getString(payload["revisedPrompt"]))
	}
	const defaultXAIImageMIMEType = valueImageJpeg4B4E8948
	if url := strings.TrimSpace(getString(payload[valueUrlA98F70A4])); url != "" {
		return GeneratedImage{
			URL:           url,
			MIMEType:      defaultXAIImageMIMEType,
			RevisedPrompt: revisedPrompt,
		}, true
	}
	if b64 := strings.TrimSpace(getString(payload["b64_json"])); b64 != "" {
		return GeneratedImage{
			B64JSON:       b64,
			MIMEType:      defaultXAIImageMIMEType,
			RevisedPrompt: revisedPrompt,
		}, true
	}
	return GeneratedImage{}, false
}

var (
	errCategory3EE87DFF16 = errors.New("image edit input image required")
	errCategoryCD3B6FC3A5 = errors.New("image edit prompt required")
	errCategory97BB4350C4 = errors.New("image generation prompt required")
	errCategory6C43D04116 = errors.New("invalid base url")
	errCategoryEF01E5A47D = errors.New("too many image edit input images")
)
