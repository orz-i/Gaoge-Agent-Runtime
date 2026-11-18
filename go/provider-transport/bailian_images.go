package transport

import (
	"fmt"
	"strconv"
	"strings"
)

func buildBailianImageRequest(model string, input GenerateInput) (map[string]interface{}, string, bool, error) {
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	if strings.TrimSpace(prompt) == "" {
		return nil, "", false, fmt.Errorf("%w: image prompt is required", errBailianInput)
	}
	images, err := bailianImageInputURLs(input)
	if err != nil {
		return nil, "", false, err
	}
	code := strings.ToLower(model)
	if err = validateBailianImageReferences(code, images); err != nil {
		return nil, "", false, err
	}
	content := make([]map[string]interface{}, 0, len(images)+1)
	for _, image := range images {
		content = append(content, map[string]interface{}{ContentPartImage: image})
	}
	content = append(content, map[string]interface{}{valueText1D495308: prompt})
	parameters := bailianOptionSubset(input.Options, valueSize7A4CF6CA, "n", "seed", "negative_prompt", "prompt_extend", "prompt_extend_mode", "enable_thinking", "watermark", "thinking_mode", "enable_sequential", "color_palette", "image_ratio")
	if err = validateBailianImageParameters(code, parameters, len(images)); err != nil {
		return nil, "", false, err
	}
	payload := bailianNativePayload(model, map[string]interface{}{EndpointMessages: []map[string]interface{}{{valueRole01C0A112: valueUserD301E989, valueContent03EE2C26: content}}}, parameters)
	path := bailianMultimodalPath
	async := modelParamBool(input.Options, "async")
	if async {
		if strings.HasPrefix(code, "qwen-image-edit") {
			return nil, "", false, fmt.Errorf("%w: this edit model has no async API", errBailianInput)
		}
		path = bailianImagePath
	}
	return payload, path, async, nil
}

func validateBailianImageParameters(model string, parameters map[string]interface{}, imageCount int) error {
	maximum, err := bailianImageCountLimit(model, parameters, imageCount)
	if err != nil {
		return err
	}
	if err := validateBailianInteger(parameters, "n", 1, maximum); err != nil {
		return err
	}
	if err := validateBailianInteger(parameters, "seed", 0, 2147483647); err != nil {
		return err
	}
	if imageCount > 0 && modelParamString(parameters, "prompt_extend_mode") == "agent" {
		return fmt.Errorf("%w: agent prompt extension supports text-to-image only", errBailianInput)
	}
	return validateBailianImageSize(model, parameters, imageCount)
}

func validateBailianImageSize(model string, parameters map[string]interface{}, imageCount int) error {
	size := modelParamString(parameters, valueSize7A4CF6CA)
	if size == "" {
		return nil
	}
	if model == "qwen-image-edit" {
		return fmt.Errorf("%w: qwen-image-edit does not accept size", errBailianInput)
	}
	if model == "qwen-image-max" || model == "qwen-image-plus" || model == "qwen-image" {
		return validateBailianEnum(parameters, valueSize7A4CF6CA, "1664*928", "1472*1104", "1328*1328", "1104*1472", "928*1664")
	}
	if strings.HasPrefix(model, "wan2.7-image") && (size == "1K" || size == "2K" || size == "4K") {
		return validateBailianWanImageSize(model, parameters, imageCount)
	}
	minimum, maximum := bailianImagePixelLimits(model, parameters, imageCount)
	return validateBailianImageDimensions(size, minimum, maximum)
}
func validateBailianWanImageSize(model string, parameters map[string]interface{}, imageCount int) error {
	if modelParamString(parameters, valueSize7A4CF6CA) == "4K" && !bailianWan4KEnabled(model, parameters, imageCount) {
		return fmt.Errorf("%w: 4K requires wan2.7-image-pro text-to-image", errBailianInput)
	}
	return nil
}
func bailianWan4KEnabled(model string, parameters map[string]interface{}, imageCount int) bool {
	return model == "wan2.7-image-pro" && imageCount == 0 && !modelParamBool(parameters, "enable_sequential")
}
func bailianImagePixelLimits(model string, parameters map[string]interface{}, imageCount int) (int64, int64) {
	minimum, maximum := int64(512*512), int64(2048*2048)
	if strings.HasPrefix(model, "wan2.7-image") {
		minimum = 768 * 768
		if bailianWan4KEnabled(model, parameters, imageCount) {
			maximum = 4096 * 4096
		}
	}
	return minimum, maximum
}
func validateBailianImageDimensions(size string, minimum, maximum int64) error {
	parts := strings.Split(size, "*")
	if len(parts) != 2 {
		return fmt.Errorf("%w: size must be width*height", errBailianInput)
	}
	width, widthErr := strconv.ParseInt(parts[0], 10, 32)
	height, heightErr := strconv.ParseInt(parts[1], 10, 32)
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return fmt.Errorf("%w: invalid image dimensions", errBailianInput)
	}
	if width*height < minimum || width*height > maximum || width > height*8 || height > width*8 {
		return fmt.Errorf("%w: image size exceeds model limits", errBailianInput)
	}
	return nil
}

func validateBailianImageReferences(code string, images []string) error {
	if strings.HasPrefix(code, "qwen-image-edit") && len(images) == 0 {
		return fmt.Errorf("%w: edit model requires images", errBailianInput)
	}
	maximum := 3
	if strings.HasPrefix(code, "wan2.7-image") {
		maximum = 9
	}
	if len(images) > maximum {
		return fmt.Errorf("%w: too many reference images", errBailianInput)
	}

	return nil
}

func bailianImageCountLimit(model string, parameters map[string]interface{}, imageCount int) (float64, error) {
	maximum := 6.0
	if strings.HasPrefix(model, "wan2.7-image") {
		maximum = 4
		if modelParamBool(parameters, "enable_sequential") {
			maximum = 12
		}
	} else if !strings.HasPrefix(model, "qwen-image-2.") && !strings.HasPrefix(model, "qwen-image-3.") && !strings.HasPrefix(model, "qwen-image-edit-") {
		maximum = 1
		if imageCount > 0 && model != "qwen-image-edit" {
			return 0, fmt.Errorf("%w: model supports text-to-image only", errBailianInput)
		}
	}

	return maximum, nil
}
