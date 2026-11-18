package transport

import (
	"fmt"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

func buildBailian3DRequest(model string, input GenerateInput) (map[string]interface{}, error) {
	if len(collectImageInputParts(input.Messages)) > 0 {
		return nil, fmt.Errorf("%w: Tripo requires public image URLs", errBailianInput)
	}
	payloadInput, err := bailian3DInput(input)
	if err != nil {
		return nil, err
	}
	if err = validateBailian3DInput(payloadInput); err != nil {
		return nil, err
	}
	parameters := bailianOptionSubset(input.Options, "texture_quality", "geometry_quality", "texture", "pbr")
	if err = validateBailian3DParameters(model, parameters); err != nil {
		return nil, err
	}
	return bailianNativePayload(model, payloadInput, parameters), nil
}
func bailian3DInput(input GenerateInput) (map[string]interface{}, error) {
	images, err := bailianStringList(input.Options, "image_urls", 4)
	if err != nil {
		return nil, err
	}
	payload := bailianOptionSubset(input.Options, ContentPartImage, "images")
	if len(images) > 0 && len(payload) > 0 {
		return nil, fmt.Errorf("%w: Tripo image supplied twice", errBailianInput)
	}
	if len(images) == 1 {
		payload[ContentPartImage] = images[0]
	}
	if len(images) > 1 {
		views, viewErr := bailian3DViewSlots(images)
		if viewErr != nil {
			return nil, viewErr
		}
		payload["images"] = views
	}
	if prompt := strings.TrimSpace(buildOpenAIImageGenerationPrompt(input.Messages)); prompt != "" {
		payload["prompt"] = prompt
	}
	return payload, nil
}
func bailian3DViewSlots(images []string) ([]interface{}, error) {
	if len(images) != 4 {
		return nil, fmt.Errorf("%w: Tripo requires four view slots; use images with empty objects for omitted views", errBailianInput)
	}
	views := make([]interface{}, 0, len(images))
	for _, imageURL := range images {
		parsed, err := url.Parse(imageURL)
		if err != nil {
			return nil, err
		}
		kind := strings.TrimPrefix(strings.ToLower(path.Ext(parsed.Path)), ".")
		if kind == bailianFormatJPG {
			kind = bailianFormatJPEG
		}
		views = append(views, map[string]interface{}{valueType5AC5AA71: kind, "file_token": imageURL})
	}
	return views, nil
}
func validateBailian3DParameters(model string, parameters map[string]interface{}) error {
	if model != bailianModelTripo && model != "Tripo/Tripo-P1.0" {
		return fmt.Errorf("%w: unsupported Tripo model", errBailianInput)
	}
	if err := validateBailianEnum(parameters, "texture_quality", "standard", "detailed"); err != nil {
		return err
	}
	if err := validateBailianEnum(parameters, "geometry_quality", "standard", "ultra"); err != nil {
		return err
	}
	if _, exists := parameters["geometry_quality"]; exists && model != bailianModelTripo {
		return fmt.Errorf("%w: geometry_quality requires Tripo-H3.1", errBailianInput)
	}
	if texture, specified := modelParamBoolValue(parameters, "texture"); specified && !texture && modelParamBool(parameters, "pbr") {
		return fmt.Errorf("%w: texture=false requires pbr=false", errBailianInput)
	}
	return nil
}

func validateBailian3DInput(input map[string]interface{}) error {
	if len(input) != 1 {
		return fmt.Errorf("%w: Tripo prompt, image and images are mutually exclusive", errBailianInput)
	}
	if prompt, exists := input["prompt"]; exists {
		if utf8.RuneCountInString(getString(prompt)) > 1024 {
			return fmt.Errorf("%w: Tripo prompt exceeds 1024 characters", errBailianInput)
		}
		return nil
	}
	if image, exists := input[ContentPartImage]; exists {
		if !bailianMediaURI(getString(image), false) {
			return fmt.Errorf("%w: invalid Tripo image URL", errBailianInput)
		}
		return nil
	}
	return validateBailian3DViews(input["images"])
}
func validateBailian3DViews(raw interface{}) error {
	views, valid := raw.([]interface{})
	if !valid || len(views) != 4 {
		return fmt.Errorf("%w: Tripo requires exactly four view slots", errBailianInput)
	}
	count := 0
	for _, rawView := range views {
		view, ok := rawView.(map[string]interface{})
		if !ok {
			return errBailianInput
		}
		if len(view) == 0 {
			continue
		}
		if err := validateBailian3DView(view); err != nil {
			return err
		}
		count++
	}
	if count < 2 {
		return fmt.Errorf("%w: Tripo multi-view requires 2-4 valid images", errBailianInput)
	}
	return nil
}
func validateBailian3DView(view map[string]interface{}) error {
	if err := validateBailianEnum(view, valueType5AC5AA71, bailianFormatJPEG, "png"); err != nil {
		return err
	}
	if getString(view[valueType5AC5AA71]) == "" || !bailianMediaURI(getString(view["file_token"]), false) {
		return fmt.Errorf("%w: invalid Tripo view URL or format", errBailianInput)
	}
	return nil
}
