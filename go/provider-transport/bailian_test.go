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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	bailianFixtureKey      = "bailian-fixture-key"
	bailianFixturePrompt   = "generate a small blue house"
	bailianFixtureAudioURL = "https://cdn.example/audio.wav"
	bailianFixtureImageURL = "https://cdn.example/image.png"
	bailianFixtureTaskID   = "task-1"
	bailianFixturePending  = `{"output":{"task_id":"task-1","task_status":"PENDING"}}`
)

func bailianFixtureInput(options map[string]interface{}) GenerateInput {
	return GenerateInput{Messages: []Message{{Role: valueUserD301E989, Content: bailianFixturePrompt}}, Options: options}
}

func bailianFixtureRoute(server *httptest.Server, protocol, model string) RouteConfig {
	return RouteConfig{BaseURL: server.URL + "/api/v1", APIKey: bailianFixtureKey, Protocol: protocol, UpstreamModel: model}
}

func TestBailianNativeProtocols(t *testing.T) {
	tests := []struct {
		protocol, model, path, response string
		options                         map[string]interface{}
		kind                            string
	}{
		{AdapterBailianImage, bailianModelQwenImage3, bailianMultimodalPath, `{"request_id":"img-1","output":{"choices":[{"message":{"content":[{"image":"https://cdn.example/image.png"}]}}]},"usage":{"image_count":1,"input_tokens":4}}`, nil, ContentPartImage},
		{AdapterBailianSpeech, "qwen-audio-3.0-tts-plus", bailianSpeechPath, `{"output":{"audio":{"url":"https://cdn.example/speech.mp3","id":"speech-1","expires_at":1800000000},"finish_reason":"stop"},"usage":{"characters":23}}`, nil, GeneratedAssetKindAudio},
		{AdapterBailianAudio, bailianModelAudioNext, bailianSpeechPath, `{"output":{"audio":{"url":"https://cdn.example/audio.wav"},"finish_reason":"stop","extra_info":{"duration":12}},"usage":{"duration":12}}`, nil, GeneratedAssetKindAudio},
		{AdapterBailianMusic, bailianModelMusic, bailianMusicPath, `{"output":{"audio":{"url":"https://cdn.example/music.mp3"},"finish_reason":"stop","extra_info":{"lyrics":"song"}},"usage":{"duration":8}}`, nil, GeneratedAssetKindAudio},
		{AdapterBailianEmbedding, bailianModelTextEmbedding37, bailianEmbeddingPath, `{"output":{"embeddings":[{"text_index":0,"embedding":[0.1234567890123456789,1.0],"sparse_embedding":[{"index":42,"value":0.5,"token":"house"}]}]},"usage":{"total_tokens":7}}`, map[string]interface{}{"texts": []string{valueHello29F339F3}, "output_type": "dense&sparse"}, ""},
		{AdapterBailianEmbedding, bailianModelVLEmbedding3, bailianMultimodalEmbeddingPath, `{"output":{"embeddings":[{"type":"fusion","embedding":[0.5,0.6]}]},"usage":{"input_tokens":7,"image_tokens":10}}`, map[string]interface{}{"contents": []interface{}{map[string]interface{}{valueText1D495308: valueHello29F339F3}, map[string]interface{}{ContentPartImage: bailianFixtureImageURL}}, "enable_fusion": true}, ""},
		{AdapterBailianRerank, "qwen3.7-text-rerank", bailianRerankPath, `{"output":{"results":[{"index":0,"relevance_score":0.9}]},"usage":{"total_tokens":15}}`, map[string]interface{}{bailianDocuments: []interface{}{"house", "tree"}}, ""},
		{AdapterBailianRerank, bailianModelQwenRerank3, "/compatible-api/v1/reranks", `{"id":"rank-1","results":[{"index":1,"relevance_score":0.9}],"usage":{"total_tokens":15}}`, map[string]interface{}{bailianDocuments: []interface{}{"house", "tree"}}, ""},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			requests := make(chan map[string]interface{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := bailianNativeBase + test.path
				if strings.HasPrefix(test.path, "/compatible-api/") {
					path = test.path
				}
				assert.Equal(t, path, r.URL.Path)
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "Bearer "+bailianFixtureKey, r.Header.Get("Authorization"))
				assert.Empty(t, r.Header.Get(bailianAsyncHeader))
				var payload map[string]interface{}
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests <- payload
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()
			output, err := newTestProviderClient().Generate(t.Context(), bailianFixtureRoute(server, test.protocol, test.model), bailianFixtureInput(test.options))
			require.NoError(t, err)
			payload := <-requests
			assert.Equal(t, test.model, payload[valueModel2D31E8EB])
			assert.JSONEq(t, test.response, output.RawJSON)
			assertBailianNativeOutput(t, test.path, test.model, test.kind, payload, output)
		})
	}
}

func TestBailianTextStreamAndProtectedOptions(t *testing.T) {
	requests := make(chan map[string]interface{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/compatible-mode/v1/chat/completions", r.URL.Path)
		var payload map[string]interface{}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- payload
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"chat-1","choices":[{"delta":{"reasoning_content":"plan"}}]}`+"\n\n"+`data: {"choices":[{"delta":{"content":"answer","tool_calls":[{"index":0,"id":"call-1","function":{"name":"lookup","arguments":"{\"id\":9007199254740993}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":8,"completion_tokens":3}}`+"\n\n"+"data: [DONE]\n\n")
	}))
	defer server.Close()
	input := bailianFixtureInput(map[string]interface{}{valueThinking0F938ED2: true, "max_completion_tokens": 64, "enable_search": true, valueModel2D31E8EB: "bad", EndpointMessages: "bad"})
	input.DisableTools = true
	var text, reasoning string
	output, err := newTestProviderClient().GenerateStream(t.Context(), bailianFixtureRoute(server, AdapterBailianChat, "qwen3.8-max"), input, func(event GenerateStreamEvent) error {
		text += event.Delta
		if event.Reasoning != nil {
			reasoning += event.Reasoning.Text
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "answer", text)
	assert.Equal(t, "plan", reasoning)
	require.Len(t, output.ToolCalls, 1)
	assert.Contains(t, output.ToolCalls[0].ArgumentsJSON, "9007199254740993")
	payload := <-requests
	assert.Equal(t, "qwen3.8-max", payload[valueModel2D31E8EB])
	assert.Equal(t, true, payload["enable_thinking"])
	assert.EqualValues(t, 64, payload["max_completion_tokens"])
	assert.Equal(t, false, payload["enable_search"])
	assert.NotContains(t, payload, valueThinking0F938ED2)
	assert.NotContains(t, payload, valueMaxTokens8DDF2E0E)
}

func TestBailianAsyncArtifacts(t *testing.T) {
	tests := []struct {
		protocol, model, path, response, kind string
		options                               map[string]interface{}
	}{
		{AdapterBailianVideo, bailianModelWan3, bailianVideoPath, `{"output":{"task_id":"task-1","task_status":"SUCCEEDED","video_url":"https://cdn.example/video.mp4"},"usage":{"input_video_duration":3,"output_video_duration":8,"duration":11}}`, GeneratedAssetKindVideo, nil},
		{AdapterBailian3D, bailianModelTripo, bailian3DPath, `{"output":{"task_id":"task-1","task_status":"SUCCEEDED","results":[{"pbr_model_url":"https://cdn.example/pbr.glb","base_model_url":"https://cdn.example/base.glb","rendered_image_url":"https://cdn.example/preview.png"}]},"usage":{"3d_task_type":"text-to-3d","count":1}}`, "model_3d", nil},
		{AdapterBailianImage, bailianModelQwenImage3, bailianImagePath, `{"output":{"task_id":"task-1","task_status":"SUCCEEDED","results":[{"url":"https://cdn.example/image.png"}]},"usage":{"image_count":1}}`, ContentPartImage, map[string]interface{}{"async": true}},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			var creates, polls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					creates.Add(1)
					assert.Equal(t, bailianNativeBase+test.path, r.URL.Path)
					assert.Equal(t, "enable", r.Header.Get(bailianAsyncHeader))
					_, _ = io.WriteString(w, bailianFixturePending)
					return
				}
				polls.Add(1)
				assert.Equal(t, "/api/v1/tasks/task-1", r.URL.Path)
				assert.Empty(t, r.Header.Get(bailianAsyncHeader))
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()
			options := cloneMap(test.options)
			options[mediaPollIntervalOption] = 1
			output, err := newTestProviderClient().Generate(t.Context(), bailianFixtureRoute(server, test.protocol, test.model), bailianFixtureInput(options))
			require.NoError(t, err)
			assert.EqualValues(t, 1, creates.Load())
			assert.EqualValues(t, 1, polls.Load())
			assert.Equal(t, bailianFixtureTaskID, output.ResponseID)
			if test.kind == ContentPartImage {
				require.Len(t, output.GeneratedImages, 1)
			} else {
				require.NotEmpty(t, output.GeneratedAssets)
				assert.Equal(t, test.kind, output.GeneratedAssets[0].Kind)
				assert.Equal(t, bailianFixtureTaskID, output.GeneratedAssets[0].ExternalID)
			}
			if test.kind == GeneratedAssetKindVideo {
				assert.Contains(t, output.Usage.RawUsageJSON, `"output_video_duration":8`)
				assert.Zero(t, output.Usage.OutputTokens)
			}
		})
	}
}

func TestBailianAsyncFailurePreservesTaskAndDoesNotRecreate(t *testing.T) {
	for _, response := range []string{`{"code":"Throttling","message":"busy"}`, `{"output":{"task_id":"other","task_status":"SUCCEEDED"}}`, `{"output":{"task_id":"task-1","task_status":"UNKNOWN"}}`, `{"output":{"task_id":"task-1","task_status":"SUCCEEDED"}}`} {
		t.Run(response, func(t *testing.T) {
			var creates, polls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					creates.Add(1)
					_, _ = io.WriteString(w, bailianFixturePending)
					return
				}
				polls.Add(1)
				_, _ = io.WriteString(w, response)
			}))
			defer server.Close()
			_, err := newTestProviderClient().Generate(t.Context(), bailianFixtureRoute(server, AdapterBailianVideo, bailianModelWan3), bailianFixtureInput(map[string]interface{}{mediaPollIntervalOption: 1}))
			require.Error(t, err)
			assert.Contains(t, err.Error(), bailianFixtureTaskID)
			assert.EqualValues(t, 1, creates.Load())
			assert.EqualValues(t, 1, polls.Load())
		})
	}
}

func TestBailianCancellationPreservesTaskID(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = io.WriteString(w, bailianFixturePending)
			return
		}
		cancel()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	_, err := newTestProviderClient().Generate(ctx, bailianFixtureRoute(server, AdapterBailianVideo, bailianModelWan3), bailianFixtureInput(map[string]interface{}{mediaPollIntervalOption: 1}))
	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), bailianFixtureTaskID)
}

func TestBailianWorldCreateAndQuery(t *testing.T) {
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer "+bailianFixtureKey, r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get(bailianAsyncHeader))
		if r.Method == http.MethodPost {
			creates.Add(1)
			var payload map[string]interface{}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Equal(t, true, payload["async"])
			assert.Equal(t, "720p", payload["resolution"])
			assert.Equal(t, "/api/v2/apps/happyoyster-1.0-directing/openapi/v1/worlds", r.URL.Path)
			_, _ = io.WriteString(w, `{"code":0,"data":{"encryptedWorldId":"world/a+b","status":"generating"}}`)
			return
		}
		assert.Equal(t, "world/a+b", r.URL.Query().Get(bailianWorldID))
		_, _ = io.WriteString(w, `{"code":0,"data":{"encryptedWorldId":"world/a+b","status":"ready","firstFrame":"https://cdn.example/world.png"}}`)
	}))
	defer server.Close()
	output, err := newTestProviderClient().Generate(t.Context(), bailianFixtureRoute(server, AdapterBailianWorld, bailianModelWorldDirecting), bailianFixtureInput(map[string]interface{}{mediaPollIntervalOption: 1}))
	require.NoError(t, err)
	assert.EqualValues(t, 1, creates.Load())
	require.Len(t, output.GeneratedAssets, 1)
	assert.Equal(t, "world", output.GeneratedAssets[0].Kind)
	assert.Equal(t, "world/a+b", output.GeneratedAssets[0].ExternalID)
	assert.Empty(t, output.GeneratedAssets[0].URL)
}

func TestBailianFileTranscriptDownloadsWithoutCredentials(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/result" {
			assert.Empty(t, r.Header.Get("Authorization"))
			assert.Empty(t, r.Header.Get("X-Custom"))
			assert.Equal(t, "signed", r.URL.Query().Get("token"))
			_, _ = io.WriteString(w, `{"transcripts":[{"text":"hello","sentences":[{"begin_time":10,"end_time":200,"speaker_id":2}]}]}`)
			return
		}
		assert.Equal(t, "Bearer "+bailianFixtureKey, r.Header.Get("Authorization"))
		if r.Method == http.MethodPost {
			var payload map[string]interface{}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Equal(t, map[string]interface{}{}, payload[bailianParameters])
			_, _ = io.WriteString(w, bailianFixturePending)
			return
		}
		_, _ = io.WriteString(w, `{"output":{"task_id":"task-1","task_status":"SUCCEEDED","results":[{"subtask_status":"SUCCEEDED","transcription_url":"`+server.URL+`/result?token=signed"}]},"usage":{"duration":2}}`)
	}))
	defer server.Close()
	route := bailianFixtureRoute(server, AdapterBailianTranscription, "qwen-audio-3.1-asr-flash-filetrans")
	route.HeadersJSON = `{"X-Custom":"private"}`
	output, err := newTestProviderClient().Generate(t.Context(), route, GenerateInput{Options: map[string]interface{}{"audio_url": bailianFixtureAudioURL, mediaPollIntervalOption: 1}})
	require.NoError(t, err)
	assert.Equal(t, valueHello29F339F3, output.Text)
	require.Len(t, output.GeneratedAssets, 1)
	assert.Contains(t, output.GeneratedAssets[0].MetadataJSON, `"speaker_id":2`)
	assert.Zero(t, output.Usage.OutputTokens)
}

func TestBailianModelCatalogPaginatesAndClassifies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/models", r.URL.Path)
		assert.Equal(t, "Bearer "+bailianFixtureKey, r.Header.Get("Authorization"))
		if r.URL.Query().Get("page_no") == "1" {
			_, _ = io.WriteString(w, `{"success":true,"output":{"total":4,"page_no":1,"models":[{"model":"qwen3.8-max","provider":"alibaba","capabilities":["TG"]},{"model":"qwen-audio-3.1-tts-next","capabilities":["TTS"]}]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"output":{"total":4,"page_no":2,"models":[{"model":"happyoyster-1.0-directing","capabilities":["VG"]},{"model":"qwen3-asr-flash-realtime","capabilities":["ASR"]}]}}`)
	}))
	defer server.Close()
	models, err := newTestProviderClient().ListModels(t.Context(), bailianFixtureRoute(server, AdapterBailianChat, ""))
	require.NoError(t, err)
	require.Len(t, models, 3)
	assert.Equal(t, AdapterBailianAudio, models[1].SuggestedProtocol)
	assert.JSONEq(t, `["audio_gen"]`, models[1].KindsJSON)
	assert.Equal(t, AdapterBailianWorld, models[2].SuggestedProtocol)
}

func TestBailianBusinessErrorsAndMediaRedirects(t *testing.T) {
	for _, protocol := range []string{AdapterBailianChat, AdapterBailianImage} {
		t.Run(protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"code":"InvalidApiKey","message":"invalid credential"}`)
			}))
			defer server.Close()
			_, err := newTestProviderClient().Generate(t.Context(), bailianFixtureRoute(server, protocol, bailianModelQwenImage3), bailianFixtureInput(nil))
			var upstream *UpstreamError
			require.ErrorAs(t, err, &upstream)
			assert.Equal(t, http.StatusUnauthorized, upstream.StatusCode)
		})
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/other")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := newTestProviderClient().Generate(t.Context(), bailianFixtureRoute(server, AdapterBailianVideo, bailianModelWan3), bailianFixtureInput(nil))
	require.Error(t, err)
	assert.EqualValues(t, 1, calls.Load())
}

func TestBailianParameterBoundaries(t *testing.T) {
	tests := []struct {
		protocol, model string
		options         map[string]interface{}
	}{
		{AdapterBailianVideo, bailianModelWan3, map[string]interface{}{"duration": -1.5}},
		{AdapterBailianVideo, bailianModelWan3, map[string]interface{}{"media": []interface{}{map[string]interface{}{valueType5AC5AA71: miniMaxRoleLastFrame, valueUrl5D7AD561: bailianFixtureImageURL}}}},
		{AdapterBailianVideo, "wan2.7-t2v", map[string]interface{}{"image_urls": []string{bailianFixtureImageURL}}},
		{AdapterBailianImage, bailianModelQwenImage3, map[string]interface{}{"n": 7}},
		{AdapterBailianImage, "wan2.7-image-pro", map[string]interface{}{valueSize7A4CF6CA: "4K", "image_urls": []string{bailianFixtureImageURL}}},
		{AdapterBailian3D, bailianModelTripo, map[string]interface{}{ContentPartImage: bailianFixtureImageURL}},
		{AdapterBailianEmbedding, bailianModelTextEmbedding37, map[string]interface{}{"dimension": 64}},
		{AdapterBailianAudio, bailianModelAudioNext, map[string]interface{}{miniMaxFormatKey: bailianFormatOpus}},
		{AdapterBailianSpeech, "qwen-audio-3.0-tts-plus", map[string]interface{}{miniMaxFormatKey: bailianFormatOpus, "sample_rate": 22050}},
		{AdapterBailianTranscription, "qwen-audio-3.1-asr-flash-filetrans", map[string]interface{}{"audio_data": "data:audio/wav;base64,WA=="}},
	}
	for _, test := range tests {
		t.Run(test.protocol+test.model, func(t *testing.T) {
			_, err := buildBailianRequest(RouteConfig{Protocol: test.protocol, UpstreamModel: test.model}, bailianFixtureInput(test.options))
			require.ErrorIs(t, err, errBailianInput)
		})
	}
	_, err := buildBailianWorldRequest(bailianModelWorldAdventure, bailianFixtureInput(nil))
	require.ErrorIs(t, err, errBailianInput)
	_, err = decodeBailianJSON([]byte(`{"output":{}} {}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, errBailianResponse)
}

func assertBailianNativeOutput(t *testing.T, path, model, kind string, payload map[string]interface{}, output *GenerateOutput) {
	t.Helper()
	if kind == ContentPartImage {
		require.Len(t, output.GeneratedImages, 1)
		assert.Equal(t, bailianFixtureImageURL, output.GeneratedImages[0].URL)
	}
	if kind == GeneratedAssetKindAudio {
		require.Len(t, output.GeneratedAssets, 1)
		assert.Equal(t, kind, output.GeneratedAssets[0].Kind)
		assert.Zero(t, output.Usage.OutputTokens)
		assert.NotEmpty(t, output.Usage.RawUsageJSON)
	}
	if path == bailianEmbeddingPath {
		assert.Equal(t, []interface{}{valueHello29F339F3}, asMap(payload["input"])["texts"])
		assert.Contains(t, output.RawJSON, "0.1234567890123456789")
		assert.EqualValues(t, 7, output.Usage.InputTokens)
	}
	if model == bailianModelQwenRerank3 {
		assert.Contains(t, payload, bailianDocuments)
		assert.NotContains(t, payload, "input")
	}
}

func TestBailianStructuredThinkingAndNativeHeaders(t *testing.T) {
	for _, mode := range []string{valueEnabledCEB1C3B3, "disabled"} {
		payload := map[string]interface{}{}
		normalizeBailianChatOptions(payload, GenerateInput{Options: map[string]interface{}{valueThinking0F938ED2: map[string]interface{}{valueType5AC5AA71: mode}}})
		assert.Equal(t, mode == valueEnabledCEB1C3B3, payload["enable_thinking"])
	}
	headers, err := bailianRequestHeaders(`{"x-dashscope-async":"old","x-dashscope-sse":"enable","X-Custom":"keep"}`, false)
	require.NoError(t, err)
	assert.JSONEq(t, `{"X-Custom":"keep"}`, headers)
	headers, err = bailianRequestHeaders("null", true)
	require.NoError(t, err)
	assert.JSONEq(t, `{"X-DashScope-Async":"enable"}`, headers)
}

func TestBailianShortAudioRecognition(t *testing.T) {
	requests := make(chan map[string]interface{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/compatible-mode/v1/chat/completions", r.URL.Path)
		assert.Empty(t, r.Header.Get(bailianAsyncHeader))
		var payload map[string]interface{}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- payload
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"recognized speech"}}],"usage":{"prompt_tokens":7,"completion_tokens":3,"seconds":2}}`)
	}))
	defer server.Close()
	output, err := newTestProviderClient().Generate(t.Context(), bailianFixtureRoute(server, AdapterBailianTranscription, "qwen3-asr-flash"), GenerateInput{Options: map[string]interface{}{"audio_url": bailianFixtureAudioURL}})
	require.NoError(t, err)
	assert.Equal(t, "recognized speech", output.Text)
	payload := <-requests
	messages := asSlice(payload[EndpointMessages])
	require.Len(t, messages, 1)
	content := asSlice(asMap(messages[0])[valueContent03EE2C26])
	assert.Equal(t, bailianFixtureAudioURL, asMap(asMap(content[0])["input_audio"])[valueDataE4BFD03E])
}

func TestBailianVectorResultsRejectCorruptIndexesAndValues(t *testing.T) {
	for _, response := range []string{
		`{"output":{"embeddings":[{"text_index":1,"embedding":[0.5]}]}}`,
		`{"output":{"embeddings":[{"text_index":0,"embedding":["wrong"]}]}}`,
		`{"output":{"embeddings":[]}}`,
	} {
		parsed, err := decodeBailianJSON([]byte(response))
		require.NoError(t, err)
		_, err = newTestProviderClient().parseBailianOutput(t.Context(), RouteConfig{Protocol: AdapterBailianEmbedding, UpstreamModel: bailianModelTextEmbedding37}, bailianFixtureInput(nil), parsed)
		require.ErrorIs(t, err, errBailianResponse)
	}
}
