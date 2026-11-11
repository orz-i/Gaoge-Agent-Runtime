package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	valueAspectRatioF313365E    = "aspect_ratio"
	valuePrompt2E719579         = "prompt"
	valueQualityAF737672        = "quality"
	valueResponseFormat751C03C5 = "response_format"
	valueSystem67AFE4D8         = "system"
	valueUser2AAD1442           = "user"
)

const (
	valueText1E61CF4C                      = "16:9"
	valueText654615FB                      = "1:1"
	valueACleanProductRender22B10AE8       = "A clean product render"
	valueRenderThisAsAPencilSketch692D4043 = "Render this as a pencil sketch"
	valueB64Json6CD8A1B0                   = "b64_json"
	valueGrokImagineImageQuality2E3A886F   = "grok-imagine-image-quality"
	valueHigh3DB58C90                      = "high"
	valueIgnore493B2F22                    = "ignore"
	valueImageJpegECE1294D                 = "image/jpeg"
	valueImagePng5786311C                  = "image/png"
	valueImageWebp5D4E4C5C                 = "image/webp"
	valueImageUrl0D8C6F1B                  = "image_url"
	valueOverride4BB36814                  = "override"
	valueResolutionE9EC2B7D                = "resolution"
	valueStream5A427B46                    = "stream"
)

func TestBuildXAIImageRequestBody(t *testing.T) {
	payload, err := buildXAIImageRequestBody(valueGrokImagineImageQuality2E3A886F, GenerateInput{
		Messages: []Message{
			{Role: valueSystem67AFE4D8, Content: valueIgnore493B2F22},
			{Role: valueUser2AAD1442, Content: valueACleanProductRender22B10AE8},
		},
		Options: map[string]interface{}{
			valueAspectRatioF313365E:    valueText1E61CF4C,
			"n":                         2,
			valueResolutionE9EC2B7D:     "2k",
			valueResponseFormat751C03C5: valueB64Json6CD8A1B0,
			valuePrompt2E719579:         valueOverride4BB36814,
			valueStream5A427B46:         true,
			valueQualityAF737672:        valueHigh3DB58C90,
		},
	})
	if err != nil {
		t.Fatalf("build xAI image request body: %v", err)
	}
	if payload["model"] != valueGrokImagineImageQuality2E3A886F || payload[valuePrompt2E719579] != valueACleanProductRender22B10AE8 {
		t.Fatalf("unexpected model or prompt: %#v", payload)
	}
	if payload[valueAspectRatioF313365E] != valueText1E61CF4C || payload["n"] != 2 || payload[valueResolutionE9EC2B7D] != "2k" || payload[valueResponseFormat751C03C5] != valueB64Json6CD8A1B0 {
		t.Fatalf("expected xAI image params, got %#v", payload)
	}
	for _, key := range []string{valueStream5A427B46, valueQualityAF737672} {
		if _, ok := payload[key]; ok {
			t.Fatalf("unexpected xAI image param %q in payload %#v", key, payload)
		}
	}
}

func TestBuildXAIImageRequestBodyDefaultsToBase64(t *testing.T) {
	payload, err := buildXAIImageRequestBody(valueGrokImagineImageQuality2E3A886F, GenerateInput{
		Messages: []Message{{Role: valueUser2AAD1442, Content: valueACleanProductRender22B10AE8}},
	})
	if err != nil {
		t.Fatalf("build xAI image request body: %v", err)
	}
	if payload[valueResponseFormat751C03C5] != valueB64Json6CD8A1B0 {
		t.Fatalf("expected xAI image generation to default to base64, got %#v", payload)
	}
}

func TestBuildXAIImageEditRequestBody(t *testing.T) {
	payload, debugBody, err := buildXAIImageEditRequestBody(valueGrokImagineImageQuality2E3A886F, GenerateInput{
		Messages: []Message{
			{
				Role: valueUser2AAD1442,
				Parts: []ContentPart{
					{Kind: ContentPartText, Text: valueRenderThisAsAPencilSketch692D4043},
					{Kind: ContentPartImage, MimeType: valueImagePng5786311C, Data: []byte("source")},
				},
			},
		},
		Options: map[string]interface{}{
			valueAspectRatioF313365E: valueText654615FB,
			valueResolutionE9EC2B7D:  "2k",
		},
	})
	requireNoError(t, err, "build xAI image edit request body: %v")
	requireCondition(t, payload["model"] == valueGrokImagineImageQuality2E3A886F && payload[valuePrompt2E719579] == valueRenderThisAsAPencilSketch692D4043, "unexpected model or prompt: %#v", payload)
	requireCondition(t, payload[valueAspectRatioF313365E] == valueText654615FB && payload[valueResolutionE9EC2B7D] == "2k", "expected xAI edit params, got %#v", payload)
	requireEqual(t, payload[valueResponseFormat751C03C5], valueB64Json6CD8A1B0, "expected xAI image edit to default to base64, got %#v", payload)
	image, ok := payload["image"].(map[string]interface{})
	requireCondition(t, ok, "expected image payload, got %#v", payload)
	requireEqual(t, image["type"], valueImageUrl0D8C6F1B, "expected image_url type, got %#v", image)
	url, ok := image["url"].(string)
	requireCondition(t, ok && strings.HasPrefix(url, "data:image/png;base64,c291cmNl"), "expected base64 data uri, got %q", url)
	requireCondition(t, !strings.Contains(string(debugBody), "c291cmNl"), "debug body must not include source image bytes: %s", string(debugBody))
}

func TestBuildXAIImageEditRequestBodyAllowsUpToThreeImages(t *testing.T) {
	payload, _, err := buildXAIImageEditRequestBody(valueGrokImagineImageQuality2E3A886F, GenerateInput{
		Messages: []Message{
			{
				Role: valueUser2AAD1442,
				Parts: []ContentPart{
					{Kind: ContentPartText, Text: "Combine these"},
					{Kind: ContentPartImage, MimeType: valueImagePng5786311C, Data: []byte("one")},
					{Kind: ContentPartImage, MimeType: valueImageJpegECE1294D, Data: []byte("two")},
					{Kind: ContentPartImage, MimeType: valueImageWebp5D4E4C5C, Data: []byte("three")},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build xAI multi-image edit request body: %v", err)
	}
	images, ok := payload["image"].([]map[string]interface{})
	if !ok || len(images) != 3 {
		t.Fatalf("expected three ordered image inputs, got %#v", images)
	}
}

func TestGenerateXAIImageUsesImageEndpoint(t *testing.T) {
	var requestPath string
	var requestBody map[string]interface{}
	server := newXAIImageEndpointServer(t, &requestPath, &requestBody, `{
		"id": "img_xai_1",
		"data": [
			{"url": "https://example.com/generated.jpg"}
		],
		"usage": {"input_tokens": 11, "output_tokens": 1}
	}`)
	defer server.Close()

	output, err := newTestProviderClient().Generate(context.Background(), RouteConfig{
		Protocol:      AdapterXAIImage,
		BaseURL:       server.URL + "/v1",
		APIKey:        "xai-key",
		UpstreamModel: valueGrokImagineImageQuality2E3A886F,
	}, GenerateInput{
		Messages: []Message{{Role: valueUser2AAD1442, Content: valueACleanProductRender22B10AE8}},
		Options: map[string]interface{}{
			valueAspectRatioF313365E: valueText1E61CF4C,
		},
	})
	requireNoError(t, err, "generate xAI image: %v")
	assertXAIImageEndpointOutput(t, requestPath, requestBody, output)
}

func newXAIImageEndpointServer(
	t *testing.T,
	requestPath *string,
	requestBody *map[string]interface{},
	response string,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requestPath = r.URL.Path
		requireEqual(t, r.Header.Get("Authorization"), "Bearer xai-key", "unexpected auth header %q", r.Header.Get("Authorization"))
		requireNoError(t, json.NewDecoder(r.Body).Decode(requestBody), "decode request body: %v")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
}

func assertXAIImageEndpointOutput(
	t *testing.T,
	requestPath string,
	requestBody map[string]interface{},
	output *GenerateOutput,
) {
	t.Helper()
	requireEqual(t, requestPath, "/v1/images/generations", "expected xAI images endpoint, got %q", requestPath)
	requireCondition(t, requestBody["model"] == valueGrokImagineImageQuality2E3A886F && requestBody[valueAspectRatioF313365E] == valueText1E61CF4C, "unexpected request body: %#v", requestBody)
	requireCondition(t, output.ResponseID == "img_xai_1" && len(output.GeneratedImages) == 1 && output.GeneratedImages[0].URL == "https://example.com/generated.jpg", "unexpected generated image output: %#v", output)
	requireCondition(t, output.Usage.InputTokens == 11 && output.Usage.OutputTokens == 1, "expected upstream usage, got %#v", output.Usage)
}

func TestGenerateXAIImageGenerationAdapterKeepsGenerationEndpoint(t *testing.T) {
	var requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"url":"https://example.com/generated.jpg"}]}`))
	}))
	defer server.Close()

	_, err := newTestProviderClient().Generate(context.Background(), RouteConfig{
		Protocol:      AdapterXAIImage,
		Endpoint:      EndpointImageEdits,
		BaseURL:       server.URL + "/v1",
		UpstreamModel: valueGrokImagineImageQuality2E3A886F,
	}, GenerateInput{
		Messages: []Message{{Role: valueUser2AAD1442, Content: valueACleanProductRender22B10AE8}},
	})
	if err != nil {
		t.Fatalf("generate xAI image: %v", err)
	}
	if requestPath != "/v1/images/generations" {
		t.Fatalf("expected xAI image generation endpoint, got %q", requestPath)
	}
}

func TestGenerateXAIImageEditUsesImageEditsEndpoint(t *testing.T) {
	var requestPath string
	var requestBody map[string]interface{}
	server := newXAIImageEndpointServer(t, &requestPath, &requestBody, `{
		"id": "img_xai_edit_1",
		"data": [
			{"b64_json": "ZWRpdGVk", "revised_prompt": "Edited prompt"}
		],
		"usage": {"input_tokens": 13, "output_tokens": 2}
	}`)
	defer server.Close()

	output, err := newTestProviderClient().Generate(context.Background(), RouteConfig{
		Protocol:      AdapterXAIImageEdits,
		BaseURL:       server.URL + "/v1",
		APIKey:        "xai-key",
		UpstreamModel: valueGrokImagineImageQuality2E3A886F,
	}, GenerateInput{
		Messages: []Message{{
			Role: valueUser2AAD1442,
			Parts: []ContentPart{
				{Kind: ContentPartText, Text: valueRenderThisAsAPencilSketch692D4043},
				{Kind: ContentPartImage, MimeType: valueImagePng5786311C, Data: []byte("source")},
			},
		}},
		Options: map[string]interface{}{
			valueResponseFormat751C03C5: valueB64Json6CD8A1B0,
		},
	})
	requireNoError(t, err, "generate xAI image edit: %v")
	assertXAIImageEditEndpointOutput(t, requestPath, requestBody, output)
}

func assertXAIImageEditEndpointOutput(
	t *testing.T,
	requestPath string,
	requestBody map[string]interface{},
	output *GenerateOutput,
) {
	t.Helper()
	requireEqual(t, requestPath, "/v1/images/edits", "expected xAI image edits endpoint, got %q", requestPath)
	image := asMap(requestBody["image"])
	requireCondition(t, image["type"] == valueImageUrl0D8C6F1B && strings.HasPrefix(getString(image["url"]), "data:image/png;base64,c291cmNl"), "unexpected edit image payload: %#v", requestBody)
	requireCondition(t, output.ResponseID == "img_xai_edit_1" && len(output.GeneratedImages) == 1 && output.GeneratedImages[0].B64JSON == "ZWRpdGVk", "unexpected edited image output: %#v", output)
	requireCondition(t, output.Usage.InputTokens == 13 && output.Usage.OutputTokens == 2, "expected upstream usage, got %#v", output.Usage)
}

func TestParseXAIImageOutput(t *testing.T) {
	output, err := parseXAIImageOutput([]byte(`{
		"id": "img_xai_1",
		"data": [
			{"url": "https://example.com/a.jpg"},
			{"b64_json": "aGVsbG8=", "revised_prompt": "A revised render"}
		]
	}`), AdapterXAIImage)
	if err != nil {
		t.Fatalf("parse xAI image output: %v", err)
	}
	if output.Text != "" {
		t.Fatalf("xAI image adapter must not write image data into text, got %q", output.Text)
	}
	if len(output.GeneratedImages) != 2 {
		t.Fatalf("expected two generated images, got %#v", output.GeneratedImages)
	}
	if output.GeneratedImages[0].URL != "https://example.com/a.jpg" {
		t.Fatalf("unexpected URL image: %#v", output.GeneratedImages[0])
	}
	if output.GeneratedImages[1].B64JSON != "aGVsbG8=" || output.GeneratedImages[1].MIMEType != valueImageJpegECE1294D {
		t.Fatalf("unexpected base64 image: %#v", output.GeneratedImages[1])
	}
	if len(output.Citations) != 1 || output.Citations[0] != "https://example.com/a.jpg" {
		t.Fatalf("expected URL citation, got %#v", output.Citations)
	}
}
