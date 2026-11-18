package transport

import (
	"encoding/json"
	"testing"
)

type mediaFixtureImageAdapterCase struct {
	name     string
	protocol string
	model    string
	path     string
	stream   bool
}

func mediaFixtureImageAdapters() []mediaFixtureImageAdapterCase {
	return []mediaFixtureImageAdapterCase{
		{name: "openai", protocol: AdapterOpenAIImageGenerations, model: mediaFixtureOpenAIModel, path: mediaFixtureGenerationsPath},
		{name: "openai_stream", protocol: AdapterOpenAIImageGenerations, model: mediaFixtureOpenAIModel, path: mediaFixtureGenerationsPath, stream: true},
		{name: "gemini", protocol: AdapterGoogleImageGeneration, model: mediaFixtureGeminiModel, path: "/v1beta/models/gemini-2.5-flash-image:generateContent"},
		{name: "gemini_stream", protocol: AdapterGoogleImageGeneration, model: mediaFixtureGeminiModel, path: "/v1beta/models/gemini-2.5-flash-image:streamGenerateContent", stream: true},
		{name: "xai", protocol: AdapterXAIImage, model: "grok-imagine-image", path: mediaFixtureGenerationsPath},
	}
}

func generateMediaAdapterImage(t *testing.T, client *Client, adapter mediaFixtureImageAdapterCase, input GenerateInput) *GenerateOutput {
	t.Helper()
	var output *GenerateOutput
	var err error
	if adapter.stream {
		output, err = client.GenerateStream(t.Context(), mediaFixtureRoute(adapter.protocol, adapter.model), input, nil)
	} else {
		output, err = client.Generate(t.Context(), mediaFixtureRoute(adapter.protocol, adapter.model), input)
	}
	requireNoError(t, err, "generate image through fake provider transport: %v")
	return output
}

func TestMediaAdapterImageGenerationSubmitsReviewedBody(t *testing.T) {
	for _, adapter := range mediaFixtureImageAdapters() {
		t.Run(adapter.name, func(t *testing.T) {
			for _, revised := range []string{mediaFixtureRevised, ""} {
				name := "provider_revision"
				if revised == "" {
					name = "no_provider_revision"
				}
				t.Run(name, func(t *testing.T) {
					client, capture := newMediaAdapterHTTPClient(t, mediaFixtureImageResponse(t, adapter.protocol, revised, adapter.stream))
					input := mediaFixtureInput(nil)
					before := mediaFixtureJSON(t, input)
					output := generateMediaAdapterImage(t, client, adapter, input)
					request := requireMediaAdapterSingleRequest(t, capture, adapter.path)
					requireMediaAdapterImageJSON(t, request, adapter.protocol, mediaFixtureReviewed, nil)
					requireMediaAdapterRevisedImage(t, output, revised)
					requireMediaAdapterInputUnchanged(t, before, input)
				})
			}
		})
	}
}

type mediaFixtureImageJSONRequest struct {
	Prompt   string          `json:"prompt"`
	Image    json.RawMessage `json:"image"`
	Contents []struct {
		Role  string `json:"role"`
		Parts []struct {
			Text       string `json:"text"`
			InlineData *struct {
				MIMEType string `json:"mime_type"`
				Data     string `json:"data"`
			} `json:"inline_data"`
		} `json:"parts"`
	} `json:"contents"`
}

func requireMediaAdapterImageJSON(t *testing.T, request mediaFixtureHTTPRequest, protocol, body string, images []ContentPart) {
	t.Helper()
	var payload mediaFixtureImageJSONRequest
	requireNoError(t, json.Unmarshal(request.body, &payload), "decode submitted image JSON: %v")
	if protocol == AdapterGoogleImageGeneration {
		requireMediaAdapterGeminiParts(t, payload, body, images)
		return
	}
	requireEqual(t, payload.Prompt, body, "submitted image prompt changed\ngot: %q\nwant: %q", payload.Prompt, body)
	if len(images) > 0 {
		requireMediaAdapterXAIReferences(t, payload.Image, images)
	}
}

func requireMediaAdapterGeminiParts(t *testing.T, payload mediaFixtureImageJSONRequest, body string, images []ContentPart) {
	t.Helper()
	if len(payload.Contents) != 1 || payload.Contents[0].Role != mediaFixtureUserRole {
		t.Fatalf("expected one user content without appended instructions, got %#v", payload.Contents)
	}
	parts := payload.Contents[0].Parts
	if len(parts) != len(images)+1 {
		t.Fatalf("submitted Gemini parts = %d, want text plus %d references", len(parts), len(images))
	}
	requireEqual(t, parts[0].Text, body, "submitted Gemini prompt changed\ngot: %q\nwant: %q", parts[0].Text, body)
	for index, image := range images {
		part := parts[index+1]
		if part.Text != "" || part.InlineData == nil {
			t.Fatalf("reference %d gained unreviewed text or lost bytes: %#v", index, part)
		}
		requireMediaAdapterDataURL(t, "data:"+part.InlineData.MIMEType+";base64,"+part.InlineData.Data, image)
	}
}

func requireMediaAdapterXAIReferences(t *testing.T, raw json.RawMessage, images []ContentPart) {
	t.Helper()
	type reference struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	var references []reference
	if len(images) == 1 {
		var single reference
		requireNoError(t, json.Unmarshal(raw, &single), "decode single xAI reference: %v")
		references = append(references, single)
	} else {
		requireNoError(t, json.Unmarshal(raw, &references), "decode ordered xAI references: %v")
	}
	if len(references) != len(images) {
		t.Fatalf("submitted xAI references = %d, want %d", len(references), len(images))
	}
	for index, image := range images {
		requireEqual(t, references[index].Type, mediaFixtureImageURLType, "xAI reference type = %q", references[index].Type)
		requireMediaAdapterDataURL(t, references[index].URL, image)
	}
}
