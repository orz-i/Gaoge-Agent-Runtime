package transport

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

const (
	volcengineTestVideoModel  = "doubao-seedance-2-0-260128"
	volcengineTestVideoPrompt = "让画面中的海浪缓慢推进"
	volcengineTestVideoURL    = "https://cdn.example/video.mp4"
)

func TestBuildVolcengineVideoRequestBody(t *testing.T) {
	input := GenerateInput{
		Messages: []Message{{
			Role: valueUserF1E3DFF7,
			Parts: []ContentPart{
				{Kind: ContentPartText, Text: volcengineTestVideoPrompt},
				{Kind: ContentPartImage, MimeType: valueImagePng415383B9, Data: []byte("first-frame")},
				{Kind: ContentPartImage, MimeType: "image/jpeg", Data: []byte("last-frame")},
			},
		}},
		Options: map[string]interface{}{
			volcengineVideoResolutionOption: "1080p",
			"ratio":                         "16:9",
			volcengineVideoDurationOption:   float64(5),
			valueSeedC8D041FC:               float64(42),
			volcengineVideoWatermarkOption:  false,
			"generate_audio":                true,
			"return_last_frame":             true,
			volcengineVideoImageRolesOption: []string{"first_frame", "last_frame"},
		},
	}
	payload, prompt, err := buildVolcengineVideoRequestBody(volcengineTestVideoModel, input)
	if err != nil {
		t.Fatalf("build request body: %v", err)
	}
	if prompt != volcengineTestVideoPrompt || payload[valueModel37CF506F] != volcengineTestVideoModel {
		t.Fatalf("unexpected prompt or model: %#v", payload)
	}
	assertVolcengineVideoContent(t, payload[valueContent03EE2C26])
	if payload[volcengineVideoResolutionOption] != "1080p" || payload["ratio"] != "16:9" || payload["duration"] != 5 {
		t.Fatalf("unexpected video options: %#v", payload)
	}
	if payload[volcengineVideoWatermarkOption] != false || payload["generate_audio"] != true || payload["return_last_frame"] != true {
		t.Fatalf("unexpected boolean video options: %#v", payload)
	}
}

func TestVolcengineVideoGenerationAdapterReportsFailedAndTimedOutTasks(t *testing.T) {
	tests := []struct {
		name       string
		pollStatus string
		timeoutMS  int
		wantStatus int
	}{
		{name: volcengineVideoStatusFailed, pollStatus: `{"id":"cgt-error","status":"failed","error":{"code":"ContentPolicy","message":"rejected"}}`, timeoutMS: 1000, wantStatus: http.StatusBadRequest},
		{name: "timeout", pollStatus: `{"id":"cgt-error","status":"running"}`, timeoutMS: 20, wantStatus: http.StatusRequestTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// Advance virtual time after task creation and the first poll,
				// so the timeout cannot race the initial HTTP request.
				client, capture := newMediaAdapterHTTPClient(t,
					mediaFixtureHTTPResponse{contentType: mediaFixtureJSONMIME, body: `{"id":"cgt-error","status":"queued"}`},
					mediaFixtureHTTPResponse{contentType: mediaFixtureJSONMIME, body: test.pollStatus},
				)
				_, err := client.Generate(t.Context(), mediaFixtureRoute(AdapterVolcengineVideo, volcengineTestVideoModel), GenerateInput{
					Messages: []Message{{Role: valueUserF1E3DFF7, Content: volcengineTestVideoPrompt}},
					Options:  map[string]interface{}{volcengineVideoDurationOption: 5, mediaPollTimeoutOption: test.timeoutMS},
				})
				var upstream *UpstreamError
				if !errors.As(err, &upstream) || upstream.StatusCode != test.wantStatus {
					t.Fatalf("error = %#v, want upstream status %d", err, test.wantStatus)
				}
				if len(capture.requests) != 2 || capture.requests[0].method != http.MethodPost || capture.requests[1].method != http.MethodGet {
					t.Fatalf("expected task creation followed by polling, got %#v", capture.requests)
				}
			})
		})
	}
}

func assertVolcengineVideoContent(t *testing.T, raw interface{}) {
	t.Helper()
	content, ok := raw.([]map[string]interface{})
	if !ok || len(content) != 3 {
		t.Fatalf("unexpected video content: %#v", raw)
	}
	if content[0][valueType945D3026] != ContentPartText || content[0][valueText1D495308] != volcengineTestVideoPrompt {
		t.Errorf("unexpected text content: %#v", content[0])
	}
	if content[1]["role"] != volcengineVideoRoleFirstFrame || content[2]["role"] != volcengineVideoRoleLastFrame {
		t.Errorf("unexpected image roles: %#v", content)
	}
	for _, index := range []int{1, 2} {
		image := asMap(content[index]["image_url"])
		imageURL, _ := image["url"].(string)
		if !strings.HasPrefix(imageURL, "data:image/") {
			t.Errorf("unexpected image URL: %#v", image)
		}
	}
}

func TestVolcengineVideoGenerationAdapterCreatesAndPollsTask(t *testing.T) {
	apiKey := strings.Join([]string{"volcengine", "video", volcengineTestKeySegment, "key"}, "-")
	var pollCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		handleVolcengineVideoTestRequest(t, w, request, apiKey, &pollCount)
	}))
	defer server.Close()

	client := newTestProviderClient()
	output, err := client.Generate(t.Context(), RouteConfig{
		Protocol:      AdapterVolcengineVideo,
		BaseURL:       server.URL + "/api/v3",
		APIKey:        apiKey,
		UpstreamModel: volcengineTestVideoModel,
	}, GenerateInput{
		Messages: []Message{{Role: valueUserF1E3DFF7, Content: volcengineTestVideoPrompt}},
		Options: map[string]interface{}{
			volcengineVideoDurationOption: 5,
			mediaPollIntervalOption:       1,
			mediaPollTimeoutOption:        2000,
		},
	})
	if err != nil {
		t.Fatalf("generate video: %v", err)
	}
	assertVolcengineVideoOutput(t, output, pollCount.Load())
}

func handleVolcengineVideoTestRequest(
	t *testing.T,
	w http.ResponseWriter,
	request *http.Request,
	apiKey string,
	pollCount *atomic.Int32,
) {
	t.Helper()
	if got := request.Header.Get("Authorization"); got != "Bearer "+apiKey {
		t.Errorf("authorization = %q", got)
	}
	w.Header().Set("Content-Type", "application/json")
	switch request.Method {
	case http.MethodPost:
		assertVolcengineVideoCreateRequest(t, request)
		_, _ = w.Write([]byte(`{"id":"cgt-test","status":"queued"}`))
	case http.MethodGet:
		if request.URL.Path != "/api/v3/contents/generations/tasks/cgt-test" {
			t.Errorf("unexpected task path: %s", request.URL.Path)
		}
		if pollCount.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"id":"cgt-test","status":"running"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"cgt-test","status":"succeeded","content":{"video_url":"https://cdn.example/video.mp4"},"usage":{"completion_tokens":12,"total_tokens":12}}`))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func assertVolcengineVideoCreateRequest(t *testing.T, request *http.Request) {
	t.Helper()
	if request.URL.Path != "/api/v3/contents/generations/tasks" {
		t.Errorf("unexpected create path: %s", request.URL.Path)
	}
	var payload map[string]interface{}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		t.Fatalf("decode create request: %v", err)
	}
	if payload[valueModel37CF506F] != volcengineTestVideoModel {
		t.Errorf("unexpected create payload: %#v", payload)
	}
}

func assertVolcengineVideoOutput(t *testing.T, output *GenerateOutput, pollCount int32) {
	t.Helper()
	if output == nil {
		t.Fatal("expected video generation output")
	}
	if pollCount != 2 || output.ResponseID != "cgt-test" || output.Text != volcengineTestVideoURL {
		t.Errorf("unexpected task output: %#v, polls=%d", output, pollCount)
	}
	if output.Usage.OutputTokens != 12 || len(output.GeneratedImages) != 0 || len(output.GeneratedAssets) != 1 {
		t.Fatalf("unexpected usage or media output: %#v", output)
	}
	media := output.GeneratedAssets[0]
	requireEqual(t, media.Kind, GeneratedAssetKindVideo, "video kind = %q", media.Kind)
	requireEqual(t, media.URL, volcengineTestVideoURL, "video URL = %q", media.URL)
	requireEqual(t, media.MIMEType, volcengineVideoMIMEType, "video MIME type = %q", media.MIMEType)
	requireEqual(t, media.ExternalID, "cgt-test", "video external ID = %q", media.ExternalID)
}

func TestValidateVolcengineSeedance2RequestRejectsRetiredModelsAndInvalidLimits(t *testing.T) {
	valid := GenerateInput{Options: map[string]interface{}{volcengineVideoDurationOption: 5}}
	if err := validateVolcengineSeedance2Request("doubao-seedance-1-0-pro-250528", valid); err == nil {
		t.Fatal("expected retired Seedance model to be rejected")
	}
	invalidDuration := valid
	invalidDuration.Options = map[string]interface{}{volcengineVideoDurationOption: 3}
	if err := validateVolcengineSeedance2Request(volcengineTestVideoModel, invalidDuration); err == nil {
		t.Fatal("expected duration below four seconds to be rejected")
	}
	parts := make([]ContentPart, 0, volcengineSeedance2MaxImages+1)
	for index := 0; index <= volcengineSeedance2MaxImages; index++ {
		parts = append(parts, ContentPart{Kind: ContentPartImage, MimeType: "image/png", Data: []byte{byte(index)}})
	}
	tooMany := GenerateInput{Messages: []Message{{Parts: parts}}, Options: map[string]interface{}{volcengineVideoDurationOption: 5}}
	if err := validateVolcengineSeedance2Request(volcengineTestVideoModel, tooMany); err == nil {
		t.Fatal("expected more than nine image references to be rejected")
	}
}
