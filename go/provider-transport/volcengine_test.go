package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	volcengineTestImageModel  = "doubao-seedream-5-0-260128"
	volcengineTestImagePrompt = "把参考图改成水彩风格"
	volcengineTestKeySegment  = "test"
)

func TestVolcengineAdaptersAreRegistered(t *testing.T) {
	client := newTestProviderClient()
	for _, protocol := range []string{AdapterVolcengineResponses, AdapterVolcengineImage, AdapterVolcengineVideo} {
		adapter, err := client.adapterFor(RouteConfig{Protocol: protocol})
		if err != nil {
			t.Fatalf("adapter %q is not registered: %v", protocol, err)
		}
		if adapter.Name() != protocol {
			t.Fatalf("adapter name = %q, want %q", adapter.Name(), protocol)
		}
	}
}

func TestNormalizeVolcengineRouteUsesOfficialV3Base(t *testing.T) {
	route := normalizeVolcengineRoute(RouteConfig{}, AdapterVolcengineResponses, EndpointResponses)
	if route.BaseURL != volcengineDefaultAPIBase {
		t.Fatalf("base URL = %q, want %q", route.BaseURL, volcengineDefaultAPIBase)
	}
	if got, want := buildOpenAIRequestURL(route.BaseURL, route.Endpoint), "https://ark.cn-beijing.volces.com/api/v3/responses"; got != want {
		t.Fatalf("request URL = %q, want %q", got, want)
	}
}

func TestBuildVolcengineImageGenerationRequestBody(t *testing.T) {
	input := GenerateInput{
		Messages: []Message{{
			Role:    valueUserF1E3DFF7,
			Content: volcengineTestImagePrompt,
			Parts: []ContentPart{
				{Kind: ContentPartText, Text: volcengineTestImagePrompt},
				{
					Kind:     ContentPartImage,
					MimeType: valueImagePng415383B9,
					Data:     []byte("image-data"),
				},
			},
		}},
		Options: map[string]interface{}{
			"size":                         "2K",
			valueResponseFormat00805748:    "b64_json",
			volcengineVideoWatermarkOption: false,
			valueSeedC8D041FC:              float64(42),
			"guidance_scale":               5.5,
			"sequential_image_generation":  "auto",
			"sequential_image_generation_options": map[string]interface{}{
				"max_images": float64(3),
			},
		},
	}
	payload, err := buildVolcengineImageGenerationRequestBody(volcengineTestImageModel, input)
	if err != nil {
		t.Fatalf("build request body: %v", err)
	}
	assertVolcengineImageGenerationRequestBody(t, payload)
}

func assertVolcengineImageGenerationRequestBody(t *testing.T, payload map[string]interface{}) {
	t.Helper()
	if payload["model"] != volcengineTestImageModel || payload["prompt"] != volcengineTestImagePrompt {
		t.Errorf("unexpected model or prompt: %#v", payload)
	}
	images, ok := payload["image"].([]string)
	if !ok || len(images) != 1 || !strings.HasPrefix(images[0], "data:image/png;base64,") {
		t.Errorf("unexpected image payload: %#v", payload["image"])
	}
	if payload[volcengineVideoWatermarkOption] != false || payload[valueSeedC8D041FC] != 42 || payload["guidance_scale"] != 5.5 {
		t.Errorf("unexpected scalar parameters: %#v", payload)
	}
	sequential := asMap(payload["sequential_image_generation_options"])
	if sequential["max_images"] != 3 {
		t.Errorf("unexpected sequential options: %#v", sequential)
	}
}

func TestVolcengineImageGenerationAdapterSendsNativeRequest(t *testing.T) {
	apiKey := strings.Join([]string{"volcengine", volcengineTestKeySegment, "key"}, "-")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/images/generations" {
			t.Errorf("request = %s %s, want POST /api/v3/images/generations", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("authorization = %q", got)
		}
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if payload["model"] != volcengineTestImageModel || payload["prompt"] != "生成一只橘猫" {
			t.Errorf("unexpected request payload: %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"https://cdn.example/image.png"}]}`))
	}))
	defer server.Close()

	client := newTestProviderClient()
	output, err := client.Generate(t.Context(), RouteConfig{
		Protocol:      AdapterVolcengineImage,
		BaseURL:       server.URL + "/api/v3",
		APIKey:        apiKey,
		UpstreamModel: volcengineTestImageModel,
	}, GenerateInput{Messages: []Message{{Role: valueUserF1E3DFF7, Content: "生成一只橘猫"}}})
	if err != nil {
		t.Fatalf("generate image: %v", err)
	}
	if output == nil {
		t.Fatal("expected image generation output")
	}
}
