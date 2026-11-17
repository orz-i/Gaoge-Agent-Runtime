package transport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMiniMaxVideoCreatesOnceAndPollsNativeV2Task(t *testing.T) {
	var creates, polls atomic.Int32
	requests := make(chan map[string]interface{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer fixture-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", valueApplicationJson71FA0DF4)
		if r.Method == http.MethodPost {
			creates.Add(1)
			assert.Equal(t, "/v2/video_generation", r.URL.Path)
			var payload map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode: %v", err)
				return
			}
			requests <- payload
			_, _ = io.WriteString(w, `{"task_id":"task-1"}`)
			return
		}
		assert.Equal(t, "/v2/query/video_generation/task-1", r.URL.Path)
		status := miniMaxStatusQueued
		if polls.Add(1) >= 2 {
			status = miniMaxStatusSucceeded
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"task": map[string]interface{}{"id": "task-1", interactionKeyStatus: status, valueContent3EAD52FD: map[string]interface{}{valueUrl5D7AD561: volcengineTestVideoURL}, volcengineVideoDurationOption: 6, interactionKeyUsage: map[string]interface{}{"seconds": 6}}})
	}))
	defer server.Close()
	output, err := newTestProviderClient().Generate(t.Context(), RouteConfig{Protocol: AdapterMiniMaxVideo, BaseURL: server.URL + "/v2", APIKey: miniMaxFixtureAPIKey, UpstreamModel: miniMaxModelH3}, GenerateInput{
		Messages: []Message{{Content: "sunrise"}},
		Options:  map[string]interface{}{miniMaxImageURLsInput: []string{"mm_file://reference-1"}, "reference_audio_urls": []string{"https://cdn.example/reference.mp3"}, mediaPollIntervalOption: 1},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, creates.Load())
	assert.EqualValues(t, 2, polls.Load())
	payload := <-requests
	content := asSlice(payload[valueContent3EAD52FD])
	require.Len(t, content, 3)
	assert.Equal(t, valueText53ED4E15, asMap(content[0])[valueTypeF2B37301])
	assert.Equal(t, miniMaxRoleReferenceImage, asMap(content[1])[valueRoleE6F72470])
	assert.Equal(t, "mm_file://reference-1", asMap(asMap(content[1])[valueImageUrlD285EA63])[valueUrl5D7AD561])
	assert.Equal(t, "reference_audio", asMap(content[2])[valueRoleE6F72470])
	require.Len(t, output.GeneratedAssets, 1)
	assert.Equal(t, GeneratedAssetKindVideo, output.GeneratedAssets[0].Kind)
	assert.Equal(t, "task-1", output.GeneratedAssets[0].ExternalID)
	assert.JSONEq(t, `{"seconds":6}`, output.Usage.RawUsageJSON)
	assert.Zero(t, output.Usage.OutputTokens)
	assert.JSONEq(t, `{"durationSeconds":6,"source":"provider"}`, output.GeneratedAssets[0].MetadataJSON)
}

func TestMiniMaxVideoFailurePreservesTaskAndDoesNotRecreate(t *testing.T) {
	for _, test := range []struct {
		name, response  string
		status, timeout int
	}{
		{miniMaxStatusFailed, `{"task":{"id":"task-2","status":"failed","error":{"code":"ContentPolicy","message":"rejected"}}}`, 200, 0},
		{miniMaxStatusCancelled, `{"task":{"id":"task-2","status":"cancelled"}}`, 200, 0},
		{"unknown", `{"task":{"id":"task-2","status":"unexpected"}}`, 200, 0},
		{"missing URL", `{"task":{"id":"task-2","status":"succeeded"}}`, 200, 0},
		{"wrong id", `{"task":{"id":"different-task","status":"succeeded","content":{"url":"https://cdn.example/wrong.mp4"}}}`, 200, 0},
		{"query error", `{"error":{"message":"unavailable"}}`, 503, 0},
		{"timeout", `{"task":{"id":"task-2","status":"running"}}`, 200, 40},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client, capture := newMediaAdapterHTTPClient(t,
					mediaFixtureHTTPResponse{contentType: mediaFixtureJSONMIME, body: `{"task_id":"task-2"}`},
					mediaFixtureHTTPResponse{statusCode: test.status, contentType: mediaFixtureJSONMIME, body: test.response},
				)
				_, err := client.Generate(t.Context(), mediaFixtureRoute(AdapterMiniMaxVideo, miniMaxModelH3), GenerateInput{
					Messages: []Message{{Content: GeneratedAssetKindVideo}},
					Options:  map[string]interface{}{mediaPollTimeoutOption: test.timeout},
				})
				require.Error(t, err)
				assert.Contains(t, err.Error(), "task_id=task-2")
				require.Len(t, capture.requests, 2)
				assert.Equal(t, http.MethodPost, capture.requests[0].method)
				assert.Equal(t, http.MethodGet, capture.requests[1].method)
				if test.timeout > 0 {
					require.ErrorIs(t, err, context.DeadlineExceeded)
					return
				}
				var upstream *UpstreamError
				require.ErrorAs(t, err, &upstream)
				assert.NotNil(t, upstream.Debug)
				if test.name == miniMaxStatusFailed || test.name == miniMaxStatusCancelled {
					assert.Equal(t, http.StatusBadRequest, upstream.StatusCode)
				} else {
					assert.Equal(t, http.StatusConflict, upstream.StatusCode)
				}
			})
		})
	}
}

func TestMiniMaxVideoCancellationAndCreationRedirect(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var creates, redirected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			creates.Add(1)
			_, _ = io.WriteString(w, `{"task_id":"task-cancel"}`)
			return
		}
		_, _ = io.WriteString(w, `{"task":{"id":"task-cancel","status":"running"}}`)
		cancel()
	}))
	defer server.Close()
	_, err := newTestProviderClient().Generate(ctx, RouteConfig{Protocol: AdapterMiniMaxVideo, BaseURL: server.URL, UpstreamModel: miniMaxModelH3}, GenerateInput{Messages: []Message{{Content: GeneratedAssetKindVideo}}})
	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "task-cancel")
	assert.EqualValues(t, 1, creates.Load())

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/video_generation" {
			http.Redirect(w, r, "/replay", http.StatusTemporaryRedirect)
			return
		}
		redirected.Add(1)
		_, _ = io.WriteString(w, `{"task_id":"duplicate"}`)
	}))
	defer redirect.Close()
	_, err = newTestProviderClient().Generate(t.Context(), RouteConfig{Protocol: AdapterMiniMaxVideo, BaseURL: redirect.URL, UpstreamModel: miniMaxModelH3}, GenerateInput{Messages: []Message{{Content: GeneratedAssetKindVideo}}})
	require.Error(t, err)
	assert.Zero(t, redirected.Load())
}

func TestMiniMaxVideoValidatesFramesReferencesAndModelConstraints(t *testing.T) {
	for _, options := range []map[string]interface{}{
		{volcengineVideoDurationOption: 3}, {volcengineVideoDurationOption: 6.5}, {volcengineVideoResolutionOption: "1080P"}, {miniMaxVideoRatioOption: valueAdaptive8B08E1F4},
		{miniMaxImageURLsInput: []string{miniMaxFixtureFirstImage, "last", "third"}, volcengineVideoImageRoleOption: miniMaxRoleFirstFrame},
		{miniMaxImageURLsInput: []string{miniMaxFixtureFirstImage}, volcengineVideoImageRoleOption: miniMaxRoleFirstFrame, "reference_video_urls": []string{"reference"}},
		{miniMaxImageURLsInput: strings.Split(strings.Repeat("image,", 10), ",")},
	} {
		_, err := buildMiniMaxVideoRequest(miniMaxModelH3, GenerateInput{Messages: []Message{{Content: GeneratedAssetKindVideo}}, Options: options})
		require.Error(t, err, "options=%v", options)
	}
	for _, options := range []map[string]interface{}{{volcengineVideoDurationOption: 4}, {volcengineVideoResolutionOption: "2K"}} {
		_, err := buildMiniMaxVideoRequest(miniMaxModelH3Max, GenerateInput{Messages: []Message{{Content: GeneratedAssetKindVideo}}, Options: options})
		require.Error(t, err)
	}
	payload, err := buildMiniMaxVideoRequest(miniMaxModelH3, GenerateInput{Messages: []Message{{Content: GeneratedAssetKindVideo}}, Options: map[string]interface{}{miniMaxImageURLsInput: []string{miniMaxFixtureFirstImage, "last"}, volcengineVideoDurationOption: 4, volcengineVideoResolutionOption: "2K"}})
	require.NoError(t, err)
	content, ok := payload[valueContent3EAD52FD].([]map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, miniMaxRoleFirstFrame, content[1][valueRoleE6F72470])
	assert.Equal(t, miniMaxRoleLastFrame, content[2][valueRoleE6F72470])
}
