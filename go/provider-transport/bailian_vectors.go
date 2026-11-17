package transport

import (
	"fmt"
	"strings"
)

func buildBailianVectorRequest(model, protocol string, input GenerateInput) (map[string]interface{}, string, error) {
	if protocol == AdapterBailianRerank {
		return buildBailianRerankRequest(model, input)
	}
	if strings.Contains(model, "-vl-embedding") || strings.HasPrefix(model, "tongyi-embedding-vision-") {
		return buildBailianMultimodalEmbedding(model, input)
	}
	texts, err := bailianStringList(input.Options, "texts", bailianEmbeddingBatchLimit(model))
	if err != nil {
		return nil, "", err
	}
	if len(texts) == 0 {
		texts = []string{buildOpenAIImageGenerationPrompt(input.Messages)}
	}
	if strings.TrimSpace(texts[0]) == "" {
		return nil, "", fmt.Errorf("%w: embedding texts are required", errBailianInput)
	}
	parameters := bailianOptionSubset(input.Options, "text_type", "dimension", "output_type", "instruct")
	if err = validateBailianEnum(parameters, "text_type", bailianQuery, "document"); err != nil {
		return nil, "", err
	}
	if err = validateBailianEnum(parameters, "output_type", "dense", "sparse", "dense&sparse"); err != nil {
		return nil, "", err
	}
	if err = validateBailianEmbeddingDimension(model, parameters); err != nil {
		return nil, "", err
	}
	return bailianNativePayload(model, map[string]interface{}{"texts": texts}, parameters), bailianEmbeddingPath, nil
}

func bailianEmbeddingBatchLimit(model string) int {
	switch {
	case strings.HasPrefix(model, bailianModelTextEmbedding37):
		return 20
	case model == "text-embedding-v1", model == "text-embedding-v2":
		return 25
	default:
		return 10
	}
}

func validateBailianEmbeddingDimension(model string, parameters map[string]interface{}) error {
	if _, exists := parameters["dimension"]; !exists {
		return nil
	}
	dimensions, exists := bailianEmbeddingDimensions[model]
	if !exists {
		dimensions = []float64{1024, 768, 512, 256, 128, 64}
		if strings.HasPrefix(model, "tongyi-embedding-vision-") {
			dimensions = nil
		}
	}
	for _, dimension := range dimensions {
		if bailianIntegerInRange(parameters, "dimension", dimension, dimension) {
			return nil
		}
	}
	return fmt.Errorf("%w: unsupported dimension for %s", errBailianInput, model)
}

var bailianEmbeddingDimensions = map[string][]float64{
	bailianModelTextEmbedding37: {2560, 2048, 1536, 1024, 768, 512, 256}, bailianModelVLEmbedding3: {2560, 2048, 1536, 1024, 768, 512, 256},
	"qwen3.7-text-embedding-flash": {1024, 768, 512, 256}, "text-embedding-v4": {2048, 1536, 1024, 768, 512, 256, 128, 64},
	"qwen2.5-vl-embedding": {2048, 1024, 768, 512}, "tongyi-embedding-vision-plus-2026-03-06": {64, 128, 256, 512, 1024, 1152},
	"tongyi-embedding-vision-flash-2026-03-06": {64, 128, 256, 512, 768}, "text-embedding-v1": nil, "text-embedding-v2": nil,
}

func buildBailianMultimodalEmbedding(model string, input GenerateInput) (map[string]interface{}, string, error) {
	contents, err := bailianMultimodalContents(input)
	if err != nil {
		return nil, "", err
	}
	entries, valid := contents.([]interface{})
	if !valid || len(entries) == 0 || len(entries) > 20 {
		return nil, "", fmt.Errorf("%w: contents must have 1-20 items", errBailianInput)
	}
	for _, item := range entries {
		if err := validateBailianVectorContent(item, true); err != nil {
			return nil, "", err
		}
	}
	parameters := bailianOptionSubset(input.Options, "dimension", "output_type", "instruct", "enable_fusion", "res_level", "max_video_frames")
	if err := validateBailianMultimodalParameters(model, parameters); err != nil {
		return nil, "", err
	}
	return bailianNativePayload(model, map[string]interface{}{"contents": entries}, parameters), bailianMultimodalEmbeddingPath, nil
}

func validateBailianVectorContent(raw interface{}, multimodal bool) error {
	if text, valid := raw.(string); valid && strings.TrimSpace(text) != "" {
		return nil
	}
	if !multimodal {
		return fmt.Errorf("%w: vector input must be non-empty text", errBailianInput)
	}
	content := asMap(raw)
	if len(content) == 0 {
		return fmt.Errorf("%w: empty vector content", errBailianInput)
	}
	for key, value := range content {
		if err := validateBailianVectorField(key, value); err != nil {
			return err
		}
	}
	return nil
}
func validateBailianVectorField(key string, value interface{}) error {
	switch key {
	case valueText1D495308:
		text, valid := value.(string)
		if !valid || strings.TrimSpace(text) == "" {
			return errBailianInput
		}
	case ContentPartImage, GeneratedAssetKindVideo:
		if !bailianMediaURI(getString(value), key == ContentPartImage) {
			return errBailianInput
		}
	case "multi_images":
		return validateBailianVectorImages(value)
	default:
		return fmt.Errorf("%w: unsupported vector content %s", errBailianInput, key)
	}
	return nil
}
func validateBailianVectorImages(value interface{}) error {
	images, err := bailianStringList(map[string]interface{}{"images": value}, "images", 64)
	if err != nil || len(images) == 0 {
		return errBailianInput
	}
	for _, image := range images {
		if !bailianMediaURI(image, true) {
			return errBailianInput
		}
	}
	return nil
}

func buildBailianRerankRequest(model string, input GenerateInput) (map[string]interface{}, string, error) {
	query, exists := input.Options[bailianQuery]
	if !exists {
		query = buildOpenAIImageGenerationPrompt(input.Messages)
	}
	multimodal := model == "qwen3-vl-rerank"
	if err := validateBailianVectorContent(query, multimodal); err != nil {
		return nil, "", err
	}
	documents, valid := bailianInterfaceList(input.Options[bailianDocuments])
	if !valid || len(documents) == 0 {
		return nil, "", fmt.Errorf("%w: rerank documents are required", errBailianInput)
	}
	for _, document := range documents {
		if err := validateBailianVectorContent(document, multimodal); err != nil {
			return nil, "", err
		}
	}
	parameters := bailianOptionSubset(input.Options, "top_n", "return_documents", "instruct", "fps")
	if err := validateBailianInteger(parameters, "top_n", 1, float64(len(documents))); err != nil {
		return nil, "", err
	}
	if model == bailianModelQwenRerank3 {
		payload := map[string]interface{}{valueModel2D31E8EB: model, bailianQuery: query, bailianDocuments: documents}
		for key, value := range parameters {
			payload[key] = value
		}
		return payload, "/compatible-api/v1/reranks", nil
	}
	return bailianNativePayload(model, map[string]interface{}{bailianQuery: query, bailianDocuments: documents}, parameters), bailianRerankPath, nil
}

func bailianMultimodalContents(input GenerateInput) (interface{}, error) {
	contents, exists := input.Options["contents"]
	if !exists {
		items := []interface{}{}
		if text := buildOpenAIImageGenerationPrompt(input.Messages); text != "" {
			items = append(items, map[string]interface{}{valueText1D495308: text})
		}
		images, err := bailianImageInputURLs(input)
		if err != nil {
			return nil, err
		}
		for _, image := range images {
			items = append(items, map[string]interface{}{ContentPartImage: image})
		}
		contents = items
	}

	return contents, nil
}

func validateBailianMultimodalParameters(model string, parameters map[string]interface{}) error {
	if err := validateBailianEnum(parameters, "output_type", "dense"); err != nil {
		return err
	}
	if err := validateBailianEmbeddingDimension(model, parameters); err != nil {
		return err
	}
	if _, specified := parameters["enable_fusion"]; specified && model != bailianModelVLEmbedding3 {
		return fmt.Errorf("%w: enable_fusion requires qwen3-vl-embedding", errBailianInput)
	}
	if err := validateBailianInteger(parameters, "res_level", 0, 3); err != nil {
		return err
	}
	if err := validateBailianInteger(parameters, "max_video_frames", 1, 64); err != nil {
		return err
	}

	return nil
}

func bailianInterfaceList(raw interface{}) ([]interface{}, bool) {
	documents, valid := raw.([]interface{})
	if stringsArray, ok := raw.([]string); ok {
		documents = make([]interface{}, len(stringsArray))
		for i, value := range stringsArray {
			documents[i] = value
		}
		valid = true
	}

	return documents, valid
}
