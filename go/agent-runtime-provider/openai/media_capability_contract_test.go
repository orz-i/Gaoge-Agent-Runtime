package openai_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestOpenAIMediaModelCapabilitiesPreflightUnarySSEAndCount(t *testing.T) {
	var physical atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical.Add(1)
		if r.URL.Path == "/v1/responses/input_tokens" {
			_, _ = fmt.Fprint(w, `{"object":"response.input_tokens","input_tokens":12}`)
			return
		}
		if r.Header.Get("Accept") == "text/event-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintln(w, "event: response.completed")
			_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"object":"response","id":"r-media","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":12,"output_tokens":2}}}`)
			_, _ = fmt.Fprintln(w)
			return
		}
		_, _ = fmt.Fprint(w, `{"object":"response","id":"r-media","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":12,"output_tokens":2}}`)
	}))
	defer server.Close()
	profiles := model.StaticModelProfiles{
		"image-only": {SupportsImageInput: true},
		"pdf-only":   {SupportsPDFInput: true},
		"both":       {SupportsImageInput: true, SupportsPDFInput: true},
		"none":       {},
	}
	client, err := openai.New(openai.Config{APIKey: "fixture-secret", Endpoint: server.URL + "/v1/responses", ModelProfiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	image := model.ContentPart{Kind: model.ContentImage, MediaType: "image/png", Source: &model.MediaSource{Kind: model.MediaSourceURL, URL: "https://assets.example.test/image.png"}}
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
				request := model.Request{Model: tc.model, Messages: []model.Message{{Role: model.RoleUser, Content: tc.parts}}}
				before := physical.Load()
				var result model.Response
				var err error
				if streaming {
					result, err = client.GenerateStream(context.Background(), request, func(model.StreamEvent) error { return nil })
				} else {
					result, err = client.Generate(context.Background(), request)
				}
				if tc.allow {
					if err != nil || result.ResponseID != "r-media" || physical.Load() != before+1 {
						t.Fatalf("authorized input did not use native provider: %+v, %v, HTTP delta %d", result, err, physical.Load()-before)
					}
				} else if !errors.Is(err, model.ErrCapabilityUnavailable) || physical.Load() != before {
					t.Fatalf("uncertified image/pdf crossed HTTP boundary: err=%v physical delta=%d", err, physical.Load()-before)
				}
				if tc.allow && tc.name != "text-uncertified" {
					before = physical.Load()
					count, countErr := client.CountInputTokens(context.Background(), request)
					if countErr != nil || count != 12 || physical.Load() != before+1 {
						t.Fatalf("certified media did not use native count: count=%d err=%v physical delta=%d", count, countErr, physical.Load()-before)
					}
				}
				if !tc.allow {
					before = physical.Load()
					_, err = client.CountInputTokens(context.Background(), request)
					if !errors.Is(err, model.ErrCapabilityUnavailable) || physical.Load() != before {
						t.Fatalf("native count bypassed missing media grant: err=%v physical delta=%d", err, physical.Load()-before)
					}
				}
			})
		}
	}
}
