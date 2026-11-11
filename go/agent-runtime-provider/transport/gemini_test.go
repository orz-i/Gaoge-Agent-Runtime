package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	valueTEXT0C472DD4        = "TEXT"
	valueAspectRatio75A66651 = "aspectRatio"
	valuePrompt99B52713      = "prompt"
	valueSystem39AB10EB      = "system"
	valueUserA1915E30        = "user"
)

const (
	valueText6FFBCFCF                 = "16:9"
	valueTextA3D7D6C5                 = "1:1"
	valueACleanProductRenderE8F0BD89  = "A clean product render"
	valueIMAGED753175C                = "IMAGE"
	valueReplaceTheBackground73AE4CB5 = "Replace the background"
	valueAnswerEB4FB1BC               = "answer"
	valueCodeExecution92424C75        = "code_execution"
	valueCompleted691D9E2F            = "completed"
	valueGenerationConfigEB41E648     = "generationConfig"
	valueGoogleSearch017D9522         = "google_search"
	valueHttpsExampleComAD1C17325     = "https://example.com/a"
	valueIgnore712EF8CC               = "ignore"
	valueImagePngF2B26E33             = "image/png"
	valueImageWebpAE2BECF4            = "image/webp"
	valueImageConfig993B709C          = "imageConfig"
	valueImageSizeF124DD0B            = "imageSize"
	valueInProgress7CE5C559           = "in_progress"
	valueOverrideCC8DD488             = "override"
	valueResponseModalities0DB4DA49   = "responseModalities"
	valueStream14C7163C               = "stream"
	valueThoughtSignature1D27B6582    = "thought-signature-1"
	valueTools52CD1EAE                = "tools"
	valueUrlContext4494EA50           = "url_context"
)

func TestConsumeGeminiStreamSemanticIdleIgnoresKeepalive(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()

	result := &GenerateOutput{}
	done := make(chan error, 1)
	go func() {
		done <- consumeGeminiStream(reader, result, nil, 80*time.Millisecond)
	}()

	if _, err := io.WriteString(writer, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"); err != nil {
		t.Fatalf("write Gemini data frame: %v", err)
	}

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(500 * time.Millisecond)
	defer timeout.Stop()
	for {
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), "stream idle timeout") {
				t.Fatalf("semantic idle error = %v", err)
			}
			if result.Text != "partial" {
				t.Fatalf("partial result = %q", result.Text)
			}
			return
		case <-ticker.C:
			if _, err := io.WriteString(writer, ": keep-alive\n\n"); err != nil {
				t.Fatalf("write Gemini keepalive: %v", err)
			}
		case <-timeout.C:
			t.Fatal("Gemini keepalive prevented semantic idle timeout")
		}
	}
}

func TestConsumeGeminiStreamStopsOnFinishReasonWithoutWaitingForEOF(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()

	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	done := make(chan error, 1)
	go func() {
		done <- consumeGeminiStream(reader, result, nil, time.Second)
	}()

	_, err := io.WriteString(writer, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"done\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":7,\"thoughtsTokenCount\":3}}\n")
	if err != nil {
		t.Fatalf("write Gemini terminal chunk: %v", err)
	}

	select {
	case consumeErr := <-done:
		if consumeErr != nil {
			t.Fatalf("consume Gemini terminal chunk: %v", consumeErr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Gemini stream waited for EOF after finishReason")
	}

	if result.Text != "done" || result.Usage.InputTokens != 11 || result.Usage.OutputTokens != 7 || result.Usage.ReasoningTokens != 3 {
		t.Fatalf("terminal result = %#v", result)
	}
}

func TestConsumeGeminiStreamStopsOnDoneSentinelWithoutWaitingForEOF(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()

	done := make(chan error, 1)
	go func() {
		done <- consumeGeminiStream(reader, &GenerateOutput{}, nil, time.Second)
	}()

	if _, err := io.WriteString(writer, "data: [DONE]\n"); err != nil {
		t.Fatalf("write Gemini done sentinel: %v", err)
	}

	select {
	case consumeErr := <-done:
		if consumeErr != nil {
			t.Fatalf("consume Gemini done sentinel: %v", consumeErr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Gemini stream waited for EOF after [DONE]")
	}
}

func TestApplyGeminiStreamChunkStoresReasoningAndCitations(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	var reasoningText string
	err := applyGeminiStreamChunk(mustDecodeObject(t, `{
		"responseId": "gemini-stream-1",
		"candidates": [
			{
				"content": {
					"parts": [
						{"text": "think", "thought": true, "thoughtSignature": "sig-stream"},
						{"text": "answer"}
					]
				},
				"groundingMetadata": {
					"groundingChunks": [
						{"web": {"uri": "https://example.com/source"}}
					]
				}
			}
		]
	}`), result, func(event GenerateStreamEvent) error {
		if event.Reasoning != nil {
			reasoningText += event.Reasoning.Text
		}
		return nil
	})
	requireNoError(t, err, "apply gemini stream chunk: %v")
	requireCondition(t, result.ResponseID == "gemini-stream-1" && result.Text == valueAnswerEB4FB1BC, "expected response id and answer text, got %#v", result)
	requireCondition(t, reasoningText == "think" && result.Reasoning != nil && result.Reasoning.Text == "think" && result.Reasoning.Signature == "sig-stream", "expected stored and emitted reasoning, got text=%q result=%#v", reasoningText, result.Reasoning)
	requireCondition(t, len(result.Citations) == 1 && result.Citations[0] == "https://example.com/source", "expected stream citations, got %#v", result.Citations)
}

func TestBuildGeminiImageGenerationRequestBody(t *testing.T) {
	payload, err := buildGeminiImageGenerationRequestBody("gemini-3.1-flash-image", GenerateInput{
		Messages: []Message{
			{Role: valueSystem39AB10EB, Content: valueIgnore712EF8CC},
			{Role: valueUserA1915E30, Content: valueACleanProductRenderE8F0BD89},
		},
		Options: map[string]interface{}{
			valueGenerationConfigEB41E648: map[string]interface{}{
				valueImageConfig993B709C: map[string]interface{}{
					valueAspectRatio75A66651: valueText6FFBCFCF,
					valueImageSizeF124DD0B:   "2K",
				},
			},
			valuePrompt99B52713: valueOverrideCC8DD488,
			valueStream14C7163C: true,
		},
	})
	requireNoError(t, err, "build gemini image request body: %v")
	contents, ok := payload["contents"].([]map[string]interface{})
	requireCondition(t, ok && len(contents) > 0, "expected contents, got %#v", payload)
	parts, ok := contents[0]["parts"].([]map[string]interface{})
	requireCondition(t, ok && len(parts) > 0, "expected parts, got %#v", contents[0])
	requireEqual(t, parts[0]["text"], valueACleanProductRenderE8F0BD89, "expected last user prompt, got %#v", payload)
	config, ok := payload[valueGenerationConfigEB41E648].(map[string]interface{})
	requireCondition(t, ok, "expected generationConfig, got %#v", payload)
	modalities, ok := config[valueResponseModalities0DB4DA49].([]string)
	requireCondition(t, ok, "expected response modalities, got %#v", config)
	requireCondition(t, len(modalities) == 2 && modalities[0] == valueTEXT0C472DD4 && modalities[1] == valueIMAGED753175C, "expected default image response modalities, got %#v", config[valueResponseModalities0DB4DA49])
	imageConfig := asMap(config[valueImageConfig993B709C])
	requireCondition(t, imageConfig[valueAspectRatio75A66651] == valueText6FFBCFCF && imageConfig[valueImageSizeF124DD0B] == "2K", "expected image config, got %#v", config)
	if _, ok := config["responseFormat"]; ok {
		t.Fatalf("Gemini image requests must use generationConfig.imageConfig, got %#v", config["responseFormat"])
	}
	if _, ok := payload[valueStream14C7163C]; ok {
		t.Fatalf("stream must not be passed to Gemini image generation: %#v", payload)
	}
}

func TestBuildGeminiImageGenerationRequestBodySupportsResponseModalitiesAndTools(t *testing.T) {
	payload, err := buildGeminiImageGenerationRequestBody("gemini-3-pro-image", GenerateInput{
		Messages: []Message{{Role: valueUserA1915E30, Content: valueACleanProductRenderE8F0BD89}},
		Options: map[string]interface{}{
			valueGenerationConfigEB41E648: map[string]interface{}{
				valueResponseModalities0DB4DA49: valueIMAGED753175C,
			},
			valueTools52CD1EAE: []interface{}{
				map[string]interface{}{valueGoogleSearch017D9522: map[string]interface{}{}},
			},
		},
	})
	if err != nil {
		t.Fatalf("build gemini image request body: %v", err)
	}
	config, ok := payload[valueGenerationConfigEB41E648].(map[string]interface{})
	if !ok {
		t.Fatalf("expected generationConfig, got %#v", payload)
	}
	modalities, ok := config[valueResponseModalities0DB4DA49].([]string)
	if !ok {
		t.Fatalf("expected responseModalities, got %#v", config)
	}
	if len(modalities) != 1 || modalities[0] != valueIMAGED753175C {
		t.Fatalf("expected configured image-only modality, got %#v", modalities)
	}
	tools, ok := payload[valueTools52CD1EAE].([]map[string]interface{})
	if !ok || len(tools) != 1 || len(asMap(tools[0][valueGoogleSearch017D9522])) != 0 {
		t.Fatalf("expected google_search tool, got %#v", tools)
	}
}

func TestBuildGeminiImageGenerationRequestBodyPreservesGoogleImageSearchOptions(t *testing.T) {
	payload, err := buildGeminiImageGenerationRequestBody("gemini-3-pro-image", GenerateInput{
		Messages: []Message{{Role: valueUserA1915E30, Content: "Find current product imagery"}},
		Options: map[string]interface{}{
			valueTools52CD1EAE: []interface{}{
				map[string]interface{}{
					valueGoogleSearch017D9522: map[string]interface{}{
						"searchTypes": map[string]interface{}{
							"webSearch":   map[string]interface{}{},
							"imageSearch": map[string]interface{}{},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build gemini image request body: %v", err)
	}
	tools, ok := payload[valueTools52CD1EAE].([]map[string]interface{})
	if !ok || len(tools) != 1 {
		t.Fatalf("expected google_search tool, got %#v", payload)
	}
	googleSearch := asMap(tools[0][valueGoogleSearch017D9522])
	searchTypes := asMap(googleSearch["searchTypes"])
	if _, ok := searchTypes["webSearch"]; !ok {
		t.Fatalf("expected webSearch to pass, got %#v", tools)
	}
	if _, ok := searchTypes["imageSearch"]; !ok {
		t.Fatalf("expected imageSearch to pass, got %#v", tools)
	}
}

func TestApplyGeminiStreamChunkEmitsServerToolTrace(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	events := make([]ToolCall, 0)
	err := applyGeminiStreamChunk(mustDecodeObject(t, `{
		"responseId": "gemini-stream-tool-1",
		"candidates": [{
			"content": {
				"parts": [
					{"executableCode": {"language": "PYTHON", "code": "print(2)"}},
					{"codeExecutionResult": {"outcome": "OUTCOME_OK", "output": "2\n"}}
				]
			}
		}]
	}`), result, func(event GenerateStreamEvent) error {
		if event.ServerToolCall != nil {
			events = append(events, *event.ServerToolCall)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("apply gemini stream chunk: %v", err)
	}
	if len(result.ServerToolCalls) != 1 || result.ServerToolCalls[0].ToolName != valueCodeExecution92424C75 {
		t.Fatalf("expected stored code_execution trace, got %#v", result.ServerToolCalls)
	}
	if len(events) != 1 || events[0].ToolName != valueCodeExecution92424C75 || events[0].OutputJSON == "" {
		t.Fatalf("expected emitted code_execution trace, got %#v", events)
	}
}

func TestApplyGeminiStreamChunkEmitsExplicitServerToolInvocation(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	events := make([]ToolCall, 0)
	err := applyGeminiStreamChunk(mustDecodeObject(t, `{
		"responseId": "gemini-stream-tool-2",
		"candidates": [{
			"serverSideToolInvocations": [{
				"id": "gsi_1",
				"name": "google_search",
				"status": "running",
				"input": {"query": "SpaceX stock price"}
			}]
		}]
	}`), result, func(event GenerateStreamEvent) error {
		if event.ServerToolCall != nil {
			events = append(events, *event.ServerToolCall)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("apply gemini stream chunk: %v", err)
	}
	if len(result.ServerToolCalls) != 1 || result.ServerToolCalls[0].Status != valueInProgress7CE5C559 {
		t.Fatalf("expected stored in-progress server tool call, got %#v", result.ServerToolCalls)
	}
	if len(events) != 1 || events[0].ToolName != valueGoogleSearch017D9522 || events[0].Status != valueInProgress7CE5C559 {
		t.Fatalf("expected emitted google_search invocation, got %#v", events)
	}
}

func TestApplyGeminiStreamChunkKeepsMultipleCodeExecutionTraces(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"executableCode":{"language":"PYTHON","code":"print(1)"}}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1\n"}}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"executableCode":{"language":"PYTHON","code":"print(2)"}}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"2\n"}}]}}]}`,
	}
	for _, chunk := range chunks {
		if err := applyGeminiStreamChunk(mustDecodeObject(t, chunk), result, nil); err != nil {
			t.Fatalf("apply gemini stream chunk: %v", err)
		}
	}
	if len(result.ServerToolCalls) != 2 {
		t.Fatalf("expected two code_execution traces, got %#v", result.ServerToolCalls)
	}
	if result.ServerToolCalls[0].ToolCallID == result.ServerToolCalls[1].ToolCallID {
		t.Fatalf("expected distinct code_execution trace ids, got %#v", result.ServerToolCalls)
	}
	if result.ServerToolCalls[0].OutputJSON == "" || result.ServerToolCalls[1].OutputJSON == "" {
		t.Fatalf("expected both code_execution outputs, got %#v", result.ServerToolCalls)
	}
	if result.ServerSideToolUsage[valueCodeExecution92424C75] != 2 {
		t.Fatalf("expected cumulative code_execution usage, got %#v", result.ServerSideToolUsage)
	}
}

func TestBuildGeminiImageGenerationRequestBodyDropsUnsupportedImageSize(t *testing.T) {
	payload, err := buildGeminiImageGenerationRequestBody("gemini-2.5-flash-image", GenerateInput{
		Messages: []Message{{Role: valueUserA1915E30, Content: valueACleanProductRenderE8F0BD89}},
		Options: map[string]interface{}{
			valueAspectRatio75A66651: valueTextA3D7D6C5,
			valueImageSizeF124DD0B:   "1K",
			valueGenerationConfigEB41E648: map[string]interface{}{
				valueImageConfig993B709C: map[string]interface{}{
					valueAspectRatio75A66651: valueTextA3D7D6C5,
					valueImageSizeF124DD0B:   "1K",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build gemini image request body: %v", err)
	}
	generationConfig, ok := payload[valueGenerationConfigEB41E648].(map[string]interface{})
	if !ok {
		t.Fatalf("expected generationConfig map, got %#v", payload[valueGenerationConfigEB41E648])
	}
	imageConfig := asMap(generationConfig[valueImageConfig993B709C])
	if imageConfig[valueAspectRatio75A66651] != valueTextA3D7D6C5 {
		t.Fatalf("expected supported aspect ratio, got %#v", imageConfig)
	}
	if _, ok := imageConfig[valueImageSizeF124DD0B]; ok {
		t.Fatalf("expected imageSize to be omitted for Gemini 2.5 image model, got %#v", imageConfig)
	}
}

func TestBuildGeminiImageGenerationRequestBodyIncludesInlineImages(t *testing.T) {
	payload, err := buildGeminiImageGenerationRequestBody("gemini-3.1-flash-image", GenerateInput{
		Messages: []Message{
			{
				Role: valueUserA1915E30,
				Parts: []ContentPart{
					{Kind: ContentPartText, Text: valueReplaceTheBackground73AE4CB5},
					{Kind: ContentPartImage, MimeType: valueImagePngF2B26E33, Data: []byte("source")},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build gemini image edit request body: %v", err)
	}

	contents, ok := payload["contents"].([]map[string]interface{})
	if !ok || len(contents) == 0 {
		t.Fatalf("expected contents, got %#v", payload)
	}
	parts, ok := contents[0]["parts"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected parts, got %#v", contents[0])
	}
	if len(parts) != 2 {
		t.Fatalf("expected text and image parts, got %#v", parts)
	}
	if parts[0]["text"] != valueReplaceTheBackground73AE4CB5 {
		t.Fatalf("expected prompt text part, got %#v", parts[0])
	}
	inlineData := asMap(parts[1]["inline_data"])
	if inlineData["mime_type"] != valueImagePngF2B26E33 || inlineData["data"] != "c291cmNl" {
		t.Fatalf("expected inline image data, got %#v", inlineData)
	}
}

func TestParseGeminiImageGenerationOutput(t *testing.T) {
	output, err := parseGeminiImageGenerationOutput([]byte(`{
		"responseId": "gemini-image-1",
		"candidates": [
			{
				"content": {
					"parts": [
						{"text": "A revised prompt"},
						{"thought": true, "inlineData": {"mimeType": "image/png", "data": "thought-image"}},
						{"inlineData": {"mimeType": "image/png", "data": "iVBORw0KGgo="}}
					]
				}
			},
			{
				"content": {
					"parts": [
						{"inline_data": {"mime_type": "image/webp", "data": "UklGRg=="}}
					]
				}
			}
		],
		"usageMetadata": {
			"promptTokenCount": 15,
			"candidatesTokenCount": 7,
			"cachedContentTokenCount": 3
		}
	}`))
	requireNoError(t, err, "parse gemini image output: %v")
	requireEqual(t, output.ResponseID, "gemini-image-1", "expected response id, got %q", output.ResponseID)
	requireCondition(t, len(output.GeneratedImages) == 2, "expected generated images, got %#v", output.GeneratedImages)
	requireCondition(t, output.GeneratedImages[0].B64JSON == "iVBORw0KGgo=" && output.GeneratedImages[0].MIMEType == valueImagePngF2B26E33, "unexpected first image: %#v", output.GeneratedImages[0])
	requireCondition(t, output.GeneratedImages[1].B64JSON == "UklGRg==" && output.GeneratedImages[1].MIMEType == valueImageWebpAE2BECF4, "unexpected second image: %#v", output.GeneratedImages[1])
	requireEqual(t, output.GeneratedImages[0].RevisedPrompt, "A revised prompt", "expected revised prompt, got %#v", output.GeneratedImages[0])
	requireCondition(t, output.Usage.InputTokens == 12 && output.Usage.OutputTokens == 7 && output.Usage.CacheReadTokens == 3, "expected parsed usage, got %#v", output.Usage)
}

func TestGeminiImageGenerationStream(t *testing.T) {
	server := newGeminiImageGenerationStreamServer(t)
	defer server.Close()

	var usageEvents []Usage
	output, err := newTestProviderClient().GenerateStream(context.Background(), RouteConfig{
		Protocol:      AdapterGoogleImageGeneration,
		BaseURL:       server.URL,
		APIKey:        "gemini-key",
		UpstreamModel: "nano-banana-pro",
	}, GenerateInput{
		Messages: []Message{{Role: valueUserA1915E30, Content: valueACleanProductRenderE8F0BD89}},
	}, func(event GenerateStreamEvent) error {
		if event.Usage != (Usage{}) {
			usageEvents = append(usageEvents, event.Usage)
		}
		return nil
	})
	requireNoError(t, err, "gemini image stream: %v")
	assertGeminiImageGenerationStreamOutput(t, output, usageEvents)
}

func newGeminiImageGenerationStreamServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertGeminiImageGenerationStreamRequest(t, r)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"responseId\":\"gemini-image-stream-1\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"A revised prompt\"}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"iVBORw0KGgo=\"}}]}}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":3}}\n\n"))
	}))
}

func assertGeminiImageGenerationStreamRequest(t *testing.T, r *http.Request) {
	t.Helper()
	requireEqual(t, r.URL.Path, "/v1beta/models/nano-banana-pro:streamGenerateContent", "unexpected path %s", r.URL.Path)
	requireEqual(t, r.Header.Get("x-goog-api-key"), "gemini-key", "expected gemini API key header, got %q", r.Header.Get("x-goog-api-key"))
	var requestBody map[string]interface{}
	requireNoError(t, json.NewDecoder(r.Body).Decode(&requestBody), "decode request: %v")
	generationConfig := asMap(requestBody[valueGenerationConfigEB41E648])
	modalities, ok := generationConfig[valueResponseModalities0DB4DA49].([]interface{})
	requireCondition(t, ok, "expected response modalities, got %#v", generationConfig)
	requireCondition(t, len(modalities) == 2 && modalities[0] == valueTEXT0C472DD4 && modalities[1] == valueIMAGED753175C, "expected text and image modalities, got %#v", modalities)
}

func assertGeminiImageGenerationStreamOutput(t *testing.T, output *GenerateOutput, usageEvents []Usage) {
	t.Helper()
	requireEqual(t, output.ResponseID, "gemini-image-stream-1", "expected response id, got %q", output.ResponseID)
	requireEqual(t, output.Text, "A revised prompt", "expected revised prompt text, got %q", output.Text)
	requireCondition(t, len(output.GeneratedImages) == 1 && output.GeneratedImages[0].B64JSON == "iVBORw0KGgo=", "expected streamed image, got %#v", output.GeneratedImages)
	requireCondition(t, output.GeneratedImages[0].MIMEType == valueImagePngF2B26E33 && output.GeneratedImages[0].RevisedPrompt == "A revised prompt", "unexpected streamed image metadata: %#v", output.GeneratedImages[0])
	requireCondition(t, len(usageEvents) == 1 && usageEvents[0].InputTokens == 10 && usageEvents[0].OutputTokens == 3, "expected usage event, got %#v", usageEvents)
}

func TestParseGeminiErrorProvidesUnauthorizedFallback(t *testing.T) {
	err := parseGeminiError(http.StatusUnauthorized, nil, nil)
	var upstreamErr *UpstreamError
	if !errors.As(err, &upstreamErr) {
		t.Fatalf("expected upstream error, got %v", err)
	}
	if upstreamErr.Message != "google authentication failed; check API key, upstream base URL, and custom auth headers" {
		t.Fatalf("unexpected unauthorized message: %q", upstreamErr.Message)
	}
}
