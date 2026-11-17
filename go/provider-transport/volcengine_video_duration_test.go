package transport

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

const (
	volcengineDurationTestUsage     = `{"completion_tokens":12,"total_tokens":12}`
	volcengineDurationTestStatusKey = "status"
)

func TestVolcengineVideoDurationMetadataUsesSuccessfulProviderResponse(t *testing.T) {
	tests := []struct {
		name         string
		durationJSON string
		wantMetadata string
	}{
		{name: "actual_differs_from_requested", durationJSON: "7", wantMetadata: `{"durationSeconds":7,"source":"provider"}`},
		{name: "missing"},
		{name: "zero", durationJSON: "0"},
		{name: "negative", durationJSON: "-1"},
		{name: "null", durationJSON: "null"},
		{name: "int64_precision", durationJSON: "9223372036854775807", wantMetadata: `{"durationSeconds":9223372036854775807,"source":"provider"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			responses := volcengineDurationTestResponses(t, test.durationJSON)
			client, capture := newMediaAdapterHTTPClient(t, responses...)
			input := mediaFixtureVideoInput(nil)
			before := mediaFixtureJSON(t, input)
			output, err := client.Generate(t.Context(), mediaFixtureRoute(AdapterVolcengineVideo, volcengineTestVideoModel), input)
			requireNoError(t, err, "generate fake video with provider duration: %v")
			request := requireMediaAdapterVideoSequence(t, capture)
			var submitted struct {
				Duration int64 `json:"duration"`
			}
			requireNoError(t, json.Unmarshal(request.body, &submitted), "decode requested duration: %v")
			requireEqual(t, submitted.Duration, int64(5), "requested duration = %d, want 5", submitted.Duration)
			requireMediaAdapterInputUnchanged(t, before, input)
			requireVolcengineDurationOutput(t, output, test.wantMetadata, responses[2].body)
		})
	}
}

func volcengineDurationTestResponses(t *testing.T, durationJSON string) []mediaFixtureHTTPResponse {
	t.Helper()
	responses := mediaFixtureVideoResponses()
	// Earlier task snapshots must not supply duration when the final result
	// omits it. Nested content duration is likewise not the top-level field.
	responses[0].body = `{"id":"media-task","status":"queued","duration":13}`
	responses[1].body = `{"id":"media-task","status":"running","duration":9}`
	final := map[string]interface{}{
		"id": "media-task", volcengineDurationTestStatusKey: "succeeded",
		"content": map[string]interface{}{"video_url": "https://media-adapter.invalid/result.mp4", volcengineVideoDurationOption: 11},
		"usage":   json.RawMessage(volcengineDurationTestUsage),
	}
	if durationJSON != "" {
		final[volcengineVideoDurationOption] = json.RawMessage(durationJSON)
	}
	responses[2].body = string(mediaFixtureJSON(t, final))
	return responses
}

func requireVolcengineDurationOutput(t *testing.T, output *GenerateOutput, metadata, responseBody string) {
	t.Helper()
	if output == nil || len(output.GeneratedAssets) != 1 {
		t.Fatalf("expected one generated video, got %#v", output)
	}
	asset := output.GeneratedAssets[0]
	requireEqual(t, asset.MetadataJSON, metadata, "duration metadata = %q, want %q", asset.MetadataJSON, metadata)
	requireEqual(t, asset.RevisedPrompt, "", "provider did not supply a prompt revision: %q", asset.RevisedPrompt)
	requireEqual(t, output.Usage.RawUsageJSON, volcengineDurationTestUsage, "raw usage was changed: %s", output.Usage.RawUsageJSON)
	requireEqual(t, output.Usage.OutputTokens, int64(12), "completion tokens were changed: %d", output.Usage.OutputTokens)
	requireEqual(t, output.RawJSON, responseBody, "provider response snapshot was changed: %s", output.RawJSON)
	converted := output.GeneratedAssets
	requireEqual(t, len(converted), 1, "gateway asset count = %d, want 1", len(converted))
	requireEqual(t, converted[0].MetadataJSON, metadata, "gateway lost provider duration metadata: %q", converted[0].MetadataJSON)
}

func TestVolcengineVideoDurationDoesNotCreateAssetForFailedTask(t *testing.T) {
	responses := volcengineDurationTestResponses(t, "7")
	responses[2].body = `{"id":"media-task","status":"failed","duration":7,"error":{"code":"GenerationFailed","message":"failed"}}`
	client, capture := newMediaAdapterHTTPClient(t, responses...)
	output, err := client.Generate(t.Context(), mediaFixtureRoute(AdapterVolcengineVideo, volcengineTestVideoModel), mediaFixtureVideoInput(nil))
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected failed video task error, got %v", err)
	}
	if output != nil {
		t.Fatalf("failed task must not produce an asset or billing metadata: %#v", output)
	}
	requireMediaAdapterVideoSequence(t, capture)
}
