package transport

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func buildBailianVideoRequest(model string, input GenerateInput) (map[string]interface{}, error) {
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	limit := 20000
	if strings.HasPrefix(model, "wan2.7-") {
		limit = 5000
	}
	if utf8.RuneCountInString(prompt) > limit {
		return nil, fmt.Errorf("%w: video prompt exceeds 20000 characters", errBailianInput)
	}
	media, err := bailianVideoMedia(model, input)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(prompt) == "" && len(media) == 0 {
		return nil, fmt.Errorf("%w: video prompt or media is required", errBailianInput)
	}
	parameters := bailianOptionSubset(input.Options, "resolution", "ratio", "duration", GeneratedAssetKindAudio, "seed", "prompt_extend", "watermark")
	if err = validateBailianVideoParameters(model, parameters, media); err != nil {
		return nil, err
	}
	payloadInput := map[string]interface{}{}
	if prompt != "" {
		payloadInput["prompt"] = prompt
	}
	if err = applyBailianVideoAudio(model, input.Options, payloadInput); err != nil {
		return nil, err
	}
	if len(media) > 0 {
		payloadInput["media"] = media
	}
	return bailianNativePayload(model, payloadInput, parameters), nil
}

func bailianVideoMedia(model string, input GenerateInput) ([]map[string]interface{}, error) {
	media, err := bailianNativeVideoMedia(input.Options)
	if err != nil {
		return nil, err
	}
	images, err := bailianImageInputURLs(input)
	if err != nil {
		return nil, err
	}
	roles, err := bailianStringList(input.Options, "image_roles", 10)
	if err != nil {
		return nil, err
	}
	if len(roles) > 0 && len(roles) != len(images) {
		return nil, fmt.Errorf("%w: image_roles must match image count", errBailianInput)
	}
	media = append(media, bailianVideoImageReferences(images, roles, modelParamString(input.Options, "image_role"))...)
	if err = validateBailianVideoMedia(model, media); err != nil {
		return nil, err
	}
	return media, nil
}

func validateBailianVideoMedia(model string, media []map[string]interface{}) error {
	limits := map[string]int{miniMaxRoleFirstFrame: 1, miniMaxRoleLastFrame: 1, "reference_image": 10, "reference_video": 5, "reference_audio": 5, "file": 1, "link": 1}
	if strings.HasPrefix(model, "wan2.7-") {
		return validateBailianWan27Media(model, media)
	}
	counts, err := bailianVideoReferenceCounts(media, limits)
	if err != nil {
		return err
	}
	frames := counts[miniMaxRoleFirstFrame] + counts[miniMaxRoleLastFrame]
	if (frames > 0 && frames != len(media)) || (counts[miniMaxRoleLastFrame] > 0 && counts[miniMaxRoleFirstFrame] == 0) || (counts["file"] > 0 && counts["link"] > 0) {
		return fmt.Errorf("%w: incompatible video references", errBailianInput)
	}
	return nil
}

func validateBailianVideoParameters(model string, parameters map[string]interface{}, media []map[string]interface{}) error {
	if err := validateBailianEnum(parameters, "resolution", "480P", "720P", "1080P"); err != nil {
		return err
	}
	if err := validateBailianEnum(parameters, "ratio", "adaptive", "21:9", "16:9", "4:3", "1:1", "3:4", "9:16"); err != nil {
		return err
	}
	if err := validateBailianVideoDuration(model, parameters); err != nil {
		return err
	}
	if err := validateBailianInteger(parameters, "seed", -1, 2147483647); err != nil {
		return err
	}
	for _, item := range media {
		if (getString(item[valueType5AC5AA71]) == "file" || getString(item[valueType5AC5AA71]) == "link") && !bailianPromptExtensionEnabled(parameters) {
			return fmt.Errorf("%w: document references require prompt_extend", errBailianInput)
		}
	}
	return nil
}

func bailianPromptExtensionEnabled(parameters map[string]interface{}) bool {
	value, specified := modelParamBoolValue(parameters, "prompt_extend")
	return !specified || value
}

func validateBailianWan27Media(model string, media []map[string]interface{}) error {
	if strings.HasPrefix(model, "wan2.7-t2v") {
		if len(media) > 0 {
			return fmt.Errorf("%w: Wan 2.7 text-to-video does not accept media", errBailianInput)
		}
		return nil
	}
	counts, err := bailianVideoReferenceCounts(media, map[string]int{miniMaxRoleFirstFrame: 1, miniMaxRoleLastFrame: 1, "driving_audio": 1, "first_clip": 1})
	if err != nil {
		return err
	}
	if counts[miniMaxRoleFirstFrame]+counts["first_clip"] != 1 || counts["first_clip"] > 0 && counts["driving_audio"] > 0 {
		return fmt.Errorf("%w: unsupported Wan 2.7 media combination", errBailianInput)
	}
	return nil
}

func bailianNativeVideoMedia(options map[string]interface{}) ([]map[string]interface{}, error) {
	media := make([]map[string]interface{}, 0)
	if raw, specified := options["media"]; specified {
		items, valid := raw.([]interface{})
		if !valid {
			return nil, fmt.Errorf("%w: media must be an array", errBailianInput)
		}
		for _, item := range items {
			value := asMap(item)
			media = append(media, map[string]interface{}{valueType5AC5AA71: getString(value[valueType5AC5AA71]), valueUrl5D7AD561: getString(value[valueUrl5D7AD561])})
		}
	}

	return media, nil
}

func bailianVideoImageReferences(images, roles []string, defaultRole string) []map[string]interface{} {
	media := make([]map[string]interface{}, 0, len(images))
	for index, image := range images {
		role := defaultRole
		if len(roles) > 0 {
			role = roles[index]
		}
		if role == "" {
			role = miniMaxRoleFirstFrame
			if index == 1 {
				role = miniMaxRoleLastFrame
			}
		}
		media = append(media, map[string]interface{}{valueType5AC5AA71: role, valueUrl5D7AD561: image})
	}

	return media
}

func applyBailianVideoAudio(model string, options, payloadInput map[string]interface{}) error {
	if audioURL := modelParamString(options, "audio_url"); audioURL != "" {
		if !strings.HasPrefix(model, "wan2.7-t2v") || !bailianMediaURI(audioURL, false) {
			return fmt.Errorf("%w: audio_url requires Wan 2.7 text-to-video; use media for other models", errBailianInput)
		}
		payloadInput["audio_url"] = audioURL
	}

	return nil
}

func bailianVideoReferenceCounts(media []map[string]interface{}, limits map[string]int) (map[string]int, error) {
	counts := map[string]int{}
	if len(media) > 20 {
		return nil, fmt.Errorf("%w: at most 20 video references", errBailianInput)
	}
	for _, item := range media {
		kind := getString(item[valueType5AC5AA71])
		limit, known := limits[kind]
		if !known {
			return nil, fmt.Errorf("%w: invalid video reference type", errBailianInput)
		}
		counts[kind]++
		if counts[kind] > limit || !bailianMediaURI(getString(item[valueUrl5D7AD561]), kind == miniMaxRoleFirstFrame || kind == miniMaxRoleLastFrame || kind == "reference_image") {
			return nil, fmt.Errorf("%w: invalid video reference", errBailianInput)
		}
	}

	return counts, nil
}

func validateBailianVideoDuration(model string, parameters map[string]interface{}) error {
	maximum := 30.0
	if strings.HasPrefix(model, "wan2.7-") {
		maximum = 15
		if modelParamString(parameters, "resolution") == "480P" {
			return fmt.Errorf("%w: Wan 2.7 requires 720P or 1080P", errBailianInput)
		}
	}
	if _, exists := parameters["duration"]; exists && !(strings.HasPrefix(model, "wan3.0-") && bailianIntegerInRange(parameters, "duration", -1, -1)) {
		if err := validateBailianInteger(parameters, "duration", 2, maximum); err != nil {
			return err
		}
	}

	return nil
}
