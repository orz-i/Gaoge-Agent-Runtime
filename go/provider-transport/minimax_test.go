package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	miniMaxFixtureMessagesPath        = "/anthropic/v1/messages"
	miniMaxFixtureContentBlockStopOne = "data: {\"type\":\"content_block_stop\",\"index\":1}"
	miniMaxFixtureAPIKey              = "fixture-key"
	miniMaxFixtureSpeechPrompt        = "speak"
	miniMaxFixtureCoverPrompt         = "a cover in a new style"
	miniMaxFixturePrompt              = "create"
	miniMaxFixtureFirstImage          = "first"
)

func miniMaxFixtureServer(t *testing.T, path, response string) (*httptest.Server, <-chan map[string]interface{}) {
	t.Helper()
	requests := make(chan map[string]interface{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, path, r.URL.Path)
		assert.Equal(t, "Bearer fixture-key", r.Header.Get("Authorization"))
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- payload
		w.Header().Set("Content-Type", valueApplicationJson71FA0DF4)
		if strings.HasPrefix(response, "data:") {
			w.Header().Set("Content-Type", mediaFixtureStreamMIME)
		}
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func TestMiniMaxNativeGenerationProtocols(t *testing.T) {
	tests := []struct {
		protocol, model, path, response string
		options                         map[string]interface{}
	}{
		{AdapterMiniMaxChat, miniMaxModelM3, "/v1/chat/completions", `{"id":"text-1","choices":[{"message":{"content":"answer","reasoning_content":"plan","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]}}],"usage":{"prompt_tokens":12,"completion_tokens":8},"base_resp":{"status_code":0}}`, map[string]interface{}{valueThinking0F938ED2: true, valueMaxCompletionTokensF93E57D2: 128}},
		{AdapterMiniMaxAnthropic, miniMaxModelM3, miniMaxFixtureMessagesPath, `{"id":"text-2","content":[{"type":"thinking","thinking":"plan","signature":"signature"},{"type":"text","text":"answer"},{"type":"tool_use","id":"call-1","name":"lookup","input":{"id":9007199254740993}}],"stop_reason":"tool_use","usage":{"input_tokens":12,"output_tokens":8}}`, map[string]interface{}{valueThinking0F938ED2: true}},
		{AdapterMiniMaxImage, miniMaxModelImage01, miniMaxImagePath, `{"id":"image-1","data":{"image_urls":["https://cdn.example/image.png"]},"base_resp":{"status_code":0}}`, nil},
		{AdapterMiniMaxSpeech, miniMaxModelSpeech28HD, miniMaxSpeechPath, `{"trace_id":"speech-1","data":{"audio":"49443301","status":2},"extra_info":{"audio_length":1250,"audio_format":"mp3","usage_characters":6},"base_resp":{"status_code":0}}`, map[string]interface{}{miniMaxVoiceSettingOption: map[string]interface{}{"speed": 1.2}}},
		{AdapterMiniMaxMusic, miniMaxModelMusic30, miniMaxMusicPath, `{"trace_id":"music-1","data":{"audio":"https://cdn.example/music.wav","status":2},"extra_info":{"music_duration":25000},"base_resp":{"status_code":0}}`, map[string]interface{}{valueOutputFormatA54B283B: valueUrl5D7AD561, miniMaxAudioSettingOption: map[string]interface{}{miniMaxFormatKey: miniMaxFormatWAV}}},
	}
	for _, test := range tests {
		t.Run(test.protocol, func(t *testing.T) {
			server, requests := miniMaxFixtureServer(t, test.path, test.response)
			options := cloneMap(test.options)
			options[valueModel2D31E8EB], options[valueStream1E52954F], options[valuePrompt9071DA16] = "wrong-model", true, "wrong prompt"
			output, err := newTestProviderClient().Generate(t.Context(), RouteConfig{
				Protocol: test.protocol, BaseURL: server.URL + "/v1", APIKey: miniMaxFixtureAPIKey, UpstreamModel: test.model,
			}, GenerateInput{Messages: []Message{{Role: valueUser72795C11, Content: miniMaxFixturePrompt}}, Options: options})
			require.NoError(t, err)
			require.NotNil(t, output.Debug)
			require.NotEmpty(t, output.RawJSON)
			payload := <-requests
			assert.Equal(t, test.model, payload[valueModel2D31E8EB])
			if test.protocol != AdapterMiniMaxImage {
				assert.Equal(t, false, payload[valueStream1E52954F], test.protocol)
			}
			assertMiniMaxNativeGeneration(t, test.protocol, payload, output)
		})
	}
}

func TestMiniMaxChatStreamPreservesReasoningAndToolCalls(t *testing.T) {
	response := strings.Join([]string{
		`data: {"id":"stream-1","choices":[{"delta":{"reasoning_content":"plan"}}]}`,
		`data: {"choices":[{"delta":{"content":"answer"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":8}}`,
		valueDataDONE26000752, "",
	}, "\n\n")
	server, requests := miniMaxFixtureServer(t, "/v1/chat/completions", response)
	var text, reasoning string
	output, err := newTestProviderClient().GenerateStream(t.Context(), RouteConfig{
		Protocol: AdapterMiniMaxChat, BaseURL: server.URL, APIKey: miniMaxFixtureAPIKey, UpstreamModel: miniMaxModelM3,
	}, GenerateInput{Messages: []Message{{Role: valueUser72795C11, Content: testInteractionToolName}}}, func(event GenerateStreamEvent) error {
		text += event.Delta
		if event.Reasoning != nil {
			reasoning += event.Reasoning.Text
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, valueAnswerEB4FB1BC, text)
	assert.Equal(t, "plan", reasoning)
	assert.Equal(t, true, (<-requests)[valueStream1E52954F])
	require.Len(t, output.ToolCalls, 1)
	assert.JSONEq(t, `{"q":"x"}`, output.ToolCalls[0].ArgumentsJSON)
	assert.EqualValues(t, 8, output.Usage.OutputTokens)
}

func TestMiniMaxBusinessErrorsAreNotSuccessfulHTTP200Responses(t *testing.T) {
	for _, protocol := range []string{AdapterMiniMaxChat, AdapterMiniMaxImage, AdapterMiniMaxSpeech, AdapterMiniMaxMusic} {
		t.Run(protocol, func(t *testing.T) {
			server, _ := miniMaxFixtureServer(t, map[string]string{
				AdapterMiniMaxChat: "/v1/chat/completions", AdapterMiniMaxImage: miniMaxImagePath, AdapterMiniMaxSpeech: miniMaxSpeechPath, AdapterMiniMaxMusic: miniMaxMusicPath,
			}[protocol], `{"base_resp":{"status_code":2049,"status_msg":"invalid key"}}`)
			_, err := newTestProviderClient().Generate(t.Context(), RouteConfig{Protocol: protocol, BaseURL: server.URL, APIKey: miniMaxFixtureAPIKey, UpstreamModel: valueModel2D31E8EB}, GenerateInput{Messages: []Message{{Role: valueUser72795C11, Content: miniMaxFixturePrompt}}})
			var upstream *UpstreamError
			require.ErrorAs(t, err, &upstream)
			assert.Equal(t, http.StatusUnauthorized, upstream.StatusCode)
			assert.Contains(t, upstream.Message, "2049")
			require.NotNil(t, upstream.Debug)
		})
	}
	for code, status := range map[int]int{1008: 402, 1002: 429, 1026: 422, 1039: 400, 1024: 502} {
		var upstream *UpstreamError
		err := miniMaxResponseError(map[string]interface{}{"base_resp": map[string]interface{}{"status_code": code}}, nil, nil)
		require.ErrorAs(t, err, &upstream)
		assert.Equal(t, status, upstream.StatusCode)
	}
}

func TestMiniMaxCatalogRequiresNoUndocumentedModelsRequest(t *testing.T) {
	client := newTestProviderClient()
	models, err := client.ListModels(t.Context(), RouteConfig{Protocol: AdapterMiniMaxChat, BaseURL: "http://127.0.0.1:1"})
	require.NoError(t, err)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		assert.Equal(t, "minimax", model.OwnedBy)
		assert.NotContains(t, model.ID, "-free")
		ids = append(ids, model.ID)
	}
	assert.Subset(t, ids, []string{miniMaxModelM3, miniMaxModelH3, miniMaxModelImage01, miniMaxModelSpeech28HD, miniMaxModelMusic30, miniMaxModelMusicCover})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.ListModels(ctx, RouteConfig{Protocol: AdapterMiniMaxMusic})
	require.ErrorIs(t, err, context.Canceled)
	for _, protocol := range []string{AdapterMiniMaxImage, AdapterMiniMaxVideo, AdapterMiniMaxSpeech, AdapterMiniMaxMusic} {
		_, err = client.GenerateStream(t.Context(), RouteConfig{Protocol: protocol}, GenerateInput{}, nil)
		require.ErrorIs(t, err, ErrUnsupportedStream)
	}
}

func TestMiniMaxRouteNormalizesVersionAndAnthropicSuffixes(t *testing.T) {
	for _, suffix := range []string{"", "/v1", "/v2", "/anthropic", "/anthropic/v1/"} {
		route := normalizeMiniMaxRoute(RouteConfig{BaseURL: "https://proxy.example/minimax" + suffix}, AdapterMiniMaxAnthropic)
		assert.Equal(t, "https://proxy.example/minimax/anthropic", route.BaseURL)
		assert.Equal(t, EndpointMessages, route.Endpoint)
		route = normalizeMiniMaxRoute(RouteConfig{BaseURL: route.BaseURL}, AdapterMiniMaxImage)
		assert.Equal(t, "https://proxy.example/minimax", route.BaseURL)
	}
	assert.Equal(t, miniMaxDefaultAPIBase, normalizeMiniMaxRoute(RouteConfig{}, AdapterMiniMaxChat).BaseURL)
}

func assertMiniMaxNativeGeneration(t *testing.T, protocol string, payload map[string]interface{}, output *GenerateOutput) {
	t.Helper()
	switch protocol {
	case AdapterMiniMaxChat, AdapterMiniMaxAnthropic:
		assert.Equal(t, valueAnswerEB4FB1BC, output.Text)
		require.NotNil(t, output.Reasoning)
		assert.Equal(t, "plan", output.Reasoning.Text)
		require.Len(t, output.ToolCalls, 1)
		assert.Equal(t, testInteractionToolName, output.ToolCalls[0].ToolName)
		assert.EqualValues(t, 12, output.Usage.InputTokens)
		assert.Equal(t, map[string]interface{}{valueTypeF2B37301: valueAdaptive8B08E1F4}, payload[valueThinking0F938ED2])
		if protocol == AdapterMiniMaxChat {
			assert.Equal(t, true, payload["reasoning_split"])
			assert.EqualValues(t, 128, payload[valueMaxCompletionTokensF93E57D2])
		} else {
			blocks := decodeProviderReplayItems(output.ReplayStateJSON, protocol)
			require.Len(t, blocks, 3)
			assert.Equal(t, interactionKeySignature, blocks[0][interactionKeySignature])
			assert.Equal(t, json.Number("9007199254740993"), asMap(blocks[2][valueInputF8378501])["id"])
			assert.Equal(t, `{"id":9007199254740993}`, output.ToolCalls[0].ArgumentsJSON)
		}
	case AdapterMiniMaxImage:
		assert.Nil(t, payload[valueStream1E52954F])
		assert.Equal(t, miniMaxFixturePrompt, payload[valuePrompt9071DA16])
		require.Len(t, output.GeneratedImages, 1)
		assert.Equal(t, "https://cdn.example/image.png", output.GeneratedImages[0].URL)
	case AdapterMiniMaxSpeech, AdapterMiniMaxMusic:
		require.Len(t, output.GeneratedAssets, 1)
		asset := output.GeneratedAssets[0]
		assert.Equal(t, GeneratedAssetKindAudio, asset.Kind)
		assert.Zero(t, output.Usage.InputTokens)
		assert.Zero(t, output.Usage.OutputTokens)
		assert.NotEmpty(t, output.Usage.RawUsageJSON)
		if protocol == AdapterMiniMaxSpeech {
			assert.Equal(t, base64.StdEncoding.EncodeToString([]byte{'I', 'D', '3', 1}), asset.B64JSON)
			assert.Equal(t, "audio/mpeg", asset.MIMEType)
			assert.JSONEq(t, `{"durationSeconds":1.25,"source":"provider"}`, asset.MetadataJSON)
			assert.Equal(t, "male-qn-qingse", asMap(payload[miniMaxVoiceSettingOption])["voice_id"])
			assert.InDelta(t, 1.2, asMap(payload[miniMaxVoiceSettingOption])["speed"], 0.000001)
		} else {
			assert.Equal(t, "https://cdn.example/music.wav", asset.URL)
			assert.Equal(t, "audio/wav", asset.MIMEType)
			assert.Equal(t, true, payload["lyrics_optimizer"])
			assert.JSONEq(t, `{"durationSeconds":25,"source":"provider"}`, asset.MetadataJSON)
		}
	}
}

func TestMiniMaxChatStreamReportsBusinessError(t *testing.T) {
	server, _ := miniMaxFixtureServer(t, "/v1/chat/completions", "data: {\"base_resp\":{\"status_code\":1008,\"status_msg\":\"insufficient balance\"}}\n\n")
	_, err := newTestProviderClient().GenerateStream(t.Context(), RouteConfig{Protocol: AdapterMiniMaxChat, BaseURL: server.URL, APIKey: miniMaxFixtureAPIKey, UpstreamModel: miniMaxModelM3}, GenerateInput{Messages: []Message{{Content: miniMaxFixturePrompt}}}, nil)
	var upstream *UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusPaymentRequired, upstream.StatusCode)
}
