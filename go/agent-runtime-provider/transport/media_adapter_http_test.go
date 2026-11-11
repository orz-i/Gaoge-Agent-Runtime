package transport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	mediaFixtureTestHost        = "media-adapter.invalid"
	mediaFixtureGenerationsPath = "/v1/images/generations"
	mediaFixtureEditsPath       = "/v1/images/edits"
	mediaFixtureOpenAIModel     = "gpt-image-1"
	mediaFixtureGeminiModel     = "gemini-2.5-flash-image"
	mediaFixtureJSONMIME        = "application/json"
	mediaFixtureStreamMIME      = "text/event-stream"
	mediaFixturePNGMIME         = "image/png"
	mediaFixtureJPEGMIME        = "image/jpeg"
	mediaFixtureUserRole        = "user"
	mediaFixtureDataKey         = "data"
	mediaFixtureTypeKey         = "type"
	mediaFixtureImageURLType    = "image_url"
	mediaFixtureReferenceRole   = "reference_image"
	mediaFixtureFirstFrameRole  = "first_frame"
	mediaFixtureLastFrameRole   = "last_frame"
	mediaFixtureReviewed        = "镜头 01｜“阿岚”在雨夜回头；参考 @Image1（身份），@参考_乙（空间），@Image3（道具）。\n\n" +
		"对白：\"别动那盏灯\"；保留原文 C:\\道具\\灯.json 与字面量 \\n。\r\n" +
		"\t窗上写着 <灯>&影；色标 e\u0301 / é，停顿\u2028继续\u2029收束 🎬。"
	mediaFixtureNegative = "不要新增人物；不改变 @Image1 的脸或 @参考_乙 的格局。\n\n" +
		"不出现 \"广告\"、字幕或 C:\\临时\\图.png；保持中文，不翻译。"
	mediaFixtureRevised = "供应商返回的独立修订：\"保留灯光\"。\n这不是用户审阅的原稿。"
)

type mediaFixtureHTTPResponse struct {
	statusCode  int
	contentType string
	body        string
}

type mediaFixtureHTTPRequest struct {
	method      string
	path        string
	contentType string
	body        []byte
}

type mediaFixtureHTTPCapture struct {
	requests []mediaFixtureHTTPRequest
}

type mediaFixtureRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn mediaFixtureRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

// Exercise the real adapters and their serialized HTTP bodies, but never open a
// socket. Even a provider URL or redirect accidentally changed by production
// code is handled by this in-memory transport, not by http.DefaultTransport.
func newMediaAdapterHTTPClient(t *testing.T, responses ...mediaFixtureHTTPResponse) (*Client, *mediaFixtureHTTPCapture) {
	t.Helper()
	capture := &mediaFixtureHTTPCapture{}
	transport := mediaFixtureRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != mediaFixtureTestHost {
			t.Fatalf("unexpected upstream host %q; network access is forbidden", request.URL.Host)
		}
		var body []byte
		if request.Body != nil {
			var err error
			body, err = io.ReadAll(request.Body)
			requireNoError(t, err, "read serialized provider request: %v")
			requireNoError(t, request.Body.Close(), "close serialized provider request: %v")
		}
		index := len(capture.requests)
		capture.requests = append(capture.requests, mediaFixtureHTTPRequest{
			method: request.Method, path: request.URL.Path,
			contentType: request.Header.Get("Content-Type"), body: body,
		})
		if index >= len(responses) {
			t.Fatalf("unexpected extra provider request: %s %s", request.Method, request.URL.Path)
		}
		response := httptest.NewRecorder()
		response.Header().Set("Content-Type", responses[index].contentType)
		if responses[index].statusCode != 0 {
			response.WriteHeader(responses[index].statusCode)
		}
		_, err := io.WriteString(response, responses[index].body)
		requireNoError(t, err, "write fake provider response: %v")
		return response.Result(), nil
	})
	client := newTestProviderClient()
	client.httpClients.Store(defaultConnectTimeoutMS, &http.Client{Transport: transport})
	return client, capture
}

func mediaFixtureRoute(protocol, model string) RouteConfig {
	return RouteConfig{
		Protocol: protocol, BaseURL: "https://" + mediaFixtureTestHost,
		UpstreamModel: model, ReadTimeoutMS: 2000, StreamIdleTimeoutMS: 2000,
	}
}

func mediaFixtureInput(images []ContentPart) GenerateInput {
	parts := append([]ContentPart{{Kind: ContentPartText, Text: mediaFixtureReviewed}}, images...)
	return GenerateInput{
		Instructions: "管理说明：不可进入图片或视频正文。",
		Messages: []Message{
			{Role: "system", Content: "技能全文：不可追加到审阅正文。"},
			{Role: mediaFixtureUserRole, Content: "历史 raw 定义：不可作为这次提交的提示词。"},
			{Role: "assistant", Content: "旧候选说明：不可覆盖新稿。"},
			{Role: mediaFixtureUserRole, Parts: parts},
		},
		Options: map[string]interface{}{"n": 1, "prompt": "参数中的 raw 文本不得覆盖正文。"},
	}
}

func mediaFixtureReferences() []ContentPart {
	// Deliberately distinct binary fixtures, including NUL/CR/LF/non-UTF-8
	// bytes, detect re-encoding, text conversion, sorting, and dropped inputs.
	return []ContentPart{
		{Kind: ContentPartImage, MimeType: mediaFixturePNGMIME, FileName: "乙-身份.png", Text: "未审阅的参考描述甲", Data: []byte{0x89, 'P', 'N', 'G', 0, '\r', '\n', 0xff, 1}},
		{Kind: ContentPartImage, MimeType: mediaFixtureJPEGMIME, FileName: "甲-空间.jpg", Text: "未审阅的参考描述乙", Data: []byte{0xff, 0xd8, 0, '\n', '\r', 0x80, 2, 0xff, 0xd9}},
		{Kind: ContentPartImage, MimeType: "image/webp", FileName: "丙-道具.webp", Text: "未审阅的参考描述丙", Data: []byte{'R', 'I', 'F', 'F', 0, 0xff, '\r', '\n', 3}},
	}
}

func mediaFixtureJSON(t *testing.T, value interface{}) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	requireNoError(t, err, "marshal media adapter test value: %v")
	return raw
}

func mediaFixtureImageResponse(t *testing.T, protocol, revised string, stream bool) mediaFixtureHTTPResponse {
	t.Helper()
	var payload interface{}
	if protocol == AdapterGoogleImageGeneration {
		payload = map[string]interface{}{
			"candidates": []interface{}{map[string]interface{}{
				"content": map[string]interface{}{"parts": []interface{}{
					map[string]interface{}{"text": revised},
					map[string]interface{}{"inlineData": map[string]interface{}{"mimeType": mediaFixturePNGMIME, mediaFixtureDataKey: "Zml4dHVyZQ=="}},
				}},
			}},
		}
	} else {
		image := map[string]interface{}{"b64_json": "Zml4dHVyZQ=="}
		if revised != "" {
			image["revised_prompt"] = revised
		}
		payload = map[string]interface{}{mediaFixtureTypeKey: "image_generation.completed", mediaFixtureDataKey: []interface{}{image}}
	}
	response := mediaFixtureHTTPResponse{contentType: mediaFixtureJSONMIME, body: string(mediaFixtureJSON(t, payload))}
	if stream {
		response.contentType = mediaFixtureStreamMIME
		response.body = "data: " + response.body + "\n\n"
	}
	return response
}

func requireMediaAdapterSingleRequest(t *testing.T, capture *mediaFixtureHTTPCapture, path string) mediaFixtureHTTPRequest {
	t.Helper()
	if len(capture.requests) != 1 {
		t.Fatalf("provider requests = %d, want exactly one", len(capture.requests))
	}
	request := capture.requests[0]
	requireEqual(t, request.method, http.MethodPost, "provider method = %q", request.method)
	requireEqual(t, request.path, path, "provider path = %q, want %q", request.path, path)
	return request
}

func requireMediaAdapterInputUnchanged(t *testing.T, before []byte, input GenerateInput) {
	t.Helper()
	if after := mediaFixtureJSON(t, input); !bytes.Equal(before, after) {
		t.Fatalf("provider mutated the original reviewed input\nbefore: %s\nafter: %s", before, after)
	}
}

func requireMediaAdapterRevisedImage(t *testing.T, output *GenerateOutput, revised string) {
	t.Helper()
	if output == nil || len(output.GeneratedImages) != 1 {
		t.Fatalf("expected one fake generated image, got %#v", output)
	}
	requireEqual(t, output.GeneratedImages[0].RevisedPrompt, revised,
		"provider revised prompt = %q, want independent response %q", output.GeneratedImages[0].RevisedPrompt, revised)
}

func requireMediaAdapterDataURL(t *testing.T, actual string, image ContentPart) {
	t.Helper()
	encoded, ok := strings.CutPrefix(actual, "data:"+image.MimeType+";base64,")
	if !ok {
		t.Fatalf("reference has wrong MIME type or data URL encoding: %q", actual)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	requireNoError(t, err, "decode submitted reference bytes: %v")
	if !bytes.Equal(decoded, image.Data) {
		t.Fatalf("reference bytes changed: got %x, want %x", decoded, image.Data)
	}
}
