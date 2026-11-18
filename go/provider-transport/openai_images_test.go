package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const (
	valuePrompt53E4DDC8         = "prompt"
	valueQualityC898EB46        = "quality"
	valueResponseFormatC64439E9 = "response_format"
	valueSize25EB8B09           = "size"
	valueSystemC6639A43         = "system"
	valueUser95B1918A           = "user"
	valueWebpF0C9FD1D           = "webp"
)

const (
	valueText882A02F0                 = "1024x1024"
	valueACleanProductRender0CB078AE  = "A clean product render"
	valueReplaceTheBackgroundE380DDBC = "Replace the background"
	valueZmluYWw19B3926B              = "ZmluYWw="
	valueB64Json6FEA0091              = "b64_json"
	valueBackgroundA6734840           = "background"
	valueGptImage1433C51B1            = "gpt-image-1"
	valueGptImage2DD41528C            = "gpt-image-2"
	valueHighC9F5A9E4                 = "high"
	valueIgnore05AF7007               = "ignore"
	valueImagePng7655D3D8             = "image/png"
	valueImageWebp8CDFEA70            = "image/webp"
	valueLow50D40DF9                  = "low"
	valueOutputCompression3E726BAB    = "output_compression"
	valueOutputFormatA54B283B         = "output_format"
	valueOverrideE37F6DB0             = "override"
	valuePartialImages8165608A        = "partial_images"
	valueStream94877F65               = "stream"
	valueTrueA041BBAA                 = "true"
	valueUrlF02A82A2                  = "url"
)

func TestResolveOpenAIImageReadTimeoutKeepsImageMinimum(t *testing.T) {
	if got := resolveOpenAIImageReadTimeout(120000); got != 5*time.Minute {
		t.Fatalf("resolveOpenAIImageReadTimeout(120000) = %s, want %s", got, 5*time.Minute)
	}
}

func TestResolveOpenAIImageReadTimeoutPreservesLongerConfiguration(t *testing.T) {
	if got := resolveOpenAIImageReadTimeout(420000); got != 7*time.Minute {
		t.Fatalf("resolveOpenAIImageReadTimeout(420000) = %s, want %s", got, 7*time.Minute)
	}
}

func TestBuildOpenAIImageGenerationRequestBody(t *testing.T) {
	payload, err := buildOpenAIImageGenerationRequestBody(valueGptImage1433C51B1, GenerateInput{
		Messages: []Message{
			{Role: valueSystemC6639A43, Content: valueIgnore05AF7007},
			{Role: valueUser95B1918A, Content: valueACleanProductRender0CB078AE},
		},
		Options: map[string]interface{}{
			valueSize25EB8B09:              valueText882A02F0,
			valueQualityC898EB46:           valueHighC9F5A9E4,
			valueResponseFormatC64439E9:    valueB64Json6FEA0091,
			valueOutputFormatA54B283B:      valueWebpF0C9FD1D,
			valueOutputCompression3E726BAB: 80,
			valuePartialImages8165608A:     2,
			valueStream94877F65:            true,
			valuePrompt53E4DDC8:            valueOverrideE37F6DB0,
		},
	})
	requireNoError(t, err, "build image request body: %v")
	requireCondition(t, payload["model"] == valueGptImage1433C51B1 && payload[valuePrompt53E4DDC8] == valueACleanProductRender0CB078AE, "unexpected model or prompt: %#v", payload)
	requireCondition(t, payload[valueSize25EB8B09] == valueText882A02F0 && payload[valueQualityC898EB46] == valueHighC9F5A9E4, "expected official image params, got %#v", payload)
	requireCondition(t, payload[valueOutputFormatA54B283B] == valueWebpF0C9FD1D && payload[valueOutputCompression3E726BAB] == 80, "expected output params, got %#v", payload)
	if _, ok := payload[valueResponseFormatC64439E9]; ok {
		t.Fatalf("response_format must not be sent for gpt-image models: %#v", payload)
	}
	if _, ok := payload[valueStream94877F65]; ok {
		t.Fatalf("stream must not be passed by non-streaming image adapter: %#v", payload)
	}
	if _, ok := payload[valuePartialImages8165608A]; ok {
		t.Fatalf("partial_images must not be passed without upstream image streaming: %#v", payload)
	}
}

func TestBuildOpenAIImageGenerationStreamRequestBody(t *testing.T) {
	payload, err := buildOpenAIImageGenerationStreamRequestBody(valueGptImage1433C51B1, GenerateInput{
		Messages: []Message{{Role: valueUser95B1918A, Content: valueACleanProductRender0CB078AE}},
		Options: map[string]interface{}{
			valueOutputFormatA54B283B:  valueWebpF0C9FD1D,
			valuePartialImages8165608A: 2,
		},
	})
	if err != nil {
		t.Fatalf("build image stream request body: %v", err)
	}
	if payload[valueStream94877F65] != true || payload[valuePartialImages8165608A] != 2 {
		t.Fatalf("expected stream params, got %#v", payload)
	}
}

func TestBuildOpenAIImageGenerationStreamRequestBodyOmitsDefaultPartialImages(t *testing.T) {
	payload, err := buildOpenAIImageGenerationStreamRequestBody(valueGptImage1433C51B1, GenerateInput{
		Messages: []Message{{Role: valueUser95B1918A, Content: valueACleanProductRender0CB078AE}},
	})
	if err != nil {
		t.Fatalf("build image stream request body: %v", err)
	}
	if payload[valueStream94877F65] != true {
		t.Fatalf("expected stream request payload, got %#v", payload)
	}
	if _, ok := payload[valuePartialImages8165608A]; ok {
		t.Fatalf("partial_images must only be sent when explicitly configured, got %#v", payload)
	}
}

func TestBuildOpenAIImageGenerationStreamRequestBodyOmitsZeroPartialImages(t *testing.T) {
	payload, err := buildOpenAIImageGenerationStreamRequestBody(valueGptImage1433C51B1, GenerateInput{
		Messages: []Message{{Role: valueUser95B1918A, Content: valueACleanProductRender0CB078AE}},
		Options:  map[string]interface{}{valuePartialImages8165608A: 0},
	})
	if err != nil {
		t.Fatalf("build image stream request body: %v", err)
	}
	if _, ok := payload[valuePartialImages8165608A]; ok {
		t.Fatalf("partial_images must only be sent when configured as a positive value, got %#v", payload)
	}
}

func TestBuildOpenAIImageGenerationRequestBodyDallEParams(t *testing.T) {
	payload, err := buildOpenAIImageGenerationRequestBody("dall-e-3", GenerateInput{
		Messages: []Message{{Role: valueUser95B1918A, Content: valueACleanProductRender0CB078AE}},
		Options: map[string]interface{}{
			valueResponseFormatC64439E9:    valueUrlF02A82A2,
			"style":                        "natural",
			valueOutputFormatA54B283B:      valueWebpF0C9FD1D,
			valueOutputCompression3E726BAB: 80,
			valueBackgroundA6734840:        "transparent",
			"moderation":                   valueLow50D40DF9,
		},
	})
	if err != nil {
		t.Fatalf("build image request body: %v", err)
	}
	if payload[valueResponseFormatC64439E9] != valueUrlF02A82A2 || payload["style"] != "natural" {
		t.Fatalf("expected DALL-E params, got %#v", payload)
	}
	for _, key := range []string{valueOutputFormatA54B283B, valueOutputCompression3E726BAB, valueBackgroundA6734840, "moderation"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("expected GPT-image-only param %q to be omitted for DALL-E, got %#v", key, payload)
		}
	}
}

func TestBuildOpenAIImageGenerationRequestBodyDefaultsDallEToBase64(t *testing.T) {
	payload, err := buildOpenAIImageGenerationRequestBody("dall-e-3", GenerateInput{
		Messages: []Message{{Role: valueUser95B1918A, Content: valueACleanProductRender0CB078AE}},
	})
	if err != nil {
		t.Fatalf("build image request body: %v", err)
	}
	if payload[valueResponseFormatC64439E9] != valueB64Json6FEA0091 {
		t.Fatalf("expected DALL-E image generation to default to base64, got %#v", payload)
	}
}

func TestBuildOpenAIImageEditMultipartRequest(t *testing.T) {
	body, contentType, debugBody, err := buildOpenAIImageEditMultipartRequest(valueGptImage1433C51B1, GenerateInput{
		Messages: []Message{{
			Role: valueUser95B1918A,
			Parts: []ContentPart{
				{Kind: ContentPartText, Text: valueReplaceTheBackgroundE380DDBC},
				{Kind: ContentPartImage, MimeType: valueImagePng7655D3D8, FileName: "source.png", Data: []byte("source")},
			},
		}},
		ImageEditMask: &ContentPart{Kind: ContentPartImage, MimeType: valueImagePng7655D3D8, FileName: "mask.png", Data: []byte("mask")},
		Options: map[string]interface{}{
			valueSize25EB8B09:              valueText882A02F0,
			valueQualityC898EB46:           valueHighC9F5A9E4,
			valueOutputFormatA54B283B:      valueWebpF0C9FD1D,
			valueOutputCompression3E726BAB: 80,
			"input_fidelity":               valueHighC9F5A9E4,
			valuePartialImages8165608A:     2,
			valueStream94877F65:            true,
			valuePrompt53E4DDC8:            valueOverrideE37F6DB0,
		},
	}, false)
	requireNoError(t, err, "build image edit multipart request: %v")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	requireNoError(t, req.ParseMultipartForm(10<<20), "parse multipart body: %v")
	form := req.MultipartForm
	requireCondition(t, form.Value["model"][0] == valueGptImage1433C51B1 && form.Value[valuePrompt53E4DDC8][0] == valueReplaceTheBackgroundE380DDBC, "unexpected edit form fields: %#v", form.Value)
	requireCondition(t, form.Value[valueOutputFormatA54B283B][0] == valueWebpF0C9FD1D && form.Value["input_fidelity"][0] == valueHighC9F5A9E4, "expected edit params, got %#v", form.Value)
	if _, ok := form.Value[valueStream94877F65]; ok {
		t.Fatalf("stream must not be passed by non-streaming edit adapter: %#v", form.Value)
	}
	if _, ok := form.Value[valuePartialImages8165608A]; ok {
		t.Fatalf("partial_images must not be passed without upstream image edit streaming: %#v", form.Value)
	}
	requireCondition(t, len(form.File["image[]"]) == 1 && len(form.File["mask"]) == 1, "expected image[] and mask files, got %#v", form.File)
	var debug map[string]interface{}
	requireNoError(t, json.Unmarshal(debugBody, &debug), "parse debug body: %v")
	requireCondition(t, debug["image_count"] == float64(1) && debug["mask"] == true, "expected sanitized debug body, got %#v", debug)
}

func TestBuildOpenAIImageEditMultipartRequestDefaultsDallEToBase64(t *testing.T) {
	body, contentType, _, err := buildOpenAIImageEditMultipartRequest("dall-e-2", GenerateInput{
		Messages: []Message{{
			Role: valueUser95B1918A,
			Parts: []ContentPart{
				{Kind: ContentPartText, Text: valueReplaceTheBackgroundE380DDBC},
				{Kind: ContentPartImage, MimeType: valueImagePng7655D3D8, Data: []byte("source")},
			},
		}},
	}, false)
	if err != nil {
		t.Fatalf("build image edit multipart request: %v", err)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if err = req.ParseMultipartForm(10 << 20); err != nil {
		t.Fatalf("parse multipart body: %v", err)
	}
	if req.MultipartForm.Value[valueResponseFormatC64439E9][0] != valueB64Json6FEA0091 {
		t.Fatalf("expected DALL-E image edit to default to base64, got %#v", req.MultipartForm.Value)
	}
}

func TestBuildOpenAIImageEditMultipartRequestOmitsGPTImage2InputFidelity(t *testing.T) {
	body, contentType, _, err := buildOpenAIImageEditMultipartRequest(valueGptImage2DD41528C, GenerateInput{
		Messages: []Message{{
			Role: valueUser95B1918A,
			Parts: []ContentPart{
				{Kind: ContentPartText, Text: valueReplaceTheBackgroundE380DDBC},
				{Kind: ContentPartImage, MimeType: valueImagePng7655D3D8, Data: []byte("source")},
			},
		}},
		Options: map[string]interface{}{
			"input_fidelity":           valueHighC9F5A9E4,
			"input_fidelity_effective": "high_automatic",
			valueOutputFormatA54B283B:  valueWebpF0C9FD1D,
		},
	}, false)
	requireNoError(t, err, "build GPT Image 2 edit request: %v")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	requireNoError(t, req.ParseMultipartForm(10<<20), "parse GPT Image 2 multipart body: %v")
	requireCondition(
		t,
		!openAIImageEditModelSupportsInputFidelity("gpt-image-2-2026-04-21"),
		"GPT Image 2 snapshots must omit input_fidelity",
	)
	if _, ok := req.MultipartForm.Value["input_fidelity"]; ok {
		t.Fatalf("GPT Image 2 requires input_fidelity to be omitted: %#v", req.MultipartForm.Value)
	}
	if _, ok := req.MultipartForm.Value["input_fidelity_effective"]; ok {
		t.Fatalf("derived receipt metadata must not enter multipart: %#v", req.MultipartForm.Value)
	}
	requireCondition(t, req.MultipartForm.Value[valueOutputFormatA54B283B][0] == valueWebpF0C9FD1D, "expected supported edit params, got %#v", req.MultipartForm.Value)
}

func TestBuildOpenAIImageEditMultipartRequestPreservesImageOrder(t *testing.T) {
	want := []string{"image-03.png", "image-01.png", "image-03-role-2.png", "image-02.png"}
	parts := []ContentPart{{Kind: ContentPartText, Text: valueReplaceTheBackgroundE380DDBC}}
	for index, fileName := range want {
		parts = append(parts, ContentPart{
			Kind: ContentPartImage, FileName: fileName,
			MimeType: valueImagePng7655D3D8, Data: []byte{byte(index + 1)},
		})
	}
	body, contentType, _, err := buildOpenAIImageEditMultipartRequest(valueGptImage2DD41528C, GenerateInput{
		Messages: []Message{{Role: valueUser95B1918A, Parts: parts}},
	}, false)
	requireNoError(t, err, "build ordered GPT Image 2 edit request: %v")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	requireNoError(t, req.ParseMultipartForm(10<<20), "parse ordered GPT Image 2 multipart body: %v")
	files := req.MultipartForm.File["image[]"]
	requireCondition(t, len(files) == len(want), "image part count = %d, want %d", len(files), len(want))
	for index, fileName := range want {
		requireCondition(t, files[index].Filename == fileName, "image part %d = %q, want %q", index, files[index].Filename, fileName)
	}
}

func TestBuildOpenAIImageEditStreamMultipartRequest(t *testing.T) {
	body, contentType, _, err := buildOpenAIImageEditMultipartRequest(valueGptImage1433C51B1, GenerateInput{
		Messages: []Message{{
			Role: valueUser95B1918A,
			Parts: []ContentPart{
				{Kind: ContentPartText, Text: valueReplaceTheBackgroundE380DDBC},
				{Kind: ContentPartImage, MimeType: valueImagePng7655D3D8, Data: []byte("source")},
			},
		}},
		Options: map[string]interface{}{valuePartialImages8165608A: 2},
	}, true)
	if err != nil {
		t.Fatalf("build image edit stream multipart request: %v", err)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if err = req.ParseMultipartForm(10 << 20); err != nil {
		t.Fatalf("parse multipart body: %v", err)
	}
	if req.MultipartForm.Value[valueStream94877F65][0] != valueTrueA041BBAA || req.MultipartForm.Value[valuePartialImages8165608A][0] != "2" {
		t.Fatalf("expected edit stream fields, got %#v", req.MultipartForm.Value)
	}
}

func TestBuildOpenAIImageEditStreamMultipartRequestOmitsZeroPartialImages(t *testing.T) {
	body, contentType, _, err := buildOpenAIImageEditMultipartRequest(valueGptImage1433C51B1, GenerateInput{
		Messages: []Message{{
			Role: valueUser95B1918A,
			Parts: []ContentPart{
				{Kind: ContentPartText, Text: valueReplaceTheBackgroundE380DDBC},
				{Kind: ContentPartImage, MimeType: valueImagePng7655D3D8, Data: []byte("source")},
			},
		}},
		Options: map[string]interface{}{valuePartialImages8165608A: 0},
	}, true)
	if err != nil {
		t.Fatalf("build image edit stream multipart request: %v", err)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if err = req.ParseMultipartForm(10 << 20); err != nil {
		t.Fatalf("parse multipart body: %v", err)
	}
	if _, ok := req.MultipartForm.Value[valuePartialImages8165608A]; ok {
		t.Fatalf("partial_images must only be sent when configured as a positive value, got %#v", req.MultipartForm.Value)
	}
}

func TestParseOpenAIImageGenerationOutput(t *testing.T) {
	output, err := parseOpenAIImageOutput([]byte(`{
		"created": 1713833628,
		"data": [
			{"url": "https://example.com/a.png", "revised_prompt": "A revised render"},
			{"b64_json": "aGVsbG8="}
		],
		"usage": {
			"input_tokens": 12,
			"output_tokens": 40,
			"input_tokens_details": {"text_tokens": 8, "image_tokens": 4},
			"output_tokens_details": {"image_tokens": 40}
		}
	}`), valueWebpF0C9FD1D)
	requireNoError(t, err, "parse image output: %v")
	assertOpenAIImageGenerationOutput(t, output)
}

func assertOpenAIImageGenerationOutput(t *testing.T, output *GenerateOutput) {
	t.Helper()
	requireEqual(t, output.Text, "", "image adapter must not put generated image data into text, got %q", output.Text)
	requireCondition(t, len(output.Citations) == 1 && output.Citations[0] == "https://example.com/a.png", "expected URL citation, got %#v", output.Citations)
	requireCondition(t, len(output.GeneratedImages) == 2, "expected generated image metadata, got %#v", output.GeneratedImages)
	requireCondition(t, output.GeneratedImages[0].URL == "https://example.com/a.png" && output.GeneratedImages[0].MIMEType == valueImageWebp8CDFEA70, "unexpected URL image metadata: %#v", output.GeneratedImages[0])
	requireCondition(t, output.GeneratedImages[1].B64JSON == "aGVsbG8=" && output.GeneratedImages[1].MIMEType == valueImageWebp8CDFEA70, "unexpected b64 image metadata: %#v", output.GeneratedImages[1])
	requireCondition(t, output.Usage.InputTokens == 12 && output.Usage.OutputTokens == 40, "expected parsed upstream image usage, got %#v", output.Usage)
}

func TestParseOpenAIImageGenerationOutputDoesNotInventUsage(t *testing.T) {
	output, err := parseOpenAIImageOutput([]byte(`{
		"data": [{"b64_json": "aGVsbG8="}]
	}`), "png")
	if err != nil {
		t.Fatalf("parse image output: %v", err)
	}
	if output.Usage != (Usage{}) {
		t.Fatalf("expected missing upstream usage to remain empty, got %#v", output.Usage)
	}
}

func TestOpenAIImageGenerationStream(t *testing.T) {
	var requestPayload map[string]interface{}
	server := newOpenAIImageGenerationStreamServer(t, &requestPayload)
	defer server.Close()

	client := newTestProviderClient()
	var partials []GenerateStreamEvent
	output, err := client.GenerateStream(context.Background(), RouteConfig{
		Protocol:      AdapterOpenAIImageGenerations,
		BaseURL:       server.URL,
		UpstreamModel: valueGptImage1433C51B1,
	}, GenerateInput{
		Messages: []Message{{Role: valueUser95B1918A, Content: valueACleanProductRender0CB078AE}},
		Options: map[string]interface{}{
			valueOutputFormatA54B283B:  valueWebpF0C9FD1D,
			valuePartialImages8165608A: 2,
		},
	}, func(event GenerateStreamEvent) error {
		if event.GeneratedImage != nil {
			partials = append(partials, event)
		}
		return nil
	})
	requireNoError(t, err, "generate image stream: %v")
	assertOpenAIImageGenerationStreamResult(t, requestPayload, partials, output)
}

func newOpenAIImageGenerationStreamServer(t *testing.T, requestPayload *map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireEqual(t, r.URL.Path, "/v1/images/generations", "unexpected path: %s", r.URL.Path)
		requireEqual(t, r.Header.Get("Accept"), "text/event-stream", "expected event stream accept header, got %q", r.Header.Get("Accept"))
		requireNoError(t, json.NewDecoder(r.Body).Decode(requestPayload), "decode request body: %v")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: image_generation.partial_image\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"image_generation.partial_image\",\"partial_image_index\":1,\"b64_json\":\"cGFydGlhbA==\"}\n\n"))
		_, _ = w.Write([]byte("event: image_generation.completed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"image_generation.completed\",\"id\":\"img_1\",\"b64_json\":\"ZmluYWw=\",\"revised_prompt\":\"A final render\",\"usage\":{\"input_tokens\":12,\"output_tokens\":40}}\n\n"))
	}))
}

func assertOpenAIImageGenerationStreamResult(
	t *testing.T,
	requestPayload map[string]interface{},
	partials []GenerateStreamEvent,
	output *GenerateOutput,
) {
	t.Helper()
	requireCondition(t, requestPayload[valueStream94877F65] == true && requestPayload[valuePartialImages8165608A] == float64(2), "expected stream request payload, got %#v", requestPayload)
	requireCondition(t, len(partials) == 1 && partials[0].GeneratedImage != nil && partials[0].GeneratedImage.B64JSON == "cGFydGlhbA==", "expected partial image event, got %#v", partials)
	requireCondition(t, partials[0].GeneratedImageIndex == 1 && partials[0].GeneratedImagePartial, "unexpected partial metadata: %#v", partials[0])
	requireCondition(t, len(output.GeneratedImages) == 1 && output.GeneratedImages[0].B64JSON == valueZmluYWw19B3926B, "expected final generated image, got %#v", output.GeneratedImages)
	requireCondition(t, output.GeneratedImages[0].MIMEType == valueImageWebp8CDFEA70 && output.GeneratedImages[0].RevisedPrompt == "A final render", "unexpected final image metadata: %#v", output.GeneratedImages[0])
	requireCondition(t, output.Usage.InputTokens == 12 && output.Usage.OutputTokens == 40, "expected upstream stream usage, got %#v", output.Usage)
}

func TestOpenAIImageGenerationStreamFallsBackToJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "img_json_1",
			"data": [{"b64_json": "ZmluYWw="}],
			"usage": {"input_tokens": 12, "output_tokens": 40}
		}`))
	}))
	defer server.Close()

	client := newTestProviderClient()
	var usageEvents []Usage
	output, err := client.GenerateStream(context.Background(), RouteConfig{
		Protocol:      AdapterOpenAIImageGenerations,
		BaseURL:       server.URL,
		UpstreamModel: valueGptImage1433C51B1,
	}, GenerateInput{
		Messages: []Message{{Role: valueUser95B1918A, Content: valueACleanProductRender0CB078AE}},
	}, func(event GenerateStreamEvent) error {
		if event.Usage != (Usage{}) {
			usageEvents = append(usageEvents, event.Usage)
		}
		return nil
	})
	requireNoError(t, err, "generate image stream json fallback: %v")
	assertOpenAIImageGenerationJSONFallback(t, output, usageEvents)
}

func assertOpenAIImageGenerationJSONFallback(t *testing.T, output *GenerateOutput, usageEvents []Usage) {
	t.Helper()
	requireCondition(t, len(output.GeneratedImages) == 1 && output.GeneratedImages[0].B64JSON == valueZmluYWw19B3926B, "expected json fallback image, got %#v", output.GeneratedImages)
	requireEqual(t, output.ResponseID, "img_json_1", "expected response id from json fallback, got %q", output.ResponseID)
	requireCondition(t, output.Usage.InputTokens == 12 && output.Usage.OutputTokens == 40, "expected parsed json fallback usage, got %#v", output.Usage)
	requireCondition(t, len(usageEvents) == 1 && usageEvents[0].InputTokens == 12 && usageEvents[0].OutputTokens == 40, "expected usage event from json fallback, got %#v", usageEvents)
}

func TestOpenAIImageEditStream(t *testing.T) {
	var requestValues map[string][]string
	server := newOpenAIImageEditStreamServer(t, &requestValues)
	defer server.Close()

	client := newTestProviderClient()
	var partials []GenerateStreamEvent
	output, err := client.GenerateStream(context.Background(), RouteConfig{
		Protocol:      AdapterOpenAIImageEdits,
		BaseURL:       server.URL,
		UpstreamModel: valueGptImage1433C51B1,
	}, GenerateInput{
		Messages: []Message{{
			Role: valueUser95B1918A,
			Parts: []ContentPart{
				{Kind: ContentPartText, Text: valueReplaceTheBackgroundE380DDBC},
				{Kind: ContentPartImage, MimeType: valueImagePng7655D3D8, Data: []byte("source")},
			},
		}},
		Options: map[string]interface{}{
			valueOutputFormatA54B283B:  valueWebpF0C9FD1D,
			valuePartialImages8165608A: 2,
		},
	}, func(event GenerateStreamEvent) error {
		if event.GeneratedImage != nil {
			partials = append(partials, event)
		}
		return nil
	})
	requireNoError(t, err, "generate image edit stream: %v")
	assertOpenAIImageEditStreamResult(t, requestValues, partials, output)
}

func newOpenAIImageEditStreamServer(t *testing.T, requestValues *map[string][]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireEqual(t, r.URL.Path, "/v1/images/edits", "unexpected path: %s", r.URL.Path)
		requireEqual(t, r.Header.Get("Accept"), "text/event-stream", "expected event stream accept header, got %q", r.Header.Get("Accept"))
		r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
		reader, err := r.MultipartReader()
		requireNoError(t, err, "create multipart reader: %v")
		form, err := reader.ReadForm(10 << 20)
		requireNoError(t, err, "parse multipart request: %v")
		defer func() { requireNoError(t, form.RemoveAll(), "remove multipart form files: %v") }()
		*requestValues = form.Value
		requireCondition(t, len(form.File["image[]"]) == 1, "expected one edit image, got %#v", form.File)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: image_edit.partial_image\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"image_edit.partial_image\",\"partial_image_index\":1,\"b64_json\":\"cGFydGlhbA==\"}\n\n"))
		_, _ = w.Write([]byte("event: image_edit.completed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"image_edit.completed\",\"id\":\"img_edit_1\",\"b64_json\":\"ZmluYWw=\",\"usage\":{\"input_tokens\":22,\"output_tokens\":44}}\n\n"))
	}))
}

func assertOpenAIImageEditStreamResult(
	t *testing.T,
	requestValues map[string][]string,
	partials []GenerateStreamEvent,
	output *GenerateOutput,
) {
	t.Helper()
	requireCondition(t, requestValues[valueStream94877F65][0] == valueTrueA041BBAA && requestValues[valuePartialImages8165608A][0] == "2", "expected stream request values, got %#v", requestValues)
	requireCondition(t, len(partials) == 1 && partials[0].GeneratedImage != nil && partials[0].GeneratedImage.B64JSON == "cGFydGlhbA==", "expected partial edit image event, got %#v", partials)
	requireCondition(t, len(output.GeneratedImages) == 1 && output.GeneratedImages[0].B64JSON == valueZmluYWw19B3926B, "expected final edit image, got %#v", output.GeneratedImages)
	requireEqual(t, output.ResponseID, "img_edit_1", "expected edit response id, got %q", output.ResponseID)
	requireCondition(t, output.Usage.InputTokens == 22 && output.Usage.OutputTokens == 44, "expected edit usage, got %#v", output.Usage)
}
