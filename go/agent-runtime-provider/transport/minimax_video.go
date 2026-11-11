package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"
)

func (c *Client) generateMiniMaxVideo(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	payload, err := buildMiniMaxVideoRequest(route.UpstreamModel, input)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, resolveMediaPollTimeout(route, input.Options))
	defer cancel()
	// A redirect must never replay creation of an asynchronous/billable task.
	route.SingleProviderAttempt = true
	created, body, debug, err := c.doMiniMaxRequest(requestCtx, route, http.MethodPost, "/v2/video_generation", encoded)
	if err != nil {
		return nil, err
	}
	taskID := strings.TrimSpace(getString(created["task_id"]))
	if taskID == "" {
		return nil, &UpstreamError{StatusCode: http.StatusConflict, Message: "MiniMax video creation returned no task_id; outcome unknown", Body: string(body), Debug: debug}
	}
	return c.pollMiniMaxVideo(requestCtx, route, taskID, input.Options)
}

func buildMiniMaxVideoRequest(model string, input GenerateInput) (map[string]interface{}, error) {
	if err := rejectUnsupportedImageMask(input.ImageEditMask, AdapterMiniMaxVideo); err != nil {
		return nil, err
	}
	model = strings.TrimSpace(model)
	if model != miniMaxModelH3 && model != miniMaxModelH3Max {
		return nil, fmt.Errorf("%w: unsupported MiniMax video model %q", errCategoryEF97804E6D, model)
	}
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	if strings.TrimSpace(prompt) == "" || utf8.RuneCountInString(prompt) > 7000 {
		return nil, fmt.Errorf("%w: MiniMax video prompt must contain 1-7000 characters", errCategoryEF97804E6D)
	}
	payload := map[string]interface{}{valueModel2D31E8EB: model, volcengineVideoResolutionOption: miniMaxResolution768P, volcengineVideoDurationOption: 6, miniMaxVideoRatioOption: valueText9601D3C9}
	copyMiniMaxOptions(payload, input.Options, volcengineVideoResolutionOption, volcengineVideoDurationOption, miniMaxVideoRatioOption, "extra", "aigc_watermark")
	if err := validateMiniMaxVideoSettings(payload); err != nil {
		return nil, err
	}
	media, err := miniMaxVideoInputs(input)
	if err != nil {
		return nil, err
	}
	payload[valueContent3EAD52FD] = append([]map[string]interface{}{{valueTypeF2B37301: valueText53ED4E15, valueText53ED4E15: prompt}}, media...)
	return payload, nil
}

func validateMiniMaxVideoSettings(payload map[string]interface{}) error {
	minimum := 4.0
	resolutions := []string{miniMaxResolution768P, "2K"}
	if modelParamString(payload, valueModel2D31E8EB) == miniMaxModelH3Max {
		minimum = 5
		resolutions = []string{"480P", miniMaxResolution768P}
	}
	if !miniMaxIntegerInRange(payload, volcengineVideoDurationOption, minimum, 15) {
		return fmt.Errorf("%w: invalid MiniMax video duration", errCategoryEF97804E6D)
	}
	if !slices.Contains(resolutions, modelParamString(payload, volcengineVideoResolutionOption)) {
		return fmt.Errorf("%w: invalid MiniMax video resolution", errCategoryEF97804E6D)
	}
	if !slices.Contains([]string{valueAdaptive8B08E1F4, "21:9", valueText9601D3C9, "4:3", "1:1", miniMaxPortraitRatio, "9:16"}, modelParamString(payload, miniMaxVideoRatioOption)) {
		return fmt.Errorf("%w: invalid MiniMax video ratio", errCategoryEF97804E6D)
	}
	return nil
}

func miniMaxVideoInputs(input GenerateInput) ([]map[string]interface{}, error) {
	images := miniMaxVideoImageURLs(input)
	roles := modelParamStringList(input.Options, volcengineVideoImageRolesOption)
	videos := modelParamStringList(input.Options, "reference_video_urls")
	audios := modelParamStringList(input.Options, "reference_audio_urls")
	if err := validateMiniMaxVideoReferenceLimits(len(images), len(videos), len(audios), len(roles)); err != nil {
		return nil, err
	}
	defaultRole := miniMaxVideoDefaultImageRole(modelParamString(input.Options, volcengineVideoImageRoleOption), len(roles), len(images), len(videos)+len(audios) > 0)
	content, counts, err := miniMaxVideoImageInputs(images, roles, defaultRole)
	if err != nil {
		return nil, err
	}
	if err = validateMiniMaxVideoInputRoles(counts, len(videos)+len(audios) > 0); err != nil {
		return nil, err
	}
	content = appendMiniMaxVideoURLs(content, "video_url", "reference_video", videos)
	content = appendMiniMaxVideoURLs(content, miniMaxAudioURLInput, "reference_audio", audios)
	if len(content) == 0 && modelParamString(input.Options, miniMaxVideoRatioOption) == valueAdaptive8B08E1F4 {
		return nil, fmt.Errorf("%w: text-only MiniMax video requires a concrete ratio", errCategoryEF97804E6D)
	}
	return content, nil
}

func miniMaxVideoImageURLs(input GenerateInput) []string {
	images := make([]string, 0)
	for _, image := range collectImageInputParts(input.Messages) {
		images = append(images, miniMaxImageDataURL(image))
	}
	return append(images, modelParamStringList(input.Options, miniMaxImageURLsInput)...)
}

func validateMiniMaxVideoReferenceLimits(images, videos, audios, roles int) error {
	if images > 9 || videos > 3 || audios > 3 {
		return fmt.Errorf("%w: MiniMax video reference limit exceeded", errCategoryEF97804E6D)
	}
	if roles > images {
		return fmt.Errorf("%w: MiniMax image_roles exceeds image count", errCategoryEF97804E6D)
	}
	return nil
}

func miniMaxVideoDefaultImageRole(configured string, roles, images int, hasMediaReferences bool) string {
	// More than two images or audio/video references imply reference generation.
	// Explicit frame roles are kept and checked for conflicting references.
	if configured == "" && roles == 0 && (images > 2 || hasMediaReferences) {
		return miniMaxRoleReferenceImage
	}
	return configured
}

func miniMaxVideoImageInputs(images, roles []string, defaultRole string) ([]map[string]interface{}, map[string]int, error) {
	content := make([]map[string]interface{}, 0, len(images))
	counts := map[string]int{}
	for index, imageURL := range images {
		role := miniMaxVideoImageRole(index, roles, defaultRole)
		if !slices.Contains([]string{miniMaxRoleFirstFrame, miniMaxRoleLastFrame, miniMaxRoleReferenceImage}, role) {
			return nil, nil, fmt.Errorf("%w: invalid MiniMax image role %q", errCategoryEF97804E6D, role)
		}
		counts[role]++
		content = appendMiniMaxVideoURLs(content, valueImageUrlD285EA63, role, []string{imageURL})
	}
	return content, counts, nil
}

func miniMaxVideoImageRole(index int, roles []string, defaultRole string) string {
	role := defaultRole
	if index < len(roles) {
		role = roles[index]
	}
	if role != "" {
		return role
	}
	if index == 1 {
		return miniMaxRoleLastFrame
	}
	return miniMaxRoleFirstFrame
}

func validateMiniMaxVideoInputRoles(counts map[string]int, hasMediaReferences bool) error {
	frames := counts[miniMaxRoleFirstFrame] > 0 || counts[miniMaxRoleLastFrame] > 0
	references := counts[miniMaxRoleReferenceImage] > 0 || hasMediaReferences
	if (frames && references) || counts[miniMaxRoleFirstFrame] > 1 || counts[miniMaxRoleLastFrame] > 1 {
		return fmt.Errorf("%w: MiniMax frame inputs and multimodal reference inputs cannot be mixed", errCategoryEF97804E6D)
	}
	return nil
}

func appendMiniMaxVideoURLs(content []map[string]interface{}, kind, role string, urls []string) []map[string]interface{} {
	for _, mediaURL := range urls {
		content = append(content, map[string]interface{}{valueTypeF2B37301: kind, kind: map[string]interface{}{valueUrl5D7AD561: mediaURL}, valueRoleE6F72470: role})
	}
	return content
}

func (c *Client) pollMiniMaxVideo(ctx context.Context, route RouteConfig, taskID string, options map[string]interface{}) (*GenerateOutput, error) {
	path := "/v2/query/video_generation/" + url.PathEscape(taskID)
	for {
		parsed, body, debug, err := c.doMiniMaxRequest(ctx, route, http.MethodGet, path, nil)
		if err != nil {
			return nil, miniMaxVideoPollFailure(ctx, taskID, err, debug)
		}
		output, done, err := resolveMiniMaxVideoTask(asMap(parsed["task"]), taskID, body, debug)
		if err != nil || done {
			return output, err
		}
		if err = waitMediaPoll(ctx, resolveMediaPollInterval(options)); err != nil {
			return nil, miniMaxVideoPollFailure(ctx, taskID, err, debug)
		}
	}
}

func resolveMiniMaxVideoTask(task map[string]interface{}, taskID string, body []byte, debug *UpstreamDebugSnapshot) (*GenerateOutput, bool, error) {
	if getString(task["id"]) != taskID {
		return nil, true, miniMaxVideoPollFailure(context.Background(), taskID, fmt.Errorf("%w: query returned a different or missing task id", errMiniMaxInvalidResponse), debug)
	}
	switch strings.ToLower(strings.TrimSpace(getString(task[interactionKeyStatus]))) {
	case miniMaxStatusQueued, miniMaxStatusRunning:
		return nil, false, nil
	case miniMaxStatusSucceeded:
		output, err := miniMaxVideoOutput(task, taskID, body, debug)
		return output, true, err
	case miniMaxStatusFailed, miniMaxStatusCancelled:
		detail := asMap(task[valueErrorAE1827EC])
		return nil, true, &UpstreamError{StatusCode: http.StatusBadRequest, Message: fmt.Sprintf("MiniMax video task_id=%s %s: %s %s", taskID, getString(task[interactionKeyStatus]), getString(detail["code"]), getString(detail[valueMessageF06D0A65])), Body: string(body), Debug: debug}
	default:
		return nil, true, miniMaxVideoPollFailure(context.Background(), taskID, fmt.Errorf("%w: unknown task status %q", errMiniMaxInvalidResponse, getString(task[interactionKeyStatus])), debug)
	}
}

func miniMaxVideoPollFailure(ctx context.Context, taskID string, err error, debug *UpstreamDebugSnapshot) error {
	if ctx.Err() != nil {
		return fmt.Errorf("MiniMax video polling stopped; task_id=%s (task may still be running): %w", taskID, ctx.Err())
	}
	// Once created, a failed query must never cause the gateway to create another
	// video. Preserve the task ID and diagnostic response for manual recovery.
	return &UpstreamError{StatusCode: http.StatusConflict, Message: fmt.Sprintf("MiniMax video outcome unknown; task_id=%s: %s", taskID, err), Debug: debug}
}

func miniMaxVideoOutput(task map[string]interface{}, taskID string, body []byte, debug *UpstreamDebugSnapshot) (*GenerateOutput, error) {
	mediaURL := strings.TrimSpace(getStringFromPath(task, valueContent3EAD52FD, valueUrl5D7AD561))
	if mediaURL == "" {
		return nil, miniMaxVideoPollFailure(context.Background(), taskID, fmt.Errorf("%w: succeeded without a video URL", errMiniMaxInvalidResponse), debug)
	}
	metadata, err := miniMaxAssetMetadata(task[volcengineVideoDurationOption])
	if err != nil {
		return nil, miniMaxVideoPollFailure(context.Background(), taskID, err, debug)
	}
	usage := asMap(task[interactionKeyUsage])
	rawUsage, err := json.Marshal(usage)
	if err != nil {
		return nil, miniMaxVideoPollFailure(context.Background(), taskID, err, debug)
	}
	return &GenerateOutput{ResponseID: taskID, Text: mediaURL,
		GeneratedAssets: []GeneratedAsset{{Kind: GeneratedAssetKindVideo, URL: mediaURL, MIMEType: volcengineVideoMIMEType, ExternalID: taskID, MetadataJSON: metadata}},
		Usage:           Usage{InputTokens: toInt64(usage["prompt_tokens"]), OutputTokens: toInt64(usage["completion_tokens"]), RawUsageJSON: string(rawUsage)}, RawJSON: string(body), Debug: debug,
	}, nil
}
