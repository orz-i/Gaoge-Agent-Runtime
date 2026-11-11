package transport

import (
	"encoding/base64"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
)

func buildBailianRequest(route RouteConfig, input GenerateInput) (bailianRequest, error) {
	var payload map[string]interface{}
	var path string
	var async bool
	var err error
	switch route.Protocol {
	case AdapterBailianImage:
		payload, path, async, err = buildBailianImageRequest(route.UpstreamModel, input)
	case AdapterBailianVideo:
		payload, err = buildBailianVideoRequest(route.UpstreamModel, input)
		path, async = bailianVideoPath, true
	case AdapterBailian3D:
		payload, err = buildBailian3DRequest(route.UpstreamModel, input)
		path, async = bailian3DPath, true
	case AdapterBailianSpeech, AdapterBailianAudio, AdapterBailianMusic:
		payload, path, err = buildBailianAudioRequest(route.UpstreamModel, route.Protocol, input)
	case AdapterBailianTranscription:
		return buildBailianTranscriptionRequest(route.UpstreamModel, input)
	case AdapterBailianEmbedding, AdapterBailianRerank:
		payload, path, err = buildBailianVectorRequest(route.UpstreamModel, route.Protocol, input)
	default:
		err = ErrUnsupportedAdapter
	}
	if !strings.HasPrefix(path, "/compatible-api/") {
		path = bailianNativeBase + path
	}
	return bailianRequest{path: path, payload: payload, async: async}, err
}

func bailianNativePayload(model string, input interface{}, parameters map[string]interface{}) map[string]interface{} {
	payload := map[string]interface{}{valueModel2D31E8EB: strings.TrimSpace(model), "input": input}
	if parameters != nil {
		payload[bailianParameters] = parameters
	}
	return payload
}

func bailianIntegerInRange(options map[string]interface{}, key string, minimum, maximum float64) bool {
	value, valid := modelParamFloat(options, key)
	return valid && !math.IsNaN(value) && !math.IsInf(value, 0) && math.Trunc(value) == value && value >= minimum && value <= maximum
}

func validateBailianInteger(options map[string]interface{}, key string, minimum, maximum float64) error {
	if _, exists := options[key]; exists && !bailianIntegerInRange(options, key, minimum, maximum) {
		return fmt.Errorf("%w: %s must be an integer in [%g, %g]", errBailianInput, key, minimum, maximum)
	}
	return nil
}

func bailianStringList(options map[string]interface{}, key string, maximum int) ([]string, error) {
	value, exists := options[key]
	if !exists {
		return nil, nil
	}
	result, err := bailianDecodeStringList(value, key)
	if err != nil {
		return nil, err
	}
	if maximum > 0 && len(result) > maximum {
		return nil, fmt.Errorf("%w: too many %s", errBailianInput, key)
	}
	for index, text := range result {
		result[index] = strings.TrimSpace(text)
		if result[index] == "" {
			return nil, fmt.Errorf("%w: %s contains empty text", errBailianInput, key)
		}
	}
	return result, nil
}

func bailianMediaURI(value string, allowData bool) bool {
	if allowData && strings.HasPrefix(value, "data:") {
		parts := strings.SplitN(value, ";base64,", 2)
		if len(parts) != 2 {
			return false
		}
		decoded, err := base64.StdEncoding.DecodeString(parts[1])
		return err == nil && len(decoded) > 0
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Hostname() != "" && parsed.User == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func bailianImageInputURLs(input GenerateInput) ([]string, error) {
	images, err := bailianStringList(input.Options, "image_urls", 20)
	if err != nil {
		return nil, err
	}
	for _, part := range collectImageInputParts(input.Messages) {
		mimeType := strings.TrimSpace(part.MimeType)
		if mimeType == "" {
			mimeType = http.DetectContentType(part.Data)
		}
		if !strings.HasPrefix(mimeType, "image/") {
			return nil, fmt.Errorf("%w: non-image input", errBailianInput)
		}
		images = append(images, "data:"+mimeType+";base64,"+base64.StdEncoding.EncodeToString(part.Data))
	}
	for _, imageURL := range images {
		if !bailianMediaURI(imageURL, true) {
			return nil, fmt.Errorf("%w: invalid image URI", errBailianInput)
		}
	}
	return images, nil
}

func validateBailianEnum(options map[string]interface{}, key string, allowed ...string) error {
	value, exists := options[key]
	if !exists {
		return nil
	}
	text, valid := value.(string)
	if valid {
		for _, candidate := range allowed {
			if text == candidate {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: unsupported %s", errBailianInput, key)
}

func bailianDecodeStringList(value interface{}, key string) ([]string, error) {
	result := []string{}
	switch items := value.(type) {
	case []string:
		result = append(result, items...)
	case []interface{}:
		for _, item := range items {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%w: %s must be a string array", errBailianInput, key)
			}
			result = append(result, text)
		}
	default:
		return nil, fmt.Errorf("%w: %s must be a string array", errBailianInput, key)
	}

	return result, nil
}
