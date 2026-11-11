package transport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

const nativeVideoTestCapabilities = `{"inputModalities":["text","image","video"]}`

func TestVideoTransmitsOriginalBytesAndTextOverGenerationAndStreams(t *testing.T) {
	for _, protocol := range []string{AdapterOpenAIResponses, AdapterOpenAIChatCompletions, AdapterMiniMaxChat, AdapterAnthropicMessages, AdapterMiniMaxAnthropic} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", protocol, streaming), func(t *testing.T) {
				captured := make(chan originalFileTestRequest, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					var body map[string]interface{}
					err := json.NewDecoder(request.Body).Decode(&body)
					captured <- originalFileTestRequest{body: body, err: err}
					response := nativeVideoTestResponse(protocol, streaming)
					w.Header().Set("Content-Type", "application/json")
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					_, _ = w.Write([]byte(response))
				}))
				defer server.Close()
				route := RouteConfig{Protocol: protocol, BaseURL: server.URL, UpstreamModel: nativeCountTestModel, ModelCapabilitiesJSON: nativeVideoTestCapabilities}
				input := GenerateInput{Messages: []Message{{Role: valueUserD301E989, Parts: []ContentPart{
					{Kind: ContentPartText, Text: "Describe the video."},
					{Kind: ContentPartVideo, MimeType: InputVideoMIMEQuickTime, Data: []byte{0, 1, 2, 255}},
				}}}}
				client := newTestProviderClient()
				var output *GenerateOutput
				var err error
				if streaming {
					output, err = client.GenerateStream(t.Context(), route, input, nil)
				} else {
					output, err = client.Generate(t.Context(), route, input)
				}
				require.NoError(t, err)
				require.Equal(t, "video received", output.Text)
				request := <-captured
				require.NoError(t, request.err)
				assertNativeVideoTestPayload(t, protocol, request.body)
			})
		}
	}
}

func nativeVideoTestResponse(protocol string, streaming bool) string {
	if protocol == AdapterOpenAIResponses {
		response := `{"id":"video-response","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"video received"}]}]}`
		if streaming {
			return "data: {\"type\":\"response.output_text.delta\",\"delta\":\"video received\"}\n\ndata: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
		}
		return response
	}
	if protocol == AdapterOpenAIChatCompletions || protocol == AdapterMiniMaxChat {
		if streaming {
			return "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"video received\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		}
		return `{"choices":[{"message":{"role":"assistant","content":"video received"},"finish_reason":"stop"}]}`
	}
	if streaming {
		return "data: {\"type\":\"message_start\",\"message\":{\"id\":\"video-message\"}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"video received\"}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_stop\"}\n\n"
	}
	return `{"id":"video-message","content":[{"type":"text","text":"video received"}],"stop_reason":"end_turn"}`
}

func assertNativeVideoTestPayload(t *testing.T, protocol string, payload map[string]interface{}) {
	t.Helper()
	key := EndpointMessages
	if protocol == AdapterOpenAIResponses {
		key = valueInput9B2BD505
	}
	messages := asSlice(payload[key])
	require.Len(t, messages, 1)
	content := asSlice(asMap(messages[0])["content"])
	require.Len(t, content, 2)
	require.Equal(t, "Describe the video.", asMap(content[0])["text"])
	video := asMap(content[1])
	switch protocol {
	case AdapterOpenAIResponses:
		require.Equal(t, "input_video", video[valueType5AC5AA71])
		require.Equal(t, "data:video/mov;base64,AAEC/w==", video[nativeVideoURLKey])
	case AdapterOpenAIChatCompletions, AdapterMiniMaxChat:
		require.Equal(t, nativeVideoURLKey, video[valueType5AC5AA71])
		require.Equal(t, "data:video/mov;base64,AAEC/w==", asMap(video[nativeVideoURLKey])[valueUrl5D7AD561])
	default:
		require.Equal(t, "video", video[valueType5AC5AA71])
		require.Equal(t, map[string]interface{}{valueType5AC5AA71: miniMaxImageBase64, nativeMediaTypeKey: InputVideoMIMEMOV, valueDataE4BFD03E: "AAEC/w=="}, asMap(video[nativeVideoSourceKey]))
	}
}

func TestVideoTokenCountingUsesTheSameNativeContent(t *testing.T) {
	for _, protocol := range []string{AdapterOpenAIResponses, AdapterAnthropicMessages, AdapterMiniMaxAnthropic} {
		t.Run(protocol, func(t *testing.T) {
			server, requests := nativeCountFixtureServer(t, protocol)
			base := server.URL + "/anthropic/v1"
			if protocol == AdapterOpenAIResponses {
				base = server.URL + "/v1"
			}
			route := RouteConfig{Protocol: protocol, BaseURL: base, APIKey: miniMaxFixtureAPIKey, HeadersJSON: `{"X-Fixture":"kept"}`, UpstreamModel: nativeCountTestModel, ModelCapabilitiesJSON: nativeVideoTestCapabilities}
			count, err := newTestProviderClient().CountInputTokens(t.Context(), route, GenerateInput{Messages: []Message{{Role: valueUserD301E989, Parts: []ContentPart{
				{Kind: ContentPartText, Text: "Describe the video."}, {Kind: ContentPartVideo, MimeType: InputVideoMIMEQuickTime, Data: []byte{0, 1, 2, 255}},
			}}}})
			require.NoError(t, err)
			require.Equal(t, int64(1500), count)
			assertNativeVideoTestPayload(t, protocol, <-requests)
		})
	}
}

func TestInvalidVideoNeverReachesGenerationStreamOrTokenHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	for _, test := range []struct {
		name, caps, mime, role, protocol string
		data                             []byte
	}{
		{"undeclared", "{}", InputVideoMIMEMP4, valueUserD301E989, AdapterOpenAIResponses, []byte{1}},
		{"unsupported format", nativeVideoTestCapabilities, "video/webm", valueUserD301E989, AdapterOpenAIResponses, []byte{1}},
		{"empty", nativeVideoTestCapabilities, InputVideoMIMEMP4, valueUserD301E989, AdapterOpenAIResponses, nil},
		{"oversized", nativeVideoTestCapabilities, InputVideoMIMEMP4, valueUserD301E989, AdapterOpenAIResponses, make([]byte, 20<<20+1)},
		{valueAssistantCAAB15A3, nativeVideoTestCapabilities, InputVideoMIMEMP4, valueAssistantCAAB15A3, AdapterOpenAIResponses, []byte{1}},
		{"wrong mime", nativeVideoTestCapabilities, "application/octet-stream", valueUserD301E989, AdapterOpenAIResponses, []byte{1}},
		{"unsupported transport", nativeVideoTestCapabilities, InputVideoMIMEMP4, valueUserD301E989, AdapterOpenRouterResponses, []byte{1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			route := RouteConfig{Protocol: test.protocol, BaseURL: server.URL, UpstreamModel: nativeCountTestModel, ModelCapabilitiesJSON: test.caps}
			input := GenerateInput{Messages: []Message{{Role: test.role, Parts: []ContentPart{{Kind: ContentPartVideo, MimeType: test.mime, Data: test.data}}}}}
			client := newTestProviderClient()
			_, err := client.Generate(t.Context(), route, input)
			require.ErrorIs(t, err, errUnsupportedNativeMediaInput)
			_, err = client.GenerateStream(t.Context(), route, input, nil)
			require.ErrorIs(t, err, errUnsupportedNativeMediaInput)
			_, err = client.CountInputTokens(t.Context(), route, input)
			require.Error(t, err)
		})
	}
	require.Zero(t, requests.Load())
}
