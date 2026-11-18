package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	volcengineVideoTasksPath          = "/contents/generations/tasks"
	volcengineVideoMIMEType           = "video/mp4"
	volcengineVideoStatusPending      = "pending"
	volcengineVideoStatusQueued       = "queued"
	volcengineVideoStatusRunning      = "running"
	volcengineVideoStatusSucceeded    = "succeeded"
	volcengineVideoStatusFailed       = "failed"
	volcengineVideoStatusCancelled    = "cancelled"
	volcengineVideoImageRoleOption    = "image_role"
	volcengineVideoResolutionOption   = "resolution"
	volcengineVideoDurationOption     = "duration"
	volcengineVideoWatermarkOption    = "watermark"
	volcengineVideoRoleFirstFrame     = "first_frame"
	volcengineVideoRoleLastFrame      = "last_frame"
	volcengineVideoRoleReferenceImage = "reference_image"
	volcengineVideoImageRolesOption   = "image_roles"
	volcengineSeedance2Model          = "doubao-seedance-2-0-260128"
	volcengineSeedance2FastModel      = "doubao-seedance-2-0-fast-260128"
	volcengineSeedance2MinDuration    = 4
	volcengineSeedance2MaxDuration    = 15
	volcengineSeedance2MaxImages      = 9
)

var (
	errVolcengineVideoTaskIDMissing = errors.New("volcengine video task response is missing id")
	errVolcengineVideoURLMissing    = errors.New("volcengine video task succeeded without video_url")
	errVolcengineVideoTaskTimeout   = errors.New("volcengine video task polling timed out")
)

type volcengineVideoGenerationAdapter struct {
	client *Client
}

func (a *volcengineVideoGenerationAdapter) Name() string { return AdapterVolcengineVideo }

func (a *volcengineVideoGenerationAdapter) Generate(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
) (*GenerateOutput, error) {
	route = normalizeVolcengineRoute(route, AdapterVolcengineVideo, EndpointContentGenerationTasks)
	return a.client.generateVolcengineVideo(ctx, route, input)
}

func (a *volcengineVideoGenerationAdapter) GenerateStream(
	_ context.Context,
	_ RouteConfig,
	_ GenerateInput,
	_ func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	return nil, fmt.Errorf("%w: %s", ErrUnsupportedStream, AdapterVolcengineVideo)
}

func (a *volcengineVideoGenerationAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	route = normalizeVolcengineRoute(route, AdapterVolcengineVideo, EndpointContentGenerationTasks)
	return a.client.listModelsOpenAICompatible(ctx, route)
}

type volcengineVideoTask struct {
	ID       string                     `json:"id"`
	Status   string                     `json:"status"`
	Duration int64                      `json:"duration"`
	Content  volcengineVideoTaskContent `json:"content"`
	Error    volcengineVideoTaskError   `json:"error"`
	Usage    map[string]interface{}     `json:"usage"`
}

type volcengineVideoTaskContent struct {
	VideoURL string `json:"video_url"`
}

type volcengineVideoTaskError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *Client) generateVolcengineVideo(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
) (*GenerateOutput, error) {
	if err := validateVolcengineSeedance2Request(route.UpstreamModel, input); err != nil {
		return nil, err
	}
	requestBody, _, err := buildVolcengineVideoRequestBody(route.UpstreamModel, input)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, resolveMediaPollTimeout(route, input.Options))
	defer cancel()

	taskURL := buildVersionedEndpointURL(route.BaseURL, "v3", volcengineVideoTasksPath)
	body, _, err := c.doMediaJSONRequest(requestCtx, route, http.MethodPost, taskURL, payload)
	if err != nil {
		return nil, err
	}
	task, err := parseVolcengineVideoTask(body)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(task.ID) == "" {
		return nil, errVolcengineVideoTaskIDMissing
	}
	return c.pollVolcengineVideoTask(requestCtx, route, task.ID, input.Options)
}

func validateVolcengineSeedance2Request(model string, input GenerateInput) error {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case volcengineSeedance2Model, volcengineSeedance2FastModel:
	default:
		return fmt.Errorf("%w: unsupported or retired Seedance model %q", errCategoryEF97804E6D, model)
	}
	duration, found := modelParamIntValue(input.Options, volcengineVideoDurationOption)
	if !found || duration < volcengineSeedance2MinDuration || duration > volcengineSeedance2MaxDuration {
		return fmt.Errorf(
			"%w: Seedance 2 duration must be %d-%d seconds",
			errCategoryEF97804E6D,
			volcengineSeedance2MinDuration,
			volcengineSeedance2MaxDuration,
		)
	}
	if len(collectImageInputParts(input.Messages)) > volcengineSeedance2MaxImages {
		return fmt.Errorf("%w: Seedance 2 accepts at most %d image references", errCategoryEF97804E6D, volcengineSeedance2MaxImages)
	}
	return nil
}

func buildVolcengineVideoRequestBody(model string, input GenerateInput) (map[string]interface{}, string, error) {
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	if strings.TrimSpace(prompt) == "" {
		return nil, "", errCategoryEF97804E6D
	}
	content := []map[string]interface{}{
		{valueType945D3026: ContentPartText, valueText1D495308: prompt},
	}
	payload := map[string]interface{}{
		valueModel37CF506F:   strings.TrimSpace(model),
		valueContent03EE2C26: content,
	}
	content = append(content, volcengineVideoImageContent(input.Messages, input.Options)...)
	payload[valueContent03EE2C26] = content
	applyVolcengineVideoOptions(payload, input.Options)
	return payload, prompt, nil
}

func volcengineVideoImageContent(messages []Message, options map[string]interface{}) []map[string]interface{} {
	images := collectImageInputParts(messages)
	content := make([]map[string]interface{}, 0, len(images))
	configuredRole := modelParamString(options, volcengineVideoImageRoleOption)
	configuredRoles := modelParamStringList(options, volcengineVideoImageRolesOption)
	for index, image := range images {
		mimeType := strings.TrimSpace(image.MimeType)
		if mimeType == "" {
			mimeType = valueImagePng8EC23E97
		}
		content = append(content, map[string]interface{}{
			valueType945D3026: "image_url",
			"image_url": map[string]interface{}{
				"url": "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(image.Data),
			},
			"role": volcengineVideoImageRoleAt(configuredRole, configuredRoles, index),
		})
	}
	return content
}

func volcengineVideoImageRoleAt(configured string, configuredRoles []string, index int) string {
	if index < len(configuredRoles) {
		switch role := strings.TrimSpace(configuredRoles[index]); role {
		case volcengineVideoRoleFirstFrame, volcengineVideoRoleLastFrame, volcengineVideoRoleReferenceImage:
			return role
		}
	}
	return volcengineVideoImageRole(configured, index)
}

func volcengineVideoImageRole(configured string, index int) string {
	if configured == volcengineVideoRoleReferenceImage {
		return volcengineVideoRoleReferenceImage
	}
	if index == 0 {
		return volcengineVideoRoleFirstFrame
	}
	if index == 1 {
		return volcengineVideoRoleLastFrame
	}
	return volcengineVideoRoleReferenceImage
}

func applyVolcengineVideoOptions(payload map[string]interface{}, options map[string]interface{}) {
	for _, key := range []string{volcengineVideoResolutionOption, "ratio", valueServiceTier0757A6BB, "callback_url", "negative_prompt"} {
		if value := modelParamString(options, key); value != "" {
			payload[key] = value
		}
	}
	for _, key := range []string{volcengineVideoDurationOption, "frames", valueSeedC8D041FC, "fps", "execution_expires_after"} {
		if value, ok := modelParamIntValue(options, key); ok {
			payload[key] = value
		}
	}
	for _, key := range []string{"camerafixed", volcengineVideoWatermarkOption, "generate_audio", "return_last_frame", "draft"} {
		if value, ok := modelParamBoolValue(options, key); ok {
			payload[key] = value
		}
	}
}

func (c *Client) pollVolcengineVideoTask(
	ctx context.Context,
	route RouteConfig,
	taskID string,
	options map[string]interface{},
) (*GenerateOutput, error) {
	taskURL := buildVersionedEndpointURL(route.BaseURL, "v3", volcengineVideoTasksPath) + "/" + url.PathEscape(taskID)
	interval := resolveMediaPollInterval(options)
	for {
		body, debug, err := c.doMediaJSONRequest(ctx, route, http.MethodGet, taskURL, nil)
		if err != nil {
			return nil, volcengineVideoPollError(ctx, taskID, err)
		}
		task, err := parseVolcengineVideoTask(body)
		if err != nil {
			return nil, err
		}
		output, done, err := resolveVolcengineVideoTask(task, body, debug)
		if err != nil || done {
			return output, err
		}
		if err := waitMediaPoll(ctx, interval); err != nil {
			return nil, volcengineVideoTimeoutError(taskID)
		}
	}
}

func parseVolcengineVideoTask(body []byte) (volcengineVideoTask, error) {
	var task volcengineVideoTask
	if err := json.Unmarshal(body, &task); err != nil {
		return volcengineVideoTask{}, err
	}
	task.ID = strings.TrimSpace(task.ID)
	task.Status = strings.ToLower(strings.TrimSpace(task.Status))
	task.Content.VideoURL = strings.TrimSpace(task.Content.VideoURL)
	return task, nil
}

func resolveVolcengineVideoTask(
	task volcengineVideoTask,
	body []byte,
	debug *UpstreamDebugSnapshot,
) (*GenerateOutput, bool, error) {
	switch task.Status {
	case volcengineVideoStatusSucceeded:
		output, err := volcengineVideoOutput(task, body, debug)
		return output, true, err
	case volcengineVideoStatusFailed, volcengineVideoStatusCancelled:
		return nil, true, volcengineVideoTaskErrorResponse(task, body, debug)
	case "", volcengineVideoStatusPending, volcengineVideoStatusQueued, volcengineVideoStatusRunning:
		return nil, false, nil
	default:
		return nil, true, &UpstreamError{
			StatusCode: http.StatusBadGateway,
			Message:    "unsupported volcengine video task status: " + task.Status,
			Body:       string(body),
			Debug:      debug,
		}
	}
}

func volcengineVideoOutput(
	task volcengineVideoTask,
	body []byte,
	debug *UpstreamDebugSnapshot,
) (*GenerateOutput, error) {
	if task.Content.VideoURL == "" {
		return nil, fmt.Errorf("%w: task_id=%s", errVolcengineVideoURLMissing, task.ID)
	}
	rawUsage, err := json.Marshal(task.Usage)
	if err != nil {
		return nil, err
	}
	metadataJSON := ""
	if task.Duration > 0 {
		metadataJSON = fmt.Sprintf(`{"durationSeconds":%d,"source":"provider"}`, task.Duration)
	}
	return &GenerateOutput{
		ResponseID: task.ID,
		Text:       task.Content.VideoURL,
		Usage: Usage{
			OutputTokens: toInt64(task.Usage["completion_tokens"]),
			RawUsageJSON: string(rawUsage),
		},
		GeneratedAssets: []GeneratedAsset{{
			Kind: GeneratedAssetKindVideo, URL: task.Content.VideoURL,
			// This task response contains no provider-authored prompt revision.
			// The original request remains in its reviewed prompt binding.
			MIMEType: volcengineVideoMIMEType, ExternalID: task.ID, MetadataJSON: metadataJSON,
		}},
		RawJSON: string(body),
		Debug:   debug,
	}, nil
}

func volcengineVideoTaskErrorResponse(
	task volcengineVideoTask,
	body []byte,
	debug *UpstreamDebugSnapshot,
) error {
	message := strings.TrimSpace(task.Error.Message)
	if message == "" {
		message = "volcengine video task " + task.Status
	}
	if code := strings.TrimSpace(task.Error.Code); code != "" {
		message = code + ": " + message
	}
	return &UpstreamError{
		StatusCode: http.StatusBadRequest,
		Message:    message,
		Body:       string(body),
		Debug:      debug,
	}
}

func volcengineVideoPollError(ctx context.Context, taskID string, err error) error {
	if ctx.Err() != nil {
		return volcengineVideoTimeoutError(taskID)
	}
	return err
}

func volcengineVideoTimeoutError(taskID string) error {
	return &UpstreamError{
		StatusCode: http.StatusRequestTimeout,
		Message:    fmt.Sprintf("%s: task_id=%s", errVolcengineVideoTaskTimeout.Error(), taskID),
	}
}
