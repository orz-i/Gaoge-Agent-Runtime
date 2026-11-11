package transport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	valueTEXT21AF6042 = "TEXT"
	valueUserF1E3DFF7 = "user"
)

const (
	valueText9601D3C9             = "16:9"
	valueIMAGE462C7646            = "IMAGE"
	valueContents2B476774         = "contents"
	valueData1BD6994F             = "data"
	valueGenerationConfigE3E3EE25 =

	// geminiImageGenerationAdapter 实现 Google Gemini 图片生成/编辑协议。
	"generationConfig"
	valueImageJpegDEC2BF89 = "image/jpeg"
	valueImagePng415383B9  = "image/png"
	valueParts73DE3A86     = "parts"
	valueRole2384F370      = "role"
	valueText8507C7C9      = "text"
)

type geminiImageGenerationAdapter struct {
	client *Client
}

func (a *geminiImageGenerationAdapter) Name() string { return AdapterGoogleImageGeneration }

// Generate 调用 Gemini generateContent 图片能力，返回结构化图片结果。
func (a *geminiImageGenerationAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	return a.client.generateGeminiImageGeneration(ctx, route, input)
}

// GenerateStream 调用 Gemini streamGenerateContent；最终图片仍由媒体任务落库。
func (a *geminiImageGenerationAdapter) GenerateStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	return a.client.generateGeminiImageGenerationStream(ctx, route, input, onEvent)
}

// ListModels 复用 Gemini models 目录，供渠道校验和展示使用。
func (a *geminiImageGenerationAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	return a.client.listModelsGemini(ctx, route)
}

// generateGeminiImageGeneration 调用 generateContent，并强制请求图片模态输出。
func (c *Client) generateGeminiImageGeneration(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	base := geminiBaseURL(route)
	model := strings.TrimSpace(route.UpstreamModel)
	requestURL := buildGeminiGenerateURL(base, model)

	requestBody, err := buildGeminiImageGenerationRequestBody(model, input)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()

	req, err := c.newGeminiRequest(requestCtx, http.MethodPost, requestURL, bytes.NewReader(payload), route)
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseGeminiError(resp.StatusCode, body, upstreamDebugSnapshot(req, payload, resp, body))
	}

	return parseGeminiImageGenerationOutput(body)
}

// generateGeminiImageGenerationStream 调用 Gemini 图片 SSE 输出并复用 GenerateContentResponse 解析。
func (c *Client) generateGeminiImageGenerationStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	base := geminiBaseURL(route)
	model := strings.TrimSpace(route.UpstreamModel)
	requestURL := buildGeminiStreamURL(base, model)

	requestBody, err := buildGeminiImageGenerationRequestBody(model, input)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	firstByteCtx, firstByteCancel := context.WithCancel(ctx)
	defer firstByteCancel()

	firstByteTimer := time.AfterFunc(resolveReadTimeout(route.ReadTimeoutMS), firstByteCancel)
	defer firstByteTimer.Stop()

	req, err := c.newGeminiRequest(firstByteCtx, http.MethodPost, requestURL, bytes.NewReader(payload), route)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClientForRoute(route).Do(req)
	firstByteTimer.Stop()
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := readUpstreamBody(resp.Body)
		return nil, parseGeminiError(resp.StatusCode, body, upstreamDebugSnapshot(req, payload, resp, body))
	}

	result := &GenerateOutput{
		ToolCalls: make([]ToolCall, 0),
	}
	idleReader := newIdleTimeoutReader(resp.Body, resolveStreamIdleTimeout(route.StreamIdleTimeoutMS))
	streamBody := newUpstreamBodyRecorder(idleReader)
	if err = consumeGeminiStream(streamBody, result, onEvent, resolveStreamIdleTimeout(route.StreamIdleTimeoutMS)); err != nil {
		return nil, attachUpstreamDebug(err, upstreamDebugSnapshot(req, payload, resp, streamErrorBody(streamBody, err)))
	}
	for i := range result.GeneratedImages {
		result.GeneratedImages[i].RevisedPrompt = strings.TrimSpace(result.Text)
	}
	return result, nil
}

// buildGeminiImageGenerationRequestBody 构造 Gemini 图片生成/编辑请求字段。
func buildGeminiImageGenerationRequestBody(model string, input GenerateInput) (map[string]interface{}, error) {
	if err := rejectUnsupportedImageMask(input.ImageEditMask, AdapterGoogleImageGeneration); err != nil {
		return nil, err
	}
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	if strings.TrimSpace(prompt) == "" {
		return nil, errCategory0156D50E77
	}
	providerTools, _, toolsEnabled, err := toolDeclarationsForInput(input)
	if err != nil {
		return nil, err
	}

	payload := map[string]interface{}{
		valueContents2B476774: []map[string]interface{}{
			{
				valueRole2384F370:  valueUserF1E3DFF7,
				valueParts73DE3A86: buildGeminiImageGenerationParts(prompt, collectImageInputParts(input.Messages)),
			},
		},
		valueGenerationConfigE3E3EE25: buildGeminiImageGenerationConfig(model, input.Options),
	}
	if toolsEnabled {
		tools := buildGeminiProviderTools(providerTools)
		if len(tools) > 0 {
			payload["tools"] = tools
		}
	}
	return payload, nil
}

func buildGeminiImageGenerationConfig(model string, options map[string]interface{}) map[string]interface{} {
	generationConfig := map[string]interface{}{
		"responseModalities": []string{valueTEXT21AF6042, valueIMAGE462C7646},
	}
	if len(options) == 0 {
		return generationConfig
	}
	rawConfig := modelParamMap(options, valueGenerationConfigE3E3EE25)
	if modalities := geminiImageResponseModalities(rawConfig["responseModalities"]); len(modalities) > 0 {
		generationConfig["responseModalities"] = modalities
	}
	if imageConfig := buildGeminiImageConfig(model, modelParamMap(rawConfig, "imageConfig")); len(imageConfig) > 0 {
		generationConfig["imageConfig"] = imageConfig
	}
	return generationConfig
}

func buildGeminiImageConfig(model string, raw map[string]interface{}) map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	imageConfig := map[string]interface{}{}
	if aspectRatio := geminiImageAspectRatio(getString(raw["aspectRatio"])); aspectRatio != "" {
		imageConfig["aspectRatio"] = aspectRatio
	}
	if imageSize := geminiImageSize(getString(raw["imageSize"]), model); imageSize != "" {
		imageConfig["imageSize"] = imageSize
	}
	if len(imageConfig) == 0 {
		return nil
	}
	return imageConfig
}

func geminiImageResponseModalities(raw interface{}) []string {
	switch value := raw.(type) {
	case string:
		return geminiImageResponseModalitiesList([]interface{}{value})
	case []string:
		items := make([]interface{}, 0, len(value))
		for _, item := range value {
			items = append(items, item)
		}
		return geminiImageResponseModalitiesList(items)
	case []interface{}:
		return geminiImageResponseModalitiesList(value)
	default:
		return nil
	}
}

func geminiImageResponseModalitiesList(raw []interface{}) []string {
	result := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, item := range raw {
		switch strings.ToLower(strings.TrimSpace(getString(item))) {
		case valueText8507C7C9:
			if _, ok := seen[valueTEXT21AF6042]; !ok {
				result = append(result, valueTEXT21AF6042)
				seen[valueTEXT21AF6042] = struct{}{}
			}
		case "image":
			if _, ok := seen[valueIMAGE462C7646]; !ok {
				result = append(result, valueIMAGE462C7646)
				seen[valueIMAGE462C7646] = struct{}{}
			}
		}
	}
	return result
}

func geminiImageAspectRatio(value string) string {
	normalized := strings.TrimSpace(value)
	switch normalized {
	case "1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", valueText9601D3C9, "21:9", "1:4", "4:1", "1:8", "8:1":
		return normalized
	default:
		return ""
	}
}

func geminiImageSize(value string, model string) string {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	if normalized == "" || geminiImageModelDisallowsImageSize(model) {
		return ""
	}
	switch normalized {
	case "512":
		if strings.Contains(strings.ToLower(strings.TrimSpace(model)), "3-pro-image") {
			return ""
		}
		return normalized
	case "1K", "2K", "4K":
		return normalized
	default:
		return ""
	}
}

func geminiImageModelDisallowsImageSize(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(normalized, "gemini-2.5-flash-image")
}

// buildGeminiImageGenerationParts 按 Google GenerateContent 格式组合文本提示词和编辑输入图。
func buildGeminiImageGenerationParts(prompt string, images []ContentPart) []map[string]interface{} {
	parts := []map[string]interface{}{
		{valueText8507C7C9: strings.TrimSpace(prompt)},
	}
	for _, image := range images {
		if len(image.Data) == 0 {
			continue
		}
		mimeType := strings.TrimSpace(image.MimeType)
		if mimeType == "" {
			mimeType = valueImageJpegDEC2BF89
		}
		parts = append(parts, map[string]interface{}{
			"inline_data": map[string]interface{}{
				"mime_type":       mimeType,
				valueData1BD6994F: base64.StdEncoding.EncodeToString(image.Data),
			},
		})
	}
	return parts
}

// parseGeminiImageGenerationOutput 抽取 Gemini inlineData 图片，文本片段只作为 revised prompt。
func parseGeminiImageGenerationOutput(body []byte) (*GenerateOutput, error) {
	parsed := make(map[string]interface{})
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	revisedPrompt := extractGeminiText(parsed)
	result := &GenerateOutput{
		ResponseID:      strings.TrimSpace(getString(parsed["responseId"])),
		Text:            revisedPrompt,
		Usage:           parseGeminiUsage(parsed),
		ToolCalls:       make([]ToolCall, 0),
		ServerToolCalls: make([]ToolCall, 0),
		GeneratedImages: extractGeminiGeneratedImages(parsed, revisedPrompt),
		RawJSON:         string(body),
	}
	return result, nil
}

// extractGeminiGeneratedImages 扫描所有候选内容，避免只读取第一个候选导致丢图。
func extractGeminiGeneratedImages(parsed map[string]interface{}, revisedPrompt string) []GeneratedImage {
	images := make([]GeneratedImage, 0)
	for _, rawCandidate := range asSlice(parsed["candidates"]) {
		candidate := asMap(rawCandidate)
		content := asMap(candidate["content"])
		for _, rawPart := range asSlice(content[valueParts73DE3A86]) {
			if image, ok := parseGeminiGeneratedImagePart(asMap(rawPart), revisedPrompt); ok {
				images = append(images, image)
			}
		}
	}
	return images
}

func parseGeminiGeneratedImagePart(part map[string]interface{}, revisedPrompt string) (GeneratedImage, bool) {
	if isGeminiThoughtPart(part) {
		return GeneratedImage{}, false
	}
	inlineData := asMap(part["inlineData"])
	if len(inlineData) == 0 {
		// Google REST 与 SDK 的 camelCase / snake_case 在适配器边界统一收敛。
		inlineData = asMap(part["inline_data"])
	}
	if len(inlineData) == 0 {
		return GeneratedImage{}, false
	}
	b64 := strings.TrimSpace(getString(inlineData[valueData1BD6994F]))
	if b64 == "" {
		return GeneratedImage{}, false
	}
	mimeType := strings.TrimSpace(getString(inlineData["mimeType"]))
	if mimeType == "" {
		mimeType = strings.TrimSpace(getString(inlineData["mime_type"]))
	}
	if mimeType == "" {
		mimeType = valueImagePng415383B9
	}
	return GeneratedImage{B64JSON: b64, MIMEType: mimeType, RevisedPrompt: revisedPrompt}, true
}

func isGeminiThoughtPart(part map[string]interface{}) bool {
	thought, ok := part["thought"].(bool)
	return ok && thought
}

var (
	errCategory0156D50E77 = errors.New("image generation prompt required")
)
