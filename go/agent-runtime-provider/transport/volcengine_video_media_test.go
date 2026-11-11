package transport

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func mediaFixtureVideoResponses() []mediaFixtureHTTPResponse {
	return []mediaFixtureHTTPResponse{
		{contentType: mediaFixtureJSONMIME, body: `{"id":"media-task","status":"queued"}`},
		{contentType: mediaFixtureJSONMIME, body: `{"id":"media-task","status":"running"}`},
		{contentType: mediaFixtureJSONMIME, body: `{"id":"media-task","status":"succeeded","content":{"video_url":"https://media-adapter.invalid/result.mp4"}}`},
	}
}

func mediaFixtureVideoInput(images []ContentPart) GenerateInput {
	input := mediaFixtureInput(images)
	input.Options["negative_prompt"] = mediaFixtureNegative
	input.Options[volcengineVideoDurationOption] = 5
	input.Options[mediaPollIntervalOption] = 1
	input.Options[mediaPollTimeoutOption] = 2000
	input.Options["generate_audio"] = false
	input.Options[volcengineVideoImageRolesOption] = []string{mediaFixtureReferenceRole, mediaFixtureFirstFrameRole, mediaFixtureLastFrameRole}
	return input
}

func TestMediaAdapterVideoSubmitsExactPositiveNegativeAndReferences(t *testing.T) {
	client, capture := newMediaAdapterHTTPClient(t, mediaFixtureVideoResponses()...)
	images := mediaFixtureReferences()
	input := mediaFixtureVideoInput(images)
	before := mediaFixtureJSON(t, input)
	output, err := client.Generate(t.Context(), mediaFixtureRoute(AdapterVolcengineVideo, volcengineTestVideoModel), input)
	requireNoError(t, err, "generate video through fake HTTP: %v")
	request := requireMediaAdapterVideoSequence(t, capture)
	requireMediaAdapterVideoBody(t, request, mediaFixtureReviewed, mediaFixtureNegative, images)
	requireMediaAdapterInputUnchanged(t, before, input)
	if output == nil || len(output.GeneratedAssets) != 1 || output.GeneratedAssets[0].ExternalID != "media-task" {
		t.Fatalf("missing fake video task result: %#v", output)
	}
}

func requireMediaAdapterVideoSequence(t *testing.T, capture *mediaFixtureHTTPCapture) mediaFixtureHTTPRequest {
	t.Helper()
	if len(capture.requests) != 3 {
		t.Fatalf("video requests = %d, want one create and two polls", len(capture.requests))
	}
	create := capture.requests[0]
	requireEqual(t, create.method, http.MethodPost, "video create method = %q", create.method)
	requireEqual(t, create.path, "/v3/contents/generations/tasks", "video create path = %q", create.path)
	for _, poll := range capture.requests[1:] {
		if poll.method != http.MethodGet || poll.path != create.path+"/media-task" || len(poll.body) != 0 {
			t.Fatalf("poll must not redispatch a paid generation or prompt: %#v", poll)
		}
	}
	return create
}

func requireMediaAdapterVideoBody(t *testing.T, request mediaFixtureHTTPRequest, positive, negative string, images []ContentPart) {
	t.Helper()
	var payload struct {
		NegativePrompt string `json:"negative_prompt"`
		Content        []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Role     string `json:"role"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"content"`
	}
	requireNoError(t, json.Unmarshal(request.body, &payload), "decode serialized video request: %v")
	if len(payload.Content) != len(images)+1 {
		t.Fatalf("video content parts = %d, want body and %d references", len(payload.Content), len(images))
	}
	requireEqual(t, payload.Content[0].Type, ContentPartText, "video first part type = %q", payload.Content[0].Type)
	requireEqual(t, payload.Content[0].Text, positive, "submitted positive video prompt changed\ngot: %q\nwant: %q", payload.Content[0].Text, positive)
	requireEqual(t, payload.NegativePrompt, negative, "submitted negative video prompt changed\ngot: %q\nwant: %q", payload.NegativePrompt, negative)
	roles := []string{mediaFixtureReferenceRole, mediaFixtureFirstFrameRole, mediaFixtureLastFrameRole}
	for index, image := range images {
		part := payload.Content[index+1]
		if part.Type != mediaFixtureImageURLType || part.Role != roles[index] || part.Text != "" {
			t.Fatalf("video reference %d changed role or gained unreviewed text: %#v", index, part)
		}
		requireMediaAdapterDataURL(t, part.ImageURL.URL, image)
	}
}

func TestMediaAdapterVideoDoesNotInventProviderRevisedPrompt(t *testing.T) {
	client, capture := newMediaAdapterHTTPClient(t, mediaFixtureVideoResponses()...)
	input := mediaFixtureVideoInput(nil)
	before := mediaFixtureJSON(t, input)
	output, err := client.Generate(t.Context(), mediaFixtureRoute(AdapterVolcengineVideo, volcengineTestVideoModel), input)
	requireNoError(t, err, "generate fake video without provider revision: %v")
	requireMediaAdapterVideoSequence(t, capture)
	requireMediaAdapterInputUnchanged(t, before, input)
	if output == nil || len(output.GeneratedAssets) != 1 {
		t.Fatalf("expected fake video result, got %#v", output)
	}
	if bytes.Contains([]byte(output.RawJSON), []byte("revised_prompt")) {
		t.Fatal("fake provider must not supply a revised prompt for this regression")
	}
	if revised := output.GeneratedAssets[0].RevisedPrompt; revised != "" {
		t.Fatalf("invented provider revised_prompt = %q; response has no revision, original request must remain separately attributed", revised)
	}
}
