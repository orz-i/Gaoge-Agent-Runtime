package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type originalFileTestRequest struct {
	body map[string]interface{}
	err  error
}

func newOriginalFileTestServer(protocol string) (*httptest.Server, <-chan originalFileTestRequest) {
	captured := make(chan originalFileTestRequest, 1)
	response := `{"id":"resp-file","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"summary"}]}]}`
	if protocol == AdapterOpenAIChatCompletions {
		response = `{"choices":[{"message":{"role":"assistant","content":"summary"},"finish_reason":"stop"}]}`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]interface{}
		err := json.NewDecoder(request.Body).Decode(&body)
		captured <- originalFileTestRequest{body: body, err: err}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	return server, captured
}

func TestOpenAITransmitsOriginalFilesAlongsideText(t *testing.T) {
	for _, test := range []struct{ protocol, mime, name string }{
		{AdapterOpenAIResponses, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "summary.docx"},
		{AdapterOpenAIChatCompletions, PDFInputMIME, "summary.pdf"},
	} {
		t.Run(test.protocol, func(t *testing.T) {
			server, captured := newOriginalFileTestServer(test.protocol)
			defer server.Close()
			file := ContentPart{Kind: ContentPartFile, MimeType: test.mime, FileName: test.name, Data: []byte("original document")}
			input := GenerateInput{Messages: []Message{{Role: valueUserD301E989, Parts: []ContentPart{
				{Kind: ContentPartText, Text: "Summarize this document."}, file,
				{Kind: ContentPartFile, Text: "Other extracted content."},
			}}}}
			output, err := newTestProviderClient().Generate(t.Context(), RouteConfig{Protocol: test.protocol, BaseURL: server.URL, UpstreamModel: nativeMediaTestModel}, input)
			require.NoError(t, err)
			require.Equal(t, "summary", output.Text)
			received := <-captured
			require.NoError(t, received.err)
			key := "input"
			if test.protocol == AdapterOpenAIChatCompletions {
				key = EndpointMessages
			}
			messages := interactionMapSlice(received.body[key])
			content := interactionMapSlice(asMap(messages[0])["content"])
			require.Len(t, content, 3)
			require.Equal(t, input.Messages[0].Parts[0].Text, asMap(content[0])["text"])
			require.Equal(t, input.Messages[0].Parts[2].Text, asMap(content[2])["text"])
			original := asMap(content[1])
			if test.protocol == AdapterOpenAIResponses {
				require.Equal(t, "input_file", original["type"])
			} else {
				require.Equal(t, ContentPartFile, original["type"])
				original = asMap(original[ContentPartFile])
			}
			require.Equal(t, test.name, original["filename"])
			require.Equal(t, "data:"+test.mime+";base64,b3JpZ2luYWwgZG9jdW1lbnQ=", original["file_data"])
		})
	}
}

func TestUnsupportedOriginalFilesFailBeforeHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	for _, test := range []struct{ protocol, mime string }{
		{AdapterOpenAIChatCompletions, "application/msword"},
		{AdapterAnthropicMessages, PDFInputMIME},
		{AdapterGoogleInteractions, PDFInputMIME},
		{AdapterOpenAIResponses, "application/x-executable"},
	} {
		file := ContentPart{Kind: ContentPartFile, MimeType: test.mime, FileName: "original", Data: []byte("document")}
		input := GenerateInput{Messages: []Message{{Role: valueUserD301E989, Parts: []ContentPart{file}}}}
		route := RouteConfig{Protocol: test.protocol, BaseURL: server.URL, UpstreamModel: nativeMediaTestModel}
		_, err := newTestProviderClient().Generate(t.Context(), route, input)
		require.ErrorContains(t, err, "invalid file input")
		_, err = newTestProviderClient().GenerateStream(t.Context(), route, input, func(GenerateStreamEvent) error { return nil })
		require.ErrorContains(t, err, "invalid file input")
	}
	require.Zero(t, requests.Load())
}
