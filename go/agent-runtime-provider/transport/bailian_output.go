package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

func (c *Client) parseBailianOutput(ctx context.Context, route RouteConfig, input GenerateInput, parsed map[string]interface{}) (*GenerateOutput, error) {
	if route.Protocol == AdapterBailianTranscription {
		return c.parseBailianTranscription(ctx, route, parsed)
	}
	native := asMap(parsed["output"])
	id := bailianResponseID(parsed, native)
	result := &GenerateOutput{ResponseID: id, Usage: bailianUsage(parsed)}
	var err error
	switch route.Protocol {
	case AdapterBailianImage:
		err = parseBailianImages(native, result)
	case AdapterBailianVideo:
		err = appendBailianAsset(result, native, "video_url", GeneratedAssetKindVideo, "video/mp4")
	case AdapterBailian3D:
		err = parseBailian3D(native, result)
	case AdapterBailianSpeech, AdapterBailianAudio, AdapterBailianMusic:
		err = parseBailianAudio(native, input.Options, route.Protocol, result)
	case AdapterBailianEmbedding:
		err = validateBailianEmbeddingOutput(native, input, route.UpstreamModel)
	case AdapterBailianRerank:
		err = validateBailianRerankOutput(parsed, input.Options)
	default:
		err = ErrUnsupportedAdapter
	}
	return result, err
}

func parseBailianImages(native map[string]interface{}, result *GenerateOutput) error {
	for _, rawChoice := range asSlice(native["choices"]) {
		message := asMap(asMap(rawChoice)["message"])
		for _, content := range asSlice(message[valueContent03EE2C26]) {
			if image := getString(asMap(content)[ContentPartImage]); image != "" {
				result.GeneratedImages = append(result.GeneratedImages, GeneratedImage{URL: image, MIMEType: valueImagePng415383B9})
			}
		}
	}
	for _, raw := range asSlice(native["results"]) {
		image := asMap(raw)
		if imageURL := getString(image[valueUrl5D7AD561]); imageURL != "" {
			result.GeneratedImages = append(result.GeneratedImages, GeneratedImage{URL: imageURL, MIMEType: valueImagePng415383B9, RevisedPrompt: getString(image["actual_prompt"])})
		}
	}
	if len(result.GeneratedImages) == 0 {
		return fmt.Errorf("%w: no generated images", errBailianResponse)
	}
	for _, image := range result.GeneratedImages {
		if !bailianMediaURI(image.URL, false) {
			return fmt.Errorf("%w: invalid image URL", errBailianResponse)
		}
	}
	return nil
}

func appendBailianAsset(result *GenerateOutput, native map[string]interface{}, key, kind, mimeType string) error {
	uri := getString(native[key])
	if !bailianMediaURI(uri, false) {
		return fmt.Errorf("%w: missing or invalid %s", errBailianResponse, key)
	}
	metadata, err := json.Marshal(native)
	if err != nil {
		return err
	}
	result.GeneratedAssets = append(result.GeneratedAssets, GeneratedAsset{Kind: kind, URL: uri, MIMEType: mimeType, ExternalID: result.ResponseID, MetadataJSON: string(metadata)})
	return nil
}

func parseBailian3D(native map[string]interface{}, result *GenerateOutput) error {
	seen := map[string]bool{}
	for _, raw := range asSlice(native["results"]) {
		if err := parseBailian3DRecord(asMap(raw), result, seen); err != nil {
			return err
		}
	}
	for _, asset := range result.GeneratedAssets {
		if asset.Kind == GeneratedAssetKind3D {
			return nil
		}
	}
	return fmt.Errorf("%w: no generated 3D model", errBailianResponse)
}

func parseBailianAudio(native, options map[string]interface{}, protocol string, result *GenerateOutput) error {
	if reason := getString(native["finish_reason"]); reason != "" && reason != "stop" {
		return fmt.Errorf("%w: audio generation unfinished: %s", errBailianResponse, reason)
	}
	audio := asMap(native[GeneratedAssetKindAudio])
	format := modelParamString(options, miniMaxFormatKey)
	if format == "" {
		format = bailianFormatMP3
		if protocol == AdapterBailianAudio {
			format = bailianFormatWAV
		}
	}
	mimeTypes := map[string]string{bailianFormatMP3: "audio/mpeg", bailianFormatWAV: "audio/wav", bailianFormatPCM: "audio/pcm", bailianFormatOpus: "audio/ogg"}
	if id := getString(audio["id"]); id != "" {
		result.ResponseID = id
	}
	metadata := map[string]interface{}{}
	for key, value := range native {
		metadata[key] = value
	}
	metadata[valueUrl5D7AD561] = audio[valueUrl5D7AD561]
	return appendBailianAsset(result, metadata, valueUrl5D7AD561, GeneratedAssetKindAudio, mimeTypes[format])
}

func validateBailianEmbeddings(native map[string]interface{}) error {
	embeddings := asSlice(native["embeddings"])
	if len(embeddings) == 0 {
		return fmt.Errorf("%w: no embeddings", errBailianResponse)
	}
	for _, raw := range embeddings {
		if err := validateBailianEmbeddingValues(asMap(raw)); err != nil {
			return err
		}
	}
	return nil
}

func bailianFiniteNumber(value interface{}) bool {
	number, valid := modelParamFloat(map[string]interface{}{"value": value}, "value")
	return valid && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func validateBailianRerankOutput(parsed, options map[string]interface{}) error {
	native := asMap(parsed["output"])
	if _, exists := parsed["output"]; !exists {
		native = parsed
	}
	results := asSlice(native["results"])
	if len(results) == 0 {
		return fmt.Errorf("%w: no rerank results", errBailianResponse)
	}
	documentCount := len(asSlice(options[bailianDocuments]))
	if docs, valid := options[bailianDocuments].([]string); valid {
		documentCount = len(docs)
	}
	seen := map[int64]bool{}
	for _, raw := range results {
		result := asMap(raw)
		index := toInt64(result["index"])
		if !bailianIntegerInRange(result, "index", 0, float64(documentCount-1)) || seen[index] || !bailianFiniteNumber(result["relevance_score"]) {
			return fmt.Errorf("%w: invalid rerank result", errBailianResponse)
		}
		seen[index] = true
	}
	return nil
}

func bailianOutputTaskID(parsed map[string]interface{}) string {
	return strings.TrimSpace(getString(asMap(parsed["output"])[bailianTaskID]))
}

func bailianResponseID(parsed, native map[string]interface{}) string {
	id := getString(native[bailianTaskID])
	if id == "" {
		id = getString(parsed["request_id"])
	}
	if id == "" {
		id = getString(parsed["id"])
	}

	return id
}

func parseBailian3DRecord(asset map[string]interface{}, result *GenerateOutput, seen map[string]bool) error {
	for _, key := range []string{"pbr_model_url", "base_model_url"} {
		uri := getString(asset[key])
		if uri != "" && !seen[uri] {
			if err := appendBailianAsset(result, asset, key, GeneratedAssetKind3D, "model/gltf-binary"); err != nil {
				return err
			}
			seen[uri] = true
		}
	}
	if getString(asset["rendered_image_url"]) != "" {
		if err := appendBailianAsset(result, asset, "rendered_image_url", GeneratedAssetKindImage, valueImagePng415383B9); err != nil {
			return err
		}
	}
	return nil
}

func validateBailianEmbeddingValues(embedding map[string]interface{}) error {
	dense := asSlice(embedding["embedding"])
	sparse := asSlice(embedding["sparse_embedding"])
	if len(dense) == 0 && len(sparse) == 0 {
		return fmt.Errorf("%w: empty embedding", errBailianResponse)
	}
	for _, value := range dense {
		if !bailianFiniteNumber(value) {
			return fmt.Errorf("%w: invalid embedding value", errBailianResponse)
		}
	}
	for _, rawSparse := range sparse {
		value := asMap(rawSparse)
		if !bailianIntegerInRange(value, "index", 0, math.MaxInt32) || !bailianFiniteNumber(value["value"]) {
			return fmt.Errorf("%w: invalid sparse embedding", errBailianResponse)
		}
	}
	return nil
}

func validateBailianTextEmbeddingIndexes(native map[string]interface{}, input GenerateInput) error {
	texts, err := bailianStringList(input.Options, "texts", 0)
	if err != nil {
		return err
	}
	count := len(texts)
	if count == 0 {
		count = 1
	}
	embeddings := asSlice(native["embeddings"])
	if len(embeddings) != count {
		return fmt.Errorf("%w: incomplete text embeddings", errBailianResponse)
	}
	seen := map[int64]bool{}
	for _, raw := range embeddings {
		embedding := asMap(raw)
		index := toInt64(embedding["text_index"])
		if !bailianIntegerInRange(embedding, "text_index", 0, float64(count-1)) || seen[index] {
			return fmt.Errorf("%w: invalid text embedding index", errBailianResponse)
		}
		seen[index] = true
	}
	return nil
}

func validateBailianEmbeddingOutput(native map[string]interface{}, input GenerateInput, model string) error {
	if err := validateBailianEmbeddings(native); err != nil {
		return err
	}
	if strings.Contains(model, "-vl-embedding") || strings.HasPrefix(model, "tongyi-embedding-vision-") {
		return nil
	}
	return validateBailianTextEmbeddingIndexes(native, input)
}
