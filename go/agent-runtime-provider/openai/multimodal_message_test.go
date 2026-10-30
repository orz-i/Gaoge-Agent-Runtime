package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestOpenAIResponsesOrderedImageAndPDFWireAndNoAudioFallback(t *testing.T) {
	calls := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Input []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Input) != 1 || body.Input[0].Role != "user" {
			t.Fatalf("input message lost: %+v", body)
		}
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL string `json:"image_url"`
			FileID   string `json:"file_id"`
			FileURL  string `json:"file_url"`
		}
		if err := json.Unmarshal(body.Input[0].Content, &parts); err != nil || len(parts) != 4 ||
			parts[0].Type != "input_text" || parts[0].Text != "before" ||
			parts[1].Type != "input_image" || parts[1].ImageURL != "https://assets.example.test/photo.png" ||
			parts[2].Type != "input_file" || parts[2].FileID != "file-pdf" ||
			parts[3].Type != "input_text" || parts[3].Text != "after" {
			t.Fatalf("openai request reordered or flattened media: %+v err=%v", parts, err)
		}
		_, _ = fmt.Fprint(w, `{"id":"resp-1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":20,"output_tokens":4}}`)
	})
	request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentPart{
		{Kind: model.ContentText, Text: "before"},
		{Kind: model.ContentImage, MediaType: "image/png", Source: &model.MediaSource{Kind: model.MediaSourceURL, URL: "https://assets.example.test/photo.png"}},
		{Kind: model.ContentFile, MediaType: "application/pdf", Source: &model.MediaSource{Kind: model.MediaSourceFileID, FileID: "file-pdf"}},
		{Kind: model.ContentText, Text: "after"},
	}}}}
	response, err := client.Generate(context.Background(), request)
	if err != nil || response.Content != "ok" || calls != 1 {
		t.Fatalf("openai multimodal content not sent as ordered blocks: response=%+v err=%v calls=%d", response, err, calls)
	}
	// Audio cannot be sent as referenced bytes in this adapter; it must not
	// be flattened into ordinary text or cause any provider HTTP request.
	request.Messages[0].Content = []model.ContentPart{{Kind: model.ContentAudio, MediaType: "audio/mpeg",
		Source: &model.MediaSource{Kind: model.MediaSourceFileID, FileID: "audio-file"}}}
	_, err = client.Generate(context.Background(), request)
	if !errors.Is(err, openai.ErrUnsupported) || calls != 1 {
		t.Fatalf("unsupported audio silently dispatched or normalized: err=%v calls=%d", err, calls)
	}
}

// A streaming Responses request keeps exactly the same ordered user media
// input and requires an authoritative terminal response.
func TestOpenAIResponsesMultimodalStreamingKeepsFrozenInput(t *testing.T) {
	calls := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Stream bool `json:"stream"`
			Input  []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL string `json:"image_url"`
		}
		if !body.Stream || len(body.Input) != 1 || body.Input[0].Role != "user" {
			t.Fatalf("stream lost user message: %+v", body)
		}
		if err := json.Unmarshal(body.Input[0].Content, &parts); err != nil ||
			len(parts) != 2 || parts[0].Type != "input_text" || parts[0].Text != "before" ||
			parts[1].Type != "input_image" || parts[1].ImageURL != "https://assets.example.test/stream.png" {
			t.Fatalf("stream lost media parts: %+v err=%v", parts, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, "event: response.output_text.delta")
		_, _ = fmt.Fprintln(w, `data: {"type":"response.output_text.delta","delta":"ok"}`)
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "event: response.completed")
		_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"r-stream","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":45,"output_tokens":2}}}`)
		_, _ = fmt.Fprintln(w)
	})
	request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentPart{
		{Kind: model.ContentText, Text: "before"},
		{Kind: model.ContentImage, MediaType: "image/png", Source: &model.MediaSource{
			Kind: model.MediaSourceURL, URL: "https://assets.example.test/stream.png",
		}},
	}}}}
	deltas := 0
	answer, err := client.GenerateStream(t.Context(), request, func(event model.StreamEvent) error {
		if event.Delta != "" {
			deltas++
		}
		return nil
	})
	if err != nil || calls != 1 || answer.Content != "ok" || answer.ResponseID != "r-stream" ||
		answer.Usage == nil || answer.Usage.InputTokens != 45 || deltas != 1 {
		t.Fatalf("terminal streaming media receipt invalid: answer=%+v calls=%d deltas=%d err=%v", answer, calls, deltas, err)
	}
}
