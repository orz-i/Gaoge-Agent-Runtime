package transport

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

const nativeMediaTestMIMEWAV = "audio/wav"
const nativeMediaTestModel = "media-model-fixture"

func TestGoogleInteractionsTransmitsMixedNativeInputs(t *testing.T) {
	type capturedRequest struct {
		path string
		body map[string]interface{}
		err  error
	}
	captured := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var received map[string]interface{}
		err := json.NewDecoder(request.Body).Decode(&received)
		captured <- capturedRequest{path: request.URL.Path, body: received, err: err}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"it_media","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"The media arrived."}]}]}`))
	}))
	defer server.Close()
	parts := []ContentPart{{Kind: ContentPartText, Text: "Describe these references."},
		{Kind: ContentPartImage, MimeType: "image/png", Data: []byte{1, 2, 3}},
		{Kind: ContentPartAudio, MimeType: nativeMediaTestMIMEWAV, Data: []byte{4, 5, 6}},
		{Kind: ContentPartVideo, MimeType: "video/mp4", Data: []byte{7, 8, 9}},
		{Kind: ContentPartFile, Text: "Extracted brief content."}}
	output, err := newTestProviderClient().Generate(t.Context(), RouteConfig{Protocol: AdapterGoogleInteractions, BaseURL: server.URL, UpstreamModel: nativeMediaTestModel}, GenerateInput{Messages: []Message{{Role: valueUserD301E989, Parts: parts}}})
	require.NoError(t, err)
	require.Equal(t, "The media arrived.", output.Text)
	received := <-captured
	require.NoError(t, received.err)
	require.Equal(t, "/v1/interactions", received.path)
	input := interactionMapSlice(received.body[interactionKeyInput])
	require.Len(t, input, 1)
	content := interactionMapSlice(asMap(input[0])[interactionKeyContent])
	require.Len(t, content, 5)
	for index := 1; index <= 3; index++ {
		part := asMap(content[index])
		require.Equal(t, parts[index].Kind, part[interactionKeyType])
		require.Equal(t, parts[index].MimeType, part[interactionKeyMimeType])
		require.Equal(t, base64.StdEncoding.EncodeToString(parts[index].Data), part[interactionKeyData])
	}
	require.Equal(t, parts[4].Text, asMap(content[4])[interactionKeyText])
}

func TestUnsupportedMediaFailsBeforeAnyProviderHTTPRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	for _, protocol := range []string{AdapterOpenAIResponses, AdapterOpenAIChatCompletions, AdapterAnthropicMessages, AdapterMiniMaxChat} {
		for _, kind := range []string{ContentPartAudio, ContentPartVideo} {
			input := GenerateInput{Messages: []Message{{Role: valueUserD301E989, Parts: []ContentPart{{Kind: kind, MimeType: kind + "/mp4", Data: []byte("media")}}}}}
			route := RouteConfig{Protocol: protocol, BaseURL: server.URL, UpstreamModel: nativeMediaTestModel}
			_, err := newTestProviderClient().Generate(t.Context(), route, input)
			require.ErrorContains(t, err, "invalid "+kind+" input")
			_, err = newTestProviderClient().GenerateStream(t.Context(), route, input, func(GenerateStreamEvent) error { return nil })
			require.ErrorContains(t, err, "invalid "+kind+" input")
		}
	}
	require.Zero(t, requests.Load())
}

func TestGoogleRejectsOversizedInlinePayloadBeforeHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	input := GenerateInput{Messages: []Message{{Role: valueUserD301E989, Parts: []ContentPart{{Kind: ContentPartVideo, MimeType: "video/mp4", Data: make([]byte, 15_000_001)}}}}}
	route := RouteConfig{Protocol: AdapterGoogleInteractions, BaseURL: server.URL, UpstreamModel: nativeMediaTestModel}
	_, err := newTestProviderClient().Generate(t.Context(), route, input)
	require.ErrorContains(t, err, "exceeds 20 MB")
	_, err = newTestProviderClient().GenerateStream(t.Context(), route, input, func(GenerateStreamEvent) error { return nil })
	require.ErrorContains(t, err, "exceeds 20 MB")
	require.Zero(t, requests.Load())
}
