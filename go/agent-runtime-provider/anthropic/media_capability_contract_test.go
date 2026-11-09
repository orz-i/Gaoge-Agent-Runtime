package anthropic_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestAnthropicMediaModelCapabilitiesPreflightUnaryAndSSE(t *testing.T) {
	var physical atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical.Add(1)
		if r.Header.Get("Accept") == "text/event-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, event := range []struct{ name, value string }{
				{"message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"msg-media","usage":{"input_tokens":12}}}`},
				{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
				{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
				{"content_block_stop", `{"type":"content_block_stop","index":0}`},
				{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`},
				{"message_stop", `{"type":"message_stop"}`},
			} {
				_, _ = fmt.Fprintln(w, "event: "+event.name)
				_, _ = fmt.Fprintln(w, "data: "+event.value)
				_, _ = fmt.Fprintln(w)
			}
			return
		}
		_, _ = fmt.Fprint(w, `{"type":"message","role":"assistant","id":"msg-media","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":12,"output_tokens":2}}`)
	}))
	defer server.Close()
	profiles := model.StaticModelProfiles{
		"image-only": {SupportsImageInput: true},
		"pdf-only":   {SupportsPDFInput: true},
		"both":       {SupportsImageInput: true, SupportsPDFInput: true},
		"none":       {},
	}
	client, err := anthropic.New(anthropic.Config{APIKey: "fixture-secret", Endpoint: server.URL + "/v1/messages", ModelProfiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	image := model.ContentPart{Kind: model.ContentImage, MediaType: "image/jpeg", Source: &model.MediaSource{Kind: model.MediaSourceURL, URL: "https://assets.example.test/image.jpg"}}
	pdf := model.ContentPart{Kind: model.ContentFile, MediaType: "application/pdf", Source: &model.MediaSource{Kind: model.MediaSourceFileID, FileID: "file-1"}}
	for _, tc := range []struct {
		name, model string
		parts       []model.ContentPart
		allow       bool
	}{
		{"text-uncertified", "unknown", model.TextParts("hello"), true},
		{"image-uncertified", "unknown", []model.ContentPart{image}, false},
		{"pdf-uncertified", "none", []model.ContentPart{pdf}, false},
		{"image-authorized", "image-only", []model.ContentPart{image}, true},
		{"pdf-authorized", "pdf-only", []model.ContentPart{pdf}, true},
		{"both-authorized", "both", []model.ContentPart{image, pdf}, true},
		{"pdf-not-authorized", "image-only", []model.ContentPart{image, pdf}, false},
		{"image-not-authorized", "pdf-only", []model.ContentPart{image, pdf}, false},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, streaming), func(t *testing.T) {
				request := model.Request{Model: tc.model, MaxOutputTokens: 128, Messages: []model.Message{{Role: model.RoleUser, Content: tc.parts}}}
				before := physical.Load()
				var result model.Response
				var err error
				if streaming {
					result, err = client.GenerateStream(context.Background(), request, func(model.StreamEvent) error { return nil })
				} else {
					result, err = client.Generate(context.Background(), request)
				}
				if tc.allow {
					if err != nil || result.ResponseID != "msg-media" || physical.Load() != before+1 {
						t.Fatalf("authorized input did not use native provider: %+v, %v, HTTP delta %d", result, err, physical.Load()-before)
					}
				} else if !errors.Is(err, model.ErrCapabilityUnavailable) || physical.Load() != before {
					t.Fatalf("uncertified image/pdf crossed HTTP boundary: err=%v physical delta=%d", err, physical.Load()-before)
				}
			})
		}
	}
}
