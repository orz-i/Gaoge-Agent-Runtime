package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

const (
	valueUserA8AD76BE = "user"
)

const minimumOpenAIImageReadTimeout = 5 * time.Minute

const (
	valueSize7A4CF6CA = "size"
)

const (
	valueDONE797A4BA5            = "[DONE]"
	valueApplicationJson5943A7D2 = "application/json"
	valueB64JsonE1F46707         = "b64_json"
	valueBackground2E724366      = "background"
	valueImageJpeg553301CC       = "image/jpeg"

	// openAIImageGenerationsAdapter 负责 OpenAI Images API 的图片生成端点。
	valueImagePng8EC23E97   = "image/png"
	valueImageWebpB4E4883E  = "image/webp"
	valueModel37CF506F      = "model"
	valueModeration51715731 = "moderation"
	valuePromptF12B8E58     = "prompt"
	valueQuality4F0BE3F6    = "quality"
	valueTrue55FD1986       = "true"
	valueWebpC50D3ABE       = "webp"

	// Generate 调用 OpenAI 图片生成接口，返回结构化图片结果。
)

type openAIImageGenerationsAdapter struct {
	client *Client
}

func (a *openAIImageGenerationsAdapter) Name() string { return AdapterOpenAIImageGenerations }

func (a *openAIImageGenerationsAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	route.Endpoint = EndpointImageGenerations
	return a.client.generateOpenAIImageGenerations(ctx, route, input)
}

// GenerateStream 调用 OpenAI 图片生成流式接口，事件只输出图片增量，不进入聊天 token delta 链路。
func (a *openAIImageGenerationsAdapter) GenerateStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	route.Endpoint = EndpointImageGenerations
	return a.client.generateOpenAIImageGenerationsStream(ctx, route, input, onEvent)
}

// ListModels 复用 OpenAI 兼容 models 目录，供渠道校验和展示使用。
func (a *openAIImageGenerationsAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	return a.client.listModelsOpenAICompatible(ctx, route)
}

// openAIImageEditsAdapter 负责 OpenAI Images API 的图片编辑端点。
type openAIImageEditsAdapter struct {
	client *Client
}

func (a *openAIImageEditsAdapter) Name() string { return AdapterOpenAIImageEdits }

// Generate 调用 OpenAI 图片编辑接口，返回结构化图片结果。
func (a *openAIImageEditsAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	route.Endpoint = EndpointImageEdits
	return a.client.generateOpenAIImageEdits(ctx, route, input)
}

// GenerateStream 调用 OpenAI 图片编辑流式接口，事件只输出图片增量，不进入聊天 token delta 链路。
func (a *openAIImageEditsAdapter) GenerateStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	route.Endpoint = EndpointImageEdits
	return a.client.generateOpenAIImageEditsStream(ctx, route, input, onEvent)
}

// ListModels 复用 OpenAI 兼容 models 目录，供渠道校验和展示使用。
func (a *openAIImageEditsAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	return a.client.listModelsOpenAICompatible(ctx, route)
}

// generateOpenAIImageGenerations 构造并执行 OpenAI 图片生成请求。
func (c *Client) generateOpenAIImageGenerations(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	requestURL := buildOpenAIRequestURL(route.BaseURL, EndpointImageGenerations)
	if requestURL == "" {
		return nil, errCategory17F3874D69
	}

	requestBody, err := buildOpenAIImageGenerationRequestBody(route.UpstreamModel, input)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, resolveOpenAIImageReadTimeout(route.ReadTimeoutMS))
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", valueApplicationJson5943A7D2)
	setBearerAuthHeader(req, route.APIKey)
	setOpenRouterAttributionHeaders(req, route)
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

	return parseOpenAIImageOutput(body, modelParamString(input.Options, "output_format"))
}

// generateOpenAIImageGenerationsStream 构造并执行 OpenAI 图片生成流式请求。
func (c *Client) generateOpenAIImageGenerationsStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	if !SupportsImageGenerationStream(route.Protocol, route.UpstreamModel) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedStream, AdapterOpenAIImageGenerations)
	}
	requestURL := buildOpenAIRequestURL(route.BaseURL, EndpointImageGenerations)
	if requestURL == "" {
		return nil, errCategory17F3874D69
	}

	requestBody, err := buildOpenAIImageGenerationStreamRequestBody(route.UpstreamModel, input)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	return c.executeOpenAIImageStream(ctx, route, openAIImageStreamRequest{
		URL:          requestURL,
		ContentType:  valueApplicationJson5943A7D2,
		Payload:      payload,
		DebugPayload: payload,
		OutputFormat: modelParamString(input.Options, "output_format"),
	}, onEvent)
}

func isEventStreamContentType(contentType string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(contentType)), "text/event-stream")
}

// generateOpenAIImageEdits 构造并执行 OpenAI 图片编辑请求。
func (c *Client) generateOpenAIImageEdits(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	requestURL := buildOpenAIRequestURL(route.BaseURL, EndpointImageEdits)
	if requestURL == "" {
		return nil, errCategory17F3874D69
	}

	payload, contentType, debugBody, err := buildOpenAIImageEditMultipartRequest(route.UpstreamModel, input, false)
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, resolveOpenAIImageReadTimeout(route.ReadTimeoutMS))
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	setBearerAuthHeader(req, route.APIKey)
	setOpenRouterAttributionHeaders(req, route)
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
		return nil, parseUpstreamError(resp.StatusCode, body, upstreamDebugSnapshot(req, debugBody, resp, body))
	}

	return parseOpenAIImageOutput(body, modelParamString(input.Options, "output_format"))
}

// generateOpenAIImageEditsStream 构造并执行 OpenAI 图片编辑流式请求。
func (c *Client) generateOpenAIImageEditsStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	if !SupportsImageGenerationStream(route.Protocol, route.UpstreamModel) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedStream, AdapterOpenAIImageEdits)
	}
	requestURL := buildOpenAIRequestURL(route.BaseURL, EndpointImageEdits)
	if requestURL == "" {
		return nil, errCategory17F3874D69
	}

	payload, contentType, debugBody, err := buildOpenAIImageEditMultipartRequest(route.UpstreamModel, input, true)
	if err != nil {
		return nil, err
	}

	return c.executeOpenAIImageStream(ctx, route, openAIImageStreamRequest{
		URL:          requestURL,
		ContentType:  contentType,
		Payload:      payload,
		DebugPayload: debugBody,
		OutputFormat: modelParamString(input.Options, "output_format"),
	}, onEvent)
}

type openAIImageStreamRequest struct {
	URL          string
	ContentType  string
	Payload      []byte
	DebugPayload []byte
	OutputFormat string
}

func (c *Client) executeOpenAIImageStream(
	ctx context.Context,
	route RouteConfig,
	streamRequest openAIImageStreamRequest,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	firstByteCtx, firstByteCancel := context.WithCancel(ctx)
	defer firstByteCancel()
	firstByteTimer := time.AfterFunc(resolveOpenAIImageReadTimeout(route.ReadTimeoutMS), firstByteCancel)
	req, err := newOpenAIImageStreamHTTPRequest(firstByteCtx, route, streamRequest)
	if err != nil {
		firstByteTimer.Stop()
		return nil, err
	}
	resp, err := c.httpClientForRoute(route).Do(req)
	firstByteTimer.Stop()
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return c.handleOpenAIImageStreamResponse(resp, req, route, streamRequest, onEvent)
}

func resolveOpenAIImageReadTimeout(configuredMS int) time.Duration {
	configured := resolveReadTimeout(configuredMS)
	if configured < minimumOpenAIImageReadTimeout {
		return minimumOpenAIImageReadTimeout
	}
	return configured
}

func newOpenAIImageStreamHTTPRequest(
	ctx context.Context,
	route RouteConfig,
	streamRequest openAIImageStreamRequest,
) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, streamRequest.URL, bytes.NewReader(streamRequest.Payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", streamRequest.ContentType)
	req.Header.Set("Accept", "text/event-stream")
	setBearerAuthHeader(req, route.APIKey)
	setOpenRouterAttributionHeaders(req, route)
	setAdditionalHeaders(req, route.HeadersJSON)
	return req, nil
}

func (c *Client) handleOpenAIImageStreamResponse(
	resp *http.Response,
	req *http.Request,
	route RouteConfig,
	streamRequest openAIImageStreamRequest,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseOpenAIImageStreamStatusError(resp, req, streamRequest.DebugPayload)
	}
	if !isEventStreamContentType(resp.Header.Get("Content-Type")) {
		return parseOpenAIImageStreamJSONResponse(resp, streamRequest.OutputFormat, onEvent)
	}
	return consumeOpenAIImageEventStream(resp, req, route, streamRequest, onEvent)
}

func parseOpenAIImageStreamStatusError(resp *http.Response, req *http.Request, debugPayload []byte) error {
	body, readErr := readUpstreamBody(resp.Body)
	if readErr != nil {
		return readErr
	}
	return parseUpstreamError(resp.StatusCode, body, upstreamDebugSnapshot(req, debugPayload, resp, body))
}

func parseOpenAIImageStreamJSONResponse(
	resp *http.Response,
	outputFormat string,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	body, readErr := readUpstreamBody(resp.Body)
	if readErr != nil {
		return nil, readErr
	}
	output, parseErr := parseOpenAIImageOutput(body, outputFormat)
	if parseErr != nil {
		return nil, parseErr
	}
	return output, emitOpenAIImageUsage(output.Usage, onEvent)
}

func consumeOpenAIImageEventStream(
	resp *http.Response,
	req *http.Request,
	route RouteConfig,
	streamRequest openAIImageStreamRequest,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	result := newOpenAIImageStreamResult()
	streamBody := newUpstreamBodyRecorder(newIdleTimeoutReader(resp.Body, resolveStreamIdleTimeout(route.StreamIdleTimeoutMS)))
	if err := consumeOpenAIImageStream(streamBody, streamRequest.OutputFormat, result, onEvent); err != nil {
		return nil, attachUpstreamDebug(err, upstreamDebugSnapshot(req, streamRequest.DebugPayload, resp, streamErrorBody(streamBody, err)))
	}
	return result, nil
}

func newOpenAIImageStreamResult() *GenerateOutput {
	return &GenerateOutput{
		ResponseID:      "",
		Usage:           Usage{},
		ToolCalls:       make([]ToolCall, 0),
		ServerToolCalls: make([]ToolCall, 0),
	}
}

func emitOpenAIImageUsage(usage Usage, onEvent func(GenerateStreamEvent) error) error {
	if usage == (Usage{}) || onEvent == nil {
		return nil
	}
	return onEvent(GenerateStreamEvent{Usage: usage})
}

// buildOpenAIImageGenerationRequestBody 只允许图片端点支持的请求字段进入上游。
func buildOpenAIImageGenerationRequestBody(model string, input GenerateInput) (map[string]interface{}, error) {
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	if strings.TrimSpace(prompt) == "" {
		return nil, errCategoryEF97804E6D
	}
	payload := map[string]interface{}{
		valueModel37CF506F:  strings.TrimSpace(model),
		valuePromptF12B8E58: prompt,
	}
	applyOpenAIImageGenerationParams(payload, strings.TrimSpace(model), input.Options)
	return payload, nil
}

// buildOpenAIImageGenerationStreamRequestBody 只在流式 adapter 内写入 stream / partial_images。
func buildOpenAIImageGenerationStreamRequestBody(model string, input GenerateInput) (map[string]interface{}, error) {
	payload, err := buildOpenAIImageGenerationRequestBody(model, input)
	if err != nil {
		return nil, err
	}
	if !openAIImageGenerationModelSupportsStream(model) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedStream, AdapterOpenAIImageGenerations)
	}
	payload["stream"] = true
	applyOpenAIImageGenerationStreamParams(payload, input.Options)
	return payload, nil
}

// buildOpenAIImageEditMultipartRequest 构造 OpenAI Images Edits 官方 multipart 请求体。
func buildOpenAIImageEditMultipartRequest(model string, input GenerateInput, stream bool) ([]byte, string, []byte, error) {
	prompt, images, err := validateOpenAIImageEditInput(model, input, stream)
	if err != nil {
		return nil, "", nil, err
	}
	formFields := openAIImageEditFormFields(model, prompt, input.Options, stream)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writeOpenAIMultipartFields(writer, formFields); err != nil {
		return nil, "", nil, err
	}
	if err := writeOpenAIImageEditImages(writer, images); err != nil {
		return nil, "", nil, err
	}
	if err := writeOpenAIImageEditMask(writer, input.ImageEditMask); err != nil {
		return nil, "", nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, "", nil, err
	}
	debugBody := buildOpenAIImageEditDebugBody(formFields, len(images), input.ImageEditMask != nil && len(input.ImageEditMask.Data) > 0)
	return body.Bytes(), writer.FormDataContentType(), debugBody, nil
}

func validateOpenAIImageEditInput(model string, input GenerateInput, stream bool) (string, []ContentPart, error) {
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	if strings.TrimSpace(prompt) == "" {
		return "", nil, errCategory3D13562A09
	}
	images := collectImageInputParts(input.Messages)
	if len(images) == 0 {
		return "", nil, errCategory5D790684CD
	}
	if stream && !openAIImageEditModelSupportsStream(model) {
		return "", nil, fmt.Errorf("%w: %s", ErrUnsupportedStream, AdapterOpenAIImageEdits)
	}
	return prompt, images, nil
}

func openAIImageEditFormFields(model string, prompt string, options map[string]interface{}, stream bool) map[string]string {
	formFields := map[string]string{
		valueModel37CF506F:  strings.TrimSpace(model),
		valuePromptF12B8E58: prompt,
	}
	applyOpenAIImageEditParams(formFields, strings.TrimSpace(model), options)
	if stream {
		formFields["stream"] = valueTrue55FD1986
		applyOpenAIImageEditStreamParams(formFields, options)
	}
	return formFields
}

func writeOpenAIMultipartFields(writer *multipart.Writer, formFields map[string]string) error {
	for key, value := range formFields {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := writer.WriteField(key, value); err != nil {
			return err
		}
	}
	return nil
}

func writeOpenAIImageEditImages(writer *multipart.Writer, images []ContentPart) error {
	for index, image := range images {
		fileName := openAIImageEditFileName(image, index)
		if err := writeOpenAIMultipartFile(writer, "image[]", fileName, image.MimeType, image.Data); err != nil {
			return err
		}
	}
	return nil
}

func openAIImageEditFileName(image ContentPart, index int) string {
	if fileName := strings.TrimSpace(image.FileName); fileName != "" {
		return fileName
	}
	return fmt.Sprintf("image-%02d%s", index+1, openAIImageFileExtension(image.MimeType))
}

func writeOpenAIImageEditMask(writer *multipart.Writer, mask *ContentPart) error {
	if mask == nil || len(mask.Data) == 0 {
		return nil
	}
	fileName := strings.TrimSpace(mask.FileName)
	if fileName == "" {
		fileName = "mask" + openAIImageFileExtension(mask.MimeType)
	}
	return writeOpenAIMultipartFile(writer, "mask", fileName, mask.MimeType, mask.Data)
}

func applyOpenAIImageEditParams(fields map[string]string, model string, options map[string]interface{}) {
	if openAIImageGenerationModelSupportsResponseFormat(model) {
		fields["response_format"] = defaultImageResponseFormat(options)
	}
	for _, key := range []string{valueQuality4F0BE3F6, valueSize7A4CF6CA, valueUserA8AD76BE} {
		if value := modelParamString(options, key); value != "" {
			fields[key] = value
		}
	}
	if value := modelParamInt(options, "n"); value > 0 {
		fields["n"] = fmt.Sprintf("%d", value)
	}
	applyOpenAIImageEditGPTParams(fields, model, options)
}

func applyOpenAIImageEditGPTParams(fields map[string]string, model string, options map[string]interface{}) {
	if !openAIImageEditModelSupportsGPTImageParams(model) {
		return
	}
	for _, key := range []string{valueBackground2E724366, "output_format"} {
		if value := modelParamString(options, key); value != "" {
			fields[key] = value
		}
	}
	if openAIImageEditModelSupportsInputFidelity(model) {
		if value := modelParamString(options, "input_fidelity"); value != "" {
			fields["input_fidelity"] = value
		}
	}
	if value := modelParamInt(options, "output_compression"); value > 0 {
		fields["output_compression"] = fmt.Sprintf("%d", value)
	}
}

func applyOpenAIImageEditStreamParams(fields map[string]string, options map[string]interface{}) {
	value, ok := modelParamIntValue(options, "partial_images")
	if !ok {
		return
	}
	if value > 0 {
		fields["partial_images"] = fmt.Sprintf("%d", value)
	}
}

func writeOpenAIMultipartFile(writer *multipart.Writer, fieldName string, fileName string, mimeType string, data []byte) error {
	if len(data) == 0 {
		return withMessage(errCategory0FA353D29A, fmt.Sprintf("multipart file %s is empty", fieldName))
	}
	normalizedMIME := strings.TrimSpace(mimeType)
	if normalizedMIME == "" {
		normalizedMIME = valueImagePng8EC23E97
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeMultipartQuote(fieldName), escapeMultipartQuote(fileName)))
	header.Set("Content-Type", normalizedMIME)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}

func buildOpenAIImageEditDebugBody(fields map[string]string, imageCount int, hasMask bool) []byte {
	payload := make(map[string]interface{}, len(fields)+2)
	for key, value := range fields {
		payload[key] = value
	}
	payload["image_count"] = imageCount
	payload["mask"] = hasMask
	raw, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{"multipart":true}`)
	}
	return raw
}

func escapeMultipartQuote(value string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(strings.TrimSpace(value))
}

// applyOpenAIImageGenerationParams 从 options 中提取官方 Images API 参数。
func applyOpenAIImageGenerationParams(payload map[string]interface{}, model string, options map[string]interface{}) {
	if openAIImageGenerationModelSupportsResponseFormat(model) {
		payload["response_format"] = defaultImageResponseFormat(options)
	}
	for _, key := range []string{valueQuality4F0BE3F6, valueSize7A4CF6CA, valueUserA8AD76BE} {
		if value := modelParamString(options, key); value != "" {
			payload[key] = value
		}
	}
	if value := modelParamInt(options, "n"); value > 0 {
		payload["n"] = value
	}
	applyOpenAIImageGenerationGPTParams(payload, model, options)
	applyOpenAIImageGenerationStyleParam(payload, model, options)
}

func applyOpenAIImageGenerationGPTParams(payload map[string]interface{}, model string, options map[string]interface{}) {
	if openAIImageGenerationModelSupportsGPTImageParams(model) {
		for _, key := range []string{valueBackground2E724366, valueModeration51715731, "output_format"} {
			if value := modelParamString(options, key); value != "" {
				payload[key] = value
			}
		}
		if value := modelParamInt(options, "output_compression"); value > 0 {
			payload["output_compression"] = value
		}
	}
}

func applyOpenAIImageGenerationStyleParam(payload map[string]interface{}, model string, options map[string]interface{}) {
	if openAIImageGenerationModelSupportsStyle(model) {
		if value := modelParamString(options, "style"); value != "" {
			payload["style"] = value
		}
	}
}

func defaultImageResponseFormat(options map[string]interface{}) string {
	if value := modelParamString(options, "response_format"); value != "" {
		return value
	}
	return valueB64JsonE1F46707
}

// applyOpenAIImageGenerationStreamParams 只处理流式图片端点支持的增量参数。
func applyOpenAIImageGenerationStreamParams(payload map[string]interface{}, options map[string]interface{}) {
	value, ok := modelParamIntValue(options, "partial_images")
	if !ok {
		return
	}
	if value > 0 {
		payload["partial_images"] = value
	}
}

func openAIImageGenerationModelSupportsStream(model string) bool {
	return openAIImageGenerationModelSupportsGPTImageParams(model)
}

func openAIImageGenerationModelSupportsGPTImageParams(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-image-")
}

func openAIImageEditModelSupportsStream(model string) bool {
	return openAIImageEditModelSupportsGPTImageParams(model)
}

func openAIImageEditModelSupportsGPTImageParams(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(normalized, "gpt-image-") || normalized == "chatgpt-image-latest"
}

func openAIImageEditModelSupportsInputFidelity(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return openAIImageEditModelSupportsGPTImageParams(normalized) &&
		normalized != "gpt-image-2" && !strings.HasPrefix(normalized, "gpt-image-2-")
}

func openAIImageGenerationModelSupportsResponseFormat(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "dall-e-2", "dall-e-3":
		return true
	default:
		return false
	}
}

func openAIImageGenerationModelSupportsStyle(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), "dall-e-3")
}

// buildOpenAIImageGenerationPrompt 使用最后一条用户文本作为图片提示词。
func buildOpenAIImageGenerationPrompt(messages []Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		msg := messages[index]
		if normalizeRole(msg.Role) != valueUserA8AD76BE {
			continue
		}
		if text := strings.TrimSpace(messagePromptText(msg)); text != "" {
			return text
		}
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if text := strings.TrimSpace(messagePromptText(messages[index])); text != "" {
			return text
		}
	}
	return ""
}

// messagePromptText 抽取消息中的可读文本，文件文本只作为提示词补充。
func messagePromptText(msg Message) string {
	if len(msg.Parts) == 0 {
		return msg.Content
	}
	parts := make([]string, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		switch part.Kind {
		case ContentPartText, ContentPartFile:
			if text := strings.TrimSpace(part.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// parseOpenAIImageOutput 解析 OpenAI 图片响应。
// 图片字节只放入 GeneratedImages，避免把 data URL 写入普通文本链路。
func parseOpenAIImageOutput(body []byte, outputFormat string) (*GenerateOutput, error) {
	parsed := make(map[string]interface{})
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	result := &GenerateOutput{
		ToolCalls:       make([]ToolCall, 0),
		ServerToolCalls: make([]ToolCall, 0),
		RawJSON:         string(body),
	}
	applyOpenAIImageCompletedPayload(parsed, outputFormat, result)
	return result, nil
}

func applyOpenAIImageCompletedPayload(parsed map[string]interface{}, outputFormat string, result *GenerateOutput) {
	if result == nil {
		return
	}
	if result.ResponseID == "" {
		result.ResponseID = strings.TrimSpace(getString(parsed["id"]))
	}
	if result.ResponseID == "" {
		result.ResponseID = strings.TrimSpace(getStringFromPath(parsed, "response", "id"))
	}
	if usage := parseOpenAICompatibleUsageForAdapter(AdapterOpenAIImageGenerations, parsed); usage != (Usage{}) {
		result.Usage = usage
	}
	data, _ := parsed["data"].([]interface{})
	citations := appendOpenAIImageCompletedData(result, data, outputFormat)
	if applyNestedOpenAIImageCompletedPayload(parsed, data, outputFormat, result) {
		return
	}
	citations = appendOpenAIImageCompletedFallback(result, parsed, data, outputFormat, citations)
	result.Citations = appendUniqueStrings(result.Citations, citations...)
}

func appendOpenAIImageCompletedData(result *GenerateOutput, data []interface{}, outputFormat string) []string {
	citations := make([]string, 0, len(data))
	for _, item := range data {
		citations = appendOpenAIImage(result, asMap(item), outputFormat, citations)
	}
	return citations
}

func appendOpenAIImage(
	result *GenerateOutput,
	payload map[string]interface{},
	outputFormat string,
	citations []string,
) []string {
	image, ok := parseOpenAIImagePayload(payload, outputFormat)
	if !ok {
		return citations
	}
	if url := strings.TrimSpace(image.URL); url != "" {
		citations = append(citations, url)
	}
	result.GeneratedImages = append(result.GeneratedImages, image)
	return citations
}

func applyNestedOpenAIImageCompletedPayload(
	parsed map[string]interface{},
	data []interface{},
	outputFormat string,
	result *GenerateOutput,
) bool {
	if len(data) != 0 {
		return false
	}
	response := asMap(parsed["response"])
	if len(response) == 0 {
		return false
	}
	applyOpenAIImageCompletedPayload(response, outputFormat, result)
	return true
}

func appendOpenAIImageCompletedFallback(
	result *GenerateOutput,
	parsed map[string]interface{},
	data []interface{},
	outputFormat string,
	citations []string,
) []string {
	if len(data) != 0 {
		return citations
	}
	return appendOpenAIImage(result, parsed, outputFormat, citations)
}

func parseOpenAIImagePayload(payload map[string]interface{}, outputFormat string) (GeneratedImage, bool) {
	if len(payload) == 0 {
		return GeneratedImage{}, false
	}
	if url := strings.TrimSpace(getString(payload["url"])); url != "" {
		return GeneratedImage{
			URL:           url,
			MIMEType:      openAIImageMIMEType(outputFormat),
			RevisedPrompt: strings.TrimSpace(getString(payload["revised_prompt"])),
		}, true
	}
	if b64 := strings.TrimSpace(getString(payload[valueB64JsonE1F46707])); b64 != "" {
		return GeneratedImage{
			B64JSON:       b64,
			MIMEType:      openAIImageMIMEType(outputFormat),
			RevisedPrompt: strings.TrimSpace(getString(payload["revised_prompt"])),
		}, true
	}
	if nested := asMap(payload["image"]); len(nested) > 0 {
		image, ok := parseOpenAIImagePayload(nested, outputFormat)
		if !ok {
			return GeneratedImage{}, false
		}
		if image.RevisedPrompt == "" {
			image.RevisedPrompt = strings.TrimSpace(getString(payload["revised_prompt"]))
		}
		return image, true
	}
	return GeneratedImage{}, false
}

func consumeOpenAIImageStream(
	reader io.Reader,
	outputFormat string,
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) error {
	parser := newOpenAIImageSSEParser(outputFormat, result, onEvent)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 256*1024), 64*1024*1024)

	for scanner.Scan() {
		if err := parser.consumeLine(scanner.Text()); err != nil {
			if errors.Is(err, errStreamDone) {
				return nil
			}
			return err
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	if err := parser.flush(); err != nil && !errors.Is(err, errStreamDone) {
		return err
	}
	return parser.applyRawFallback()
}

type openAIImageSSEParser struct {
	eventName    string
	dataLines    []string
	rawLines     []string
	outputFormat string
	result       *GenerateOutput
	onEvent      func(GenerateStreamEvent) error
}

func newOpenAIImageSSEParser(
	outputFormat string,
	result *GenerateOutput,
	onEvent func(GenerateStreamEvent) error,
) *openAIImageSSEParser {
	return &openAIImageSSEParser{
		dataLines:    make([]string, 0, 4),
		rawLines:     make([]string, 0, 16),
		outputFormat: outputFormat,
		result:       result,
		onEvent:      onEvent,
	}
}

func (p *openAIImageSSEParser) consumeLine(raw string) error {
	line := strings.TrimRight(raw, "\r")
	if line == "" {
		return p.flush()
	}
	if strings.HasPrefix(line, "event:") {
		p.eventName = strings.TrimSpace(line[len("event:"):])
		return nil
	}
	if strings.HasPrefix(line, "data:") {
		p.dataLines = append(p.dataLines, strings.TrimPrefix(line[len("data:"):], " "))
		return nil
	}
	if !strings.HasPrefix(line, ":") {
		p.rawLines = append(p.rawLines, line)
	}
	return nil
}

func (p *openAIImageSSEParser) flush() error {
	if len(p.dataLines) == 0 && strings.TrimSpace(p.eventName) == "" {
		return nil
	}
	eventName := strings.TrimSpace(p.eventName)
	payloadText := strings.Join(p.dataLines, "\n")
	p.eventName = ""
	p.dataLines = p.dataLines[:0]
	return p.dispatch(eventName, payloadText)
}

func (p *openAIImageSSEParser) dispatch(eventName string, payloadText string) error {
	trimmedPayload := strings.TrimSpace(payloadText)
	if trimmedPayload == "" {
		return nil
	}
	if trimmedPayload == valueDONE797A4BA5 {
		return errStreamDone
	}
	p.appendRawJSON(payloadText)
	parsed, err := parseOpenAIImageStreamPayload(payloadText)
	if err != nil {
		return err
	}
	eventType := openAIImageStreamEventType(eventName, parsed)
	if err := p.applyUsage(parsed); err != nil {
		return err
	}
	return p.applyEvent(eventType, parsed)
}

func parseOpenAIImageStreamPayload(payloadText string) (map[string]interface{}, error) {
	parsed, err := decodeIndexedStreamObject(payloadText)
	if err != nil {
		return nil, err
	}
	if err := parseStreamUpstreamError(parsed, payloadText); err != nil {
		return nil, err
	}
	return parsed, nil
}

func openAIImageStreamEventType(eventName string, parsed map[string]interface{}) string {
	eventType := strings.TrimSpace(getString(parsed["type"]))
	if eventType != "" {
		return eventType
	}
	if eventName != "" {
		return eventName
	}
	return "image_generation.completed"
}

func (p *openAIImageSSEParser) applyUsage(parsed map[string]interface{}) error {
	usage := parseOpenAICompatibleUsageForAdapter(AdapterOpenAIImageGenerations, parsed)
	if usage == (Usage{}) {
		usage = parseOpenAICompatibleUsageForAdapter(AdapterOpenAIImageGenerations, asMap(parsed["response"]))
	}
	if p.result != nil && usage != (Usage{}) {
		p.result.Usage = usage
	}
	return emitOpenAIImageUsage(usage, p.onEvent)
}

func (p *openAIImageSSEParser) applyEvent(eventType string, parsed map[string]interface{}) error {
	if responseID := strings.TrimSpace(getString(parsed["id"])); responseID != "" && p.result != nil && p.result.ResponseID == "" {
		p.result.ResponseID = responseID
	}
	switch {
	case strings.Contains(eventType, "partial_image"):
		return emitOpenAIImagePartial(parsed, p.outputFormat, p.onEvent)
	case strings.Contains(eventType, "completed") || strings.Contains(eventType, "final"):
		applyOpenAIImageCompletedPayload(parsed, p.outputFormat, p.result)
	}
	return nil
}

func (p *openAIImageSSEParser) appendRawJSON(payloadText string) {
	if p.result == nil {
		return
	}
	if p.result.RawJSON != "" {
		p.result.RawJSON += "\n"
	}
	p.result.RawJSON += payloadText
}

func (p *openAIImageSSEParser) applyRawFallback() error {
	if len(p.rawLines) == 0 || p.result == nil || len(p.result.GeneratedImages) != 0 {
		return nil
	}
	payloadText := strings.TrimSpace(strings.Join(p.rawLines, "\n"))
	if payloadText == "" {
		return nil
	}
	parsed, err := parseOpenAIImageStreamPayload(payloadText)
	if err != nil {
		return err
	}
	p.appendRawJSON(payloadText)
	applyOpenAIImageCompletedPayload(parsed, p.outputFormat, p.result)
	return emitOpenAIImageUsage(p.result.Usage, p.onEvent)
}

func emitOpenAIImagePartial(
	parsed map[string]interface{},
	outputFormat string,
	onEvent func(GenerateStreamEvent) error,
) error {
	if onEvent == nil {
		return nil
	}
	image, ok := parseOpenAIImagePayload(parsed, outputFormat)
	if !ok {
		return nil
	}
	index, err := streamIndexField(parsed, "partial_image_index")
	if err != nil {
		return err
	}
	if index == 0 {
		index, err = streamIndexField(parsed, "index")
		if err != nil {
			return err
		}
	}
	return onEvent(GenerateStreamEvent{
		GeneratedImage:        &image,
		GeneratedImageIndex:   index,
		GeneratedImagePartial: true,
		ResponseID:            strings.TrimSpace(getString(parsed["id"])),
	})
}

// openAIImageMIMEType 根据 output_format 推断上游图片 MIME。
func openAIImageMIMEType(outputFormat string) string {
	switch strings.TrimSpace(strings.ToLower(outputFormat)) {
	case "jpeg", "jpg":
		return valueImageJpeg553301CC
	case valueWebpC50D3ABE:
		return valueImageWebpB4E4883E
	default:
		return valueImagePng8EC23E97
	}
}

func openAIImageFileExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case valueImageJpeg553301CC, "image/jpg":
		return ".jpg"
	case valueImageWebpB4E4883E:
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}

var (
	errCategory5D790684CD = errors.New("image edit input image required")
	errCategory3D13562A09 = errors.New("image edit prompt required")
	errCategoryEF97804E6D = errors.New("image generation prompt required")
	errCategory17F3874D69 = errors.New("invalid base url")
	errCategory0FA353D29A = errors.New("error category 0FA353D29A")
)
