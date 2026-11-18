package transport

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nativeCountTestModel = "MiniMax-M3.1-Flash-Preview"

func TestMiniMaxAnthropicCounterMeasuresInputAboveGenerationCeiling(t *testing.T) {
	// C2-L2 counter-only receipts: the same 1,507,328-byte corpus returned
	// zero with a 64,000 output reserve, and 995,318 input tokens with reserve 1.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var payload map[string]interface{}
		if !assert.NoError(t, json.NewDecoder(request.Body).Decode(&payload)) {
			return
		}
		count := 0
		if toInt64(payload[valueMaxTokens8DDF2E0E]) == 1 {
			count = 995318
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]int{anthropicInputTokensKey: count}))
	}))
	t.Cleanup(server.Close)
	input := GenerateInput{Messages: []Message{{Role: valueUser72795C11, Content: "nonempty synthetic boundary input"}}, Options: map[string]interface{}{valueMaxTokens8DDF2E0E: 64000}}
	count, err := newTestProviderClient().CountInputTokens(t.Context(), RouteConfig{Protocol: AdapterMiniMaxAnthropic, BaseURL: server.URL, UpstreamModel: "MiniMax-M3", APIKey: miniMaxFixtureAPIKey}, input)
	require.NoError(t, err)
	require.EqualValues(t, 995318, count)
	require.Greater(t, count, int64(1048576-64000))
	generation, err := buildAnthropicRequestBody("MiniMax-M3", input, true)
	require.NoError(t, err)
	require.EqualValues(t, 64000, generation[valueMaxTokens8DDF2E0E], "the actual generation retains its own output reserve")
	require.EqualValues(t, 64000, input.Options[valueMaxTokens8DDF2E0E])
}

func TestMiniMaxInputTokenCountPreservesNativeImageAndTools(t *testing.T) {
	for _, protocol := range []string{AdapterOpenAIResponses, AdapterMiniMaxAnthropic, AdapterAnthropicMessages} {
		t.Run(protocol, func(t *testing.T) {
			server, requests := nativeCountFixtureServer(t, protocol)
			base := server.URL + "/anthropic/v1"
			if protocol == AdapterOpenAIResponses {
				base = server.URL + "/v1"
			}
			route := RouteConfig{Protocol: protocol, BaseURL: base, APIKey: miniMaxFixtureAPIKey, UpstreamModel: nativeCountTestModel, HeadersJSON: `{"X-Fixture":"kept"}`}
			image := []byte{1, 2, 3, 4, 5}
			input := GenerateInput{
				Messages: []Message{
					{Role: valueSystem730C6E5F, Content: "describe accurately"},
					{Role: valueUser72795C11, Parts: []ContentPart{{Kind: valueText53ED4E15, Text: "describe this"}, {Kind: ContentPartImage, MimeType: valueImagePng415383B9, Data: image}}},
				},
				Tools:   []ToolDefinition{{Name: "lookup", Description: "look up", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)}},
				Options: map[string]interface{}{valueMaxTokens8DDF2E0E: 2048, valueMaxOutputTokens5044A27A: 2048},
			}
			count, err := newTestProviderClient().CountInputTokens(t.Context(), route, input)
			require.NoError(t, err)
			assert.Equal(t, int64(1500), count)
			captured := <-requests
			assert.Equal(t, nativeCountTestModel, captured[valueModel2D31E8EB])
			require.NotEmpty(t, captured["tools"])
			assert.NotContains(t, captured, valueStream26D75EAE)
			assertNativeCountImage(t, protocol, captured, image)
		})
	}
}

func nativeCountFixtureServer(t *testing.T, protocol string) (*httptest.Server, <-chan map[string]interface{}) {
	t.Helper()
	requests := make(chan map[string]interface{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodPost, request.Method)
		path := "/anthropic/v1/messages/count_tokens"
		if protocol == AdapterOpenAIResponses {
			path = "/v1/responses/input_tokens"
		}
		assert.Equal(t, path, request.URL.Path)
		if protocol == AdapterAnthropicMessages {
			assert.Equal(t, miniMaxFixtureAPIKey, request.Header.Get("x-api-key"))
		} else {
			assert.Equal(t, "Bearer "+miniMaxFixtureAPIKey, request.Header.Get("Authorization"))
		}
		if protocol != AdapterOpenAIResponses {
			assert.Equal(t, anthropicVersion, request.Header.Get("anthropic-version"))
		}
		assert.Equal(t, "kept", request.Header.Get("X-Fixture"))
		var captured map[string]interface{}
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			t.Errorf("decode count request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- captured
		response := `{"usage":{"input_tokens":1200,"cache_creation_input_tokens":200,"cache_read_input_tokens":100}}`
		if protocol == AdapterOpenAIResponses {
			response = `{"object":"response.input_tokens","input_tokens":1500}`
		}
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func assertNativeCountImage(t *testing.T, protocol string, captured map[string]interface{}, image []byte) {
	t.Helper()
	if protocol == AdapterOpenAIResponses {
		assert.Equal(t, int64(2048), toInt64(captured[valueMaxOutputTokens5044A27A]))
		items := asSlice(captured[valueInputF8378501])
		require.NotEmpty(t, items)
		content := asSlice(asMap(items[len(items)-1])["content"])
		require.Len(t, content, 2)
		assert.Equal(t, "describe this", asMap(content[0])[valueText53ED4E15])
		assert.Equal(t, "input_image", asMap(content[1])[valueTypeF2B37301])
		assert.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(image), asMap(content[1])["image_url"])
		return
	}
	require.NotEmpty(t, captured[valueSystem730C6E5F])
	messages := asSlice(captured["messages"])
	require.Len(t, messages, 1)
	content := asSlice(asMap(messages[0])["content"])
	require.Len(t, content, 2)
	assert.Equal(t, "describe this", asMap(content[0])[valueText53ED4E15])
	source := asMap(asMap(content[1])["source"])
	assert.Equal(t, valueImagePng415383B9, source["media_type"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(image), source["data"])
}

func TestMiniMaxInputTokenCountRejectsInvalidEstimates(t *testing.T) {
	for _, response := range []string{
		`{}`, `{"input_tokens":1} {"input_tokens":2}`, `{"input_tokens":0}`, `{"input_tokens":-1}`, `{"input_tokens":1.5}`, `{"input_tokens":"20"}`,
		`{"input_tokens":1099511627777}`, `{"input_tokens":12,"base_resp":{"status_code":1002,"status_msg":"limited"}}`,
	} {
		t.Run(response, func(t *testing.T) {
			server, _ := miniMaxFixtureServer(t, "/v1/responses/input_tokens", response)
			_, err := newTestProviderClient().CountInputTokens(t.Context(), RouteConfig{
				Protocol: AdapterOpenAIResponses, BaseURL: server.URL, APIKey: miniMaxFixtureAPIKey, UpstreamModel: nativeCountTestModel,
			}, GenerateInput{Messages: []Message{{Role: valueUserD301E989, Content: "describe"}}})
			require.Error(t, err)
		})
	}
}

func TestMiniMaxInputTokenCountKeepsNativeThinkingReplay(t *testing.T) {
	server, requests := miniMaxFixtureServer(t, "/anthropic/v1/messages/count_tokens", `{"usage":{"input_tokens":500}}`)
	replay := encodeProviderReplayItems(AdapterMiniMaxAnthropic, []interface{}{
		map[string]interface{}{valueType5AC5AA71: "thinking", "thinking": "opaque reasoning", interactionKeySignature: interactionKeySignature},
		map[string]interface{}{valueType5AC5AA71: valueText1D495308, valueText1D495308: "previous response"},
	})
	_, err := newTestProviderClient().CountInputTokens(t.Context(), RouteConfig{Protocol: AdapterMiniMaxAnthropic, BaseURL: server.URL, UpstreamModel: nativeCountTestModel, APIKey: miniMaxFixtureAPIKey}, GenerateInput{Messages: []Message{
		{Role: valueUserD301E989, Content: "describe"}, {Role: valueAssistantCAAB15A3, Content: "previous response", ReplayStateJSON: replay},
	}})
	require.NoError(t, err)
	messages := asSlice((<-requests)[EndpointMessages])
	blocks := asSlice(asMap(messages[1])[valueContent03EE2C26])
	require.Len(t, blocks, 2)
	assert.Equal(t, "opaque reasoning", asMap(blocks[0])["thinking"])
	assert.Equal(t, interactionKeySignature, asMap(blocks[0])[interactionKeySignature])
}

func TestMiniMaxInputTokenCountUsesGenerationOutputReserveNearBoundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var payload map[string]interface{}
		if !assert.NoError(t, json.NewDecoder(request.Body).Decode(&payload)) {
			return
		}
		count := int64(0)
		if payload[valueMaxOutputTokens5044A27A] == float64(4096) {
			count = 995204
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{"object": "response.input_tokens", anthropicInputTokensKey: count}))
	}))
	t.Cleanup(server.Close)
	count, err := newTestProviderClient().CountInputTokens(t.Context(), RouteConfig{
		Protocol: AdapterOpenAIResponses, BaseURL: server.URL, APIKey: miniMaxFixtureAPIKey, UpstreamModel: nativeCountTestModel,
	}, GenerateInput{
		Messages: []Message{{Role: valueUserD301E989, Content: "non-empty boundary input"}},
		Options:  map[string]interface{}{valueMaxOutputTokens5044A27A: 4096},
	})
	require.NoError(t, err)
	require.Equal(t, int64(995204), count)
}

func TestMiniMaxUsageInputCountIncludesCacheAndRejectsEmptyTotal(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want int64
	}{
		{"cached-only", `{"usage":{"input_tokens":0,"cache_read_input_tokens":1500}}`, 1500},
		{"empty-total", `{"usage":{"input_tokens":0}}`, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			count, err := parseMiniMaxInputTokenCount([]byte(test.body), nil, AdapterMiniMaxAnthropic)
			if test.want == 0 {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, count)
		})
	}
}

func TestAnthropicNativeCounterUsesCountResponseAndPreservesInvalidReceipt(t *testing.T) {
	for _, protocol := range []string{AdapterAnthropicMessages, AdapterMiniMaxAnthropic} {
		t.Run(protocol, func(t *testing.T) {
			const body = `{"input_tokens":12345}`
			count, err := parseMiniMaxInputTokenCount([]byte(body), nil, protocol)
			require.NoError(t, err)
			require.Equal(t, int64(12345), count)
			for _, invalid := range []string{`{}`, `{"input_tokens":0}`, `{"input_tokens":-1,"usage":{"input_tokens":7}}`, `{"usage":{"input_tokens":0}}`} {
				debug := &UpstreamDebugSnapshot{Response: UpstreamDebugResponse{StatusCode: http.StatusOK, Body: invalid}}
				_, err = parseMiniMaxInputTokenCount([]byte(invalid), debug, protocol)
				var upstream *UpstreamError
				require.ErrorAs(t, err, &upstream)
				require.Same(t, debug, upstream.Debug)
				require.Equal(t, invalid, upstream.Body)
			}
		})
	}
}
