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
