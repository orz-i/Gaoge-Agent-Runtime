package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestAnthropicMessagesOrderedImageAndPDFSourcesAndNoAudioFallback(t *testing.T) {
	calls := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type   string `json:"type"`
					Text   string `json:"text"`
					Source struct {
						Type   string `json:"type"`
						URL    string `json:"url"`
						FileID string `json:"file_id"`
					} `json:"source"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 1 || body.Messages[0].Role != "user" {
			t.Fatalf("messages not mapped: %+v", body)
		}
		p := body.Messages[0].Content
		if len(p) != 4 || p[0].Type != "text" || p[0].Text != "before" ||
			p[1].Type != "image" || p[1].Source.Type != "url" || p[1].Source.URL != "https://assets.example.test/photo.jpg" ||
			p[2].Type != "document" || p[2].Source.Type != "file" || p[2].Source.FileID != "file-doc" ||
			p[3].Type != "text" || p[3].Text != "after" {
			t.Fatalf("anthropic media order or source changed: %+v", p)
		}
		_, _ = fmt.Fprint(w, `{"id":"msg-1","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":20,"output_tokens":4}}`)
	})
	request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentPart{
		{Kind: model.ContentText, Text: "before"},
		{Kind: model.ContentImage, MediaType: "image/jpeg", Source: &model.MediaSource{Kind: model.MediaSourceURL, URL: "https://assets.example.test/photo.jpg"}},
		{Kind: model.ContentFile, MediaType: "application/pdf", Source: &model.MediaSource{Kind: model.MediaSourceFileID, FileID: "file-doc"}},
		{Kind: model.ContentText, Text: "after"},
	}}}}
	response, err := client.Generate(context.Background(), request)
	if err != nil || response.Content != "ok" || calls != 1 {
		t.Fatalf("anthropic multimodal content failed: response=%+v err=%v calls=%d", response, err, calls)
	}
	request.Messages[0].Content = []model.ContentPart{{Kind: model.ContentAudio, MediaType: "audio/wav",
		Source: &model.MediaSource{Kind: model.MediaSourceURL, URL: "https://assets.example.test/audio.wav"}}}
	_, err = client.Generate(context.Background(), request)
	if !errors.Is(err, anthropic.ErrUnsupported) || calls != 1 {
		t.Fatalf("unsupported audio crossed provider boundary: err=%v calls=%d", err, calls)
	}
}

// A streaming Messages request must keep the FileID document part and still
// require the original terminal SSE completion.
func TestAnthropicMessagesMultimodalStreamingKeepsFrozenInput(t *testing.T) {
	calls := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Content []struct {
					Type   string `json:"type"`
					Source struct {
						Type   string `json:"type"`
						FileID string `json:"file_id"`
					} `json:"source"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !body.Stream || len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 ||
			body.Messages[0].Content[0].Type != "text" ||
			body.Messages[0].Content[1].Type != "document" ||
			body.Messages[0].Content[1].Source.Type != "file" ||
			body.Messages[0].Content[1].Source.FileID != "file-stream" {
			t.Fatalf("stream lost PDF media: %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []struct{ kind, body string }{
			{"message_start", `{"type":"message_start","message":{"id":"msg-stream","usage":{"input_tokens":40}}}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`},
			{"message_stop", `{"type":"message_stop"}`},
		} {
			_, _ = fmt.Fprintln(w, "event: "+event.kind)
			_, _ = fmt.Fprintln(w, "data: "+event.body)
			_, _ = fmt.Fprintln(w)
		}
	})
	request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentPart{
		{Kind: model.ContentText, Text: "before"},
		{Kind: model.ContentFile, MediaType: "application/pdf", Source: &model.MediaSource{
			Kind: model.MediaSourceFileID, FileID: "file-stream",
		}},
	}}}}
	deltas := 0
	answer, err := client.GenerateStream(t.Context(), request, func(event model.StreamEvent) error {
		if event.Delta != "" {
			deltas++
		}
		return nil
	})
	if err != nil || calls != 1 || answer.Content != "ok" || answer.ResponseID != "msg-stream" ||
		answer.Usage == nil || answer.Usage.InputTokens != 40 || deltas != 1 {
		t.Fatalf("terminal streaming PDF receipt invalid: answer=%+v calls=%d deltas=%d err=%v", answer, calls, deltas, err)
	}
}
